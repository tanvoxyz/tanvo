package billing_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// bucketedEvents is the harness event store with the usage buckets of the
// definitions defined on it, counting the bucket reads and those a ready
// definition served, and keeping the lifecycle of the definitions.
type bucketedEvents struct {
	*billingtest.Events
	now           func() time.Time
	defs          []metering.BucketDefinition
	blocked       []metering.BucketDefinition
	definedAt     map[string]time.Time
	readAt        map[string]time.Time
	reads, served int
	// stats is the size of the buckets of every definition.
	stats metering.BucketStats
}

var _ kernel.BucketLifecycle = (*bucketedEvents)(nil)

func bucketKey(d metering.BucketDefinition) string { return d.Code + "/" + d.Normalize().ID() }

func (b *bucketedEvents) ReadBuckets(ctx context.Context, r metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	b.reads++
	totals, ok, err := b.MemoryStore.Buckets(b.defs...).ReadBuckets(ctx, r)
	if ok {
		b.served++
		if d, ok := metering.CoveringDefinition(b.defs, r); ok {
			b.readAt[bucketKey(d)] = b.now()
		}
	}
	return totals, ok, err
}

func (b *bucketedEvents) DefineBuckets(_ context.Context, d metering.BucketDefinition) error {
	k := bucketKey(d)
	if slices.ContainsFunc(b.blocked, func(o metering.BucketDefinition) bool { return bucketKey(o) == k }) {
		return metering.ErrBucketsBlocked
	}
	if _, ok := b.definedAt[k]; ok {
		return nil
	}
	b.defs = append(b.defs, d.Normalize())
	b.definedAt[k] = b.now()
	return nil
}

func (b *bucketedEvents) BucketDefinitionStates(_ context.Context, org string) ([]metering.BucketDefinitionState, error) {
	var out []metering.BucketDefinitionState
	for _, d := range b.defs {
		if org == "" || d.OrganizationID == org {
			out = append(out, metering.BucketDefinitionState{Definition: d, Ready: true, DefinedAt: b.definedAt[bucketKey(d)], ReadAt: b.readAt[bucketKey(d)]})
		}
	}
	for _, d := range b.blocked {
		if org == "" || d.OrganizationID == org {
			out = append(out, metering.BucketDefinitionState{Definition: d, Blocked: true, DefinedAt: b.definedAt[bucketKey(d)]})
		}
	}
	return out, nil
}

func (b *bucketedEvents) DropBuckets(_ context.Context, d metering.BucketDefinition) error {
	k := bucketKey(d)
	other := func(o metering.BucketDefinition) bool { return bucketKey(o) == k }
	b.defs = slices.DeleteFunc(b.defs, other)
	b.blocked = slices.DeleteFunc(b.blocked, other)
	delete(b.definedAt, k)
	delete(b.readAt, k)
	return nil
}

func (b *bucketedEvents) BlockBuckets(ctx context.Context, d metering.BucketDefinition) error {
	if err := b.DropBuckets(ctx, d); err != nil {
		return err
	}
	b.blocked = append(b.blocked, d.Normalize())
	b.definedAt[bucketKey(d)] = b.now()
	return nil
}

func (b *bucketedEvents) BucketStats(_ context.Context, d metering.BucketDefinition) (metering.BucketStats, error) {
	if _, ok := b.definedAt[bucketKey(d)]; !ok || slices.ContainsFunc(b.blocked, func(o metering.BucketDefinition) bool { return bucketKey(o) == bucketKey(d) }) {
		return metering.BucketStats{}, nil
	}
	return b.stats, nil
}

// bucketScenario is a scenario whose event store keeps usage buckets and
// whose engine caches the current usage.
type bucketScenario struct {
	*scenario
	events *bucketedEvents
	cache  *kernel.MemoryUsageCache
}

func newBucketScenario(t *testing.T, now time.Time, opts ...billing.Option) *bucketScenario {
	t.Helper()
	h := billingtest.New(t, now)
	be := &bucketedEvents{Events: h.Events, now: h.Now, definedAt: map[string]time.Time{}, readAt: map[string]time.Time{}}
	cache := kernel.NewMemoryUsageCache(100)
	h.Deps.Events = be
	h.Deps.UsageCache = cache
	e, err := billing.New(*h.Deps, opts...)
	if err != nil {
		t.Fatalf("billing.New() error = %v", err)
	}
	return &bucketScenario{scenario: &scenario{Harness: h, E: e}, events: be, cache: cache}
}

