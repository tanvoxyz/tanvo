package invoicing_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// countEmitted counts the webhooks of type e the fixture emitted.
func (f *fixture) countEmitted(e webhook.EventType) int {
	n := 0
	for _, w := range f.Webhooks() {
		if w == e {
			n++
		}
	}
	return n
}

// Pulling the taxes of an open invoice finalizes it as finalizing it
// directly does: it is numbered and announced, its document is rendered
// and its payment collected.
func TestPullTaxesFinalizes(t *testing.T) {
	f := loggedFixture(t)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	inv := f.billOne(f.subscribe())
	if f.emitted(webhook.EventTypeInvoiceCreated) || f.enqueued(kernel.JobGenerateInvoiceDocument) != 0 {
		t.Fatalf("before the pull: webhooks = %v, jobs = %v, want = no announcement", f.Webhooks(), f.JobKinds())
	}
	f.Taxes.Compute = providerTaxes("0.1")
	got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:215
	if got.Number != "ORG-001-001" || got.SequentialID == nil {
		t.Errorf("number: got = %q, want = ORG-001-001", got.Number)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:225
	checkAmounts(t, f.Invoice(inv.ID), 1000, 100, 1100)
	// The accounting sync follows invoice.created (integrations.Triggers).
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:242
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:246
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:258
	if f.countEmitted(webhook.EventTypeInvoiceCreated) != 1 || f.emitted(webhook.EventTypeInvoiceReadyToFinalize) {
		t.Errorf("webhooks: got = %v, want = one invoice.created and no invoice.ready_to_finalize", f.Webhooks())
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:252
	f.WantActivities(f.invoiceLog(auditlog.ActivityInvoiceCreated, inv.ID))
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:302
	if f.enqueued(kernel.JobGenerateInvoiceDocument) != 1 || f.enqueued(kernel.JobCreateInvoicePayment) != 1 {
		t.Errorf("jobs: got = %v, want = the document and the payment", f.JobKinds())
	}

	// A second pull, as a concurrent job's, announces nothing again.
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:149
	if _, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("pull taxes again: %v", err)
	}
	if n := f.countEmitted(webhook.EventTypeInvoiceCreated); n != 1 {
		t.Errorf("invoice.created: got = %d, want = 1", n)
	}

	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:107
	_, err = f.svc.PullTaxes(f.Ctx, f.OrgID(), core.NewID[core.InvoiceID](f.IDs))
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown invoice: got = %v, want = not found", err)
	}
}

// Pulling the taxes applies the customer's credit notes once the totals
// are known.
//
// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:325
func TestPullTaxesAppliesCreditNotes(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	prior := f.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
	cn := f.StoredCreditNote(prior, 10)
	inv := f.billOne(f.subscribe())
	f.Taxes.Compute = providerTaxes("0.1")
	if _, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	got := f.Invoice(inv.ID)
	if got.FeesAmount != 1000 || got.TaxesAmount != 100 || got.TotalAmount != 1090 {
		t.Errorf("fees, taxes, total: got = %d, %d, %d, want = 1000, 100, 1090", got.FeesAmount, got.TaxesAmount, got.TotalAmount)
	}
	credits := f.InvoiceCredits(inv.ID)
	if len(credits) != 1 || credits[0].CreditNoteID != cn.ID || credits[0].Amount != 10 {
		t.Errorf("credits: got = %+v, want = 10 of the credit note", credits)
	}
}

