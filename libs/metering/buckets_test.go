package metering_test

import (
	"context"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestBucketServable(t *testing.T) {
	const src = "spec/services/realtime_usage_spec.rb:"
	count := metering.Metric{Code: "m", Aggregation: metering.AggregationCount}
	sum := metering.Metric{Code: "m", Aggregation: metering.AggregationSum, FieldName: "v"}
	maxM := metering.Metric{Code: "m", Aggregation: metering.AggregationMax, FieldName: "v"}
	latest := metering.Metric{Code: "m", Aggregation: metering.AggregationLatest, FieldName: "v"}
	unique := metering.Metric{Code: "m", Aggregation: metering.AggregationUniqueCount, FieldName: "v"}
	weighted := metering.Metric{Code: "m", Aggregation: metering.AggregationWeightedSum, FieldName: "v"}
	recurring := sum
	recurring.Recurring = true
	standard := metering.BucketCharge{ChargeModel: "standard"}
	tests := []struct {
		source string
		metric metering.Metric
		charge metering.BucketCharge
		want   string
	}{
		{src + "125", count, standard, ""},
		{src + "132", sum, standard, ""},
		{src + "138", maxM, standard, ""},
		{src + "144", latest, standard, ""},
		{src + "159", unique, standard, "unsupported_aggregation_type"},
		{src + "237", weighted, standard, "unsupported_aggregation_type"},
		{src + "237", unique, metering.BucketCharge{ChargeModel: "graduated"}, "unsupported_aggregation_type"},
		{src + "165", count, metering.BucketCharge{ChargeModel: "percentage"}, "unsupported_charge_model"},
		{src + "231", count, metering.BucketCharge{ChargeModel: "custom"}, "unsupported_charge_model"},
		{src + "231", sum, metering.BucketCharge{ChargeModel: "percentage"}, "unsupported_charge_model"},
		// Free units are a property of the charge, not of the totals.
		{src + "216", count, standard, ""},
		{src + "173", count, metering.BucketCharge{ChargeModel: "graduated"}, ""},
		{src + "231", count, metering.BucketCharge{ChargeModel: "package"}, ""},
		{src + "231", count, metering.BucketCharge{ChargeModel: "volume"}, ""},
		{src + "231", count, metering.BucketCharge{ChargeModel: "graduated_percentage"}, ""},
		{src + "231", sum, metering.BucketCharge{ChargeModel: "dynamic"}, ""},
		{src + "179", count, metering.BucketCharge{ChargeModel: "standard", PayInAdvance: true}, "pay_in_advance"},
		{src + "186", recurring, metering.BucketCharge{ChargeModel: "standard", Prorated: true}, "prorated"},
		{src + "192", recurring, standard, "recurring_metric"},
		{src + "206", count, metering.BucketCharge{ChargeModel: "standard", AcceptsTargetWallet: true}, "target_wallet"},
	}
	for _, tt := range tests {
		got := metering.BucketUnsupportedReason(tt.metric, tt.charge)
		if got != tt.want {
			t.Errorf("%s: BucketUnsupportedReason(%s, %+v) got = %q, want = %q", tt.source, tt.metric.Aggregation, tt.charge, got, tt.want)
		}
		if served := metering.BucketServable(tt.metric, tt.charge); served != (tt.want == "") {
			t.Errorf("%s: BucketServable got = %v, want = %v", tt.source, served, tt.want == "")
		}
	}
}

func TestBucketBounds(t *testing.T) {
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		at, floor, ceil time.Time
	}{
		{base, base, base},
		{base.Add(time.Nanosecond), base, base.Add(15 * time.Minute)},
		{base.Add(14*time.Minute + 59*time.Second), base, base.Add(15 * time.Minute)},
		{base.Add(15 * time.Minute), base.Add(15 * time.Minute), base.Add(15 * time.Minute)},
	}
	for _, tt := range tests {
		if got := metering.BucketFloor(tt.at); !got.Equal(tt.floor) {
			t.Errorf("BucketFloor(%s) got = %s, want = %s", tt.at, got, tt.floor)
		}
		if got := metering.BucketCeil(tt.at); !got.Equal(tt.ceil) {
			t.Errorf("BucketCeil(%s) got = %s, want = %s", tt.at, got, tt.ceil)
		}
	}
}

