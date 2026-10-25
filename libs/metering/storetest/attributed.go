package storetest

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// NewAttributedStore returns the store under test holding exactly the
// events, with their attribution labels.
type NewAttributedStore func(t *testing.T, events []metering.Event) metering.AttributedUsageReader

// attributedEvent is an enriched event of the attributed usage cases.
type attributedEvent struct {
	code   string
	value  string
	labels map[string]string
	props  map[string]string
	at     time.Time
}

// event returns the event of org and sub with the transaction index i.
func (e attributedEvent) event(org, sub string, i int) metering.Event {
	at := e.at
	if at.IsZero() {
		at = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	}
	v := e.value
	return metering.Event{
		OrganizationID: org, SubscriptionID: sub, Code: e.code, TransactionID: fmt.Sprintf("tx-%d", i), Timestamp: at,
		IngestedAt: at, Properties: metering.Properties(e.props), Value: &v, AttributionLabels: e.labels,
	}
}

// attributedCells returns the units, amount and events of each cell of v.
func attributedCells(v metering.AttributedValues) [][3]string {
	out := make([][3]string, len(v.Cells))
	for i, c := range v.Cells {
		out[i] = [3]string{c.Units.Reduce().String(), c.Amount.Reduce().String(), fmt.Sprint(c.Events)}
	}
	return out
}

// attributedRows returns the value, rank, page membership, amount and
// events of each row.
func attributedRows(u metering.AttributedUsage) []string {
	out := make([]string, len(u.Rows))
	for i, r := range u.Rows {
		out[i] = fmt.Sprintf("%s/%d/%t/%s/%d", r.Value, r.Rank, r.InPage, r.Amount.Reduce(), r.Events)
	}
	return out
}

// querySpec is the spec of the attributed usage query.
const querySpec = "spec/services/events/stores/clickhouse/attributed_usage_query_spec.rb:"

