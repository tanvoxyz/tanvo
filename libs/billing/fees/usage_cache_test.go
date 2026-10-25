package fees_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// countingCache is a usage cache that counts the entries it returns and
// writes; a returned entry may still be found stale by its reader.
type countingCache struct {
	*kernel.MemoryUsageCache
	hits, puts int
	keys       []string
	// onGet, when set, runs on each lookup: before the aggregation.
	onGet func()
}

func (c *countingCache) Get(key string, now time.Time) (kernel.UsageCacheEntry, bool) {
	if c.onGet != nil {
		c.onGet()
	}
	e, ok := c.MemoryUsageCache.Get(key, now)
	if ok {
		c.hits++
	}
	return e, ok
}

func (c *countingCache) Put(key string, e kernel.UsageCacheEntry) {
	c.puts++
	c.keys = append(c.keys, key)
	c.MemoryUsageCache.Put(key, e)
}

// cacheFixture is a sum charge with a usage cache, the clock on 2022-03-20
// within the subscription's first period.
type cacheFixture struct {
	*chargeFixture
	m     *catalog.BillableMetric
	cache *countingCache
}

var cacheNow = date(2022, time.March, 20)

func newCacheFixture(t *testing.T, model catalog.ChargeModel, props string, opts ...func(*catalog.Charge)) (*cacheFixture, *catalog.Charge) {
	t.Helper()
	return newCacheFixtureOf(t, nil, model, props, opts...)
}

func newCacheFixtureOf(t *testing.T, metric func(*catalog.BillableMetric), model catalog.ChargeModel, props string, opts ...func(*catalog.Charge)) (*cacheFixture, *catalog.Charge) {
	t.Helper()
	f := newChargeFixture(t)
	f.Clock.Set(cacheNow)
	var mo []func(*catalog.BillableMetric)
	if metric != nil {
		mo = append(mo, metric)
	}
	m := f.Metric("metric", catalog.AggregationTypeSum, "value", mo...)
	c := f.Charge(f.plan, m, model, props, opts...)
	return &cacheFixture{chargeFixture: f, m: m, cache: &countingCache{MemoryUsageCache: kernel.NewMemoryUsageCache(100)}}, c
}

// event stores an event of the metric ingested at ingested.
func (f *cacheFixture) event(ingested time.Time, props ...string) {
	f.T.Helper()
	e := metering.Event{
		OrganizationID: f.OrgID().String(), SubscriptionID: f.sub.ExternalID, Code: f.m.Code,
		TransactionID: core.NewID[core.FeeID](f.IDs).String(), Timestamp: date(2022, time.March, 16), IngestedAt: ingested,
		Properties: metering.Properties{},
	}
	for i := 0; i+1 < len(props); i += 2 {
		e.Properties[props[i]] = props[i+1]
	}
	f.Events.Add(e)
}

func (f *cacheFixture) usage(c *catalog.Charge, opts ...func(*fees.Options)) []*invoice.Fee {
	f.T.Helper()
	o := fees.Options{CurrentUsage: true, UsageCache: f.cache}
	for _, fn := range opts {
		fn(&o)
	}
	return f.run(f.usageScope(f.sub, f.Now()), c, o).Fees
}

func units(fs []*invoice.Fee) string {
	if len(fs) != 1 {
		return fmt.Sprintf("%d fees", len(fs))
	}
	return fs[0].Units.Reduce().String()
}

