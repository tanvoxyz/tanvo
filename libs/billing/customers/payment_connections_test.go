package customers_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// notFoundResource returns the resource of a not-found error, "" for
// another error.
func notFoundResource(err error) string {
	var nf *core.NotFoundError
	if !errors.As(err, &nf) {
		return ""
	}
	return nf.Resource
}

// paymentLinks returns the payment connections of customer c, discarded
// ones included.
func paymentLinks(h *billingtest.Harness, c *customer.Customer) []*payment.ProviderCustomer {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*payment.ProviderCustomer, error) {
		return r.PaymentProviderCustomers.ListByCustomer(ctx, h.OrgID(), c.ID)
	})
}

// reloadLink returns the payment connection id.
func reloadLink(h *billingtest.Harness, id core.PaymentProviderCustomerID) *payment.ProviderCustomer {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*payment.ProviderCustomer, error) {
		return r.PaymentProviderCustomers.Get(ctx, h.OrgID(), id)
	})
}

func TestCreatePaymentConnection(t *testing.T) {
	stripeEU := func(h *billingtest.Harness) { h.Integration("stripe_eu", integration.ProviderStripe) }
	for _, tc := range []struct {
		name    string
		source  string
		setup   func(h *billingtest.Harness, c *customer.Customer)
		missing bool
		ref     refs.Ref
		in      customers.ConnectionInput
		check   func(t *testing.T, h *billingtest.Harness, c *customer.Customer, got customers.Connection, err error)
	}{
		{
			name:    "customer not found",
			source:  "spec/services/payment_provider_customers/create_connection_service_spec.rb:18",
			missing: true,
			ref:     refs.ByCode("stripe_eu"),
			in:      customers.ConnectionInput{ExternalCustomerID: core.Some("cus_123")},
			check: func(t *testing.T, _ *billingtest.Harness, _ *customer.Customer, _ customers.Connection, err error) {
				if got := notFoundResource(err); got != customers.Resource {
					t.Errorf("got = %v, want = customer_not_found", err)
				}
			},
		},
		{
			// A connection names its integration; a missing reference is a
			// missing integration.
			name:   "integration missing",
			source: "spec/services/payment_provider_customers/create_connection_service_spec.rb:27",
			in:     customers.ConnectionInput{ExternalCustomerID: core.Some("cus_123")},
			check: func(t *testing.T, h *billingtest.Harness, c *customer.Customer, _ customers.Connection, err error) {
				if got := notFoundResource(err); got != customers.ResourceIntegration {
					t.Errorf("got = %v, want = integration_not_found", err)
				}
				if got := paymentLinks(h, c); len(got) != 0 {
					t.Errorf("got = %d connections, want = 0", len(got))
				}
			},
		},
		{
			name:   "neither a provider customer id nor a sync",
			source: "spec/services/payment_provider_customers/create_connection_service_spec.rb:37-47",
			ref:    refs.ByCode("stripe_eu"),
			check: func(t *testing.T, h *billingtest.Harness, c *customer.Customer, got customers.Connection, err error) {
				if err != nil || got.Integration != nil {
					t.Errorf("got = %+v %v, want = no connection", got, err)
				}
				if got := paymentLinks(h, c); len(got) != 0 {
					t.Errorf("got = %d connections, want = 0", len(got))
				}
			},
		},
		{
			name:   "integration does not exist",
			source: "spec/services/payment_provider_customers/create_connection_service_spec.rb:54",
			ref:    refs.ByCode("adyen"),
			in:     customers.ConnectionInput{ExternalCustomerID: core.Some("cus_123")},
			check: func(t *testing.T, _ *billingtest.Harness, _ *customer.Customer, _ customers.Connection, err error) {
				if got := notFoundResource(err); got != customers.ResourceIntegration {
					t.Errorf("got = %v, want = integration_not_found", err)
				}
			},
		},
		{
			name:   "valid integration",
			source: "spec/services/payment_provider_customers/create_connection_service_spec.rb:74-90",
			ref:    refs.ByCode("stripe_eu"),
			in: customers.ConnectionInput{
				ExternalCustomerID:     core.Some("cus_123"),
				ProviderPaymentMethods: core.Some([]string{"card", "sepa_debit"}),
			},
			check: func(t *testing.T, h *billingtest.Harness, c *customer.Customer, got customers.Connection, err error) {
				if err != nil {
					t.Fatalf("UpsertConnection: %v", err)
				}
				if got.Integration.Code != "stripe_eu" || got.ExternalCustomerID != "cus_123" || !got.IsDefault ||
					!slices.Equal(got.ProviderPaymentMethods, []string{"card", "sepa_debit"}) {
					t.Errorf("got = %+v, want = default stripe_eu connection cus_123 [card sepa_debit]", got)
				}
				if got := paymentLinks(h, c); len(got) != 1 {
					t.Errorf("got = %d connections, want = 1", len(got))
				}
			},
		},
		{
			name:   "several integrations of the provider",
			source: "spec/services/payment_provider_customers/create_connection_service_spec.rb:120",
			setup: func(h *billingtest.Harness, _ *customer.Customer) {
				h.Integration("stripe_us", integration.ProviderStripe)
			},
			ref: refs.ByCode("stripe_us"),
			in:  customers.ConnectionInput{ExternalCustomerID: core.Some("cus_123")},
			check: func(t *testing.T, _ *billingtest.Harness, _ *customer.Customer, got customers.Connection, err error) {
				if err != nil || got.Integration.Code != "stripe_us" {
					t.Errorf("got = %+v %v, want = stripe_us connection", got, err)
				}
			},
		},
		{
			name:   "customer already has a connection",
			source: "spec/services/payment_provider_customers/create_connection_service_spec.rb:141-147",
			setup: func(h *billingtest.Harness, c *customer.Customer) {
				h.PaymentConnection(c, h.Integration("adyen", integration.ProviderAdyen), true)
			},
			ref: refs.ByCode("stripe_eu"),
			in:  customers.ConnectionInput{ExternalCustomerID: core.Some("cus_123")},
			check: func(t *testing.T, h *billingtest.Harness, c *customer.Customer, got customers.Connection, err error) {
				if err != nil || got.IsDefault {
					t.Errorf("got = %+v %v, want = a connection that is not the default", got, err)
				}
				var defaults int
				for _, l := range paymentLinks(h, c) {
					if l.IsDefault {
						defaults++
					}
				}
				if defaults != 1 {
					t.Errorf("got = %d defaults, want = the adyen default only", defaults)
				}
			},
		},
		{
			name:   "sync with the provider",
			source: "spec/services/payment_provider_customers/create_connection_service_spec.rb:191",
			ref:    refs.ByCode("stripe_eu"),
			in:     customers.ConnectionInput{SyncWithProvider: core.Some(true)},
			check: func(t *testing.T, h *billingtest.Harness, _ *customer.Customer, _ customers.Connection, err error) {
				if err != nil {
					t.Fatalf("UpsertConnection: %v", err)
				}
				if got := jobs(h, kernel.JobSyncProviderCustomer); len(got) != 1 {
					t.Errorf("got = %d sync jobs, want = 1", len(got))
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Log("source: " + tc.source)
			h, svc := setup(t)
			stripeEU(h)
			c := h.Customer()
			if tc.setup != nil {
				tc.setup(h, c)
			}
			id := c.ID
			if tc.missing {
				id = core.NewID[core.CustomerID](h.IDs)
			}
			got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), id, tc.ref, tc.in)
			tc.check(t, h, c, got, err)
		})
	}
}

