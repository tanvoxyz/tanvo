package storetest

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

//go:embed testdata/*.json
var testdata embed.FS

// Defaults applied to the events, queries and requests of a case that leave
// them empty.
const (
	DefaultOrganizationID = "org_1"
	DefaultSubscriptionID = "sub_1"
	DefaultCode           = "metric"
)

// NewStore returns the store under test, holding exactly the events (in
// ingestion order when their IngestedAt is not set).
type NewStore func(t *testing.T, events []metering.Event) metering.Store

// File is a set of cases ported from one source.
type File struct {
	// Fixtures are named event lists cases can start from.
	Fixtures map[string][]metering.Event `json:"fixtures,omitempty"`
	Cases    []Case                      `json:"cases"`
}

// Case is one conformance case: events, one call and its expected outcome.
type Case struct {
	Name string `json:"name"`
	// Source points at the lago spec the case is ported from, as
	// "spec/path_spec.rb:line".
	Source string `json:"source"`
	// Skip, when set, is why the case does not run.
	Skip string `json:"skip,omitempty"`
	// Code is the default billable metric code.
	Code string `json:"code,omitempty"`
	// Location is the customer's time zone.
	Location string `json:"location,omitempty"`
	// Fixture names the fixture of the file whose events come before
	// Events.
	Fixture string                       `json:"fixture,omitempty"`
	Events  []metering.Event             `json:"events,omitempty"`
	Cache   []metering.CachedAggregation `json:"cache,omitempty"`

	// One of Store, Aggregate, PerEvent and Breakdown is set.
	Store     *StoreCall        `json:"store,omitempty"`
	Aggregate *metering.Request `json:"aggregate,omitempty"`
	PerEvent  *PerEventCall     `json:"per_event,omitempty"`
	Breakdown *metering.Request `json:"breakdown,omitempty"`

	// Want is the expected outcome, as JSON. Objects match when their
	// fields match ("groups" objects must be equal), numbers compare as
	// decimals, and arrays of objects carrying "groups" match in any
	// order.
	Want json.RawMessage `json:"want,omitempty"`
	// WantError is "not_implemented" when the call must fail with
	// [metering.ErrNotImplemented], or "error" for any error.
	WantError string `json:"want_error,omitempty"`
	// Precision, when set, accepts decimals differing by less than one unit
	// of that many decimal places.
	Precision *int32 `json:"precision,omitempty"`
}

// StoreCall is a call of one [metering.Store] method.
type StoreCall struct {
	// Method is the method name in snake case: "sum", "grouped_sum",
	// "unique_count_intervals"…
	Method        string                  `json:"method"`
	Query         metering.Query          `json:"query"`
	By            []string                `json:"by,omitempty"`
	Partition     []string                `json:"partition,omitempty"`
	Columns       []string                `json:"columns,omitempty"`
	Limit         int                     `json:"limit,omitempty"`
	Exclude       string                  `json:"exclude,omitempty"`
	PersistedDays *int                    `json:"persisted_days,omitempty"`
	Initial       money.Decimal           `json:"initial,omitzero"`
	InitialValues []metering.GroupedValue `json:"initial_values,omitempty"`
	WithRemove    bool                    `json:"with_remove,omitempty"`
	Event         *metering.Event         `json:"event,omitempty"`
}

// PerEventCall is a call of [metering.PerEvent].
type PerEventCall struct {
	Request metering.Request         `json:"request"`
	Options metering.PerEventOptions `json:"options"`
}

// Cases returns the embedded cases by file name.
func Cases() (map[string][]Case, error) {
	names, err := fs.Glob(testdata, "testdata/*.json")
	if err != nil {
		return nil, fmt.Errorf("storetest: %w", err)
	}
	out := make(map[string][]Case, len(names))
	for _, name := range names {
		b, err := testdata.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("storetest: %w", err)
		}
		var f File
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return nil, fmt.Errorf("storetest: %s: %w", name, err)
		}
		for i, c := range f.Cases {
			if c.Fixture == "" {
				continue
			}
			fixture, ok := f.Fixtures[c.Fixture]
			if !ok {
				return nil, fmt.Errorf("storetest: %s: case %q: unknown fixture %q", name, c.Name, c.Fixture)
			}
			f.Cases[i].Events = append(append([]metering.Event(nil), fixture...), c.Events...)
		}
		out[strings.TrimSuffix(path.Base(name), ".json")] = f.Cases
	}
	return out, nil
}

// Run runs every embedded case against the stores newStore returns. The
// aggregation cases run the aggregations of package metering over the
// store, so that a store passing them computes what the reference
// [metering.MemoryStore] does.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	files, err := Cases()
	if err != nil {
		t.Fatal(err)
	}
	for file, cases := range files {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(c.Name, func(t *testing.T) {
					if c.Skip != "" {
						t.Skipf("%s: %s", c.Source, c.Skip)
					}
					runCase(t, newStore, c)
				})
			}
		})
	}
}

