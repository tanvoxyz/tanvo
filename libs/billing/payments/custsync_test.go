package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// custGateway is a Stripe adapter whose customer, checkout page and
// webhook registration calls answer with fixed results.
type custGateway struct {
	*fakeGateway

	createID  string
	createErr error
	created   []payment.CustomerParams

	updateErr error
	updated   []string

	setup    string
	setupErr error
	setups   []payment.SetupParams

	register    payment.ConnectionUpdate
	registerErr error
	registered  []string
}

func (g *custGateway) CreateCustomer(_ context.Context, _ payment.Connection, p payment.CustomerParams) (string, error) {
	g.created = append(g.created, p)
	return g.createID, g.createErr
}

func (g *custGateway) UpdateCustomer(_ context.Context, _ payment.Connection, id string, _ payment.CustomerParams) error {
	g.updated = append(g.updated, id)
	return g.updateErr
}

func (g *custGateway) SetupURL(_ context.Context, _ payment.Connection, p payment.SetupParams) (string, error) {
	g.setups = append(g.setups, p)
	return g.setup, g.setupErr
}

func (g *custGateway) RegisterWebhook(_ context.Context, _ payment.Connection, url string) (payment.ConnectionUpdate, error) {
	g.registered = append(g.registered, url)
	return g.register, g.registerErr
}

// custFixture is a provider fixture whose provider customer is not yet
// created at Stripe, in an organization with a webhook endpoint.
type custFixture struct {
	*providerFixture
	cg    *custGateway
	pcust *payment.ProviderCustomer
}

func newCustFixture(t *testing.T) *custFixture {
	t.Helper()
	f := &custFixture{providerFixture: newProviderFixture(t)}
	f.cg = &custGateway{fakeGateway: f.gw, createID: "cus_new", setup: "https://checkout.stripe.com/c/pay/cs_1"}
	set, err := port.NewPaymentProviderSet(f.cg)
	if err != nil {
		t.Fatal(err)
	}
	f.svc = payments.New(f.h.Deps).WithProviders(payments.Providers{
		Gateways:   set,
		WebhookURL: func(payment.Connection) string { return "https://billing.example.com/webhooks/stripe" },
	})
	f.h.Do(func(ctx context.Context, r port.Repos) error {
		pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, f.h.OrgID(), f.cust.ID)
		if err != nil {
			return err
		}
		f.pcust = pcs[0]
		f.pcust.ProviderCustomerID = nil
		if err := r.PaymentProviderCustomers.Update(ctx, f.pcust); err != nil {
			return err
		}
		return r.WebhookEndpoints.Create(ctx, &webhook.Endpoint{
			ID: core.NewID[core.WebhookEndpointID](f.h.IDs), OrganizationID: f.h.OrgID(), URL: "https://example.com/hooks",
			Secret: "whsec_dGVzdA==", CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
		})
	})
	return f
}

func (f *custFixture) sync(args kernel.ProviderCustomerArgs) error {
	args.ProviderCustomerID = f.pcust.ID
	return f.svc.SyncProviderCustomer(f.h.Ctx, f.h.OrgID(), args)
}

func (f *custFixture) providerCustomer() *payment.ProviderCustomer {
	return billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*payment.ProviderCustomer, error) {
		return r.PaymentProviderCustomers.Get(ctx, f.h.OrgID(), f.pcust.ID)
	})
}

// payloadOf decodes the payload of the last webhook of type e, failing
// when there is none.
func payloadOf[T any](t *testing.T, h *billingtest.Harness, e webhook.EventType) T {
	t.Helper()
	var out T
	evs := h.Emitted.Events()
	i := slices.IndexFunc(evs, func(ev webhook.Event) bool { return ev.Type == e })
	if i < 0 {
		t.Fatalf("webhooks: got = %v, want = %s", h.Webhooks(), e)
	}
	if err := json.Unmarshal(evs[i].Payload, &out); err != nil {
		t.Fatalf("payload of %s: %v", e, err)
	}
	return out
}

var authFailure = &payment.ProviderError{
	Provider: "stripe", Kind: payment.KindAuthentication, Message: "Invalid API Key provided: sk_test_*********",
}