func TestSetDefaultPaymentConnection(t *testing.T) {
	setupLinks := func(t *testing.T) (*billingtest.Harness, *customers.Service, *customer.Customer, *payment.ProviderCustomer, *payment.ProviderCustomer) {
		h, svc := setup(t)
		c := h.Customer()
		stripe := h.PaymentConnection(c, h.Integration("stripe_eu", integration.ProviderStripe), true)
		gc := h.PaymentConnection(c, h.Integration("gocardless_eu", integration.ProviderGocardless), false)
		return h, svc, c, stripe, gc
	}

	t.Run("sets the default and clears the others", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/set_as_default_service_spec.rb:22")
		h, svc, c, stripe, gc := setupLinks(t)
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("gocardless_eu"), customers.ConnectionInput{IsDefault: core.Some(true)}); err != nil {
			t.Fatalf("UpsertConnection: %v", err)
		}
		if got := reloadLink(h, gc.ID).IsDefault; !got {
			t.Errorf("gocardless: got = %t, want = true", got)
		}
		if got := reloadLink(h, stripe.ID).IsDefault; got {
			t.Errorf("stripe: got = %t, want = false", got)
		}
	})

	t.Run("already the default", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/set_as_default_service_spec.rb:30")
		h, svc, c, stripe, gc := setupLinks(t)
		got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("stripe_eu"), customers.ConnectionInput{IsDefault: core.Some(true)})
		if err != nil || !got.IsDefault || got.ExternalCustomerID != *stripe.ProviderCustomerID {
			t.Errorf("got = %+v %v, want = the stripe default", got, err)
		}
		if got := reloadLink(h, gc.ID).IsDefault; got {
			t.Errorf("gocardless: got = %t, want = false", got)
		}
	})

	t.Run("no connection", func(t *testing.T) {
		// A default is set on an existing connection; naming an
		// integration the customer has no connection to creates none.
		t.Log("source: spec/services/payment_provider_customers/set_as_default_service_spec.rb:42")
		h, svc, c, _, _ := setupLinks(t)
		h.Integration("adyen", integration.ProviderAdyen)
		got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("adyen"), customers.ConnectionInput{IsDefault: core.Some(true)})
		if err != nil || got.Integration != nil {
			t.Errorf("got = %+v %v, want = no connection", got, err)
		}
		if got := len(paymentLinks(h, c)); got != 2 {
			t.Errorf("got = %d connections, want = 2", got)
		}
	})
}

