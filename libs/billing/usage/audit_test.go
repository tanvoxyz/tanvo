package usage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// source: app/services/invoices/customer_usage_service.rb:56 (no spec case)
func TestCurrentUsageUnknownCharge(t *testing.T) {
	t.Parallel()
	f := customerUsageFixture(t, billingtest.Date(2025, time.June, 15))
	_, err := f.usageOf(f.sub, usage.Options{ChargeIDs: []core.ChargeID{core.NewID[core.ChargeID](f.IDs)}})
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("current usage error = %v, want = %v", err, core.ErrNotFound)
	}
}

// The lifetime usage of a new subscription reusing an external ID starts
// fresh, while a subscription of the same chain takes the record over
// (source: app/models/subscription.rb:129, mark_as_active!).
func TestLifetimeUsageChainTakeover(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	cases := []struct {
		name           string
		sameChain      bool
		wantHistorical int64
	}{
		{name: "next subscription of the chain", sameChain: true, wantHistorical: 500},
		{name: "new subscription reusing the external ID", sameChain: false, wantHistorical: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, now, "1")
			old := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(now.AddDate(-1, 0, 0)), subscriptionNamed("ext"), status(subscription.StatusTerminated))
			f.lifetime(old, func(lu *subscription.LifetimeUsage) {
				lu.ExternalSubscriptionID, lu.HistoricalUsageAmount, lu.InvoicedUsageAmount = "ext", 500, 300
			})
			at := now.AddDate(0, -1, 0)
			opts := []func(*subscription.Subscription){billingtest.SubscribedAt(at), subscriptionNamed("ext")}
			if tc.sameChain {
				opts = append(opts, billingtest.SubscribedAt(old.SubscriptionAt), func(s *subscription.Subscription) { s.PreviousSubscriptionID = old.ID })
			}
			next := f.Subscription(f.cust, f.plan, opts...)
			lu := f.refresh(next)
			if lu.SubscriptionID != next.ID || lu.HistoricalUsageAmount != tc.wantHistorical || lu.InvoicedUsageAmount != 0 {
				t.Errorf("lifetime usage = (%s, historical %d, invoiced %d), want = (%s, %d, 0)",
					lu.SubscriptionID, lu.HistoricalUsageAmount, lu.InvoicedUsageAmount, next.ID, tc.wantHistorical)
			}
		})
	}
}

// Periods with the same charges start are listed latest created first
// (source: app/queries/past_usage_query.rb:40, app/queries/base_query.rb:112).
func TestPastUsageTieOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t, billingtest.Date(2024, time.May, 15), "1")
	from, to := billingtest.Date(2024, time.April, 1), billingtest.Date(2024, time.May, 1)
	first := f.LinkSubscription(f.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusVoided }), f.sub, invoice.InvoicingReasonSubscriptionTerminating, from, to)
	f.Clock.Advance(time.Hour)
	second := f.LinkSubscription(f.StoredInvoice(f.cust), f.sub, invoice.InvoicingReasonSubscriptionStarting, from, to)
	got := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]usage.PastPeriod, error) {
		return f.svc.PastUsage(ctx, r, f.OrgID(), usage.PastUsageFilter{CustomerID: f.cust.ID, ExternalSubscriptionID: f.sub.ExternalID})
	})
	if len(got) != 2 || got[0].InvoiceSubscription.ID != second.ID || got[1].InvoiceSubscription.ID != first.ID {
		t.Fatalf("past usage = %d periods, want = [%s %s]", len(got), second.ID, first.ID)
	}
}
