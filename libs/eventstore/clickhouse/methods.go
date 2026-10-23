package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/calc"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func wrap(method string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("eventstore: clickhouse: %s: %w", method, err)
}

// each runs the statement and calls fn after scanning each row into dest.
// Reads are sequentially consistent on replicated tables (see
// [consistent]), so a replica that lags behind does not answer billing or
// deduplication without the events and buckets a quorum acknowledged.
func (s *Store) each(ctx context.Context, sql string, a args, dest []any, fn func() error) error {
	rows, err := s.conn.Query(consistent(ctx), sql, a...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		if err := fn(); err != nil {
			return err
		}
	}
	return rows.Err()
}

// decimal is a scan target for decimals read as text.
type decimal struct{ s *string }

func newDecimal() decimal { return decimal{new(string)} }

func (d decimal) get() (money.Decimal, error) {
	if *d.s == "" {
		return money.Zero, nil
	}
	v, err := money.Parse(*d.s)
	if err != nil {
		return money.Zero, fmt.Errorf("eventstore: clickhouse: decimal %q: %w", *d.s, err)
	}
	return v, nil
}

// groupDest returns scan targets for n grouping values.
func groupDest(n int) ([]*string, []any) {
	vals := make([]*string, n)
	dest := make([]any, n)
	for i := range vals {
		dest[i] = &vals[i]
	}
	return vals, dest
}

// Count implements [metering.Store].
func (s *Store) Count(ctx context.Context, q metering.Query) (int64, error) {
	sel, err := newSelection(q)
	if err != nil {
		return 0, wrap("count", err)
	}
	var n int64
	err = s.conn.QueryRow(consistent(ctx), sel.with("SELECT toInt64(count()) FROM sel"), sel.args...).Scan(&n)
	return n, wrap("count", err)
}

// grouped runs an aggregate per group of the keys.
func (s *Store) grouped(ctx context.Context, q metering.Query, by []string, aggregate string) ([]metering.GroupedAggregate, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, err
	}
	exprs, aliases := sel.groups(by, "g")
	sql := sel.with(fmt.Sprintf("SELECT %s toString(%s), toInt64(count()) FROM sel %s", prefixList(exprs), aggregate, groupBy(aliases)))
	vals, dest := groupDest(len(by))
	var (
		value = newDecimal()
		count int64
		out   []metering.GroupedAggregate
	)
	err = s.each(ctx, sql, sel.args, append(dest, value.s, &count), func() error {
		if count == 0 {
			return nil
		}
		v, err := value.get()
		if err != nil {
			return err
		}
		out = append(out, metering.GroupedAggregate{Groups: calc.Groups(by, vals), Value: v, Count: count})
		return nil
	})
	return out, err
}

func single(r []metering.GroupedAggregate) metering.Total {
	if len(r) == 0 {
		return metering.Total{Value: money.Zero}
	}
	return metering.Total{Value: r[0].Value, Count: r[0].Count}
}

// GroupedCount implements [metering.Store].
func (s *Store) GroupedCount(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	r, err := s.grouped(ctx, q, by, "count()")
	return r, wrap("grouped count", err)
}

const (
	sumValue  = "sum(value)"
	maxValue  = "max(value)"
	sumAmount = "sum(ifNull(amount, 0))"
)

// Sum implements [metering.Store].
func (s *Store) Sum(ctx context.Context, q metering.Query) (metering.Total, error) {
	r, err := s.grouped(ctx, q, nil, sumValue)
	return single(r), wrap("sum", err)
}

// GroupedSum implements [metering.Store].
func (s *Store) GroupedSum(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	r, err := s.grouped(ctx, q, by, sumValue)
	return r, wrap("grouped sum", err)
}

// Max implements [metering.Store].
func (s *Store) Max(ctx context.Context, q metering.Query) (metering.Total, error) {
	r, err := s.grouped(ctx, q, nil, maxValue)
	return single(r), wrap("max", err)
}

// GroupedMax implements [metering.Store].
func (s *Store) GroupedMax(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	r, err := s.grouped(ctx, q, by, maxValue)
	return r, wrap("grouped max", err)
}

// SumPreciseTotalAmountCents implements [metering.Store].
func (s *Store) SumPreciseTotalAmountCents(ctx context.Context, q metering.Query) (money.Decimal, error) {
	r, err := s.grouped(ctx, q, nil, sumAmount)
	return single(r).Value, wrap("sum precise total amount cents", err)
}

