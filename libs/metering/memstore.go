package metering

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// MemoryStore is the reference [Store] over a slice of events. It is safe
// for concurrent use as it never changes after construction.
type MemoryStore struct {
	events []stored
}

type stored struct {
	Event
	seq int // ingestion rank
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns a store over the events. Events are ingested in
// order of IngestedAt, then of the slice; an event re-sent with the same
// transaction ID and timestamp replaces the earlier copy.
func NewMemoryStore(events []Event) *MemoryStore {
	all := make([]stored, len(events))
	for i, e := range events {
		all[i] = stored{Event: e, seq: i}
	}
	slices.SortStableFunc(all, func(a, b stored) int { return a.IngestedAt.Compare(b.IngestedAt) })
	type key struct {
		org, sub, code, tx string
		ts                 int64
	}
	latest := map[key]int{}
	for i := range all {
		all[i].seq = i
		latest[key{all[i].OrganizationID, all[i].SubscriptionID, all[i].Code, all[i].TransactionID, all[i].Timestamp.UnixNano()}] = i
	}
	s := &MemoryStore{}
	for i, e := range all {
		if latest[key{e.OrganizationID, e.SubscriptionID, e.Code, e.TransactionID, e.Timestamp.UnixNano()}] == i {
			s.events = append(s.events, e)
		}
	}
	return s
}

func (q Query) from() time.Time { return q.From.Truncate(time.Millisecond) }

// selectEvents returns the events of the query ordered by timestamp, then
// ingestion.
func (s *MemoryStore) selectEvents(q Query) []stored {
	until := q.To
	if !q.Until.IsZero() {
		until = q.Until
	}
	boundary := -1
	if !q.Until.IsZero() && q.UntilTransactionID != "" {
		for _, e := range s.events {
			if e.TransactionID == q.UntilTransactionID && e.Timestamp.Equal(until) && s.scoped(q, e) {
				boundary = e.seq
			}
		}
	}
	var out []stored
	for _, e := range s.events {
		if !s.scoped(q, e) {
			continue
		}
		if !q.IncludeHistory && e.Timestamp.Before(q.from()) {
			continue
		}
		if e.Timestamp.After(until) || (boundary >= 0 && e.Timestamp.Equal(until) && e.seq > boundary) {
			continue
		}
		if q.Numeric {
			if _, ok := numeric(e.Properties, q.Property); !ok {
				continue
			}
		}
		if !q.GroupValues.matches(e.Properties) || !q.Filter.Matches(e.Properties) {
			continue
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b stored) int {
		return cmp.Or(a.Timestamp.Compare(b.Timestamp), cmp.Compare(a.seq, b.seq))
	})
	return out
}

func (s *MemoryStore) scoped(q Query, e stored) bool {
	return (q.OrganizationID == "" || e.OrganizationID == q.OrganizationID) &&
		(q.SubscriptionID == "" || e.SubscriptionID == q.SubscriptionID) &&
		(q.Code == "" || e.Code == q.Code)
}

func value(q Query, e Event) money.Decimal {
	v, _ := numeric(e.Properties, q.Property)
	return v
}

type group struct {
	groups Groups
	events []stored
}

// groupEvents partitions ordered events by the values of the properties, in
// order of first appearance.
func groupEvents(events []stored, by []string) []group {
	var out []group
	index := map[string]int{}
	for _, e := range events {
		g := GroupsOf(e.Properties, by)
		k := g.Key()
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, group{groups: g})
		}
		out[i].events = append(out[i].events, e)
	}
	return out
}

func checkContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("metering: memory store: %w", err)
	}
	return nil
}

// Count implements [Store].
func (s *MemoryStore) Count(ctx context.Context, q Query) (int64, error) {
	if err := checkContext(ctx); err != nil {
		return 0, err
	}
	return int64(len(s.selectEvents(q))), nil
}

// GroupedCount implements [Store].
func (s *MemoryStore) GroupedCount(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []GroupedAggregate
	for _, g := range groupEvents(s.selectEvents(q), by) {
		n := int64(len(g.events))
		out = append(out, GroupedAggregate{Groups: g.groups, Value: money.NewFromInt(n), Count: n})
	}
	return out, nil
}

func sumOf(q Query, events []stored) money.Decimal {
	total := money.Zero
	for _, e := range events {
		total = total.Add(value(q, e.Event))
	}
	return total
}

