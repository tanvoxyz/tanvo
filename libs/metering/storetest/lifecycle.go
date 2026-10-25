package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// LifecycleBuckets is the usage buckets of an event store whose
// definitions are defined, dropped and blocked.
type LifecycleBuckets interface {
	metering.BucketReader
	metering.BucketLifecycle
	// DefineBuckets defines a definition and backfills its buckets.
	DefineBuckets(ctx context.Context, d metering.BucketDefinition) error
}

// NewLifecycleStore returns the events and the usage buckets under test:
// the store holds exactly the events, and the buckets have no definition.
type NewLifecycleStore func(t *testing.T, events []metering.Event) (metering.Store, LifecycleBuckets)

// lifecycleStage is what [RunBucketLifecycle] does to the definitions of
// a case before running it.
type lifecycleStage struct {
	name string
	// prepare acts on the definitions of the case, defined.
	prepare func(t *testing.T, b LifecycleBuckets, defs []metering.BucketDefinition)
	// served reports whether the buckets must serve the case after.
	served bool
}

var lifecycleStages = []lifecycleStage{
	{name: "dropped", prepare: func(t *testing.T, b LifecycleBuckets, defs []metering.BucketDefinition) {
		t.Helper()
		for _, d := range defs {
			if err := b.DropBuckets(t.Context(), d); err != nil {
				t.Fatal(err)
			}
		}
	}},
	{name: "blocked", prepare: func(t *testing.T, b LifecycleBuckets, defs []metering.BucketDefinition) {
		t.Helper()
		for _, d := range defs {
			if err := b.BlockBuckets(t.Context(), d); err != nil {
				t.Fatal(err)
			}
			if err := b.DefineBuckets(t.Context(), d); !errors.Is(err, metering.ErrBucketsBlocked) {
				t.Fatalf("define blocked got = %v, want = %v", err, metering.ErrBucketsBlocked)
			}
		}
		states, err := b.BucketDefinitionStates(t.Context(), defs[0].OrganizationID)
		if err != nil {
			t.Fatal(err)
		}
		blocked := 0
		for _, s := range states {
			if s.Blocked && !s.Ready && s.Definition.Code == defs[0].Code {
				blocked++
			}
		}
		if blocked != len(defs) {
			t.Errorf("blocked states got = %d, want = %d", blocked, len(defs))
		}
	}},
	{name: "redefined", served: true, prepare: func(t *testing.T, b LifecycleBuckets, defs []metering.BucketDefinition) {
		t.Helper()
		for _, d := range defs {
			// Block, lift the block, then define again.
			for _, step := range []func(context.Context, metering.BucketDefinition) error{b.BlockBuckets, b.DropBuckets, b.DefineBuckets} {
				if err := step(t.Context(), d); err != nil {
					t.Fatal(err)
				}
			}
		}
	}},
}

// RunBucketLifecycle runs the embedded cases the usage buckets serve
// ([BucketServed]) through a [metering.UsageBucketStore] over the events
// and buckets newStore returns, with the definitions of
// [BucketDefinitions] defined, then dropped, blocked, or blocked, lifted
// and defined again: the store must compute what the events alone compute
// at every stage, from the events while the definitions are dropped or
// blocked, and from the buckets again once they are defined again.
func RunBucketLifecycle(t *testing.T, newStore NewLifecycleStore) {
	t.Helper()
	for _, stage := range lifecycleStages {
		t.Run(stage.name, func(t *testing.T) {
			RunCases(t, func(t *testing.T, c Case, events []metering.Event) metering.Store {
				if !BucketServed(c) {
					t.Skip("not served by the usage buckets")
				}
				store, buckets := newStore(t, events)
				defs := BucketDefinitions(c)
				for _, d := range defs {
					if err := buckets.DefineBuckets(t.Context(), d); err != nil {
						t.Fatal(err)
					}
				}
				stage.prepare(t, buckets, defs)
				counting := &countingReader{BucketReader: buckets}
				t.Cleanup(func() {
					switch n := counting.served.Load(); {
					case stage.served && n == 0:
						t.Errorf("%s: usage buckets read got = 0, want > 0", c.Source)
					case !stage.served && n != 0:
						t.Errorf("%s: usage buckets read got = %d, want = 0", c.Source, n)
					}
				})
				return metering.NewUsageBucketStore(store, counting)
			})
		})
	}
}
