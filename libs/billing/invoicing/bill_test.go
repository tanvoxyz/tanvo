package invoicing_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// source: spec/services/invoices/subscription_service_spec.rb:73
func TestBillPeriodicFinalized(t *testing.T) {
	f := newFixture(t, billingDay)
	f.taxCustomer("20")
	sub := f.subscribe()
	inv := f.billOne(sub)

	if inv.Status != invoice.StatusFinalized {
		t.Fatalf("status = %s, want = finalized", inv.Status)
	}
	checkAmounts(t, inv, 1000, 200, 1200)
	if inv.Type != invoice.TypeSubscription || inv.PaymentStatus != core.PaymentStatusPending {
		t.Errorf("type, payment = %s, %s, want = subscription, pending", inv.Type, inv.PaymentStatus)
	}
	if got, want := inv.IssuingDate, core.DateOf(billingDay, time.UTC); got != want {
		t.Errorf("issuing date = %v, want = %v", got, want)
	}
	if inv.Number != "ORG-001-001" || inv.SequentialID == nil {
		t.Errorf("number = %q, want = ORG-001-001", inv.Number)
	}
	fs := f.Fees(inv.ID)
	if len(fs) != 1 || fs[0].Type != core.FeeTypeSubscription {
		t.Fatalf("fees = %d, want = 1 subscription fee", len(fs))
	}
	iss := f.invoiceSubscriptions(inv.ID)
	if len(iss) != 1 || !iss[0].Recurring || !iss[0].Period.From.Equal(billingtest.Date(2026, time.January, 1)) {
		t.Fatalf("invoice subscriptions = %+v, want = one recurring row from 1 January", iss)
	}
	if !f.emitted(webhook.EventTypeInvoiceCreated) {
		t.Errorf("webhooks = %v, want = invoice.created", f.Webhooks())
	}
	for _, kind := range []string{kernel.JobGenerateInvoiceDocument, kernel.JobCreateInvoicePayment, kernel.JobRefreshLifetimeUsage} {
		if f.enqueued(kind) != 1 {
			t.Errorf("jobs = %v, want = one %s", f.JobKinds(), kind)
		}
	}
}

// source: spec/services/invoices/subscription_service_spec.rb:461
func TestBillPeriodicWithoutActiveSubscription(t *testing.T) {
	f := newFixture(t, billingDay)
	sub := f.subscribe()
	f.UpdateSubscription(sub, func(s *subscription.Subscription) {
		end := billingtest.Date(2026, time.January, 20)
		s.Status, s.TerminatedAt = subscription.StatusTerminated, &end
	})
	if invs := f.bill(billingDay, invoice.InvoicingReasonSubscriptionPeriodic, sub); len(invs) != 0 {
		t.Fatalf("invoices = %d, want = 0", len(invs))
	}
}

// source: spec/services/invoices/subscription_service_spec.rb:654
func TestBillPeriodicIdempotent(t *testing.T) {
	f := newFixture(t, billingDay)
	sub := f.subscribe()
	f.billOne(sub)
	if invs := f.bill(billingDay, invoice.InvoicingReasonSubscriptionPeriodic, sub); len(invs) != 0 {
		t.Fatalf("second run invoices = %d, want = 0", len(invs))
	}
	if got := len(f.Invoices(f.cust.ID)); got != 1 {
		t.Fatalf("stored invoices = %d, want = 1", got)
	}
}

// source: spec/services/invoices/subscription_service_spec.rb:552
func TestBillGracePeriodDraft(t *testing.T) {
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
	sub := f.subscribe()
	inv := f.billOne(sub)

	if inv.Status != invoice.StatusDraft {
		t.Fatalf("status = %s, want = draft", inv.Status)
	}
	if !f.emitted(webhook.EventTypeInvoiceDrafted) || f.emitted(webhook.EventTypeInvoiceCreated) {
		t.Errorf("webhooks = %v, want = invoice.drafted only", f.Webhooks())
	}
	for _, kind := range []string{kernel.JobCreateInvoicePayment, kernel.JobRefreshLifetimeUsage, kernel.JobGenerateInvoiceDocument} {
		if f.enqueued(kind) != 0 {
			t.Errorf("jobs = %v, want = no %s", f.JobKinds(), kind)
		}
	}
}

