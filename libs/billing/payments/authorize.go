package payments

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CodeStripeRequired refuses a pre-authorization for a customer whose
// payment connection is not Stripe's (lago "stripe_required",
// app/controllers/api/v1/subscriptions_controller.rb:35-45).
const CodeStripeRequired = "stripe_required"

// CodeCustomerHasNoPaymentMethod refuses a pre-authorization when the
// customer has no payment method to hold the amount on (lago
// authorize_service.rb:23-28).
const CodeCustomerHasNoPaymentMethod = "customer_has_no_payment_method"

// AuthorizeInput describes the pre-authorization of a subscription's
// first charge (lago SubscriptionsController#create `authorization`).
type AuthorizeInput struct {
	// Customer is the subscription's customer; one that does not exist yet
	// has no payment connection.
	Customer CustomerRef
	// PaymentMethodID is the method to hold the amount on; zero uses the
	// customer's default, else the provider's.
	PaymentMethodID core.PaymentMethodID
	// Amount is the amount to hold, in minor units of Currency.
	Amount int64
	// Currency is the currency of Amount.
	Currency money.Currency
	// Plan is the subscription's plan, whose code is attached to the
	// authorization.
	Plan refs.Ref
	// RequestID identifies the request: authorizations of one request are
	// one at the provider.
	RequestID string
}

// Authorize places a hold of in.Amount on the customer's Stripe payment
// method before a subscription is created, as lago
// PaymentProviders::Stripe::Payments::AuthorizeService does. The hold
// only checks that the method can pay: its release is enqueued at once
// ([kernel.JobCancelAuthorization], authorize_service.rb:37-42) and no
// payment is recorded. It refuses with [CodeStripeRequired] a customer
// without a Stripe connection, with [CodeCustomerHasNoPaymentMethod] one
// without a method, and returns the provider's refusal as a
// *payment.ProviderError.
func (s *Service) Authorize(ctx context.Context, org core.OrganizationID, in AuthorizeInput) (payment.PaymentResult, error) {
	type target struct {
		pc     providerConn
		params payment.AuthorizeParams
	}
	t, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*target, error) {
		in.Customer.ExternalID = strings.TrimSpace(in.Customer.ExternalID)
		c, err := in.Customer.find(ctx, r, org)
		if errors.Is(err, core.ErrNotFound) {
			return nil, core.NewRuleError(CodeStripeRequired, "only Stripe supports authorization")
		}
		if err != nil {
			return nil, err
		}
		pcust, integ, err := customerConnection(ctx, r, org, c.ID)
		if err != nil {
			return nil, err
		}
		if integ == nil || integ.Provider != integration.ProviderStripe {
			return nil, core.NewRuleError(CodeStripeRequired, "only Stripe supports authorization")
		}
		method, err := defaultMethod(ctx, r, org, c.ID)
		if !in.PaymentMethodID.IsZero() {
			method, err = liveCustomerMethod(ctx, r, org, c.ID, in.PaymentMethodID)
			if err == nil && method == nil {
				return nil, notFound("payment_method")
			}
		}
		if err != nil {
			return nil, err
		}
		code, err := planCode(ctx, r, org, in.Plan)
		if err != nil {
			return nil, err
		}
		pc, err := s.open(ctx, integ)
		if err != nil {
			return nil, err
		}
		p := payment.AuthorizeParams{
			ProviderCustomerID: providerCustomerID(pcust),
			Amount:             in.Amount,
			Currency:           in.Currency,
			IdempotencyKey:     "auth-" + pcust.ID.String() + "-" + in.RequestID,
			Metadata:           map[string]string{"plan_code": code},
		}
		if method != nil {
			p.ProviderMethodID = method.ProviderMethodID
		}
		return &target{pc: pc, params: p}, nil
	})
	if err != nil {
		return payment.PaymentResult{}, err
	}
	res, err := t.pc.gw.Authorize(ctx, t.pc.conn, t.params)
	if errors.Is(err, payment.ErrNoPaymentMethod) {
		return payment.PaymentResult{}, fieldError("payment_method_id", CodeCustomerHasNoPaymentMethod)
	}
	if err != nil {
		return payment.PaymentResult{}, wrap(err, "authorize")
	}
	if res.ProviderPaymentID != "" {
		args := kernel.AuthorizationArgs{IntegrationID: t.pc.integ.ID, ProviderPaymentID: res.ProviderPaymentID}
		if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			return kernel.EnqueueCancelAuthorization(ctx, r, org, args)
		}); err != nil {
			return payment.PaymentResult{}, err
		}
	}
	return res, nil
}

// planCode returns the code of the plan ref: its code, or the code of the
// plan of its ID; "" for a plan that does not exist (lago attaches the
// plan_code parameter as sent).
func planCode(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (string, error) {
	if ref.Code != "" || ref.ID == uuid.Nil {
		return ref.Code, nil
	}
	id, err := core.ParseID[core.PlanID](ref.ID.String())
	if err != nil {
		return "", nil
	}
	p, err := r.Plans.Get(ctx, org, id)
	if isNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", wrap(err, "load plan %s", ref.ID)
	}
	return p.Code, nil
}

// CancelAuthorization releases the pre-authorization args at its
// provider (lago PaymentProviders::CancelPaymentAuthorizationJob). A hold
// whose connection is gone is left to expire at the provider.
func (s *Service) CancelAuthorization(ctx context.Context, org core.OrganizationID, args kernel.AuthorizationArgs) error {
	integ, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*integration.Integration, error) {
		return liveIntegration(ctx, r, org, args.IntegrationID)
	})
	if err != nil || integ == nil {
		return err
	}
	pc, err := s.open(ctx, integ)
	if err != nil {
		return err
	}
	if _, err := pc.gw.CancelPayment(ctx, pc.conn, args.ProviderPaymentID); err != nil {
		return wrap(err, "cancel authorization %s", args.ProviderPaymentID)
	}
	return nil
}
