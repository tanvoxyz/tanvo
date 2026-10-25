package kernel

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// JobDefineUsageBuckets defines a usage bucket definition in the event
// store and backfills its buckets from the stored events (args
// [DefineUsageBucketsArgs]). A current usage enqueues it when one of its
// charges may be served from the buckets but no ready definition covers
// its read: the read falls back to the events meanwhile, and the next ones
// read the buckets. It stands for the stream lago fills its buckets with
// for every supported charge (parity §3).
const JobDefineUsageBuckets = "billing.define_usage_buckets"

// DefineUsageBucketsArgs are the args of [JobDefineUsageBuckets].
type DefineUsageBucketsArgs struct {
	// Definition is the definition to define.
	Definition metering.BucketDefinition `json:"definition"`
}

// UsageBucketsKey is the unique key of the [JobDefineUsageBuckets] of the
// definition d: one pending job per definition, whatever the number of
// reads it serves.
func UsageBucketsKey(d metering.BucketDefinition) string {
	return "usage_buckets:" + d.OrganizationID + ":" + d.Code + ":" + d.ID()
}

// BucketDefiner adds usage bucket definitions to an event store: the part
// of libs/eventstore's BucketDefiner billing needs. An event store
// implementing it next to [metering.BucketReader] gets the definitions the
// current usage and the hourly usage read.
type BucketDefiner interface {
	// DefineBuckets adds a definition and fills its buckets from the
	// events already stored; defining it again is a no-op. A store
	// without usage buckets fails with [metering.ErrBucketsUnavailable].
	DefineBuckets(ctx context.Context, d metering.BucketDefinition) error
}