func TestDeletePaymentConnection(t *testing.T) {
	t.Run("connection not found", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/destroy_service_spec.rb:20")
		h, svc := setup(t)
		h.Integration("stripe", integration.ProviderStripe)
		c := h.Customer()
		err := svc.DeleteConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("stripe"))
		if got := notFoundResource(err); got != customers.ResourceConnection {
			t.Errorf("got = %v, want = connection_not_found", err)
		}
	})

	t.Run("discards the connection and its methods", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/destroy_service_spec.rb:30-46")
		t.Log("source: spec/services/payment_provider_customers/destroy_service_spec.rb:56")
		h, svc := setup(t)
		c := h.Customer()
		pc := h.PaymentConnection(c, h.Integration("stripe", integration.ProviderStripe), true)
		m := &payment.Method{
			ID: core.NewID[core.PaymentMethodID](h.IDs), OrganizationID: h.OrgID(), CustomerID: c.ID, IntegrationID: pc.IntegrationID,
			PaymentProviderCustomerID: pc.ID, ProviderMethodID: "pm_1", IsDefault: true, CreatedAt: h.Now(), UpdatedAt: h.Now(),
		}
		h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Create(ctx, m) })
		if err := svc.DeleteConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("stripe")); err != nil {
			t.Fatalf("DeleteConnection: %v", err)
		}
		got := reloadLink(h, pc.ID)
		if got.IsDefault || got.DeletedAt == nil || !got.DeletedAt.Equal(h.Now()) {
			t.Errorf("got = default %t deleted %v, want = not default, deleted at %v", got.IsDefault, got.DeletedAt, h.Now())
		}
		gotM := billingtest.Read(h, func(ctx context.Context, r port.Repos) (*payment.Method, error) {
			return r.PaymentMethods.Get(ctx, h.OrgID(), m.ID)
		})
		if gotM.DeletedAt == nil {
			t.Errorf("got = live payment method, want = discarded")
		}
	})

	t.Run("billing objects routed to the connection", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/destroy_service_spec.rb:97-107")
		h, svc := setup(t)
		c := h.Customer()
		pc := h.PaymentConnection(c, h.Integration("stripe", integration.ProviderStripe), true)
		sub := h.Subscription(c, h.Plan(period.Monthly, 100))
		owner := integration.ObjectOwner{SubscriptionID: sub.ID}
		row := &integration.ObjectConnection{
			ID: core.NewID[core.ObjectConnectionID](h.IDs), OrganizationID: h.OrgID(), Owner: owner, Category: integration.CategoryPayment,
			Behavior: integration.ConnectionBehaviorSpecific, PaymentProviderCustomerID: pc.ID, CreatedAt: h.Now(), UpdatedAt: h.Now(),
		}
		h.Do(func(ctx context.Context, r port.Repos) error { return r.ObjectConnections.Create(ctx, row) })
		if err := svc.DeleteConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("stripe")); err != nil {
			t.Fatalf("DeleteConnection: %v", err)
		}
		if got := h.ObjectConnections(owner); len(got) != 0 {
			t.Errorf("got = %d choices, want = 0", len(got))
		}
		h.Integration("gocardless", integration.ProviderGocardless)
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("gocardless"), customers.ConnectionInput{ExternalCustomerID: core.Some("gc_1")}); err != nil {
			t.Fatalf("UpsertConnection: %v", err)
		}
		routes := billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]kernel.ConnectionRoute, error) {
			return kernel.ConnectionRouting(ctx, r, h.OrgID(), c.ID, owner)
		})
		i := slices.IndexFunc(routes, func(r kernel.ConnectionRoute) bool { return r.Category == integration.CategoryPayment })
		if i < 0 || routes[i].Behavior != integration.ConnectionBehaviorInherit || routes[i].Code != "gocardless" {
			t.Errorf("got = %+v, want = payment inherits gocardless", routes)
		}
	})
}

