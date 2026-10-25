package rpc_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	attributionv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/attribution/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/attribution/v1/attributionv1connect"
)

const attributedSpec = "spec/requests/api/v1/customers/attributed_usage_controller_spec.rb:"

// attributedFixture is the setup of attributed_usage_controller_spec.rb:8-51:
// a monthly EUR plan with the tokens (0.5) and requests (2) charges, a
// subscription started on August 1st, the types team and model (flat),
// and five labeled events on September 10th, the clock on September 15th.
type attributedFixture struct {
	s        *rpctest.Server
	events   *attributedReader
	customer *customer.Customer
	sub      *subscription.Subscription
	tokens   *catalog.Charge
	requests *catalog.Charge
	tx       int
}

// attributedReader is the event store of the fixture, which may fail or
// hold no labels.
type attributedReader struct {
	*billingtest.Events
	unavailable bool
	err         error
}

func (r *attributedReader) AttributedUsageAvailable(org string) bool {
	return !r.unavailable && r.Events.AttributedUsageAvailable(org)
}

func (r *attributedReader) AttributedUsage(ctx context.Context, q metering.AttributedQuery) (metering.AttributedUsage, error) {
	if r.err != nil {
		return metering.AttributedUsage{}, r.err
	}
	return r.Events.AttributedUsage(ctx, q)
}

