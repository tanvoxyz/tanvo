package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var _ metering.AttributedUsageReader = (*Store)(nil)

// attributedScale is the number of decimal places of attributed units and
// amounts.
const attributedScale = 12

// attributedOrders are the columns of the orders.
var attributedOrders = map[metering.AttributedOrder]string{
	metering.AttributedOrderAmount: "amount",
	metering.AttributedOrderEvents: "events",
}

// attributedFailures map the ClickHouse error codes of the query limits to
// the metering errors: TOO_MANY_ROWS, TOO_MANY_ROWS_OR_BYTES,
// TIMEOUT_EXCEEDED and MEMORY_LIMIT_EXCEEDED.
//
// source: app/services/usage_attributions/query_service.rb:29-33
var attributedFailures = map[int32]error{
	158: metering.ErrAttributedTooManyGroups,
	396: metering.ErrAttributedTooManyGroups,
	159: metering.ErrAttributedTimeout,
	241: metering.ErrAttributedMemoryLimit,
}

// AttributedUsageAvailable implements [metering.AttributedUsageReader]:
// the events of the store carry their attribution labels.
func (s *Store) AttributedUsageAvailable(string) bool { return true }

// AttributedUsage implements [metering.AttributedUsageReader]. Each event
// is priced on its own row, the charge's price lookup giving the rank of
// the filter pricing it; the rows are then grouped by the label's value
// only, into the fixed cells of the query, so the filters add no groups.
// Ranks and level totals are window functions over every value, computed
// before paging; the unattributed usage (the empty value) is ranked apart,
// and returned with the page and the first ranked row, which carries the
// level totals. The current copy of each event is read (FINAL).
//
// source: app/services/events/stores/clickhouse/attributed_usage_query.rb:57-105
func (s *Store) AttributedUsage(ctx context.Context, q metering.AttributedQuery) (metering.AttributedUsage, error) {
	if q.OrganizationID == "" {
		return metering.AttributedUsage{}, eventstore.ErrMissingOrganization
	}
	if len(q.Charges) == 0 {
		return metering.AttributedUsage{}, nil
	}
	order, ok := attributedOrders[q.Order]
	if !ok {
		return metering.AttributedUsage{}, fmt.Errorf("eventstore: clickhouse: attributed usage: order %q", q.Order)
	}
	cells := q.Cells()
	sql, a := attributedSQL(q, cells, order)

	// Sequentially consistent, as every read (see [consistent]), with the
	// query's own limits, which win over the read limits.
	settings := ch.Settings{"select_sequential_consistency": 1}
	if q.MaxGroups > 0 {
		settings["max_rows_to_group_by"] = q.MaxGroups
		settings["group_by_overflow_mode"] = "throw"
	}
	if q.MaxExecutionTime > 0 {
		settings["max_execution_time"] = int(math.Ceil(q.MaxExecutionTime.Seconds()))
		settings["timeout_overflow_mode"] = "throw"
	}
	ctx = withSettings(ctx, settings)

	var (
		node         string
		rank, groups uint64
		inPage       uint8
		values       = make([]string, 2+3*len(cells))
		totals       = make([]string, 2+3*len(cells))
		dest         = []any{&node, &rank, &inPage, &groups}
		res          metering.AttributedUsage
		ranked       bool
	)
	for i := range values {
		dest = append(dest, &values[i])
	}
	for i := range totals {
		dest = append(dest, &totals[i])
	}
	err := s.scan(ctx, sql, a, dest, func() error {
		v, err := attributedValues(values, len(cells))
		if err != nil {
			return err
		}
		if node == "" {
			res.Unattributed = v
			return nil
		}
		if !ranked {
			ranked = true
			if res.Level, err = attributedValues(totals, len(cells)); err != nil {
				return err
			}
			res.Groups = int64(groups)
		}
		res.Rows = append(res.Rows, metering.AttributedRow{Value: node, Rank: int(rank), InPage: inPage == 1, AttributedValues: v})
		return nil
	})
	if err != nil {
		if exc, ok := errors.AsType[*ch.Exception](err); ok {
			if failure, ok := attributedFailures[exc.Code]; ok {
				return metering.AttributedUsage{}, fmt.Errorf("%w: %s", failure, exc.Message)
			}
		}
		return metering.AttributedUsage{}, wrap("attributed usage", err)
	}
	if res.Unattributed.Cells == nil {
		res.Unattributed = zeroAttributed(len(cells))
	}
	if !ranked {
		res.Level = zeroAttributed(len(cells))
	}
	return res, nil
}