func TestUpdatePaymentConnection(t *testing.T) {
	// setupLink returns a customer's Stripe connection with the provider
	// customer id ext ("" for none) and the card method type.
	setupLink := func(t *testing.T, ext string) (*billingtest.Harness, *customers.Service, *customer.Customer, *payment.ProviderCustomer) {
		h, svc := setup(t)
		c := h.Customer()
		pc := h.PaymentConnection(c, h.Integration("stripe", integration.ProviderStripe), true)
		pc.ProviderCustomerID, pc.ProviderPaymentMethods = nil, []string{"card"}
		if ext != "" {
			pc.ProviderCustomerID = new(ext)
		}
		h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Update(ctx, pc) })
		return h, svc, c, pc
	}
	update := func(t *testing.T, h *billingtest.Harness, svc *customers.Service, c *customer.Customer, in customers.ConnectionInput) {
		t.Helper()
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("stripe"), in); err != nil {
			t.Fatalf("UpsertConnection: %v", err)
		}
	}
	pushes := func(h *billingtest.Harness) int {
		var n int
		for _, j := range jobs(h, kernel.JobSyncProviderCustomer) {
			var args kernel.ProviderCustomerArgs
			if err := json.Unmarshal(j.Args, &args); err == nil && args.Update {
				n++
			}
		}
		return n
	}

	t.Run("provider payment methods", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/update_connection_service_spec.rb:69")
		h, svc, c, pc := setupLink(t, "cus_old")
		update(t, h, svc, c, customers.ConnectionInput{ProviderPaymentMethods: core.Some([]string{"card", "sepa_debit"})})
		if got := reloadLink(h, pc.ID).ProviderPaymentMethods; !slices.Equal(got, []string{"card", "sepa_debit"}) {
			t.Errorf("got = %v, want = [card sepa_debit]", got)
		}
		if got := pushes(h); got != 0 {
			t.Errorf("got = %d pushes, want = 0", got)
		}
	})

	t.Run("sync with the provider", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/update_connection_service_spec.rb:93")
		h, svc, c, pc := setupLink(t, "cus_old")
		update(t, h, svc, c, customers.ConnectionInput{SyncWithProvider: core.Some(true)})
		if got := reloadLink(h, pc.ID).SyncWithProvider; !got {
			t.Errorf("got = %t, want = true", got)
		}
	})

	t.Run("provider customer id", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/update_connection_service_spec.rb:107, 112")
		t.Log("source: spec/services/payment_provider_customers/update_service_spec.rb:20")
		h, svc, c, pc := setupLink(t, "cus_old")
		update(t, h, svc, c, customers.ConnectionInput{ExternalCustomerID: core.Some("cus_new")})
		if got := reloadLink(h, pc.ID).ProviderCustomerID; got == nil || *got != "cus_new" {
			t.Errorf("got = %v, want = cus_new", got)
		}
		if got := pushes(h); got != 1 {
			t.Errorf("got = %d pushes, want = 1", got)
		}
	})

	t.Run("no provider customer id", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/update_connection_service_spec.rb:124")
		h, svc, c, pc := setupLink(t, "")
		update(t, h, svc, c, customers.ConnectionInput{ProviderPaymentMethods: core.Some([]string{"card", "sepa_debit"})})
		if got := reloadLink(h, pc.ID).ProviderPaymentMethods; !slices.Equal(got, []string{"card", "sepa_debit"}) {
			t.Errorf("got = %v, want = [card sepa_debit]", got)
		}
	})
}

// methodTypesGateway is a payment adapter with Stripe's method type rules
// reduced to card and sepa_debit.
type methodTypesGateway struct{ payment.Gateway }

func (methodTypesGateway) DefaultMethodTypes() []string { return []string{"card"} }

func (methodTypesGateway) ValidateMethodTypes(types []string) error {
	var v core.Validator
	v.Check(len(types) > 0, "provider_payment_methods", core.CodeRequired)
	for _, t := range types {
		v.Check(t == "card" || t == "sepa_debit", "provider_payment_methods", core.CodeInvalid)
	}
	return v.Err()
}

// methodTypesGateways serves [methodTypesGateway] for Stripe only.
type methodTypesGateways struct{}