// newAttributedFixture returns the fixture, metric changing the tokens
// metric and charge the tokens charge when set.
func newAttributedFixture(t *testing.T, metric func(*catalog.BillableMetric), charge func(*catalog.Charge)) *attributedFixture {
	t.Helper()
	f := &attributedFixture{events: &attributedReader{Events: billingtest.NewEvents()}}
	f.s = rpctest.New(t, func(o *app.APIOptions) { o.AttributedUsage = f.events })
	h := f.s.Fixtures
	f.s.Clock.Set(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	for _, ty := range []struct {
		code string
		role attribution.Role
	}{{"team", attribution.RoleHierarchical}, {"model", attribution.RoleFlat}} {
		typ := &attribution.Type{
			ID: core.NewID[core.UsageAttributionTypeID](h.IDs), OrganizationID: h.OrgID(), Code: ty.code, Name: new(ty.code),
			AttributionKeys: []string{ty.code + "_id"}, Role: ty.role, CreatedAt: h.Now(), UpdatedAt: h.Now(),
		}
		h.Do(func(ctx context.Context, r port.Repos) error { return r.UsageAttributionTypes.Create(ctx, typ) })
	}
	f.customer = h.Customer()
	plan := h.Plan(period.Monthly, 0, func(p *catalog.Plan) { p.Currency = money.MustCurrency("EUR") })
	tokens := h.Metric("tokens", catalog.AggregationTypeSum, "tokens", func(m *catalog.BillableMetric) {
		if metric != nil {
			metric(m)
		}
	})
	requests := h.Metric("requests", catalog.AggregationTypeCount, "")
	f.tokens = h.Charge(plan, tokens, catalog.ChargeModelStandard, `{"amount":"0.5"}`, func(c *catalog.Charge) {
		c.Code = "tokens"
		if charge != nil {
			charge(c)
		}
	})
	f.requests = h.Charge(plan, requests, catalog.ChargeModelStandard, `{"amount":"2"}`, func(c *catalog.Charge) { c.Code = "requests" })
	f.sub = h.Subscription(f.customer, plan, billingtest.SubscribedAt(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
	f.event("tokens", "1000", map[string]string{"team": "eng", "model": "opus"}, map[string]string{"model": "opus"})
	f.event("tokens", "500", map[string]string{"team": "eng", "model": "sonnet"}, map[string]string{"model": "sonnet"})
	f.event("tokens", "2000", map[string]string{"team": "data", "model": "opus"}, map[string]string{"model": "opus"})
	f.event("requests", "1", map[string]string{"team": "eng"}, nil)
	f.event("tokens", "100", nil, nil)
	return f
}

func (f *attributedFixture) event(code, value string, labels, props map[string]string) {
	f.tx++
	f.events.Add(metering.Event{
		OrganizationID: f.s.Org.ID.String(), SubscriptionID: f.sub.ExternalID, Code: code, TransactionID: fmt.Sprintf("tx-%d", f.tx),
		Timestamp: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), Properties: props, Value: &value, AttributionLabels: labels,
	})
}

// request is the request of attributed_usage_controller_spec.rb:57.
func (f *attributedFixture) request() *attributionv1.GetAttributedUsageRequest {
	return attributionv1.GetAttributedUsageRequest_builder{
		CustomerId: new("external_id:" + f.customer.ExternalID), SubscriptionId: new("external_id:" + f.sub.ExternalID),
		GroupBy: new("team"),
	}.Build()
}

func (f *attributedFixture) get(t *testing.T, c rpctest.Credential, req *attributionv1.GetAttributedUsageRequest) (*attributionv1.GetAttributedUsageResponse, error) {
	t.Helper()
	return attributionv1connect.NewUsageAttributionServiceClient(f.s.Connect(c)).GetAttributedUsage(t.Context(), req)
}

func (f *attributedFixture) mustGet(t *testing.T, req *attributionv1.GetAttributedUsageRequest) *attributionv1.GetAttributedUsageResponse {
	t.Helper()
	res, err := f.get(t, f.s.APIKey, req)
	if err != nil {
		t.Fatalf("GetAttributedUsage() error = %v", err)
	}
	return res
}

func attributedRows(res *attributionv1.GetAttributedUsageResponse, fn func(*attributionv1.AttributedUsageRow) string) []string {
	out := []string{}
	for _, r := range res.GetRows() {
		out = append(out, fn(r))
	}
	return out
}

func wantError(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("code got = %v (%v), want = %v", got, err, code)
	}
	if got := rpc.ErrorReason(err); got != reason {
		t.Errorf("reason got = %q, want = %q", got, reason)
	}
}

// restAttributedCell is the JSON of an AttributedChargeUsage.
type restAttributedCell struct {
	ChargeID           string           `json:"charge_id"`
	ChargeCode         string           `json:"charge_code"`
	MetricCode         string           `json:"metric_code"`
	ChargeFilterID     string           `json:"charge_filter_id"`
	ChargeFilterValues map[string]any   `json:"charge_filter_values"`
	Units              string           `json:"units"`
	Amount             *json.RawMessage `json:"amount"`
	PreciseAmount      *string          `json:"precise_amount"`
	EventsCount        json.Number      `json:"events_count"`
}

type restAttributedAggregate struct {
	Amount       *json.RawMessage     `json:"amount"`
	EventsCount  json.Number          `json:"events_count"`
	ChargesUsage []restAttributedCell `json:"charges_usage"`
}

type restAttributedRow struct {
	Value string                  `json:"value"`
	Rank  int                     `json:"rank"`
	Usage restAttributedAggregate `json:"usage"`
}

type restAttributedUsage struct {
	SubscriptionID         string                    `json:"subscription_id"`
	ExternalSubscriptionID string                    `json:"external_subscription_id"`
	GroupBy                string                    `json:"group_by"`
	Basis                  string                    `json:"basis"`
	Period                 struct{ From, To string } `json:"period"`
	Currency               string                    `json:"currency"`
	Rows                   []restAttributedRow       `json:"rows"`
	Unattributed           restAttributedAggregate   `json:"unattributed"`
	Totals                 restAttributedAggregate   `json:"totals"`
	NextPageToken          string                    `json:"next_page_token"`
	TotalSize              json.Number               `json:"total_size"`
}

func TestGetAttributedUsageREST(t *testing.T) {
	t.Parallel()
	t.Log("source:", attributedSpec+"61")
	f := newAttributedFixture(t, nil, nil)
	path := "/v1/customers/" + url.PathEscape("external_id:"+f.customer.ExternalID) +
		"/subscriptions/" + url.PathEscape("external_id:"+f.sub.ExternalID) + "/usage:attributed?group_by=team"
	status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, path, nil)
	if status != http.StatusOK {
		t.Fatalf("status got = %d (%s), want = 200", status, body)
	}
	u := decode[restAttributedUsage](t, body)
	// The period is half-open: it ends at the start of October.
	if u.SubscriptionID != f.sub.ID.String() || u.ExternalSubscriptionID != f.sub.ExternalID || u.GroupBy != "team" ||
		u.Basis != "ATTRIBUTION_BASIS_UNITS" || u.Currency != "EUR" ||
		u.Period.From != "2026-09-01T00:00:00Z" || u.Period.To != "2026-10-01T00:00:00Z" {
		t.Errorf("attributed usage got = %+v, want = the units of team in EUR over September", u)
	}
	var rows []string
	for _, r := range u.Rows {
		rows = append(rows, fmt.Sprintf("%s %d %s %t", r.Value, r.Rank, r.Usage.EventsCount, r.Usage.Amount != nil))
	}
	if want := []string{"eng 1 3 false", "data 2 1 false"}; !slices.Equal(rows, want) {
		t.Errorf("rows got = %q, want = %q", rows, want)
	}
	var cells []string
	for _, c := range u.Rows[0].Usage.ChargesUsage {
		cells = append(cells, fmt.Sprintf("%s %s %s %s %q %v %s %t %t", c.ChargeID, c.ChargeCode, c.MetricCode, c.Units, c.ChargeFilterID,
			c.ChargeFilterValues, c.EventsCount, c.Amount != nil, c.PreciseAmount != nil))
	}
	want := []string{
		f.tokens.ID.String() + ` tokens tokens 1500 "" map[] 2 false false`,
		f.requests.ID.String() + ` requests requests 1 "" map[] 1 false false`,
	}
	if !slices.Equal(cells, want) {
		t.Errorf("cells got = %q, want = %q", cells, want)
	}
	if u.Unattributed.EventsCount != "1" || u.Totals.EventsCount != "5" || u.TotalSize != "2" || u.NextPageToken != "" {
		t.Errorf("unattributed, totals and page got = %s, %s, %s, %q, want = 1, 5, 2 values, last page",
			u.Unattributed.EventsCount, u.Totals.EventsCount, u.TotalSize, u.NextPageToken)
	}
}