func TestUsageCache(t *testing.T) {
	t.Parallel()
	settled := cacheNow.Add(-time.Minute)

	// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:54
	// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:95
	// source: spec/services/fees/charge_service_spec.rb:3867
	// source: spec/services/fees/charge_service_spec.rb:3938
	t.Run("serves the cached fees", func(t *testing.T) {
		t.Parallel()
		f, c := newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"1"}`)
		f.event(settled, "value", "10")
		first := f.usage(c)
		second := f.usage(c)
		if got := units(first) + " " + units(second); got != "10 10" {
			t.Errorf("units: got = %s, want = 10 10", got)
		}
		if f.cache.puts != 1 || f.cache.hits != 1 {
			t.Errorf("got = %d puts and %d hits, want = 1 and 1", f.cache.puts, f.cache.hits)
		}
		if first[0].ID == second[0].ID {
			t.Errorf("got = the same fee ID twice, want = a new fee per computation")
		}
	})
	// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:121
	// source: spec/services/cache_service_spec.rb:384
	t.Run("an event ingested before the entry keeps it", func(t *testing.T) {
		t.Parallel()
		f, c := newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"1"}`)
		f.event(settled, "value", "10")
		f.usage(c)
		f.event(settled.Add(-time.Hour), "value", "5")
		if got := units(f.usage(c)); got != "10" {
			t.Errorf("units: got = %s, want = 10 (cached)", got)
		}
	})
	// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:109
	// source: spec/services/cache_service_spec.rb:314
	// source: spec/services/cache_service_spec.rb:394
	t.Run("a more recent event recomputes", func(t *testing.T) {
		t.Parallel()
		f, c := newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"1"}`)
		f.event(settled, "value", "10")
		f.usage(c)
		f.event(settled.Add(time.Microsecond), "value", "5")
		if got := units(f.usage(c)); got != "15" {
			t.Errorf("units: got = %s, want = 15", got)
		}
		if f.cache.puts != 2 {
			t.Errorf("got = %d puts, want = 2", f.cache.puts)
		}
	})
	// source: spec/services/cache_service_spec.rb:221
	// source: spec/services/cache_service_spec.rb:233
	// source: spec/services/cache_service_spec.rb:243
	// source: spec/services/cache_service_spec.rb:296
	t.Run("settle window", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			ingested time.Time
			puts     int
		}{
			{cacheNow.Add(-time.Second / 2), 0},
			{cacheNow.Add(-time.Second), 0},
			{cacheNow.Add(-time.Second - time.Microsecond), 1},
		} {
			f, c := newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"1"}`)
			f.event(tc.ingested, "value", "10")
			f.usage(c)
			if f.cache.puts != tc.puts {
				t.Errorf("event %s before now: got = %d puts, want = %d", cacheNow.Sub(tc.ingested), f.cache.puts, tc.puts)
			}
		}
	})
	// The clock moves 30 seconds between the lookup and the aggregation:
	// the settle window and the fallback stamp are measured from before.
	// source: spec/services/cache_service_spec.rb:198
	// source: spec/services/cache_service_spec.rb:258
	t.Run("a slow aggregation", func(t *testing.T) {
		t.Parallel()
		f, c := newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"1"}`)
		f.cache.onGet = func() { f.Clock.Set(cacheNow.Add(30 * time.Second)) }
		f.usage(c)
		e, ok := f.cache.MemoryUsageCache.Get(f.cache.keys[0], cacheNow)
		if want := cacheNow.Add(-time.Second); !ok || !e.CachedAt.Equal(want) {
			t.Errorf("no event: got = %v %v, want = %v", e.CachedAt, ok, want)
		}
		f, c = newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"1"}`)
		f.cache.onGet = func() { f.Clock.Set(cacheNow.Add(30 * time.Second)) }
		f.event(cacheNow.Add(-400*time.Millisecond), "value", "10")
		f.usage(c)
		if f.cache.puts != 0 {
			t.Errorf("unsettled event: got = %d puts, want = 0", f.cache.puts)
		}
	})
	// source: spec/services/cache_service_spec.rb:64
	t.Run("nothing is cached once the period is over", func(t *testing.T) {
		t.Parallel()
		f, c := newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"1"}`)
		f.event(settled, "value", "10")
		sc := f.usageScope(f.sub, f.Now())
		f.Clock.Set(sc.Boundaries.Charges.To)
		f.run(sc, c, fees.Options{CurrentUsage: true, UsageCache: f.cache})
		if f.cache.puts != 0 {
			t.Errorf("got = %d puts, want = 0", f.cache.puts)
		}
	})
	// source: spec/services/subscriptions/charge_cache_service_spec.rb:30
	// source: spec/services/invoices/customer_usage_service_spec.rb:806
	// source: spec/services/invoices/customer_usage_service_spec.rb:839
	t.Run("full usage has its own entry", func(t *testing.T) {
		t.Parallel()
		f, c := newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"1"}`)
		f.event(settled, "value", "10")
		f.usage(c)
		f.usage(c, func(o *fees.Options) { o.FullUsage = true })
		if f.cache.puts != 2 || f.cache.hits != 0 || f.cache.keys[0] == f.cache.keys[1] {
			t.Errorf("got = %d puts, %d hits, keys %q, want = two distinct entries", f.cache.puts, f.cache.hits, f.cache.keys)
		}
	})
	// source: spec/services/subscriptions/charge_cache_service_spec.rb:13
	t.Run("a changed charge misses", func(t *testing.T) {
		t.Parallel()
		f, c := newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"1"}`)
		f.event(settled, "value", "10")
		f.usage(c)
		changed := *c
		changed.UpdatedAt = c.UpdatedAt.Add(time.Second)
		f.usage(&changed)
		if f.cache.hits != 0 || f.cache.puts != 2 {
			t.Errorf("got = %d hits and %d puts, want = 0 and 2", f.cache.hits, f.cache.puts)
		}
	})
	// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:145
	// source: spec/services/subscriptions/charge_cache_service_spec.rb:21
	// source: spec/services/fees/charge_service_spec.rb:3971
	// source: spec/services/fees/charge_service_spec.rb:3987
	t.Run("one entry per filter", func(t *testing.T) {
		t.Parallel()
		props := `{"amount":"1"}`
		region := func(m *catalog.BillableMetric) {
			m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](core.UUIDv7Generator{}), Key: "region", Values: []string{"europe", "usa"}}}
		}
		f, c := newCacheFixtureOf(t, region, catalog.ChargeModelStandard, props, func(c *catalog.Charge) {
			c.Filters = []catalog.ChargeFilter{
				billingtest.ChargeFilter(map[string][]string{"region": {"europe"}}, props),
				billingtest.ChargeFilter(map[string][]string{"region": {"usa"}}, props),
			}
		})
		f.event(settled, "region", "europe", "value", "10")
		first := f.usage(c)
		// Europe has an event, the usa filter and the default bucket none:
		// three entries, the latter two stamped with the settle bound.
		if f.cache.puts != 3 {
			t.Fatalf("got = %d puts, want = 3", f.cache.puts)
		}
		f.event(cacheNow, "region", "usa", "value", "5")
		second := f.usage(c)
		// The usa event invalidates the usa entry only, which is recomputed
		// and, its event not settled yet, not cached again.
		if f.cache.puts != 3 {
			t.Errorf("got = %d puts, want = 3", f.cache.puts)
		}
		if u := byFilterFees(second, c.Filters[1].ID); u != "5" {
			t.Errorf("usa units: got = %s, want = 5", u)
		}
		if a, b := byFilterFees(first, c.Filters[0].ID), byFilterFees(second, c.Filters[0].ID); a != "10" || b != "10" {
			t.Errorf("europe units: got = %s then %s, want = 10 both times", a, b)
		}
	})
	// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:160
	// source: spec/services/fees/charge_service_spec.rb:3912
	t.Run("breakdowns come back from the cache", func(t *testing.T) {
		t.Parallel()
		f, c := newCacheFixture(t, catalog.ChargeModelStandard, `{"amount":"20","presentation_group_keys":[{"value":"region"}]}`)
		f.event(settled, "region", "eu", "value", "10")
		f.event(settled, "region", "us", "value", "5")
		f.usage(c)
		got := f.usage(c)
		if f.cache.hits != 1 || len(got) != 1 {
			t.Fatalf("got = %d hits and %d fees, want = 1 and 1", f.cache.hits, len(got))
		}
		checkBreakdowns(t, "cached", got[0].PresentationBreakdowns, "region=eu:10;region=us:5")
	})
	// source: spec/services/fees/charge_service_spec.rb:3595
	// source: spec/services/fees/charge_service_spec.rb:3602
	// source: spec/services/fees/charge_service_spec.rb:3630
	// source: spec/services/fees/charge_service_spec.rb:3666
	// source: spec/services/fees/charge_service_spec.rb:3696
	// source: spec/services/fees/charge_service_spec.rb:3732
	// source: spec/services/fees/charge_service_spec.rb:3772
	// source: spec/services/fees/charge_service_spec.rb:3805
	// source: spec/services/fees/charge_service_spec.rb:3841
	t.Run("zero fees", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			model catalog.ChargeModel
			props string
		}{
			{catalog.ChargeModelStandard, `{"amount":"1"}`},
			{catalog.ChargeModelGraduated, `{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"2","flat_amount":"1"},{"from_value":11,"to_value":null,"per_unit_amount":"1","flat_amount":"0"}]}`},
			{catalog.ChargeModelPackage, `{"amount":"100","free_units":0,"package_size":10}`},
			{catalog.ChargeModelPercentage, `{"rate":"1","fixed_amount":"2"}`},
			{catalog.ChargeModelPercentage, `{"rate":"1","per_transaction_min_amount":"1","per_transaction_max_amount":"5"}`},
			{catalog.ChargeModelVolume, `{"volume_ranges":[{"from_value":0,"to_value":100,"per_unit_amount":"2","flat_amount":"1"},{"from_value":101,"to_value":null,"per_unit_amount":"1","flat_amount":"0"}]}`},
			{catalog.ChargeModelGraduatedPercentage, `{"graduated_percentage_ranges":[{"from_value":0,"to_value":10,"rate":"1","flat_amount":"200"},{"from_value":11,"to_value":null,"rate":"0.5","flat_amount":"300"}]}`},
			{catalog.ChargeModelStandard, `{"amount":"1","pricing_group_keys":["cloud"]}`},
		} {
			f, c := newCacheFixture(t, tc.model, tc.props)
			first := f.usage(c)
			second := f.usage(c)
			if f.cache.puts != 1 || f.cache.hits != 1 {
				t.Errorf("%s %s: got = %d puts and %d hits, want = 1 and 1", tc.model, tc.props, f.cache.puts, f.cache.hits)
			}
			if len(first) != 1 || len(second) != 1 {
				t.Errorf("%s %s: got = %d then %d fees, want = 1", tc.model, tc.props, len(first), len(second))
				continue
			}
			a, b := first[0], second[0]
			if !a.Units.IsZero() || a.EventsCount != 0 || !b.Units.IsZero() || b.Amount != a.Amount || string(b.AmountDetails) != string(a.AmountDetails) || len(b.GroupedBy) != len(a.GroupedBy) {
				t.Errorf("%s %s: got = %+v then %+v, want = the same fee without units", tc.model, tc.props, a, b)
			}
		}
	})
}

func byFilterFees(fs []*invoice.Fee, id core.ChargeFilterID) string {
	for _, f := range fs {
		if f.ChargeFilterID == id {
			return f.Units.Reduce().String()
		}
	}
	return "none"
}