// GroupedSumPreciseTotalAmountCents implements [metering.Store].
func (s *Store) GroupedSumPreciseTotalAmountCents(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	r, err := s.grouped(ctx, q, by, sumAmount)
	return r, wrap("grouped sum precise total amount cents", err)
}

// latest returns a statement selecting cols from the most recent event of
// each group of the partition aliases (all events when there are none),
// over the source.
func latest(cols, partition []string, source string) string {
	limit := "LIMIT 1"
	order := "ts DESC, seq DESC"
	if len(partition) > 0 {
		limit = "LIMIT 1 BY " + strings.Join(partition, ", ")
		order = strings.Join(partition, ", ") + ", " + order
	}
	return fmt.Sprintf("SELECT %s FROM %s ORDER BY %s %s", strings.Join(cols, ", "), source, order, limit)
}

// Last implements [metering.Store].
func (s *Store) Last(ctx context.Context, q metering.Query) (metering.LastValue, error) {
	sel, err := newSelection(q)
	if err != nil {
		return metering.LastValue{}, wrap("last", err)
	}
	var (
		value *string
		count int64
		r     metering.LastValue
	)
	err = s.each(ctx, sel.with(latest([]string{"if(isnum, toString(value), NULL)", "toInt64(count() OVER ())"}, nil, "sel")),
		sel.args, []any{&value, &count}, func() error {
			r.Count = count
			if value != nil {
				v, err := money.Parse(*value)
				if err != nil {
					return err
				}
				r.Value = &v
			}
			return nil
		})
	return r, wrap("last", err)
}

// GroupedLast implements [metering.Store].
func (s *Store) GroupedLast(ctx context.Context, q metering.Query, partition, columns []string) ([]metering.GroupedAggregate, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, wrap("grouped last", err)
	}
	pExprs, pAliases := sel.groups(partition, "p")
	cExprs, cAliases := sel.groups(columns, "c")
	source := "sel"
	if extra := append(append([]string{}, pExprs...), cExprs...); len(extra) > 0 {
		source = fmt.Sprintf("(SELECT *, %s FROM sel)", strings.Join(extra, ", "))
	}
	count := fmt.Sprintf("toInt64(count() OVER (%s))", partitionBy(pAliases...))
	sql := sel.with(latest(append(append([]string{}, cAliases...), "toString(value)", count), pAliases, source))
	vals, dest := groupDest(len(columns))
	var (
		value = newDecimal()
		n     int64
		out   []metering.GroupedAggregate
	)
	err = s.each(ctx, sql, sel.args, append(dest, value.s, &n), func() error {
		v, err := value.get()
		if err != nil {
			return err
		}
		out = append(out, metering.GroupedAggregate{Groups: calc.Groups(columns, vals), Value: v, Count: n})
		return nil
	})
	return out, wrap("grouped last", err)
}

// LastEvent implements [metering.Store].
func (s *Store) LastEvent(ctx context.Context, q metering.Query) (*metering.Event, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, wrap("last event", err)
	}
	var (
		e      metering.Event
		props  map[string]string
		amount *string
		found  bool
	)
	err = s.each(ctx,
		sel.with(latest([]string{"organization_id", "subscription_id", "code", "tx", "ts", "props", "if(isNull(amount), NULL, toString(amount))", "ingested_at"}, nil, "sel")),
		sel.args,
		[]any{&e.OrganizationID, &e.SubscriptionID, &e.Code, &e.TransactionID, &e.Timestamp, &props, &amount, &e.IngestedAt},
		func() error {
			found = true
			e.Properties = metering.Properties(props)
			if amount != nil {
				a, err := money.Parse(*amount)
				if err != nil {
					return err
				}
				e.PreciseTotalAmountCents = &a
			}
			return nil
		})
	if err != nil || !found {
		return nil, wrap("last event", err)
	}
	e.Timestamp, e.IngestedAt = e.Timestamp.UTC(), e.IngestedAt.UTC()
	return &e, nil
}

