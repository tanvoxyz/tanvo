package metering_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// cannedBuckets answers every bucket read with fixed totals: those of the
// charge filter value "unknown" are empty, as buckets holding no row for
// it. It counts the reads.
type cannedBuckets struct {
	totals []metering.BucketTotal
	reads  int
}

func (c *cannedBuckets) ReadBuckets(_ context.Context, r metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	c.reads++
	if vs := r.Query.Filter.Matching["region"]; len(vs) == 1 && vs[0] == "unknown" {
		return nil, true, nil
	}
	if len(r.By) == 0 {
		var t metering.BucketTotal
		for _, g := range c.totals {
			t.Count += g.Count
			t.EventsCount += g.EventsCount
			t.Sum = t.Sum.Add(g.Sum)
			t.PreciseTotalAmountCents = t.PreciseTotalAmountCents.Add(g.PreciseTotalAmountCents)
			if g.Max != nil && (t.Max == nil || g.Max.GreaterThan(*t.Max)) {
				t.Max = g.Max
			}
			t.Latest = g.Latest
		}
		return []metering.BucketTotal{t}, true, nil
	}
	return c.totals, true, nil
}

func decimalPtr(s string) *money.Decimal {
	d := money.MustParse(s)
	return &d
}

func region(v string) metering.Groups { return metering.Groups{"region": &v} }

// renderGrouped renders grouped aggregates as "group:value/count".
func renderGrouped(rows []metering.GroupedAggregate) string {
	var out []string
	for _, r := range rows {
		g := "<nil>"
		if v := r.Groups["region"]; v != nil {
			g = *v
		}
		out = append(out, fmt.Sprintf("region=%s:%s/%d", g, r.Value.Reduce(), r.Count))
	}
	return strings.Join(out, " ")
}

