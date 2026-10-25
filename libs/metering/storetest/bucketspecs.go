package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// bucketSpec is a lago usage bucket spec ported to events: the buckets the
// spec inserts are the buckets of the events.
type bucketSpec struct {
	name, source string
	metric       metering.Metric
	dims         []string
	// filters are the metric filters; their declared values fold the
	// others into [metering.BucketOtherValue].
	filters []metering.MetricFilter
	events  []metering.Event
	check   func(ctx context.Context, s metering.Store, r metering.BucketReader) error
	// noDefs leaves the stores without definitions; fails marks a spec
	// expecting an error before any read.
	noDefs, fails bool
}

var (
	specFrom = time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	// specTo is the inclusive end of a month long period, as a billing
	// period ends on the last millisecond of its last day.
	specTo = specFrom.AddDate(0, 1, 0).Add(-time.Millisecond)
)

// specEvent returns an event of the spec subscription at t, with the
// property pairs given as "key", "value"…
func specEvent(tx string, t time.Time, pairs ...string) metering.Event {
	props := metering.Properties{}
	for i := 0; i+1 < len(pairs); i += 2 {
		props[pairs[i]] = pairs[i+1]
	}
	return metering.Event{
		OrganizationID: DefaultOrganizationID, SubscriptionID: DefaultSubscriptionID, Code: DefaultCode,
		TransactionID: tx, Timestamp: t, Properties: props, IngestedAt: t,
	}
}

func withAmount(e metering.Event, amount string) metering.Event {
	a := money.MustParse(amount)
	e.PreciseTotalAmountCents = &a
	return e
}

func specQuery(numeric bool) metering.Query {
	q := metering.Query{
		OrganizationID: DefaultOrganizationID, SubscriptionID: DefaultSubscriptionID, Code: DefaultCode,
		From: specFrom, To: specTo,
	}
	if numeric {
		q.Numeric, q.Property = true, "value"
	}
	return q
}

func wantDecimal(what string, got money.Decimal, want string) error {
	if got.Cmp(money.MustParse(want)) != 0 {
		return fmt.Errorf("%s got = %s, want = %s", what, got, want)
	}
	return nil
}

func wantInt(what string, got, want int64) error {
	if got != want {
		return fmt.Errorf("%s got = %d, want = %d", what, got, want)
	}
	return nil
}

// groupedString renders grouped aggregates sorted, as "key=value:value/count".
func groupedString(rows []metering.GroupedAggregate) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var keys []string
		for k, v := range r.Groups {
			s := "<nil>"
			if v != nil {
				s = *v
			}
			keys = append(keys, k+"="+s)
		}
		slices.Sort(keys)
		out = append(out, strings.Join(keys, ",")+":"+r.Value.Reduce().String()+"/"+fmt.Sprint(r.Count))
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

func wantGrouped(what string, rows []metering.GroupedAggregate, want string) error {
	if got := groupedString(rows); got != want {
		return fmt.Errorf("%s got = %q, want = %q", what, got, want)
	}
	return nil
}

var (
	sumMetric    = metering.Metric{Code: DefaultCode, Aggregation: metering.AggregationSum, FieldName: "value"}
	countMetric  = metering.Metric{Code: DefaultCode, Aggregation: metering.AggregationCount}
	maxMetric    = metering.Metric{Code: DefaultCode, Aggregation: metering.AggregationMax, FieldName: "value"}
	latestMetric = metering.Metric{Code: DefaultCode, Aggregation: metering.AggregationLatest, FieldName: "value"}
)

