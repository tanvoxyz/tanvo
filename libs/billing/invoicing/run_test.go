package invoicing_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// source: spec/services/subscriptions/organization_billing_service_spec.rb:1
func TestBillSubscriptions(t *testing.T) {
	tests := []struct {
		name  string
		at    time.Time
		start time.Time
		want  int
	}{
		{"billing day", billingDay, billingtest.Date(2026, time.January, 1), 1},
		{"not a billing day", billingtest.Date(2026, time.February, 2), billingtest.Date(2026, time.January, 1), 0},
		{"started that day", billingDay, billingDay, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.at)
			f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(tt.start))
			f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(tt.start))
			n, err := f.svc.BillSubscriptions(f.Ctx, f.OrgID(), tt.at)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if n != tt.want || f.enqueued(kernel.JobBillSubscription) != tt.want || f.enqueued(kernel.JobBillAdvanceCharges) != tt.want {
				t.Fatalf("groups = %d (%v), want = %d", n, f.JobKinds(), tt.want)
			}
		})
	}
}

// source: spec/services/subscriptions/organization_billing_service_spec.rb:1 (downgrade)
func TestBillSubscriptionsSkipsDowngrades(t *testing.T) {
	f := newFixture(t, billingDay)
	sub := f.subscribe()
	f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2026, time.March, 1)), func(s *subscription.Subscription) {
		s.ExternalID, s.PreviousSubscriptionID = sub.ExternalID, sub.ID
	})
	n, err := f.svc.BillSubscriptions(f.Ctx, f.OrgID(), billingDay)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n != 0 {
		t.Fatalf("groups = %d, want = 0", n)
	}
}

// TestHandleJobs runs the jobs of a billing run through HandleJob.
func TestHandleJobs(t *testing.T) {
	f := newFixture(t, billingDay)
	f.subscribe()
	f.subscribe()
	if _, err := f.svc.BillSubscriptions(f.Ctx, f.OrgID(), billingDay); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, j := range f.Store.TakeJobs() {
		ok, err := f.svc.HandleJob(f.Ctx, j)
		if err != nil {
			t.Fatalf("job %s: %v", j.Kind, err)
		}
		if !ok {
			t.Fatalf("job %s: handled = false, want = true", j.Kind)
		}
	}
	invs := f.Invoices(f.cust.ID)
	if len(invs) != 1 || invs[0].Status != invoice.StatusFinalized {
		t.Fatalf("invoices = %d, want = one finalized invoice", len(invs))
	}
	checkAmounts(t, invs[0], 2000, 0, 2000)
	if ok, _ := f.svc.HandleJob(f.Ctx, f.Store.Jobs()[0]); ok {
		t.Errorf("handled %s, want = not an invoicing job", f.Store.Jobs()[0].Kind)
	}
}

// source: spec/services/invoices/subscription_service_spec.rb:281
func TestBillSubscriptionRetriesGenerating(t *testing.T) {
	f := newFixture(t, billingDay)
	sub := f.subscribe()
	var inv *invoice.Invoice
	f.Do(func(ctx context.Context, r port.Repos) error {
		cenv, err := kernel.LoadCustomerEnv(ctx, r, f.OrgID(), f.cust.ID, core.BillingEntityID{})
		if err != nil {
			return err
		}
		inv = kernel.NewInvoice(f.Deps, cenv, kernel.InvoiceKind{Type: invoice.TypeSubscription, Recurring: true, UsesGracePeriod: true}, f.plan.Currency, billingDay)
		return r.Invoices.Create(ctx, inv)
	})
	got, err := f.svc.BillSubscription(f.Ctx, f.OrgID(), kernel.BillSubscriptionArgs{
		SubscriptionIDs: []core.SubscriptionID{sub.ID}, BillingAt: billingDay,
		Reason: string(invoice.InvoicingReasonSubscriptionPeriodic), InvoiceID: inv.ID,
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(got) != 1 || got[0].ID != inv.ID || got[0].Status != invoice.StatusFinalized {
		t.Fatalf("invoices = %+v, want = the same invoice finalized", got)
	}
	if n := len(f.Fees(inv.ID)); n != 1 {
		t.Errorf("fees = %d, want = 1", n)
	}
}

// source: spec/services/subscriptions/organization_billing_service_spec.rb:427
// A later tick of the billing day (lago runs hourly) skips a subscription
// already billed that day, and its job key is the local billing date.
func TestBillSubscriptionsSkipsBilledToday(t *testing.T) {
	f := newFixture(t, billingDay)
	f.subscribe()
	if _, err := f.svc.BillSubscriptions(f.Ctx, f.OrgID(), billingDay); err != nil {
		t.Fatalf("run: %v", err)
	}
	jobs := f.Store.TakeJobs()
	var key string
	for _, j := range jobs {
		if j.Kind == kernel.JobBillSubscription {
			key = j.UniqueKey
		}
		if _, err := f.svc.HandleJob(f.Ctx, j); err != nil {
			t.Fatalf("job %s: %v", j.Kind, err)
		}
	}
	if want := core.DateOf(billingDay, time.UTC).String(); len(key) < len(want) || key[len(key)-len(want):] != want {
		t.Errorf("unique key = %q, want = suffix %q", key, want)
	}
	later := billingDay.Add(5 * time.Hour)
	f.Clock.Set(later)
	n, err := f.svc.BillSubscriptions(f.Ctx, f.OrgID(), later)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n != 0 {
		t.Errorf("groups on the second tick = %d, want = 0", n)
	}
}

// The billing day filters apply to the subscriptions billed and to those
// whose pending downgrade swaps (lago enqueues Subscriptions::TerminateJob
// only for billable subscriptions).
//
// source: app/services/subscriptions/organization_billing_service.rb:111-121
func TestBillSubscriptionsFilters(t *testing.T) {
	feb2 := billingtest.Date(2026, time.February, 2)
	tests := []struct {
		name   string
		source string
		edit   func(*subscription.Subscription)
		want   int
	}{
		{"billable", "spec/services/subscriptions/organization_billing_service_spec.rb:395", func(*subscription.Subscription) {}, 1},
		{"ending today", "spec/services/subscriptions/organization_billing_service_spec.rb:311", func(s *subscription.Subscription) {
			end := billingDay.Add(12 * time.Hour)
			s.EndingAt = &end
		}, 0},
		{"started after the billing day", "spec/services/subscriptions/organization_billing_service_spec.rb:328", func(s *subscription.Subscription) {
			s.StartedAt = &feb2
		}, 0},
		{"created after the billing day", "app/services/subscriptions/organization_billing_service.rb:118", func(s *subscription.Subscription) {
			s.CreatedAt = feb2
		}, 0},
	}
	for _, tc := range tests {
		for _, downgrade := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/downgrade=%v", tc.name, downgrade), func(t *testing.T) {
				t.Log("source: " + tc.source)
				f := newFixture(t, billingDay)
				sub := f.subscribe()
				f.UpdateSubscription(sub, tc.edit)
				kind := kernel.JobBillSubscription
				if downgrade {
					kind = kernel.JobSwapDowngrade
					f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2026, time.March, 1)), func(s *subscription.Subscription) {
						s.ExternalID, s.PreviousSubscriptionID = sub.ExternalID, sub.ID
					})
				}
				if _, err := f.svc.BillSubscriptions(f.Ctx, f.OrgID(), billingDay); err != nil {
					t.Fatalf("run: %v", err)
				}
				if got := f.enqueued(kind); got != tc.want {
					t.Errorf("%s jobs: got = %d, want = %d", kind, got, tc.want)
				}
			})
		}
	}
}
