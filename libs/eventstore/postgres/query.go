package postgres

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/calc"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// numericPattern is the form of the property values taking part in numeric
// aggregations, as metering reads them.
const numericPattern = `'^-?[0-9]+([.][0-9]+)?$'`

// args collects the parameters of a statement.
type args []any

// add appends a parameter and returns its placeholder.
func (a *args) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// text appends a text parameter and returns its placeholder, cast to text
// for the operators overloaded on other types (->>, AT TIME ZONE).
func (a *args) text(v string) string { return a.add(v) + "::text" }

// selection is the selected events of a query, as the CTE "sel" with the
// columns:
//
//	organization_id, subscription_id, code, tx, ts, seq, props, amount,
//	ingested_at, value (numeric, 0 unless the property is a number),
//	isnum, prop (the property, "" when missing), op (the unique-count
//	operation), day (the local day number)
type selection struct {
	q    metering.Query
	args args
	sql  string
}

// group returns the expression of a grouping key: nil when missing or
// empty.
func (s *selection) group(key string) string {
	return "NULLIF(props->>" + s.args.text(key) + ", '')"
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

func jsonObject(k, v string) string {
	b, _ := json.Marshal(map[string]string{k: v})
	return string(b)
}

func newSelection(q metering.Query) (*selection, error) {
	if q.OrganizationID == "" {
		return nil, eventstore.ErrMissingOrganization
	}
	s := &selection{q: q}
	a := &s.args
	org := a.add(q.OrganizationID)
	where := []string{"e.organization_id = " + org}
	scope := []string{"b.organization_id = " + org}
	if q.SubscriptionID != "" {
		p := a.add(q.SubscriptionID)
		where = append(where, "e.subscription_id = "+p)
		scope = append(scope, "b.subscription_id = "+p)
	}
	if q.Code != "" {
		p := a.add(q.Code)
		where = append(where, "e.code = "+p)
		scope = append(scope, "b.code = "+p)
	}
	if !q.IncludeHistory {
		where = append(where, `e."timestamp" >= `+a.add(calc.From(q))+"::timestamptz")
	}
	until := a.add(calc.Until(q)) + "::timestamptz"
	where = append(where, `e."timestamp" <= `+until)
	if !q.Until.IsZero() && q.UntilTransactionID != "" {
		// The events sharing the timestamp of the closing event are
		// selected up to it in ingestion order.
		where = append(where, fmt.Sprintf(`(e."timestamp" < %[1]s OR e.sequence <= coalesce((
			SELECT max(b.sequence) FROM __schema__.events b WHERE %[2]s AND b."timestamp" = %[1]s AND b.transaction_id = %[3]s
		), 9223372036854775807))`, until, strings.Join(scope, " AND "), a.add(q.UntilTransactionID)))
	}

	value, isnum, prop := "0::numeric", "false", "''"
	if q.Property != "" {
		p := a.text(q.Property)
		value = fmt.Sprintf("CASE WHEN e.properties->>%[1]s ~ %[2]s THEN (e.properties->>%[1]s)::numeric ELSE 0 END", p, numericPattern)
		isnum = fmt.Sprintf("coalesce(e.properties->>%s ~ %s, false)", p, numericPattern)
		prop = fmt.Sprintf("coalesce(e.properties->>%s, '')", p)
		if q.Numeric {
			where = append(where, fmt.Sprintf("e.properties->>%s ~ %s", p, numericPattern))
		}
	}

	where = append(where, filterConditions(a, "e.properties", q)...)

	tz := a.text(calc.Location(q).String())
	s.sql = fmt.Sprintf(`sel AS NOT MATERIALIZED (
	SELECT e.organization_id, e.subscription_id, e.code, e.transaction_id AS tx, e."timestamp" AS ts, e.sequence AS seq,
		e.properties AS props, e.precise_total_amount_cents AS amount, e.ingested_at,
		%s AS value, %s AS isnum, %s AS prop,
		coalesce(e.properties->>'operation_type', 'add') AS op,
		((e."timestamp" AT TIME ZONE %s)::date - DATE '1970-01-01') AS day
	FROM __schema__.events e
	WHERE %s
)`, value, isnum, prop, tz, strings.Join(where, "\n\t\tAND "))
	return s, nil
}

// with returns the statement body preceded by the selection.
func (s *selection) with(body string, ctes ...string) string {
	return "WITH " + strings.Join(append([]string{s.sql}, ctes...), ",\n") + "\n" + body
}

// tz returns a placeholder of the time zone of the query.
func (s *selection) tz() string { return s.args.text(calc.Location(s.q).String()) }

// dayOf returns the expression of the local day number of a timestamp
// expression.
func (s *selection) dayOf(ts string) string {
	return fmt.Sprintf("((%s AT TIME ZONE %s)::date - DATE '1970-01-01')", ts, s.tz())
}

func positions(n int) string {
	p := make([]string, n)
	for i := range p {
		p[i] = strconv.Itoa(i + 1)
	}
	return strings.Join(p, ", ")
}

func groupBy(n int) string {
	if n == 0 {
		return ""
	}
	return "GROUP BY " + positions(n)
}

func joinCols(cols ...[]string) string {
	var all []string
	for _, c := range cols {
		all = append(all, c...)
	}
	return strings.Join(all, ", ")
}

// filterConditions returns the conditions of the pricing group values and
// the charge filter of a query on the JSON object col: the properties of
// the events, or the dimensions of the usage buckets.
func filterConditions(a *args, col string, q metering.Query) []string {
	var where []string
	for _, k := range slices.Sorted(maps.Keys(q.GroupValues)) {
		if v := q.GroupValues[k]; v != nil && *v != "" {
			where = append(where, col+" @> "+a.add(jsonObject(k, *v))+"::jsonb")
		} else {
			where = append(where, "coalesce("+col+"->>"+a.text(k)+", '') = ''")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(q.Filter.Matching)) {
		values := q.Filter.Matching[k]
		if len(values) == 0 {
			where = append(where, "false")
			continue
		}
		var or []string
		for _, v := range values {
			or = append(or, col+" @> "+a.add(jsonObject(k, v))+"::jsonb")
		}
		where = append(where, "("+strings.Join(or, " OR ")+")")
	}
	for _, clause := range q.Filter.Ignored {
		var and []string
		for _, k := range slices.Sorted(maps.Keys(clause)) {
			values := clause[k]
			if len(values) == 0 {
				continue
			}
			and = append(and, fmt.Sprintf("coalesce(%s->>%s, '') = ANY(%s::text[])", col, a.text(k), a.add(values)))
		}
		if len(and) > 0 {
			where = append(where, "NOT ("+strings.Join(and, " AND ")+")")
		}
	}
	return where
}
