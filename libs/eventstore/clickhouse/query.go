package clickhouse

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/calc"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// numericPattern is the form of the property values taking part in numeric
// aggregations, as metering reads them.
const numericPattern = `'^-?[0-9]+([.][0-9]+)?$'`

// scale is the number of decimal places of the values read as decimals.
const scale = 26

// args collects the parameters of a statement, bound client-side by the
// driver.
type args []any

// add appends a parameter and returns its placeholder.
func (a *args) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// time appends a time parameter and returns its expression.
func (a *args) time(t time.Time) string {
	return "fromUnixTimestamp64Micro(" + a.add(t.UnixMicro()) + ", 'UTC')"
}

// selection is the selected events of a query, as the CTE "sel" with the
// columns:
//
//	organization_id, subscription_id, code, tx, ts, seq, props, amount,
//	ingested_at, value (Decimal, 0 unless the property is a number),
//	isnum, prop (the property, "" when missing), op (the unique-count
//	operation), day (the local day number)
type selection struct {
	q    metering.Query
	args args
	sql  string
}

// group returns the expression of a grouping key: NULL when missing or
// empty.
func (s *selection) group(key string) string {
	return "nullIf(props[" + s.args.add(key) + "], '')"
}

// groups returns the grouping expressions of the keys, aliased with the
// prefix.
func (s *selection) groups(keys []string, prefix string) (exprs []string, aliases []string) {
	for i, k := range keys {
		alias := fmt.Sprintf("%s%d", prefix, i)
		exprs = append(exprs, s.group(k)+" AS "+alias)
		aliases = append(aliases, alias)
	}
	return exprs, aliases
}

func newSelection(q metering.Query) (*selection, error) {
	if q.OrganizationID == "" {
		return nil, eventstore.ErrMissingOrganization
	}
	s := &selection{q: q}
	a := &s.args
	org := a.add(q.OrganizationID)
	scope := []string{"organization_id = " + org}
	if q.SubscriptionID != "" {
		scope = append(scope, "subscription_id = "+a.add(q.SubscriptionID))
	}
	if q.Code != "" {
		scope = append(scope, "code = "+a.add(q.Code))
	}
	// The deduplicated events are selected by the sorting key; property
	// filters apply to the latest copies, outside.
	bounds := slices.Clone(scope)
	if !q.IncludeHistory {
		bounds = append(bounds, "timestamp >= "+a.time(calc.From(q)))
	}
	until := a.time(calc.Until(q))
	bounds = append(bounds, "timestamp <= "+until)

	var where []string
	if !q.Until.IsZero() && q.UntilTransactionID != "" {
		// The events sharing the timestamp of the closing event are
		// selected up to it in ingestion order.
		where = append(where, fmt.Sprintf(`(timestamp < %[1]s OR sequence <= ifNull((
			SELECT maxOrNull(sequence) FROM events_enriched FINAL WHERE %[2]s AND timestamp = %[1]s AND transaction_id = %[3]s
		), 18446744073709551615))`, until, strings.Join(scope, " AND "), a.add(q.UntilTransactionID)))
	}

	value, isnum, prop := fmt.Sprintf("toDecimal256(0, %d)", scale), "false", "''"
	if q.Property != "" {
		p := "properties[" + a.add(q.Property) + "]"
		isnum = fmt.Sprintf("match(%s, %s)", p, numericPattern)
		value = fmt.Sprintf("if(%[1]s, toDecimal256OrZero(%[2]s, %[3]d), toDecimal256(0, %[3]d))", isnum, p, scale)
		prop = p
		if q.Numeric {
			where = append(where, isnum)
		}
	}

	where = append(where, filterConditions(a, "properties", q)...)
	filter := ""
	if len(where) > 0 {
		filter = "WHERE " + strings.Join(where, "\n\t\tAND ")
	}

	s.sql = fmt.Sprintf(`sel AS (
	SELECT organization_id, subscription_id, code, transaction_id AS tx, timestamp AS ts, sequence AS seq,
		properties AS props, precise_total_amount_cents AS amount, ingested_at,
		%s AS value, %s AS isnum, %s AS prop,
		if(mapContains(properties, 'operation_type'), properties['operation_type'], 'add') AS op,
		%s AS day
	FROM (
		SELECT * FROM events_enriched FINAL
		WHERE %s
	)
	%s
)`, value, isnum, prop, s.dayOf("timestamp"), strings.Join(bounds, " AND "), filter)
	return s, nil
}

// with returns the statement body preceded by the selection.
func (s *selection) with(body string, ctes ...string) string {
	return "WITH " + strings.Join(append([]string{s.sql}, ctes...), ",\n") + "\n" + body
}

// dayOf returns the expression of the local day number of a timestamp
// expression.
func (s *selection) dayOf(ts string) string {
	return fmt.Sprintf("toInt64(toDate32(toTimeZone(%s, %s)))", ts, s.args.add(calc.Location(s.q).String()))
}

func groupBy(cols []string) string {
	if len(cols) == 0 {
		return ""
	}
	return "GROUP BY " + strings.Join(cols, ", ")
}

func partitionBy(cols ...string) string {
	if len(cols) == 0 {
		return ""
	}
	return "PARTITION BY " + strings.Join(cols, ", ")
}

// prefixList returns the items as the head of a select list.
func prefixList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return strings.Join(items, ", ") + ","
}

// filterConditions returns the conditions of the pricing group values and
// the charge filter of a query on the map col: the properties of the
// events, or the dimensions of the usage buckets.
func filterConditions(a *args, col string, q metering.Query) []string {
	var where []string
	for _, k := range slices.Sorted(maps.Keys(q.GroupValues)) {
		v := ""
		if q.GroupValues[k] != nil {
			v = *q.GroupValues[k]
		}
		where = append(where, fmt.Sprintf("%s[%s] = %s", col, a.add(k), a.add(v)))
	}
	for _, k := range slices.Sorted(maps.Keys(q.Filter.Matching)) {
		values := q.Filter.Matching[k]
		if len(values) == 0 {
			where = append(where, "false")
			continue
		}
		key := a.add(k)
		where = append(where, fmt.Sprintf("(mapContains(%[3]s, %[1]s) AND has(%[2]s, %[3]s[%[1]s]))", key, a.add(values), col))
	}
	for _, clause := range q.Filter.Ignored {
		var and []string
		for _, k := range slices.Sorted(maps.Keys(clause)) {
			values := clause[k]
			if len(values) == 0 {
				continue
			}
			and = append(and, fmt.Sprintf("has(%s, %s[%s])", a.add(values), col, a.add(k)))
		}
		if len(and) > 0 {
			where = append(where, "NOT ("+strings.Join(and, " AND ")+")")
		}
	}
	return where
}