// Sum implements [Store].
func (s *MemoryStore) Sum(ctx context.Context, q Query) (Total, error) {
	if err := checkContext(ctx); err != nil {
		return Total{}, err
	}
	events := s.selectEvents(q)
	return Total{Value: sumOf(q, events), Count: int64(len(events))}, nil
}

// GroupedSum implements [Store].
func (s *MemoryStore) GroupedSum(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []GroupedAggregate
	for _, g := range groupEvents(s.selectEvents(q), by) {
		out = append(out, GroupedAggregate{Groups: g.groups, Value: sumOf(q, g.events), Count: int64(len(g.events))})
	}
	return out, nil
}

func maxOf(q Query, events []stored) money.Decimal {
	if len(events) == 0 {
		return money.Zero
	}
	m := value(q, events[0].Event)
	for _, e := range events[1:] {
		m = money.Max(m, value(q, e.Event))
	}
	return m
}

// Max implements [Store].
func (s *MemoryStore) Max(ctx context.Context, q Query) (Total, error) {
	if err := checkContext(ctx); err != nil {
		return Total{}, err
	}
	events := s.selectEvents(q)
	return Total{Value: maxOf(q, events), Count: int64(len(events))}, nil
}

// GroupedMax implements [Store].
func (s *MemoryStore) GroupedMax(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []GroupedAggregate
	for _, g := range groupEvents(s.selectEvents(q), by) {
		out = append(out, GroupedAggregate{Groups: g.groups, Value: maxOf(q, g.events), Count: int64(len(g.events))})
	}
	return out, nil
}

// Last implements [Store].
func (s *MemoryStore) Last(ctx context.Context, q Query) (LastValue, error) {
	if err := checkContext(ctx); err != nil {
		return LastValue{}, err
	}
	events := s.selectEvents(q)
	if len(events) == 0 {
		return LastValue{}, nil
	}
	last := events[len(events)-1]
	r := LastValue{Count: int64(len(events))}
	if v, ok := numeric(last.Properties, q.Property); ok {
		r.Value = &v
	}
	return r, nil
}

// GroupedLast implements [Store].
func (s *MemoryStore) GroupedLast(ctx context.Context, q Query, partition, columns []string) ([]GroupedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []GroupedAggregate
	for _, g := range groupEvents(s.selectEvents(q), partition) {
		last := g.events[len(g.events)-1]
		out = append(out, GroupedAggregate{
			Groups: GroupsOf(last.Properties, columns),
			Value:  value(q, last.Event),
			Count:  int64(len(g.events)),
		})
	}
	return out, nil
}

// LastEvent implements [Store].
func (s *MemoryStore) LastEvent(ctx context.Context, q Query) (*Event, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	events := s.selectEvents(q)
	if len(events) == 0 {
		return nil, nil //nolint:nilnil // no event is not an error
	}
	e := events[len(events)-1].Event
	return &e, nil
}

// GroupedLastEvent implements [Store].
func (s *MemoryStore) GroupedLastEvent(ctx context.Context, q Query, by []string) ([]GroupedEvent, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []GroupedEvent
	for _, g := range groupEvents(s.selectEvents(q), by) {
		last := g.events[len(g.events)-1]
		out = append(out, GroupedEvent{Groups: g.groups, Timestamp: last.Timestamp, Value: value(q, last.Event)})
	}
	return out, nil
}

func amountOf(events []stored) money.Decimal {
	total := money.Zero
	for _, e := range events {
		if e.PreciseTotalAmountCents != nil {
			total = total.Add(*e.PreciseTotalAmountCents)
		}
	}
	return total
}

// SumPreciseTotalAmountCents implements [Store].
func (s *MemoryStore) SumPreciseTotalAmountCents(ctx context.Context, q Query) (money.Decimal, error) {
	if err := checkContext(ctx); err != nil {
		return money.Zero, err
	}
	return amountOf(s.selectEvents(q)), nil
}

// GroupedSumPreciseTotalAmountCents implements [Store].
func (s *MemoryStore) GroupedSumPreciseTotalAmountCents(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []GroupedAggregate
	for _, g := range groupEvents(s.selectEvents(q), by) {
		out = append(out, GroupedAggregate{Groups: g.groups, Value: amountOf(g.events), Count: int64(len(g.events))})
	}
	return out, nil
}