func fetchBucketSpecs() []bucketSpec {
	m := func(d time.Duration) time.Time { return specFrom.Add(d) }
	const src = "spec/services/realtime_usage/fetch_buckets_service_spec.rb:"
	sum := func(want, count string) func(context.Context, metering.Store, metering.BucketReader) error {
		return func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
			t, err := s.Sum(ctx, specQuery(true))
			if err != nil {
				return err
			}
			var n int64
			_, _ = fmt.Sscan(count, &n)
			return errors.Join(wantDecimal("sum", t.Value, want), wantInt("count", t.Count, n))
		}
	}
	return []bucketSpec{
		{
			name: "sums every bucket of the window into the charge totals", source: src + "63", metric: sumMetric,
			events: []metering.Event{
				specEvent("a", m(0), "value", "4"), specEvent("b", m(time.Minute), "value", "6"),
				specEvent("c", m(15*time.Minute), "value", "5.5"),
				specEvent("d", specTo.Add(15*time.Minute), "value", "99"),
			},
			check: sum("15.5", "3"),
		},
		{
			// Lago re-upserts the bucket; Tanvo's buckets hold the latest
			// copy of a re-sent event, so a deduplicating organization is
			// served from them too.
			// source: spec/services/events/stores/provider_spec.rb:144, 316
			name: "a re-sent event is not counted twice", source: src + "74, 424", metric: sumMetric,
			events: []metering.Event{
				specEvent("a", m(0), "value", "10"),
				func() metering.Event { e := specEvent("a", m(0), "value", "12"); e.IngestedAt = m(time.Hour); return e }(),
			},
			check: sum("12", "1"),
		},
		{
			name: "keys the totals by charge filter", source: src + "93", metric: sumMetric, dims: []string{"region"},
			events: []metering.Event{
				specEvent("a", m(0), "value", "10"),
				specEvent("b", m(time.Minute), "value", "3", "region", "eu"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				def, flt := specQuery(true), specQuery(true)
				def.Filter.Ignored = []map[string][]string{{"region": {"eu"}}}
				flt.Filter.Matching = map[string][]string{"region": {"eu"}}
				d, err := s.Sum(ctx, def)
				if err != nil {
					return err
				}
				f, err := s.Sum(ctx, flt)
				if err != nil {
					return err
				}
				return errors.Join(wantDecimal("default sum", d.Value, "10"), wantDecimal("filter sum", f.Value, "3"))
			},
		},
		{
			name: "returns one result per group, and the total across them", source: src + "105", metric: sumMetric, dims: []string{"region"},
			events: []metering.Event{
				specEvent("a", m(0), "value", "6", "region", "eu"), specEvent("b", m(0), "value", "4", "region", "eu"),
				specEvent("c", m(15*time.Minute), "value", "4", "region", "eu"),
				specEvent("d", m(time.Minute), "value", "1", "region", "us"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				g, err := s.GroupedSum(ctx, specQuery(true), []string{"region"})
				if err != nil {
					return err
				}
				t, err := s.Sum(ctx, specQuery(true))
				if err != nil {
					return err
				}
				return errors.Join(wantGrouped("grouped sum", g, "region=eu:14/3 region=us:1/1"), wantDecimal("sum", t.Value, "15"))
			},
		},
		{
			name: "normalizes the absent group value to nil", source: src + "122", metric: sumMetric, dims: []string{"region"},
			events: []metering.Event{specEvent("a", m(0), "value", "2", "region", "")},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				g, err := s.GroupedSum(ctx, specQuery(true), []string{"region"})
				if err != nil {
					return err
				}
				return wantGrouped("grouped sum", g, "region=<nil>:2/1")
			},
		},
		{
			name: "sums the precise amounts of the window, per charge and per group", source: src + "134", metric: sumMetric, dims: []string{"region"},
			events: []metering.Event{
				withAmount(specEvent("a", m(0), "value", "1", "region", "eu"), "1000.000000000000001"),
				withAmount(specEvent("b", m(15*time.Minute), "value", "1", "region", "eu"), "250.5"),
				withAmount(specEvent("c", m(0), "value", "1", "region", "us"), "10"),
				withAmount(specEvent("d", specTo.Add(15*time.Minute), "value", "1", "region", "us"), "99999"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				q := specQuery(false)
				total, err := s.SumPreciseTotalAmountCents(ctx, q)
				if err != nil {
					return err
				}
				g, err := s.GroupedSumPreciseTotalAmountCents(ctx, q, []string{"region"})
				if err != nil {
					return err
				}
				return errors.Join(wantDecimal("amount", total, "1260.500000000000001"),
					wantGrouped("grouped amount", g, "region=eu:1250.500000000000001/2 region=us:10/1"))
			},
		},
		{
			name: "keeps the largest bucket rather than adding them up, and counts every event", source: src + "156", metric: maxMetric,
			events: []metering.Event{
				specEvent("a", m(0), "value", "10"), specEvent("b", m(time.Minute), "value", "1"),
				specEvent("c", m(15*time.Minute), "value", "4"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				t, err := s.Max(ctx, specQuery(true))
				if err != nil {
					return err
				}
				return errors.Join(wantDecimal("max", t.Value, "10"), wantInt("count", t.Count, 3))
			},
		},
		{
			name: "takes the max across the groups as the charge total, and per group below it", source: src + "165", metric: maxMetric, dims: []string{"region"},
			events: []metering.Event{
				specEvent("a", m(0), "value", "10", "region", "eu"), specEvent("b", m(15*time.Minute), "value", "4", "region", "eu"),
				specEvent("c", m(0), "value", "12", "region", "us"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				g, err := s.GroupedMax(ctx, specQuery(true), []string{"region"})
				if err != nil {
					return err
				}
				t, err := s.Max(ctx, specQuery(true))
				if err != nil {
					return err
				}
				return errors.Join(wantGrouped("grouped max", g, "region=eu:10/2 region=us:12/1"), wantDecimal("max", t.Value, "12"))
			},
		},
		{
			name: "keeps the units of the most recent bucket, whatever their order", source: src + "181", metric: latestMetric,
			events: []metering.Event{
				specEvent("b", m(15*time.Minute), "value", "4"),
				specEvent("a", m(0), "value", "10"), specEvent("c", m(time.Minute), "value", "10"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				l, err := s.Last(ctx, specQuery(true))
				if err != nil {
					return err
				}
				if l.Value == nil {
					return errors.New("latest got = nil, want = 4")
				}
				return errors.Join(wantDecimal("latest", *l.Value, "4"), wantInt("count", l.Count, 3))
			},
		},
		{
			name: "picks the group holding the last event when two of them close on the same bucket", source: src + "192", metric: latestMetric, dims: []string{"region"},
			events: []metering.Event{
				specEvent("a", m(5*time.Minute), "value", "10", "region", "eu"), specEvent("b", m(10*time.Minute), "value", "10", "region", "eu"),
				specEvent("c", m(12*time.Minute), "value", "4", "region", "us"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				l, err := s.Last(ctx, specQuery(true))
				if err != nil {
					return err
				}
				if l.Value == nil {
					return errors.New("latest got = nil, want = 4")
				}
				return errors.Join(wantDecimal("latest", *l.Value, "4"), wantInt("count", l.Count, 3))
			},
		},
		{
			name: "takes the latest across the groups as the charge total, and per group below it", source: src + "201", metric: latestMetric, dims: []string{"region"},
			events: []metering.Event{
				specEvent("a", m(0), "value", "10", "region", "eu"), specEvent("b", m(30*time.Minute), "value", "4", "region", "eu"),
				specEvent("c", m(15*time.Minute), "value", "12", "region", "us"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				g, err := s.GroupedLast(ctx, specQuery(true), []string{"region"}, []string{"region"})
				if err != nil {
					return err
				}
				l, err := s.Last(ctx, specQuery(true))
				if err != nil {
					return err
				}
				if l.Value == nil {
					return errors.New("latest got = nil, want = 4")
				}
				return errors.Join(wantGrouped("grouped latest", g, "region=eu:4/2 region=us:12/1"), wantDecimal("latest", *l.Value, "4"))
			},
		},
		{
			name: "when the window holds no bucket, returns nothing", source: src + "249", metric: sumMetric,
			events: []metering.Event{specEvent("a", specFrom.Add(-24*time.Hour), "value", "10")},
			check:  sum("0", "0"),
		},
		{
			// Lago floors the start to the enclosing bucket; Tanvo reads the
			// partial bucket from the events, so only the events of the
			// window count.
			name: "when the window starts inside a bucket, counts the events of the window only", source: src + "347, 365", metric: sumMetric,
			events: []metering.Event{
				specEvent("a", m(0), "value", "7"), specEvent("b", m(2*time.Second), "value", "3"),
				specEvent("c", m(time.Hour), "value", "1"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				q := specQuery(true)
				q.From = specFrom.Add(time.Second)
				t, err := s.Sum(ctx, q)
				if err != nil {
					return err
				}
				return wantDecimal("sum", t.Value, "4")
			},
		},
		{
			// Lago tops the end to the enclosing bucket; Tanvo reads the
			// partial bucket from the events.
			name: "when the window ends inside a bucket, counts the events of the window only", source: src + "377", metric: sumMetric,
			events: []metering.Event{
				specEvent("a", specTo.Add(-10*time.Minute), "value", "3"),
				specEvent("b", specTo.Add(-time.Second), "value", "2"),
				specEvent("c", specFrom, "value", "1"),
			},
			check: func(ctx context.Context, s metering.Store, _ metering.BucketReader) error {
				q := specQuery(true)
				q.To = specTo.Add(-5 * time.Second)
				t, err := s.Sum(ctx, q)
				if err != nil {
					return err
				}
				return wantDecimal("sum", t.Value, "4")
			},
		},
		{
			name: "when the window ends on a bucket wall, leaves out the bucket opening at the end", source: src + "387", metric: sumMetric,
			events: []metering.Event{specEvent("a", specTo.Add(time.Millisecond), "value", "3")},
			check:  sum("0", "0"),
		},
	}
}

func hourlySpecs() []bucketSpec {
	const src = "spec/services/realtime_usage/hourly_breakdown_service_spec.rb:"
	day := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	from, to := at(9, 20), at(12, 5)
	europe := []metering.HourlySeries{
		{Key: "europe", Filter: metering.Filter{Matching: map[string][]string{"region": {"eu"}}}},
		{Key: "", Filter: metering.Filter{Ignored: []map[string][]string{{"region": {"eu"}}}}},
	}
	request := func(series []metering.HourlySeries) metering.HourlyRequest {
		return metering.HourlyRequest{Query: specQuery(false), Metric: sumMetric, From: from, To: to, Series: series}
	}
	run := func(req metering.HourlyRequest, want func(metering.HourlyUsageResult) error) func(context.Context, metering.Store, metering.BucketReader) error {
		return func(ctx context.Context, s metering.Store, r metering.BucketReader) error {
			res, err := metering.HourlyUsage(ctx, s, r, req)
			if err != nil {
				return err
			}
			return want(res)
		}
	}
	fails := func(req metering.HourlyRequest, target error) func(context.Context, metering.Store, metering.BucketReader) error {
		return func(ctx context.Context, s metering.Store, r metering.BucketReader) error {
			if _, err := metering.HourlyUsage(ctx, s, r, req); !errors.Is(err, target) {
				return fmt.Errorf("error got = %w, want = %w", err, target)
			}
			return nil
		}
	}
	hourUnits := func(res metering.HourlyUsageResult) string {
		var out []string
		for _, h := range res.Hours {
			out = append(out, h.Time.UTC().Format("15:04")+"="+h.Units.Reduce().String())
		}
		return strings.Join(out, " ")
	}
	usages := func(res metering.HourlyUsageResult) string {
		var out []string
		for _, h := range res.Hours {
			var u []string
			for _, s := range h.Usages {
				u = append(u, s.Units.Reduce().String())
			}
			out = append(out, "["+strings.Join(u, ",")+"]")
		}
		return strings.Join(out, " ")
	}
	series := func(res metering.HourlyUsageResult) string {
		var out []string
		for _, s := range res.Series {
			k := s.Key
			if s.Other != (k == metering.HourlyOtherKey) {
				k += "<mismatch>"
			}
			out = append(out, fmt.Sprintf("%s:%s/%d", k, s.Units.Reduce(), s.EventsCount))
		}
		return strings.Join(out, " ")
	}
	want := func(what, got, want string) error {
		if got != want {
			return fmt.Errorf("%s got = %q, want = %q", what, got, want)
		}
		return nil
	}
	inWindow := []metering.Event{
		specEvent("a", at(9, 30), "value", "10", "region", "eu"),
		specEvent("b", at(9, 45), "value", "5", "region", "eu"),
		specEvent("c", at(11, 0), "value", "3"),
		specEvent("d", at(12, 0), "value", "1", "region", "eu"),
	}
	dims := []string{"region", "zone"}
	return []bucketSpec{
		{
			name: "returns one gap-filled hour per hour of the window, broken down by charge filter", source: src + "49, 64, 76",
			metric: sumMetric, dims: dims, events: inWindow,
			check: run(request(europe), func(res metering.HourlyUsageResult) error {
				return errors.Join(
					want("hours", hourUnits(res), "09:00=15 10:00=0 11:00=3 12:00=1"),
					want("window", res.From.Format(time.TimeOnly)+"-"+res.To.Format(time.TimeOnly), "09:00:00-12:15:00"),
					want("series", series(res), "europe:16/3 :3/1"),
					want("usages", usages(res), "[15,0] [0,0] [0,3] [1,0]"),
					want("aggregation", string(res.Aggregation), string(metering.AggregationSum)),
					want("timezone", res.Location, "UTC"),
					want("last ingested at", res.LastIngestedAt.Format(time.TimeOnly), "12:00:00"),
				)
			}),
		},
		{
			name: "folds every filter past the biggest ones into one series", source: src + "95, 102",
			metric: sumMetric, dims: dims,
			events: []metering.Event{
				specEvent("a", at(9, 30), "value", "100", "region", "a"),
				specEvent("b", at(9, 30), "value", "50", "region", "b"),
				specEvent("c", at(10, 30), "value", "7", "region", "c"),
				specEvent("d", at(11, 30), "value", "3", "region", "d"),
			},
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				var ss []metering.HourlySeries
				for _, k := range []string{"a", "b", "c", "d"} {
					ss = append(ss, metering.HourlySeries{Key: k, Filter: metering.Filter{Matching: map[string][]string{"region": {k}}}})
				}
				req := request(ss)
				req.MaxSeries = 2
				return run(req, func(res metering.HourlyUsageResult) error {
					return errors.Join(
						want("series", series(res), "a:100/1 b:50/1 __other__:10/2"),
						want("usages", usages(res), "[100,50,0] [0,0,7] [0,0,3] [0,0,0]"),
						want("hours", hourUnits(res), "09:00=150 10:00=7 11:00=3 12:00=0"),
					)
				})
			}(),
		},
		{
			// Lago buckets carry the internal subscription, so the events of
			// the subscription an upgrade replaced never reach the window;
			// Tanvo keys by the shared external id and starts the window at
			// the subscription start, reading its partial bucket from events.
			name: "starts the window at the subscription start, inside an hour and a bucket", source: "app/services/realtime_usage/hourly_breakdown_service.rb:75",
			metric: sumMetric, dims: dims,
			events: []metering.Event{
				specEvent("a", at(10, 10), "value", "40"),
				specEvent("b", at(10, 21), "value", "2"),
				specEvent("c", at(10, 40), "value", "5"),
				specEvent("d", at(11, 0), "value", "1"),
			},
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				req := request(europe)
				req.Start = at(10, 20)
				return run(req, func(res metering.HourlyUsageResult) error {
					return errors.Join(
						want("window", res.From.Format(time.TimeOnly)+"-"+res.To.Format(time.TimeOnly), "10:20:00-12:15:00"),
						want("hours", hourUnits(res), "10:00=7 11:00=1 12:00=0"),
						want("series", series(res), ":8/3"),
					)
				})
			}(),
		},
		{
			// Charge filters only take declared values
			// (app/models/charge_filter_value.rb:25), so folding the others
			// into one bucket value keeps the default series exact.
			name: "breaks down charge filters over a dimension folding undeclared values", source: src + "64",
			metric: sumMetric, filters: []metering.MetricFilter{{Key: "region", Values: []string{"asia", "eu", "us"}}},
			events: []metering.Event{
				specEvent("a", at(9, 30), "value", "10", "region", "eu"),
				specEvent("b", at(9, 40), "value", "20", "region", "us"),
				specEvent("c", at(10, 0), "value", "1", "region", "asia"),
				specEvent("d", at(10, 10), "value", "2", "region", "undeclared"),
				specEvent("e", at(10, 20), "value", "4", "region", "other"),
				specEvent("f", at(11, 0), "value", "8"),
				specEvent("g", at(11, 10), "value", "x", "region", "eu"),
			},
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				declared := []metering.MetricFilter{{Key: "region", Values: []string{"asia", "eu", "us"}}}
				ss, err := metering.HourlySeriesFor([]metering.ChargeFilter{
					{ID: "f-eu", Values: map[string][]string{"region": {"eu"}}},
					{ID: "f-us", Values: map[string][]string{"region": {"us"}}},
				}, declared)
				if err != nil {
					return func(context.Context, metering.Store, metering.BucketReader) error { return err }
				}
				return run(request(ss), func(res metering.HourlyUsageResult) error {
					return errors.Join(
						want("series", series(res), "f-us:20/1 :15/4 f-eu:10/2"),
						want("hours", hourUnits(res), "09:00=30 10:00=7 11:00=8 12:00=0"),
					)
				})
			}(),
		},
		{
			name: "keeps a dimension value too long for an index key", source: src + "152", metric: sumMetric, dims: dims,
			events: []metering.Event{
				specEvent("a", at(9, 30), "value", "4", "zone", strings.Repeat("z", 10<<10)),
				specEvent("b", at(9, 31), "value", "6", "zone", strings.Repeat("y", 10<<10)),
			},
			check: run(request(europe), func(res metering.HourlyUsageResult) error {
				return want("series", series(res), ":10/2")
			}),
		},
		{
			name: "ignores the buckets outside the window", source: src + "118", metric: sumMetric, dims: dims,
			events: []metering.Event{specEvent("a", at(8, 45), "value", "99"), specEvent("b", at(12, 15), "value", "77")},
			check: run(request(europe), func(res metering.HourlyUsageResult) error {
				return errors.Join(want("series", series(res), ""), want("hours", hourUnits(res), "09:00=0 10:00=0 11:00=0 12:00=0"))
			}),
		},
		{
			name: "sums several grouped rows of the same filter into the filter total", source: src + "152", metric: sumMetric, dims: dims,
			events: []metering.Event{specEvent("a", at(9, 30), "value", "4", "zone", "eu"), specEvent("b", at(9, 31), "value", "6", "zone", "us")},
			check: run(request(europe), func(res metering.HourlyUsageResult) error {
				return errors.Join(want("series", series(res), ":10/2"), want("first hour", res.Hours[0].Units.Reduce().String(), "10"))
			}),
		},
		{
			name: "cuts the hours on the walls of a timezone offset by half an hour", source: src + "166", metric: sumMetric, dims: dims,
			events: []metering.Event{specEvent("a", at(9, 15), "value", "2"), specEvent("b", at(9, 45), "value", "8")},
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				req := request(europe)
				loc, err := time.LoadLocation("Asia/Kolkata")
				if err != nil {
					return func(context.Context, metering.Store, metering.BucketReader) error { return err }
				}
				req.Location = loc
				return run(req, func(res metering.HourlyUsageResult) error {
					return errors.Join(want("timezone", res.Location, "Asia/Kolkata"),
						want("hours", hourUnits(res), "08:30=2 09:30=8 10:30=0 11:30=0"))
				})
			}(),
		},
		{
			name: "when to sits inside a bucket, reports the end of that bucket", source: src + "178", metric: sumMetric, dims: dims,
			events: []metering.Event{specEvent("a", at(12, 0), "value", "7")},
			check: run(request(europe), func(res metering.HourlyUsageResult) error {
				return errors.Join(want("to", res.To.Format(time.TimeOnly), "12:15:00"),
					want("last hour", res.Hours[len(res.Hours)-1].Units.Reduce().String(), "7"))
			}),
		},
		{
			name: "when to sits on a bucket wall, keeps it and leaves out the bucket opening on it", source: src + "189", metric: sumMetric, dims: dims,
			events: []metering.Event{specEvent("a", at(12, 0), "value", "7")},
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				req := request(europe)
				req.To = at(12, 0)
				return run(req, func(res metering.HourlyUsageResult) error {
					return errors.Join(want("to", res.To.Format(time.TimeOnly), "12:00:00"),
						want("last hour", res.Hours[len(res.Hours)-1].Time.Format(time.TimeOnly), "11:00:00"),
						want("series", series(res), ""))
				})
			}(),
		},
		{
			name: "counts the events of a count charge", source: src + "49", metric: countMetric, dims: dims, events: inWindow,
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				req := request(europe)
				req.Metric = countMetric
				return run(req, func(res metering.HourlyUsageResult) error {
					return want("hours", hourUnits(res), "09:00=2 10:00=0 11:00=1 12:00=1")
				})
			}(),
		},
		{
			fails: true, name: "fails when the window is longer than the cap", source: src + "213", metric: sumMetric, dims: dims,
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				req := request(europe)
				req.To = from.Add(40 * 24 * time.Hour)
				return fails(req, metering.ErrHourlyWindowTooLong)
			}(),
		},
		{
			fails: true, name: "fails when the charge is not one the buckets can answer", source: src + "231", metric: sumMetric, dims: dims,
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				req := request(europe)
				req.Metric = metering.Metric{Code: DefaultCode, Aggregation: metering.AggregationUniqueCount, FieldName: "value"}
				return fails(req, metering.ErrBucketsUnavailable)
			}(),
		},
		{
			fails: true, name: "fails when the charge aggregates values that do not add up across hours", source: src + "240", metric: sumMetric, dims: dims,
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				req := request(europe)
				req.Metric = maxMetric
				return fails(req, metering.ErrBucketsUnavailable)
			}(),
		},
		{
			fails: true, name: "fails when the window is empty", source: src + "249", metric: sumMetric, dims: dims,
			check: func() func(context.Context, metering.Store, metering.BucketReader) error {
				req := request(europe)
				req.From = to
				return fails(req, metering.ErrHourlyInvalidWindow)
			}(),
		},
		{
			name: "fails when no bucket definition covers a series", source: src + "231", metric: sumMetric,
			check: fails(request(europe), metering.ErrBucketsUnavailable), noDefs: true,
		},
	}
}

