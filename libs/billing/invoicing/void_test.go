package invoicing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// source: spec/services/invoices/void_service_spec.rb:64
func TestVoid(t *testing.T) {
	f := newFixture(t, billingDay)
	inv := f.billOne(f.subscribe())
	f.Store.TakeJobs()
	got, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{})
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if got.Status != invoice.StatusVoided || f.Invoice(inv.ID).Status != invoice.StatusVoided {
		t.Fatalf("status = %s, want = voided", got.Status)
	}
	if !f.emitted(webhook.EventTypeInvoiceVoided) {
		t.Errorf("webhooks = %v, want = invoice.voided", f.Webhooks())
	}
	// source: spec/services/invoices/void_service_spec.rb:76
	if f.enqueued(kernel.JobVoidProviderTaxes) != 1 {
		t.Errorf("jobs = %v, want = a provider void", f.JobKinds())
	}
	// source: spec/services/invoices/void_service_spec.rb:86
	if f.enqueued(kernel.JobRefreshLifetimeUsage) != 1 {
		t.Errorf("jobs = %v, want = a lifetime usage refresh", f.JobKinds())
	}
}

// source: spec/services/invoices/void_service_spec.rb:24
func TestVoidNotVoidable(t *testing.T) {
	tests := []struct {
		name  string
		grace int
		twice bool
	}{
		// source: spec/services/invoices/void_service_spec.rb:24
		{"draft", 3, false},
		// source: spec/services/invoices/void_service_spec.rb:36
		{"voided", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = tt.grace })
			inv := f.billOne(f.subscribe())
			if tt.twice {
				if _, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{}); err != nil {
					t.Fatalf("void: %v", err)
				}
			}
			var re *core.RuleError
			if _, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{}); !errors.As(err, &re) {
				t.Fatalf("err = %v, want = a rule error", err)
			}
		})
	}
}

// source: spec/services/invoices/void_service_spec.rb:144
func TestVoidRestoresCoupon(t *testing.T) {
	f := newFixture(t, billingDay)
	applied := f.ApplyCoupon(f.FixedAmountCoupon(300), f.cust)
	inv := f.billOne(f.subscribe())
	if inv.CouponsAmount != 300 || inv.TotalAmount != 700 {
		t.Fatalf("coupons, total = %d, %d, want = 300, 700", inv.CouponsAmount, inv.TotalAmount)
	}
	if a := f.appliedCoupon(applied.ID); a.Status != coupon.StatusTerminated {
		t.Fatalf("coupon status = %s, want = terminated", a.Status)
	}
	if _, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{}); err != nil {
		t.Fatalf("void: %v", err)
	}
	if a := f.appliedCoupon(applied.ID); a.Status != coupon.StatusActive {
		t.Errorf("coupon status = %s, want = active", a.Status)
	}
}

// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:54
func TestRegenerateFromVoided(t *testing.T) {
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 30 })
	f.taxCustomer("20")
	draft := f.billOne(f.subscribe(func(s *subscription.Subscription) { s.PurchaseOrderNumber = new("PO-ORIGINAL") }))
	if _, err := f.svc.FinalizeDraft(f.Ctx, f.OrgID(), draft.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if _, err := f.svc.Void(f.Ctx, f.OrgID(), draft.ID, invoicing.VoidInput{}); err != nil {
		t.Fatalf("void: %v", err)
	}
	inv, err := f.svc.Regenerate(f.Ctx, f.OrgID(), draft.ID, invoicing.RegenerateInput{})
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:307
	if inv.Status != invoice.StatusFinalized || inv.VoidedInvoiceID != draft.ID {
		t.Fatalf("status, voided = %s, %s, want = finalized, %s", inv.Status, inv.VoidedInvoiceID, draft.ID)
	}
	checkAmounts(t, f.Invoice(inv.ID), 1000, 200, 1200)
	// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:100
	if got := f.Invoice(inv.ID).PurchaseOrderNumber; got == nil || *got != "PO-ORIGINAL" {
		t.Errorf("purchase_order_number: got = %v, want = PO-ORIGINAL", got)
	}
	if got := len(f.Fees(inv.ID)); got != 1 {
		t.Errorf("fees = %d, want = 1", got)
	}
	var re *core.RuleError
	if _, err := f.svc.Regenerate(f.Ctx, f.OrgID(), inv.ID, invoicing.RegenerateInput{}); !errors.As(err, &re) {
		t.Errorf("regenerate a finalized invoice: err = %v, want = a rule error", err)
	}
}

// source: spec/services/invoices/provider_taxes/void_service_spec.rb:1
func TestVoidProviderTaxes(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = providerTaxes("0.1")
	inv := f.billOne(f.subscribe())
	if _, err := f.svc.Retry(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	if _, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{}); err != nil {
		t.Fatalf("void: %v", err)
	}
	if err := f.svc.VoidProviderTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("void provider taxes: %v", err)
	}
	if len(f.Taxes.Voided) != 1 || f.Taxes.Voided[0] != inv.ID {
		t.Errorf("voided = %v, want = [%s]", f.Taxes.Voided, inv.ID)
	}
}

// appliedCoupon reloads an applied coupon.
func (f *fixture) appliedCoupon(id core.AppliedCouponID) *coupon.AppliedCoupon {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*coupon.AppliedCoupon, error) {
		return r.AppliedCoupons.Get(ctx, f.OrgID(), id)
	})
}
