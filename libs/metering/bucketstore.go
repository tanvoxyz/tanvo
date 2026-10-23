package metering

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// UsageBucketStore is a [Store] answering from the usage buckets the
// queries they cover, and from the events it wraps everything else.
//
// A query is served from the buckets when it reads count, sum, max, latest
// or precise amounts over a window of its period (no history, no
// pay-in-advance bound), with a numeric property for the values, and a
// ready [BucketDefinition] covers its keys. The window is split on bucket
// boundaries: the whole buckets it holds are read from the buckets, and
// the partial buckets at its ends, if any, from the events, so that the
// answer is exactly the one of the events whatever the boundaries of the
// period. When the window holds no whole bucket, the events answer.
//
// The three segments are disjoint and ordered in time, which is how they
// combine: counts, sums and amounts add up, maxima take the largest, and
// the latest value is the one of the last segment holding events.
type UsageBucketStore struct {
	Store
	buckets BucketReader
}

var _ Store = (*UsageBucketStore)(nil)

// NewUsageBucketStore returns a store answering from buckets what they
// cover and from events the rest.
func NewUsageBucketStore(events Store, buckets BucketReader) *UsageBucketStore {
	return &UsageBucketStore{Store: events, buckets: buckets}
}

// segmentPlan is a query split on bucket boundaries.
type segmentPlan struct {
	lead, trail *Query
	read        BucketRead
}

// plan splits q, read for the given keys, when the buckets may answer it.
// values reports a read of the property values (sum, max, latest), which
// needs a numeric property.
func (s *UsageBucketStore) plan(q Query, by, columns []string, values bool) (segmentPlan, bool) {
	if q.OrganizationID == "" || q.SubscriptionID == "" || q.Code == "" ||
		q.IncludeHistory || !q.Until.IsZero() || q.UntilTransactionID != "" {
		return segmentPlan{}, false
	}
	if (q.Numeric && q.Property == "") || (values && !q.Numeric) {
		return segmentPlan{}, false
	}
	lo := q.From.Truncate(time.Millisecond).UTC()
	// Events are stored to the microsecond: those at or before To are
	// those before end.
	end := q.To.UTC().Truncate(time.Microsecond).Add(time.Microsecond)
	start, stop := BucketCeil(lo), BucketFloor(end)
	if !start.Before(stop) {
		return segmentPlan{}, false
	}
	rq := q
	if !rq.Numeric {
		rq.Property = ""
	}
	p := segmentPlan{read: BucketRead{Query: rq, From: start, To: stop, By: by, Columns: columns}}
	if lo.Before(start) {
		lead := q
		lead.To = start.Add(-time.Microsecond)
		p.lead = &lead
	}
	if stop.Before(end) {
		trail := q
		trail.From = stop
		p.trail = &trail
	}
	return p, true
}

// part is the total of one group over one segment.
type part struct {
	groups  Groups
	count   int64
	sum     money.Decimal
	max     *money.Decimal
	latest  *money.Decimal
	columns Groups
	amount  money.Decimal
}

// merged accumulates the parts of the segments in time order.
type merged struct {
	order []string
	parts map[string]*part
}

func (m *merged) add(p part) {
	if p.count == 0 {
		return
	}
	if m.parts == nil {
		m.parts = map[string]*part{}
	}
	k := p.groups.Key()
	acc, ok := m.parts[k]
	if !ok {
		m.order = append(m.order, k)
		cp := p
		m.parts[k] = &cp
		return
	}
	acc.count += p.count
	acc.sum = acc.sum.Add(p.sum)
	acc.amount = acc.amount.Add(p.amount)
	if p.max != nil && (acc.max == nil || p.max.GreaterThan(*acc.max)) {
		acc.max = p.max
	}
	// A later segment holds the later events.
	if p.latest != nil {
		acc.latest, acc.columns = p.latest, p.columns
	}
}

func (m *merged) each(yield func(*part)) {
	for _, k := range m.order {
		yield(m.parts[k])
	}
}