// TestUsageBucketStoreAnswers reads the totals of the buckets, which differ
// from the events the store wraps (none), through every aggregation the
// buckets answer.
func TestUsageBucketStoreAnswers(t *testing.T) {
	const (
		store = "spec/services/events/stores/usage_bucket_store_spec.rb:"
		set   = "spec/services/events/stores/usage_bucket_set_spec.rb:"
	)
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	q := metering.Query{
		OrganizationID: "o", SubscriptionID: "s", Code: "m", From: from,
		To: from.AddDate(0, 1, 0).Add(-time.Microsecond), Numeric: true, Property: "v",
	}
	unknown := q
	unknown.Filter = metering.Filter{Matching: map[string][]string{"region": {"unknown"}}}
	totals := []metering.BucketTotal{
		{
			Groups: region("us"), Count: 5, EventsCount: 5, Sum: money.MustParse("30"), Max: decimalPtr("30"), Latest: decimalPtr("30"),
			PreciseTotalAmountCents: money.MustParse("300.5"), LatestColumns: region("us"),
		},
		{
			Groups: region("eu"), Count: 2, EventsCount: 2, Sum: money.MustParse("12.5"), Max: decimalPtr("12.5"), Latest: decimalPtr("12.5"),
			PreciseTotalAmountCents: money.MustParse("934.000000000000001"), LatestColumns: region("eu"),
		},
	}
	by := []string{"region"}
	tests := []struct {
		name, source string
		read         func(context.Context, metering.Store) (string, error)
		want         string
	}{
		{
			name: "count answers the number of events of the buckets", source: store + "45, " + set + "53, 74",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				n, err := s.Count(ctx, q)
				return fmt.Sprint(n), err
			},
			want: "7",
		},
		{
			name: "sum answers the units of the buckets", source: store + "53, " + set + "53",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				t, err := s.Sum(ctx, q)
				return fmt.Sprintf("%s/%d", t.Value.Reduce(), t.Count), err
			},
			want: "42.5/7",
		},
		// A ready definition holding no row is no usage, served as zero.
		// source: spec/services/events/stores/provider_spec.rb:354, 370
		{
			name: "sum of a filter the buckets hold no row for is zero", source: store + "62, " + set + "59, 65",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				t, err := s.Sum(ctx, unknown)
				return fmt.Sprintf("%s/%d", t.Value.Reduce(), t.Count), err
			},
			want: "0/0",
		},
		{
			name: "the precise amounts add up", source: store + "69, " + set + "104",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				a, err := s.SumPreciseTotalAmountCents(ctx, q)
				return a.Reduce().String(), err
			},
			want: "1234.500000000000001",
		},
		{
			name: "the precise amount of a filter the buckets hold no row for is zero", source: store + "76, " + set + "109",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				a, err := s.SumPreciseTotalAmountCents(ctx, unknown)
				return a.Reduce().String(), err
			},
			want: "0",
		},
		{
			name: "one precise amount per group", source: store + "87, " + set + "124",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				rows, err := s.GroupedSumPreciseTotalAmountCents(ctx, q, by)
				return renderGrouped(rows), err
			},
			want: "region=us:300.5/5 region=eu:934.000000000000001/2",
		},
		{
			name: "no precise amount group for a filter the buckets hold no row for", source: set + "133",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				rows, err := s.GroupedSumPreciseTotalAmountCents(ctx, unknown, by)
				return renderGrouped(rows), err
			},
			want: "",
		},
		{
			name: "max answers the largest value of the buckets", source: store + "97",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				t, err := s.Max(ctx, q)
				return fmt.Sprintf("%s/%d", t.Value.Reduce(), t.Count), err
			},
			want: "30/7",
		},
		{
			name: "latest answers the latest value, counting every event alongside", source: store + "107",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				l, err := s.Last(ctx, q)
				if l.Value == nil {
					return "nil", err
				}
				return fmt.Sprintf("%s/%d", l.Value.Reduce(), l.Count), err
			},
			want: "12.5/7",
		},
		{
			name: "grouped count", source: store + "119",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				rows, err := s.GroupedCount(ctx, q, by)
				return renderGrouped(rows), err
			},
			want: "region=us:5/5 region=eu:2/2",
		},
		{
			name: "grouped sum", source: store + "145, " + set + "83",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				rows, err := s.GroupedSum(ctx, q, by)
				return renderGrouped(rows), err
			},
			want: "region=us:30/5 region=eu:12.5/2",
		},
		{
			name: "no group for a filter the buckets hold no row for", source: set + "94",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				rows, err := s.GroupedSum(ctx, unknown, by)
				return renderGrouped(rows), err
			},
			want: "",
		},
		{
			name: "grouped max", source: store + "172",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				rows, err := s.GroupedMax(ctx, q, by)
				return renderGrouped(rows), err
			},
			want: "region=us:30/5 region=eu:12.5/2",
		},
		{
			name: "grouped latest, counting the events of each group", source: store + "199",
			read: func(ctx context.Context, s metering.Store) (string, error) {
				rows, err := s.GroupedLast(ctx, q, by, by)
				return renderGrouped(rows), err
			},
			want: "region=us:30/5 region=eu:12.5/2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &cannedBuckets{totals: totals}
			got, err := tt.read(t.Context(), metering.NewUsageBucketStore(metering.NewMemoryStore(nil), b))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("%s: got = %q, want = %q", tt.source, got, tt.want)
			}
			if b.reads != 1 {
				t.Errorf("bucket reads got = %d, want = 1", b.reads)
			}
		})
	}
}

