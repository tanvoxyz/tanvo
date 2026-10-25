package usageattribution_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/usageattribution"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

const querySpec = "spec/services/usage_attributions/query_service_spec.rb:"

var (
	queryNow  = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	startedAt = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	eventAt   = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	eur       = money.MustCurrency("EUR")
)

// queryOptions change the fixture of query_service_spec.rb:16-79.
type queryOptions struct {
	interval period.Interval
	// tokensMetric, tokensCharge and requestsMetric change the metrics and
	// charges before they are stored.
	tokensMetric   func(*catalog.BillableMetric)
	tokensCharge   func(*catalog.Charge)
	requestsMetric func(*catalog.BillableMetric)
	subscription   func(*subscription.Subscription)
}

// queryFixture is a monthly EUR plan of a subscription started on August
// 1st with a tokens sum charge at 0.5 and a requests count charge at 2,
// the types team and user (hierarchical) and model (flat), and the events
// of query_service_spec.rb:39-48.
type queryFixture struct {
	fixture
	q        *usageattribution.QueryService
	reader   *recordingReader
	customer *customer.Customer
	plan     *catalog.Plan
	sub      *subscription.Subscription
	tokens   *catalog.Charge
	requests *catalog.Charge
	tx       int
}

// recordingReader records the last query.
type recordingReader struct {
	metering.AttributedUsageReader
	unavailable bool
	last        *metering.AttributedQuery
}

func (r *recordingReader) AttributedUsageAvailable(org string) bool {
	return !r.unavailable && r.AttributedUsageReader.AttributedUsageAvailable(org)
}

func (r *recordingReader) AttributedUsage(ctx context.Context, q metering.AttributedQuery) (metering.AttributedUsage, error) {
	r.last = &q
	return r.AttributedUsageReader.AttributedUsage(ctx, q)
}

func newQueryFixture(t *testing.T, o queryOptions) *queryFixture {
	t.Helper()
	h := billingtest.New(t, queryNow)
	s, err := usageattribution.New(h.Deps)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	f := &queryFixture{fixture: fixture{h, s}, reader: &recordingReader{AttributedUsageReader: h.Events}}
	f.q, err = usageattribution.NewQuery(h.Deps, f.reader)
	if err != nil {
		t.Fatalf("NewQuery() error = %v", err)
	}
	f.create(t, "team", attribution.RoleHierarchical, none)
	f.create(t, "user", attribution.RoleHierarchical, none)
	f.create(t, "model", attribution.RoleFlat, none)

	f.customer = h.Customer()
	f.plan = h.Plan(cmp.Or(o.interval, period.Monthly), 0, func(p *catalog.Plan) { p.Currency = eur })
	field := func(fn func(*catalog.BillableMetric)) []func(*catalog.BillableMetric) {
		if fn == nil {
			return nil
		}
		return []func(*catalog.BillableMetric){fn}
	}
	tokens := h.Metric("tokens", catalog.AggregationTypeSum, "tokens", field(o.tokensMetric)...)
	requests := h.Metric("requests", catalog.AggregationTypeCount, "", field(o.requestsMetric)...)
	var chargeOpts []func(*catalog.Charge)
	if o.tokensCharge != nil {
		chargeOpts = append(chargeOpts, o.tokensCharge)
	}
	f.tokens = h.Charge(f.plan, tokens, catalog.ChargeModelStandard, `{"amount":"0.5"}`, chargeOpts...)
	f.requests = h.Charge(f.plan, requests, catalog.ChargeModelStandard, `{"amount":"2"}`)
	subOpts := []func(*subscription.Subscription){billingtest.SubscribedAt(startedAt)}
	if o.subscription != nil {
		subOpts = append(subOpts, o.subscription)
	}
	f.sub = h.Subscription(f.customer, f.plan, subOpts...)

	f.event("tokens", "1000", map[string]string{"team": "eng", "user": "alice", "model": "opus"}, map[string]string{"model": "opus"})
	f.event("tokens", "500", map[string]string{"team": "eng", "user": "bob", "model": "sonnet"}, map[string]string{"model": "sonnet"})
	f.event("tokens", "2000", map[string]string{"team": "data", "user": "carol", "model": "opus"}, map[string]string{"model": "opus"})
	f.event("requests", "1", map[string]string{"team": "eng", "user": "alice", "model": "opus"}, nil)
	f.event("tokens", "100", nil, nil)
	f.eventAt(time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), "", "tokens", "999", map[string]string{"team": "eng", "user": "alice"}, nil)
	h.Clock.Set(queryNow) // creating the types advanced it
	return f
}

