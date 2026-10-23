package usage_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// amounts returns the amounts of thresholds.
func amounts(ts []*catalog.UsageThreshold) []int64 {
	var out []int64
	for _, t := range ts {
		out = append(out, t.Amount)
	}
	return out
}

func TestThresholds(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	of := func(f *fixture, s *subscription.Subscription) []int64 {
		return amounts(billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*catalog.UsageThreshold, error) {
			return f.svc.Thresholds(ctx, r, s)
		}))
	}
	t.Run("plan thresholds, recurring last", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		f.UsageThreshold(f.plan, 5, true)
		f.UsageThreshold(f.plan, 20, false)
		f.UsageThreshold(f.plan, 10, false)
		if got := of(f, f.sub); !slices.Equal(got, []int64{10, 20, 5}) {
			t.Errorf("got = %v, want = [10 20 5]", got)
		}
	})
	// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:42
	t.Run("own thresholds replace the plan's", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		f.UsageThreshold(f.plan, 10, false)
		f.UsageThreshold(f.plan, 30, false, func(u *catalog.UsageThreshold) { u.PlanID, u.SubscriptionID = core.PlanID{}, f.sub.ID })
		if got := of(f, f.sub); !slices.Equal(got, []int64{30}) {
			t.Errorf("got = %v, want = [30]", got)
		}
	})
	t.Run("parent plan thresholds of an override plan", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		f.UsageThreshold(f.plan, 10, false)
		child := f.Plan(f.plan.Interval, 0, func(p *catalog.Plan) { p.ParentID = f.plan.ID })
		s := f.Subscription(f.cust, child)
		if got := of(f, s); !slices.Equal(got, []int64{10}) {
			t.Errorf("got = %v, want = [10]", got)
		}
	})
	t.Run("progressive billing disabled", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10", func(s *subscription.Subscription) { s.ProgressiveBillingDisabled = true })
		f.UsageThreshold(f.plan, 10, false)
		if got := of(f, f.sub); len(got) != 0 {
			t.Errorf("got = %v, want none", got)
		}
	})
}

func TestCheckThresholds(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	cases := []struct {
		name       string
		current    int64
		thresholds []int64
		billed     int64
		terminated bool
		want       []int64
	}{
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:33
		{name: "one threshold passed", current: 20, thresholds: []int64{10}, want: []int64{10}},
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:68
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:76
		{name: "multiple thresholds passed", current: 401, thresholds: []int64{10, 400}, want: []int64{10, 400}},
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:116
		{name: "with a progressive billing invoice", current: 401, thresholds: []int64{10, 400}, billed: 20, want: []int64{400}},
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:139
		{name: "no threshold passed", current: 0, thresholds: []int64{3000}},
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:154
		{name: "terminated subscription", current: 20, thresholds: []int64{10}, terminated: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, now, "10")
			for _, a := range tc.thresholds {
				f.UsageThreshold(f.plan, a, false)
			}
			f.lifetime(f.sub, func(lu *subscription.LifetimeUsage) {
				lu.CurrentUsageAmount, lu.RecalculateCurrentUsage, lu.RecalculateInvoicedUsage = tc.current, true, true
			})
			if tc.billed > 0 {
				f.invoiceOf(f.sub, invoice.TypeProgressiveBilling, invoice.StatusFinalized, func(i *invoice.Invoice) { i.FeesAmount = tc.billed })
			}
			if tc.terminated {
				f.UpdateSubscription(f.sub, func(s *subscription.Subscription) { s.Status = subscription.StatusTerminated })
			}
			c := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (usage.Crossing, error) {
				return f.svc.CheckThresholds(ctx, r, f.sub)
			})
			if got := amounts(c.Thresholds); !slices.Equal(got, tc.want) || c.ProgressiveBilled != tc.billed {
				t.Errorf("got crossed = %v, billed = %d, want = %v, %d", got, c.ProgressiveBilled, tc.want, tc.billed)
			}
			// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:28
			if lu := f.reload(f.sub); !lu.RecalculateCurrentUsage || !lu.RecalculateInvoicedUsage {
				t.Errorf("got flags = %t %t, want them untouched", lu.RecalculateCurrentUsage, lu.RecalculateInvoicedUsage)
			}
			if got := f.Invoices(f.cust.ID); len(got) != min(1, int(tc.billed)) {
				t.Errorf("got %d invoices, want no new one", len(got))
			}
		})
	}
}

