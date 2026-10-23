package invoicing_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// integration creates an integration of the provider with the code.
func (f *fixture) integration(code string, p integration.Provider) *integration.Integration {
	f.T.Helper()
	cat, _ := integration.CategoryOf(p)
	in := &integration.Integration{
		ID: core.NewID[core.IntegrationID](f.IDs), OrganizationID: f.OrgID(), Code: code, Name: code,
		Category: cat, Provider: p, Settings: json.RawMessage(`{}`),
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.Integrations.Create(ctx, in) })
	return in
}

// source: spec/services/invoices/preview_context_service_spec.rb:120
func TestPreviewRequestNewCustomer(t *testing.T) {
	f := newFixture(t, previewDay)
	f.integration("anrok", integration.ProviderAnrok)
	name := "Mislav M"
	v, err := f.svc.PreviewRequest(f.Ctx, invoicing.PreviewRequest{
		OrganizationID: f.OrgID(),
		Subscription:   &invoicing.PreviewSubscription{Plan: refs.ByID(f.plan.ID), BillingTime: period.Anniversary},
		Customer: &invoicing.PreviewCustomer{
			Name: &name, Currency: money.MustCurrency("USD"),
			BillingAddress: invoicing.PreviewAddress{City: new("Paris"), Country: new("IT")},
			Integrations:   []refs.Ref{refs.ByCode("anrok")},
		},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got, want := v.Invoice.FeesAmount, int64(1000); got != want {
		t.Errorf("fees amount = %d, want = %d", got, want)
	}
	if v.Invoice.CustomerID == f.cust.ID {
		t.Errorf("customer = the fixture's, want = a new one")
	}
	// The customer does not survive the preview.
	stored := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*customer.Customer, error) {
		c, err := r.Customers.Get(ctx, f.OrgID(), v.Invoice.CustomerID)
		if errors.Is(err, core.ErrNotFound) {
			return nil, nil
		}
		return c, err
	})
	if stored != nil {
		t.Errorf("preview customer is stored")
	}
}

// isNotFound reports whether err is the not-found error of resource.
func isNotFound(err error, resource string) bool {
	var nf *core.NotFoundError
	return errors.As(err, &nf) && nf.Resource == resource
}

// hasFieldError reports whether err is a validation error of code on
// field.
func hasFieldError(err error, field, code string) bool {
	var ve *core.ValidationError
	return errors.As(err, &ve) && ve.Has(field, code)
}

func TestPreviewRequestCustomerRejections(t *testing.T) {
	tests := []struct {
		name  string
		req   func(f *fixture) invoicing.PreviewRequest
		check func(error) bool
	}{
		// source: spec/services/invoices/preview/subscriptions_service_spec.rb:51
		{"new customer with subscriptions", func(f *fixture) invoicing.PreviewRequest {
			s := f.started()
			return invoicing.PreviewRequest{OrganizationID: f.OrgID(), Subscriptions: subRefs(s)}
		}, func(err error) bool { return hasFieldError(err, "customer", invoicing.CodeMustBePersisted) }},
		// source: spec/services/invoices/preview_context_service_spec.rb:169
		{"unknown integration", func(f *fixture) invoicing.PreviewRequest {
			return invoicing.PreviewRequest{
				OrganizationID: f.OrgID(), CustomerID: f.cust.ID,
				Subscription: &invoicing.PreviewSubscription{Plan: refs.ByID(f.plan.ID)},
				Customer:     &invoicing.PreviewCustomer{Integrations: []refs.Ref{refs.ByCode("missing")}},
			}
		}, func(err error) bool { return isNotFound(err, "integration") }},
		{"unsupported integration", func(f *fixture) invoicing.PreviewRequest {
			f.integration("stripe", integration.ProviderStripe)
			return invoicing.PreviewRequest{
				OrganizationID: f.OrgID(), CustomerID: f.cust.ID,
				Subscription: &invoicing.PreviewSubscription{Plan: refs.ByID(f.plan.ID)},
				Customer:     &invoicing.PreviewCustomer{Integrations: []refs.Ref{refs.ByCode("stripe")}},
			}
		}, func(err error) bool {
			return hasFieldError(err, "customer.integration_customers[0].integration_id", core.CodeForbidden)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, previewDay)
			_, err := f.svc.PreviewRequest(f.Ctx, tt.req(f))
			if !tt.check(err) {
				t.Errorf("err = %v, want = the %s rejection", err, tt.name)
			}
		})
	}
}

