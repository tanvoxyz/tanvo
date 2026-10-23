// Package latency measures the ingestion latency of the event stores: the
// duration of [eventstore.Writer.Write] calls over batch sizes, numbers of
// usage bucket definitions, cold and hot buckets and concurrent writers.
package latency

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Env is the variable that enables the measurements; they are skipped
// when it is not set.
const Env = "TANVO_LATENCY"

// Backend is an event store under measurement.
type Backend interface {
	eventstore.Writer
	eventstore.BucketDefiner
}

// Open returns the backend over empty tables.
type Open func(t *testing.T) Backend

// Config sets the scenarios.
type Config struct {
	// Writers are the numbers of concurrent writers, each on its own
	// subscriptions; {1} when empty.
	Writers []int
	// Hot is the number of events a hot bucket holds before the writes.
	Hot int
}

const (
	org  = "org_lat"
	code = "calls"
)

var bucket = time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)

// definitions are the bucket definitions of the scenarios, the first n of
// which are defined.
var definitions = []metering.BucketDefinition{
	{OrganizationID: org, Code: code, Property: "value"},
	{OrganizationID: org, Code: code},
	{OrganizationID: org, Code: code, Property: "value", Dimensions: []string{"region"}},
	{OrganizationID: org, Code: code, Dimensions: []string{"region", "zone"}},
	{OrganizationID: org, Code: code, Property: "value", Dimensions: []string{"region"}, Values: map[string][]string{"region": {"eu", "us"}}},
}

func event(sub, tx string, i int) metering.Event {
	return metering.Event{
		OrganizationID: org, SubscriptionID: sub, Code: code, TransactionID: tx,
		Timestamp: bucket.Add(time.Duration(i%900_000) * time.Millisecond),
		Properties: metering.Properties{
			"value":  fmt.Sprint(i % 97),
			"region": []string{"eu", "us", "apac"}[i%3],
			"zone":   fmt.Sprintf("z%d", i%4),
		},
	}
}

// Result is the measurement of one scenario.
type Result struct {
	Name          string
	P50, P95, P99 time.Duration
	EventsPerSec  float64
}

// String renders r as a row of a table.
func (r Result) String() string {
	ms := func(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }
	return fmt.Sprintf("| %s | %s | %s | %s | %.0f |", r.Name, ms(r.P50), ms(r.P95), ms(r.P99), r.EventsPerSec)
}

// writes is the number of writes measured per writer for a batch size.
func writes(batch int) int {
	switch {
	case batch >= 1000:
		return 20
	case batch >= 100:
		return 50
	default:
		return 200
	}
}

// Run measures every scenario and logs a Markdown table of the results.
func Run(t *testing.T, name string, open Open, cfg Config) []Result {
	t.Helper()
	if os.Getenv(Env) == "" {
		t.Skipf("set %s=1 to measure the ingestion latency", Env)
	}
	writers := cfg.Writers
	if len(writers) == 0 {
		writers = []int{1}
	}
	hot := cmp.Or(cfg.Hot, 100_000)
	ctx := context.Background()
	var out []Result
	for _, defs := range []int{0, 1, 5} {
		for _, temp := range []string{"cold", "hot"} {
			for _, w := range writers {
				for _, batch := range []int{1, 100, 1000} {
					s := open(t)
					// Hot subscriptions hold hot events in the bucket the
					// writes go to before the definitions are backfilled.
					if temp == "hot" {
						for i := 0; i < hot; i += 10_000 {
							evs := make([]metering.Event, 0, 10_000)
							for j := i; j < min(hot, i+10_000); j++ {
								for k := range w {
									evs = append(evs, event(fmt.Sprintf("hot%d", k), fmt.Sprintf("pre%d-%d", k, j), j))
								}
							}
							if _, err := s.Write(ctx, evs); err != nil {
								t.Fatal(err)
							}
						}
					}
					for _, d := range definitions[:defs] {
						if err := s.DefineBuckets(ctx, d); err != nil {
							t.Fatal(err)
						}
					}
					var (
						mu   sync.Mutex
						durs []time.Duration
						wg   sync.WaitGroup
						errs = make(chan error, w)
					)
					start := time.Now()
					for k := range w {
						wg.Go(func() {
							for n := range writes(batch) {
								sub := fmt.Sprintf("hot%d", k)
								if temp == "cold" {
									sub = fmt.Sprintf("cold%d-%d", k, n)
								}
								evs := make([]metering.Event, batch)
								for j := range evs {
									evs[j] = event(sub, fmt.Sprintf("w%d-%d-%d", k, n, j), n*batch+j)
								}
								t0 := time.Now()
								if _, err := s.Write(ctx, evs); err != nil {
									errs <- err
									return
								}
								d := time.Since(t0)
								mu.Lock()
								durs = append(durs, d)
								mu.Unlock()
							}
						})
					}
					wg.Wait()
					elapsed := time.Since(start)
					close(errs)
					for err := range errs {
						t.Fatal(err)
					}
					slices.Sort(durs)
					q := func(p float64) time.Duration { return durs[min(len(durs)-1, int(p*float64(len(durs))))] }
					r := Result{
						Name: fmt.Sprintf("%s | defs=%d | %s | writers=%d | batch=%d", name, defs, temp, w, batch),
						P50:  q(0.50), P95: q(0.95), P99: q(0.99),
						EventsPerSec: float64(len(durs)*batch) / elapsed.Seconds(),
					}
					t.Log(r)
					out = append(out, r)
				}
			}
		}
	}
	var b strings.Builder
	b.WriteString("\n| backend | definitions | bucket | writers | batch | p50 ms | p95 ms | p99 ms | events/s |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range out {
		b.WriteString(r.String() + "\n")
	}
	t.Log(b.String())
	return out
}
