package storetest

import (
	"context"
	"maps"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// NewBucketStore returns the events and the usage buckets under test: the
// store holds exactly the events, and the reader the buckets of the
// definitions over them.
type NewBucketStore func(t *testing.T, defs []metering.BucketDefinition, events []metering.Event) (metering.Store, metering.BucketReader)

// bucketMethods are the store methods [metering.UsageBucketStore] may answer
// from the buckets, and whether they read property values.
var bucketMethods = map[string]bool{
	"count": false, "grouped_count": false,
	"sum": true, "grouped_sum": true,
	"max": true, "grouped_max": true,
	"last": true, "grouped_last": true,
	"sum_precise_total_amount_cents": false, "grouped_sum_precise_total_amount_cents": false,
}

// requestKeys returns the properties an aggregation request reads.
func requestKeys(r metering.Request) []string {
	keys := slices.Concat(slices.Collect(maps.Keys(r.Filter.Matching)), slices.Collect(maps.Keys(r.GroupValues)), r.GroupBy, r.PresentationBy)
	for _, c := range r.Filter.Ignored {
		keys = slices.AppendSeq(keys, maps.Keys(c))
	}
	return keys
}

func queryKeys(c StoreCall) []string {
	q := c.Query
	keys := slices.Concat(slices.Collect(maps.Keys(q.Filter.Matching)), slices.Collect(maps.Keys(q.GroupValues)), c.By, c.Partition, c.Columns)
	for _, cl := range q.Filter.Ignored {
		keys = slices.AppendSeq(keys, maps.Keys(cl))
	}
	return keys
}

// BucketDefinitions returns the bucket definitions serving a case: for its
// code, one counting events and one aggregating the property it reads,
// keeping apart every property the case filters or groups on.
func BucketDefinitions(c Case) []metering.BucketDefinition {
	code := or(c.Code, DefaultCode)
	var (
		org, property string
		keys          []string
	)
	switch {
	case c.Store != nil:
		org, code = c.Store.Query.OrganizationID, or(c.Store.Query.Code, code)
		property, keys = c.Store.Query.Property, queryKeys(*c.Store)
	case c.Aggregate != nil:
		org, code = c.Aggregate.OrganizationID, or(c.Aggregate.Metric.Code, code)
		property, keys = c.Aggregate.Metric.FieldName, requestKeys(*c.Aggregate)
	default:
		return nil
	}
	org = or(org, DefaultOrganizationID)
	defs := []metering.BucketDefinition{{OrganizationID: org, Code: code, Dimensions: keys}}
	if property != "" {
		defs = append(defs, metering.BucketDefinition{OrganizationID: org, Code: code, Property: property, Dimensions: keys})
	}
	for i := range defs {
		defs[i] = defs[i].Normalize()
	}
	return defs
}

// wholeBucket reports whether a window, its bounds included, holds a whole
// bucket.
func wholeBucket(from, to time.Time) bool {
	lo := from.Truncate(time.Millisecond)
	end := to.Truncate(time.Microsecond).Add(time.Microsecond)
	return metering.BucketCeil(lo).Before(metering.BucketFloor(end))
}

// BucketServed reports whether a case is one the usage buckets serve: a
// count, sum, max or latest aggregation of a charge [metering.BucketServable]
// accepts (or a store call of the matching queries) over a window holding a
// whole bucket.
func BucketServed(c Case) bool {
	if c.WantError != "" || c.Skip != "" {
		return false
	}
	switch {
	case c.Store != nil:
		q := c.Store.Query
		values, ok := bucketMethods[c.Store.Method]
		if !ok || q.IncludeHistory || !q.Until.IsZero() || (values && (!q.Numeric || q.Property == "")) {
			return false
		}
		return wholeBucket(q.From, q.To)
	case c.Aggregate != nil:
		r := *c.Aggregate
		if r.Event != nil || !r.MaxTimestamp.IsZero() || r.BypassAggregation && !r.Metric.Recurring {
			return false
		}
		if !metering.BucketServable(r.Metric, metering.BucketCharge{ChargeModel: "standard", PayInAdvance: r.PayInAdvance, Prorated: r.Prorated}) {
			return false
		}
		return wholeBucket(r.From, r.To)
	}
	return false
}

// countingReader counts the reads the buckets answered.
type countingReader struct {
	metering.BucketReader
	served atomic.Int64
}

func (r *countingReader) ReadBuckets(ctx context.Context, read metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	totals, ok, err := r.BucketReader.ReadBuckets(ctx, read)
	if ok {
		r.served.Add(1)
	}
	return totals, ok, err
}

// RunBucketConformance runs every embedded case through a
// [metering.UsageBucketStore] over the events and buckets newStore returns,
// with the definitions of [BucketDefinitions]: the store must compute what
// the events alone compute, and the cases [BucketServed] accepts must have
// read the buckets.
func RunBucketConformance(t *testing.T, newStore NewBucketStore) {
	t.Helper()
	var eligible, served atomic.Int64
	t.Cleanup(func() {
		t.Logf("usage buckets served %d of %d eligible cases", served.Load(), eligible.Load())
	})
	RunCases(t, func(t *testing.T, c Case, events []metering.Event) metering.Store {
		events0, reader := newStore(t, BucketDefinitions(c), events)
		counting := &countingReader{BucketReader: reader}
		if BucketServed(c) {
			eligible.Add(1)
			t.Cleanup(func() {
				if counting.served.Load() == 0 {
					t.Errorf("%s: usage buckets read got = 0, want > 0", c.Source)
					return
				}
				served.Add(1)
			})
		}
		return metering.NewUsageBucketStore(events0, counting)
	})
}
