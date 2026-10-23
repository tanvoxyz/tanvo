package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/calc"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func wrap(method string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("eventstore: postgres: %s: %w", method, err)
}

// each runs the statement and calls fn after scanning each row into dest.
func (s *Store) each(ctx context.Context, sql string, a args, dest []any, fn func() error) error {
	rows, err := s.db.Query(ctx, sql, a...)
	if err != nil {
		return err
	}
	_, err = pgx.ForEachRow(rows, dest, fn)
	return err
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
	err = s.db.QueryRow(ctx, sel.with("SELECT count(*) FROM sel"), sel.args...).Scan(&n)
	return n, wrap("count", err)
}

// grouped runs an aggregate per group of the keys.
func (s *Store) grouped(ctx context.Context, q metering.Query, by []string, aggregate string) ([]metering.GroupedAggregate, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, err
	}
	exprs, _ := sel.groups(by, "g")
	cols := append(exprs, aggregate+"::text", "count(*)")
	sql := sel.with(fmt.Sprintf("SELECT %s FROM sel %s", strings.Join(cols, ", "), groupBy(len(by))))
	vals, dest := groupDest(len(by))
	var (
		value money.Decimal
		count int64
		out   []metering.GroupedAggregate
	)
	err = s.each(ctx, sql, sel.args, append(dest, &value, &count), func() error {
		if count > 0 {
			out = append(out, metering.GroupedAggregate{Groups: calc.Groups(by, vals), Value: value, Count: count})
		}
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
	r, err := s.grouped(ctx, q, by, "count(*)")
	return r, wrap("grouped count", err)
}

const sumValue = "coalesce(sum(value), 0)"

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

const maxValue = "coalesce(max(value), 0)"

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

const sumAmount = "coalesce(sum(amount), 0)"

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
	if len(partition) == 0 {
		return fmt.Sprintf("SELECT %s FROM %s ORDER BY ts DESC, seq DESC LIMIT 1", strings.Join(cols, ", "), source)
	}
	p := strings.Join(partition, ", ")
	return fmt.Sprintf("SELECT DISTINCT ON (%[2]s) %[1]s FROM %[3]s ORDER BY %[2]s, ts DESC, seq DESC", strings.Join(cols, ", "), p, source)
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
	)
	err = sel.err(s.db.QueryRow(ctx,
		sel.with(`SELECT (SELECT CASE WHEN isnum THEN value::text END FROM sel ORDER BY ts DESC, seq DESC LIMIT 1), (SELECT count(*) FROM sel)`),
		sel.args...).Scan(&value, &count))
	if err != nil {
		return metering.LastValue{}, wrap("last", err)
	}
	r := metering.LastValue{Count: count}
	if value != nil {
		v, err := money.Parse(*value)
		if err != nil {
			return metering.LastValue{}, wrap("last", err)
		}
		r.Value = &v
	}
	return r, nil
}