// current returns the current usage of sub with opts.
func (s *bucketScenario) current(sub *subscription.Subscription, opts usage.Options) *usage.Usage {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*usage.Usage, error) {
		return s.E.Usage.CurrentUsage(ctx, r, s.OrgID(), sub.ID, opts)
	})
}

// defineJobs returns the number of pending usage bucket definitions.
func (s *bucketScenario) defineJobs() int {
	n := 0
	for _, k := range s.JobKinds() {
		if k == kernel.JobDefineUsageBuckets {
			n++
		}
	}
	return n
}

// TestCurrentUsageFromBuckets checks which current usages read the usage
// buckets (lago Events::Stores::Provider): a read no ready definition
// covers falls back to the events and defines the buckets in a job, and
// the next one is served from them with the same usage.
func TestCurrentUsageFromBuckets(t *testing.T) {
	t.Parallel()
	june := billingtest.Date(2025, time.June, 15, 12)
	type setup struct {
		agg                       catalog.AggregationType
		field                     string
		recurring                 bool
		model                     catalog.ChargeModel
		props                     string
		inAdvance, prorated, wall bool
		// filtered gives the charge a filter on region eu.
		filtered bool
	}
	count := setup{agg: catalog.AggregationTypeCount, model: catalog.ChargeModelStandard, props: `{"amount":"1"}`}
	sum := setup{agg: catalog.AggregationTypeSum, field: "value", model: catalog.ChargeModelStandard, props: `{"amount":"1"}`}
	with := func(s setup, f func(*setup)) setup { f(&s); return s }
	cases := []struct {
		name, source string
		setup        setup
		opts         func(c *catalog.Charge) usage.Options
		// optOut leaves Options.UsageBuckets unset.
		optOut bool
		served bool
	}{
		{
			// The first read misses and falls back to the events.
			name: "count", source: "spec/services/invoices/customer_usage_service_spec.rb:1111, spec/services/events/stores/provider_spec.rb:81, 178, 231, 382",
			setup: count, served: true,
		},
		{
			name: "sum", source: "spec/services/invoices/customer_usage_service_spec.rb:1487, 1498",
			setup: sum, served: true,
		},
		{
			name: "max", source: "spec/services/invoices/customer_usage_service_spec.rb:1569, 1577, spec/services/events/stores/provider_spec.rb:269",
			setup: with(sum, func(s *setup) { s.agg = catalog.AggregationTypeMax }), served: true,
		},
		{
			name: "latest", source: "spec/services/invoices/customer_usage_service_spec.rb:1585, 1593, spec/services/events/stores/provider_spec.rb:283",
			setup: with(sum, func(s *setup) { s.agg = catalog.AggregationTypeLatest }), served: true,
		},
		{
			name: "dynamic", source: "spec/services/invoices/customer_usage_service_spec.rb:1657, 1665",
			setup: with(sum, func(s *setup) { s.model, s.props = catalog.ChargeModelDynamic, `{}` }), served: true,
		},
		{
			name: "graduated", source: "spec/services/realtime_usage_spec.rb:173",
			setup: with(count, func(s *setup) {
				s.model = catalog.ChargeModelGraduated
				s.props = `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"1","flat_amount":"0"}]}`
			}), served: true,
		},
		{
			// The eu filter and the default bucket holding the us event.
			name: "charge filters", source: "spec/services/invoices/customer_usage_service_spec.rb:1430, spec/services/events/stores/provider_spec.rb:340, 350",
			setup: with(count, func(s *setup) { s.filtered = true }), served: true,
		},
		{
			name: "projected", source: "spec/services/invoices/customer_usage_service_spec.rb:1170, 1185",
			setup: count, opts: func(*catalog.Charge) usage.Options { return usage.Options{Projection: true} }, served: true,
		},
		{
			name: "grouped", source: "spec/services/events/stores/usage_bucket_store_spec.rb:119, 145, 172, 199",
			setup:  with(count, func(s *setup) { s.props = `{"amount":"1","pricing_group_keys":["region"]}` }),
			served: true,
		},
		{
			name: "grouping skipped", source: "spec/services/invoices/customer_usage_service_spec.rb:1731",
			setup: with(count, func(s *setup) { s.props = `{"amount":"1","pricing_group_keys":["region"]}` }),
			opts: func(*catalog.Charge) usage.Options {
				return usage.Options{Filters: fees.UsageFilters{SkipGrouping: true}}
			},
			served: true,
		},
		{
			name: "a presentation breakdown asked for none", source: "spec/services/invoices/customer_usage_service_spec.rb:1340, 1362, spec/services/events/stores/provider_spec.rb:500, 514",
			setup: with(count, func(s *setup) { s.props = `{"amount":"1","presentation_group_keys":[{"value":"region"}]}` }),
			opts: func(*catalog.Charge) usage.Options {
				return usage.Options{Filters: fees.UsageFilters{Presentation: []string{}}}
			},
			served: true,
		},
		{
			name: "a presentation breakdown", source: "spec/services/events/stores/provider_spec.rb:416, 482, 494, spec/services/events/stores/usage_bucket_store_spec.rb:132, 158, 185, 212",
			setup: with(count, func(s *setup) { s.props = `{"amount":"1","presentation_group_keys":[{"value":"region"}]}` }),
		},
		{
			name: "not asked for", source: "spec/services/invoices/customer_usage_service_spec.rb:1160, 1163, spec/services/events/stores/provider_spec.rb:89, 196",
			setup: count, optOut: true,
		},
		{
			name: "full usage", source: "spec/services/invoices/customer_usage_service_spec.rb:1210, 1219, spec/services/events/stores/provider_spec.rb:119",
			setup: count,
			opts: func(c *catalog.Charge) usage.Options {
				return usage.Options{FullUsage: true, ChargeIDs: []core.ChargeID{c.ID}}
			},
		},
		{
			name: "a group filter", source: "spec/services/events/stores/provider_spec.rb:104, 324, 432",
			setup: with(count, func(s *setup) { s.props = `{"amount":"1","pricing_group_keys":["region"]}` }),
			opts: func(*catalog.Charge) usage.Options {
				return usage.Options{Filters: fees.UsageFilters{Group: map[string][]string{"region": {"eu"}}}}
			},
		},
		{
			name: "capped by an instant", source: "spec/services/events/stores/provider_spec.rb:407",
			setup: count,
			opts: func(*catalog.Charge) usage.Options {
				return usage.Options{MaxTimestamp: june.Add(-time.Minute)}
			},
		},
		{
			name: "pay in advance", source: "spec/services/events/stores/provider_spec.rb:332",
			setup: with(sum, func(s *setup) { s.inAdvance = true }),
		},
		{
			name: "prorated", source: "spec/services/events/stores/provider_spec.rb:240",
			setup: with(sum, func(s *setup) { s.recurring, s.prorated = true, true }),
		},
		{
			name: "recurring", source: "spec/services/events/stores/provider_spec.rb:248",
			setup: with(sum, func(s *setup) { s.recurring = true }),
		},
		{
			name: "unique count", source: "spec/services/events/stores/provider_spec.rb:292",
			setup: with(sum, func(s *setup) { s.agg, s.field = catalog.AggregationTypeUniqueCount, "user" }),
		},
		{
			name: "weighted sum", source: "spec/services/events/stores/provider_spec.rb:256",
			setup: with(sum, func(s *setup) { s.agg = catalog.AggregationTypeWeightedSum }),
		},
		{
			name: "percentage", source: "spec/services/events/stores/provider_spec.rb:300, spec/services/invoices/customer_usage_service_spec.rb:1375, spec/services/realtime_usage/fetch_buckets_service_spec.rb:259",
			setup: with(sum, func(s *setup) { s.model, s.props = catalog.ChargeModelPercentage, `{"rate":"1"}` }),
		},
		{
			name: "a target wallet", source: "spec/services/realtime_usage_spec.rb:206",
			setup: with(count, func(s *setup) { s.wall = true }),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			t.Log("source: " + tc.source)
			s := newBucketScenario(t, june)
			c := s.Customer()
			p := s.Plan(period.Monthly, 0)
			m := s.Metric("calls", tc.setup.agg, tc.setup.field, func(m *catalog.BillableMetric) {
				m.Recurring = tc.setup.recurring
				if tc.setup.filtered {
					m.Filters = []catalog.MetricFilter{{Key: "region", Values: []string{"eu", "us"}}}
				}
			})
			ch := s.Charge(p, m, tc.setup.model, tc.setup.props, func(ch *catalog.Charge) {
				ch.PayInAdvance, ch.Prorated, ch.AcceptsTargetWallet = tc.setup.inAdvance, tc.setup.prorated, tc.setup.wall
				if tc.setup.filtered {
					ch.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"2"}`)}
				}
			})
			sub := s.Subscription(c, p, billingtest.SubscribedAt(june.AddDate(0, -3, 0)))
			for i, region := range []string{"eu", "eu", "us"} {
				s.Event(sub, "calls", june.Add(-time.Duration(3-i)*time.Hour), "region", region, "value", "2", "user", region)
			}
			var opts usage.Options
			if tc.opts != nil {
				opts = tc.opts(ch)
			}
			opts.UsageBuckets, opts.SkipTaxes, opts.SkipCache = !tc.optOut, true, true

			first := s.current(sub, opts)
			if s.events.served != 0 {
				t.Errorf("first read: served got = %d, want = 0 before any definition", s.events.served)
			}
			wantJobs := 0
			if tc.served {
				wantJobs = 1
			}
			if got := s.defineJobs(); got != wantJobs {
				t.Fatalf("define jobs: got = %d, want = %d (%v)", got, wantJobs, s.JobKinds())
			}
			if !tc.served && s.events.reads != 0 {
				t.Errorf("reads: got = %d, want = 0 for a usage the buckets do not serve", s.events.reads)
			}
			s.drain()
			if got := len(s.events.defs); got != wantJobs {
				t.Fatalf("definitions: got = %d, want = %d", got, wantJobs)
			}

			// The second read asks the same and enqueues nothing more.
			s.events.reads, s.events.served = 0, 0
			second := s.current(sub, opts)
			if tc.served && (s.events.served == 0 || s.events.served != s.events.reads) {
				t.Errorf("second read: served got = %d of %d reads, want = every read served", s.events.served, s.events.reads)
			}
			if !tc.served && s.events.reads != 0 {
				t.Errorf("second read: reads got = %d, want = 0", s.events.reads)
			}
			if got := s.defineJobs(); got != 0 {
				t.Errorf("define jobs after the definition: got = %d, want = 0", got)
			}
			if first.Amount != second.Amount {
				t.Errorf("amount: got = %d then %d, want = the same amount", first.Amount, second.Amount)
			}
			if tc.served && (len(second.Fees) == 0 || !second.Fees[0].Units.IsPositive()) {
				t.Errorf("fees: got = %+v, want = the units of the events", second.Fees)
			}
			if !slices.EqualFunc(first.Fees, second.Fees, func(a, b *invoice.Fee) bool {
				return a.Units.Equal(b.Units) && a.EventsCount == b.EventsCount && a.Amount == b.Amount
			}) {
				t.Errorf("fees: got = %+v then %+v, want = the same fees", first.Fees, second.Fees)
			}
		})
	}
}

// TestUsageBucketsCache checks that a current usage served from the
// buckets bypasses the usage cache, while a charge they do not serve next
// to it is cached as before.
//
// source: spec/services/invoices/customer_usage_service_spec.rb:1256, 1262
func TestUsageBucketsCache(t *testing.T) {
	t.Parallel()
	june := billingtest.Date(2025, time.June, 15, 12)
	s := newBucketScenario(t, june)
	c := s.Customer()
	p := s.Plan(period.Monthly, 0)
	served := s.Metric("calls", catalog.AggregationTypeCount, "")
	delegated := s.Metric("users", catalog.AggregationTypeUniqueCount, "user")
	s.Charge(p, served, catalog.ChargeModelStandard, `{"amount":"1"}`)
	s.Charge(p, delegated, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := s.Subscription(c, p, billingtest.SubscribedAt(june.AddDate(0, -3, 0)))
	s.Clock.Set(june.Add(-time.Hour))
	s.Event(sub, "calls", june.Add(-2*time.Hour))
	s.Event(sub, "users", june.Add(-2*time.Hour), "user", "a")
	s.Clock.Set(june)
	opts := usage.Options{UsageBuckets: true, SkipTaxes: true}
	for i := range 2 {
		u := s.current(sub, opts)
		if u.Amount != 2_00 {
			t.Errorf("read %d: amount got = %d, want = 200", i, u.Amount)
		}
		if got := s.cache.Len(); got != 1 {
			t.Errorf("read %d: cache entries got = %d, want = 1 (the delegated charge only)", i, got)
		}
		s.drain()
	}
	if s.events.served == 0 {
		t.Errorf("served got = 0, want = the second read served from the buckets")
	}

	// Without the buckets, the served charge is cached too.
	s.current(sub, usage.Options{SkipTaxes: true})
	if got := s.cache.Len(); got != 2 {
		t.Errorf("without the buckets: cache entries got = %d, want = 2", got)
	}
}

// TestDefineUsageBucketsJob checks the job defining usage buckets.
func TestDefineUsageBucketsJob(t *testing.T) {
	t.Parallel()
	s := newBucketScenario(t, billingtest.Date(2025, time.June, 15))
	def := metering.BucketDefinition{OrganizationID: s.OrgID().String(), Code: "calls"}
	s.run(kernel.JobDefineUsageBuckets, kernel.DefineUsageBucketsArgs{Definition: def})
	if len(s.events.defs) != 1 || s.events.defs[0].Code != "calls" {
		t.Errorf("definitions: got = %+v, want = the definition of calls", s.events.defs)
	}
	if !slices.Contains(kernel.JobKinds(), kernel.JobDefineUsageBuckets) {
		t.Errorf("JobKinds: got = no %s, want = it", kernel.JobDefineUsageBuckets)
	}
	if got := kernel.QueueOf(kernel.JobDefineUsageBuckets); got != port.QueueDefault {
		t.Errorf("queue: got = %s, want = %s", got, port.QueueDefault)
	}

	// Another organization's definition is refused.
	other := def
	other.OrganizationID = core.NewID[core.OrganizationID](s.IDs).String()
	if err := s.E.DefineUsageBuckets(s.Ctx, s.OrgID(), other); err == nil {
		t.Errorf("another organization: got = nil, want = an error")
	}
	// A store without buckets has nothing to define.
	h := billingtest.New(t, s.Now())
	e, err := billing.New(*h.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DefineUsageBuckets(h.Ctx, h.OrgID(), metering.BucketDefinition{OrganizationID: h.OrgID().String(), Code: "calls"}); err != nil {
		t.Errorf("a store without buckets: got = %v, want = nil", err)
	}
}

// TestBucketClassification checks that the usage buckets serve known
// charge models and aggregations only, and exclude exactly the ones whose
// totals do not recompose (lago's custom model and aggregation are not
// ported, domain §24).
//
// source: spec/services/realtime_usage_spec.rb:223, 227, 231, 237
func TestBucketClassification(t *testing.T) {
	t.Parallel()
	var models []string
	for _, m := range catalog.ChargeModelValues() {
		models = append(models, string(m))
	}
	var excludedModels []string
	for _, m := range models {
		if !slices.Contains(metering.BucketChargeModels, m) {
			excludedModels = append(excludedModels, m)
		}
	}
	for _, m := range metering.BucketChargeModels {
		if !slices.Contains(models, m) {
			t.Errorf("charge model %q: got = served, want = a known model", m)
		}
	}
	if want := []string{"percentage"}; !slices.Equal(excludedModels, want) {
		t.Errorf("excluded charge models: got = %v, want = %v", excludedModels, want)
	}
	var aggs, excludedAggs []string
	for _, a := range catalog.AggregationTypeValues() {
		aggs = append(aggs, string(a))
		if !slices.Contains(metering.BucketAggregations, metering.AggregationType(a)) {
			excludedAggs = append(excludedAggs, string(a))
		}
	}
	for _, a := range metering.BucketAggregations {
		if !slices.Contains(aggs, string(a)) {
			t.Errorf("aggregation %q: got = served, want = a known aggregation", a)
		}
	}
	if want := []string{"unique_count", "weighted_sum"}; !slices.Equal(excludedAggs, want) {
		t.Errorf("excluded aggregations: got = %v, want = %v", excludedAggs, want)
	}
}