// event stores an enriched event of the subscription on September 10th.
func (f *queryFixture) event(code, value string, labels, props map[string]string) {
	f.eventAt(eventAt, "", code, value, labels, props)
}

func (f *queryFixture) eventAt(ts time.Time, tx, code, value string, labels, props map[string]string) {
	if tx == "" {
		f.tx++
		tx = fmt.Sprintf("tx-%d", f.tx)
	}
	f.h.Events.Add(metering.Event{
		OrganizationID: f.h.OrgID().String(), SubscriptionID: f.sub.ExternalID, Code: code, TransactionID: tx,
		Timestamp: ts, Properties: props, Value: &value, AttributionLabels: labels, IngestedAt: queryNow,
	})
}

// input is the query of query_service_spec.rb:27-37.
func (f *queryFixture) input() usageattribution.QueryInput {
	return usageattribution.QueryInput{
		OrganizationID: f.h.OrgID(), CustomerExternalID: f.customer.ExternalID, SubscriptionExternalID: f.sub.ExternalID,
		GroupBy: "team", Basis: usageattribution.BasisAmount, Limit: 50,
	}
}

func (f *queryFixture) run(t *testing.T, in usageattribution.QueryInput) *usageattribution.Result {
	t.Helper()
	res, err := f.q.Query(t.Context(), in)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	return res
}

// amount formats an amount in minor units, "nil" when unset.
func amount(d *money.Decimal) string {
	if d == nil {
		return "nil"
	}
	return d.Reduce().String()
}

// summary returns value, amount and events of each row (spec :64).
func summary(rows []usageattribution.Row) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s %s %d", r.Value, amount(r.Amount), r.Events))
	}
	return out
}

func aggregateOf(a usageattribution.Aggregate) string {
	return fmt.Sprintf("%s %d", amount(a.Amount), a.Events)
}

// cellsOf returns charge, filter, units, amount and events of each cell,
// by units (spec :68).
func (f *queryFixture) cellsOf(cells []usageattribution.Cell) []string {
	cells = slices.Clone(cells)
	slices.SortStableFunc(cells, func(a, b usageattribution.Cell) int { return a.Units.Cmp(b.Units) })
	out := []string{}
	for _, c := range cells {
		out = append(out, f.cell(c))
	}
	return out
}

func (f *queryFixture) cell(c usageattribution.Cell) string {
	name := map[core.ChargeID]string{f.tokens.ID: "tokens", f.requests.ID: "requests"}[c.Charge.ID]
	filter := "nil"
	if c.Filter != nil {
		filter = fmt.Sprint(slices.IndexFunc(f.tokens.Filters, func(cf catalog.ChargeFilter) bool { return cf.ID == c.Filter.ID }))
	}
	return fmt.Sprintf("%s %s %s %s %d", name, filter, c.Units.Reduce(), amount(c.Amount), c.Events)
}

func wantStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s got = %q, want = %q", what, got, want)
	}
}

func wantValidation(t *testing.T, err error, want ...string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error got = %v, want = a validation error", err)
	}
	var got []string
	for _, fe := range ve.Errors {
		got = append(got, fe.Field+": "+fe.Code)
	}
	wantStrings(t, "validation errors", got, want)
}

func wantResourceNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	if nf, ok := errors.AsType[*core.NotFoundError](err); !ok || nf.Resource != resource {
		t.Fatalf("error got = %v, want = %s_not_found", err, resource)
	}
}

func values(vs ...string) []string { return vs }

// opusFilter is the model filter on opus of query_service_spec.rb:318-322.
func opusFilter(metric *catalog.BillableMetric) {
	metric.Filters = append(metric.Filters, catalog.MetricFilter{Key: "model", Values: values("opus", "sonnet")})
}