// TestSyncProviderCustomerCreate ports PaymentProviderCustomers::StripeService#create.
func TestSyncProviderCustomerCreate(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:73, 84
		f := newCustFixture(t)
		if err := f.sync(kernel.ProviderCustomerArgs{}); err != nil {
			t.Fatalf("SyncProviderCustomer: %v", err)
		}
		if got := deref(f.providerCustomer().ProviderCustomerID); got != "cus_new" {
			t.Errorf("provider customer ID: got = %q, want = cus_new", got)
		}
		if !hasWebhook(f.h, webhook.EventTypeCustomerPaymentProviderCreated) {
			t.Errorf("webhooks: got = %v, want = %s", f.h.Webhooks(), webhook.EventTypeCustomerPaymentProviderCreated)
		}
		if !slices.Contains(f.h.JobKinds(), kernel.JobSyncProviderCustomer) {
			t.Errorf("jobs: got = %v, want = the checkout URL job", f.h.JobKinds())
		}
	})
	t.Run("already created", func(t *testing.T) {
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:101
		f := newCustFixture(t)
		id := "cus_1"
		f.pcust.ProviderCustomerID = &id
		f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Update(ctx, f.pcust) })
		if err := f.sync(kernel.ProviderCustomerArgs{}); err != nil {
			t.Fatalf("SyncProviderCustomer: %v", err)
		}
		if len(f.cg.created) != 0 {
			t.Errorf("provider calls: got = %d, want = 0", len(f.cg.created))
		}
	})
	t.Run("refused", func(t *testing.T) {
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:148, 162
		f := newCustFixture(t)
		f.cg.createErr = authFailure
		if err := f.sync(kernel.ProviderCustomerArgs{}); err != nil {
			t.Fatalf("SyncProviderCustomer: %v", err)
		}
		got := payloadOf[payments.CustomerProviderError](t, f.h, webhook.EventTypeCustomerPaymentProviderError)
		if got.CustomerID != f.cust.ID || got.PaymentProvider != "stripe" || got.PaymentProviderCode != "stripe" || got.ProviderError.Message != authFailure.Message {
			t.Errorf("payload: got = %+v, want = the customer, stripe and the provider's message", got)
		}
		if deref(f.providerCustomer().ProviderCustomerID) != "" {
			t.Errorf("provider customer ID: got = %q, want = none", deref(f.providerCustomer().ProviderCustomerID))
		}
	})
}

// TestSyncProviderCustomerUpdate ports PaymentProviderCustomers::StripeService#update.
func TestSyncProviderCustomerUpdate(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		err        error
		wantCalls  int
		wantError  bool
	}{
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:227, 253, 277
		{name: "refused", providerID: "cus_1", err: authFailure, wantCalls: 1, wantError: true},
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:297, 303, 309
		{name: "updated", providerID: "cus_1", wantCalls: 1},
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:360, 366, 372
		{name: "not created at the provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCustFixture(t)
			if tt.providerID != "" {
				f.pcust.ProviderCustomerID = &tt.providerID
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Update(ctx, f.pcust) })
			}
			f.cg.updateErr = tt.err
			if err := f.sync(kernel.ProviderCustomerArgs{Update: true}); err != nil {
				t.Fatalf("SyncProviderCustomer: %v", err)
			}
			if len(f.cg.updated) != tt.wantCalls {
				t.Errorf("provider calls: got = %d, want = %d", len(f.cg.updated), tt.wantCalls)
			}
			if got := hasWebhook(f.h, webhook.EventTypeCustomerPaymentProviderError); got != tt.wantError {
				t.Errorf("customer.payment_provider_error: got = %t, want = %t", got, tt.wantError)
			}
		})
	}
}

