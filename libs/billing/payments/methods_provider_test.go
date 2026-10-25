package payments_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// providerMethod stores a method of the provider customer pc with the
// provider identifier id.
func providerMethod(h *billingtest.Harness, pc *payment.ProviderCustomer, id string, isDefault bool, details payment.MethodDetails) *payment.Method {
	h.T.Helper()
	m := &payment.Method{
		ID: core.NewID[core.PaymentMethodID](h.IDs), OrganizationID: h.OrgID(), CustomerID: pc.CustomerID, IntegrationID: pc.IntegrationID,
		PaymentProviderCustomerID: pc.ID, ProviderMethodID: id, Details: details, IsDefault: isDefault, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Create(ctx, m) })
	return m
}

// customerMethods returns the payment methods of customer c.
func customerMethods(h *billingtest.Harness, c *customer.Customer) []*payment.Method {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*payment.Method, error) {
		return r.PaymentMethods.ListByCustomer(ctx, h.OrgID(), c.ID)
	})
}

func TestFindOrCreateMethod(t *testing.T) {
	card := payment.MethodDetails{Type: "card", Last4: "4242", Brand: "visa", ExpMonth: 12, ExpYear: 2028}
	for _, tt := range []struct {
		name       string
		sources    []string
		types      []string
		existing   *bool
		id         string
		details    payment.MethodDetails
		setDefault bool
		// want is the details of the method returned, nil for none.
		want        *payment.MethodDetails
		wantDefault bool
		wantCount   int
	}{
		{
			name: "no provider method id", sources: []string{"spec/services/payment_methods/find_or_create_from_provider_service_spec.rb:27"},
			wantCount: 0,
		},
		{
			name: "created", id: "pm_123456",
			sources: []string{
				"spec/services/payment_methods/find_or_create_from_provider_service_spec.rb:37, 41",
				"spec/services/payment_methods/create_from_provider_service_spec.rb:43, 55, 115",
			},
			want: &payment.MethodDetails{Type: "card"}, wantDefault: true, wantCount: 1,
		},
		{
			name: "first method type of the connection", id: "pm_123456", types: []string{"sepa_debit", "card"},
			sources: []string{
				"spec/services/payment_methods/find_or_create_from_provider_service_spec.rb:55",
				"spec/services/payment_methods/create_from_provider_service_spec.rb:102",
			},
			want: &payment.MethodDetails{Type: "sepa_debit"}, wantDefault: true, wantCount: 1,
		},
		{
			name: "with details", id: "pm_123456", details: card,
			sources: []string{"spec/services/payment_methods/create_from_provider_service_spec.rb:79"},
			want:    &card, wantDefault: true, wantCount: 1,
		},
		{
			name: "existing", id: "pm_123456", existing: new(false), details: card,
			sources: []string{"spec/services/payment_methods/find_or_create_from_provider_service_spec.rb:74, 78, 85"},
			want:    &payment.MethodDetails{}, wantDefault: false, wantCount: 1,
		},
		{
			name: "created as the default", id: "pm_123456", setDefault: true,
			sources: []string{"spec/services/payment_methods/find_or_create_from_provider_service_spec.rb:114"},
			want:    &payment.MethodDetails{Type: "card"}, wantDefault: true, wantCount: 1,
		},
		{
			name: "existing made the default", id: "pm_123456", existing: new(false), setDefault: true,
			sources: []string{"spec/services/payment_methods/find_or_create_from_provider_service_spec.rb:132"},
			want:    &payment.MethodDetails{}, wantDefault: true, wantCount: 1,
		},
		{
			name: "existing default", id: "pm_123456", existing: new(true), setDefault: true,
			sources: []string{"spec/services/payment_methods/find_or_create_from_provider_service_spec.rb:161"},
			want:    &payment.MethodDetails{}, wantDefault: true, wantCount: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range tt.sources {
				t.Log("source: " + s)
			}
			h, svc := newHarness(t)
			c := h.Customer()
			pc := h.PaymentConnection(c, h.Integration("stripe", integration.ProviderStripe), true)
			if tt.types != nil {
				pc.ProviderPaymentMethods = tt.types
				h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Update(ctx, pc) })
			}
			var existing *payment.Method
			if tt.existing != nil {
				existing = providerMethod(h, pc, tt.id, *tt.existing, payment.MethodDetails{})
			}
			got, err := svc.FindOrCreateMethod(h.Ctx, pc, tt.id, tt.details, tt.setDefault)
			if err != nil {
				t.Fatalf("FindOrCreateMethod: %v", err)
			}
			if n := len(customerMethods(h, c)); n != tt.wantCount {
				t.Errorf("methods: got = %d, want = %d", n, tt.wantCount)
			}
			if tt.want == nil {
				if got != nil {
					t.Errorf("got = %+v, want = nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got = nil, want = a method")
			}
			if existing != nil && got.ID != existing.ID {
				t.Errorf("ID: got = %s, want = the existing %s", got.ID, existing.ID)
			}
			stored := method(h, got.ID)
			if stored.CustomerID != c.ID || stored.PaymentProviderCustomerID != pc.ID || stored.IntegrationID != pc.IntegrationID || stored.ProviderMethodID != tt.id {
				t.Errorf("got = %+v, want = method %s of connection %s", stored, tt.id, pc.ID)
			}
			if stored.Details != *tt.want || stored.IsDefault != tt.wantDefault {
				t.Errorf("got = %+v default %t, want = %+v default %t", stored.Details, stored.IsDefault, *tt.want, tt.wantDefault)
			}
			if tt.existing != nil && *tt.existing && !stored.UpdatedAt.Equal(existing.UpdatedAt) {
				t.Errorf("updated at: got = %v, want = unchanged %v", stored.UpdatedAt, existing.UpdatedAt)
			}
		})
	}
}