// GroupedLastEvent implements [metering.Store].
func (s *Store) GroupedLastEvent(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedEvent, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, wrap("grouped last event", err)
	}
	exprs, aliases := sel.groups(by, "g")
	source := "sel"
	if len(exprs) > 0 {
		source = fmt.Sprintf("(SELECT *, %s FROM sel)", strings.Join(exprs, ", "))
	}
	sql := sel.with(latest(append(append([]string{}, aliases...), "ts", "toString(value)"), aliases, source))
	vals, dest := groupDest(len(by))
	var (
		ts    time.Time
		value = newDecimal()
		out   []metering.GroupedEvent
	)
	err = s.each(ctx, sql, sel.args, append(dest, &ts, value.s), func() error {
		v, err := value.get()
		if err != nil {
			return err
		}
		out = append(out, metering.GroupedEvent{Groups: calc.Groups(by, vals), Timestamp: ts.UTC(), Value: v})
		return nil
	})
	return out, wrap("grouped last event", err)
}

// EventValues implements [metering.Store].
func (s *Store) EventValues(ctx context.Context, q metering.Query, limit int, exclude string) ([]money.Decimal, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, wrap("event values", err)
	}
	where := ""
	if exclude != "" {
		where = "WHERE tx != " + sel.args.add(exclude)
	}
	lim := ""
	if limit > 0 {
		lim = "LIMIT " + sel.args.add(limit)
	}
	var (
		value = newDecimal()
		out   []money.Decimal
	)
	err = s.each(ctx, sel.with(fmt.Sprintf("SELECT toString(value) FROM sel %s ORDER BY ts, seq %s", where, lim)), sel.args, []any{value.s}, func() error {
		v, err := value.get()
		out = append(out, v)
		return err
	})
	return out, wrap("event values", err)
}

// ProratedEventValues implements [metering.Store].
func (s *Store) ProratedEventValues(ctx context.Context, q metering.Query) ([]money.Decimal, error) {
	period, err := calc.PeriodDays(q)
	if err != nil {
		return nil, err
	}
	sel, err := newSelection(q)
	if err != nil {
		return nil, wrap("prorated event values", err)
	}
	var (
		value = newDecimal()
		day   int64
		out   []money.Decimal
	)
	err = s.each(ctx, sel.with("SELECT toString(value), day FROM sel ORDER BY ts, seq"), sel.args, []any{value.s, &day}, func() error {
		v, err := value.get()
		out = append(out, v.Mul(calc.Ratio(calc.RemainingDays(q, int(day)), period)))
		return err
	})
	return out, wrap("prorated event values", err)
}

// proratedSums returns the prorated sums per group.
func (s *Store) proratedSums(ctx context.Context, q metering.Query, by []string, persistedDays *int) ([]metering.GroupedProratedAggregate, error) {
	period, err := calc.PeriodDays(q)
	if err != nil {
		return nil, err
	}
	sel, err := newSelection(q)
	if err != nil {
		return nil, err
	}
	exprs, aliases := sel.groups(by, "g")
	sql := sel.with(fmt.Sprintf("SELECT %s day, toString(sum(value)), toInt64(count()) FROM sel %s",
		prefixList(exprs), groupBy(append(append([]string{}, aliases...), "day"))))
	vals, dest := groupDest(len(by))
	var (
		day   int64
		sum   = newDecimal()
		count int64
		order []string
		days  = map[string][]calc.DaySum{}
		gs    = map[string]metering.Groups{}
	)
	err = s.each(ctx, sql, sel.args, append(dest, &day, sum.s, &count), func() error {
		v, err := sum.get()
		if err != nil {
			return err
		}
		g := calc.Groups(by, vals)
		k := g.Key()
		if _, ok := gs[k]; !ok {
			gs[k] = g
			order = append(order, k)
		}
		days[k] = append(days[k], calc.DaySum{Day: int(day), Sum: v, Count: count})
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]metering.GroupedProratedAggregate, 0, len(order))
	for _, k := range order {
		r := calc.Prorate(q, days[k], period, persistedDays)
		out = append(out, metering.GroupedProratedAggregate{Groups: gs[k], Value: r.Value, ProratedValue: r.ProratedValue, Count: r.Count})
	}
	return out, nil
}