// run reads the segments of a plan: the events for the partial buckets
// with raw, the buckets for the whole ones. ok is false when no definition
// covers the read; the caller then asks the events.
func (s *UsageBucketStore) run(ctx context.Context, p segmentPlan, raw func(Query) ([]part, error)) (*merged, bool, error) {
	totals, ok, err := s.buckets.ReadBuckets(ctx, p.read)
	if err != nil {
		return nil, false, fmt.Errorf("metering: usage buckets: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	m := &merged{}
	if p.lead != nil {
		parts, err := raw(*p.lead)
		if err != nil {
			return nil, false, err
		}
		for _, pt := range parts {
			m.add(pt)
		}
	}
	for _, t := range totals {
		m.add(part{
			groups: t.Groups, count: t.Count, sum: t.Sum, max: t.Max, latest: t.Latest,
			columns: t.LatestColumns, amount: t.PreciseTotalAmountCents,
		})
	}
	if p.trail != nil {
		parts, err := raw(*p.trail)
		if err != nil {
			return nil, false, err
		}
		for _, pt := range parts {
			m.add(pt)
		}
	}
	return m, true, nil
}

func groupedParts(rows []GroupedAggregate, set func(*part, GroupedAggregate)) []part {
	out := make([]part, 0, len(rows))
	for _, r := range rows {
		p := part{groups: r.Groups, count: r.Count}
		set(&p, r)
		out = append(out, p)
	}
	return out
}

// Count implements [Store].
func (s *UsageBucketStore) Count(ctx context.Context, q Query) (int64, error) {
	p, ok := s.plan(q, nil, nil, false)
	if !ok {
		return s.Store.Count(ctx, q)
	}
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		n, err := s.Store.Count(ctx, q)
		return []part{{groups: Groups{}, count: n}}, err
	})
	if err != nil {
		return 0, err
	}
	if !ok {
		return s.Store.Count(ctx, q)
	}
	var n int64
	m.each(func(p *part) { n += p.count })
	return n, nil
}

// GroupedCount implements [Store].
func (s *UsageBucketStore) GroupedCount(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	p, ok := s.plan(q, by, nil, false)
	if !ok {
		return s.Store.GroupedCount(ctx, q, by)
	}
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		rows, err := s.Store.GroupedCount(ctx, q, by)
		return groupedParts(rows, func(*part, GroupedAggregate) {}), err
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.Store.GroupedCount(ctx, q, by)
	}
	var out []GroupedAggregate
	m.each(func(p *part) {
		out = append(out, GroupedAggregate{Groups: p.groups, Value: money.NewFromInt(p.count), Count: p.count})
	})
	return out, nil
}

// Sum implements [Store].
func (s *UsageBucketStore) Sum(ctx context.Context, q Query) (Total, error) {
	p, ok := s.plan(q, nil, nil, true)
	if !ok {
		return s.Store.Sum(ctx, q)
	}
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		t, err := s.Store.Sum(ctx, q)
		return []part{{groups: Groups{}, count: t.Count, sum: t.Value}}, err
	})
	if err != nil {
		return Total{}, err
	}
	if !ok {
		return s.Store.Sum(ctx, q)
	}
	t := Total{Value: money.Zero}
	m.each(func(p *part) { t.Value, t.Count = t.Value.Add(p.sum), t.Count+p.count })
	return t, nil
}

// GroupedSum implements [Store].
func (s *UsageBucketStore) GroupedSum(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	p, ok := s.plan(q, by, nil, true)
	if !ok {
		return s.Store.GroupedSum(ctx, q, by)
	}
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		rows, err := s.Store.GroupedSum(ctx, q, by)
		return groupedParts(rows, func(p *part, r GroupedAggregate) { p.sum = r.Value }), err
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.Store.GroupedSum(ctx, q, by)
	}
	var out []GroupedAggregate
	m.each(func(p *part) { out = append(out, GroupedAggregate{Groups: p.groups, Value: p.sum, Count: p.count}) })
	return out, nil
}

// Max implements [Store].
func (s *UsageBucketStore) Max(ctx context.Context, q Query) (Total, error) {
	p, ok := s.plan(q, nil, nil, true)
	if !ok {
		return s.Store.Max(ctx, q)
	}
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		t, err := s.Store.Max(ctx, q)
		return []part{{groups: Groups{}, count: t.Count, max: &t.Value}}, err
	})
	if err != nil {
		return Total{}, err
	}
	if !ok {
		return s.Store.Max(ctx, q)
	}
	t := Total{Value: money.Zero}
	m.each(func(p *part) {
		t.Count = p.count
		if p.max != nil {
			t.Value = *p.max
		}
	})
	return t, nil
}

