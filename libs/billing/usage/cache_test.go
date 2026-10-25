package usage_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachetest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// TestUsageCacheGate checks which current usages read and fill the usage
// cache of the engine (lago charge_cache_enabled?,
// app/services/invoices/customer_usage_service.rb:346-364).
func TestUsageCacheGate(t *testing.T) {
	t.Parallel()
	june := billingtest.Date(2025, time.June, 15)
	cases := []struct {
		name string
		opts func(f *fixture) usage.Options
		want int
	}{
		// source: spec/services/invoices/customer_usage_service_spec.rb:71
		// source: spec/services/invoices/customer_usage_service_spec.rb:916
		{"not filtered", func(*fixture) usage.Options { return usage.Options{} }, 1},
		// source: spec/services/invoices/customer_usage_service_spec.rb:934
		{"filtered by charge", func(f *fixture) usage.Options { return usage.Options{ChargeIDs: []core.ChargeID{f.charge.ID}} }, 1},
		// source: spec/services/invoices/customer_usage_service_spec.rb:946
		// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:29
		{"disabled by the caller", func(*fixture) usage.Options { return usage.Options{SkipCache: true} }, 0},
		{"capped by an instant", func(f *fixture) usage.Options { return usage.Options{MaxTimestamp: f.Now()} }, 0},
		// source: spec/services/invoices/customer_usage_service_spec.rb:1005
		{"full usage", func(f *fixture) usage.Options {
			return usage.Options{ChargeIDs: []core.ChargeID{f.charge.ID}, FullUsage: true}
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, june, "10")
			cache := cachetest.New(f.Now)
			d := f.Deps
			d.UsageCache = kernel.NewUsageCache(cache)
			f.svc = usage.New(d)
			f.Clock.Set(june.Add(-time.Hour))
			f.events(f.sub, 3, june.Add(-2*time.Hour))
			f.Clock.Set(june)
			opts := tc.opts(f)
			opts.SkipTaxes = true
			first, err := f.usageOf(f.sub, opts)
			if err != nil {
				t.Fatal(err)
			}
			if cache.Len() != tc.want {
				t.Fatalf("got = %d entries, want = %d", cache.Len(), tc.want)
			}
			// A second read gives the same usage, from the cache or not.
			second, err := f.usageOf(f.sub, opts)
			if err != nil {
				t.Fatal(err)
			}
			if first.Amount != second.Amount || first.Amount != 30_00 {
				t.Errorf("got = %d then %d, want = 3000 both times", first.Amount, second.Amount)
			}
		})
	}

	// source: spec/services/invoices/customer_usage_service_spec.rb:121
	t.Run("projects the cached fees", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, june, "10")
		d := f.Deps
		d.UsageCache = kernel.NewUsageCache(cachetest.New(f.Now))
		f.svc = usage.New(d)
		f.Clock.Set(june.Add(-time.Hour))
		f.events(f.sub, 3, june.Add(-2*time.Hour))
		f.Clock.Set(june)
		opts := usage.Options{SkipTaxes: true, Projection: true}
		a, err := f.usageOf(f.sub, opts)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f.usageOf(f.sub, opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Projections) != 1 || len(b.Projections) != 1 || a.Projections[0].Amount != b.Projections[0].Amount {
			t.Errorf("got = %+v then %+v, want = the same projection", a.Projections, b.Projections)
		}
	})
}