// ProratedSum implements [metering.Store].
func (s *Store) ProratedSum(ctx context.Context, q metering.Query, persistedDays *int) (metering.ProratedAggregate, error) {
	r, err := s.proratedSums(ctx, q, nil, persistedDays)
	if err != nil {
		return metering.ProratedAggregate{}, wrap("prorated sum", err)
	}
	if len(r) == 0 {
		return metering.ProratedAggregate{Value: money.Zero, ProratedValue: money.Zero}, nil
	}
	return metering.ProratedAggregate{Value: r[0].Value, ProratedValue: r[0].ProratedValue, Count: r[0].Count}, nil
}

// GroupedProratedSum implements [metering.Store].
func (s *Store) GroupedProratedSum(ctx context.Context, q metering.Query, by []string, persistedDays *int) ([]metering.GroupedProratedAggregate, error) {
	r, err := s.proratedSums(ctx, q, by, persistedDays)
	return r, wrap("grouped prorated sum", err)
}

// SumByDay implements [metering.Store].
func (s *Store) SumByDay(ctx context.Context, q metering.Query) ([]metering.DailyValue, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, wrap("sum by day", err)
	}
	var (
		day   int64
		value = newDecimal()
		out   []metering.DailyValue
	)
	err = s.each(ctx, sel.with("SELECT day, toString(sum(value)) FROM sel GROUP BY day ORDER BY day"), sel.args, []any{&day, value.s}, func() error {
		v, err := value.get()
		out = append(out, metering.DailyValue{Date: calc.DateOfDay(int(day)), Value: v})
		return err
	})
	return out, wrap("sum by day", err)
}

const (
	pastRows  = "ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW"
	nextRow   = "ROWS BETWEEN CURRENT ROW AND 1 FOLLOWING"
	ordered   = "ORDER BY ts, seq"
	removedOp = "'remove'"
	addOp     = "'add'"
)

// uniqueCounts returns the unique counts per group.
func (s *Store) uniqueCounts(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, err
	}
	exprs, aliases := sel.groups(by, "g")
	partition := partitionBy(append(append([]string{}, aliases...), "prop")...)
	sql := sel.with(fmt.Sprintf(`SELECT %[1]s toInt64(sum(multiIf(op = prev, 0, op = %[5]s, 1, -1))), toInt64(count())
FROM (
	SELECT *, lagInFrame(op, 1, %[6]s) OVER (%[2]s %[7]s %[8]s) AS prev
	FROM (SELECT %[3]s prop, op, ts, seq FROM sel)
) %[4]s`, prefixList(aliases), partition, prefixList(exprs), groupBy(aliases), addOp, removedOp, ordered, pastRows))
	vals, dest := groupDest(len(by))
	var (
		value, count int64
		out          []metering.GroupedAggregate
	)
	err = s.each(ctx, sql, sel.args, append(dest, &value, &count), func() error {
		if count > 0 {
			out = append(out, metering.GroupedAggregate{Groups: calc.Groups(by, vals), Value: money.NewFromInt(value), Count: value})
		}
		return nil
	})
	return out, err
}

// UniqueCount implements [metering.Store].
func (s *Store) UniqueCount(ctx context.Context, q metering.Query) (money.Decimal, error) {
	r, err := s.uniqueCounts(ctx, q, nil)
	if err != nil || len(r) == 0 {
		return money.Zero, wrap("unique count", err)
	}
	return r[0].Value, nil
}

// GroupedUniqueCount implements [metering.Store].
func (s *Store) GroupedUniqueCount(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	r, err := s.uniqueCounts(ctx, q, by)
	return r, wrap("grouped unique count", err)
}