// TestUsageBucketStoreDelegates reads an aggregation the buckets do not
// answer: the events the store wraps answer it, and no bucket is read.
//
// source: spec/services/events/stores/usage_bucket_store_spec.rb:224
func TestUsageBucketStoreDelegates(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	events := metering.NewMemoryStore([]metering.Event{{
		OrganizationID: "o", SubscriptionID: "s", Code: "m", TransactionID: "a",
		Timestamp: from.Add(time.Hour), Properties: metering.Properties{"v": "3"},
	}})
	q := metering.Query{OrganizationID: "o", SubscriptionID: "s", Code: "m", From: from, To: from.AddDate(0, 1, 0), Numeric: true, Property: "v"}
	b := &cannedBuckets{totals: []metering.BucketTotal{{Count: 9, Sum: money.MustParse("99")}}}
	got, err := metering.NewUsageBucketStore(events, b).UniqueCount(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(money.NewFromInt(1)) != 0 || b.reads != 0 {
		t.Errorf("got = %s with %d bucket reads, want = 1 with 0", got, b.reads)
	}
}

// TestUsageBucketStoreCombines combines the events of the partial buckets
// at the ends of a window with the totals of its whole buckets, in time
// order.
func TestUsageBucketStoreCombines(t *testing.T) {
	const set = "spec/services/events/stores/usage_bucket_set_spec.rb:"
	from := time.Date(2026, 8, 1, 0, 7, 0, 0, time.UTC)
	q := metering.Query{OrganizationID: "o", SubscriptionID: "s", Code: "m", From: from, To: from.Add(2 * time.Hour), Numeric: true, Property: "v"}
	event := func(id string, ts time.Time, v string) metering.Event {
		return metering.Event{
			OrganizationID: "o", SubscriptionID: "s", Code: "m", TransactionID: id, Timestamp: ts,
			Properties: metering.Properties{"v": v}, PreciseTotalAmountCents: decimalPtr("0.25"),
		}
	}
	// The lead segment holds the events before 00:15, the trail one those
	// after 02:00.
	lead := event("lead", from.Add(time.Minute), "10")
	lead2 := event("lead2", from.Add(2*time.Minute), "10")
	trail := event("trail", from.Add(2*time.Hour-time.Minute), "7")
	buckets := []metering.BucketTotal{{
		Count: 1, EventsCount: 1, Sum: money.MustParse("5"), Max: decimalPtr("5"), Latest: decimalPtr("5"),
		PreciseTotalAmountCents: money.MustParse("100.25"),
	}}
	tests := []struct {
		name, source string
		events       []metering.Event
		read         func(context.Context, metering.Store) (string, error)
		want         string
	}{
		{
			name: "sums add up", source: set + "148", events: []metering.Event{lead, lead2},
			read: func(ctx context.Context, s metering.Store) (string, error) {
				t, err := s.Sum(ctx, q)
				return fmt.Sprintf("%s/%d", t.Value.Reduce(), t.Count), err
			},
			want: "25/3",
		},
		{
			name: "the largest value is kept", source: set + "154", events: []metering.Event{lead, lead2},
			read: func(ctx context.Context, s metering.Store) (string, error) {
				t, err := s.Max(ctx, q)
				return fmt.Sprintf("%s/%d", t.Value.Reduce(), t.Count), err
			},
			want: "10/3",
		},
		{
			name: "the latest value is the one of the last segment holding events", source: set + "161, 182", events: []metering.Event{lead, trail},
			read: func(ctx context.Context, s metering.Store) (string, error) {
				l, err := s.Last(ctx, q)
				return fmt.Sprintf("%s/%d", l.Value.Reduce(), l.Count), err
			},
			want: "7/3",
		},
		{
			name: "an earlier segment does not override a later one", source: set + "168", events: []metering.Event{event("lead", from.Add(time.Minute), "10")},
			read: func(ctx context.Context, s metering.Store) (string, error) {
				l, err := s.Last(ctx, q)
				return fmt.Sprintf("%s/%d", l.Value.Reduce(), l.Count), err
			},
			want: "5/2",
		},
		{
			name: "the precise amounts add up", source: set + "175", events: []metering.Event{lead, lead2},
			read: func(ctx context.Context, s metering.Store) (string, error) {
				a, err := s.SumPreciseTotalAmountCents(ctx, q)
				return a.Reduce().String(), err
			},
			want: "100.75",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &cannedBuckets{totals: buckets}
			got, err := tt.read(t.Context(), metering.NewUsageBucketStore(metering.NewMemoryStore(tt.events), b))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("%s: got = %q, want = %q", tt.source, got, tt.want)
			}
			if b.reads != 1 {
				t.Errorf("bucket reads got = %d, want = 1", b.reads)
			}
		})
	}
}
