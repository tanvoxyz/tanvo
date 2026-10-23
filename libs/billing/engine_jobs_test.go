package billing_test

import (
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func TestEngineHandlesEveryKind(t *testing.T) {
	s := newScenario(t, time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC))
	kinds := s.E.JobKinds()
	for _, k := range kinds {
		t.Run(k, func(t *testing.T) {
			ok, _ := s.E.HandleJob(s.Ctx, port.Job{Kind: k, Queue: kernel.QueueOf(k), OrganizationID: s.OrgID(), Args: []byte(`{}`)})
			if !ok {
				t.Errorf("HandleJob(%s): got = not handled, want = handled", k)
			}
		})
	}
	for _, k := range []string{kernel.JobCreateInvoicePayment, kernel.JobGenerateInvoiceDocument, kernel.JobGenerateCreditNoteDocument} {
		if slices.Contains(kinds, k) {
			t.Errorf("JobKinds() contains %s, which billing does not run", k)
		}
		ok, err := s.E.HandleJob(s.Ctx, port.Job{Kind: k, OrganizationID: s.OrgID(), Args: []byte(`{}`)})
		if ok || err != nil {
			t.Errorf("HandleJob(%s): got = (%v, %v), want = (false, <nil>)", k, ok, err)
		}
	}
	if got, want := len(kinds), len(slices.Compact(slices.Sorted(slices.Values(kinds)))); got != want {
		t.Errorf("JobKinds(): got = %d kinds, want = %d distinct", got, want)
	}
}

func TestEnqueueInAdvanceCharges(t *testing.T) {
	// source: app/jobs/events/pay_in_advance_job.rb:19 (unique per event)
	s := newScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
	c := s.Customer()
	p := s.Plan(period.Monthly, 0)
	m := s.Metric("api", catalog.AggregationTypeCount, "")
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"2"}`, func(c *catalog.Charge) { c.PayInAdvance = true })
	sub := s.Subscription(c, p)
	s.drain()
	e := s.Event(sub, m.Code, s.Now())
	args := kernel.InAdvanceChargeArgs{SubscriptionID: sub.ID, TransactionID: e.TransactionID, Timestamp: e.Timestamp, Code: m.Code}

	tests := []struct {
		name   string
		events []kernel.InAdvanceChargeArgs
		err    bool
	}{
		{"one event", []kernel.InAdvanceChargeArgs{args}, false},
		{"the event again", []kernel.InAdvanceChargeArgs{args}, false},
		{"no events", nil, false},
		{"no subscription", []kernel.InAdvanceChargeArgs{{TransactionID: "tr"}}, true},
	}
	for _, tt := range tests {
		err := s.E.EnqueueInAdvanceCharges(s.Ctx, s.OrgID(), tt.events)
		if (err != nil) != tt.err {
			t.Errorf("%s: EnqueueInAdvanceCharges() error = %v, want error = %v", tt.name, err, tt.err)
		}
	}
	jobs := s.Store.Jobs()
	if got := len(jobs); got != 1 {
		t.Fatalf("jobs: got = %d, want = 1", got)
	}
	if got, want := jobs[0].UniqueKey, kernel.InAdvanceKey(sub.ID, e.TransactionID); got != want {
		t.Errorf("unique key: got = %q, want = %q", got, want)
	}
	s.drain()
	var amount int64
	n := 0
	for _, f := range s.usgFees(sub) {
		if f.Type == core.FeeTypeCharge {
			amount += f.Amount
			n++
		}
	}
	if n != 1 || amount != 200 {
		t.Errorf("in-advance fees: got = %d fees of %d, want = 1 fee of 200", n, amount)
	}
}
