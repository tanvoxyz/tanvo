package invoicing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
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
	got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
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
			got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
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

// A failed pull fails the invoice, keeping its coupon; retrying reopens it
// with its taxes pending and queues a pull, which keeps the coupon credit
// and finalizes the invoice.
func TestRetryFailedTaxes(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Fail = true
	applied := f.ApplyCoupon(f.FixedAmountCoupon(300), f.cust)
	inv := f.billOne(f.subscribe())
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:550
	inv, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	if inv.Status != invoice.StatusFailed || inv.TaxStatus != invoice.TaxStatusFailed {
		t.Fatalf("status, tax status = %s, %s, want = failed, failed", inv.Status, inv.TaxStatus)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:557
	if got := f.errorDetails(inv.ID); got != 1 {
		t.Errorf("error details = %d, want = 1", got)
	}
	if inv.CouponsAmount != 300 {
		t.Errorf("coupons = %d, want = 300", inv.CouponsAmount)
	}

	// source: spec/services/invoices/retry_service_spec.rb:105
	// source: spec/services/invoices/retry_service_spec.rb:111
	// The pull is queued once per invoice (lago PullTaxesAndApplyJob is
	// unique until executed).
	got, err := f.svc.Retry(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got.Status != invoice.StatusOpen || got.TaxStatus != invoice.TaxStatusPending {
		t.Fatalf("after retry: status, tax status = %s, %s, want = open, pending", got.Status, got.TaxStatus)
	}
	if n := f.enqueued(kernel.JobPullProviderTaxes); n != 1 {
		t.Errorf("tax pulls = %d, want = 1", n)
	}

	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:158
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:163
	f.Taxes.Fail = false
	f.Taxes.Compute = providerTaxes("0.1")
	got, err = f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
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
	// source: spec/services/invoices/retry_service_spec.rb:76
	_, err = f.svc.Retry(f.Ctx, f.OrgID(), inv.ID)
	wantRule(t, err, invoicing.CodeInvalidStatus)
}

// A tax provider at its API limit leaves the invoice unchanged and fails
// the pull, so its job retries it.
func TestPullTaxesContention(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = func(kernel.TaxRequest) ([]tax.ProviderTaxResult, error) {
		return nil, &kernel.TaxProviderError{Code: "validationError", Message: "You've exceeded your API limit of 10 per second"}
	}
	inv := f.billOne(f.subscribe())
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:577
	_, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	if !errors.Is(err, kernel.ErrTaxContention) || errors.Is(err, kernel.ErrTaxProvider) {
		t.Fatalf("pull taxes error got = %v, want = %v", err, kernel.ErrTaxContention)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:581
	got := f.Invoice(inv.ID)
	if got.Status != invoice.StatusOpen || got.TaxStatus != invoice.TaxStatusPending {
		t.Errorf("status, tax status got = %s, %s, want = open, pending", got.Status, got.TaxStatus)
	}
	if n := f.errorDetails(inv.ID); n != 0 {
		t.Errorf("error details got = %d, want = 0", n)
	}
	if len(f.Taxes.Requests) == 0 {
		t.Errorf("provider requests got = 0, want > 0")
	}
}

// The tax_error detail records the provider's code and, when it has one,
// its message.
//
// source: app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb:164-178
func TestPullTaxesErrorDetail(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"code", &kernel.TaxProviderError{Code: "taxDateTooFarInFuture"}, `{"tax_error":"taxDateTooFarInFuture"}`},
		{
			"message", &kernel.TaxProviderError{Code: "validationError", Message: "Invalid address"},
			`{"tax_error":"validationError","tax_error_message":"Invalid address"}`,
		},
		{"other failure", fmt.Errorf("boom"), `{"tax_error":"internal_error","tax_error_message":"billing: tax provider error: boom"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
			f.Taxes.Compute = func(kernel.TaxRequest) ([]tax.ProviderTaxResult, error) { return nil, tt.err }
			inv := f.billOne(f.subscribe())
			got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
			if err != nil || got.Status != invoice.StatusFailed {
				t.Fatalf("pull taxes got = %v, %v, want = failed, nil", got, err)
			}
			details := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.ErrorDetail, error) {
				return r.ErrorDetails.ListByInvoice(ctx, f.OrgID(), inv.ID)
			})
			if len(details) != 1 || details[0].Code != invoice.ErrorCodeTaxError {
				t.Fatalf("error details got = %v, want = one tax_error", details)
			}
			var gotJSON, wantJSON any
			if err := json.Unmarshal(details[0].Details, &gotJSON); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.want), &wantJSON); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotJSON, wantJSON) {
				t.Errorf("details got = %s, want = %s", details[0].Details, tt.want)
			}
		})
	}
}

// Pulling the taxes keeps the deductions applied when the invoice was
// held, though the customer's coupons changed meanwhile, and computes only
// its taxes and totals, then its credit notes.
//
// source: app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb:48-51, app/services/invoices/compute_amounts_from_fees.rb:28-31
func TestPullTaxesKeepsDeductions(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	first := f.ApplyCoupon(f.FixedAmountCoupon(300), f.cust)
	inv := f.billOne(f.subscribe())
	if inv.TaxStatus != invoice.TaxStatusPending || inv.CouponsAmount != 300 {
		t.Fatalf("tax status, coupons = %s, %d, want = pending, 300", inv.TaxStatus, inv.CouponsAmount)
	}
	second := f.ApplyCoupon(f.FixedAmountCoupon(500), f.cust)
	f.Taxes.Compute = providerTaxes("0.1")
	if _, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	inv = f.Invoice(inv.ID)
	if inv.Status != invoice.StatusFinalized || inv.CouponsAmount != 300 {
		t.Fatalf("status, coupons = %s, %d, want = finalized, 300", inv.Status, inv.CouponsAmount)
	}
	checkAmounts(t, inv, 1000, 70, 770)
	credits := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Credit, error) {
		return r.Credits.ListByInvoice(ctx, f.OrgID(), inv.ID)
	})
	if len(credits) != 1 || credits[0].AppliedCouponID != first.ID || credits[0].Amount != 300 {
		t.Errorf("credits = %+v, want = the 300 credit of the first coupon", credits)
	}
	if a := f.appliedCoupon(second.ID); a.Status != coupon.StatusActive {
		t.Errorf("second coupon status = %s, want = active", a.Status)
	}
}

// A draft's pulled taxes leave it a draft, announced ready to finalize,
// with no credit note applied and no payment.
//
// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:356
func TestPullTaxesDraft(t *testing.T) {
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	inv := f.billOne(f.subscribe())
	if inv.Status != invoice.StatusDraft || inv.TaxStatus != invoice.TaxStatusPending {
		t.Fatalf("status, tax status = %s, %s, want = draft, pending", inv.Status, inv.TaxStatus)
	}
	f.Taxes.Compute = providerTaxes("0.1")
	got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:361
	if got.Status != invoice.StatusDraft || got.TaxStatus != invoice.TaxStatusSucceeded {
		t.Fatalf("status, tax status = %s, %s, want = draft, succeeded", got.Status, got.TaxStatus)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:377
	checkAmounts(t, f.Invoice(inv.ID), 1000, 100, 1100)
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:394
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:400
	if !f.emitted(webhook.EventTypeInvoiceReadyToFinalize) || f.emitted(webhook.EventTypeInvoiceCreated) {
		t.Errorf("webhooks = %v, want = invoice.ready_to_finalize and no invoice.created", f.Webhooks())
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:411
	if n := f.enqueued(kernel.JobCreateInvoicePayment); n != 0 {
		t.Errorf("payments = %d, want = 0", n)
	}
}

// Pulling the taxes of an invoice that waits for no taxes leaves it as it
// is; a customer the provider does not tax fails.
func TestPullTaxesSkips(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = providerTaxes("0.1")
	inv := f.billOne(f.subscribe())
	if _, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:126
	before := len(f.Taxes.Requests)
	if got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil || got.Status != invoice.StatusFinalized {
		t.Fatalf("pull a finalized invoice: %v, %v, want = finalized, nil", got, err)
	}
	if n := len(f.Taxes.Requests); n != before {
		t.Errorf("provider requests = %d, want = %d", n, before)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:115
	f.Taxes.Customers = nil
	_, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	wantNotFound(t, err, "integration_customer")
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
			if _, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
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

// Pulling the taxes of an invoice gated by a subscription's payment rule
// asks the provider for draft taxes and only creates the payment; a zero
// total satisfies the rule and activates the subscription.
func TestPullTaxesGated(t *testing.T) {
	tests := []struct {
		name, source string
		units        string
		wantStatus   invoice.Status
		wantPayments int
		wantRule     subscription.ActivationRuleStatus
	}{
		{"payment only", "spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:664", "10", invoice.StatusOpen, 1, subscription.ActivationRuleStatusPending},
		{"zero total", "spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:709", "0", invoice.StatusFinalized, 0, subscription.ActivationRuleStatusSatisfied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newFCFixture(t)
			f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
			f.Taxes.Compute = providerTaxes("0.1")
			s := f.subscribe()
			rule := f.gated(s)
			f.event(s, f.fc, tt.units, fcAt)
			inv := f.Invoice(f.run(s).ID)
			if inv.Status != invoice.StatusOpen || inv.TaxStatus != invoice.TaxStatusPending {
				t.Fatalf("status, tax status = %s, %s, want = open, pending", inv.Status, inv.TaxStatus)
			}
			got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
			if err != nil {
				t.Fatalf("pull taxes: %v", err)
			}
			if got.Status != tt.wantStatus || got.TaxStatus != invoice.TaxStatusSucceeded {
				t.Errorf("status, tax status = %s, %s, want = %s, succeeded", got.Status, got.TaxStatus, tt.wantStatus)
			}
			// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:674
			if n := len(f.Taxes.Requests); n == 0 || !f.Taxes.Requests[n-1].Draft {
				t.Errorf("provider requests = %+v, want = a draft request", f.Taxes.Requests)
			}
			if n := f.enqueued(kernel.JobCreateInvoicePayment); n != tt.wantPayments {
				t.Errorf("payments = %d, want = %d", n, tt.wantPayments)
			}
			if tt.wantStatus == invoice.StatusOpen && f.emitted(webhook.EventTypeInvoiceCreated) {
				t.Errorf("webhooks = %v, want = no invoice.created", f.Webhooks())
			}
			if got := f.ruleStatus(rule.ID, s); got != tt.wantRule {
				t.Errorf("rule = %s, want = %s", got, tt.wantRule)
			}
			// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:715
			if tt.wantRule == subscription.ActivationRuleStatusSatisfied && f.subscriptionStatus(s) != subscription.StatusActive {
				t.Errorf("subscription = %s, want = active", f.subscriptionStatus(s))
			}
		})
	}
}
