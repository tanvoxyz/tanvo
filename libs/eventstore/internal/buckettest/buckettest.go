// Package buckettest holds the tests of the usage bucket maintenance shared
// by the event stores: late events, deduplication, replacements, backfill,
// dropped definitions and concurrent writers must all leave buckets that
// answer as the events do.
package buckettest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Backend is an event store maintaining usage buckets.
type Backend interface {
	metering.Store
	metering.BucketReader
	eventstore.Writer
	eventstore.Replacer
	eventstore.BucketDefiner
}

// Open returns the backend under test, with no events nor definitions.
type Open func(t *testing.T) Backend

const (
	org  = "org_b"
	sub  = "sub_b"
	code = "calls"
)

var day = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

func at(h, m int) time.Time {
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func ev(tx string, ts time.Time, value string, pairs ...string) metering.Event {
	props := metering.Properties{}
	if value != "" {
		props["value"] = value
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		props[pairs[i]] = pairs[i+1]
	}
	return metering.Event{OrganizationID: org, SubscriptionID: sub, Code: code, TransactionID: tx, Timestamp: ts, Properties: props}
}

var (
	countDef = metering.BucketDefinition{OrganizationID: org, Code: code, Dimensions: []string{"region"}}
	valueDef = metering.BucketDefinition{OrganizationID: org, Code: code, Property: "value", Dimensions: []string{"region"}}
)

// counting counts the reads the buckets answered.
type counting struct {
	metering.BucketReader
	served atomic.Int64
}

func (c *counting) ReadBuckets(ctx context.Context, r metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	totals, ok, err := c.BucketReader.ReadBuckets(ctx, r)
	if ok {
		c.served.Add(1)
	}
	return totals, ok, err
}

// snapshot renders what the bucket and the event paths compute over the
// day, so that both can be compared.
func snapshot(ctx context.Context, s metering.Store) (string, error) {
	count := metering.Query{OrganizationID: org, SubscriptionID: sub, Code: code, From: day, To: day.Add(24*time.Hour - time.Millisecond)}
	values := count
	values.Numeric, values.Property = true, "value"
	// Edges inside buckets read the events.
	edged := values
	edged.From, edged.To = at(0, 7), at(23, 52)
	var b strings.Builder
	n, err := s.Count(ctx, count)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "count=%d", n)
	for _, q := range []metering.Query{values, edged} {
		sum, err := s.Sum(ctx, q)
		if err != nil {
			return "", err
		}
		mx, err := s.Max(ctx, q)
		if err != nil {
			return "", err
		}
		last, err := s.Last(ctx, q)
		if err != nil {
			return "", err
		}
		l := "nil"
		if last.Value != nil {
			l = last.Value.Reduce().String()
		}
		fmt.Fprintf(&b, " sum=%s/%d max=%s/%d last=%s/%d", sum.Value.Reduce(), sum.Count, mx.Value.Reduce(), mx.Count, l, last.Count)
		grouped, err := s.GroupedSum(ctx, q, []string{"region"})
		if err != nil {
			return "", err
		}
		var g []string
		for _, r := range grouped {
			k := "<nil>"
			if v := r.Groups["region"]; v != nil {
				k = *v
			}
			g = append(g, fmt.Sprintf("%s:%s/%d", k, r.Value.Reduce(), r.Count))
		}
		slices.Sort(g)
		fmt.Fprintf(&b, " grouped=[%s]", strings.Join(g, " "))
	}
	return b.String(), nil
}

// check compares the bucket path with the event path, and returns the
// snapshot.
func check(t *testing.T, s Backend) string {
	t.Helper()
	ctx := t.Context()
	c := &counting{BucketReader: s}
	got, err := snapshot(ctx, metering.NewUsageBucketStore(s, c))
	if err != nil {
		t.Fatalf("buckets: %v", err)
	}
	want, err := snapshot(ctx, s)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if got != want {
		t.Errorf("buckets got = %s\n want = %s", got, want)
	}
	if c.served.Load() == 0 {
		t.Errorf("usage buckets read got = 0, want > 0")
	}
	return got
}

