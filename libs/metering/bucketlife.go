package metering

import (
	"context"
	"fmt"
	"time"
)

// ErrBucketsBlocked reports the definition of a [BucketLifecycle.BlockBuckets]
// block: its buckets are not maintained and defining it again is refused,
// so that its reads stay on the events. It is an [ErrBucketsUnavailable].
var ErrBucketsBlocked = fmt.Errorf("%w: definition blocked", ErrBucketsUnavailable)

// BucketStats is the size of the usage buckets of a definition.
type BucketStats struct {
	// Rows is the number of rows, one per subscription, bucket and
	// combination of dimension values.
	Rows int64
	// MaxRowsPerBucket is the largest number of rows of one bucket of one
	// subscription: its combinations of dimension values.
	MaxRowsPerBucket int64
}

// BucketDefinitionState is a stored usage bucket definition, or the block
// of one, and what its lifecycle is decided on.
type BucketDefinitionState struct {
	// Definition is the definition, normalized.
	Definition BucketDefinition
	// Ready reports a definition whose buckets hold every stored event,
	// which reads use.
	Ready bool
	// Blocked reports the block of a definition (see
	// [BucketLifecycle.BlockBuckets]): it has no buckets and is not
	// defined again.
	Blocked bool
	// DefinedAt is when the definition was defined (blocked, for a
	// block); zero when the store does not know.
	DefinedAt time.Time
	// ReadAt is the last time a read was served from the buckets, to
	// within [BucketReadResolution]; zero before the first one.
	ReadAt time.Time
}

// LastUsed returns the later of the last read and the definition: the
// instant the idle time of the definition is counted from.
func (s BucketDefinitionState) LastUsed() time.Time {
	if s.ReadAt.After(s.DefinedAt) {
		return s.ReadAt
	}
	return s.DefinedAt
}

// BucketReadResolution is the resolution of [BucketDefinitionState.ReadAt]:
// an event store records the read of a definition at most once per
// resolution and process, so that reads cost no write.
const BucketReadResolution = time.Hour

// BucketLifecycle drops the usage bucket definitions nothing reads any
// more and blocks the oversized ones. The PostgreSQL and ClickHouse event
// stores implement it next to [BucketReader].
//
// Drops are safe against concurrent writes and reads: a read checks the
// definition in the snapshot of its rows, or again after them, so that a
// read overlapping a drop never answers from part of the buckets, but
// from the events ([BucketReader.ReadBuckets] reports no definition); a
// writer that saw the definition before the drop leaves no row a later
// definition reads.
type BucketLifecycle interface {
	// BucketDefinitionStates returns the definitions and blocks of an
	// organization, ready or still backfilling, ordered by code and ID;
	// with organizationID empty, those of every organization, ordered by
	// organization first.
	BucketDefinitionStates(ctx context.Context, organizationID string) ([]BucketDefinitionState, error)
	// DropBuckets removes a definition and its buckets, and lifts its
	// block. Dropping a definition that is not stored is a no-op. The
	// reads it covered go back to the events until it is defined again.
	DropBuckets(ctx context.Context, d BucketDefinition) error
	// BlockBuckets drops a definition and blocks it: defining it again
	// fails with [ErrBucketsBlocked], until [BucketLifecycle.DropBuckets]
	// lifts the block.
	BlockBuckets(ctx context.Context, d BucketDefinition) error
	// BucketStats returns the size of the buckets of a definition.
	BucketStats(ctx context.Context, d BucketDefinition) (BucketStats, error)
}

// BucketDefinitionCurrent reports whether d is a definition
// [BucketDefinitionFor] returns for the billable metric m with the metric
// filters, for some keys: the same code and aggregated property, and the
// filter keys folding exactly the values the metric filters declare. A
// definition that is not current serves no read of the metric any more,
// as every read defines the definition of the current metric.
func BucketDefinitionCurrent(d BucketDefinition, m Metric, filters []MetricFilter) bool {
	n := d.Normalize()
	var keys []string
	for _, k := range n.Dimensions {
		// A folded key is a filter key no grouping reads.
		if _, folded := n.Values[k]; !folded && k != TargetWalletCodeProperty {
			keys = append(keys, k)
		}
	}
	want := BucketDefinitionFor(n.OrganizationID, m, filters, keys)
	return want.Code == n.Code && want.ID() == n.ID()
}