func TestQueryAggregates(t *testing.T) {
	t.Run("aggregates the current period per node, ranked by amount, with the unattributed usage apart", func(t *testing.T) {
		t.Log("source:", querySpec+"81")
		f := newQueryFixture(t, queryOptions{})
		res := f.run(t, f.input())
		wantStrings(t, "rows", summary(res.Rows), values("data 100000 1", "eng 75200 3"))
		if got := []int{res.Rows[0].Rank, res.Rows[1].Rank}; !slices.Equal(got, []int{1, 2}) {
			t.Errorf("ranks got = %v, want = [1 2]", got)
		}
		wantStrings(t, "unattributed", values(aggregateOf(res.Unattributed)), values("5000 1"))
		wantStrings(t, "totals", values(aggregateOf(res.Totals)), values("180200 5"))
		if res.Groups != 2 || res.Basis != usageattribution.BasisAmount || res.Currency != eur ||
			res.GroupBy != "team" || res.Limit != 50 || res.Offset != 0 || res.Subscription.ID != f.sub.ID {
			t.Errorf("result got = %+v, want = 2 groups on the amount basis in EUR by team, page 50 at 0", res)
		}
		// The window is half-open: it ends at the start of October.
		if !res.From.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) || !res.To.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("window got = %v - %v, want = 2026-09-01 - 2026-10-01", res.From, res.To)
		}
	})

	t.Run("returns the units, amount and events count of each charge", func(t *testing.T) {
		t.Log("source:", querySpec+"95")
		f := newQueryFixture(t, queryOptions{})
		res := f.run(t, f.input())
		wantStrings(t, "cells", f.cellsOf(res.Rows[1].Cells), values("requests nil 1 200 1", "tokens nil 1500 75000 2"))
	})

	t.Run("narrows the events to the matching labels", func(t *testing.T) {
		t.Log("source:", querySpec+"108")
		f := newQueryFixture(t, queryOptions{})
		in := f.input()
		in.GroupBy, in.Filters = "user", map[string][]string{"team": {"eng"}, "model": {"opus", "haiku"}}
		res := f.run(t, in)
		wantStrings(t, "rows", summary(res.Rows), values("alice 50200 2"))
		wantStrings(t, "unattributed", values(aggregateOf(res.Unattributed)), values("0 0"))
	})

	t.Run("matches the events without the label with a filter on unattributed usage", func(t *testing.T) {
		t.Log("source:", querySpec+"118")
		f := newQueryFixture(t, queryOptions{})
		in := f.input()
		in.GroupBy, in.Filters = "user", map[string][]string{"team": {""}}
		res := f.run(t, in)
		wantStrings(t, "rows", summary(res.Rows), values())
		wantStrings(t, "unattributed", values(aggregateOf(res.Unattributed)), values("5000 1"))
		wantStrings(t, "totals", values(aggregateOf(res.Totals)), values("5000 1"))
	})

	t.Run("matches a label value containing a quote", func(t *testing.T) {
		t.Log("source:", querySpec+"130")
		f := newQueryFixture(t, queryOptions{})
		f.event("tokens", "10", map[string]string{"team": "o'brien"}, nil)
		in := f.input()
		in.Filters = map[string][]string{"team": {"o'brien"}}
		wantStrings(t, "rows", summary(f.run(t, in).Rows), values("o'brien 500 1"))
	})

	t.Run("clamps a custom window to the subscription lifetime", func(t *testing.T) {
		t.Log("source:", querySpec+"139")
		f := newQueryFixture(t, queryOptions{})
		in := f.input()
		in.From, in.To = new(time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)), new(time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC))
		res := f.run(t, in)
		wantStrings(t, "rows", summary(res.Rows), values("eng 49950 1"))
		if !res.From.Equal(startedAt) || !res.To.Equal(*in.To) {
			t.Errorf("window got = %v - %v, want = %v - %v", res.From, res.To, startedAt, *in.To)
		}
	})

	t.Run("narrows the current period of a yearly plan to its last 31 days", func(t *testing.T) {
		t.Log("source:", querySpec+"149")
		f := newQueryFixture(t, queryOptions{interval: period.Yearly})
		res := f.run(t, f.input())
		if !res.From.Equal(time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)) || !res.To.Equal(queryNow) {
			t.Errorf("window got = %v - %v, want = 2026-08-15 12:00 - %v", res.From, res.To, queryNow)
		}
		wantStrings(t, "rows", summary(res.Rows), values("eng 125150 4", "data 100000 1"))
	})

	for _, tt := range []struct {
		source        string
		name          string
		offset        int
		rows          []string
		limit, groups int
	}{
		{querySpec + "160", "returns the requested page with the totals of the whole level", 1, values("eng 75200 3"), 1, 2},
		{querySpec + "170", "returns the first ranked value with the totals of the whole level", 0, values("data 100000 1"), 1, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newQueryFixture(t, queryOptions{})
			in := f.input()
			in.Limit, in.Offset = 1, tt.offset
			res := f.run(t, in)
			wantStrings(t, "rows", summary(res.Rows), tt.rows)
			wantStrings(t, "totals", values(aggregateOf(res.Totals)), values("180200 5"))
			if res.Groups != int64(tt.groups) || res.Limit != tt.limit || res.Offset != tt.offset {
				t.Errorf("page got = %d groups, %d at %d, want = %d groups, %d at %d", res.Groups, res.Limit, res.Offset, tt.groups, tt.limit, tt.offset)
			}
		})
	}
}