// source: spec/services/invoices/preview_context_service_spec.rb:70
func TestPreviewRequestCustomerOverrides(t *testing.T) {
	f := newFixture(t, previewDay)
	f.integration("anrok", integration.ProviderAnrok)
	before := f.ReloadCustomer(f.cust.ID)
	tin := "123"
	if _, err := f.previewRequest(invoicing.PreviewRequest{
		Subscription: &invoicing.PreviewSubscription{Plan: refs.ByID(f.plan.ID)},
		Customer: &invoicing.PreviewCustomer{
			TaxIdentificationNumber: &tin, Timezone: time.UTC,
			BillingAddress:  invoicing.PreviewAddress{Line1: new("Rue de Tax")},
			ShippingAddress: core.Address{City: "Paris"},
			Integrations:    []refs.Ref{refs.ByCode("anrok")},
		},
	}); err != nil {
		t.Fatalf("preview: %v", err)
	}
	after := f.ReloadCustomer(f.cust.ID)
	if !after.UpdatedAt.Equal(before.UpdatedAt) || after.TaxIdentificationNumber != nil || after.BillingAddress != before.BillingAddress {
		t.Errorf("stored customer = %+v, want = unchanged %+v", after, before)
	}
	links := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*integration.IntegrationCustomer, error) {
		return r.IntegrationCustomers.ListByCustomer(ctx, f.OrgID(), f.cust.ID, integration.CategoryTax)
	})
	if len(links) != 0 {
		t.Errorf("stored integration customers = %d, want = 0", len(links))
	}
}

func TestPreviewRequestCouponAttributes(t *testing.T) {
	rate := money.MustParse("10")
	tests := []struct {
		name  string
		attrs func(f *fixture) []*coupon.Coupon
		want  int64
	}{
		// source: spec/services/invoices/preview_context_service_spec.rb:314
		{"stored coupon of the code", func(f *fixture) []*coupon.Coupon {
			return []*coupon.Coupon{{Code: f.FixedAmountCoupon(50).Code}}
		}, 50},
		// source: spec/services/invoices/preview_context_service_spec.rb:314
		{"percentage built from attributes", func(*fixture) []*coupon.Coupon {
			return []*coupon.Coupon{{Code: "coupon_preview", Name: "coupon_preview", Type: coupon.TypePercentage, PercentageRate: &rate}}
		}, 100},
		{"fixed amount built from attributes", func(*fixture) []*coupon.Coupon {
			return []*coupon.Coupon{{Amount: new(int64(30)), Currency: billingtest.USD}}
		}, 30},
		// An unknown code builds a fixed amount coupon without an amount,
		// which has no effect (lago Coupon.new).
		{"unknown code", func(*fixture) []*coupon.Coupon {
			return []*coupon.Coupon{{Code: "unknown"}}
		}, 0},
		{"another currency", func(*fixture) []*coupon.Coupon {
			return []*coupon.Coupon{{Amount: new(int64(30)), Currency: money.MustCurrency("EUR")}}
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, previewDay)
			v, err := f.previewRequest(invoicing.PreviewRequest{
				Subscription:     &invoicing.PreviewSubscription{Plan: refs.ByID(f.plan.ID), BillingTime: period.Anniversary},
				CouponAttributes: tt.attrs(f),
			})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if got := v.Invoice.CouponsAmount; got != tt.want {
				t.Errorf("coupons amount = %d, want = %d", got, tt.want)
			}
			f.checkNothingStored(t, 0)
			if c := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*coupon.Coupon, error) {
				c, err := r.Coupons.GetByCode(ctx, f.OrgID(), "coupon_preview")
				if errors.Is(err, core.ErrNotFound) {
					return nil, nil
				}
				return c, err
			}); c != nil {
				t.Errorf("stored built coupon = %+v, want = none", c)
			}
		})
	}
}
