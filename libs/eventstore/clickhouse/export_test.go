package clickhouse

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// SetHook sets the hook s calls at named stages of a write.
func SetHook(s *Store, hook func(stage string)) { s.hook = hook }

// BucketVersion returns the highest version of the rows of the bucket of e.
func BucketVersion(ctx context.Context, s *Store, e metering.Event) (uint64, error) {
	k := keyOf(row{Row: ingest.Row{Event: e}})
	st, _, err := s.states(consistent(ctx), buckets{org: e.OrganizationID, keys: []bucketKey{k}})
	return st[k].version, err
}

// RecomputeAt recomputes the bucket of e as version, by the recompute
// writer, without checking for conflicts.
func RecomputeAt(ctx context.Context, s *Store, e metering.Event, version, writer uint64) error {
	k := keyOf(row{Row: ingest.Row{Event: e}})
	b := buckets{org: e.OrganizationID, keys: []bucketKey{k}}
	return s.recomputeAt(consistent(ctx), b, counters{k: version}, writer&^1, "")
}