func TestQueryUnitsBasis(t *testing.T) {
	// The default basis, with a filter on the tokens charge
	// (query_service_spec.rb:177-185).
	setup := func(t *testing.T) *queryFixture {
		f := newQueryFixture(t, queryOptions{
			tokensMetric: opusFilter,
			tokensCharge: func(c *catalog.Charge) {
				c.Filters = append(c.Filters, billingtest.ChargeFilter(map[string][]string{"model": {"opus"}}, `{"amount":"10"}`))
			},
		})
		return f
	}
	units := func(f *queryFixture) usageattribution.QueryInput {
		in := f.input()
		in.Basis = ""
		return in
	}

	t.Run("returns the units ranked by events, without amounts", func(t *testing.T) {
		t.Log("source:", querySpec+"187")
		f := setup(t)
		res := f.run(t, units(f))
		if res.Basis != usageattribution.BasisUnits {
			t.Errorf("basis got = %q, want = %q", res.Basis, usageattribution.BasisUnits)
		}
		wantStrings(t, "rows", summary(res.Rows), values("eng nil 3", "data nil 1"))
		wantStrings(t, "cells", f.cellsOf(res.Rows[0].Cells), values("requests nil 1 nil 1", "tokens nil 1500 nil 2"))
		wantStrings(t, "totals", values(aggregateOf(res.Totals)), values("nil 5"))
	})

	t.Run("does not match the events with the charge filters", func(t *testing.T) {
		t.Log("source:", querySpec+"199")
		f := setup(t)
		f.run(t, units(f))
		for _, c := range f.reader.last.Charges {
			if c.Lookup != nil {
				t.Errorf("charge %s lookup got = %+v, want = nil", c.Code, c.Lookup)
			}
		}
	})

	t.Run("returns the units of each filter of a split charge", func(t *testing.T) {
		t.Log("source:", querySpec+"207")
		f := setup(t)
		in := units(f)
		in.SplitChargeCode = f.tokens.Code
		res := f.run(t, in)
		wantStrings(t, "cells", f.cellsOf(res.Rows[0].Cells), values("requests nil 1 nil 1", "tokens nil 500 nil 1", "tokens 0 1000 nil 1"))
	})
}

func TestQueryHierarchy(t *testing.T) {
	// A squad type under a department type (query_service_spec.rb:227-231).
	setup := func(t *testing.T) (*queryFixture, *attribution.Type) {
		f := newQueryFixture(t, queryOptions{})
		department := f.create(t, "department", attribution.RoleHierarchical, none)
		f.create(t, "squad", attribution.RoleHierarchical, department.ID)
		return f, department
	}
	tests := []struct {
		source  string
		name    string
		filters map[string][]string
		deleted bool
		want    []string
	}{
		{source: querySpec + "233", name: "fails without a filter on the parent", want: values("group_by: parent_filter_required")},
		{source: querySpec + "240", name: "reads the level with a filter on the parent", filters: map[string][]string{"department": {"rnd"}}},
		{source: querySpec + "248", name: "reads the level with a filter on the unattributed parent", filters: map[string][]string{"department": {""}}},
		{source: querySpec + "256", name: "reads the level as a top level when the parent type is deleted", deleted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f, department := setup(t)
			if tt.deleted {
				if _, err := f.s.Delete(t.Context(), f.h.OrgID(), department.ID); err != nil {
					t.Fatalf("Delete() error = %v", err)
				}
			}
			in := f.input()
			in.GroupBy, in.Filters = "squad", tt.filters
			_, err := f.q.Query(t.Context(), in)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Query() error = %v, want = nil", err)
				}
				return
			}
			wantValidation(t, err, tt.want...)
		})
	}
}