// intervals returns the CTEs of the presence intervals of the unique-count
// values per group of the aliases, ending with "iv" whose columns are the
// group aliases, ts, seq, prop, op and days (the days of presence of an
// add).
func (s *selection) intervals(exprs, aliases []string) []string {
	byProp := append(append([]string{}, aliases...), "prop")
	partition := partitionBy(byProp...)
	from := s.args.time(calc.From(s.q))
	to := s.args.time(s.q.To.UTC())
	end := fmt.Sprintf("if(next_ts < %[1]s, %[2]s, %[3]s)", from, s.dayOf(from), s.dayOf("next_ts + INTERVAL 24 HOUR"))
	start := s.dayOf(fmt.Sprintf("greatest(ts, %s)", from))
	return []string{
		fmt.Sprintf(`rows AS (SELECT %s prop, op, ts, seq, day FROM sel)`, prefixList(exprs)),
		// A remove followed the same day by the opposite operation is
		// ignored.
		fmt.Sprintf(`kept AS (
	SELECT * EXCEPT (same_day) FROM (
		SELECT *, groupArray((ts, op)) OVER (%s) AS same_day FROM rows
	)
	WHERE op = %s OR NOT arrayExists(x -> x.1 > ts AND x.2 != op, same_day)
)`, partitionBy(append(byProp, "day")...), addOp),
		// Only the effective operations remain, once each.
		fmt.Sprintf(`eff AS (
	SELECT *, lagInFrame(op, 1, %s) OVER (%s %s %s) AS prev FROM kept
)`, removedOp, partition, ordered, pastRows),
		fmt.Sprintf(`vals AS (
	SELECT *, row_number() OVER (%s ORDER BY seq) AS rn FROM eff WHERE op != prev
)`, partitionBy(append(byProp, "ts", "op")...)),
		fmt.Sprintf(`nexts AS (
	SELECT *, leadInFrame(ts, 1, %s) OVER (%s %s %s) AS next_ts FROM vals WHERE rn = 1
)`, to, partition, ordered, nextRow),
		fmt.Sprintf(`iv AS (
	SELECT %s ts, seq, prop, op, if(op = %s, %s - %s, 0) AS days FROM nexts
)`, prefixList(aliases), addOp, end, start),
	}
}

// UniqueCountIntervals implements [metering.Store].
func (s *Store) UniqueCountIntervals(ctx context.Context, q metering.Query, withRemove bool) ([]metering.UniqueCountInterval, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, wrap("unique count intervals", err)
	}
	period := calc.UniquePeriod(q)
	sql := sel.with(`SELECT ts, prop, op, toInt64(days) FROM iv ORDER BY ts, prop, seq`, sel.intervals(nil, nil)...)
	var (
		iv  calc.Interval
		op  string
		out []metering.UniqueCountInterval
	)
	err = s.each(ctx, sql, sel.args, []any{&iv.Timestamp, &iv.Property, &op, &iv.Days}, func() error {
		iv.Operation = metering.Operation(op)
		r := calc.UniqueInterval(iv, period)
		if withRemove || !r.ProratedValue.IsZero() {
			out = append(out, r)
		}
		return nil
	})
	return out, wrap("unique count intervals", err)
}

