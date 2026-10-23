package invoicing_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// providerTaxes returns a fake provider computation taxing every fee at
// the rate (a fraction).
func providerTaxes(rate string) func(req kernel.TaxRequest) ([]tax.ProviderTaxResult, error) {
	r := money.MustParse(rate)
	return func(req kernel.TaxRequest) ([]tax.ProviderTaxResult, error) {
		out := make([]tax.ProviderTaxResult, len(req.Fees))
		for i, fe := range req.Fees {
			amount := money.NewFromInt(fe.Amount).Sub(fe.PreciseCouponsAmount)
			out[i] = tax.ProviderTaxResult{
				ItemID: fe.ID.String(), AmountCents: amount,
				Breakdown: []tax.ProviderTax{{Name: "State", Type: "state", Rate: r, TaxAmount: amount.Mul(r)}},
			}
		}
		return out, nil
	}
}

// pendingTaxes is a provider computation still in progress.
func pendingTaxes(kernel.TaxRequest) ([]tax.ProviderTaxResult, error) {
	return nil, fmt.Errorf("anrok: %w", kernel.ErrTaxesPending)
}

// The invoice of a provider-taxed customer waits for its taxes: the
// provider is never called inside the billing transaction, but by the
// pull job, outside any transaction.
//
// source: app/services/invoices/compute_taxes_and_totals_service.rb:18
// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:1
func TestBillProviderTaxes(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = providerTaxes("0.1")
	inv := f.billOne(f.subscribe())
	if inv.Status != invoice.StatusOpen || inv.TaxStatus != invoice.TaxStatusPending {
		t.Fatalf("status, tax status = %s, %s, want = open, pending", inv.Status, inv.TaxStatus)
	}
	if got := len(f.Taxes.Requests); got != 0 {
		t.Errorf("provider requests while billing = %d, want = 0", got)
	}
	if f.enqueued(kernel.JobPullProviderTaxes) != 1 {
		t.Errorf("jobs = %v, want = a pull of the taxes", f.JobKinds())
	}
	got, err := f.svc.Retry(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	if got.Status != invoice.StatusFinalized || got.TaxStatus != invoice.TaxStatusSucceeded {
		t.Fatalf("after pull: status, tax status = %s, %s, want = finalized, succeeded", got.Status, got.TaxStatus)
	}
	if got := len(f.Taxes.Requests); got != 1 {
		t.Errorf("provider requests = %d, want = 1", got)
	}
	checkAmounts(t, f.Invoice(inv.ID), 1000, 100, 1100)
}

// source: spec/services/invoices/subscription_service_spec.rb:429
func TestBillPendingProviderTaxes(t *testing.T) {
	tests := []struct {
		name  string
		grace int
		want  invoice.Status
	}{
		// source: spec/services/invoices/subscription_service_spec.rb:437
		{"no grace period", 0, invoice.StatusOpen},
		// source: spec/services/invoices/subscription_service_spec.rb:626
		{"grace period", 3, invoice.StatusDraft},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = tt.grace })
			f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
			f.Taxes.Compute = pendingTaxes
			inv := f.billOne(f.subscribe())
			if inv.Status != tt.want || inv.TaxStatus != invoice.TaxStatusPending {
				t.Fatalf("status, tax status = %s, %s, want = %s, pending", inv.Status, inv.TaxStatus, tt.want)
			}
			if inv.TaxesAmount != 0 || inv.FeesAmount != 1000 {
				t.Errorf("fees, taxes = %d, %d, want = 1000, 0", inv.FeesAmount, inv.TaxesAmount)
			}
			if f.enqueued(kernel.JobPullProviderTaxes) != 1 {
				t.Errorf("jobs = %v, want = a pull of the taxes", f.JobKinds())
			}

			// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:1
			f.Taxes.Compute = providerTaxes("0.1")
			got, err := f.svc.Retry(f.Ctx, f.OrgID(), inv.ID)
			if err != nil {
				t.Fatalf("pull taxes: %v", err)
			}
			want := invoice.StatusFinalized
			if tt.grace > 0 {
				want = invoice.StatusDraft
			}
			if got.Status != want || got.TaxStatus != invoice.TaxStatusSucceeded {
				t.Fatalf("after pull: status, tax status = %s, %s, want = %s, succeeded", got.Status, got.TaxStatus, want)
			}
			checkAmounts(t, f.Invoice(inv.ID), 1000, 100, 1100)
		})
	}
}