// EventValues implements [Store].
func (s *MemoryStore) EventValues(ctx context.Context, q Query, limit int, exclude string) ([]money.Decimal, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []money.Decimal
	for _, e := range s.selectEvents(q) {
		if exclude != "" && e.TransactionID == exclude {
			continue
		}
		if limit > 0 && len(out) == limit {
			break
		}
		out = append(out, value(q, e.Event))
	}
	return out, nil
}

func periodDays(q Query) (money.Decimal, error) {
	if q.PeriodDays <= 0 {
		return money.Zero, fmt.Errorf("metering: query period of %d days", q.PeriodDays)
	}
	return money.NewFromInt(int64(q.PeriodDays)), nil
}

// remainingRatio is the share of the period from the day of t to the day of
// q.To, both included.
func remainingRatio(q Query, t time.Time, period money.Decimal) money.Decimal {
	days := DateOf(q.To, q.location()).Sub(DateOf(t, q.location())) + 1
	return money.NewFromInt(int64(days)).Div(period)
}

// ProratedEventValues implements [Store].
func (s *MemoryStore) ProratedEventValues(ctx context.Context, q Query) ([]money.Decimal, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	period, err := periodDays(q)
	if err != nil {
		return nil, err
	}
	var out []money.Decimal
	for _, e := range s.selectEvents(q) {
		out = append(out, value(q, e.Event).Mul(remainingRatio(q, e.Timestamp, period)))
	}
	return out, nil
}

func proratedSumOf(q Query, events []stored, period money.Decimal, persistedDays *int) ProratedAggregate {
	r := ProratedAggregate{Value: money.Zero, ProratedValue: money.Zero, Count: int64(len(events))}
	for _, e := range events {
		v := value(q, e.Event)
		ratio := remainingRatio(q, e.Timestamp, period)
		if persistedDays != nil {
			ratio = money.NewFromInt(int64(*persistedDays)).Div(period)
		}
		r.Value = r.Value.Add(v)
		r.ProratedValue = r.ProratedValue.Add(v.Mul(ratio))
	}
	return r
}

// ProratedSum implements [Store].
func (s *MemoryStore) ProratedSum(ctx context.Context, q Query, persistedDays *int) (ProratedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return ProratedAggregate{}, err
	}
	period, err := periodDays(q)
	if err != nil {
		return ProratedAggregate{}, err
	}
	return proratedSumOf(q, s.selectEvents(q), period, persistedDays), nil
}

// GroupedProratedSum implements [Store].
func (s *MemoryStore) GroupedProratedSum(ctx context.Context, q Query, by []string, persistedDays *int) ([]GroupedProratedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	period, err := periodDays(q)
	if err != nil {
		return nil, err
	}
	var out []GroupedProratedAggregate
	for _, g := range groupEvents(s.selectEvents(q), by) {
		r := proratedSumOf(q, g.events, period, persistedDays)
		out = append(out, GroupedProratedAggregate{Groups: g.groups, Value: r.Value, ProratedValue: r.ProratedValue, Count: r.Count})
	}
	return out, nil
}

// SumByDay implements [Store].
func (s *MemoryStore) SumByDay(ctx context.Context, q Query) ([]DailyValue, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []DailyValue
	for _, e := range s.selectEvents(q) {
		d := DateOf(e.Timestamp, q.location())
		i := slices.IndexFunc(out, func(v DailyValue) bool { return v.Date == d })
		if i < 0 {
			out = append(out, DailyValue{Date: d, Value: money.Zero})
			i = len(out) - 1
		}
		out[i].Value = out[i].Value.Add(value(q, e.Event))
	}
	slices.SortFunc(out, func(a, b DailyValue) int { return a.Date.Compare(b.Date) })
	return out, nil
}

// uniqueRow is an event as unique counts read it.
type uniqueRow struct {
	ts       time.Time
	property string
	op       Operation
}

func (r uniqueRow) same(o uniqueRow) bool {
	return r.ts.Equal(o.ts) && r.property == o.property && r.op == o.op
}

func uniqueRows(q Query, events []stored) []uniqueRow {
	rows := make([]uniqueRow, len(events))
	for i, e := range events {
		rows[i] = uniqueRow{ts: e.Timestamp, property: e.Properties[q.Property], op: e.Operation()}
	}
	return rows
}

// effective returns, for rows of one property in timestamp order, the
// change each makes to the count: +1 for an add of an absent value, -1 for
// a remove of a present one, 0 otherwise.
func effective(rows []uniqueRow) []int {
	out := make([]int, len(rows))
	prev := OperationRemove
	for i, r := range rows {
		switch r.op {
		case prev:
		case OperationAdd:
			out[i] = 1
		default:
			out[i] = -1
		}
		prev = r.op
	}
	return out
}