func (methodTypesGateways) PaymentProvider(p integration.Provider) (port.PaymentProvider, error) {
	if p != integration.ProviderStripe {
		return nil, core.ErrNotFound
	}
	return methodTypesGateway{}, nil
}

func TestPaymentConnectionMethodTypes(t *testing.T) {
	setupTypes := func(t *testing.T) (*billingtest.Harness, *customers.Service, *customer.Customer) {
		t.Helper()
		h := billingtest.New(t, june22)
		svc, err := customers.New(h.Deps, customers.Relations{}, customers.WithPaymentProviders(methodTypesGateways{}))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		h.Integration("stripe", integration.ProviderStripe)
		h.Integration("adyen", integration.ProviderAdyen)
		return h, svc, h.Customer()
	}
	upsert := func(h *billingtest.Harness, svc *customers.Service, c *customer.Customer, code string, in customers.ConnectionInput) (customers.Connection, error) {
		return svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode(code), in)
	}

	t.Run("default types of a new connection", func(t *testing.T) {
		h, svc, c := setupTypes(t)
		got, err := upsert(h, svc, c, "stripe", customers.ConnectionInput{ExternalCustomerID: core.Some("cus_1")})
		if err != nil || !slices.Equal(got.ProviderPaymentMethods, []string{"card"}) {
			t.Errorf("got = %v %v, want = [card]", got.ProviderPaymentMethods, err)
		}
	})

	t.Run("invalid types of a new connection", func(t *testing.T) {
		h, svc, c := setupTypes(t)
		_, err := upsert(h, svc, c, "stripe", customers.ConnectionInput{ExternalCustomerID: core.Some("cus_1"), ProviderPaymentMethods: core.Some([]string{"crypto"})})
		if got := fieldCode(err, "provider_payment_methods"); got != core.CodeInvalid {
			t.Errorf("got = %v, want = provider_payment_methods invalid_value", err)
		}
		if got := paymentLinks(h, c); len(got) != 0 {
			t.Errorf("got = %d connections, want = 0", len(got))
		}
	})

	t.Run("invalid types of an existing connection", func(t *testing.T) {
		t.Log("source: spec/services/payment_provider_customers/update_connection_service_spec.rb:80, 85")
		h, svc, c := setupTypes(t)
		if _, err := upsert(h, svc, c, "stripe", customers.ConnectionInput{ExternalCustomerID: core.Some("cus_1")}); err != nil {
			t.Fatalf("UpsertConnection: %v", err)
		}
		_, err := upsert(h, svc, c, "stripe", customers.ConnectionInput{ExternalCustomerID: core.Some("cus_2"), ProviderPaymentMethods: core.Some([]string{"invalid_method"})})
		if got := fieldCode(err, "provider_payment_methods"); got != core.CodeInvalid {
			t.Errorf("got = %v, want = provider_payment_methods invalid_value", err)
		}
		links := paymentLinks(h, c)
		if len(links) != 1 || *links[0].ProviderCustomerID != "cus_1" || !slices.Equal(links[0].ProviderPaymentMethods, []string{"card"}) {
			t.Errorf("got = %+v, want = the cus_1 connection unchanged", links)
		}
	})

	t.Run("empty types keep the existing ones", func(t *testing.T) {
		h, svc, c := setupTypes(t)
		if _, err := upsert(h, svc, c, "stripe", customers.ConnectionInput{ExternalCustomerID: core.Some("cus_1"), ProviderPaymentMethods: core.Some([]string{"sepa_debit"})}); err != nil {
			t.Fatalf("UpsertConnection: %v", err)
		}
		got, err := upsert(h, svc, c, "stripe", customers.ConnectionInput{ProviderPaymentMethods: core.Some([]string{})})
		if err != nil || !slices.Equal(got.ProviderPaymentMethods, []string{"sepa_debit"}) {
			t.Errorf("got = %v %v, want = [sepa_debit]", got.ProviderPaymentMethods, err)
		}
	})

	t.Run("vendor without an adapter", func(t *testing.T) {
		h, svc, c := setupTypes(t)
		got, err := upsert(h, svc, c, "adyen", customers.ConnectionInput{ExternalCustomerID: core.Some("shopper_1"), ProviderPaymentMethods: core.Some([]string{"anything"})})
		if err != nil || !slices.Equal(got.ProviderPaymentMethods, []string{"anything"}) {
			t.Errorf("got = %v %v, want = [anything]", got.ProviderPaymentMethods, err)
		}
	})
}
