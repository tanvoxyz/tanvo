package metering_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// TestDaysTo checks that the prorated units of a period cut by a
// termination at local midnight count the day it ends on, while the event
// sent at the termination instant stays out of the period. The setup is
// the termination of spec/scenarios/current_usage/by_charge_model/
// prorated_graduated_spec.rb:301 and :1080: units persisted since October,
// the 2023-12 period ended on 2023-12-07 00:00 (7 of 31 days).
//
// source: app/services/utils/datetime.rb:64 (date_diff_with_timezone)
func TestDaysTo(t *testing.T) {
	t.Parallel()
	from := time.Date(2023, time.December, 1, 0, 0, 0, 0, time.UTC)
	terminated := time.Date(2023, time.December, 7, 0, 0, 0, 0, time.UTC)
	old := time.Date(2023, time.October, 5, 0, 0, 0, 0, time.UTC)
	ev := func(id string, ts time.Time, props metering.Properties) metering.Event {
		return metering.Event{OrganizationID: "org", SubscriptionID: "sub", Code: "bm", TransactionID: id, Timestamp: ts, Properties: props, IngestedAt: ts}
	}
	tests := []struct {
		name   string
		agg    metering.AggregationType
		events []metering.Event
		daysTo time.Time
		want   string
	}{
		{
			name: "sum, without DaysTo", agg: metering.AggregationSum,
			events: []metering.Event{ev("a", old, metering.Properties{"amount": "6"}), ev("b", terminated, metering.Properties{"amount": "1"})},
			// 6 × 6/31
			want: "1.16130",
		},
		{
			name: "sum, to the termination", agg: metering.AggregationSum,
			events: []metering.Event{ev("a", old, metering.Properties{"amount": "6"}), ev("b", terminated, metering.Properties{"amount": "1"})},
			daysTo: terminated,
			// 6 × 7/31; the event at the termination instant is left out.
			want: "1.35484",
		},
		{
			name: "unique count, without DaysTo", agg: metering.AggregationUniqueCount,
			events: []metering.Event{ev("a", old, metering.Properties{"amount": "1111"}), ev("b", terminated, metering.Properties{"amount": "2222"})},
			// 1 × 6/31
			want: "0.19355",
		},
		{
			name: "unique count, to the termination", agg: metering.AggregationUniqueCount,
			events: []metering.Event{ev("a", old, metering.Properties{"amount": "1111"}), ev("b", terminated, metering.Properties{"amount": "2222"})},
			daysTo: terminated,
			// 1 × 7/31
			want: "0.22581",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := metering.Request{
				Metric:         metering.Metric{Code: "bm", Aggregation: tt.agg, FieldName: "amount", Recurring: true},
				OrganizationID: "org", SubscriptionID: "sub",
				From: from, To: terminated.Add(-time.Microsecond), DaysTo: tt.daysTo,
				PeriodDays: 31, Prorated: true,
			}
			got, err := metering.Aggregate(context.Background(), metering.NewMemoryStore(tt.events), req)
			if err != nil {
				t.Fatalf("aggregate: %v", err)
			}
			if want := money.MustParse(tt.want); !got.Aggregation.Equal(want) {
				t.Errorf("aggregation = %s, want = %s", got.Aggregation, want)
			}
		})
	}
}

// TestDateDiffInDSTGap checks that a to at the start of a day whose local
// midnight is skipped by daylight saving time still counts that day.
// America/Santiago moves from 00:00 -04 to 01:00 -03 on 2026-09-06, so the
// day begins at 01:00 local (04:00 UTC), the instant ActiveSupport's
// beginning_of_day resolves to.
//
// source: app/services/utils/datetime.rb:70 (date_diff_with_timezone)
func TestDateDiffInDSTGap(t *testing.T) {
	loc, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		from, to time.Time
		want     int
	}{
		{"to at the shifted start of day", time.Date(2026, 9, 5, 5, 0, 0, 0, time.UTC), time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC), 2},
		{"23-hour day ending at an ordinary midnight", time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC), time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC), 1},
		{"to inside the day", time.Date(2026, 9, 5, 5, 0, 0, 0, time.UTC), time.Date(2026, 9, 6, 5, 0, 0, 0, time.UTC), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metering.DateDiff(tt.from, tt.to, loc); got != tt.want {
				t.Errorf("DateDiff() = %d, want = %d", got, tt.want)
			}
		})
	}
}

// TestBreakdownDaysTo checks that the sum breakdown of a period cut by a
// termination at local midnight counts the durations to the day the
// termination falls on, as lago's to_date_in_customer_timezone of the
// termination instant does: units persisted since October and 2 added on
// 2023-12-03, the period of 2023-12 ended on 2023-12-07 00:00.
//
// source: app/services/billable_metrics/breakdown/sum_service.rb:23,39
func TestBreakdownDaysTo(t *testing.T) {
	t.Parallel()
	from := time.Date(2023, time.December, 1, 0, 0, 0, 0, time.UTC)
	terminated := time.Date(2023, time.December, 7, 0, 0, 0, 0, time.UTC)
	ev := func(id string, ts time.Time, amount string) metering.Event {
		return metering.Event{OrganizationID: "org", SubscriptionID: "sub", Code: "bm", TransactionID: id, Timestamp: ts, Properties: metering.Properties{"amount": amount}, IngestedAt: ts}
	}
	events := []metering.Event{
		ev("a", time.Date(2023, time.October, 5, 0, 0, 0, 0, time.UTC), "6"),
		ev("b", time.Date(2023, time.December, 3, 10, 0, 0, 0, time.UTC), "2"),
	}
	req := metering.Request{
		Metric:         metering.Metric{Code: "bm", Aggregation: metering.AggregationSum, FieldName: "amount", Recurring: true},
		OrganizationID: "org", SubscriptionID: "sub",
		From: from, To: terminated.Add(-time.Microsecond), DaysTo: terminated,
		PeriodDays: 31, Prorated: true,
	}
	items, err := metering.Breakdown(context.Background(), metering.NewMemoryStore(events), req)
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	var got []int
	for _, it := range items {
		got = append(got, it.Duration)
	}
	// Persisted: 2023-12-08 − 2023-12-01; added: 2023-12-08 − 2023-12-03.
	if want := []int{7, 5}; !slices.Equal(got, want) {
		t.Errorf("durations = %v, want = %v", got, want)
	}
}