func TestGetAttributedUsage(t *testing.T) {
	t.Parallel()

	t.Run("returns the amounts of the selected charges", func(t *testing.T) {
		t.Parallel()
		t.Log("source:", attributedSpec+"118")
		f := newAttributedFixture(t, nil, nil)
		req := f.request()
		req.SetBasis(attributionv1.AttributionBasis_ATTRIBUTION_BASIS_AMOUNT)
		req.SetChargeCodes([]string{"tokens"})
		res := f.mustGet(t, req)
		got := attributedRows(res, func(r *attributionv1.AttributedUsageRow) string {
			codes := []string{}
			for _, c := range r.GetUsage().GetChargesUsage() {
				codes = append(codes, c.GetChargeCode())
			}
			return fmt.Sprintf("%s %s %s %v", r.GetValue(), r.GetUsage().GetAmount().GetAmount(), r.GetUsage().GetPreciseAmount(), codes)
		})
		if want := []string{"data 1000.00 100000 [tokens]", "eng 750.00 75000 [tokens]"}; !slices.Equal(got, want) {
			t.Errorf("rows got = %q, want = %q", got, want)
		}
		if got := res.GetTotals().GetAmount().GetAmount(); got != "1800.00" {
			t.Errorf("totals amount got = %s, want = 1800.00", got)
		}
	})

	t.Run("returns one cell per filter and one for the default price of a split charge", func(t *testing.T) {
		t.Parallel()
		t.Log("source:", attributedSpec+"147")
		f := newAttributedFixture(t, func(m *catalog.BillableMetric) {
			m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](core.UUIDv7Generator{}), Key: "model", Values: []string{"opus", "sonnet"}}}
		}, func(c *catalog.Charge) {
			cf := billingtest.ChargeFilter(map[string][]string{"model": {"opus"}}, `{"amount":"1"}`)
			cf.InvoiceDisplayName = new("Opus")
			c.Filters = append(c.Filters, cf)
		})
		req := f.request()
		req.SetChargeCodes([]string{"tokens"})
		req.SetSplitChargeCode("tokens")
		res := f.mustGet(t, req)
		i := slices.IndexFunc(res.GetRows(), func(r *attributionv1.AttributedUsageRow) bool { return r.GetValue() == "eng" })
		if i < 0 {
			t.Fatalf("rows got = %v, want = an eng row", res.GetRows())
		}
		var got []string
		for _, c := range res.GetRows()[i].GetUsage().GetChargesUsage() {
			got = append(got, fmt.Sprintf("%q %v %q %s", c.GetChargeFilterId(), c.GetChargeFilterValues()["model"].GetValues(), c.GetChargeFilterInvoiceDisplayName(), c.GetUnits()))
		}
		want := []string{fmt.Sprintf("%q [opus] \"Opus\" 1000", f.tokens.Filters[0].ID), `"" [] "" 500`}
		if !slices.Equal(got, want) {
			t.Errorf("cells got = %q, want = %q", got, want)
		}
	})

	t.Run("narrows the usage to the matching labels", func(t *testing.T) {
		t.Parallel()
		t.Log("source:", attributedSpec+"165")
		f := newAttributedFixture(t, nil, nil)
		req := f.request()
		req.SetFilters(map[string]*attributionv1.Values{"model": attributionv1.Values_builder{Values: []string{"opus", "haiku"}}.Build()})
		got := attributedRows(f.mustGet(t, req), func(r *attributionv1.AttributedUsageRow) string {
			return fmt.Sprintf("%s %d", r.GetValue(), r.GetUsage().GetEventsCount())
		})
		if want := []string{"data 1", "eng 1"}; !slices.Equal(got, want) {
			t.Errorf("rows got = %q, want = %q", got, want)
		}
	})

	t.Run("reads a custom window", func(t *testing.T) {
		t.Parallel()
		t.Log("source:", attributedSpec+"184")
		f := newAttributedFixture(t, nil, nil)
		req := f.request()
		from, to := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
		req.SetFromTime(timestamppb.New(from))
		req.SetToTime(timestamppb.New(to))
		res := f.mustGet(t, req)
		if !res.GetPeriod().GetFrom().AsTime().Equal(from) || !res.GetPeriod().GetTo().AsTime().Equal(to) {
			t.Errorf("period got = %v, want = %v - %v", res.GetPeriod(), from, to)
		}
		if len(res.GetRows()) != 0 || res.GetTotalSize() != 0 || res.GetNextPageToken() != "" {
			t.Errorf("rows got = %v (%d values), want = none", res.GetRows(), res.GetTotalSize())
		}
	})

	t.Run("returns the requested page", func(t *testing.T) {
		t.Parallel()
		t.Log("source:", attributedSpec+"196")
		f := newAttributedFixture(t, nil, nil)
		req := f.request()
		req.SetPageSize(1)
		first := f.mustGet(t, req)
		if first.GetNextPageToken() == "" {
			t.Fatal("next page token got = \"\", want = a token")
		}
		req.SetPageToken(first.GetNextPageToken())
		res := f.mustGet(t, req)
		got := attributedRows(res, func(r *attributionv1.AttributedUsageRow) string {
			return fmt.Sprintf("%s %d", r.GetValue(), r.GetRank())
		})
		if want := []string{"data 2"}; !slices.Equal(got, want) || res.GetTotalSize() != 2 || res.GetNextPageToken() != "" {
			t.Errorf("page got = %q (%d values, next %q), want = %q (2 values, last page)", got, res.GetTotalSize(), res.GetNextPageToken(), want)
		}
	})

	for _, tt := range []struct {
		name, source string
		change       func(*attributionv1.GetAttributedUsageRequest)
		code         connect.Code
		reason       string
	}{
		{
			// The dates are timestamps: an invalid one does not decode.
			"returns a validation error on invalid parameters", attributedSpec + "209",
			func(r *attributionv1.GetAttributedUsageRequest) { r.SetPageSize(101) },
			connect.CodeInvalidArgument, rpc.ReasonValidationErrors,
		},
		{
			"returns the query's validation error", attributedSpec + "224",
			func(r *attributionv1.GetAttributedUsageRequest) { r.SetBasis(attributionv1.AttributionBasis(9)) },
			connect.CodeInvalidArgument, "",
		},
		{
			"returns a not found error on an unknown charge code", attributedSpec + "249",
			func(r *attributionv1.GetAttributedUsageRequest) { r.SetChargeCodes([]string{"tokens", "unknown"}) },
			connect.CodeNotFound, "charge_not_found",
		},
		{
			"returns a not found error on an unknown split charge code", attributedSpec + "259",
			func(r *attributionv1.GetAttributedUsageRequest) { r.SetSplitChargeCode("unknown") },
			connect.CodeNotFound, "charge_not_found",
		},
		{
			"returns a not found error on an unknown customer", attributedSpec + "269",
			func(r *attributionv1.GetAttributedUsageRequest) { r.SetCustomerId("external_id:unknown") },
			connect.CodeNotFound, "customer_not_found",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Log("source:", tt.source)
			f := newAttributedFixture(t, nil, nil)
			req := f.request()
			tt.change(req)
			_, err := f.get(t, f.s.APIKey, req)
			if tt.reason == "" {
				// protovalidate rejects an undefined basis.
				if got := connect.CodeOf(err); got != tt.code {
					t.Fatalf("code got = %v (%v), want = %v", got, err, tt.code)
				}
				return
			}
			wantError(t, err, tt.code, tt.reason)
		})
	}

	t.Run("returns a not found error on a terminated subscription", func(t *testing.T) {
		t.Parallel()
		t.Log("source:", attributedSpec+"282")
		f := newAttributedFixture(t, nil, nil)
		f.s.Fixtures.UpdateSubscription(f.sub, func(s *subscription.Subscription) {
			if err := s.Terminate(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
		})
		_, err := f.get(t, f.s.APIKey, f.request())
		wantError(t, err, connect.CodeNotFound, "subscription_not_found")
	})

	t.Run("asks to narrow a query past the limits", func(t *testing.T) {
		t.Parallel()
		t.Log("source:", attributedSpec+"238")
		f := newAttributedFixture(t, nil, nil)
		f.events.err = fmt.Errorf("%w: TOO_MANY_ROWS", metering.ErrAttributedTooManyGroups)
		_, err := f.get(t, f.s.APIKey, f.request())
		wantError(t, err, connect.CodeFailedPrecondition, "too_many_groups")
	})
}