func TestQuerySelection(t *testing.T) {
	t.Run("returns the matching values of a search with their rank in the level", func(t *testing.T) {
		t.Log("source:", querySpec+"265")
		f := newQueryFixture(t, queryOptions{})
		in := f.input()
		in.Search = "EN"
		res := f.run(t, in)
		if len(res.Rows) != 1 || res.Rows[0].Value != "eng" || res.Rows[0].Rank != 2 || res.Groups != 2 {
			t.Errorf("rows got = %+v (%d groups), want = eng ranked 2 (2 groups)", res.Rows, res.Groups)
		}
	})

	t.Run("only reads the events of the selected charges", func(t *testing.T) {
		t.Log("source:", querySpec+"282")
		f := newQueryFixture(t, queryOptions{})
		in := f.input()
		in.ChargeCodes = values(f.tokens.Code)
		res := f.run(t, in)
		wantStrings(t, "rows", summary(res.Rows), values("data 100000 1", "eng 75000 2"))
		wantStrings(t, "unattributed", values(aggregateOf(res.Unattributed)), values("5000 1"))
		for _, r := range res.Rows {
			for _, c := range r.Cells {
				if c.Charge.ID != f.tokens.ID {
					t.Errorf("cell charge got = %s, want = %s", c.Charge.ID, f.tokens.ID)
				}
			}
		}
	})

	t.Run("fails on invalid selected charges", func(t *testing.T) {
		t.Log("source:", querySpec+"294")
		f := newQueryFixture(t, queryOptions{})
		max := f.h.Charge(f.plan, f.h.Metric("max", catalog.AggregationTypeMax, "max"), catalog.ChargeModelStandard, `{"amount":"1"}`)
		in := f.input()
		in.ChargeCodes, in.SplitChargeCode = values(max.Code), f.tokens.Code
		_, err := f.q.Query(t.Context(), in)
		wantValidation(t, err, "charge_codes: unsupported_aggregation_type", "split_charge_code: must_be_selected", "split_charge_code: must_have_filters")
	})

	t.Run("fails on an empty charges selection", func(t *testing.T) {
		t.Log("source:", querySpec+"305")
		f := newQueryFixture(t, queryOptions{})
		in := f.input()
		in.ChargeCodes = []string{}
		_, err := f.q.Query(t.Context(), in)
		wantValidation(t, err, "charge_codes: must_not_be_empty")
	})

	outside := func(f *queryFixture) string {
		other := f.h.Plan(period.Monthly, 0)
		return f.h.Charge(other, f.h.Metric("other", catalog.AggregationTypeSum, "tokens"), catalog.ChargeModelStandard, `{"amount":"1"}`).Code
	}
	t.Run("fails on a selected charge outside the plan", func(t *testing.T) {
		t.Log("source:", querySpec+"313")
		f := newQueryFixture(t, queryOptions{})
		in := f.input()
		in.ChargeCodes = values(outside(f))
		_, err := f.q.Query(t.Context(), in)
		wantResourceNotFound(t, err, usageattribution.ResourceCharge)
	})

	t.Run("fails on a split charge outside the plan", func(t *testing.T) {
		t.Log("source:", querySpec+"552")
		f := newQueryFixture(t, queryOptions{})
		in := f.input()
		in.SplitChargeCode = outside(f)
		_, err := f.q.Query(t.Context(), in)
		wantResourceNotFound(t, err, usageattribution.ResourceCharge)
	})

	t.Run("fails on a split charge holding too many filters", func(t *testing.T) {
		t.Log("source:", querySpec+"544")
		f := newQueryFixture(t, queryOptions{tokensCharge: func(c *catalog.Charge) {
			for range 11 {
				c.Filters = append(c.Filters, billingtest.ChargeFilter(nil, `{}`))
			}
		}})
		in := f.input()
		in.SplitChargeCode = f.tokens.Code
		_, err := f.q.Query(t.Context(), in)
		wantValidation(t, err, "split_charge_code: too_many_filters")
	})
}