func runCase(t *testing.T, newStore NewStore, c Case) {
	t.Helper()
	loc := time.UTC
	if c.Location != "" {
		var err error
		if loc, err = time.LoadLocation(c.Location); err != nil {
			t.Fatalf("%s: %v", c.Source, err)
		}
	}
	code := c.Code
	if code == "" {
		code = DefaultCode
	}
	events := make([]metering.Event, len(c.Events))
	for i, e := range c.Events {
		e.OrganizationID = or(e.OrganizationID, DefaultOrganizationID)
		e.SubscriptionID = or(e.SubscriptionID, DefaultSubscriptionID)
		e.Code = or(e.Code, code)
		if e.TransactionID == "" {
			e.TransactionID = fmt.Sprintf("tx_%d", i+1)
		}
		events[i] = e
	}
	store := newStore(t, events)
	ctx := context.Background()

	request := func(r metering.Request) metering.Request {
		r.OrganizationID = or(r.OrganizationID, DefaultOrganizationID)
		r.SubscriptionID = or(r.SubscriptionID, DefaultSubscriptionID)
		r.Metric.Code = or(r.Metric.Code, code)
		r.Location = loc
		if c.Cache != nil {
			r.Cache = metering.MemoryCache(c.Cache)
		}
		if r.Event != nil {
			r.Event.OrganizationID = or(r.Event.OrganizationID, DefaultOrganizationID)
			r.Event.SubscriptionID = or(r.Event.SubscriptionID, DefaultSubscriptionID)
			r.Event.Code = or(r.Event.Code, r.Metric.Code)
		}
		return r
	}

	var (
		got any
		err error
	)
	switch {
	case c.Store != nil:
		q := c.Store.Query
		q.OrganizationID = or(q.OrganizationID, DefaultOrganizationID)
		q.SubscriptionID = or(q.SubscriptionID, DefaultSubscriptionID)
		q.Code = or(q.Code, code)
		q.Location = loc
		got, err = callStore(ctx, store, q, *c.Store)
	case c.Aggregate != nil:
		got, err = metering.Aggregate(ctx, store, request(*c.Aggregate))
	case c.PerEvent != nil:
		got, err = metering.PerEvent(ctx, store, request(c.PerEvent.Request), c.PerEvent.Options)
	case c.Breakdown != nil:
		got, err = metering.Breakdown(ctx, store, request(*c.Breakdown))
	default:
		t.Fatalf("%s: case without a call", c.Source)
	}

	switch {
	case c.WantError == "not_implemented":
		if !errors.Is(err, metering.ErrNotImplemented) {
			t.Fatalf("%s: error got = %v, want = %v", c.Source, err, metering.ErrNotImplemented)
		}
		return
	case c.WantError != "":
		if err == nil {
			t.Fatalf("%s: error got = nil, want = an error", c.Source)
		}
		return
	case err != nil:
		t.Fatalf("%s: unexpected error: %v", c.Source, err)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: %v", c.Source, err)
	}
	actual, err := decode(b)
	if err != nil {
		t.Fatalf("%s: %v", c.Source, err)
	}
	want, err := decode(c.Want)
	if err != nil {
		t.Fatalf("%s: want: %v", c.Source, err)
	}
	if diff := compare("", want, actual, c.Precision, false); diff != "" {
		t.Errorf("%s: %s\ngot  = %s\nwant = %s", c.Source, diff, b, c.Want)
	}
}

func decode(b []byte) (any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("storetest: %w", err)
	}
	return v, nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func callStore(ctx context.Context, s metering.Store, q metering.Query, c StoreCall) (any, error) {
	switch c.Method {
	case "count":
		return s.Count(ctx, q)
	case "grouped_count":
		return s.GroupedCount(ctx, q, c.By)
	case "sum":
		return s.Sum(ctx, q)
	case "grouped_sum":
		return s.GroupedSum(ctx, q, c.By)
	case "max":
		return s.Max(ctx, q)
	case "grouped_max":
		return s.GroupedMax(ctx, q, c.By)
	case "last":
		return s.Last(ctx, q)
	case "grouped_last":
		return s.GroupedLast(ctx, q, c.Partition, c.Columns)
	case "last_event":
		e, err := s.LastEvent(ctx, q)
		if e == nil {
			return nil, err
		}
		return map[string]any{"transaction_id": e.TransactionID, "timestamp": e.Timestamp}, err
	case "grouped_last_event":
		return s.GroupedLastEvent(ctx, q, c.By)
	case "sum_precise_total_amount_cents":
		return s.SumPreciseTotalAmountCents(ctx, q)
	case "grouped_sum_precise_total_amount_cents":
		return s.GroupedSumPreciseTotalAmountCents(ctx, q, c.By)
	case "event_values":
		return s.EventValues(ctx, q, c.Limit, c.Exclude)
	case "prorated_event_values":
		return s.ProratedEventValues(ctx, q)
	case "prorated_sum":
		return s.ProratedSum(ctx, q, c.PersistedDays)
	case "grouped_prorated_sum":
		return s.GroupedProratedSum(ctx, q, c.By, c.PersistedDays)
	case "sum_by_day":
		return s.SumByDay(ctx, q)
	case "unique_count":
		return s.UniqueCount(ctx, q)
	case "grouped_unique_count":
		return s.GroupedUniqueCount(ctx, q, c.By)
	case "prorated_unique_count":
		return s.ProratedUniqueCount(ctx, q)
	case "grouped_prorated_unique_count":
		return s.GroupedProratedUniqueCount(ctx, q, c.By)
	case "unique_count_intervals":
		return s.UniqueCountIntervals(ctx, q, c.WithRemove)
	case "active_unique_property":
		if c.Event == nil {
			return nil, errors.New("storetest: active_unique_property needs an event")
		}
		e := *c.Event
		e.OrganizationID = or(e.OrganizationID, q.OrganizationID)
		e.SubscriptionID = or(e.SubscriptionID, q.SubscriptionID)
		e.Code = or(e.Code, q.Code)
		return s.ActiveUniqueProperty(ctx, q, e)
	case "weighted_sum":
		return s.WeightedSum(ctx, q, c.Initial)
	case "grouped_weighted_sum":
		return s.GroupedWeightedSum(ctx, q, c.By, c.InitialValues)
	default:
		return nil, fmt.Errorf("storetest: unknown store method %q", c.Method)
	}
}

