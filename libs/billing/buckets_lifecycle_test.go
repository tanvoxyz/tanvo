package billing_test

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/metrics"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// definitionOf returns the usage bucket definition the reads of m define,
// with the keys.
func definitionOf(m *catalog.BillableMetric, keys ...string) metering.BucketDefinition {
	return metering.BucketDefinitionFor(m.OrganizationID.String(), kernel.MeteringMetric(m), kernel.MeteringMetricFilters(m), keys).Normalize()
}

// sweep runs the sweep at the current instant and returns the reasons of
// the drops by definition code and ID.
func (s *bucketScenario) sweep() map[string]string {
	s.T.Helper()
	swept, err := s.E.SweepUsageBuckets(s.Ctx, s.OrgID(), s.Now())
	if err != nil {
		s.T.Fatalf("SweepUsageBuckets() error = %v", err)
	}
	out := map[string]string{}
	for _, d := range swept {
		out[bucketKey(d.Definition)] = d.Reason
	}
	return out
}

// define defines d through the engine.
func (s *bucketScenario) define(d metering.BucketDefinition) {
	s.T.Helper()
	if err := s.E.DefineUsageBuckets(s.Ctx, s.OrgID(), d); err != nil {
		s.T.Fatalf("DefineUsageBuckets() error = %v", err)
	}
}

// TestSweepUsageBuckets checks the rules of the sweep of the usage bucket
// definitions: what no metric, charge or read uses any more goes, the
// rest stays, and a block goes only with its metric or charges.
func TestSweepUsageBuckets(t *testing.T) {
	t.Parallel()
	june := billingtest.Date(2025, time.June, 1)
	s := newBucketScenario(t, june)
	p := s.Plan(period.Monthly, 0)
	calls := s.Metric("calls", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](s.IDs), Key: "region", Values: []string{"eu", "us"}}}
	})
	s.Charge(p, calls, catalog.ChargeModelStandard, `{"amount":"1"}`)
	seats := s.Metric("seats", catalog.AggregationTypeSum, "value")
	s.Charge(p, seats, catalog.ChargeModelPercentage, `{"rate":"1"}`)
	bytes := s.Metric("bytes", catalog.AggregationTypeSum, "value")
	s.Charge(p, bytes, catalog.ChargeModelStandard, `{"amount":"1"}`)
	idle := s.Metric("idle", catalog.AggregationTypeMax, "value")
	s.Charge(p, idle, catalog.ChargeModelStandard, `{"amount":"1"}`)
	org := s.OrgID().String()

	current := definitionOf(calls)
	grouped := definitionOf(calls, "user")
	outdated := metering.BucketDefinition{OrganizationID: org, Code: "calls", Property: "value"}.Normalize()
	// Folding the values the filter declared before it changed.
	unfiltered := metering.BucketDefinition{OrganizationID: org, Code: "calls", Dimensions: []string{"region"}, Values: map[string][]string{"region": {"eu"}}}.Normalize()
	deleted := metering.BucketDefinition{OrganizationID: org, Code: "gone"}.Normalize()
	noCharges := definitionOf(seats)
	blockedIdle := definitionOf(bytes)
	unread := definitionOf(idle)
	for _, d := range []metering.BucketDefinition{current, grouped, outdated, unfiltered, deleted, noCharges, unread} {
		s.define(d)
	}
	if err := s.events.BlockBuckets(s.Ctx, blockedIdle); err != nil {
		t.Fatal(err)
	}
	// The current definition of calls is read 40 days later, the
	// others never.
	s.Clock.Set(june.AddDate(0, 0, 40))
	s.events.readAt[bucketKey(current)] = s.Now()
	s.events.readAt[bucketKey(grouped)] = s.Now()

	got := s.sweep()
	want := map[string]string{
		bucketKey(outdated):   billing.BucketDropOutdated,
		bucketKey(unfiltered): billing.BucketDropOutdated,
		bucketKey(deleted):    billing.BucketDropMetricDeleted,
		bucketKey(noCharges):  billing.BucketDropNoCharges,
	}
	if !maps.Equal(got, want) {
		t.Errorf("first sweep got = %v, want = %v", got, want)
	}

	// 46 days after the definitions: the unread one is idle; the block
	// is kept however old, the read ones are within their idle time.
	s.Clock.Set(june.AddDate(0, 0, 46))
	got = s.sweep()
	want = map[string]string{bucketKey(unread): billing.BucketDropIdle}
	if !maps.Equal(got, want) {
		t.Errorf("idle sweep got = %v, want = %v", got, want)
	}
	states, err := s.events.BucketDefinitionStates(s.Ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, st := range states {
		kept = append(kept, bucketKey(st.Definition))
	}
	slices.Sort(kept)
	wantKept := []string{bucketKey(current), bucketKey(grouped), bucketKey(blockedIdle)}
	slices.Sort(wantKept)
	if !slices.Equal(kept, wantKept) {
		t.Errorf("kept got = %v, want = %v", kept, wantKept)
	}

	// A block goes with the charges reading the buckets.
	s.Do(func(ctx context.Context, r port.Repos) error {
		_, err := r.Charges.DiscardByMetric(ctx, s.OrgID(), bytes.ID, s.Now())
		return err
	})
	got = s.sweep()
	if want := map[string]string{bucketKey(blockedIdle): billing.BucketDropNoCharges}; !maps.Equal(got, want) {
		t.Errorf("sweep after the charges are gone got = %v, want = %v", got, want)
	}

	// Without an idle time, nothing is idle.
	s2 := newBucketScenario(t, june, billing.WithUsageBucketLimits(billing.UsageBucketLimits{}))
	p2 := s2.Plan(period.Monthly, 0)
	m2 := s2.Metric("calls", catalog.AggregationTypeCount, "")
	s2.Charge(p2, m2, catalog.ChargeModelStandard, `{"amount":"1"}`)
	s2.define(definitionOf(m2))
	s2.Clock.Set(june.AddDate(1, 0, 0))
	if got := s2.sweep(); len(got) != 0 {
		t.Errorf("sweep without an idle time got = %v, want = none", got)
	}
}

