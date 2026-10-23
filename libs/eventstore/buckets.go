package eventstore

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// BucketDefiner manages the usage bucket definitions of an event store.
//
// Once a definition is in place, the store maintains its buckets with every
// [Writer.Write] and [Replacer.Replace] of events of its organization and
// code, and answers [metering.BucketReader] reads from them.
type BucketDefiner interface {
	// DefineBuckets adds a definition and fills its buckets from the events
	// already stored. Defining a definition again is a no-op. A definition
	// is read only once its buckets hold every stored event.
	DefineBuckets(ctx context.Context, d metering.BucketDefinition) error
	// DropBuckets removes a definition and its buckets.
	DropBuckets(ctx context.Context, d metering.BucketDefinition) error
	// BucketDefinitions returns the definitions of an organization's
	// billable metric code, ready or still backfilling.
	BucketDefinitions(ctx context.Context, organizationID, code string) ([]metering.BucketDefinition, error)
	// AllBucketDefinitions returns the definitions of every organization
	// and code, ready or still backfilling, ordered by organization, code
	// and ID: the operator's view, to watch their [BucketStats].
	AllBucketDefinitions(ctx context.Context) ([]metering.BucketDefinition, error)
	// BucketStats returns the size of the buckets of a definition, to watch
	// their cardinality: a definition whose rows grow too many (pricing
	// group or breakdown values without bounds) is better dropped, which
	// sends its reads back to the events.
	BucketStats(ctx context.Context, d metering.BucketDefinition) (BucketStats, error)
}

// BucketStats is the size of the buckets of a definition.
type BucketStats struct {
	// Rows is the number of rows, one per subscription, bucket and
	// combination of dimension values.
	Rows int64
	// MaxRowsPerBucket is the largest number of rows of one bucket of one
	// subscription: its combinations of dimension values.
	MaxRowsPerBucket int64
}