func TestQueryPricing(t *testing.T) {
	// The opus filter at 1 of query_service_spec.rb:318-322.
	opus := func(c *catalog.Charge) {
		c.Filters = append(c.Filters, billingtest.ChargeFilter(map[string][]string{"model": {"opus"}}, `{"amount":"1"}`))
	}
	// The opus filter and a more specific one on opus in eu at 3
	// (query_service_spec.rb:328-336).
	specific := queryOptions{
		tokensMetric: func(m *catalog.BillableMetric) {
			opusFilter(m)
			m.Filters = append(m.Filters, catalog.MetricFilter{Key: "region", Values: values("eu", "us")})
		},
		tokensCharge: func(c *catalog.Charge) {
			opus(c)
			c.Filters = append(c.Filters, billingtest.ChargeFilter(map[string][]string{"model": {"opus"}, "region": {"eu"}}, `{"amount":"3"}`))
		},
	}
	withSpecific := func(t *testing.T) *queryFixture {
		f := newQueryFixture(t, specific)
		f.event("tokens", "10", map[string]string{"team": "eng"}, map[string]string{"model": "opus", "region": "eu"})
		return f
	}
	filtered := queryOptions{tokensMetric: opusFilter, tokensCharge: opus}

	t.Run("prices each event with its matching filter", func(t *testing.T) {
		t.Log("source:", querySpec+"324")
		f := newQueryFixture(t, filtered)
		wantStrings(t, "rows", summary(f.run(t, f.input()).Rows), values("data 200000 1", "eng 125200 3"))
	})

	t.Run("prices the events matching a more specific filter with its amount", func(t *testing.T) {
		t.Log("source:", querySpec+"338")
		f := withSpecific(t)
		wantStrings(t, "rows", summary(f.run(t, f.input()).Rows), values("data 200000 1", "eng 128200 4"))
	})

	t.Run("lists the filters of a split charge in their order, the default last", func(t *testing.T) {
		t.Log("source:", querySpec+"345")
		f := withSpecific(t)
		in := f.input()
		in.SplitChargeCode = f.tokens.Code
		var got []string
		for _, c := range f.run(t, in).Totals.Cells {
			if c.Charge.ID == f.tokens.ID {
				got = append(got, strings.Fields(f.cell(c))[1])
			}
		}
		wantStrings(t, "split filters", got, values("0", "1", "nil"))
	})

	t.Run("returns the charge in units only when the price lookup is too large", func(t *testing.T) {
		t.Log("source:", querySpec+"354")
		f := newQueryFixture(t, filtered)
		f.q.SetLimits(usageattribution.MaxGroups, 0)
		res := f.run(t, f.input())
		wantStrings(t, "rows", summary(res.Rows), values("eng 200 3", "data 0 1"))
		wantStrings(t, "cells", f.cellsOf(res.Rows[0].Cells), values("requests nil 1 200 1", "tokens nil 1500 nil 2"))
	})

	t.Run("returns one cell per filter and one for the default bucket of a split charge", func(t *testing.T) {
		t.Log("source:", querySpec+"368")
		f := newQueryFixture(t, filtered)
		in := f.input()
		in.SplitChargeCode = f.tokens.Code
		res := f.run(t, in)
		wantStrings(t, "cells", f.cellsOf(res.Rows[1].Cells),
			values("requests nil 1 200 1", "tokens nil 500 25000 1", "tokens 0 1000 100000 1"))
	})

	t.Run("converts the amount of a charge priced in a pricing unit", func(t *testing.T) {
		t.Log("source:", querySpec+"383")
		f := newQueryFixture(t, queryOptions{tokensCharge: func(c *catalog.Charge) {
			c.AppliedPricingUnit = &catalog.AppliedPricingUnit{
				PricingUnitID: core.NewID[core.PricingUnitID](core.UUIDv7Generator{}), ConversionRate: money.NewFromInt(2),
			}
		}})
		res := f.run(t, f.input())
		wantStrings(t, "rows", summary(res.Rows), values("data 200000 1", "eng 150200 3"))
		wantStrings(t, "unattributed", values(aggregateOf(res.Unattributed)), values("10000 1"))
	})

	t.Run("returns the units without an amount of a non linear charge", func(t *testing.T) {
		t.Log("source:", querySpec+"392")
		f := newQueryFixture(t, queryOptions{tokensCharge: func(c *catalog.Charge) {
			c.ChargeModel = catalog.ChargeModelGraduated
			c.Properties = catalog.MustProperties(`{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0.5","flat_amount":"0"}]}`)
		}})
		res := f.run(t, f.input())
		wantStrings(t, "rows", summary(res.Rows), values("eng 200 3", "data 0 1"))
		wantStrings(t, "cells", f.cellsOf(res.Rows[0].Cells), values("requests nil 1 200 1", "tokens nil 1500 nil 2"))
	})

	t.Run("prices each event with its filter among thousands", func(t *testing.T) {
		t.Log("source:", querySpec+"428")
		f := newQueryFixture(t, queryOptions{tokensMetric: opusFilter, tokensCharge: func(c *catalog.Charge) {
			for i := range 3000 {
				v := "opus"
				if i > 0 {
					v = fmt.Sprintf("model-%d", i)
				}
				cf := billingtest.ChargeFilter(map[string][]string{"model": {v}}, `{"amount":"0.1"}`)
				cf.CreatedAt = queryNow
				c.Filters = append(c.Filters, cf)
			}
		}})
		res := f.run(t, f.input())
		wantStrings(t, "rows", summary(res.Rows), values("eng 35200 3", "data 20000 1"))
		wantStrings(t, "unattributed", values(aggregateOf(res.Unattributed)), values("5000 1"))
	})
}