// Pulling the taxes of a non-recurring invoice issues it on the day the
// taxes arrive, keep_anchor notwithstanding.
//
// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:171
func TestPullTaxesRedatesNonRecurring(t *testing.T) {
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.IssuingDateAdjustment = core.IssuingDateAdjustmentKeepAnchor })
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	inv := f.billOne(f.subscribe())
	f.Do(func(ctx context.Context, r port.Repos) error {
		iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, f.OrgID(), inv.ID)
		if err != nil {
			return err
		}
		if err := r.InvoiceSubscriptions.DeleteByInvoice(ctx, f.OrgID(), inv.ID); err != nil {
			return err
		}
		for _, is := range iss {
			is.Recurring = false
			if err := r.InvoiceSubscriptions.Create(ctx, is); err != nil {
				return err
			}
		}
		return nil
	})
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation() error = %v", err)
	}
	f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.Timezone = ny })
	f.Clock.Set(billingtest.Date(2026, time.February, 4).Add(2 * time.Hour))
	f.Taxes.Compute = providerTaxes("0.1")
	if _, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	got := f.Invoice(inv.ID)
	// 02:00 UTC on 4 February is 3 February in New York.
	want := core.NewDate(2026, time.February, 3)
	if got.IssuingDate != want || got.PaymentDueDate != want {
		t.Errorf("issuing, due dates: got = %v, %v, want = %v", got.IssuingDate, got.PaymentDueDate, want)
	}
}

// A draft whose taxes the provider refuses stays a draft with its taxes
// failed, its tax error replaced by the new one, and is announced ready
// to finalize.
func TestPullTaxesDraftFailure(t *testing.T) {
	f := loggedFixture(t)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	inv := f.billOne(f.subscribe())
	if inv.Status != invoice.StatusDraft || inv.TaxStatus != invoice.TaxStatusPending {
		t.Fatalf("status, tax status = %s, %s, want = draft, pending", inv.Status, inv.TaxStatus)
	}
	details := func() []*invoice.ErrorDetail {
		return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.ErrorDetail, error) {
			return r.ErrorDetails.ListByInvoice(ctx, f.OrgID(), inv.ID)
		})
	}
	f.Taxes.Fail = true
	if _, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	before := details()
	// The draft waits for its taxes again, its tax error left.
	f.Do(func(ctx context.Context, r port.Repos) error {
		got, err := r.Invoices.Get(ctx, f.OrgID(), inv.ID)
		if err != nil {
			return err
		}
		got.TaxStatus = invoice.TaxStatusPending
		return r.Invoices.Update(ctx, got)
	})
	ready := f.countEmitted(webhook.EventTypeInvoiceReadyToFinalize)
	logs := len(f.Activities(auditlog.ActivityInvoiceReadyToFinalize))
	got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:614
	if got.Status != invoice.StatusDraft || got.TaxStatus != invoice.TaxStatusFailed {
		t.Errorf("status, tax status = %s, %s, want = draft, failed", got.Status, got.TaxStatus)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:622
	after := details()
	if len(before) != 1 || len(after) != 1 || after[0].ID == before[0].ID {
		t.Errorf("error details: got = %d then %d, want = one replaced by another", len(before), len(after))
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:636
	if n := f.countEmitted(webhook.EventTypeInvoiceReadyToFinalize); n != ready+1 {
		t.Errorf("invoice.ready_to_finalize: got = %d, want = %d", n, ready+1)
	}
	// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:642
	if n := len(f.Activities(auditlog.ActivityInvoiceReadyToFinalize)); n != logs+1 {
		t.Errorf("invoice.ready_to_finalize activities: got = %d, want = %d", n, logs+1)
	}
}

// A one-off invoice keeps the customer's credit notes when its taxes are
// pulled.
//
// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:344
func TestPullTaxesOneOffKeepsCreditNotes(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	prior := f.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
	f.StoredCreditNote(prior, 10)
	inv := f.billOne(f.subscribe())
	f.Do(func(ctx context.Context, r port.Repos) error {
		got, err := r.Invoices.Get(ctx, f.OrgID(), inv.ID)
		if err != nil {
			return err
		}
		got.Type = invoice.TypeOneOff
		return r.Invoices.Update(ctx, got)
	})
	f.Taxes.Compute = providerTaxes("0.1")
	if _, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	checkAmounts(t, f.Invoice(inv.ID), 1000, 100, 1100)
	if credits := f.InvoiceCredits(inv.ID); len(credits) != 0 {
		t.Errorf("credits: got = %d, want = 0", len(credits))
	}
}

// An invoice with no amount is finalized with no tax due, keeping the
// rate of the provider's jurisdiction.
//
// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:529
func TestPullTaxesNoAmount(t *testing.T) {
	f := newFixture(t, billingDay, func(p *catalog.Plan) { p.Amount = 0 })
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	inv := f.billOne(f.subscribe())
	if inv.TaxStatus != invoice.TaxStatusPending {
		t.Fatalf("tax status = %s, want = pending", inv.TaxStatus)
	}
	f.Taxes.Compute = providerTaxes("0.1")
	got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	if got.TaxStatus != invoice.TaxStatusSucceeded {
		t.Errorf("tax status: got = %s, want = succeeded", got.TaxStatus)
	}
	got = f.Invoice(inv.ID)
	checkAmounts(t, got, 0, 0, 0)
	if got.TaxesRate.Cmp(money.MustParse("10")) != 0 {
		t.Errorf("taxes rate: got = %s, want = 10", got.TaxesRate)
	}
	applied := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.AppliedTax, error) {
		return r.InvoiceAppliedTaxes.ListByInvoice(ctx, f.OrgID(), inv.ID)
	})
	if len(applied) != 1 {
		t.Errorf("applied taxes: got = %d, want = 1", len(applied))
	}
}