func TestUpdateMethodDetails(t *testing.T) {
	for _, tt := range []struct {
		name     string
		source   string
		missing  bool
		existing payment.MethodDetails
		in       payment.MethodDetails
		want     payment.MethodDetails
	}{
		{
			name: "unknown method", source: "spec/services/payment_methods/update_details_service_spec.rb:17",
			missing: true, in: payment.MethodDetails{Brand: "visa"},
		},
		{
			name: "inserted", source: "spec/services/payment_methods/update_details_service_spec.rb:29",
			in:   payment.MethodDetails{Brand: "visa", Last4: "4242"},
			want: payment.MethodDetails{Brand: "visa", Last4: "4242"},
		},
		{
			name: "existing kept", source: "spec/services/payment_methods/update_details_service_spec.rb:42",
			existing: payment.MethodDetails{Type: "card"},
			in:       payment.MethodDetails{Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2025},
			want:     payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2025},
		},
		{
			name: "replaced", source: "spec/services/payment_methods/update_details_service_spec.rb:58",
			existing: payment.MethodDetails{Type: "card", Brand: "mastercard", Last4: "1111"},
			in:       payment.MethodDetails{Brand: "visa"},
			want:     payment.MethodDetails{Type: "card", Brand: "visa", Last4: "1111"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			h, svc := newHarness(t)
			c := h.Customer()
			pc := h.PaymentConnection(c, h.Integration("moneyhash", integration.ProviderMoneyhash), true)
			var m *payment.Method
			if !tt.missing {
				m = providerMethod(h, pc, "pm_1", true, tt.existing)
			}
			if err := svc.UpdateMethodDetails(h.Ctx, pc, "pm_1", tt.in); err != nil {
				t.Fatalf("UpdateMethodDetails: %v", err)
			}
			if tt.missing {
				if n := len(customerMethods(h, c)); n != 0 {
					t.Errorf("methods: got = %d, want = 0", n)
				}
				return
			}
			if got := method(h, m.ID).Details; got != tt.want {
				t.Errorf("got = %+v, want = %+v", got, tt.want)
			}
		})
	}
}
