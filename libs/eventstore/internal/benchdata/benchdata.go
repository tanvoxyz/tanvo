// Package benchdata generates the event volumes of the event store
// benchmarks and runs the hot aggregation queries over them.
package benchdata

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Env is the variable setting the number of events of the benchmarks; they
// are skipped when it is not set.
const Env = "TANVO_BENCH_EVENTS"

// Size returns the number of events of the benchmarks, skipping them when
// [Env] is not set.
func Size(b *testing.B) int {
	b.Helper()
	n, err := strconv.Atoi(os.Getenv(Env))
	if err != nil || n <= 0 {
		b.Skipf("set %s to the number of events (e.g. 1000000) to run the benchmarks", Env)
	}
	return n
}

// month is the period of the benchmark events.
var month = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// Query returns the query of the benchmark subscription over its month.
func Query() metering.Query {
	return metering.Query{
		OrganizationID: "org_bench", SubscriptionID: "sub_bench", Code: "api_calls",
		From: month, To: month.AddDate(0, 1, 0).Add(-time.Microsecond),
		Property: "value", PeriodDays: 31,
	}
}

// Load writes n events of one subscription spread over the month, with a
// numeric value, a region among 5, and a user among 10,000 added or
// removed. Another subscription gets as many events, which the queries
// must skip.
func Load(ctx context.Context, b *testing.B, w eventstore.Writer, n, batch int) {
	b.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	regions := []string{"europe", "us", "asia", "africa", "oceania"}
	span := int64(31 * 24 * time.Hour / time.Microsecond)
	start := time.Now()
	for _, sub := range []string{"sub_bench", "sub_other"} {
		for i := 0; i < n; i += batch {
			events := make([]metering.Event, 0, batch)
			for j := i; j < min(i+batch, n); j++ {
				op := "add"
				if r.IntN(10) == 0 {
					op = "remove"
				}
				events = append(events, metering.Event{
					OrganizationID: "org_bench", SubscriptionID: sub, Code: "api_calls",
					TransactionID: fmt.Sprintf("%s_%d", sub, j),
					Timestamp:     month.Add(time.Duration(r.Int64N(span)) * time.Microsecond),
					Properties: metering.Properties{
						"value":          strconv.Itoa(r.IntN(1000)) + "." + strconv.Itoa(r.IntN(100)),
						"region":         regions[r.IntN(len(regions))],
						"user_id":        "user_" + strconv.Itoa(r.IntN(10_000)),
						"operation_type": op,
					},
				})
			}
			if _, err := w.Write(ctx, events); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Logf("loaded 2 x %d events in %s", n, time.Since(start).Round(time.Millisecond))
}

// Run runs the hot aggregation queries as sub-benchmarks.
func Run(b *testing.B, s metering.Store) {
	ctx := context.Background()
	q := Query()
	filtered := q
	filtered.Filter = metering.Filter{Matching: map[string][]string{"region": {"europe", "us"}}}
	unique := q
	unique.Property = "user_id"
	cases := []struct {
		name string
		run  func() error
	}{
		{"Count", func() error { _, err := s.Count(ctx, q); return err }},
		{"Sum", func() error { _, err := s.Sum(ctx, q); return err }},
		{"SumFiltered", func() error { _, err := s.Sum(ctx, filtered); return err }},
		{"GroupedSum", func() error { _, err := s.GroupedSum(ctx, q, []string{"region"}); return err }},
		{"Max", func() error { _, err := s.Max(ctx, q); return err }},
		{"Last", func() error { _, err := s.Last(ctx, q); return err }},
		{"ProratedSum", func() error { _, err := s.ProratedSum(ctx, q, nil); return err }},
		{"SumByDay", func() error { _, err := s.SumByDay(ctx, q); return err }},
		{"UniqueCount", func() error { _, err := s.UniqueCount(ctx, unique); return err }},
		{"ProratedUniqueCount", func() error { _, err := s.ProratedUniqueCount(ctx, unique); return err }},
		{"WeightedSum", func() error { _, err := s.WeightedSum(ctx, q, money.Zero); return err }},
		{"GroupedWeightedSum", func() error {
			_, err := s.GroupedWeightedSum(ctx, q, []string{"region"}, nil)
			return err
		}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				if err := c.run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