func TestBucketDefinitionCovers(t *testing.T) {
	d := metering.BucketDefinition{OrganizationID: "o", Code: "m", Property: "v", Dimensions: []string{"region", "region", "cloud"}}
	n := d.Normalize()
	if want := []string{"cloud", "region", metering.TargetWalletCodeProperty}; !slices.Equal(n.Dimensions, want) {
		t.Errorf("Normalize().Dimensions got = %v, want = %v", n.Dimensions, want)
	}
	reordered := metering.BucketDefinition{OrganizationID: "o", Code: "m", Property: "v", Dimensions: []string{"cloud", "region"}}
	if d.ID() != reordered.ID() {
		t.Errorf("ID got = %s, want = %s", d.ID(), reordered.ID())
	}
	q := metering.Query{OrganizationID: "o", Code: "m"}
	numeric := q
	numeric.Numeric, numeric.Property = true, "v"
	other := q
	other.Numeric, other.Property = true, "w"
	filtered := q
	filtered.Filter.Matching = map[string][]string{"cloud": {"aws"}}
	unknown := q
	unknown.Filter.Ignored = []map[string][]string{{"os": {"linux"}}}
	tests := []struct {
		name string
		read metering.BucketRead
		want bool
	}{
		{"count", metering.BucketRead{Query: q}, true},
		{"property", metering.BucketRead{Query: numeric}, true},
		{"other property", metering.BucketRead{Query: other}, false},
		{"filter dimension", metering.BucketRead{Query: filtered, By: []string{"region"}}, true},
		{"filter key not kept", metering.BucketRead{Query: unknown}, false},
		{"grouping key not kept", metering.BucketRead{Query: q, By: []string{"os"}}, false},
		{"target wallet", metering.BucketRead{Query: q, By: []string{metering.TargetWalletCodeProperty}}, true},
		{"other code", metering.BucketRead{Query: metering.Query{OrganizationID: "o", Code: "x"}}, false},
	}
	for _, tt := range tests {
		if got := d.Covers(tt.read); got != tt.want {
			t.Errorf("%s: Covers got = %v, want = %v", tt.name, got, tt.want)
		}
	}
	narrow := metering.BucketDefinition{OrganizationID: "o", Code: "m", Property: "v"}
	got, ok := metering.CoveringDefinition([]metering.BucketDefinition{d, narrow}, metering.BucketRead{Query: numeric})
	if !ok || got.ID() != narrow.ID() {
		t.Errorf("CoveringDefinition got = %v %v, want = %v true", got, ok, narrow.Normalize())
	}
}

// bucketedStore is a store reading usage buckets, counting the reads.
type bucketedStore struct {
	*metering.MemoryStore
	buckets *metering.MemoryBuckets
	reads   atomic.Int64
}

func (s *bucketedStore) ReadBuckets(ctx context.Context, r metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	s.reads.Add(1)
	return s.buckets.ReadBuckets(ctx, r)
}

