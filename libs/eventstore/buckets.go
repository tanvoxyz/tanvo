package eventstore

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// BucketDefiner manages the usage bucket definitions of an event store.
//
// Once a definition is in place, the store maintains its buckets with every
// [Writer.Write] and [Replacer.Replace] of events of its organization and
// code, and answers [metering.BucketReader] reads from them. Its
// [metering.BucketLifecycle] drops the definitions nothing reads and
// blocks the oversized ones.
type BucketDefiner interface {
	metering.BucketLifecycle
	// DefineBuckets adds a definition and fills its buckets from the events
	// already stored. Defining a definition again is a no-op; defining a
	// blocked one fails with [metering.ErrBucketsBlocked]. A definition is
	// read only once its buckets hold every stored event.
	DefineBuckets(ctx context.Context, d metering.BucketDefinition) error
	// BucketDefinitions returns the definitions of an organization's
	// billable metric code, ready or still backfilling.
	BucketDefinitions(ctx context.Context, organizationID, code string) ([]metering.BucketDefinition, error)
	// AllBucketDefinitions returns the definitions of every organization
	// and code, ready or still backfilling, ordered by organization, code
	// and ID: the operator's view, to watch their [BucketStats].
	AllBucketDefinitions(ctx context.Context) ([]metering.BucketDefinition, error)
}

// BucketStats is the size of the buckets of a definition: a definition
// whose rows grow too many (pricing group or breakdown values without
// bounds) is better blocked, which sends its reads back to the events.
type BucketStats = metering.BucketStats
