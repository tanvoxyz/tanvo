package kernel

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
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

// JobSweepUsageBuckets drops the usage bucket definitions of an
// organization nothing reads any more (args [InstantArgs], unique per
// organization, [SweepUsageBucketsKey]): those of a deleted metric, of a
// metric whose code, property or filters changed, of a metric no live
// charge reads from the buckets, and those no read used for the idle
// time. The daily clock enqueues it for every organization with
// definitions, and a metric update or deletion for its organization. It
// is Tanvo design: lago's buckets are keyed by charge and have no
// definitions to drop.
const JobSweepUsageBuckets = "billing.sweep_usage_buckets"

// SweepUsageBucketsKey is the unique key of the [JobSweepUsageBuckets] of
// an organization: one pending sweep, which sweeps every definition.
const SweepUsageBucketsKey = "usage_buckets:sweep"

// BucketLifecycle drops and blocks usage bucket definitions: the
// lifecycle of libs/eventstore's BucketDefiner billing needs
// ([metering.BucketLifecycle]).
type BucketLifecycle = metering.BucketLifecycle

// EnqueueUsageBucketsSweep enqueues, in the transaction of r, the
// [JobSweepUsageBuckets] of the organization org at the instant at, once
// while one is pending.
func EnqueueUsageBucketsSweep(ctx context.Context, r port.Repos, org core.OrganizationID, at time.Time) error {
	return Enqueue(ctx, r, org, JobSweepUsageBuckets, InstantArgs{At: at}, SweepUsageBucketsKey, time.Time{})
}