// byProperty partitions ordered rows by property, in order of first
// appearance.
func byProperty(rows []uniqueRow) [][]uniqueRow {
	return groupBy(rows, func(r uniqueRow) string { return r.property })
}

func uniqueCountOf(q Query, events []stored) money.Decimal {
	total := 0
	for _, rows := range byProperty(uniqueRows(q, events)) {
		for _, v := range effective(rows) {
			total += v
		}
	}
	return money.NewFromInt(int64(total))
}

// UniqueCount implements [Store].
func (s *MemoryStore) UniqueCount(ctx context.Context, q Query) (money.Decimal, error) {
	if err := checkContext(ctx); err != nil {
		return money.Zero, err
	}
	return uniqueCountOf(q, s.selectEvents(q)), nil
}

// GroupedUniqueCount implements [Store].
func (s *MemoryStore) GroupedUniqueCount(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []GroupedAggregate
	for _, g := range groupEvents(s.selectEvents(q), by) {
		v := uniqueCountOf(q, g.events)
		n, _ := v.Int64()
		out = append(out, GroupedAggregate{Groups: g.groups, Value: v, Count: n})
	}
	return out, nil
}

// intervals computes the presence intervals of the events of one group.
func intervals(q Query, events []stored, period money.Decimal) []UniqueCountInterval {
	loc := q.location()
	from := q.from()
	var out []UniqueCountInterval
	for _, rows := range byProperty(uniqueRows(q, events)) {
		// A remove followed the same day by the opposite operation is
		// ignored.
		var kept []uniqueRow
		for i, r := range rows {
			ignored := false
			if r.op != OperationAdd {
				for _, next := range rows[i+1:] {
					if next.ts.After(r.ts) && next.op != r.op && DateOf(next.ts, loc) == DateOf(r.ts, loc) {
						ignored = true
						break
					}
				}
			}
			if !ignored {
				kept = append(kept, r)
			}
		}
		// Only the effective operations remain, once each.
		var values []uniqueRow
		for i, v := range effective(kept) {
			if v != 0 && !slices.ContainsFunc(values, kept[i].same) {
				values = append(values, kept[i])
			}
		}
		for i, r := range values {
			ratio := money.Zero
			if r.op == OperationAdd {
				lead := q.To
				if i+1 < len(values) {
					lead = values[i+1].ts
				}
				end := from
				if !lead.Before(from) {
					end = lead.Add(24 * time.Hour)
				}
				start := r.ts
				if start.Before(from) {
					start = from
				}
				days := DateOf(end, loc).Sub(DateOf(start, loc))
				ratio = money.NewFromInt(int64(days)).Div(period)
			}
			out = append(out, UniqueCountInterval{Timestamp: r.ts, Property: r.property, Operation: r.op, ProratedValue: ratio})
		}
	}
	slices.SortStableFunc(out, func(a, b UniqueCountInterval) int {
		return cmp.Or(a.Timestamp.Compare(b.Timestamp), cmp.Compare(a.Property, b.Property))
	})
	return out
}

func proratedUniqueCountOf(q Query, events []stored, period money.Decimal) money.Decimal {
	total := money.Zero
	for _, iv := range intervals(q, events, period) {
		total = total.Add(iv.ProratedValue)
	}
	return total
}

func uniquePeriod(q Query) money.Decimal {
	if q.PeriodDays <= 0 {
		return money.One
	}
	return money.NewFromInt(int64(q.PeriodDays))
}

// ProratedUniqueCount implements [Store].
func (s *MemoryStore) ProratedUniqueCount(ctx context.Context, q Query) (money.Decimal, error) {
	if err := checkContext(ctx); err != nil {
		return money.Zero, err
	}
	return proratedUniqueCountOf(q, s.selectEvents(q), uniquePeriod(q)), nil
}

// GroupedProratedUniqueCount implements [Store].
func (s *MemoryStore) GroupedProratedUniqueCount(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var out []GroupedAggregate
	for _, g := range groupEvents(s.selectEvents(q), by) {
		v := proratedUniqueCountOf(q, g.events, uniquePeriod(q))
		out = append(out, GroupedAggregate{Groups: g.groups, Value: v, Count: int64(len(g.events))})
	}
	return out, nil
}

