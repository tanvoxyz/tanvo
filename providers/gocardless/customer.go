package gocardless

import (
	"context"
	"fmt"
	"strings"
	"time"

	gc "github.com/gocardless/gocardless-pro-go/v6"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

const (
	// setupScheme is the scheme of the mandate a setup page requests (lago
	// payment_provider_customers/gocardless_service.rb create_billing_request).
	setupScheme = "bacs"
)

// usableMandateStatuses are the mandate states a payment can be created
// against (lago gocardless/payments/create_service.rb fetch_mandate_from_api).
var usableMandateStatuses = []string{"pending_customer_approval", "pending_submission", "submitted", "active"}

// DefaultMethodTypes implements [payment.CustomerGateway]: GoCardless
// customers have no method types; lago ignores provider_payment_methods
// for GoCardless.
func (g *Gateway) DefaultMethodTypes() []string { return nil }

// ValidateMethodTypes implements [payment.CustomerGateway]: any list is
// accepted and unused, as in lago.
func (g *Gateway) ValidateMethodTypes([]string) error { return nil }

// CreateCustomer implements [payment.CustomerGateway] (lago
// payment_provider_customers/gocardless_service.rb create_gocardless_customer):
// a customer with the first email address, the customer's own name as
// company name and its first and last names, blank ones left out. The
// request carries [payment.CustomerParams.IdempotencyKey], which lago
// does not send; a creation already made with it returns that customer.
func (g *Gateway) CreateCustomer(ctx context.Context, conn payment.Connection, p payment.CustomerParams) (string, error) {
	var id string
	err := g.do(ctx, conn, "create_customer", func(ctx context.Context, c call) error {
		opts := []gc.RequestOption{noRetries()}
		if p.IdempotencyKey != "" {
			opts = append(opts, gc.WithIdempotencyKey(p.IdempotencyKey))
		}
		cu, err := c.svc.Customers.Create(ctx, gc.CustomerCreateParams{
			Email:       p.Email,
			CompanyName: p.CompanyName,
			GivenName:   p.Firstname,
			FamilyName:  p.Lastname,
		}, opts...)
		if existing := conflictingResource(err); existing != "" {
			id = existing
			return nil
		}
		if err != nil {
			return c.sdk(err)
		}
		id = cu.Id
		return nil
	})
	return id, err
}

// UpdateCustomer implements [payment.CustomerGateway]: a no-op, as lago's
// update changes nothing at GoCardless (gocardless_service.rb update).
func (g *Gateway) UpdateCustomer(context.Context, payment.Connection, string, payment.CustomerParams) error {
	return nil
}

// SetupURL implements [payment.MethodGateway] (lago
// payment_provider_customers/gocardless_service.rb generate_checkout_url):
// a billing request for a Bacs mandate of the provider customer and its
// hosted flow, returning to the connection's success_redirect_url whether
// the customer completes or leaves it. The mandate's "created" event
// yields [payment.PaymentMethodAttached].
func (g *Gateway) SetupURL(ctx context.Context, conn payment.Connection, p payment.SetupParams) (string, error) {
	var u string
	err := g.do(ctx, conn, "setup_url", func(ctx context.Context, c call) error {
		br, err := c.svc.BillingRequests.Create(ctx, gc.BillingRequestCreateParams{
			MandateRequest: &gc.BillingRequestCreateParamsMandateRequest{Scheme: setupScheme},
			Links:          &gc.BillingRequestCreateParamsLinks{Customer: p.ProviderCustomerID},
		}, noRetries())
		if err != nil {
			return c.sdk(err)
		}
		ret := c.settings.successURL()
		flow, err := c.svc.BillingRequestFlows.Create(ctx, gc.BillingRequestFlowCreateParams{
			RedirectUri: ret,
			ExitUri:     ret,
			Links:       gc.BillingRequestFlowCreateParamsLinks{BillingRequest: br.Id},
		}, noRetries())
		if err != nil {
			return c.sdk(err)
		}
		u = flow.AuthorisationUrl
		return nil
	})
	return u, err
}

// ListPaymentMethods implements [payment.MethodGateway]: the provider
// customer's mandates a payment can be created against, in the API's
// order, newest first.
func (g *Gateway) ListPaymentMethods(ctx context.Context, conn payment.Connection, providerCustomerID string) ([]payment.ProviderMethod, error) {
	var out []payment.ProviderMethod
	err := g.do(ctx, conn, "list_payment_methods", func(ctx context.Context, c call) error {
		var err error
		out, err = mandates(ctx, c, providerCustomerID)
		return err
	})
	return out, err
}

// mandates lists the provider customer's usable mandates.
func mandates(ctx context.Context, c call, providerCustomerID string) ([]payment.ProviderMethod, error) {
	res, err := c.svc.Mandates.List(ctx, gc.MandateListParams{
		Customer: providerCustomerID,
		// The client encodes a list as repeated keys; the API takes a
		// comma-separated value.
		Status: []string{strings.Join(usableMandateStatuses, ",")},
	}, noRetries())
	if err != nil {
		return nil, c.sdk(err)
	}
	out := make([]payment.ProviderMethod, 0, len(res.Mandates))
	for _, m := range res.Mandates {
		out = append(out, methodOf(m))
	}
	return out, nil
}

// methodOf returns a mandate as a payment method; its type is the
// mandate's scheme ("bacs", "sepa_core").
func methodOf(m gc.Mandate) payment.ProviderMethod {
	pm := payment.ProviderMethod{ID: m.Id, Details: payment.MethodDetails{Type: m.Scheme}}
	if m.Links != nil {
		pm.ProviderCustomerID = m.Links.Customer
	}
	if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
		pm.CreatedAt = t.UTC()
	}
	return pm
}

// DefaultPaymentMethod implements [payment.MethodGateway]: GoCardless
// customers have no default mandate, so it is the newest usable one, the
// mandate lago charges when none is given (create_service.rb
// fetch_mandate_from_api); "" when there is none.
func (g *Gateway) DefaultPaymentMethod(ctx context.Context, conn payment.Connection, providerCustomerID string) (string, error) {
	ms, err := g.ListPaymentMethods(ctx, conn, providerCustomerID)
	if err != nil || len(ms) == 0 {
		return "", err
	}
	return ms[0].ID, nil
}

// PaymentMethod implements [payment.MethodGateway]: one mandate.
func (g *Gateway) PaymentMethod(ctx context.Context, conn payment.Connection, providerMethodID string) (payment.ProviderMethod, error) {
	var pm payment.ProviderMethod
	err := g.do(ctx, conn, "payment_method", func(ctx context.Context, c call) error {
		m, err := c.svc.Mandates.Get(ctx, providerMethodID, noRetries())
		if err != nil {
			return c.sdk(err)
		}
		pm = methodOf(*m)
		return nil
	})
	return pm, err
}

// SetDefaultPaymentMethod implements [payment.MethodGateway]: GoCardless
// keeps no default mandate, so it is [payment.ErrUnsupported]; the default
// lives in Tanvo only, as in lago.
func (g *Gateway) SetDefaultPaymentMethod(context.Context, payment.Connection, string, string) error {
	return fmt.Errorf("gocardless set default payment method: %w", payment.ErrUnsupported)
}

// DetachPaymentMethod implements [payment.MethodGateway]: lago removes a
// GoCardless method in its database only and leaves the mandate active
// (payment_methods/destroy_service.rb), so it is [payment.ErrUnsupported].
func (g *Gateway) DetachPaymentMethod(context.Context, payment.Connection, string) error {
	return fmt.Errorf("gocardless detach payment method: %w", payment.ErrUnsupported)
}
