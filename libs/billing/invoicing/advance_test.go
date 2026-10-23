package invoicing_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// inAdvance is a fixture with a pay-in-advance standard charge of 3.00
// per call on a subscription started on 1 January.
func inAdvance(t *testing.T, now time.Time, opts ...func(*catalog.Charge)) (*fixture, *subscription.Subscription) {
	t.Helper()
	f := newFixture(t, now)
	m := f.Metric("calls", catalog.AggregationTypeCount, "")
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"3"}`,
		append([]func(*catalog.Charge){func(c *catalog.Charge) { c.PayInAdvance = true }}, opts...)...)
	return f, f.subscribe()
}

// charge bills one event at ts.
func (f *fixture) charge(sub *subscription.Subscription, ts time.Time) []*invoice.Fee {
	f.T.Helper()
	e := f.Event(sub, "calls", ts)
	fs, err := f.svc.InAdvanceCharge(f.Ctx, f.OrgID(), kernel.InAdvanceChargeArgs{
		SubscriptionID: sub.ID, TransactionID: e.TransactionID, Timestamp: ts, Code: "calls",
	})
	if err != nil {
		f.T.Fatalf("in-advance charge: %v", err)
	}
	return fs
}

// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:89
func TestInAdvanceChargeInvoice(t *testing.T) {
	ts := billingtest.Date(2026, time.January, 15, 10)
	f, sub := inAdvance(t, ts)
	f.taxCustomer("20")
	fs := f.charge(sub, ts)
	if len(fs) != 1 || fs[0].InvoiceID.IsZero() {
		t.Fatalf("fees = %d, want = one fee on an invoice", len(fs))
	}
	inv := f.Invoice(fs[0].InvoiceID)
	if inv.Type != invoice.TypeSubscription || inv.Status != invoice.StatusFinalized {
		t.Fatalf("type, status = %s, %s, want = subscription, finalized", inv.Type, inv.Status)
	}
	checkAmounts(t, inv, 300, 60, 360)
	// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:146
	iss := f.invoiceSubscriptions(inv.ID)
	if len(iss) != 1 || iss[0].Reason != invoice.InvoicingReasonInAdvanceCharge {
		t.Errorf("invoice subscriptions = %+v, want = one in_advance_charge row", iss)
	}
	// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:298
	if !f.emitted(webhook.EventTypeInvoiceCreated) || !f.emitted(webhook.EventTypeFeeCreated) {
		t.Errorf("webhooks = %v, want = invoice.created and fee.created", f.Webhooks())
	}
	// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:290
	if f.enqueued(kernel.JobCreateInvoicePayment) != 1 {
		t.Errorf("jobs = %v, want = a payment", f.JobKinds())
	}
}

// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:445
func TestInAdvanceChargeIgnoresGracePeriod(t *testing.T) {
	ts := billingtest.Date(2026, time.January, 15, 10)
	f, sub := inAdvance(t, ts)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
	fs := f.charge(sub, ts)
	inv := f.Invoice(fs[0].InvoiceID)
	if inv.Status != invoice.StatusFinalized || inv.IssuingDate != core.DateOf(ts, time.UTC) {
		t.Fatalf("status, issuing date = %s, %v, want = finalized, %v", inv.Status, inv.IssuingDate, core.DateOf(ts, time.UTC))
	}
}

// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:233
func TestInAdvanceChargeIdempotent(t *testing.T) {
	ts := billingtest.Date(2026, time.January, 15, 10)
	f, sub := inAdvance(t, ts)
	e := f.Event(sub, "calls", ts)
	args := kernel.InAdvanceChargeArgs{SubscriptionID: sub.ID, TransactionID: e.TransactionID, Timestamp: ts, Code: "calls"}
	for i, want := range []int{1, 0} {
		fs, err := f.svc.InAdvanceCharge(f.Ctx, f.OrgID(), args)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if len(fs) != want {
			t.Fatalf("run %d: fees = %d, want = %d", i, len(fs), want)
		}
	}
	if got := len(f.Invoices(f.cust.ID)); got != 1 {
		t.Errorf("invoices = %d, want = 1", got)
	}
}

// source: spec/services/fees/create_pay_in_advance_service_spec.rb:1 (non-invoiceable)
// source: spec/services/invoices/advance_charges_service_spec.rb:201
func TestAdvanceCharges(t *testing.T) {
	ts := billingtest.Date(2026, time.January, 15, 10)
	f, sub := inAdvance(t, ts, func(c *catalog.Charge) {
		c.Invoiceable, c.RegroupPaidFees = false, catalog.RegroupPaidFeesInvoice
	})
	f.taxCustomer("20")
	fs := f.charge(sub, ts)
	if len(fs) != 1 || !fs[0].InvoiceID.IsZero() {
		t.Fatalf("fees = %d, want = one standalone fee", len(fs))
	}
	if fs[0].PaymentStatus != invoice.FeePaymentStatusPending || fs[0].TaxesAmount != 60 {
		t.Fatalf("payment, taxes = %s, %d, want = pending, 60", fs[0].PaymentStatus, fs[0].TaxesAmount)
	}
	if !f.emitted(webhook.EventTypeFeeCreated) || len(f.Invoices(f.cust.ID)) != 0 {
		t.Fatalf("webhooks = %v, want = fee.created and no invoice", f.Webhooks())
	}

	// source: spec/services/invoices/advance_charges_service_spec.rb:153
	if inv := f.advanceCharges(sub, billingDay); inv != nil {
		t.Fatalf("invoice of unpaid fees = %s, want = none", inv.ID)
	}
	paid := ts.Add(time.Hour)
	f.Do(func(ctx context.Context, r port.Repos) error {
		fs[0].PaymentStatus, fs[0].SucceededAt = invoice.FeePaymentStatusSucceeded, &paid
		return r.Fees.Update(ctx, fs[0])
	})
	inv := f.advanceCharges(sub, billingDay)
	if inv == nil {
		t.Fatalf("invoice = none, want = an advance charges invoice")
	}
	inv = f.Invoice(inv.ID)
	if inv.Type != invoice.TypeAdvanceCharges || inv.Status != invoice.StatusFinalized || inv.PaymentStatus != core.PaymentStatusSucceeded {
		t.Fatalf("type, status, payment = %s, %s, %s, want = advance_charges, finalized, succeeded", inv.Type, inv.Status, inv.PaymentStatus)
	}
	checkAmounts(t, inv, 300, 60, 360)
	if f.enqueued(kernel.JobCreateInvoicePayment) != 0 {
		t.Errorf("jobs = %v, want = no payment", f.JobKinds())
	}
	if got := f.advanceCharges(sub, billingDay); got != nil {
		t.Errorf("second run invoice = %s, want = none", got.ID)
	}
}

// advanceCharges runs the advance charges of sub at billingAt.
func (f *fixture) advanceCharges(sub *subscription.Subscription, billingAt time.Time) *invoice.Invoice {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*invoice.Invoice, error) {
		return f.svc.AdvanceCharges(ctx, r, f.OrgID(), []core.SubscriptionID{sub.ID}, billingAt)
	})
}

// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:262
func TestInAdvanceChargePurchaseOrderNumber(t *testing.T) {
	ts := billingtest.Date(2026, time.January, 15, 10)
	f, sub := inAdvance(t, ts)
	f.UpdateSubscription(sub, func(s *subscription.Subscription) { s.PurchaseOrderNumber = new("PO-SUB-123") })
	fs := f.charge(sub, ts)
	if len(fs) != 1 || fs[0].InvoiceID.IsZero() {
		t.Fatalf("fees = %d, want = one fee on an invoice", len(fs))
	}
	if got := f.Invoice(fs[0].InvoiceID).PurchaseOrderNumber; got == nil || *got != "PO-SUB-123" {
		t.Errorf("purchase_order_number: got = %v, want = PO-SUB-123", got)
	}
}