// TestGetAttributedUsageUnavailable checks an organization whose events
// are not in ClickHouse is denied with feature_unavailable (lago's answer
// to an organization without the feature).
func TestGetAttributedUsageUnavailable(t *testing.T) {
	t.Parallel()
	f := newAttributedFixture(t, nil, nil)
	f.events.unavailable = true
	_, err := f.get(t, f.s.APIKey, f.request())
	wantError(t, err, connect.CodePermissionDenied, rpc.ReasonFeatureUnavailable)
}

// TestGetAttributedUsagePermissions ports the "requires API permission"
// examples of the endpoint.
func TestGetAttributedUsagePermissions(t *testing.T) {
	t.Parallel()
	t.Log("source:", attributedSpec+"59")
	f := newAttributedFixture(t, nil, nil)
	d := auth.Deps{Tx: f.s.Store, Clock: f.s.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader}
	keyWith := func(resource permission.APIResource, level permission.Level) rpctest.Credential {
		k, err := auth.NewAPIKeys(d, nil).Create(t.Context(), f.s.Org.ID, nil, map[permission.APIResource]permission.Level{resource: level})
		if err != nil {
			t.Fatal(err)
		}
		return rpctest.Credential{Token: k.Secret}
	}
	if _, err := f.get(t, keyWith(permission.APIResourceAttributedUsage, permission.LevelRead), f.request()); err != nil {
		t.Errorf("attributed usage read key got = %v, want = nil", err)
	}
	_, err := f.get(t, keyWith(permission.APIResourceCustomerUsage, permission.LevelRead), f.request())
	wantError(t, err, connect.CodePermissionDenied, "read_action_not_allowed_for_attributed_usage")
}