// source: spec/services/invoices/retry_service_spec.rb:111
func TestRetryFailedTaxes(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Fail = true
	applied := f.ApplyCoupon(f.FixedAmountCoupon(300), f.cust)
	inv := f.billOne(f.subscribe())
	// The pull job meets the provider failure.
	inv, err := f.svc.Retry(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	if inv.Status != invoice.StatusFailed || inv.TaxStatus != invoice.TaxStatusFailed {
		t.Fatalf("status, tax status = %s, %s, want = failed, failed", inv.Status, inv.TaxStatus)
	}
	if got := f.errorDetails(inv.ID); got != 1 {
		t.Errorf("error details = %d, want = 1", got)
	}

	// source: spec/services/invoices/retry_service_spec.rb:76
	f.Taxes.Fail = false
	f.Taxes.Compute = providerTaxes("0.1")
	got, err := f.svc.Retry(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := len(f.Taxes.Requests); n != 2 {
		t.Errorf("provider requests = %d, want = 2", n)
	}
	if got.Status != invoice.StatusFinalized || got.TaxStatus != invoice.TaxStatusSucceeded {
		t.Fatalf("status, tax status = %s, %s, want = finalized, succeeded", got.Status, got.TaxStatus)
	}
	inv = f.Invoice(inv.ID)
	if inv.CouponsAmount != 300 || inv.TaxesAmount != 70 || inv.TotalAmount != 770 {
		t.Errorf("coupons, taxes, total = %d, %d, %d, want = 300, 70, 770", inv.CouponsAmount, inv.TaxesAmount, inv.TotalAmount)
	}
	if got := f.errorDetails(inv.ID); got != 0 {
		t.Errorf("error details = %d, want = 0", got)
	}
	if a := f.appliedCoupon(applied.ID); a.Status != "terminated" {
		t.Errorf("coupon status = %s, want = terminated", a.Status)
	}
	if _, err := f.svc.Retry(f.Ctx, f.OrgID(), inv.ID); err == nil {
		t.Errorf("retry a finalized invoice: err = nil, want = an error")
	}
}

// errorDetails counts the error details of an invoice.
func (f *fixture) errorDetails(id core.InvoiceID) int {
	f.T.Helper()
	return len(billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.ErrorDetail, error) {
		return r.ErrorDetails.ListByInvoice(ctx, f.OrgID(), id)
	}))
}

// Pulling the taxes of an open invoice redates it: issued today unless a
// recurring invoice keeps its anchor, and due after the customer's net
// payment term as it is then, while the invoice keeps its snapshot.
//
// source: app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb:43-46,145-158
func TestPullTaxesRedates(t *testing.T) {
	tests := []struct {
		name, source string
		adjustment   core.IssuingDateAdjustment
		wantIssuing  core.Date
		wantDue      core.Date
	}{
		{
			"align with finalization date", "spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:200",
			core.IssuingDateAdjustmentAlignWithFinalizationDate, core.NewDate(2026, time.February, 4), core.NewDate(2026, time.February, 14),
		},
		{
			"keep anchor", "spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:192",
			core.IssuingDateAdjustmentKeepAnchor, core.NewDate(2026, time.February, 1), core.NewDate(2026, time.February, 11),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newFixture(t, billingDay)
			f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.IssuingDateAdjustment = tt.adjustment })
			f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
			f.Taxes.Compute = pendingTaxes
			inv := f.billOne(f.subscribe())
			if inv.Status != invoice.StatusOpen || inv.NetPaymentTermDays != 0 {
				t.Fatalf("status, net payment term = %s, %d, want = open, 0", inv.Status, inv.NetPaymentTermDays)
			}
			f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.NetPaymentTermDays = new(10) })
			f.Clock.Set(billingtest.Date(2026, time.February, 4))
			f.Taxes.Compute = providerTaxes("0.1")
			if _, err := f.svc.Retry(f.Ctx, f.OrgID(), inv.ID); err != nil {
				t.Fatalf("pull taxes: %v", err)
			}
			got := f.Invoice(inv.ID)
			if got.Status != invoice.StatusFinalized {
				t.Fatalf("status: got = %s, want = finalized", got.Status)
			}
			if got.IssuingDate != tt.wantIssuing || got.PaymentDueDate != tt.wantDue {
				t.Errorf("issuing, due dates: got = %v, %v, want = %v, %v", got.IssuingDate, got.PaymentDueDate, tt.wantIssuing, tt.wantDue)
			}
			if got.NetPaymentTermDays != 0 {
				t.Errorf("net payment term snapshot: got = %d, want = 0", got.NetPaymentTermDays)
			}
		})
	}
}