// GroupedMax implements [Store].
func (s *UsageBucketStore) GroupedMax(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	p, ok := s.plan(q, by, nil, true)
	if !ok {
		return s.Store.GroupedMax(ctx, q, by)
	}
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		rows, err := s.Store.GroupedMax(ctx, q, by)
		return groupedParts(rows, func(p *part, r GroupedAggregate) { p.max = &r.Value }), err
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.Store.GroupedMax(ctx, q, by)
	}
	var out []GroupedAggregate
	m.each(func(p *part) {
		v := money.Zero
		if p.max != nil {
			v = *p.max
		}
		out = append(out, GroupedAggregate{Groups: p.groups, Value: v, Count: p.count})
	})
	return out, nil
}

// Last implements [Store].
func (s *UsageBucketStore) Last(ctx context.Context, q Query) (LastValue, error) {
	p, ok := s.plan(q, nil, nil, true)
	if !ok {
		return s.Store.Last(ctx, q)
	}
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		l, err := s.Store.Last(ctx, q)
		return []part{{groups: Groups{}, count: l.Count, latest: l.Value}}, err
	})
	if err != nil {
		return LastValue{}, err
	}
	if !ok {
		return s.Store.Last(ctx, q)
	}
	var l LastValue
	m.each(func(p *part) { l = LastValue{Value: p.latest, Count: p.count} })
	return l, nil
}

// GroupedLast implements [Store].
func (s *UsageBucketStore) GroupedLast(ctx context.Context, q Query, partition, columns []string) ([]GroupedAggregate, error) {
	p, ok := s.plan(q, partition, columns, true)
	if !ok {
		return s.Store.GroupedLast(ctx, q, partition, columns)
	}
	// The events report the columns of the latest event; reading the
	// partition keys along tells the groups apart.
	keys := unionKeys(partition, columns)
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		rows, err := s.Store.GroupedLast(ctx, q, partition, keys)
		out := make([]part, 0, len(rows))
		for _, r := range rows {
			v := r.Value
			out = append(out, part{
				groups: projectGroups(r.Groups, partition), count: r.Count,
				latest: &v, columns: projectGroups(r.Groups, columns),
			})
		}
		return out, err
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.Store.GroupedLast(ctx, q, partition, columns)
	}
	var out []GroupedAggregate
	m.each(func(p *part) {
		v := money.Zero
		if p.latest != nil {
			v = *p.latest
		}
		cols := p.columns
		if cols == nil {
			cols = projectGroups(nil, columns)
		}
		out = append(out, GroupedAggregate{Groups: cols, Value: v, Count: p.count})
	})
	return out, nil
}

// SumPreciseTotalAmountCents implements [Store].
func (s *UsageBucketStore) SumPreciseTotalAmountCents(ctx context.Context, q Query) (money.Decimal, error) {
	p, ok := s.plan(q, nil, nil, false)
	if !ok {
		return s.Store.SumPreciseTotalAmountCents(ctx, q)
	}
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		a, err := s.Store.SumPreciseTotalAmountCents(ctx, q)
		// The amount is kept whatever the number of events.
		return []part{{groups: Groups{}, count: 1, amount: a}}, err
	})
	if err != nil {
		return money.Zero, err
	}
	if !ok {
		return s.Store.SumPreciseTotalAmountCents(ctx, q)
	}
	total := money.Zero
	m.each(func(p *part) { total = p.amount })
	return total, nil
}

// GroupedSumPreciseTotalAmountCents implements [Store].
func (s *UsageBucketStore) GroupedSumPreciseTotalAmountCents(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	p, ok := s.plan(q, by, nil, false)
	if !ok {
		return s.Store.GroupedSumPreciseTotalAmountCents(ctx, q, by)
	}
	m, ok, err := s.run(ctx, p, func(q Query) ([]part, error) {
		rows, err := s.Store.GroupedSumPreciseTotalAmountCents(ctx, q, by)
		return groupedParts(rows, func(p *part, r GroupedAggregate) { p.amount = r.Value }), err
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.Store.GroupedSumPreciseTotalAmountCents(ctx, q, by)
	}
	var out []GroupedAggregate
	m.each(func(p *part) { out = append(out, GroupedAggregate{Groups: p.groups, Value: p.amount, Count: p.count}) })
	return out, nil
}