// RunAttributed runs the attributed usage cases against the store
// newStore returns: two charges (tokens, a sum priced 1 with filters
// {model, region} at 20 and {model} at 10; requests, an unpriced count)
// over events of the teams eng and data, one without a team, one of
// another code and one after the window.
//
// source: spec/services/events/stores/clickhouse/attributed_usage_query_spec.rb:132-145
func RunAttributed(t *testing.T, newStore NewAttributedStore) {
	t.Helper()
	dec := money.MustParse
	ptr := func(d money.Decimal) *money.Decimal { return &d }
	base := []attributedEvent{
		{code: "tokens", value: "100", labels: map[string]string{"team": "eng"}, props: map[string]string{"model": "opus"}},
		{code: "tokens", value: "10", labels: map[string]string{"team": "eng"}, props: map[string]string{"model": "opus", "region": "eu"}},
		{code: "tokens", value: "5", labels: map[string]string{"team": "eng"}, props: map[string]string{"model": "haiku"}},
		{code: "requests", value: "1", labels: map[string]string{"team": "eng"}},
		{code: "requests", value: "1", labels: map[string]string{"team": "data"}},
		{code: "requests", value: "1", labels: map[string]string{"team": "data"}},
		{code: "tokens", value: "7", labels: map[string]string{"team": "data"}},
		{code: "tokens", value: "3"},
		{code: "other", value: "1000", labels: map[string]string{"team": "eng"}},
		{code: "tokens", value: "1000", labels: map[string]string{"team": "eng"}, at: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
	}
	tricky := `o\'brien`
	var events []metering.Event
	for i, e := range base {
		events = append(events, e.event("org_a", "sub_a", i))
	}
	for i, e := range append(slices.Clone(base), attributedEvent{code: "tokens", value: "1", labels: map[string]string{"team": tricky}}) {
		events = append(events, e.event("org_t", "sub_t", i))
	}
	s := newStore(t, events)

	lookup := &metering.PriceLookup{
		KeySets: []metering.PriceKeySet{
			{Keys: []string{"model", "region"}, Entries: map[string]int{"opus\x1feu": 1}},
			{Keys: []string{"model"}, Entries: map[string]int{"opus": 2}},
		},
		FilterIDs:   []string{"opus_eu", "opus"},
		UnitAmounts: []money.Decimal{dec("20"), dec("10")},
	}
	bigLookup := &metering.PriceLookup{KeySets: []metering.PriceKeySet{{Keys: []string{"model"}, Entries: map[string]int{"opus": 20_001}}}}
	for i := range 20_000 {
		bigLookup.KeySets[0].Entries[fmt.Sprintf("model-%012d", i)] = i + 1
		bigLookup.FilterIDs = append(bigLookup.FilterIDs, fmt.Sprintf("f%d", i))
		bigLookup.UnitAmounts = append(bigLookup.UnitAmounts, money.Zero)
	}
	bigLookup.FilterIDs = append(bigLookup.FilterIDs, "opus")
	bigLookup.UnitAmounts = append(bigLookup.UnitAmounts, dec("10"))

	query := func(mod func(*metering.AttributedQuery)) metering.AttributedQuery {
		q := metering.AttributedQuery{
			OrganizationID: "org_a", SubscriptionID: "sub_a",
			From:     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			To:       time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC),
			GroupKey: "team",
			Charges: []metering.AttributedCharge{
				{ChargeID: "tokens_charge", Code: "tokens", UnitAmount: ptr(dec("1")), Lookup: lookup},
				{ChargeID: "requests_charge", Code: "requests", Count: true},
			},
			Order: metering.AttributedOrderAmount, Limit: 50, MaxGroups: 1_000, MaxExecutionTime: 10 * time.Second,
		}
		if mod != nil {
			mod(&q)
		}
		return q
	}
	tricked := func(q *metering.AttributedQuery) { q.OrganizationID, q.SubscriptionID = "org_t", "sub_t" }
	row := func(t *testing.T, u metering.AttributedUsage, value string) metering.AttributedRow {
		t.Helper()
		for _, r := range u.Rows {
			if r.Value == value {
				return r
			}
		}
		t.Fatalf("rows got = %v, want = a row %q", attributedRows(u), value)
		return metering.AttributedRow{}
	}
	tests := []struct {
		name   string
		source string
		q      metering.AttributedQuery
		check  func(t *testing.T, u metering.AttributedUsage)
	}{
		{
			name: "ranks the attributed values, with the unattributed usage apart", source: querySpec + "146",
			q: query(nil),
			check: func(t *testing.T, u metering.AttributedUsage) {
				if got, want := attributedRows(u), []string{"eng/1/true/1205/4", "data/2/true/7/3"}; !slices.Equal(got, want) {
					t.Errorf("rows got = %v, want = %v", got, want)
				}
				if got := fmt.Sprintf("%s/%d", u.Unattributed.Amount.Reduce(), u.Unattributed.Events); got != "3/1" {
					t.Errorf("unattributed got = %s, want = 3/1", got)
				}
			},
		},
		{
			name: "prices each event with its most specific filter", source: querySpec + "156",
			q: query(nil),
			check: func(t *testing.T, u metering.AttributedUsage) {
				if got, want := attributedCells(row(t, u, "eng").AttributedValues), [][3]string{{"115", "1205", "3"}, {"1", "0", "1"}}; !slices.Equal(got, want) {
					t.Errorf("eng cells got = %v, want = %v", got, want)
				}
			},
		},
		{
			// The level counts values, not filters: the query groups by the
			// value only (attributed_usage_query_spec.rb:115, cited below).
			name: "returns the totals of the attributed values", source: querySpec + "160, 115",
			q: query(nil),
			check: func(t *testing.T, u metering.AttributedUsage) {
				if got := fmt.Sprintf("%d/%s/%d", u.Groups, u.Level.Amount.Reduce(), u.Level.Events); got != "2/1212/7" {
					t.Errorf("level got = %s, want = 2/1212/7", got)
				}
			},
		},
		{
			name: "sums each filter of a split charge in its own cell", source: querySpec + "168",
			q: query(func(q *metering.AttributedQuery) { q.Charges[0].Split = true }),
			check: func(t *testing.T, u metering.AttributedUsage) {
				want := [][3]string{{"10", "200", "1"}, {"100", "1000", "1"}, {"5", "5", "1"}, {"1", "0", "1"}}
				if got := attributedCells(row(t, u, "eng").AttributedValues); !slices.Equal(got, want) {
					t.Errorf("eng cells got = %v, want = %v", got, want)
				}
			},
		},
		{
			name: "ranks the values by their events", source: querySpec + "176",
			q: query(func(q *metering.AttributedQuery) { q.Order = metering.AttributedOrderEvents }),
			check: func(t *testing.T, u metering.AttributedUsage) {
				if got, want := attributedRows(u), []string{"eng/1/true/1205/4", "data/2/true/7/3"}; !slices.Equal(got, want) {
					t.Errorf("rows got = %v, want = %v", got, want)
				}
			},
		},
		{
			name: "returns the page, the first row and the unattributed usage", source: querySpec + "185",
			q: query(func(q *metering.AttributedQuery) { q.Order, q.Limit = metering.AttributedOrderEvents, 1 }),
			check: func(t *testing.T, u metering.AttributedUsage) {
				if got, want := attributedRows(u), []string{"eng/1/true/1205/4"}; !slices.Equal(got, want) || u.Unattributed.Events != 1 {
					t.Errorf("rows got = %v (unattributed %d), want = %v (1)", got, u.Unattributed.Events, want)
				}
			},
		},
		{
			name: "returns the matching values with their rank in the level", source: querySpec + "193",
			q: query(func(q *metering.AttributedQuery) { q.Search = "DAT" }),
			check: func(t *testing.T, u metering.AttributedUsage) {
				if got, want := attributedRows(u), []string{"eng/1/false/1205/4", "data/2/true/7/3"}; !slices.Equal(got, want) {
					t.Errorf("rows got = %v, want = %v", got, want)
				}
			},
		},
		{
			name: "keeps the events matching the label filters", source: querySpec + "203",
			q: query(func(q *metering.AttributedQuery) { q.LabelFilters = map[string][]string{"team": {"data", ""}} }),
			check: func(t *testing.T, u metering.AttributedUsage) {
				if got, want := attributedRows(u), []string{"data/1/true/7/3"}; !slices.Equal(got, want) || u.Unattributed.Events != 1 {
					t.Errorf("rows got = %v (unattributed %d), want = %v (1)", got, u.Unattributed.Events, want)
				}
			},
		},
		{
			name: "matches a label filter value holding quotes and backslashes as a plain string", source: querySpec + "216",
			q: query(func(q *metering.AttributedQuery) {
				tricked(q)
				q.LabelFilters = map[string][]string{"team": {tricky}}
			}),
			check: func(t *testing.T, u metering.AttributedUsage) {
				if len(u.Rows) != 1 || u.Rows[0].Value != tricky || u.Unattributed.Events != 0 {
					t.Errorf("rows got = %v (unattributed %d), want = [%s]", attributedRows(u), u.Unattributed.Events, tricky)
				}
			},
		},
		{
			name: "searches for a value holding quotes and backslashes as a plain string", source: querySpec + "224",
			q: query(func(q *metering.AttributedQuery) {
				tricked(q)
				q.Search = `\' OR 1=1 OR '`
			}),
			check: func(t *testing.T, u metering.AttributedUsage) {
				for _, r := range u.Rows {
					if r.InPage {
						t.Errorf("rows in page got = %v, want = none", attributedRows(u))
					}
				}
			},
		},
		{
			name: "runs a lookup past the ClickHouse parser defaults", source: querySpec + "241",
			q: query(func(q *metering.AttributedQuery) { q.Charges[0].Lookup = bigLookup }),
			check: func(t *testing.T, u metering.AttributedUsage) {
				if got, want := attributedCells(row(t, u, "eng").AttributedValues), [][3]string{{"115", "1105", "3"}, {"1", "0", "1"}}; !slices.Equal(got, want) {
					t.Errorf("eng cells got = %v, want = %v", got, want)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			u, err := s.AttributedUsage(t.Context(), tt.q)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, u)
		})
	}

	// The limits guard the query: grouping more values than MaxGroups
	// fails.
	//
	// source: spec/services/events/stores/clickhouse/attributed_usage_query_spec.rb:106
	t.Run("fails past the group limit", func(t *testing.T) {
		_, err := s.AttributedUsage(t.Context(), query(func(q *metering.AttributedQuery) { q.MaxGroups = 1 }))
		if !errors.Is(err, metering.ErrAttributedTooManyGroups) {
			t.Errorf("error got = %v, want = %v", err, metering.ErrAttributedTooManyGroups)
		}
	})
}
