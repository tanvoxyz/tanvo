package usage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachetest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
)

// TestUsageFilters narrows a current usage by group and by skipping the
// grouping, through the charge usage cache, which a group filter bypasses
// and skipped grouping and a presentation filter key apart (§4).
func TestUsageFilters(t *testing.T) {
	t.Parallel()
	june := billingtest.Date(2025, time.June, 15)
	cases := []struct {
		name   string
		source string
		opts   func(f *fixture, c *catalog.Charge) usage.Options
		// fees is the number of fees of the grouped charge, amount the
		// usage amount and entries the number of cache entries written.
		fees     int
		amount   int64
		entries  int
		prorated bool
		err      error
	}{
		{
			name: "grouped", source: "spec/scenarios/customer_usage_spec.rb:313",
			opts: func(*fixture, *catalog.Charge) usage.Options { return usage.Options{} },
			fees: 3, amount: 5_00, entries: 1,
		},
		{
			name: "group filter", source: "spec/scenarios/customer_usage_spec.rb:256, spec/services/invoices/customer_usage_service_spec.rb:567, :977",
			opts: func(*fixture, *catalog.Charge) usage.Options {
				return usage.Options{Filters: fees.UsageFilters{Group: map[string][]string{"region": {"eu"}}}}
			},
			fees: 2, amount: 3_00,
		},
		{
			name: "group filter of two values",
			opts: func(*fixture, *catalog.Charge) usage.Options {
				return usage.Options{Filters: fees.UsageFilters{Group: map[string][]string{"region": {"eu", "us"}}}}
			},
			fees: 3, amount: 5_00,
		},
		{
			name: "grouping skipped", source: "spec/scenarios/customer_usage_spec.rb:338, spec/services/invoices/customer_usage_service_spec.rb:851",
			opts: func(*fixture, *catalog.Charge) usage.Options {
				return usage.Options{Filters: fees.UsageFilters{SkipGrouping: true}}
			},
			fees: 1, amount: 5_00, entries: 1,
		},
		{
			name: "presentation filter",
			opts: func(*fixture, *catalog.Charge) usage.Options {
				return usage.Options{Filters: fees.UsageFilters{Presentation: []string{}}}
			},
			fees: 3, amount: 5_00, entries: 1,
		},
		{
			name: "full usage with a group filter", source: "spec/requests/api/v1/customers/usage_controller_spec.rb:402",
			opts: func(*fixture, *catalog.Charge) usage.Options {
				return usage.Options{FullUsage: true, Filters: fees.UsageFilters{Group: map[string][]string{"region": {"eu"}}}}
			},
			// The previous period's event of user 0 counts too.
			fees: 3, amount: 4_00,
		},
		{
			name: "full usage with grouping skipped", source: "spec/services/invoices/customer_usage_service_spec.rb:1060",
			opts: func(_ *fixture, c *catalog.Charge) usage.Options {
				return usage.Options{FullUsage: true, ChargeIDs: []core.ChargeID{c.ID}, Filters: fees.UsageFilters{SkipGrouping: true}}
			},
			fees: 1, amount: 6_00, entries: 1,
		},
		{
			name: "full usage of a prorated charge", source: "app/services/invoices/customer_usage_service.rb:366-375",
			opts: func(_ *fixture, c *catalog.Charge) usage.Options {
				return usage.Options{FullUsage: true, ChargeIDs: []core.ChargeID{c.ID}}
			},
			prorated: true, err: usage.ErrFullUsageNotAllowed,
		},
		{
			name: "full usage without a charge or group filter", source: "app/services/invoices/customer_usage_service.rb:366-375",
			opts: func(*fixture, *catalog.Charge) usage.Options { return usage.Options{FullUsage: true} },
			err:  usage.ErrFullUsageNotAllowed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, june, "1")
			cache := cachetest.New(f.Now)
			d := f.Deps
			d.UsageCache = kernel.NewUsageCache(cache)
			f.svc = usage.New(d)
			m := f.Metric("seats", catalog.AggregationTypeCount, "")
			c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1","pricing_group_keys":["region","user"]}`,
				func(c *catalog.Charge) { c.Prorated = tc.prorated })
			f.Clock.Set(june.Add(-time.Hour))
			f.Event(f.sub, "seats", june.AddDate(0, -1, 0), "region", "eu", "user", "0")
			at := june.Add(-2 * time.Hour)
			f.Event(f.sub, "seats", at, "region", "eu", "user", "1")
			f.Event(f.sub, "seats", at, "region", "eu", "user", "1")
			f.Event(f.sub, "seats", at, "region", "eu", "user", "2")
			f.Event(f.sub, "seats", at, "region", "us", "user", "3")
			f.Event(f.sub, "seats", at, "region", "us", "user", "3")
			f.Clock.Set(june)
			opts := tc.opts(f, c)
			if opts.ChargeIDs == nil && !opts.FullUsage {
				opts.ChargeIDs = []core.ChargeID{c.ID}
			}
			opts.SkipTaxes = true
			u, err := f.usageOf(f.sub, opts)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("got err = %v, want = %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var n int
			for _, fee := range u.Fees {
				if fee.ChargeID == c.ID {
					n++
				}
			}
			if n != tc.fees || u.Amount != tc.amount {
				t.Errorf("got = %d fees for %d, want = %d fees for %d", n, u.Amount, tc.fees, tc.amount)
			}
			if cache.Len() != tc.entries {
				t.Errorf("cache entries: got = %d, want = %d", cache.Len(), tc.entries)
			}
			// The entries of a usage shaped by its filters are never
			// served to the unfiltered usage, which writes its own.
			if flt := opts.Filters; flt.SkipGrouping || flt.Presentation != nil {
				plain := opts
				plain.Filters = fees.UsageFilters{}
				if _, err := f.usageOf(f.sub, plain); err != nil {
					t.Fatal(err)
				}
				if cache.Len() != tc.entries+1 {
					t.Errorf("cache entries after the unfiltered usage: got = %d, want = %d", cache.Len(), tc.entries+1)
				}
			}
		})
	}
}