// zeroAttributed returns the zero usage of n cells.
func zeroAttributed(n int) metering.AttributedValues {
	return metering.AttributedValues{Cells: make([]metering.AttributedCellUsage, n)}
}

// attributedValues parses the amount, events and cell columns of a row.
func attributedValues(cols []string, n int) (metering.AttributedValues, error) {
	dec := func(s string) (money.Decimal, error) {
		v, err := money.Parse(s)
		if err != nil {
			return money.Zero, fmt.Errorf("eventstore: clickhouse: attributed usage: decimal %q: %w", s, err)
		}
		return v, nil
	}
	count := func(s string) (int64, error) {
		v, err := dec(s)
		if err != nil {
			return 0, err
		}
		i, _ := v.Int64()
		return i, nil
	}
	var (
		out = metering.AttributedValues{Cells: make([]metering.AttributedCellUsage, n)}
		err error
	)
	if out.Amount, err = dec(cols[0]); err != nil {
		return out, err
	}
	if out.Events, err = count(cols[1]); err != nil {
		return out, err
	}
	for i := range n {
		c := &out.Cells[i]
		if c.Units, err = dec(cols[2+3*i]); err != nil {
			return out, err
		}
		if c.Amount, err = dec(cols[3+3*i]); err != nil {
			return out, err
		}
		if c.Events, err = count(cols[4+3*i]); err != nil {
			return out, err
		}
	}
	return out, nil
}

// attributedSQL returns the statement of q over its cells and its
// parameters.
func attributedSQL(q metering.AttributedQuery, cells []metering.AttributedCell, order string) (string, args) {
	var a args
	dec := func(v money.Decimal) string {
		return fmt.Sprintf("toDecimal128(%s, %d)", a.add(v.String()), attributedScale)
	}
	label := func(key string) string { return "attribution_labels[" + a.add(key) + "]" }
	codes := make([]string, len(q.Charges))
	codeCond := make([]string, len(q.Charges))
	for i, c := range q.Charges {
		codes[i] = c.Code
		codeCond[i] = "code = " + a.add(c.Code)
	}

	// The events, each with its rank and unit price per charge.
	events := []string{
		label(q.GroupKey) + " AS node",
		"code",
		fmt.Sprintf("toDecimal128(coalesce(decimal_value, 0), %d) AS event_units", attributedScale),
	}
	for i, c := range q.Charges {
		if c.Lookup != nil {
			events = append(events, fmt.Sprintf("if(%s, %s, 0) AS rank_%d", codeCond[i], rankSQL(&a, c.Lookup), i))
		}
		if c.UnitAmount != nil {
			events = append(events, priceSQL(&a, c, i, dec)+fmt.Sprintf(" AS price_%d", i))
		}
	}
	where := []string{
		"organization_id = " + a.add(q.OrganizationID),
		"subscription_id = " + a.add(q.SubscriptionID),
		"code IN " + a.add(uniqueStrings(codes)),
		"timestamp >= " + a.time(q.From),
		"timestamp <= " + a.time(q.To),
	}
	for _, key := range slices.Sorted(maps.Keys(q.LabelFilters)) {
		where = append(where, label(key)+" IN "+a.add(q.LabelFilters[key]))
	}

	// The cells of each value.
	var cellCols, names, priced []string
	for i, cell := range cells {
		c := q.Charges[cell.Charge]
		cond := codeCond[cell.Charge]
		if cell.Rank > 0 {
			cond += fmt.Sprintf(" AND rank_%d = %d", cell.Charge, cell.Rank)
		}
		units := "event_units"
		if c.Count {
			units = fmt.Sprintf("toDecimal128(1, %d)", attributedScale)
		}
		amount := fmt.Sprintf("toDecimal128(0, %d)", attributedScale)
		if cell.Priced {
			amount = fmt.Sprintf("sumIf(toDecimal128(%s * price_%d, %d), %s)", units, cell.Charge, attributedScale, cond)
			priced = append(priced, fmt.Sprintf("amount_%d", i))
		}
		cellCols = append(cellCols,
			fmt.Sprintf("sumIf(%s, %s) AS units_%d", units, cond, i),
			fmt.Sprintf("%s AS amount_%d", amount, i),
			fmt.Sprintf("countIf(%s) AS events_%d", cond, i))
		names = append(names, fmt.Sprintf("units_%d", i), fmt.Sprintf("amount_%d", i), fmt.Sprintf("events_%d", i))
	}
	amount := fmt.Sprintf("toDecimal128(0, %d)", attributedScale)
	if len(priced) > 0 {
		amount = strings.Join(priced, " + ")
	}

	all := append([]string{"amount", "events"}, names...)
	levels := make([]string, len(all))
	out := make([]string, 0, 2*len(all))
	for i, n := range all {
		levels[i] = fmt.Sprintf("sum(%[1]s) OVER level AS total_%[1]s", n)
		out = append(out, "toString("+n+")")
	}
	for _, n := range all {
		out = append(out, "toString(total_"+n+")")
	}

	searchRank, inPage := "", fmt.Sprintf("node != '' AND rank BETWEEN %d AND %d", q.Offset+1, q.Offset+q.Limit)
	if q.Search != "" {
		match := "positionCaseInsensitiveUTF8(node, " + a.add(q.Search) + ") > 0"
		searchRank = "sum(" + match + ") OVER ranked AS search_rank,"
		inPage = fmt.Sprintf("node != '' AND %s AND search_rank BETWEEN %d AND %d", match, q.Offset+1, q.Offset+q.Limit)
	}

	sql := fmt.Sprintf(`SELECT node, toUInt64(rank), toUInt8(in_page), toUInt64(groups_count), %[1]s
FROM (
	SELECT *, %[2]s AS in_page
	FROM (
		SELECT *,
			row_number() OVER ranked AS rank,
			count() OVER level AS groups_count,
			%[3]s
			%[4]s
		FROM (
			SELECT node, %[5]s, %[6]s AS amount, count() AS events
			FROM (
				SELECT %[7]s
				FROM events_enriched FINAL
				WHERE %[8]s
			)
			GROUP BY node
		)
		WINDOW
			level AS (PARTITION BY node = ''),
			ranked AS (PARTITION BY node = '' ORDER BY %[9]s DESC, node ASC ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
	)
)
WHERE node = '' OR rank = 1 OR in_page
ORDER BY node = '' ASC, rank ASC`,
		strings.Join(out, ", "), inPage, searchRank, strings.Join(levels, ", "),
		strings.Join(cellCols, ", "), amount, strings.Join(events, ", "), strings.Join(where, " AND "), order)
	return sql, a
}

