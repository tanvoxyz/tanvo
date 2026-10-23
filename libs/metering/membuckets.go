package metering

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// MemoryBuckets is the reference [BucketReader]: the usage buckets of the
// events of a [MemoryStore], built at construction. The event stores
// maintain the same rows in their databases.
type MemoryBuckets struct {
	defs []BucketDefinition
	rows []bucketRow
}

var _ BucketReader = (*MemoryBuckets)(nil)

// bucketRow is one row of the buckets: the events of one subscription,
// code, definition, bucket and combination of dimension values.
type bucketRow struct {
	organizationID, subscriptionID, code, definitionID string
	bucket                                             time.Time
	dimensions                                         Properties

	eventsCount int64
	amount      money.Decimal
	valueCount  int64
	units       money.Decimal
	max         *money.Decimal
	latest      *money.Decimal
	latestAt    time.Time
	latestSeq   int
	valueAmount money.Decimal
	lastEventAt time.Time
	lastIngest  time.Time
}

// Buckets returns the usage buckets of the definitions over the events of
// the store.
func (s *MemoryStore) Buckets(defs ...BucketDefinition) *MemoryBuckets {
	b := &MemoryBuckets{}
	index := map[string]int{}
	seen := map[string]bool{}
	for _, d := range defs {
		d = d.Normalize()
		id := d.OrganizationID + "\x00" + d.Code + "\x00" + d.ID()
		if seen[id] {
			continue
		}
		seen[id] = true
		b.defs = append(b.defs, d)
		id = d.ID()
		for _, e := range s.events {
			if e.OrganizationID != d.OrganizationID || e.Code != d.Code {
				continue
			}
			dims := d.DimensionsOf(e.Properties)
			bucket := BucketFloor(e.Timestamp)
			k := e.SubscriptionID + "\x00" + e.Code + "\x00" + id + "\x00" + bucket.String() + "\x00" + propertiesKey(dims)
			i, ok := index[k]
			if !ok {
				i = len(b.rows)
				index[k] = i
				b.rows = append(b.rows, bucketRow{
					organizationID: e.OrganizationID, subscriptionID: e.SubscriptionID, code: e.Code, definitionID: id,
					bucket: bucket, dimensions: dims, amount: money.Zero, units: money.Zero, valueAmount: money.Zero,
				})
			}
			r := &b.rows[i]
			r.eventsCount++
			amount := money.Zero
			if e.PreciseTotalAmountCents != nil {
				amount = *e.PreciseTotalAmountCents
			}
			r.amount = r.amount.Add(amount)
			if e.Timestamp.After(r.lastEventAt) {
				r.lastEventAt = e.Timestamp
			}
			if e.IngestedAt.After(r.lastIngest) {
				r.lastIngest = e.IngestedAt
			}
			v, ok := numeric(e.Properties, d.Property)
			if d.Property == "" || !ok {
				continue
			}
			r.valueCount++
			r.units = r.units.Add(v)
			r.valueAmount = r.valueAmount.Add(amount)
			if r.max == nil || v.GreaterThan(*r.max) {
				r.max = &v
			}
			if r.latest == nil || cmp.Or(e.Timestamp.Compare(r.latestAt), cmp.Compare(e.seq, r.latestSeq)) > 0 {
				r.latest, r.latestAt, r.latestSeq = &v, e.Timestamp, e.seq
			}
		}
	}
	return b
}

// propertiesKey returns a canonical string identifying the properties,
// telling a missing property from an empty one.
func propertiesKey(p Properties) string {
	g := make(Groups, len(p))
	for k, v := range p {
		g[k] = &v
	}
	return g.Key()
}

// ReadBuckets implements [BucketReader].
func (b *MemoryBuckets) ReadBuckets(ctx context.Context, r BucketRead) ([]BucketTotal, bool, error) {
	if err := checkContext(ctx); err != nil {
		return nil, false, err
	}
	d, ok := CoveringDefinition(b.defs, r)
	if !ok {
		return nil, false, nil
	}
	q := r.Query
	id := d.ID()
	var (
		out    []BucketTotal
		latest []struct {
			at  time.Time
			seq int
		}
		index = map[string]int{}
	)
	for _, row := range b.rows {
		if row.organizationID != q.OrganizationID || row.subscriptionID != q.SubscriptionID || row.code != q.Code ||
			row.definitionID != id || row.bucket.Before(r.From) || !row.bucket.Before(r.To) ||
			!q.GroupValues.matches(row.dimensions) || !q.Filter.Matches(row.dimensions) {
			continue
		}
		count, amount, last := row.eventsCount, row.amount, row.lastEventAt
		if q.Numeric {
			count, amount, last = row.valueCount, row.valueAmount, row.latestAt
		}
		groups := GroupsOf(row.dimensions, r.By)
		k := groups.Key()
		var bucket time.Time
		if r.PerBucket {
			bucket = row.bucket
			k += bucket.String()
		}
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, BucketTotal{Groups: groups, Bucket: bucket, Sum: money.Zero, PreciseTotalAmountCents: money.Zero})
			latest = append(latest, struct {
				at  time.Time
				seq int
			}{})
		}
		t := &out[i]
		t.Count += count
		t.EventsCount += row.eventsCount
		t.PreciseTotalAmountCents = t.PreciseTotalAmountCents.Add(amount)
		if last.After(t.LastEventAt) {
			t.LastEventAt = last
		}
		if row.lastIngest.After(t.LastIngestedAt) {
			t.LastIngestedAt = row.lastIngest
		}
		if !q.Numeric {
			continue
		}
		t.Sum = t.Sum.Add(row.units)
		if row.max != nil && (t.Max == nil || row.max.GreaterThan(*t.Max)) {
			t.Max = row.max
		}
		l := &latest[i]
		if row.latest != nil && (t.Latest == nil || cmp.Or(row.latestAt.Compare(l.at), cmp.Compare(row.latestSeq, l.seq)) > 0) {
			t.Latest, l.at, l.seq = row.latest, row.latestAt, row.latestSeq
			t.LatestColumns = GroupsOf(row.dimensions, r.Columns)
		}
	}
	slices.SortStableFunc(out, func(a, b BucketTotal) int { return a.Bucket.Compare(b.Bucket) })
	return out, true, nil
}
