package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// authorizingGateway is the fake Stripe adapter with pre-authorizations.
type authorizingGateway struct {
	*fakeGateway

	auth    payment.PaymentResult
	authErr error
	auths   []payment.AuthorizeParams
}

func (g *authorizingGateway) Authorize(_ context.Context, _ payment.Connection, p payment.AuthorizeParams) (payment.PaymentResult, error) {
	g.auths = append(g.auths, p)
	return g.auth, g.authErr
}

// newAuthorizeFixture is the provider fixture with an authorizing
// gateway holding pi_auth.
func newAuthorizeFixture(t *testing.T) (*providerFixture, *authorizingGateway, *payments.Service) {
	t.Helper()
	f := newProviderFixture(t)
	gw := &authorizingGateway{fakeGateway: f.gw, auth: payment.PaymentResult{
		ProviderPaymentID: "pi_auth", ProviderStatus: "requires_capture", Status: payment.StateProcessing,
		Amount: 100, AmountCapturable: 100, Currency: money.MustCurrency("USD"),
	}}
	set, err := port.NewPaymentProviderSet(gw)
	if err != nil {
		t.Fatal(err)
	}
	return f, gw, payments.New(f.h.Deps).WithProviders(payments.Providers{Gateways: set})
}

func TestAuthorize(t *testing.T) {
	usd := money.MustCurrency("USD")
	t.Run("holds the amount and releases it", func(t *testing.T) {
		// source: spec/requests/api/v1/subscriptions_controller_spec.rb:470
		f, gw, svc := newAuthorizeFixture(t)
		plan := f.h.Plan(period.Monthly, 1000)
		res, err := svc.Authorize(f.h.Ctx, f.h.OrgID(), payments.AuthorizeInput{
			Customer: payments.CustomerRef{ExternalID: " " + f.cust.ExternalID + " "}, Amount: 100, Currency: usd,
			Plan: refs.ByID(plan.ID), RequestID: "req_1",
		})
		if err != nil {
			t.Fatalf("Authorize: %v", err)
		}
		if res.ProviderPaymentID != "pi_auth" || res.AmountCapturable != 100 {
			t.Errorf("result: got = %+v, want = pi_auth holding 100", res)
		}
		if len(gw.auths) != 1 {
			t.Fatalf("authorizations: got = %d, want = 1", len(gw.auths))
		}
		p := gw.auths[0]
		// The customer's default method, the plan's code
		// (subscriptions_controller.rb:49-61).
		def := defaultProviderMethod(t, f)
		if p.ProviderMethodID != def || p.ProviderCustomerID != "cus_1" || p.Amount != 100 || p.Metadata["plan_code"] != plan.Code {
			t.Errorf("params: got = %+v, want = %s on cus_1, 100, plan_code %s", p, def, plan.Code)
		}
		jobs := f.h.Store.Jobs()
		var found bool
		for _, j := range jobs {
			if j.Kind != kernel.JobCancelAuthorization {
				continue
			}
			var a kernel.AuthorizationArgs
			if err := json.Unmarshal(j.Args, &a); err != nil {
				t.Fatal(err)
			}
			found = a.ProviderPaymentID == "pi_auth" && a.IntegrationID == f.integ.ID
		}
		if !found {
			t.Errorf("jobs: got = %v, want = the release of pi_auth", f.h.JobKinds())
		}
		// source: spec/jobs/payment_providers/cancel_payment_authorization_job_spec.rb:6
		if err := svc.CancelAuthorization(f.h.Ctx, f.h.OrgID(), kernel.AuthorizationArgs{IntegrationID: f.integ.ID, ProviderPaymentID: "pi_auth"}); err != nil {
			t.Fatalf("CancelAuthorization: %v", err)
		}
		if len(gw.cancelled) != 1 || gw.cancelled[0] != "pi_auth" {
			t.Errorf("cancelled: got = %v, want = [pi_auth]", gw.cancelled)
		}
	})
	t.Run("the chosen method", func(t *testing.T) {
		f, gw, svc := newAuthorizeFixture(t)
		m := storedMethod(f.h, f.cust, false)
		if _, err := svc.Authorize(f.h.Ctx, f.h.OrgID(), payments.AuthorizeInput{
			Customer: payments.CustomerRef{ID: f.cust.ID}, PaymentMethodID: m.ID, Amount: 100, Currency: usd,
			Plan: refs.Ref{Code: "premium"},
		}); err != nil {
			t.Fatalf("Authorize: %v", err)
		}
		if got := gw.auths[0]; got.ProviderMethodID != m.ProviderMethodID || got.Metadata["plan_code"] != "premium" {
			t.Errorf("params: got = %+v, want = %s, plan_code premium", got, m.ProviderMethodID)
		}
		_, err := svc.Authorize(f.h.Ctx, f.h.OrgID(), payments.AuthorizeInput{
			Customer: payments.CustomerRef{ID: f.cust.ID}, PaymentMethodID: core.NewID[core.PaymentMethodID](f.h.IDs), Amount: 100, Currency: usd,
		})
		if nf, ok := errors.AsType[*core.NotFoundError](err); !ok || nf.Resource != "payment_method" {
			t.Errorf("unknown method: got = %v, want = payment_method not found", err)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		f, gw, svc := newAuthorizeFixture(t)
		other := f.h.Customer()
		adyen := &integration.Integration{
			ID: core.NewID[core.IntegrationID](f.h.IDs), OrganizationID: f.h.OrgID(), Code: "adyen", Name: "Adyen",
			Category: integration.CategoryPayment, Provider: integration.ProviderAdyen,
			Settings: []byte("{}"), CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
		}
		f.h.Do(func(ctx context.Context, r port.Repos) error {
			if err := r.Integrations.Create(ctx, adyen); err != nil {
				return err
			}
			return r.PaymentProviderCustomers.Create(ctx, &payment.ProviderCustomer{
				ID: core.NewID[core.PaymentProviderCustomerID](f.h.IDs), OrganizationID: f.h.OrgID(), CustomerID: other.ID,
				IntegrationID: adyen.ID, CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
			})
		})
		bare := f.h.Customer()
		tests := []struct {
			name     string
			source   string
			customer payments.CustomerRef
			authErr  error
			check    func(err error) bool
		}{
			{
				name: "customer without connection", source: "app/controllers/api/v1/subscriptions_controller.rb:35",
				customer: payments.CustomerRef{ID: bare.ID},
				check:    func(err error) bool { return isRule(err, payments.CodeStripeRequired) },
			},
			{
				name: "unknown customer", source: "app/controllers/api/v1/subscriptions_controller.rb:17-20",
				customer: payments.CustomerRef{ExternalID: "new_customer"},
				check:    func(err error) bool { return isRule(err, payments.CodeStripeRequired) },
			},
			{
				name: "connection of another provider", source: "app/controllers/api/v1/subscriptions_controller.rb:35",
				customer: payments.CustomerRef{ID: other.ID},
				check:    func(err error) bool { return isRule(err, payments.CodeStripeRequired) },
			},
			{
				name: "no payment method", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:499",
				customer: payments.CustomerRef{ID: f.cust.ID}, authErr: payment.ErrNoPaymentMethod,
				check: func(err error) bool {
					ve, ok := errors.AsType[*core.ValidationError](err)
					return ok && len(ve.Errors) == 1 && ve.Errors[0].Field == "payment_method_id" && ve.Errors[0].Code == payments.CodeCustomerHasNoPaymentMethod
				},
			},
			{
				name: "card declined", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:520",
				customer: payments.CustomerRef{ID: f.cust.ID}, authErr: declined,
				check: func(err error) bool {
					pe, ok := payment.AsProviderError(err)
					return ok && pe.Code == "card_declined"
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Logf("source: %s", tt.source)
				gw.authErr = tt.authErr
				before := len(f.h.JobKinds())
				_, err := svc.Authorize(f.h.Ctx, f.h.OrgID(), payments.AuthorizeInput{Customer: tt.customer, Amount: 100, Currency: usd})
				if !tt.check(err) {
					t.Errorf("error: got = %v, want = the %s refusal", err, tt.name)
				}
				if n := len(f.h.JobKinds()); n != before {
					t.Errorf("jobs: got = %d new, want = 0", n-before)
				}
			})
		}
	})
}

func isRule(err error, code string) bool {
	re, ok := errors.AsType[*core.RuleError](err)
	return ok && re.Code == code
}

// defaultProviderMethod returns the provider ID of the fixture's
// default method.
func defaultProviderMethod(t *testing.T, f *providerFixture) string {
	t.Helper()
	var id string
	f.h.Do(func(ctx context.Context, r port.Repos) error {
		m, err := r.PaymentMethods.GetDefault(ctx, f.h.OrgID(), f.cust.ID)
		if err == nil {
			id = m.ProviderMethodID
		}
		return err
	})
	return id
}