// A failed pull of an open invoice announces nothing ready to finalize.
//
// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:565
func TestPullTaxesFailureNotReady(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Fail = true
	inv := f.billOne(f.subscribe())
	got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	if got.Status != invoice.StatusFailed || f.emitted(webhook.EventTypeInvoiceReadyToFinalize) {
		t.Errorf("status, webhooks: got = %s, %v, want = failed without invoice.ready_to_finalize", got.Status, f.Webhooks())
	}
}

// A zero-amount fee the provider is not asked about stays untaxed while
// the rest of the invoice is taxed.
//
// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:479
func TestPullTaxesZeroFee(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	s := f.subscribe()
	inv := f.billOne(s)
	zero := f.unattachedFee(s, func(fe *invoice.Fee) {
		fe.InvoiceID, fe.Type, fe.Amount, fe.PreciseAmount, fe.PayInAdvance = inv.ID, core.FeeTypeSubscription, 0, money.Zero, false
	})
	taxed := providerTaxes("0.1")
	// The integrations leave zero-amount fees out of the request
	// (integrations.TestComputeTaxesFeeSelection).
	f.Taxes.Compute = func(req kernel.TaxRequest) ([]tax.ProviderTaxResult, error) {
		req.Fees = slices.DeleteFunc(slices.Clone(req.Fees), func(fe *invoice.Fee) bool { return fe.Amount == 0 })
		return taxed(req)
	}
	got, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("pull taxes: %v", err)
	}
	checkAmounts(t, f.Invoice(inv.ID), 1000, 100, 1100)
	if got.TaxesRate.Cmp(money.MustParse("10")) != 0 {
		t.Errorf("taxes rate: got = %s, want = 10", got.TaxesRate)
	}
	fe := f.feeOf(inv.ID, func(fe *invoice.Fee) bool { return fe.ID == zero.ID })
	if fe.TaxesAmount != 0 || !fe.TaxesRate.IsZero() {
		t.Errorf("zero fee taxes: got = %d at %s, want = 0 at 0", fe.TaxesAmount, fe.TaxesRate)
	}
}

// Voiding the provider taxes of an unknown invoice fails.
//
// source: spec/services/invoices/provider_taxes/void_service_spec.rb:109
func TestVoidProviderTaxesUnknownInvoice(t *testing.T) {
	f := newFixture(t, billingDay)
	err := f.svc.VoidProviderTaxes(f.Ctx, f.OrgID(), core.NewID[core.InvoiceID](f.IDs))
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("error: got = %v, want = not found", err)
	}
}