// UniqueCountIntervals implements [Store].
func (s *MemoryStore) UniqueCountIntervals(ctx context.Context, q Query, withRemove bool) ([]UniqueCountInterval, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	all := intervals(q, s.selectEvents(q), uniquePeriod(q))
	if withRemove {
		return all, nil
	}
	return slices.DeleteFunc(all, func(iv UniqueCountInterval) bool { return iv.ProratedValue.IsZero() }), nil
}

// ActiveUniqueProperty implements [Store].
func (s *MemoryStore) ActiveUniqueProperty(ctx context.Context, q Query, e Event) (bool, error) {
	if err := checkContext(ctx); err != nil {
		return false, err
	}
	v, ok := e.Properties[q.Property]
	if !ok {
		return false, nil
	}
	var previous *stored
	for _, other := range s.selectEvents(q) {
		if other.TransactionID == e.TransactionID && other.Timestamp.Equal(e.Timestamp) {
			continue
		}
		if p, ok := other.Properties[q.Property]; !ok || p != v || !other.Timestamp.Before(e.Timestamp) {
			continue
		}
		if previous == nil || !other.Timestamp.Before(previous.Timestamp) {
			previous = &other
		}
	}
	if previous == nil {
		return false, nil
	}
	op, set := previous.Properties[OperationTypeProperty]
	return !set || Operation(op) == OperationAdd, nil
}

type weightedRow struct {
	ts    time.Time
	value money.Decimal
}

func ceilSecond(t time.Time) time.Time {
	if s := t.Truncate(time.Second); !s.Equal(t) {
		return s.Add(time.Second)
	}
	return t
}

// weighted computes the time-weighted sum of rows starting at q.From with
// initial and ending at q.To.
func weighted(q Query, events []stored, initial money.Decimal, period money.Decimal) WeightedAggregate {
	to := ceilSecond(q.To)
	rows := []weightedRow{{q.from(), initial}}
	for _, e := range events {
		rows = append(rows, weightedRow{e.Timestamp, value(q, e.Event)})
	}
	rows = append(rows, weightedRow{to, money.Zero})
	slices.SortStableFunc(rows, func(a, b weightedRow) int { return a.ts.Compare(b.ts) })

	periodSeconds := period.Mul(money.NewFromInt(86400))
	r := WeightedAggregate{Value: money.Zero, Variation: money.Zero, Count: int64(max(len(rows)-2, 0))}
	cumul := money.Zero
	for i, row := range rows {
		cumul = cumul.Add(row.value)
		r.Variation = r.Variation.Add(row.value)
		next := to
		if i+1 < len(rows) {
			next = rows[i+1].ts
		}
		d := next.Sub(row.ts)
		if d == 0 {
			continue
		}
		seconds := money.New(d.Nanoseconds(), -9)
		r.Value = r.Value.Add(cumul.Mul(seconds).Div(periodSeconds))
	}
	r.Variation = r.Variation.Sub(initial)
	return r
}

// WeightedSum implements [Store].
func (s *MemoryStore) WeightedSum(ctx context.Context, q Query, initial money.Decimal) (WeightedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return WeightedAggregate{}, err
	}
	period, err := periodDays(q)
	if err != nil {
		return WeightedAggregate{}, err
	}
	return weighted(q, s.selectEvents(q), initial, period), nil
}

// GroupedWeightedSum implements [Store].
func (s *MemoryStore) GroupedWeightedSum(ctx context.Context, q Query, by []string, initial []GroupedValue) ([]GroupedWeightedAggregate, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	period, err := periodDays(q)
	if err != nil {
		return nil, err
	}
	groups := groupEvents(s.selectEvents(q), by)
	type seeded struct {
		group
		initial money.Decimal
	}
	var all []seeded
	for _, g := range groups {
		v := money.Zero
		if i := slices.IndexFunc(initial, func(iv GroupedValue) bool { return iv.Groups.Equal(g.groups) }); i >= 0 {
			v = initial[i].Value
		}
		all = append(all, seeded{g, v})
	}
	for _, iv := range initial {
		if !slices.ContainsFunc(all, func(s seeded) bool { return s.groups.Equal(iv.Groups) }) {
			all = append(all, seeded{group{groups: iv.Groups}, iv.Value})
		}
	}
	var out []GroupedWeightedAggregate
	for _, g := range all {
		r := weighted(q, g.events, g.initial, period)
		out = append(out, GroupedWeightedAggregate{Groups: g.groups, Value: r.Value, Variation: r.Variation, Count: r.Count})
	}
	return out, nil
}