// TestSyncProviderCustomerCheckout ports
// PaymentProviderCustomers::StripeService#generate_checkout_url.
func TestSyncProviderCustomerCheckout(t *testing.T) {
	tests := []struct {
		name      string
		setupErr  error
		noHooks   bool
		wantURL   bool
		wantError bool
	}{
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:510
		{name: "generated", wantURL: true},
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:560
		{name: "no method to set up", setupErr: payment.ErrNoSetupMethods},
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:595
		{name: "refused", setupErr: authFailure, wantError: true},
		{name: "no webhook endpoint", noHooks: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCustFixture(t)
			id := "cus_1"
			f.pcust.ProviderCustomerID = &id
			f.h.Do(func(ctx context.Context, r port.Repos) error {
				if tt.noHooks {
					eps, err := r.WebhookEndpoints.List(ctx, f.h.OrgID(), core.PageRequest{})
					if err != nil {
						return err
					}
					for _, ep := range eps.Items {
						if err := r.WebhookEndpoints.Delete(ctx, f.h.OrgID(), ep.ID); err != nil {
							return err
						}
					}
				}
				return r.PaymentProviderCustomers.Update(ctx, f.pcust)
			})
			f.cg.setupErr = tt.setupErr
			if err := f.sync(kernel.ProviderCustomerArgs{Checkout: true}); err != nil {
				t.Fatalf("SyncProviderCustomer: %v", err)
			}
			if got := hasWebhook(f.h, webhook.EventTypeCustomerCheckoutURLGenerated); got != tt.wantURL {
				t.Fatalf("customer.checkout_url_generated: got = %t, want = %t", got, tt.wantURL)
			}
			if tt.wantURL {
				got := payloadOf[payments.CheckoutURLGenerated](t, f.h, webhook.EventTypeCustomerCheckoutURLGenerated)
				if got.CheckoutURL != f.cg.setup || got.CustomerID != f.cust.ID || got.ExternalCustomerID != f.cust.ExternalID || got.PaymentProvider != "stripe" {
					t.Errorf("payload: got = %+v, want = %s of the customer", got, f.cg.setup)
				}
				if p := f.cg.setups[0]; p.ProviderCustomerID != "cus_1" || !slices.Equal(p.MethodTypes, []string{"card"}) {
					t.Errorf("setup: got = %+v, want = cus_1 with card", p)
				}
			}
			if got := hasWebhook(f.h, webhook.EventTypeCustomerPaymentProviderError); got != tt.wantError {
				t.Errorf("customer.payment_provider_error: got = %t, want = %t", got, tt.wantError)
			}
		})
	}
}

// TestRegisterProviderWebhook ports PaymentProviders::Stripe::RegisterWebhookService.
func TestRegisterProviderWebhook(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantError bool
	}{
		// source: spec/services/payment_providers/stripe/register_webhook_service_spec.rb:33
		{name: "registered"},
		// source: spec/services/payment_providers/stripe/register_webhook_service_spec.rb:48
		{name: "authentication failure", err: authFailure, wantError: true},
		// source: spec/services/payment_providers/stripe/register_webhook_service_spec.rb:77
		{
			name: "endpoint limit", wantError: true,
			err: &payment.ProviderError{Provider: "stripe", Kind: payment.KindInvalidRequest, Message: "You have reached the maximum of 16 test webhook endpoints."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCustFixture(t)
			f.cg.registerErr = tt.err
			f.cg.register = payment.ConnectionUpdate{Settings: json.RawMessage(`{"webhook_endpoint_id":"we_1"}`)}
			if err := f.svc.RegisterProviderWebhook(f.h.Ctx, f.h.OrgID(), kernel.IntegrationArgs{IntegrationID: f.integ.ID}); err != nil {
				t.Fatalf("RegisterProviderWebhook: %v", err)
			}
			if !slices.Equal(f.cg.registered, []string{"https://billing.example.com/webhooks/stripe"}) {
				t.Errorf("registered: got = %v, want = the connection's endpoint", f.cg.registered)
			}
			if !tt.wantError {
				integ := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*integration.Integration, error) {
					return r.Integrations.Get(ctx, f.h.OrgID(), f.integ.ID)
				})
				if got := string(integ.Settings); got != `{"webhook_endpoint_id":"we_1"}` {
					t.Errorf("settings: got = %s, want = the endpoint ID", got)
				}
			}
			if got := hasWebhook(f.h, webhook.EventTypePaymentProviderError); got != tt.wantError {
				t.Fatalf("payment_provider.error: got = %t, want = %t", got, tt.wantError)
			}
			if tt.wantError {
				got := payloadOf[payments.ProviderFailure](t, f.h, webhook.EventTypePaymentProviderError)
				pe, _ := payment.AsProviderError(tt.err)
				if got.Source != "stripe" || got.Action != "payment_provider.register_webhook" || got.ProviderError.Message != pe.Message ||
					got.IntegrationID != f.integ.ID || got.PaymentProviderCode != "stripe" {
					t.Errorf("payload: got = %+v, want = stripe payment_provider.register_webhook %q", got, pe.Message)
				}
			}
		})
	}
}