func TestAggregateUsageBuckets(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0).Add(-time.Millisecond)
	var events []metering.Event
	for i := range 10 {
		events = append(events, metering.Event{
			OrganizationID: "o", SubscriptionID: "s", Code: "m", TransactionID: string(rune('a' + i)),
			Timestamp: from.Add(time.Duration(i) * 7 * time.Hour), Properties: metering.Properties{"v": "2"},
		})
	}
	sum := metering.Metric{Code: "m", Aggregation: metering.AggregationSum, FieldName: "v"}
	recurring := sum
	recurring.Recurring = true
	tests := []struct {
		name      string
		req       metering.Request
		wantReads bool
	}{
		{"served", metering.Request{Metric: sum, UsageBuckets: true}, true},
		{"not asked", metering.Request{Metric: sum}, false},
		{"recurring", metering.Request{Metric: recurring, UsageBuckets: true}, false},
		{"pay in advance", metering.Request{Metric: sum, UsageBuckets: true, PayInAdvance: true}, false},
		{"capped", metering.Request{Metric: sum, UsageBuckets: true, MaxTimestamp: to}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := metering.NewMemoryStore(events)
			s := &bucketedStore{MemoryStore: ms, buckets: ms.Buckets(metering.BucketDefinitionFor("o", sum, nil))}
			req := tt.req
			req.OrganizationID, req.SubscriptionID, req.From, req.To = "o", "s", from, to
			got, err := metering.Aggregate(t.Context(), s, req)
			if err != nil {
				t.Fatalf("Aggregate: %v", err)
			}
			want, err := metering.Aggregate(t.Context(), ms, req)
			if err != nil {
				t.Fatalf("Aggregate events: %v", err)
			}
			if got.Aggregation.Cmp(want.Aggregation) != 0 || got.Aggregation.Cmp(money.NewFromInt(20)) != 0 && !req.Metric.Recurring {
				t.Errorf("aggregation got = %s, want = %s", got.Aggregation, want.Aggregation)
			}
			if reads := s.reads.Load() > 0; reads != tt.wantReads {
				t.Errorf("buckets read got = %v, want = %v", reads, tt.wantReads)
			}
		})
	}
}

func TestHourlySeriesForFoldedDimension(t *testing.T) {
	day := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	metric := metering.Metric{Code: "m", Aggregation: metering.AggregationSum, FieldName: "v"}
	declared := []metering.MetricFilter{{Key: "region", Values: []string{"asia", "eu", "us"}}}
	filters := []metering.ChargeFilter{
		{ID: "f-eu", Values: map[string][]string{"region": {"eu"}}},
		{ID: "f-us", Values: map[string][]string{"region": {"us"}}},
	}
	event := func(id, region, v string, minute int) metering.Event {
		p := metering.Properties{"v": v}
		if region != "" {
			p["region"] = region
		}
		return metering.Event{
			OrganizationID: "o", SubscriptionID: "s", Code: "m", TransactionID: id,
			Timestamp: day.Add(time.Duration(minute) * time.Minute), Properties: p,
		}
	}
	events := []metering.Event{
		event("a", "eu", "10", 5),
		event("b", "us", "20", 20),
		event("c", "asia", "1", 40),
		event("d", "undeclared", "2", 70),
		event("e", "", "4", 80),
		event("f", "eu", "x", 90),
	}
	def := metering.BucketDefinitionFor("o", metric, declared)
	if got, want := def.Dimension("region", "undeclared"), metering.BucketOtherValue; got != want {
		t.Fatalf("folded value got = %q, want = %q", got, want)
	}
	series, err := metering.HourlySeriesFor(filters, declared)
	if err != nil {
		t.Fatalf("HourlySeriesFor: %v", err)
	}
	ms := metering.NewMemoryStore(events)
	req := metering.HourlyRequest{
		Query:  metering.Query{OrganizationID: "o", SubscriptionID: "s", Code: "m"},
		Metric: metric, From: day, To: day.Add(2 * time.Hour), Series: series,
	}
	res, err := metering.HourlyUsage(t.Context(), ms, ms.Buckets(def), req)
	if err != nil {
		t.Fatalf("HourlyUsage: %v", err)
	}
	var got []string
	for _, s := range res.Series {
		got = append(got, s.Key+"="+s.Units.Reduce().String()+"/"+strconv.FormatInt(s.EventsCount, 10))
	}
	// lago sums events_count over every event of the bucket, numeric or not.
	if want := []string{"f-us=20/1", "f-eu=10/2", "=7/3"}; !slices.Equal(got, want) {
		t.Errorf("series got = %v, want = %v", got, want)
	}
}