// TestSweepUsageBucketsJob checks the job of the sweep and its triggers:
// the update of what a metric's definitions depend on and the deletion of
// a metric enqueue it, other updates do not.
func TestSweepUsageBucketsJob(t *testing.T) {
	t.Parallel()
	june := billingtest.Date(2025, time.June, 1)
	s := newBucketScenario(t, june)
	if !slices.Contains(kernel.JobKinds(), kernel.JobSweepUsageBuckets) {
		t.Errorf("JobKinds: got = no %s, want = it", kernel.JobSweepUsageBuckets)
	}
	if got := kernel.QueueOf(kernel.JobSweepUsageBuckets); got != port.QueueDefault {
		t.Errorf("queue: got = %s, want = %s", got, port.QueueDefault)
	}
	m := s.Metric("calls", catalog.AggregationTypeCount, "")
	stale := metering.BucketDefinition{OrganizationID: s.OrgID().String(), Code: "gone"}
	s.define(stale)
	s.run(kernel.JobSweepUsageBuckets, kernel.InstantArgs{At: s.Now()})
	if len(s.events.defs) != 0 {
		t.Errorf("definitions after the sweep job got = %v, want = none", s.events.defs)
	}

	svc, err := metrics.New(s.Deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	sweeps := func() int {
		n := 0
		for _, k := range s.JobKinds() {
			if k == kernel.JobSweepUsageBuckets {
				n++
			}
		}
		return n
	}
	tests := []struct {
		name  string
		in    metrics.UpdateInput
		sweep int
	}{
		{"name", metrics.UpdateInput{Name: core.Some("Calls")}, 0},
		{"filters", metrics.UpdateInput{Filters: core.Some([]catalog.MetricFilter{{Key: "region", Values: []string{"eu"}}})}, 1},
		{"aggregation", metrics.UpdateInput{AggregationType: core.Some(catalog.AggregationTypeSum), FieldName: core.Some(new("value"))}, 1},
	}
	for _, tt := range tests {
		before := sweeps()
		if _, err := svc.Update(s.Ctx, s.OrgID(), m.ID, tt.in); err != nil {
			t.Fatalf("%s: Update() error = %v", tt.name, err)
		}
		if got := sweeps() - before; got != tt.sweep {
			t.Errorf("%s: sweeps enqueued got = %d, want = %d", tt.name, got, tt.sweep)
		}
		s.Store.TakeJobs()
	}
	if _, err := svc.Delete(s.Ctx, s.OrgID(), m.ID); err != nil {
		t.Fatal(err)
	}
	if got := sweeps(); got != 1 {
		t.Errorf("delete: sweeps enqueued got = %d, want = 1", got)
	}
}

// TestBlockOversizedUsageBuckets checks that a definition whose buckets
// hold more rows per bucket than the limit is blocked when defined: its
// reads stay on the events, and defining it again neither fails nor
// defines it.
func TestBlockOversizedUsageBuckets(t *testing.T) {
	t.Parallel()
	june := billingtest.Date(2025, time.June, 1)
	limits := billing.UsageBucketLimits{MaxRowsPerBucket: 100, IdleTTL: time.Hour}
	tests := []struct {
		name    string
		limits  billing.UsageBucketLimits
		rows    int64
		blocked bool
	}{
		{"under the limit", limits, 100, false},
		{"past the limit", limits, 101, true},
		{"no limit", billing.UsageBucketLimits{}, 1 << 40, false},
		{"default limit", billing.DefaultUsageBucketLimits(), 10_001, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := []billing.Option{billing.WithUsageBucketLimits(tt.limits)}
			if tt.limits == billing.DefaultUsageBucketLimits() {
				opts = nil
			}
			s := newBucketScenario(t, june, opts...)
			s.events.stats = metering.BucketStats{Rows: tt.rows * 3, MaxRowsPerBucket: tt.rows}
			d := metering.BucketDefinition{OrganizationID: s.OrgID().String(), Code: "calls", Dimensions: []string{"user"}}
			s.run(kernel.JobDefineUsageBuckets, kernel.DefineUsageBucketsArgs{Definition: d})
			if got := len(s.events.blocked) == 1 && len(s.events.defs) == 0; got != tt.blocked {
				t.Errorf("blocked got = %v (defs %v, blocks %v), want = %v", got, s.events.defs, s.events.blocked, tt.blocked)
			}
			// Defining it again is no error, and leaves the block.
			s.define(d)
			if got := len(s.events.blocked) == 1 && len(s.events.defs) == 0; got != tt.blocked {
				t.Errorf("blocked after a second define got = %v, want = %v", got, tt.blocked)
			}
		})
	}
	if got := billing.DefaultUsageBucketLimits(); got.MaxRowsPerBucket != 10_000 || got.IdleTTL != 45*24*time.Hour {
		t.Errorf("default limits got = %+v, want = {10000 1080h}", got)
	}
}
