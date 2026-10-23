package invoicing_test

import (
	"context"
	"strings"
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
	"github.com/tanvoxyz/tanvo/libs/money"
)

// draftFixture bills, with a grace period of 3 days, a draft of the plan
// and a standard charge of 2.00 per call with one call in January.
func draftFixture(t *testing.T) (*fixture, *subscription.Subscription, *invoice.Invoice) {
	t.Helper()
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
	m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"2"}`)
	sub := f.subscribe()
	f.Event(sub, "api_calls", billingtest.Date(2026, time.January, 10))
	inv := f.billOne(sub)
	if inv.Status != invoice.StatusDraft {
		t.Fatalf("status = %s, want = draft", inv.Status)
	}
	checkAmounts(t, inv, 1200, 0, 1200)
	return f, sub, inv
}

// source: spec/services/invoices/refresh_draft_and_finalize_service_spec.rb:1
func TestFinalizeDraft(t *testing.T) {
	f, _, draft := draftFixture(t)
	f.Clock.Set(billingtest.Date(2026, time.February, 4))
	inv, err := f.svc.FinalizeDraft(f.Ctx, f.OrgID(), draft.ID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if inv.Status != invoice.StatusFinalized || inv.Number == "" {
		t.Fatalf("status, number = %s, %q, want = finalized, a number", inv.Status, inv.Number)
	}
	checkAmounts(t, f.Invoice(inv.ID), 1200, 0, 1200)
	if !f.emitted(webhook.EventTypeInvoiceCreated) {
		t.Errorf("webhooks = %v, want = invoice.created", f.Webhooks())
	}
	if f.enqueued(kernel.JobCreateInvoicePayment) != 1 || f.enqueued(kernel.JobRefreshLifetimeUsage) != 1 {
		t.Errorf("jobs = %v, want = a payment and a lifetime usage refresh", f.JobKinds())
	}
	if got := len(f.Fees(inv.ID)); got != 2 {
		t.Errorf("fees = %d, want = 2", got)
	}
}

// source: spec/services/invoices/finalize_service_spec.rb:49
func TestFinalizeDraftNotDraft(t *testing.T) {
	f := newFixture(t, billingDay)
	inv := f.billOne(f.subscribe())
	got, err := f.svc.FinalizeDraft(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if got.Status != invoice.StatusFinalized || got.Number != inv.Number {
		t.Errorf("status, number = %s, %q, want = finalized, %q", got.Status, got.Number, inv.Number)
	}
}

// source: spec/services/invoices/refresh_draft_service_spec.rb:164
func TestRefreshDraftRegeneratesFees(t *testing.T) {
	f, sub, draft := draftFixture(t)
	f.Event(sub, "api_calls", billingtest.Date(2026, time.January, 20))
	inv, err := f.svc.RefreshDraft(f.Ctx, f.OrgID(), draft.ID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if inv.Status != invoice.StatusDraft {
		t.Fatalf("status = %s, want = draft", inv.Status)
	}
	checkAmounts(t, f.Invoice(inv.ID), 1400, 0, 1400)
	if got := len(f.Fees(inv.ID)); got != 2 {
		t.Errorf("fees = %d, want = 2", got)
	}
	if got := len(f.invoiceSubscriptions(inv.ID)); got != 1 {
		t.Errorf("invoice subscriptions = %d, want = 1", got)
	}
}

// source: spec/services/invoices/refresh_draft_service_spec.rb:209
func TestRefreshDraftUpdatesTaxes(t *testing.T) {
	f, _, draft := draftFixture(t)
	f.taxCustomer("10")
	if _, err := f.svc.RefreshDraft(f.Ctx, f.OrgID(), draft.ID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	inv := f.Invoice(draft.ID)
	checkAmounts(t, inv, 1200, 120, 1320)
	if got := inv.TaxesRate; got.Cmp(money.MustParse("10")) != 0 {
		t.Errorf("taxes rate = %s, want = 10", got)
	}
}

// source: spec/services/invoices/refresh_draft_service_spec.rb:80
func TestRefreshDraftFinalized(t *testing.T) {
	f := newFixture(t, billingDay)
	inv := f.billOne(f.subscribe())
	got, err := f.svc.RefreshDraft(f.Ctx, f.OrgID(), inv.ID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got.Status != invoice.StatusFinalized || len(f.Fees(inv.ID)) != 1 {
		t.Errorf("status = %s, want = finalized and untouched", got.Status)
	}
}

// source: spec/services/invoices/finalize_batch_service_spec.rb:1
func TestFinalizeDueDrafts(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		want int
	}{
		{"inside the grace period", billingtest.Date(2026, time.February, 3, 12), 0},
		{"grace period ended", billingtest.Date(2026, time.February, 4, 1), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := draftFixture(t)
			n, err := f.svc.FinalizeDueDrafts(f.Ctx, f.OrgID(), tt.at)
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			if n != tt.want || f.enqueued(kernel.JobFinalizeDraft) != tt.want {
				t.Fatalf("enqueued = %d (%v), want = %d", n, f.JobKinds(), tt.want)
			}
		})
	}
}

// A draft without an expected finalization date is due on its issuing
// date in its customer's time zone.
//
// source: app/models/invoice.rb:148
func TestFinalizeDueDraftsWithoutExpectedDate(t *testing.T) {
	tests := []struct {
		name   string
		offset time.Duration // from the issuing date's UTC midnight
		want   int
	}{
		{"the day before the issuing date", -12 * time.Hour, 0},
		{"on the issuing date", time.Hour, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, draft := draftFixture(t)
			var issuing core.Date
			f.Do(func(ctx context.Context, r port.Repos) error {
				inv, err := r.Invoices.Get(ctx, f.OrgID(), draft.ID)
				if err != nil {
					return err
				}
				inv.ExpectedFinalizationDate = core.Date{}
				issuing = inv.IssuingDate
				return r.Invoices.Update(ctx, inv)
			})
			at := issuing.Midnight(time.UTC).Add(tt.offset)
			n, err := f.svc.FinalizeDueDrafts(f.Ctx, f.OrgID(), at)
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			if n != tt.want {
				t.Errorf("enqueued = %d, want = %d", n, tt.want)
			}
		})
	}
}

// A draft finalized by the due-drafts sweep is issued on the sweep's
// local date, even when its job runs after midnight; finalizing it
// directly issues it today. Lago redates with the worker's clock
// (app/services/invoices/refresh_draft_and_finalize_service.rb:77).
func TestFinalizeDueDraftsIssuesOnSweepDate(t *testing.T) {
	sweep := billingtest.Date(2026, time.February, 4, 23)
	f, _, draft := draftFixture(t)
	if _, err := f.svc.FinalizeDueDrafts(f.Ctx, f.OrgID(), sweep); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f.Clock.Set(sweep.Add(3 * time.Hour))
	for _, j := range f.Store.TakeJobs() {
		if j.Kind != kernel.JobFinalizeDraft {
			continue
		}
		if _, err := f.svc.HandleJob(f.Ctx, j); err != nil {
			t.Fatalf("finalize job: %v", err)
		}
	}
	inv := f.Invoice(draft.ID)
	if want := core.NewDate(2026, time.February, 4); inv.Status != invoice.StatusFinalized || inv.IssuingDate != want {
		t.Errorf("status, issuing date = %s, %s, want = finalized, %s", inv.Status, inv.IssuingDate, want)
	}
}

// TestFinalizeDueDraftsNumbersOnSweepDate checks that a draft finalized by
// a job running after a month boundary is numbered for the month it is
// issued in: lago numbers and redates with the same clock reading
// (app/models/invoice.rb:738, Time.now for the YYYYMM part;
// app/services/invoices/refresh_draft_and_finalize_service.rb:76 for the
// issuing date), so the two always agree.
func TestFinalizeDueDraftsNumbersOnSweepDate(t *testing.T) {
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) {
		b.InvoiceGracePeriodDays = 27
		b.DocumentNumbering = organization.DocumentNumberingPerBillingEntity
	})
	m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"2"}`)
	sub := f.subscribe()
	f.Event(sub, "api_calls", billingtest.Date(2026, time.January, 10))
	draft := f.billOne(sub)
	if draft.Status != invoice.StatusDraft {
		t.Fatalf("status = %s, want = draft", draft.Status)
	}
	sweep := billingtest.Date(2026, time.February, 28, 23)
	if _, err := f.svc.FinalizeDueDrafts(f.Ctx, f.OrgID(), sweep); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f.Clock.Set(sweep.Add(3 * time.Hour))
	for _, j := range f.Store.TakeJobs() {
		if j.Kind != kernel.JobFinalizeDraft {
			continue
		}
		if _, err := f.svc.HandleJob(f.Ctx, j); err != nil {
			t.Fatalf("finalize job: %v", err)
		}
	}
	inv := f.Invoice(draft.ID)
	if want := core.NewDate(2026, time.February, 28); inv.Status != invoice.StatusFinalized || inv.IssuingDate != want {
		t.Fatalf("status, issuing date = %s, %s, want = finalized, %s", inv.Status, inv.IssuingDate, want)
	}
	if !strings.Contains(inv.Number, "-202602-") {
		t.Errorf("number = %q, want = one of 2026-02", inv.Number)
	}
}