// RunBucketSpecs runs the lago usage bucket specs, ported to events, against
// the usage buckets newStore returns: the totals, groups, amounts, maximums
// and latest values read through a [metering.UsageBucketStore], and the
// hourly breakdowns of [metering.HourlyUsage]. The stores receive the
// definitions of each spec's metric with the properties it filters and
// groups on as dimensions.
func RunBucketSpecs(t *testing.T, newStore NewBucketStore) {
	t.Helper()
	for _, spec := range slices.Concat(fetchBucketSpecs(), hourlySpecs()) {
		t.Run(spec.name, func(t *testing.T) {
			defs := []metering.BucketDefinition{
				metering.BucketDefinitionFor(DefaultOrganizationID, spec.metric, spec.filters, spec.dims),
				metering.BucketDefinitionFor(DefaultOrganizationID, countMetric, spec.filters, spec.dims),
			}
			if spec.noDefs {
				defs = nil
			}
			events, reader := newStore(t, defs, spec.events)
			counting := &countingReader{BucketReader: reader}
			if err := spec.check(t.Context(), metering.NewUsageBucketStore(events, counting), counting); err != nil {
				t.Errorf("%s: %v", spec.source, err)
			}
			if !spec.noDefs && !spec.fails && counting.served.Load() == 0 {
				t.Errorf("%s: usage buckets read got = 0, want > 0", spec.source)
			}
		})
	}
}