// compare returns a description of the first difference between want and
// got, or "" when they match.
func compare(at string, want, got any, precision *int32, exact bool) string {
	switch w := want.(type) {
	case nil:
		if isEmpty(got) {
			return ""
		}
		return fmt.Sprintf("%s: got = %v, want = null", at, got)
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			if len(w) == 0 && isEmpty(got) {
				return ""
			}
			return fmt.Sprintf("%s: got = %v, want = an object", at, got)
		}
		for k, wv := range w {
			if d := compare(at+"."+k, wv, g[k], precision, exact || k == "groups"); d != "" {
				return d
			}
		}
		if exact {
			for k, gv := range g {
				if _, ok := w[k]; !ok && !isEmpty(gv) {
					return fmt.Sprintf("%s.%s: got = %v, want = absent", at, k, gv)
				}
			}
		}
		return ""
	case []any:
		g, ok := got.([]any)
		if !ok {
			if len(w) == 0 && isEmpty(got) {
				return ""
			}
			return fmt.Sprintf("%s: got = %v, want = an array", at, got)
		}
		if len(g) != len(w) {
			return fmt.Sprintf("%s: got %d elements = %v, want %d = %v", at, len(g), g, len(w), w)
		}
		if grouped(w) && grouped(g) {
			return compareGrouped(at, w, g, precision)
		}
		for i := range w {
			if d := compare(fmt.Sprintf("%s[%d]", at, i), w[i], g[i], precision, exact); d != "" {
				return d
			}
		}
		return ""
	default:
		if equalScalars(want, got, precision) {
			return ""
		}
		return fmt.Sprintf("%s: got = %v, want = %v", at, got, want)
	}
}

func isEmpty(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

func grouped(items []any) bool {
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := m["groups"]; !ok {
			return false
		}
	}
	return true
}

func compareGrouped(at string, want, got []any, precision *int32) string {
	used := make([]bool, len(got))
	for i, w := range want {
		wg := w.(map[string]any)["groups"]
		found := false
		for j, g := range got {
			if used[j] || compare("", wg, g.(map[string]any)["groups"], precision, true) != "" {
				continue
			}
			if d := compare(fmt.Sprintf("%s[%d]", at, i), w, g, precision, false); d != "" {
				return d
			}
			used[j], found = true, true
			break
		}
		if !found {
			return fmt.Sprintf("%s[%d]: no element with groups %v in %v", at, i, wg, got)
		}
	}
	return ""
}

func equalScalars(want, got any, precision *int32) bool {
	if wd, ok := decimal(want); ok {
		gd, ok := decimal(got)
		if !ok {
			return false
		}
		if precision != nil {
			return wd.Sub(gd).Abs().LessThan(money.New(1, -*precision))
		}
		return wd.Equal(gd)
	}
	if ws, ok := want.(string); ok {
		gs, ok := got.(string)
		if !ok {
			return false
		}
		wt, err1 := time.Parse(time.RFC3339Nano, ws)
		gt, err2 := time.Parse(time.RFC3339Nano, gs)
		if err1 == nil && err2 == nil {
			return wt.Equal(gt)
		}
		return ws == gs
	}
	return want == got
}

func decimal(v any) (money.Decimal, bool) {
	switch v := v.(type) {
	case json.Number:
		d, err := money.Parse(v.String())
		return d, err == nil
	case string:
		if _, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return money.Zero, false
		}
		d, err := money.Parse(v)
		return d, err == nil
	}
	return money.Zero, false
}
