package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

func TestCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := testNow
	clock := func() time.Time { return now }
	cat := &fakeCatalog{}
	m := cat.metric("bm", catalog.AggregationTypeSum, "v")
	s := cat.subscription("sub", subscription.StatusActive, testNow.Add(-time.Hour))
	cat.charge(s.PlanID, m, true, "")
	c := NewCache(cat, WithCacheTTL(time.Minute), WithCacheClock(clock))

	lookups := func() {
		t.Helper()
		if got, err := c.Metric(ctx, testOrg, "bm"); err != nil || got != m {
			t.Fatalf("Metric() got = %v, %v, want = %v", got, err, m)
		}
		if got, err := c.Metric(ctx, testOrg, "missing"); err != nil || got != nil {
			t.Fatalf("Metric(missing) got = %v, %v, want = nil", got, err)
		}
		if got, err := c.Subscriptions(ctx, testOrg, "sub"); err != nil || len(got) != 1 {
			t.Fatalf("Subscriptions() got = %v, %v, want = 1", got, err)
		}
		if got, err := c.Charges(ctx, testOrg, s.PlanID, m.ID); err != nil || len(got) != 1 {
			t.Fatalf("Charges() got = %v, %v, want = 1", got, err)
		}
	}
	calls := func(want int) {
		t.Helper()
		cat.mu.Lock()
		defer cat.mu.Unlock()
		if cat.calls != want {
			t.Errorf("catalog calls got = %d, want = %d", cat.calls, want)
		}
	}

	lookups()
	calls(4)
	lookups()
	calls(4) // all cached, the missing metric too

	c.Invalidate(Change{OrganizationID: testOrg, Kind: ChangeSubscription, Key: "sub"})
	lookups()
	calls(5)

	c.Invalidate(Change{OrganizationID: testOrg, Kind: ChangePlan, Key: s.PlanID.String()})
	lookups()
	calls(6)

	c.Invalidate(Change{OrganizationID: testOrg, Kind: ChangeMetric, Key: "bm"})
	lookups()
	calls(8) // the metric and the organization's charges

	other := core.NewID[core.OrganizationID](testIDs)
	c.Invalidate(Change{OrganizationID: other, Kind: ChangeOrganization})
	lookups()
	calls(8)

	c.Invalidate(Change{OrganizationID: testOrg, Kind: ChangeOrganization})
	lookups()
	calls(12)

	c.Purge()
	lookups()
	calls(16)

	now = now.Add(2 * time.Minute)
	lookups()
	calls(20) // expired
}

func TestCacheLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cat := &fakeCatalog{}
	c := NewCache(cat, WithCacheLimit(2))
	for _, code := range []string{"a", "b", "c", "a"} {
		if _, err := c.Metric(ctx, testOrg, code); err != nil {
			t.Fatalf("Metric() error = %v", err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if got := len(c.metrics.m); got > 2 {
		t.Errorf("entries got = %d, want ≤ 2", got)
	}
}

func TestSignals(t *testing.T) {
	t.Parallel()
	cust1, cust2 := core.NewID[core.CustomerID](testIDs), core.NewID[core.CustomerID](testIDs)
	sub1, sub2, sub3 := core.NewID[core.SubscriptionID](testIDs), core.NewID[core.SubscriptionID](testIDs), core.NewID[core.SubscriptionID](testIDs)
	t0 := testNow
	events := []*Enriched{
		{OrganizationID: testOrg, CustomerID: cust1, SubscriptionID: sub2, IngestedAt: t0},
		{OrganizationID: testOrg, CustomerID: cust2, SubscriptionID: sub3, IngestedAt: t0},
		{OrganizationID: testOrg, CustomerID: cust1, SubscriptionID: sub1, IngestedAt: t0.Add(time.Second)},
		{OrganizationID: testOrg, CustomerID: cust1, SubscriptionID: sub2, IngestedAt: t0.Add(-time.Second)},
		{OrganizationID: testOrg}, // no subscription
	}
	got := Signals(events)
	if len(got) != 2 {
		t.Fatalf("signals got = %d, want = 2", len(got))
	}
	if got[0].CustomerID != cust1 || len(got[0].SubscriptionIDs) != 2 || !got[0].Watermark.Equal(t0.Add(time.Second)) {
		t.Errorf("signal 0 got = %+v", got[0])
	}
	if got[0].SubscriptionIDs[0].String() > got[0].SubscriptionIDs[1].String() {
		t.Errorf("subscriptions got = %v, want = sorted", got[0].SubscriptionIDs)
	}
	if got[1].CustomerID != cust2 || len(got[1].SubscriptionIDs) != 1 || !got[1].Watermark.Equal(t0) {
		t.Errorf("signal 1 got = %+v", got[1])
	}
}

func TestRepoCatalog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memstore.New()
	src := &fakeCatalog{}
	m := src.metric("bm", catalog.AggregationTypeSum, "v")
	other := src.metric("other", catalog.AggregationTypeCount, "")
	var subs []*subscription.Subscription
	for i := range 3 {
		subs = append(subs, src.subscription("sub", subscription.StatusTerminated, testNow.Add(-time.Duration(i+2)*time.Hour)))
	}
	active := src.subscription("sub", subscription.StatusActive, testNow.Add(-time.Hour))
	ch := src.charge(active.PlanID, m, true, "")
	src.charge(active.PlanID, other, false, "")
	err := st.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		for _, x := range src.metrics {
			if err := r.Metrics.Create(ctx, x); err != nil {
				return err
			}
		}
		for _, s := range append(subs, active) {
			if err := r.Subscriptions.Create(ctx, s); err != nil {
				return err
			}
		}
		for _, c := range src.charges {
			if err := r.Charges.Create(ctx, c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seeding error = %v", err)
	}
	c := RepoCatalog{Transactor: st}
	if got, err := c.Metric(ctx, testOrg, "bm"); err != nil || got == nil || got.ID != m.ID {
		t.Errorf("Metric() got = %v, %v, want = %v", got, err, m.ID)
	}
	if got, err := c.Metric(ctx, testOrg, "missing"); err != nil || got != nil {
		t.Errorf("Metric(missing) got = %v, %v, want = nil, nil", got, err)
	}
	if got, err := c.Subscriptions(ctx, testOrg, "sub"); err != nil || len(got) != 4 {
		t.Errorf("Subscriptions() got = %d, %v, want = 4", len(got), err)
	}
	got, err := c.Charges(ctx, testOrg, active.PlanID, m.ID)
	if err != nil || len(got) != 1 || got[0].ID != ch.ID {
		t.Errorf("Charges() got = %v, %v, want = [%v]", got, err, ch.ID)
	}
}