// source: spec/services/invoices/subscription_service_spec.rb:689
func TestBillZeroAmount(t *testing.T) {
	tests := []struct {
		name   string
		amount int64
		grace  int
		want   invoice.Status
	}{
		// source: spec/services/invoices/subscription_service_spec.rb:695
		{"not zero", 1000, 0, invoice.StatusFinalized},
		// source: spec/services/invoices/subscription_service_spec.rb:709
		{"zero", 0, 0, invoice.StatusClosed},
		// source: spec/services/invoices/subscription_service_spec.rb:718
		{"zero with grace period", 0, 30, invoice.StatusDraft},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay, func(p *catalog.Plan) { p.Amount = tt.amount })
			f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = tt.grace })
			f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.FinalizeZeroAmountInvoice = customer.ZeroAmountFinalizationSkip })
			inv := f.billOne(f.subscribe())
			if inv.Status != tt.want {
				t.Fatalf("status = %s, want = %s", inv.Status, tt.want)
			}
		})
	}
}

// source: spec/services/invoices/subscription_service_spec.rb:540
func TestBillIssuingDateInCustomerZone(t *testing.T) {
	ts := billingtest.Date(2022, time.November, 25, 1)
	f := newFixture(t, ts)
	f.UpdateCustomer(f.cust, func(c *customer.Customer) {
		tz, err := time.LoadLocation("America/Los_Angeles")
		if err != nil {
			t.Fatalf("load location: %v", err)
		}
		grace := 3
		c.Timezone, c.InvoiceGracePeriodDays = tz, &grace
	})
	sub := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2022, time.October, 1)))
	invs := f.bill(ts, invoice.InvoicingReasonSubscriptionPeriodic, sub)
	if len(invs) != 1 {
		t.Fatalf("invoices = %d, want = 1", len(invs))
	}
	if got, want := invs[0].IssuingDate, (core.Date{Year: 2022, Month: time.November, Day: 27}); got != want {
		t.Errorf("issuing date = %v, want = %v", got, want)
	}
}

// source: spec/services/invoices/subscription_service_spec.rb:73 (charges)
func TestBillPeriodicChargeFees(t *testing.T) {
	f := newFixture(t, billingDay)
	m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"2"}`)
	sub := f.subscribe()
	f.Event(sub, "api_calls", billingtest.Date(2026, time.January, 10))
	f.Event(sub, "api_calls", billingtest.Date(2026, time.January, 11))
	inv := f.billOne(sub)
	checkAmounts(t, inv, 1400, 0, 1400)
	var charges int
	for _, fe := range f.Fees(inv.ID) {
		if fe.Type == core.FeeTypeCharge {
			charges++
		}
	}
	if charges != 1 {
		t.Errorf("charge fees = %d, want = 1", charges)
	}
}

// A subscription terminated the day after its period ended bills that
// period unless a recurring row already covers it: a progressive billing
// row of the same period does not count.
//
// source: app/services/subscriptions/terminated_dates_service.rb:43-47
func TestBillTerminationIgnoresNonRecurringRows(t *testing.T) {
	jan := billingtest.Date(2026, time.January, 1)
	end := billingDay.Add(10 * time.Hour)
	tests := []struct {
		name     string
		reason   invoice.InvoicingReason
		wantFrom time.Time
	}{
		{"progressive row", invoice.InvoicingReasonProgressiveBilling, jan},
		{"recurring row", invoice.InvoicingReasonSubscriptionPeriodic, billingDay},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, end)
			sub := f.subscribe()
			f.LinkSubscription(f.StoredInvoice(f.cust), sub, tc.reason, jan, billingDay)
			f.UpdateSubscription(sub, func(s *subscription.Subscription) {
				s.Status, s.TerminatedAt = subscription.StatusTerminated, &end
			})
			invs := f.bill(end, invoice.InvoicingReasonSubscriptionTerminating, sub)
			if len(invs) != 1 {
				t.Fatalf("invoices = %d, want = 1", len(invs))
			}
			is := f.invoiceSubscriptions(invs[0].ID)
			if got := is[0].Period.From; !got.Equal(tc.wantFrom) {
				t.Errorf("period from: got = %s, want = %s", got, tc.wantFrom)
			}
		})
	}
}