func define(t *testing.T, s Backend, defs ...metering.BucketDefinition) {
	t.Helper()
	for _, d := range defs {
		if err := s.DefineBuckets(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, s Backend, events ...metering.Event) []int {
	t.Helper()
	dups, err := s.Write(t.Context(), events)
	if err != nil {
		t.Fatal(err)
	}
	return dups
}

func replace(t *testing.T, s Backend, events ...metering.Event) {
	t.Helper()
	if err := s.Replace(t.Context(), events); err != nil {
		t.Fatal(err)
	}
}

func wantSnapshot(t *testing.T, got, want string) {
	t.Helper()
	if !strings.HasPrefix(got, want) {
		t.Errorf("snapshot got = %s, want prefix = %s", got, want)
	}
}

// totals reads the buckets of the day directly.
func totals(t *testing.T, s Backend, numeric bool) (metering.BucketTotal, bool) {
	t.Helper()
	q := metering.Query{OrganizationID: org, SubscriptionID: sub, Code: code}
	if numeric {
		q.Numeric, q.Property = true, "value"
	}
	rows, ok, err := s.ReadBuckets(t.Context(), metering.BucketRead{Query: q, From: day, To: day.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return metering.BucketTotal{}, ok
	}
	return rows[0], ok
}

// Peers returns n backends over the storage of s, as writers in other
// processes see it: for the concurrent writers to run apart from any
// coordination within a process.
type Peers func(s Backend, n int) []Backend

// Run runs the maintenance tests. With peers, the concurrent writers run
// once through s and once through peers of it.
func Run(t *testing.T, open Open, peers ...Peers) {
	t.Run("late events", func(t *testing.T) {
		s := open(t)
		define(t, s, countDef, valueDef)
		write(t, s, ev("a", at(10, 0), "4", "region", "eu"), ev("b", at(10, 5), "6", "region", "us"))
		// Later ingestions of earlier events: in another bucket, and in the
		// same bucket before its latest event.
		write(t, s, ev("c", at(9, 1), "100", "region", "eu"))
		write(t, s, ev("d", at(10, 2), "1", "region", "eu"))
		wantSnapshot(t, check(t, s), "count=4 sum=111/4 max=100/4 last=6/4")
	})
	t.Run("same timestamp ranks by ingestion", func(t *testing.T) {
		s := open(t)
		define(t, s, valueDef)
		write(t, s, ev("a", at(10, 0), "4"))
		write(t, s, ev("b", at(10, 0), "9"))
		wantSnapshot(t, check(t, s), "count=2 sum=13/2 max=9/2 last=9/2")
	})
	t.Run("duplicates are not counted", func(t *testing.T) {
		s := open(t)
		define(t, s, countDef, valueDef)
		write(t, s, ev("a", at(10, 0), "4"))
		if dups := write(t, s, ev("a", at(10, 0), "40"), ev("a", at(11, 0), "40"), ev("b", at(11, 0), "1"), ev("b", at(12, 0), "1")); !slices.Equal(dups, []int{0, 1, 3}) {
			t.Errorf("duplicates got = %v, want = [0 1 3]", dups)
		}
		wantSnapshot(t, check(t, s), "count=2 sum=5/2 max=4/2 last=1/2")
	})
	t.Run("a transaction ID under two subscriptions is two events", func(t *testing.T) {
		s := open(t)
		define(t, s, countDef, valueDef)
		other := ev("a", at(10, 0), "40")
		other.SubscriptionID = "sub_other"
		if dups := write(t, s, ev("a", at(10, 0), "4"), other); len(dups) != 0 {
			t.Errorf("duplicates got = %v, want = []", dups)
		}
		if dups := write(t, s, other, ev("a", at(11, 0), "5")); !slices.Equal(dups, []int{0, 1}) {
			t.Errorf("duplicates got = %v, want = [0 1]", dups)
		}
		wantSnapshot(t, check(t, s), "count=1 sum=4/1 max=4/1 last=4/1")
		q := metering.Query{OrganizationID: org, SubscriptionID: "sub_other", Code: code, From: day, To: day.Add(24*time.Hour - time.Millisecond)}
		if n, err := s.Count(t.Context(), q); err != nil || n != 1 {
			t.Errorf("other subscription count got = %d %v, want = 1 <nil>", n, err)
		}
	})
	t.Run("replace moves the event", func(t *testing.T) {
		s := open(t)
		define(t, s, countDef, valueDef)
		write(t, s, ev("a", at(10, 0), "4", "region", "eu"), ev("b", at(10, 5), "1", "region", "eu"))
		// To another timestamp, in another bucket: the event leaves its
		// bucket.
		replace(t, s, ev("a", at(12, 0), "4", "region", "eu"))
		wantSnapshot(t, check(t, s), "count=2 sum=5/2 max=4/2 last=4/2")
		// To another code: the event leaves the code.
		moved := ev("a", at(12, 0), "4", "region", "eu")
		moved.Code = "other"
		replace(t, s, moved)
		wantSnapshot(t, check(t, s), "count=1 sum=1/1 max=1/1 last=1/1")
		// The moved event is still known.
		if dups := write(t, s, ev("a", at(9, 0), "7")); !slices.Equal(dups, []int{0}) {
			t.Errorf("duplicates got = %v, want = [0]", dups)
		}
	})
	t.Run("replace corrects the bucket", func(t *testing.T) {
		s := open(t)
		define(t, s, countDef, valueDef)
		write(t, s, ev("a", at(10, 0), "10", "region", "eu"), ev("b", at(10, 1), "3", "region", "eu"), ev("c", at(10, 2), "7", "region", "eu"))
		// The maximum and the latest value are taken back.
		replace(t, s, ev("a", at(10, 0), "1", "region", "eu"), ev("c", at(10, 2), "2", "region", "eu"))
		wantSnapshot(t, check(t, s), "count=3 sum=6/3 max=3/3 last=2/3")
		// A value no longer numeric leaves the numeric totals.
		replace(t, s, ev("b", at(10, 1), "n/a", "region", "eu"))
		wantSnapshot(t, check(t, s), "count=3 sum=3/2 max=2/2 last=2/2")
		// Other dimension values move the event to another row.
		replace(t, s, ev("c", at(10, 2), "2", "region", "us"))
		got := check(t, s)
		if !strings.Contains(got, "grouped=[eu:1/1 us:2/1]") {
			t.Errorf("snapshot got = %s, want grouped = [eu:1/1 us:2/1]", got)
		}
		// Every numeric value gone, the bucket keeps its events only.
		replace(t, s, ev("a", at(10, 0), "", "region", "eu"), ev("c", at(10, 2), "", "region", "us"))
		wantSnapshot(t, check(t, s), "count=3 sum=0/0 max=0/0 last=nil/0")
		if tot, ok := totals(t, s, false); !ok || tot.Count != 3 {
			t.Errorf("bucket events got = %d %v, want = 3 true", tot.Count, ok)
		}
	})
	t.Run("replace leaves a row", func(t *testing.T) {
		s := open(t)
		define(t, s, countDef, valueDef)
		write(t, s, ev("a", at(11, 0), "5", "region", "apac"), ev("b", at(11, 1), "1", "region", "eu"))
		// The only event of its dimension values moves away: its row must
		// not answer any more.
		replace(t, s, ev("a", at(11, 0), "5", "region", "eu"))
		got := check(t, s)
		if !strings.Contains(got, "grouped=[eu:6/2]") {
			t.Errorf("snapshot got = %s, want grouped = [eu:6/2]", got)
		}
	})
	t.Run("replace of an event not yet stored", func(t *testing.T) {
		s := open(t)
		define(t, s, valueDef)
		write(t, s, ev("a", at(10, 0), "1"))
		replace(t, s, ev("z", at(10, 3), "5"), ev("z", at(10, 3), "8"))
		wantSnapshot(t, check(t, s), "count=2 sum=9/2 max=8/2 last=8/2")
	})
	t.Run("backfill", func(t *testing.T) {
		s := open(t)
		write(t, s, ev("a", at(1, 0), "2", "region", "eu"), ev("b", at(5, 30), "3"))
		replace(t, s, ev("b", at(5, 30), "5"))
		define(t, s, countDef, valueDef)
		define(t, s, valueDef) // idempotent
		wantSnapshot(t, check(t, s), "count=2 sum=7/2 max=5/2 last=5/2")
		write(t, s, ev("c", at(6, 0), "1"))
		wantSnapshot(t, check(t, s), "count=3 sum=8/3 max=5/3 last=1/3")
		defs, err := s.BucketDefinitions(t.Context(), org, code)
		if err != nil {
			t.Fatal(err)
		}
		if len(defs) != 2 {
			t.Errorf("definitions got = %d, want = 2", len(defs))
		}
	})
	t.Run("drop", func(t *testing.T) {
		s := open(t)
		define(t, s, valueDef)
		write(t, s, ev("a", at(1, 0), "2"))
		if err := s.DropBuckets(t.Context(), valueDef); err != nil {
			t.Fatal(err)
		}
		if _, ok := totals(t, s, true); ok {
			t.Errorf("read after drop got = ok, want = no definition")
		}
		define(t, s, valueDef)
		if tot, ok := totals(t, s, true); !ok || tot.Sum.Cmp(money.NewFromInt(2)) != 0 {
			t.Errorf("sum after redefinition got = %s %v, want = 2 true", tot.Sum, ok)
		}
	})
	t.Run("all definitions", func(t *testing.T) {
		s := open(t)
		other := metering.BucketDefinition{OrganizationID: "org_a", Code: "zeta", Property: "value"}
		define(t, s, valueDef, other, countDef)
		if err := s.DropBuckets(t.Context(), countDef); err != nil {
			t.Fatal(err)
		}
		defs, err := s.AllBucketDefinitions(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, d := range defs {
			got = append(got, d.OrganizationID+"/"+d.Code+"/"+d.ID())
		}
		want := []string{"org_a/zeta/" + other.Normalize().ID(), org + "/" + code + "/" + valueDef.Normalize().ID()}
		if !slices.Equal(got, want) {
			t.Errorf("all definitions got = %v, want = %v", got, want)
		}
	})
	t.Run("codes without definitions", func(t *testing.T) {
		s := open(t)
		define(t, s, valueDef)
		other := ev("x", at(1, 0), "50")
		other.Code = "other"
		write(t, s, ev("a", at(1, 0), "2"), other)
		replace(t, s, other)
		wantSnapshot(t, check(t, s), "count=1 sum=2/1")
		if _, ok, err := s.ReadBuckets(t.Context(), metering.BucketRead{Query: metering.Query{OrganizationID: org, SubscriptionID: sub, Code: "other"}, From: day, To: day.Add(time.Hour)}); err != nil || ok {
			t.Errorf("read of a code without definitions got = %v %v, want = false <nil>", ok, err)
		}
	})
	t.Run("long dimension values", func(t *testing.T) {
		s := open(t)
		define(t, s, countDef, valueDef)
		long := strings.Repeat("r", 10<<10)
		write(t, s, ev("a", at(10, 0), "4", "region", long), ev("b", at(10, 1), "6", "region", long+"x"))
		write(t, s, ev("c", at(10, 2), "1", "region", long))
		replace(t, s, ev("b", at(10, 1), "7", "region", long))
		got := check(t, s)
		if !strings.Contains(got, "count=3 sum=12/3") {
			t.Errorf("snapshot got = %.80s, want prefix = count=3 sum=12/3", got)
		}
	})
	t.Run("folded dimension", func(t *testing.T) {
		s := open(t)
		folded := metering.BucketDefinition{
			OrganizationID: org, Code: code, Property: "value", Dimensions: []string{"region"},
			Values: map[string][]string{"region": {"eu", "us"}},
		}
		write(t, s, ev("a", at(10, 0), "1", "region", "eu"), ev("b", at(10, 1), "2", "region", "apac"))
		define(t, s, folded)
		write(t, s, ev("c", at(10, 2), "4", "region", "mars"), ev("d", at(10, 3), "8"), ev("e", at(10, 4), "16", "region", "us"))
		read := func(f metering.Filter) (string, bool) {
			q := metering.Query{OrganizationID: org, SubscriptionID: sub, Code: code, Numeric: true, Property: "value", Filter: f}
			rows, ok, err := s.ReadBuckets(t.Context(), metering.BucketRead{Query: q, From: day, To: day.Add(24 * time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 0 {
				return "", ok
			}
			return fmt.Sprintf("%s/%d", rows[0].Sum.Reduce(), rows[0].Count), ok
		}
		tests := []struct {
			name, want string
			filter     metering.Filter
			ok         bool
		}{
			{"declared value", "1/1", metering.Filter{Matching: map[string][]string{"region": {"eu"}}}, true},
			{"other values", "14/3", metering.Filter{Ignored: []map[string][]string{{"region": {"eu"}}, {"region": {"us"}}}}, true},
			{"undeclared value", "", metering.Filter{Matching: map[string][]string{"region": {"mars"}}}, false},
		}
		for _, tt := range tests {
			got, ok := read(tt.filter)
			if got != tt.want || ok != tt.ok {
				t.Errorf("%s got = %q %v, want = %q %v", tt.name, got, ok, tt.want, tt.ok)
			}
		}
		st, err := s.BucketStats(t.Context(), folded)
		if err != nil {
			t.Fatal(err)
		}
		// eu, us, the folded values and no region, in one bucket.
		if st.Rows != 4 || st.MaxRowsPerBucket != 4 {
			t.Errorf("stats got = %+v, want = {Rows:4 MaxRowsPerBucket:4}", st)
		}
	})
	t.Run("backfill alongside writers", func(t *testing.T) {
		s := open(t)
		var history []metering.Event
		for i := range 40 {
			e := ev(fmt.Sprintf("h%d", i), at(i%6, i*7%60), fmt.Sprint(i), "region", []string{"eu", "us", ""}[i%3])
			if i%4 == 0 {
				e.SubscriptionID = "sub_other"
			}
			history = append(history, e)
		}
		write(t, s, history...)
		var wg sync.WaitGroup
		errs := make(chan error, 32)
		wg.Go(func() {
			if err := s.DefineBuckets(context.Background(), valueDef); err != nil {
				errs <- err
			}
		})
		for w := range 4 {
			wg.Go(func() {
				for i := range 6 {
					e := ev(fmt.Sprintf("n%d-%d", w, i), at((w+i)%6, (w*13+i*11)%60), fmt.Sprint(100+i), "region", "eu")
					if _, err := s.Write(context.Background(), []metering.Event{e}); err != nil {
						errs <- err
						return
					}
					if i == 3 {
						if err := s.Replace(context.Background(), []metering.Event{history[w*5+1]}); err != nil {
							errs <- err
							return
						}
					}
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		check(t, s)
	})
	same := func(s Backend, n int) []Backend { return slices.Repeat([]Backend{s}, n) }
	for i, p := range append([]Peers{same}, peers...) {
		concurrent(t, open, p, i > 0)
	}
}

// concurrent runs writers of the same buckets concurrently, through the
// backends of peers.
func concurrent(t *testing.T, open Open, peers Peers, apart bool) {
	name := "concurrent writers"
	if apart {
		name += " of other processes"
	}
	t.Run(name, func(t *testing.T) {
		s := open(t)
		define(t, s, countDef)
		ws := peers(s, 8)
		var wg sync.WaitGroup
		errs := make(chan error, 64)
		for w := range 8 {
			s := ws[w]
			wg.Go(func() {
				for i := range 8 {
					// Every writer hits the same few buckets, re-sends the
					// events of another, and replaces some of its own.
					tx := fmt.Sprintf("w%d-%d", w, i)
					ts := at(10, (w+i)%4*7)
					region := []string{"eu", "us"}[i%2]
					if _, err := s.Write(context.Background(), []metering.Event{
						ev(tx, ts, fmt.Sprint(w*10+i), "region", region),
						ev(fmt.Sprintf("w%d-%d", (w+1)%8, i), at(10, (w+1+i)%4*7), "999", "region", region),
					}); err != nil {
						errs <- err
						return
					}
					if i%3 == 0 {
						if err := s.Replace(context.Background(), []metering.Event{ev(tx, ts, fmt.Sprint(i), "region", "eu")}); err != nil {
							errs <- err
							return
						}
					}
					if w == 0 && i == 4 {
						if err := s.DefineBuckets(context.Background(), valueDef); err != nil {
							errs <- err
							return
						}
					}
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		check(t, s)
	})
}