func TestQuerySupportedCharges(t *testing.T) {
	maxMetric := func(m *catalog.BillableMetric) {
		m.AggregationType, m.FieldName = catalog.AggregationTypeMax, new(m.Code)
	}

	t.Run("leaves a charge on an unsupported aggregation out", func(t *testing.T) {
		t.Log("source:", querySpec+"437")
		f := newQueryFixture(t, queryOptions{requestsMetric: maxMetric})
		for _, r := range f.run(t, f.input()).Rows {
			for _, c := range r.Cells {
				if c.Charge.ID != f.tokens.ID {
					t.Errorf("cell charge got = %s, want = %s", c.Charge.ID, f.tokens.ID)
				}
			}
		}
	})

	t.Run("returns no rows without any supported charge", func(t *testing.T) {
		t.Log("source:", querySpec+"446")
		f := newQueryFixture(t, queryOptions{tokensMetric: maxMetric, requestsMetric: maxMetric})
		res := f.run(t, f.input())
		wantStrings(t, "rows", summary(res.Rows), values())
		wantStrings(t, "totals", values(aggregateOf(res.Totals)), values("0 0"))
		if res.Groups != 0 {
			t.Errorf("groups got = %d, want = 0", res.Groups)
		}
	})

	t.Run("counts duplicated events once", func(t *testing.T) {
		t.Log("source:", querySpec+"464")
		f := newQueryFixture(t, queryOptions{})
		for range 2 {
			f.eventAt(eventAt, "tr_dup", "tokens", "50", map[string]string{"team": "ops"}, nil)
		}
		res := f.run(t, f.input())
		i := slices.IndexFunc(res.Rows, func(r usageattribution.Row) bool { return r.Value == "ops" })
		if i < 0 || res.Rows[i].Events != 1 {
			t.Errorf("ops rows got = %+v, want = one event", res.Rows)
		}
	})
}