// err maps no row to nil.
func (s *selection) err(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
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
	if len(pExprs)+len(cExprs) > 0 {
		source = fmt.Sprintf("(SELECT sel.*, %s FROM sel) s", joinCols(pExprs, cExprs))
	}
	count := "count(*) OVER ()"
	if len(pAliases) > 0 {
		count = "count(*) OVER (PARTITION BY " + strings.Join(pAliases, ", ") + ")"
	}
	sql := sel.with(latest(append(append(cAliases, "value::text"), count), pAliases, source))
	vals, dest := groupDest(len(columns))
	var (
		value money.Decimal
		n     int64
		out   []metering.GroupedAggregate
	)
	err = s.each(ctx, sql, sel.args, append(dest, &value, &n), func() error {
		out = append(out, metering.GroupedAggregate{Groups: calc.Groups(columns, vals), Value: value, Count: n})
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
		amount *string
		found  bool
	)
	err = s.each(ctx,
		sel.with(latest([]string{"organization_id", "subscription_id", "code", "tx", "ts", "props", "amount::text", "ingested_at"}, nil, "sel")),
		sel.args,
		[]any{&e.OrganizationID, &e.SubscriptionID, &e.Code, &e.TransactionID, &e.Timestamp, &e.Properties, &amount, &e.IngestedAt},
		func() error {
			found = true
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
		source = fmt.Sprintf("(SELECT sel.*, %s FROM sel) s", joinCols(exprs))
	}
	sql := sel.with(latest(append(append([]string{}, aliases...), "ts", "value::text"), aliases, source))
	vals, dest := groupDest(len(by))
	var (
		ts    time.Time
		value money.Decimal
		out   []metering.GroupedEvent
	)
	err = s.each(ctx, sql, sel.args, append(dest, &ts, &value), func() error {
		out = append(out, metering.GroupedEvent{Groups: calc.Groups(by, vals), Timestamp: ts.UTC(), Value: value})
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
		where = "WHERE tx <> " + sel.args.add(exclude)
	}
	lim := ""
	if limit > 0 {
		lim = "LIMIT " + sel.args.add(limit)
	}
	var (
		value money.Decimal
		out   []money.Decimal
	)
	err = s.each(ctx, sel.with(fmt.Sprintf("SELECT value::text FROM sel %s ORDER BY ts, seq %s", where, lim)), sel.args, []any{&value}, func() error {
		out = append(out, value)
		return nil
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
		value money.Decimal
		day   int
		out   []money.Decimal
	)
	err = s.each(ctx, sel.with("SELECT value::text, day FROM sel ORDER BY ts, seq"), sel.args, []any{&value, &day}, func() error {
		out = append(out, value.Mul(calc.Ratio(calc.RemainingDays(q, day), period)))
		return nil
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
	exprs, _ := sel.groups(by, "g")
	cols := append(exprs, "day", "sum(value)::text", "count(*)")
	sql := sel.with(fmt.Sprintf("SELECT %s FROM sel GROUP BY %s", strings.Join(cols, ", "), positions(len(by)+1)))
	vals, dest := groupDest(len(by))
	var (
		d     calc.DaySum
		order []string
		days  = map[string][]calc.DaySum{}
		gs    = map[string]metering.Groups{}
	)
	err = s.each(ctx, sql, sel.args, append(dest, &d.Day, &d.Sum, &d.Count), func() error {
		g := calc.Groups(by, vals)
		k := g.Key()
		if _, ok := gs[k]; !ok {
			gs[k] = g
			order = append(order, k)
		}
		days[k] = append(days[k], d)
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
		day   int
		value money.Decimal
		out   []metering.DailyValue
	)
	err = s.each(ctx, sel.with("SELECT day, sum(value)::text FROM sel GROUP BY day ORDER BY day"), sel.args, []any{&day, &value}, func() error {
		out = append(out, metering.DailyValue{Date: calc.DateOfDay(day), Value: value})
		return nil
	})
	return out, wrap("sum by day", err)
}

// uniqueCounts returns the unique counts per group.
func (s *Store) uniqueCounts(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, err
	}
	exprs, aliases := sel.groups(by, "g")
	partition := strings.Join(append(append([]string{}, aliases...), "prop"), ", ")
	sql := sel.with(fmt.Sprintf(`SELECT %[1]s coalesce(sum(CASE WHEN op = prev THEN 0 WHEN op = 'add' THEN 1 ELSE -1 END), 0)::text, count(*)
FROM (
	SELECT *, lag(op, 1, 'remove') OVER (PARTITION BY %[2]s ORDER BY ts, seq) AS prev
	FROM (SELECT %[3]s prop, op, ts, seq FROM sel) r
) u %[4]s`, prefixList(aliases), partition, prefixList(exprs), groupBy(len(by))))
	vals, dest := groupDest(len(by))
	var (
		value money.Decimal
		count int64
		out   []metering.GroupedAggregate
	)
	err = s.each(ctx, sql, sel.args, append(dest, &value, &count), func() error {
		if count > 0 {
			n, _ := value.Int64()
			out = append(out, metering.GroupedAggregate{Groups: calc.Groups(by, vals), Value: value, Count: n})
		}
		return nil
	})
	return out, err
}

// prefixList returns the items as the head of a select list.
func prefixList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return strings.Join(items, ", ") + ","
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
	partition := strings.Join(append(append([]string{}, aliases...), "prop"), ", ")
	from := s.args.add(calc.From(s.q)) + "::timestamptz"
	to := s.args.add(s.q.To.UTC()) + "::timestamptz"
	end := fmt.Sprintf("CASE WHEN coalesce(next_ts, %[1]s) < %[2]s THEN %[3]s ELSE %[4]s END",
		to, from, s.dayOf(from), s.dayOf(fmt.Sprintf("(coalesce(next_ts, %s) + interval '24 hours')", to)))
	start := s.dayOf(fmt.Sprintf("greatest(ts, %s)", from))
	return []string{
		fmt.Sprintf(`rows AS (SELECT %s prop, op, ts, seq, day FROM sel)`, prefixList(exprs)),
		// A remove followed the same day by the opposite operation is
		// ignored.
		fmt.Sprintf(`kept AS (
	SELECT %s prop, op, ts, seq, day FROM (
		SELECT *, array_agg(ts) OVER w AS day_ts, array_agg(op) OVER w AS day_ops
		FROM rows WINDOW w AS (PARTITION BY %s, day)
	) r
	WHERE r.op = 'add' OR NOT EXISTS (SELECT 1 FROM unnest(r.day_ts, r.day_ops) AS n(ts, op) WHERE n.ts > r.ts AND n.op <> r.op)
)`, prefixList(aliases), partition),
		// Only the effective operations remain, once each.
		fmt.Sprintf(`eff AS (
	SELECT *, lag(op, 1, 'remove') OVER (PARTITION BY %s ORDER BY ts, seq) AS prev FROM kept
)`, partition),
		fmt.Sprintf(`vals AS (
	SELECT *, row_number() OVER (PARTITION BY %s, ts, op ORDER BY seq) AS rn FROM eff WHERE op <> prev
)`, partition),
		fmt.Sprintf(`nexts AS (
	SELECT *, lead(ts) OVER (PARTITION BY %s ORDER BY ts, seq) AS next_ts FROM vals WHERE rn = 1
)`, partition),
		fmt.Sprintf(`iv AS (
	SELECT %s ts, seq, prop, op, CASE WHEN op = 'add' THEN %s - %s ELSE 0 END AS days FROM nexts
)`, prefixList(aliases), end, start),
	}
}

// UniqueCountIntervals implements [metering.Store].
func (s *Store) UniqueCountIntervals(ctx context.Context, q metering.Query, withRemove bool) ([]metering.UniqueCountInterval, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, wrap("unique count intervals", err)
	}
	period := calc.UniquePeriod(q)
	sql := sel.with(`SELECT ts, prop, op, days FROM iv ORDER BY ts, prop COLLATE "C", seq`, sel.intervals(nil, nil)...)
	var (
		iv  calc.Interval
		out []metering.UniqueCountInterval
	)
	var op string
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
	sql := sel.with(fmt.Sprintf(`SELECT %[1]s 0 AS kind, 0::bigint AS days, count(*) FROM rows %[2]s
UNION ALL
SELECT %[1]s 1, days, count(*) FROM iv WHERE op = 'add' GROUP BY %[3]s`,
		prefixList(aliases), groupBy(len(by)), positions(len(by)+2)), ctes...)
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
	ts := sel.args.add(e.Timestamp.UTC()) + "::timestamptz"
	sql := sel.with(fmt.Sprintf(`SELECT op FROM sel
WHERE props ? %s AND props->>%s = %s AND ts < %s AND NOT (tx = %s AND ts = %s)
ORDER BY ts DESC, seq DESC LIMIT 1`,
		sel.args.text(q.Property), sel.args.text(q.Property), sel.args.text(v), ts, sel.args.text(e.TransactionID), ts))
	var op string
	err = sel.err(s.db.QueryRow(ctx, sql, sel.args...).Scan(&op))
	if err != nil {
		return false, wrap("active unique property", err)
	}
	return op == string(metering.OperationAdd), nil
}

// weightedSums returns the stored part of the time-weighted sums per
// group.
func (s *Store) weightedSums(ctx context.Context, q metering.Query, by []string) ([]metering.Groups, []calc.Weighted, error) {
	sel, err := newSelection(q)
	if err != nil {
		return nil, nil, err
	}
	exprs, aliases := sel.groups(by, "g")
	window := "ORDER BY ts, seq"
	if len(aliases) > 0 {
		window = "PARTITION BY " + strings.Join(aliases, ", ") + " " + window
	}
	to := sel.args.add(calc.WeightedTo(q)) + "::timestamptz"
	sql := sel.with(fmt.Sprintf(`SELECT %[1]s coalesce(sum(cumul * extract(epoch FROM next_ts - ts) * 1000000), 0)::text, coalesce(sum(value), 0)::text, count(*)
FROM (
	SELECT %[1]s ts, value,
		sum(value) OVER (%[3]s ROWS UNBOUNDED PRECEDING) AS cumul,
		lead(ts, 1, %[4]s) OVER (%[3]s) AS next_ts
	FROM (SELECT %[2]s ts, seq, value FROM sel) g
) w %[5]s`, prefixList(aliases), prefixList(exprs), window, to, groupBy(len(by))))
	vals, dest := groupDest(len(by))
	var (
		w      calc.Weighted
		groups []metering.Groups
		sums   []calc.Weighted
	)
	err = s.each(ctx, sql, sel.args, append(dest, &w.Micros, &w.Variation, &w.Count), func() error {
		if w.Count > 0 {
			groups = append(groups, calc.Groups(by, vals))
			sums = append(sums, w)
		}
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