// rankSQL returns the rank of the filter pricing an event: the lowest rank
// its values find across the key sets, each missing with the default rank.
//
// source: app/services/events/stores/clickhouse/attributed_usage_query.rb:131-146
func rankSQL(a *args, l *metering.PriceLookup) string {
	def := fmt.Sprintf("toUInt32(%d)", l.DefaultRank())
	var lookups []string
	for _, ks := range l.KeySets {
		props := make([]string, len(ks.Keys))
		for i, k := range ks.Keys {
			props[i] = "properties[" + a.add(k) + "]"
		}
		values := props[0]
		if len(props) > 1 {
			values = "concat(" + strings.Join(props, ", char(31), ") + ")"
		}
		keys := slices.Sorted(maps.Keys(ks.Entries))
		ranks := make([]uint32, len(keys))
		for i, k := range keys {
			ranks[i] = uint32(ks.Entries[k])
		}
		lookups = append(lookups, fmt.Sprintf("transform(%s, %s, CAST(%s, 'Array(UInt32)'), %s)", values, a.add(keys), a.add(ranks), def))
	}
	switch len(lookups) {
	case 0:
		return def
	case 1:
		return lookups[0]
	default:
		return "least(" + strings.Join(lookups, ", ") + ")"
	}
}

// priceSQL returns the unit price of an event of the charge at index i:
// the price of its filter's rank, the charge's own price by default.
//
// source: app/services/events/stores/clickhouse/attributed_usage_query.rb:148-153
func priceSQL(a *args, c metering.AttributedCharge, i int, dec func(money.Decimal) string) string {
	if c.Lookup == nil {
		return dec(*c.UnitAmount)
	}
	prices := make([]string, 0, len(c.Lookup.UnitAmounts)+1)
	for _, p := range c.Lookup.UnitAmounts {
		prices = append(prices, p.String())
	}
	prices = append(prices, c.UnitAmount.String())
	return fmt.Sprintf("arrayElement(arrayMap(x -> toDecimal128(x, %d), %s), rank_%d)", attributedScale, a.add(prices), i)
}

// uniqueStrings returns the distinct values of ss in order.
func uniqueStrings(ss []string) []string {
	var out []string
	for _, s := range ss {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// scan runs the statement with the settings of ctx and calls fn after
// scanning each row into dest; unlike [Store.each], it keeps the settings.
func (s *Store) scan(ctx context.Context, sql string, a args, dest []any, fn func() error) error {
	rows, err := s.conn.Query(ctx, sql, a...)
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
