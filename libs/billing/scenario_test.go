package billing_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// scenario is the end-to-end harness of the ported lago spec/scenarios: a
// billing engine over the billingtest harness, with a job runner standing
// in for the workers and the clock jobs of lago's ScenariosHelper.
type scenario struct {
	*billingtest.Harness
	// E is the engine under test.
	E *billing.Engine
	// Unhandled are the jobs of other libraries (payments, documents) the
	// runner drained without running, in order.
	Unhandled []port.Job
	// PaysCredits stands in for a payment provider whose payments succeed
	// (lago specs stubbing a succeeded Stripe payment intent): a payment
	// requested for a credit invoice marks it paid and runs
	// Wallets.HandleCreditInvoicePayment (lago Invoices::PrepaidCreditJob).
	PaysCredits bool

	future []port.Job
}

// newScenario returns a scenario whose clock reads now.
func newScenario(t *testing.T, now time.Time) *scenario {
	t.Helper()
	h := billingtest.New(t, now)
	e, err := billing.New(*h.Deps)
	if err != nil {
		t.Fatalf("billing.New() error = %v", err)
	}
	return &scenario{Harness: h, E: e}
}

// travelTo moves the clock to at (lago travel_to); it never runs jobs.
func (s *scenario) travelTo(at time.Time) { s.Clock.Set(at) }

// must fails the test on err.
func (s *scenario) must(err error) {
	s.T.Helper()
	if err != nil {
		s.T.Fatalf("error = %v", err)
	}
}

// drain runs every due enqueued job, and those they enqueue, until none
// is left (lago perform_all_enqueued_jobs). Jobs delayed past now wait
// for a later drain; jobs of other libraries are kept in Unhandled.
func (s *scenario) drain() {
	s.T.Helper()
	for range 1000 {
		jobs := append(s.future, s.Store.TakeJobs()...)
		s.future = nil
		now := s.Now()
		ran := false
		for _, j := range jobs {
			if j.RunAt.After(now) {
				s.future = append(s.future, j)
				continue
			}
			ok, err := s.E.HandleJob(s.Ctx, j)
			if err != nil {
				s.T.Fatalf("job %s: %v", j.Kind, err)
			}
			if !ok {
				s.Unhandled = append(s.Unhandled, j)
				if s.PaysCredits && j.Kind == kernel.JobCreateInvoicePayment {
					s.payCredit(j)
				}
			}
			ran = true
		}
		if !ran {
			return
		}
	}
	s.T.Fatalf("jobs never settle")
}

// unhandledKinds returns the kinds of the unhandled jobs.
func (s *scenario) unhandledKinds() []string {
	var out []string
	for _, j := range s.Unhandled {
		out = append(out, j.Kind)
	}
	return out
}

// hasUnhandled reports whether a job of kind was drained without running.
func (s *scenario) hasUnhandled(kind string) bool {
	return slices.Contains(s.unhandledKinds(), kind)
}

// run handles a job of kind with args now, then drains.
func (s *scenario) run(kind string, args any) {
	s.T.Helper()
	raw, err := json.Marshal(args)
	s.must(err)
	ok, err := s.E.HandleJob(s.Ctx, port.Job{Kind: kind, Queue: kernel.QueueOf(kind), OrganizationID: s.OrgID(), Args: raw})
	if !ok {
		s.T.Fatalf("job %s not handled", kind)
	}
	s.must(err)
	s.drain()
}

// clock runs the daily clock jobs at the current instant: activation of
// pending subscriptions, termination of ended ones, end of trials, the
// periodic billing, then drains.
func (s *scenario) clock() {
	s.T.Helper()
	now := kernel.InstantArgs{At: s.Now()}
	s.run(kernel.JobActivatePendingSubscriptions, now)
	s.run(kernel.JobTerminateEndedSubscriptions, now)
	s.run(kernel.JobEndTrials, now)
	s.performBilling()
}

// performBilling runs the periodic billing at the current instant (lago
// perform_billing).
func (s *scenario) performBilling() {
	s.T.Helper()
	s.run(kernel.JobBillSubscriptions, kernel.BillSubscriptionsArgs{BillingAt: s.Now()})
}

// performFinalize finalizes the drafts whose grace period ended (lago
// perform_finalize_refresh).
func (s *scenario) performFinalize() {
	s.T.Helper()
	s.run(kernel.JobFinalizeDueDrafts, kernel.InstantArgs{At: s.Now()})
}

// performWalletRefresh refreshes the ongoing balances of the wallets of
// customer c (lago perform_wallet_refresh).
func (s *scenario) performWalletRefresh(c core.CustomerID) {
	s.T.Helper()
	_, err := s.E.Wallets.RefreshOngoingBalances(s.Ctx, s.OrgID(), c)
	s.must(err)
	s.drain()
}

// ingest stores a usage event of sub at the current instant and runs what
// ingestion triggers in lago: the in-advance charge fees of the event and
// the usage threshold check of its subscription.
func (s *scenario) ingest(sub *subscription.Subscription, code string, props ...string) metering.Event {
	s.T.Helper()
	return s.ingestAt(sub, code, s.Now(), props...)
}

// ingestAt is [scenario.ingest] with the event timestamp ts.
func (s *scenario) ingestAt(sub *subscription.Subscription, code string, ts time.Time, props ...string) metering.Event {
	s.T.Helper()
	e := s.Event(sub, code, ts, props...)
	var p map[string]string
	if len(e.Properties) > 0 {
		p = map[string]string(e.Properties)
	}
	_, err := s.E.Invoices.InAdvanceCharge(s.Ctx, s.OrgID(), kernel.InAdvanceChargeArgs{
		SubscriptionID: sub.ID, TransactionID: e.TransactionID, Timestamp: e.Timestamp, Code: code, Properties: p,
	})
	s.must(err)
	s.drain()
	_, err = s.E.CheckUsageThresholds(s.Ctx, s.OrgID(), sub.ID)
	s.must(err)
	s.drain()
	return e
}

// tx runs fn in a transaction, failing the test on error.
func (s *scenario) tx(fn func(ctx context.Context, r port.Repos) error) {
	s.T.Helper()
	s.Do(fn)
}

// payCredit pays the invoice of a payment job when it is a credit invoice.
func (s *scenario) payCredit(j port.Job) {
	s.T.Helper()
	var a kernel.InvoiceArgs
	s.must(json.Unmarshal(j.Args, &a))
	inv := s.Invoice(a.InvoiceID)
	if inv.Type != invoice.TypeCredit {
		return
	}
	inv.PaymentStatus = core.PaymentStatusSucceeded
	s.SaveInvoice(inv)
	s.must(s.E.Wallets.HandleCreditInvoicePayment(s.Ctx, s.OrgID(), inv.ID, true))
}