// TestSyncProviderCustomerIgnored checks that a provider customer job of
// an archived customer or of a removed connection calls nothing.
func TestSyncProviderCustomerIgnored(t *testing.T) {
	tests := []struct {
		name  string
		args  kernel.ProviderCustomerArgs
		setup func(f *custFixture)
	}{
		{
			// source: spec/services/payment_provider_customers/stripe_service_spec.rb:22
			name: "create for an archived customer", setup: archiveCustomer,
		},
		{
			// source: spec/services/payment_provider_customers/stripe_service_spec.rb:549
			name: "checkout for an archived customer", args: kernel.ProviderCustomerArgs{Checkout: true}, setup: archiveCustomer,
		},
		{
			// source: spec/services/payment_provider_customers/stripe_service_spec.rb:117, 125
			name: "create without connection", setup: removeConnection,
		},
		{
			// source: spec/services/payment_provider_customers/stripe_service_spec.rb:317, 323, 329, 380, 386, 392
			name: "update without connection", args: kernel.ProviderCustomerArgs{Update: true}, setup: removeConnection,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCustFixture(t)
			id := "cus_1"
			if tt.args.Update || tt.args.Checkout {
				f.pcust.ProviderCustomerID = &id
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Update(ctx, f.pcust) })
			}
			tt.setup(f)
			if err := f.sync(tt.args); err != nil {
				t.Fatalf("SyncProviderCustomer: %v", err)
			}
			if n := len(f.cg.created) + len(f.cg.updated) + len(f.cg.setups); n != 0 {
				t.Errorf("provider calls: got = %d, want = 0", n)
			}
			if got := f.h.Webhooks(); len(got) != 0 {
				t.Errorf("webhooks: got = %v, want = none", got)
			}
		})
	}
}

func archiveCustomer(f *custFixture) {
	at := f.h.Now()
	f.cust.DeletedAt = &at
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Customers.Update(ctx, f.cust) })
}

func removeConnection(f *custFixture) {
	at := f.h.Now()
	f.integ.DeletedAt = &at
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Integrations.Update(ctx, f.integ) })
}

// TestGenerateCheckoutURL ports Customers::GenerateCheckoutUrlService
// through the customer service wired to the payment providers: the URL of
// the customer's connection is returned without the
// customer.checkout_url_generated webhook (send_webhook: false).
func TestGenerateCheckoutURL(t *testing.T) {
	f := newCustFixture(t)
	id := "cus_1"
	f.pcust.ProviderCustomerID = &id
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Update(ctx, f.pcust) })
	cs, err := customers.New(f.h.Deps, customers.Relations{}, customers.WithCheckout(f.svc))
	if err != nil {
		t.Fatal(err)
	}
	// source: spec/services/customers/generate_checkout_url_service_spec.rb:29
	u, _, err := cs.CheckoutURL(f.h.Ctx, f.h.OrgID(), f.cust.ID, refs.Ref{})
	if err != nil || u != f.cg.setup {
		t.Fatalf("CheckoutURL: got = %q, %v, want = %q", u, err, f.cg.setup)
	}
	if len(f.cg.setups) != 1 || f.cg.setups[0].ProviderCustomerID != "cus_1" {
		t.Errorf("setup calls: got = %+v, want = one for cus_1", f.cg.setups)
	}
	if hasWebhook(f.h, webhook.EventTypeCustomerCheckoutURLGenerated) {
		t.Errorf("customer.checkout_url_generated: got = sent, want = none")
	}
	// source: spec/services/customers/generate_checkout_url_service_spec.rb:37
	if _, _, err := cs.CheckoutURL(f.h.Ctx, f.h.OrgID(), core.NewID[core.CustomerID](f.h.IDs), refs.Ref{}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown customer: got = %v, want = not found", err)
	}
}