func TestRefreshAndCheck(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	f := newFixture(t, now, "10")
	f.UsageThreshold(f.plan, 1500, false)
	f.events(f.sub, 2, now.Add(-time.Hour))
	c := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (usage.Crossing, error) {
		return f.svc.RefreshAndCheck(ctx, r, f.OrgID(), f.sub.ID, nil)
	})
	if got := amounts(c.Thresholds); !slices.Equal(got, []int64{1500}) || c.LifetimeUsage.CurrentUsageAmount != 2000 {
		t.Errorf("got crossed = %v, current = %d, want = [1500], 2000", got, c.LifetimeUsage.CurrentUsageAmount)
	}
}

func TestOngoingUsage(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	f := newFixture(t, now, "10")
	inAdvance := f.Charge(f.plan, f.Metric("seats", catalog.AggregationTypeCount, ""), catalog.ChargeModelStandard, `{"amount":"1"}`,
		func(c *catalog.Charge) { c.PayInAdvance = true })
	f.events(f.sub, 2, now.Add(-time.Hour))
	f.Event(f.sub, "seats", now.Add(-time.Hour))
	pb := f.invoiceOf(f.sub, invoice.TypeProgressiveBilling, invoice.StatusFinalized)
	f.StoredFee(pb, 500, billingtest.ChargeFee(f.sub, f.charge.ID), func(fee *invoice.Fee) { fee.TaxesAmount = 50 })
	draft := f.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status, i.TotalAmount = invoice.StatusDraft, 300 })
	f.StoredFee(draft, 300, billingtest.ChargeFee(f.sub, f.charge.ID))
	empty := f.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusDraft })
	f.StoredFee(empty, 999, billingtest.ChargeFee(f.sub, f.charge.ID))

	got := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]wallets.UsageFee, error) {
		return f.svc.OngoingUsage(ctx, r, f.cust)
	})
	sums := map[core.ChargeID]int64{}
	var total int64
	for _, uf := range got {
		total += uf.Amount
		if uf.BillableMetricID == f.metric.ID {
			sums[f.charge.ID] += uf.Amount
		} else {
			sums[inAdvance.ID] += uf.Amount
		}
	}
	// calls: usage 2000 − progressive 550 + draft 300; seats: 100 − 100.
	if sums[f.charge.ID] != 1750 || sums[inAdvance.ID] != 0 || total != 1750 {
		t.Errorf("got amounts = %v (total %d) from %+v, want calls 1750, seats 0", sums, total, got)
	}
}

// source: app/queries/past_usage_query.rb (no service spec)
func TestPastUsage(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	f := newFixture(t, now, "10")
	other := f.Charge(f.plan, f.Metric("seats", catalog.AggregationTypeCount, ""), catalog.ChargeModelStandard, `{"amount":"1"}`)
	var invs []*invoice.Invoice
	for _, m := range []time.Month{time.March, time.April} {
		inv := f.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
		from := billingtest.Date(2024, m, 1)
		f.LinkSubscription(inv, f.sub, invoice.InvoicingReasonSubscriptionPeriodic, from, from.AddDate(0, 1, 0))
		f.StoredFee(inv, 100, billingtest.ChargeFee(f.sub, f.charge.ID))
		f.StoredFee(inv, 7, billingtest.ChargeFee(f.sub, other.ID))
		f.StoredFee(inv, 1000, billingtest.SubscriptionFee(f.sub))
		invs = append(invs, inv)
	}
	past := func(flt usage.PastUsageFilter) []usage.PastPeriod {
		flt.CustomerID, flt.ExternalSubscriptionID = f.cust.ID, f.sub.ExternalID
		return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]usage.PastPeriod, error) {
			return f.svc.PastUsage(ctx, r, f.OrgID(), flt)
		})
	}
	all := past(usage.PastUsageFilter{})
	if len(all) != 2 || all[0].Invoice.ID != invs[1].ID || len(all[0].Fees) != 2 {
		t.Fatalf("got %d periods, want = 2, April first with 2 charge fees", len(all))
	}
	if got := past(usage.PastUsageFilter{PeriodsCount: 1}); len(got) != 1 || got[0].Invoice.ID != invs[1].ID {
		t.Errorf("got %d periods, want = the April one", len(got))
	}
	if got := past(usage.PastUsageFilter{BillableMetricID: f.metric.ID}); len(got[0].Fees) != 1 || got[0].Fees[0].ChargeID != f.charge.ID {
		t.Errorf("got fees = %+v, want the calls fee", got[0].Fees)
	}
}
