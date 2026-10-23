package usage_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// crossRow is one (invoiced, current) → crossed amounts check.
type crossRow struct {
	invoiced, current int64
	want              []int64
}

// thresholds builds sorted thresholds: fixed ones, then the recurring one
// when recurring > 0.
func thresholds(fixed []int64, recurring int64) []*catalog.UsageThreshold {
	ids := &billingtest.IDs{}
	var out []*catalog.UsageThreshold
	for _, a := range fixed {
		out = append(out, &catalog.UsageThreshold{ID: core.NewID[core.UsageThresholdID](ids), Amount: a})
	}
	if recurring > 0 {
		out = append(out, &catalog.UsageThreshold{ID: core.NewID[core.UsageThresholdID](ids), Amount: recurring, Recurring: true})
	}
	return out
}

func TestCrossedThresholds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixed                         []int64
		recurring, billed, historical int64
		rows                          []crossRow
	}{
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:52
		{fixed: []int64{}, recurring: 0, billed: 0, historical: 0, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 10, []int64{}},
			{9, 2, []int64{}},
			{11, 1, []int64{}},
			{11, 10, []int64{}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:68
		{fixed: []int64{10}, recurring: 0, billed: 0, historical: 0, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 10, []int64{10}},
			{9, 2, []int64{10}},
			{11, 1, []int64{}},
			{11, 10, []int64{}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:84
		{fixed: []int64{10, 20, 31, 40}, recurring: 0, billed: 0, historical: 0, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 10, []int64{10}},
			{0, 31, []int64{10, 20, 31}},
			{9, 2, []int64{10}},
			{9, 20, []int64{10, 20}},
			{9, 31, []int64{10, 20, 31, 40}},
			{11, 1, []int64{}},
			{11, 10, []int64{20}},
			{21, 20, []int64{31, 40}},
			{40, 2, []int64{}},
			{50, 0, []int64{}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:108
		{fixed: []int64{}, recurring: 10, billed: 0, historical: 0, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 10, []int64{10}},
			{9, 2, []int64{10}},
			{11, 1, []int64{}},
			{11, 8, []int64{}},
			{11, 9, []int64{10}},
			{11, 10, []int64{10}},
			{11, 20, []int64{10}},
			{202, 7, []int64{}},
			{202, 8, []int64{10}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:129
		{fixed: []int64{10}, recurring: 5, billed: 0, historical: 0, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 10, []int64{10}},
			{0, 15, []int64{10, 5}},
			{0, 20, []int64{10, 5}},
			{9, 2, []int64{10}},
			{9, 6, []int64{10, 5}},
			{9, 20, []int64{10, 5}},
			{11, 3, []int64{}},
			{11, 4, []int64{5}},
			{11, 20, []int64{5}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:150
		{fixed: []int64{10, 20, 31, 40}, recurring: 5, billed: 0, historical: 0, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 10, []int64{10}},
			{0, 31, []int64{10, 20, 31}},
			{0, 44, []int64{10, 20, 31, 40}},
			{0, 45, []int64{10, 20, 31, 40, 5}},
			{9, 2, []int64{10}},
			{9, 20, []int64{10, 20}},
			{9, 31, []int64{10, 20, 31, 40}},
			{9, 37, []int64{10, 20, 31, 40, 5}},
			{11, 1, []int64{}},
			{11, 10, []int64{20}},
			{21, 20, []int64{31, 40}},
			{21, 24, []int64{31, 40, 5}},
			{40, 2, []int64{}},
			{40, 5, []int64{5}},
			{41, 4, []int64{5}},
			{49, 1, []int64{5}},
			{50, 0, []int64{}},
			{50, 5, []int64{5}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:186
		{fixed: []int64{}, recurring: 0, billed: 10, historical: 0, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 10, []int64{}},
			{9, 2, []int64{}},
			{11, 1, []int64{}},
			{11, 10, []int64{}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:202
		{fixed: []int64{10}, recurring: 0, billed: 10, historical: 0, rows: []crossRow{
			{9, 2, []int64{}},
			{9, 20, []int64{}},
			{11, 10, []int64{}},
			{11, 20, []int64{}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:217
		{fixed: []int64{10, 20, 31, 40}, recurring: 0, billed: 10, historical: 0, rows: []crossRow{
			{0, 10, []int64{}},
			{0, 31, []int64{20, 31}},
			{9, 10, []int64{}},
			{9, 12, []int64{20}},
			{9, 20, []int64{20}},
			{9, 31, []int64{20, 31, 40}},
			{11, 11, []int64{}},
			{11, 20, []int64{31}},
			{21, 20, []int64{40}},
			{30, 12, []int64{}},
			{50, 10, []int64{}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:241
		{fixed: []int64{}, recurring: 10, billed: 10, historical: 0, rows: []crossRow{
			{0, 10, []int64{}},
			{11, 10, []int64{}},
			{11, 19, []int64{10}},
			{202, 17, []int64{}},
			{202, 18, []int64{10}},
			{202, 28, []int64{10}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:258
		{fixed: []int64{10}, recurring: 5, billed: 10, historical: 0, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 10, []int64{}},
			{0, 15, []int64{5}},
			{0, 20, []int64{5}},
			{9, 2, []int64{}},
			{9, 6, []int64{}},
			{9, 16, []int64{5}},
			{11, 3, []int64{}},
			{11, 4, []int64{}},
			{11, 14, []int64{5}},
			{11, 24, []int64{5}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:280
		{fixed: []int64{10, 20, 31, 40}, recurring: 5, billed: 10, historical: 0, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 10, []int64{}},
			{0, 31, []int64{20, 31}},
			{0, 44, []int64{20, 31, 40}},
			{0, 45, []int64{20, 31, 40, 5}},
			{9, 2, []int64{}},
			{9, 20, []int64{20}},
			{9, 31, []int64{20, 31, 40}},
			{9, 37, []int64{20, 31, 40, 5}},
			{11, 1, []int64{}},
			{11, 10, []int64{}},
			{20, 20, []int64{31, 40}},
			{21, 20, []int64{40}},
			{21, 24, []int64{40, 5}},
			{40, 14, []int64{}},
			{40, 15, []int64{5}},
			{41, 14, []int64{5}},
			{49, 1, []int64{}},
			{49, 11, []int64{5}},
			{50, 5, []int64{}},
			{50, 15, []int64{5}},
		}},
		// source: spec/services/lifetime_usages/usage_thresholds/check_service_spec.rb:317
		{fixed: []int64{10, 20, 31, 40}, recurring: 0, billed: 0, historical: 11, rows: []crossRow{
			{0, 7, []int64{}},
			{0, 9, []int64{20}},
			{0, 10, []int64{20}},
			{9, 0, []int64{}},
			{0, 31, []int64{20, 31, 40}},
			{8, 2, []int64{20}},
			{8, 20, []int64{20, 31}},
			{8, 31, []int64{20, 31, 40}},
			{11, 1, []int64{}},
			{11, 10, []int64{31}},
			{21, 20, []int64{40}},
			{40, 2, []int64{}},
			{50, 0, []int64{}},
		}},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d_%v_%d_%d_%d", i, tc.fixed, tc.recurring, tc.billed, tc.historical), func(t *testing.T) {
			t.Parallel()
			ts := thresholds(tc.fixed, tc.recurring)
			for _, row := range tc.rows {
				lu := &subscription.LifetimeUsage{HistoricalUsageAmount: tc.historical, InvoicedUsageAmount: row.invoiced, CurrentUsageAmount: row.current}
				var got []int64
				for _, x := range usage.CrossedThresholds(lu, ts, tc.billed) {
					got = append(got, x.Amount)
				}
				if !slices.Equal(got, row.want) {
					t.Errorf("invoiced %d, current %d: got = %v, want = %v", row.invoiced, row.current, got, row.want)
				}
			}
		})
	}
}

// progress is the expected progress of one threshold.
type progress struct {
	amount  int64
	ratio   string
	reached *time.Time
}

func TestCompletion(t *testing.T) {
	t.Parallel()
	now := time.Date(2024, time.May, 1, 0, 0, 0, 0, time.UTC)
	applied1 := now.Add(-48 * time.Hour)
	applied2 := now.Add(-24 * time.Hour)
	// applied lists the applied rows of the thresholds at index i of ts.
	type applied struct {
		index  int
		amount int64
		at     time.Time
	}
	cases := []struct {
		name      string
		fixed     []int64
		recurring int64
		total     int64
		applied   []applied
		want      []progress
		last      *int64
		next      *int64
		nextRatio string
	}{
		// source: spec/services/lifetime_usages/usage_thresholds_completion_service_spec.rb:22
		// source: spec/services/lifetime_usages/find_last_and_next_thresholds_service_spec.rb:11
		{name: "no thresholds"},
		// source: spec/services/lifetime_usages/usage_thresholds_completion_service_spec.rb:31
		// source: spec/services/lifetime_usages/find_last_and_next_thresholds_service_spec.rb:19
		{name: "one threshold", fixed: []int64{100}, want: []progress{{100, "0", nil}}, next: ptr[int64](100), nextRatio: "0"},
		// source: spec/services/lifetime_usages/usage_thresholds_completion_service_spec.rb:42
		// source: spec/services/lifetime_usages/find_last_and_next_thresholds_service_spec.rb:26
		{name: "one threshold with usage", fixed: []int64{100}, total: 23, want: []progress{{100, "0.23", nil}}, next: ptr[int64](100), nextRatio: "0.23"},
		// source: spec/services/lifetime_usages/usage_thresholds_completion_service_spec.rb:65
		// source: spec/services/lifetime_usages/find_last_and_next_thresholds_service_spec.rb:46
		{
			name: "past threshold", fixed: []int64{100, 200}, total: 120, applied: []applied{{0, 100, applied1}},
			want: []progress{{100, "1", &applied1}, {200, "0.2", nil}}, last: ptr[int64](100), next: ptr[int64](200), nextRatio: "0.2",
		},
		// source: spec/services/lifetime_usages/usage_thresholds_completion_service_spec.rb:85
		// source: spec/services/lifetime_usages/find_last_and_next_thresholds_service_spec.rb:54
		{
			name: "above the last threshold", fixed: []int64{100, 200}, total: 223, applied: []applied{{0, 100, applied1}, {1, 200, applied2}},
			want: []progress{{100, "1", &applied1}, {200, "1", &applied2}}, last: ptr[int64](200),
		},
		// source: spec/services/lifetime_usages/usage_thresholds_completion_service_spec.rb:102
		// source: spec/services/lifetime_usages/find_last_and_next_thresholds_service_spec.rb:62
		{
			name: "next threshold recurring", fixed: []int64{100}, recurring: 200, total: 120, applied: []applied{{0, 100, applied1}},
			want: []progress{{100, "1", &applied1}, {300, "0.1", nil}}, last: ptr[int64](100), next: ptr[int64](300), nextRatio: "0.1",
		},
		// source: spec/services/lifetime_usages/usage_thresholds_completion_service_spec.rb:121
		// source: spec/services/lifetime_usages/find_last_and_next_thresholds_service_spec.rb:70
		{
			name: "above the next recurring threshold", fixed: []int64{100}, recurring: 200, total: 723,
			applied: []applied{{0, 100, applied1}, {1, 700, applied2}},
			want:    []progress{{100, "1", &applied1}, {300, "1", &applied2}, {500, "1", &applied2}, {700, "1", &applied2}, {900, "0.115", nil}},
			last:    ptr[int64](700), next: ptr[int64](900), nextRatio: "0.115",
		},
		// A recurring threshold without a reached occurrence is reached now.
		{
			name: "recurring occurrence not applied yet", recurring: 50, total: 60,
			want: []progress{{50, "1", &now}, {100, "0.2", nil}}, last: ptr[int64](50), next: ptr[int64](100), nextRatio: "0.2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ts := thresholds(tc.fixed, tc.recurring)
			var as []*subscription.AppliedUsageThreshold
			for _, a := range tc.applied {
				as = append(as, &subscription.AppliedUsageThreshold{UsageThresholdID: ts[a.index].ID, LifetimeUsageAmount: a.amount, CreatedAt: a.at})
			}
			got := usage.Completion(tc.total, ts, as, now)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d thresholds = %+v, want = %d", len(got), got, len(tc.want))
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Amount != w.amount || !g.Ratio.Equal(money.MustParse(w.ratio)) || g.Reached != (w.reached != nil) {
					t.Errorf("threshold %d: got = (%d, %s, %t), want = (%d, %s, %t)", i, g.Amount, g.Ratio, g.Reached, w.amount, w.ratio, w.reached != nil)
				}
				if w.reached != nil && (g.ReachedAt == nil || !g.ReachedAt.Equal(*w.reached)) {
					t.Errorf("threshold %d: got reached at = %v, want = %v", i, g.ReachedAt, *w.reached)
				}
			}
			ln := usage.LastAndNextOf(got)
			if !eqPtr(ln.LastAmount, tc.last) || !eqPtr(ln.NextAmount, tc.next) {
				t.Errorf("got last, next = %v, %v, want = %v, %v", deref(ln.LastAmount), deref(ln.NextAmount), deref(tc.last), deref(tc.next))
			}
			if (ln.NextRatio == nil) != (tc.nextRatio == "") || ln.NextRatio != nil && !ln.NextRatio.Equal(money.MustParse(tc.nextRatio)) {
				t.Errorf("got next ratio = %v, want = %q", ln.NextRatio, tc.nextRatio)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func eqPtr[T comparable](a, b *T) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