// proratedUniqueCounts returns the prorated unique counts per group.
func (s *Store) proratedUniqueCounts(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, err
	}
	period := calc.UniquePeriod(q)
	exprs, aliases := sel.groups(by, "g")
	ctes := sel.intervals(exprs, aliases)
	// The events of each group (kind 0), then the number of intervals per
	// days of presence (kind 1).
	sql := sel.with(fmt.Sprintf(`SELECT %[1]s toInt64(0) AS kind, toInt64(0) AS d, toInt64(count()) FROM rows %[2]s
UNION ALL
SELECT %[1]s toInt64(1) AS kind, toInt64(days) AS d, toInt64(count()) FROM iv WHERE op = %[4]s %[3]s`,
		prefixList(aliases), groupBy(aliases), groupBy(append(append([]string{}, aliases...), "d")), addOp), ctes...)
	vals, dest := groupDest(len(by))
	var (
		kind, days, n int64
		order         []string
		result        = map[string]*metering.GroupedAggregate{}
	)
	err = s.each(ctx, sql, sel.args, append(dest, &kind, &days, &n), func() error {
		g := calc.Groups(by, vals)
		k := g.Key()
		r, ok := result[k]
		if !ok {
			r = &metering.GroupedAggregate{Groups: g, Value: money.Zero}
			result[k] = r
			order = append(order, k)
		}
		if kind == 0 {
			r.Count = n
		} else {
			r.Value = r.Value.Add(money.NewFromInt(n).Mul(calc.Ratio(days, period)))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []metering.GroupedAggregate
	for _, k := range order {
		if result[k].Count > 0 {
			out = append(out, *result[k])
		}
	}
	return out, nil
}

// ProratedUniqueCount implements [metering.Store].
func (s *Store) ProratedUniqueCount(ctx context.Context, q metering.Query) (money.Decimal, error) {
	r, err := s.proratedUniqueCounts(ctx, q, nil)
	if err != nil || len(r) == 0 {
		return money.Zero, wrap("prorated unique count", err)
	}
	return r[0].Value, nil
}

// GroupedProratedUniqueCount implements [metering.Store].
func (s *Store) GroupedProratedUniqueCount(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	r, err := s.proratedUniqueCounts(ctx, q, by)
	return r, wrap("grouped prorated unique count", err)
}

// ActiveUniqueProperty implements [metering.Store].
func (s *Store) ActiveUniqueProperty(ctx context.Context, q metering.Query, e metering.Event) (bool, error) {
	v, ok := e.Properties[q.Property]
	if !ok {
		return false, nil
	}
	sel, err := newSelection(q)
	if err != nil {
		return false, wrap("active unique property", err)
	}
	ts := sel.args.time(e.Timestamp.UTC())
	p := sel.args.add(q.Property)
	sql := sel.with(fmt.Sprintf(`SELECT op FROM sel
WHERE mapContains(props, %[1]s) AND props[%[1]s] = %[2]s AND ts < %[3]s AND NOT (tx = %[4]s AND ts = %[3]s)
ORDER BY ts DESC, seq DESC LIMIT 1`, p, sel.args.add(v), ts, sel.args.add(e.TransactionID)))
	var (
		op    string
		found bool
	)
	err = s.each(ctx, sql, sel.args, []any{&op}, func() error {
		found = true
		return nil
	})
	if err != nil {
		return false, wrap("active unique property", err)
	}
	return found && op == string(metering.OperationAdd), nil
}

// weightedSums returns the stored part of the time-weighted sums per
// group.
func (s *Store) weightedSums(ctx context.Context, q metering.Query, by []string) ([]metering.Groups, []calc.Weighted, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, nil, err
	}
	exprs, aliases := sel.groups(by, "g")
	window := partitionBy(aliases...) + " " + ordered
	to := sel.args.time(calc.WeightedTo(q))
	sql := sel.with(fmt.Sprintf(`SELECT %[1]s toString(sum(cumul * (toUnixTimestamp64Micro(next_ts) - toUnixTimestamp64Micro(ts)))), toString(sum(value)), toInt64(count())
FROM (
	SELECT %[1]s ts, value,
		sum(value) OVER (%[3]s %[6]s) AS cumul,
		leadInFrame(ts, 1, %[4]s) OVER (%[3]s %[7]s) AS next_ts
	FROM (SELECT %[2]s ts, seq, value FROM sel)
) %[5]s`, prefixList(aliases), prefixList(exprs), window, to, groupBy(aliases), pastRows, nextRow))
	vals, dest := groupDest(len(by))
	var (
		micros, variation = newDecimal(), newDecimal()
		count             int64
		groups            []metering.Groups
		sums              []calc.Weighted
	)
	err = s.each(ctx, sql, sel.args, append(dest, micros.s, variation.s, &count), func() error {
		if count == 0 {
			return nil
		}
		m, err := micros.get()
		if err != nil {
			return err
		}
		v, err := variation.get()
		if err != nil {
			return err
		}
		groups = append(groups, calc.Groups(by, vals))
		sums = append(sums, calc.Weighted{Micros: m, Variation: v, Count: count})
		return nil
	})
	return groups, sums, err
}

// WeightedSum implements [metering.Store].
func (s *Store) WeightedSum(ctx context.Context, q metering.Query, initial money.Decimal) (metering.WeightedAggregate, error) {
	period, err := calc.PeriodDays(q)
	if err != nil {
		return metering.WeightedAggregate{}, err
	}
	_, sums, err := s.weightedSums(ctx, q, nil)
	if err != nil {
		return metering.WeightedAggregate{}, wrap("weighted sum", err)
	}
	w := calc.Weighted{Micros: money.Zero, Variation: money.Zero}
	if len(sums) > 0 {
		w = sums[0]
	}
	return calc.Weight(q, w, initial, period), nil
}

// GroupedWeightedSum implements [metering.Store].
func (s *Store) GroupedWeightedSum(ctx context.Context, q metering.Query, by []string, initial []metering.GroupedValue) ([]metering.GroupedWeightedAggregate, error) {
	period, err := calc.PeriodDays(q)
	if err != nil {
		return nil, err
	}
	groups, sums, err := s.weightedSums(ctx, q, by)
	if err != nil {
		return nil, wrap("grouped weighted sum", err)
	}
	return calc.WeightGroups(q, groups, sums, initial, period), nil
}
