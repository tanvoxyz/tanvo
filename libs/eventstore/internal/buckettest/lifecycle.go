package buckettest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// states returns the definition states of the test organization by
// definition ID.
func states(t *testing.T, s Backend) map[string]metering.BucketDefinitionState {
	t.Helper()
	got, err := s.BucketDefinitionStates(t.Context(), org)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]metering.BucketDefinitionState{}
	for _, st := range got {
		if st.Definition.OrganizationID != org {
			t.Errorf("state organization got = %s, want = %s", st.Definition.OrganizationID, org)
		}
		key := st.Definition.ID()
		if st.Blocked {
			key += "/blocked"
		}
		out[key] = st
	}
	return out
}

// runLifecycle runs the tests of the definition lifecycle: states, blocks,
// and drops overlapping reads, writes and definitions.
func runLifecycle(t *testing.T, open Open) {
	t.Run("states", func(t *testing.T) {
		s := open(t)
		start := time.Now().Add(-time.Minute)
		other := metering.BucketDefinition{OrganizationID: "org_a", Code: "zeta", Property: "value"}
		define(t, s, countDef, valueDef, other)
		write(t, s, ev("a", at(1, 0), "2"))
		got := states(t, s)
		if len(got) != 2 {
			t.Fatalf("states got = %d, want = 2", len(got))
		}
		v := got[valueDef.Normalize().ID()]
		if !v.Ready || v.Blocked || v.DefinedAt.Before(start) || !v.ReadAt.IsZero() {
			t.Errorf("state before read got = %+v, want = ready, defined after %s, never read", v, start)
		}
		if _, ok := totals(t, s, true); !ok {
			t.Fatal("read got = no definition, want = ok")
		}
		v = states(t, s)[valueDef.Normalize().ID()]
		if v.ReadAt.Before(start) || v.LastUsed() != v.ReadAt && v.LastUsed() != v.DefinedAt {
			t.Errorf("state after read got = %+v, want = read after %s", v, start)
		}
		all, err := s.BucketDefinitionStates(t.Context(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 3 || all[0].Definition.OrganizationID != "org_a" {
			t.Errorf("states of every organization got = %d (first %+v), want = 3, org_a first", len(all), all[0].Definition)
		}
	})
	t.Run("block", func(t *testing.T) {
		s := open(t)
		define(t, s, valueDef, countDef)
		write(t, s, ev("a", at(1, 0), "2", "region", "eu"))
		if err := s.BlockBuckets(t.Context(), valueDef); err != nil {
			t.Fatal(err)
		}
		if _, ok := totals(t, s, true); ok {
			t.Errorf("read of a blocked definition got = ok, want = no definition")
		}
		if err := s.DefineBuckets(t.Context(), valueDef); !errors.Is(err, metering.ErrBucketsBlocked) || !errors.Is(err, metering.ErrBucketsUnavailable) {
			t.Errorf("define blocked got = %v, want = %v", err, metering.ErrBucketsBlocked)
		}
		got := states(t, s)
		id := valueDef.Normalize().ID()
		if b, ok := got[id+"/blocked"]; !ok || b.Ready || b.DefinedAt.IsZero() {
			t.Errorf("block state got = %+v %v, want = blocked with its instant", b, ok)
		}
		if _, ok := got[id]; ok {
			t.Errorf("blocked definition state got = present, want = dropped")
		}
		if _, ok := got[countDef.Normalize().ID()]; !ok {
			t.Errorf("other definition got = dropped, want = kept")
		}
		write(t, s, ev("b", at(1, 5), "3", "region", "eu"))
		if st, err := s.BucketStats(t.Context(), valueDef); err != nil || st.Rows != 0 {
			t.Errorf("stats of a blocked definition got = %+v %v, want = no rows", st, err)
		}
		if err := s.DropBuckets(t.Context(), valueDef); err != nil {
			t.Fatal(err)
		}
		if got := states(t, s); len(got) != 1 {
			t.Errorf("states after lifting the block got = %d, want = 1", len(got))
		}
		define(t, s, valueDef)
		if tot, ok := totals(t, s, true); !ok || tot.Sum.Cmp(money.NewFromInt(5)) != 0 {
			t.Errorf("sum after lifting the block got = %s %v, want = 5 true", tot.Sum, ok)
		}
		check(t, s)
	})
	t.Run("drop of a definition never stored", func(t *testing.T) {
		s := open(t)
		if err := s.DropBuckets(t.Context(), valueDef); err != nil {
			t.Fatal(err)
		}
		if got := states(t, s); len(got) != 0 {
			t.Errorf("states got = %d, want = 0", len(got))
		}
	})
	t.Run("drops alongside readers", func(t *testing.T) {
		s := open(t)
		var events []metering.Event
		for i := range 30 {
			events = append(events, ev(fmt.Sprintf("r%d", i), at(i%5, i*7%60), "1", "region", []string{"eu", "us"}[i%2]))
		}
		write(t, s, events...)
		define(t, s, valueDef)
		var wg sync.WaitGroup
		errs := make(chan error, 64)
		done := make(chan struct{})
		for range 4 {
			wg.Go(func() {
				q := metering.Query{OrganizationID: org, SubscriptionID: sub, Code: code, Numeric: true, Property: "value"}
				for {
					select {
					case <-done:
						return
					default:
					}
					rows, ok, err := s.ReadBuckets(context.Background(), metering.BucketRead{Query: q, From: day, To: day.Add(24 * time.Hour)})
					if err != nil {
						errs <- err
						return
					}
					// A read served while the definition is dropped,
					// blocked or backfilled again holds every event.
					if ok && (len(rows) != 1 || rows[0].Count != 30 || rows[0].Sum.Cmp(money.NewFromInt(30)) != 0) {
						errs <- fmt.Errorf("read during drops got = %+v, want = count 30, sum 30", rows)
						return
					}
				}
			})
		}
		wg.Go(func() {
			defer close(done)
			ctx := context.Background()
			for i := range 6 {
				drop := s.DropBuckets
				if i%2 == 1 {
					drop = func(ctx context.Context, d metering.BucketDefinition) error {
						if err := s.BlockBuckets(ctx, d); err != nil {
							return err
						}
						return s.DropBuckets(ctx, d)
					}
				}
				if err := drop(ctx, valueDef); err != nil {
					errs <- err
					return
				}
				if err := s.DefineBuckets(ctx, valueDef); err != nil {
					errs <- err
					return
				}
			}
		})
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		check(t, s)
	})
	t.Run("drops alongside writers and definers", func(t *testing.T) {
		s := open(t)
		var history []metering.Event
		for i := range 40 {
			history = append(history, ev(fmt.Sprintf("h%d", i), at(i%6, i*7%60), fmt.Sprint(i), "region", []string{"eu", "us", ""}[i%3]))
		}
		write(t, s, history...)
		define(t, s, valueDef)
		var wg sync.WaitGroup
		errs := make(chan error, 64)
		for w := range 4 {
			wg.Go(func() {
				for i := range 6 {
					e := ev(fmt.Sprintf("n%d-%d", w, i), at((w+i)%6, (w*13+i*11)%60), fmt.Sprint(100+i), "region", "eu")
					if _, err := s.Write(context.Background(), []metering.Event{e}); err != nil {
						errs <- err
						return
					}
				}
			})
		}
		// Definers and droppers race: a drop may land in the middle of a
		// backfill.
		for g := range 2 {
			wg.Go(func() {
				for range 4 {
					var err error
					if g == 0 {
						err = s.DefineBuckets(context.Background(), valueDef)
					} else {
						err = s.DropBuckets(context.Background(), valueDef)
					}
					if err != nil {
						errs <- err
						return
					}
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		define(t, s, valueDef)
		check(t, s)
		if got := states(t, s); len(got) != 1 || !got[valueDef.Normalize().ID()].Ready {
			t.Errorf("states got = %+v, want = the definition, ready", got)
		}
	})
}
