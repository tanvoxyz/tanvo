package fees

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// bucketProbe is the event store of an aggregation that may read the usage
// buckets: it records the bucket reads no ready definition covered, which
// the events answered instead.
type bucketProbe struct {
	metering.Store
	reader metering.BucketReader
	missed *[]metering.BucketRead
}

var _ metering.BucketReader = bucketProbe{}

// ReadBuckets implements [metering.BucketReader].
func (p bucketProbe) ReadBuckets(ctx context.Context, r metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	totals, ok, err := p.reader.ReadBuckets(ctx, r)
	if err == nil && !ok {
		*p.missed = append(*p.missed, r)
	}
	return totals, ok, err
}

// eventsFor returns the event store the aggregation req reads: the store
// itself, or a [bucketProbe] recording in cc the bucket reads it misses
// when req may read the buckets of a store that keeps them.
func (s *Service) eventsFor(cc *chargeContext, req metering.Request) metering.Store {
	if !req.UsageBuckets {
		return s.d.Events
	}
	br, ok := s.d.Events.(metering.BucketReader)
	if !ok {
		return s.d.Events
	}
	return bucketProbe{Store: s.d.Events, reader: br, missed: &cc.bucketMisses}
}

// defineMissedBuckets enqueues, in the transaction of r, the
// [kernel.JobDefineUsageBuckets] of the definition covering the bucket
// reads of the charge that no ready definition covered. The buckets of
// lago are written by a stream for every charge they serve; Tanvo's are
// declared per metric and the read keys, from the reads that need them.
// Nothing is enqueued when the store defines no buckets or when no single
// definition covers the reads.
func (s *Service) defineMissedBuckets(ctx context.Context, r port.Repos, cc *chargeContext) error {
	if len(cc.bucketMisses) == 0 {
		return nil
	}
	if _, ok := s.d.Events.(kernel.BucketDefiner); !ok {
		return nil
	}
	var keys []string
	for _, read := range cc.bucketMisses {
		keys = append(keys, read.By...)
		keys = append(keys, read.Columns...)
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	org := cc.charge.OrganizationID
	def := metering.BucketDefinitionFor(org.String(), kernel.MeteringMetric(cc.metric), cc.metricF, keys)
	for _, read := range cc.bucketMisses {
		if !def.Covers(read) {
			s.d.Logger.DebugContext(ctx, "usage buckets: no definition covers the read",
				"organization_id", org.String(), "charge_id", cc.charge.ID.String(), "code", def.Code)
			return nil
		}
	}
	cc.bucketMisses = nil
	if err := kernel.Enqueue(ctx, r, org, kernel.JobDefineUsageBuckets, kernel.DefineUsageBucketsArgs{Definition: def},
		kernel.UsageBucketsKey(def), time.Time{}); err != nil {
		return fmt.Errorf("charge %s: %w", cc.charge.ID, err)
	}
	return nil
}