func TestQueryFailures(t *testing.T) {
	t.Run("fails when the query exceeds the groups limit", func(t *testing.T) {
		t.Log("source:", querySpec+"472")
		f := newQueryFixture(t, queryOptions{})
		f.q.SetLimits(1, usageattribution.MaxLookupEntries)
		_, err := f.q.Query(t.Context(), f.input())
		if re, ok := errors.AsType[*core.RuleError](err); !ok || re.Code != usageattribution.CodeTooManyGroups {
			t.Errorf("error got = %v, want = %s", err, usageattribution.CodeTooManyGroups)
		}
	})

	t.Run("fails when the organization's events are not in the ClickHouse store", func(t *testing.T) {
		t.Log("source:", querySpec+"489")
		f := newQueryFixture(t, queryOptions{})
		f.reader.unavailable = true
		if _, err := f.q.Query(t.Context(), f.input()); !errors.Is(err, usageattribution.ErrUnavailable) {
			t.Errorf("error got = %v, want = %v", err, usageattribution.ErrUnavailable)
		}
	})

	// The API reads the customer's active subscriptions only
	// (attributed_usage_controller.rb:52): a pending or terminated one is
	// not found there.
	for _, tt := range []struct {
		source string
		name   string
		sub    func(*subscription.Subscription)
		in     func(*usageattribution.QueryInput)
	}{
		{source: querySpec + "499", name: "fails without subscription", in: func(in *usageattribution.QueryInput) { in.SubscriptionExternalID = "unknown" }},
		{source: querySpec + "507", name: "fails on a pending subscription", sub: func(s *subscription.Subscription) {
			*s = *subscription.New(s.OrganizationID, s.ID, s.CustomerID, s.PlanID, s.ExternalID, queryNow.AddDate(0, 1, 0), subscription.PlanFacts{}, queryNow)
		}},
		{source: querySpec + "518", name: "fails on a terminated subscription", sub: func(s *subscription.Subscription) {
			if err := s.Activate(startedAt, queryNow); err != nil {
				panic(err)
			}
			if err := s.Terminate(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)); err != nil {
				panic(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newQueryFixture(t, queryOptions{subscription: tt.sub})
			in := f.input()
			if tt.in != nil {
				tt.in(&in)
			}
			_, err := f.q.Query(t.Context(), in)
			wantResourceNotFound(t, err, usageattribution.ResourceSubscription)
		})
	}

	for _, tt := range []struct {
		source string
		name   string
		in     func(*usageattribution.QueryInput)
	}{
		{querySpec + "526", "fails on an unknown group_by type", func(in *usageattribution.QueryInput) { in.GroupBy = "department" }},
		{querySpec + "534", "fails on an unknown filter type", func(in *usageattribution.QueryInput) {
			in.Filters = map[string][]string{"department": {"rnd"}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newQueryFixture(t, queryOptions{})
			in := f.input()
			tt.in(&in)
			_, err := f.q.Query(t.Context(), in)
			wantResourceNotFound(t, err, usageattribution.ResourceType)
		})
	}
}

func TestQueryValidation(t *testing.T) {
	date := func(m time.Month, d int) *time.Time { return new(time.Date(2026, m, d, 0, 0, 0, 0, time.UTC)) }
	tests := []struct {
		source string
		name   string
		in     func(f *queryFixture, in *usageattribution.QueryInput)
		want   []string
	}{
		{
			// The order_by parameter is not part of the API (the basis
			// orders), and the page size is checked first, as the
			// controller does (attributed_usage_controller.rb:55-62).
			source: querySpec + "568", name: "fails on invalid parameters",
			in: func(f *queryFixture, in *usageattribution.QueryInput) {
				var teams []string
				for i := range 21 {
					teams = append(teams, fmt.Sprintf("team-%d", i))
				}
				in.Filters = map[string][]string{"team": teams, "model": {}}
				in.From, in.To = date(9, 1), date(10, 15)
				in.SplitChargeCode = f.requests.Code
				in.Search = strings.Repeat("a", 101)
				in.Basis = "euros"
				in.Offset = -1
			},
			want: values(
				"filters: values_are_required", "filters: too_many_values", "to_time: window_too_long",
				"page_token: value_is_out_of_range", "search: value_is_too_long", "basis: value_is_invalid",
				"split_charge_code: must_have_filters",
			),
		},
		{
			source: querySpec + "568", name: "fails on a page size out of range",
			in:   func(_ *queryFixture, in *usageattribution.QueryInput) { in.Limit = 101 },
			want: values("page_size: value_is_out_of_range"),
		},
		{
			source: querySpec + "587", name: "fails on too many flat filters",
			in: func(f *queryFixture, in *usageattribution.QueryInput) {
				in.Filters = map[string][]string{}
				for _, code := range values("model", "api_key", "endpoint", "agent") {
					if code != "model" {
						f.create(t, code, attribution.RoleFlat, none)
					}
					in.Filters[code] = values("value")
				}
			},
			want: values("filters: too_many_flat_filters"),
		},
		{
			source: querySpec + "596", name: "fails on an inverted window",
			in:   func(_ *queryFixture, in *usageattribution.QueryInput) { in.From, in.To = date(9, 10), date(9, 1) },
			want: values("to_time: invalid_date_range"),
		},
		{
			source: querySpec + "605", name: "fails on a window ending before the subscription start",
			in:   func(_ *queryFixture, in *usageattribution.QueryInput) { in.From, in.To = date(7, 1), date(7, 20) },
			want: values("to_time: invalid_date_range"),
		},
		{
			source: querySpec + "613", name: "fails on a single window boundary",
			in:   func(_ *queryFixture, in *usageattribution.QueryInput) { in.From = date(9, 10) },
			want: values("to_time: both_boundaries_are_required"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newQueryFixture(t, queryOptions{})
			in := f.input()
			tt.in(f, &in)
			_, err := f.q.Query(t.Context(), in)
			wantValidation(t, err, tt.want...)
		})
	}
}
