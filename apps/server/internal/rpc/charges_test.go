package rpc_test

import (
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// restChargeFilter is the JSON of a charge filter in a REST response.
type restChargeFilter struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Values map[string]struct {
		Values []string `json:"values"`
	} `json:"values"`
	Properties         map[string]any `json:"properties"`
	InvoiceDisplayName string         `json:"invoice_display_name"`
}

// restCharge is the JSON of a charge in a REST response.
type restCharge struct {
	ID                 string             `json:"id"`
	Code               string             `json:"code"`
	PlanID             string             `json:"plan_id"`
	ParentID           string             `json:"parent_id"`
	BillableMetricID   string             `json:"billable_metric_id"`
	ChargeModel        string             `json:"charge_model"`
	Properties         map[string]any     `json:"properties"`
	Filters            []restChargeFilter `json:"filters"`
	MinAmount          string             `json:"min_amount"`
	InvoiceDisplayName string             `json:"invoice_display_name"`
	TaxIDs             []string           `json:"tax_ids"`
	AcceptsTarget      bool               `json:"accepts_target_wallet"`
}

type restChargeBody struct {
	Charge restCharge `json:"charge"`
}

type restChargesBody struct {
	Charges       []restCharge `json:"charges"`
	NextPageToken string       `json:"next_page_token"`
}

type restChargeFilterBody struct {
	Filter restChargeFilter `json:"filter"`
}

type restChargeFiltersBody struct {
	Filters []restChargeFilter `json:"filters"`
}

// chargeFixture is a USD plan with a standard charge "api" on the metric
// "api" with a filter on region eu, and a subscription to it.
type chargeFixture struct {
	s      *rpctest.Server
	plan   *catalog.Plan
	metric *catalog.BillableMetric
	charge *catalog.Charge
	sub    *subscription.Subscription
}

func newChargeFixture(t *testing.T) chargeFixture {
	t.Helper()
	s := rpctest.New(t)
	f := s.Fixtures
	p := f.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.Code = "basic" })
	m := f.Metric("api", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](f.IDs), Key: "region", Values: []string{"eu", "us"}}}
	})
	c := f.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`, func(c *catalog.Charge) {
		c.Code = "api"
		c.MinAmount = 1250
		c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"5"}`)}
		c.Filters[0].Code = "eu"
	})
	sub := f.Subscription(f.Customer(), p)
	return chargeFixture{s: s, plan: p, metric: m, charge: c, sub: sub}
}

func standardAmount(t *testing.T, props map[string]any) any {
	t.Helper()
	std, _ := props["standard"].(map[string]any)
	return std["amount"]
}

func TestPlanCharges(t *testing.T) {
	t.Parallel()

	// source: spec/requests/api/v1/plans/charges_controller_spec.rb:10, 49, 83
	t.Run("list and get", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		child := f.s.Fixtures.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.ParentID, p.Code = f.plan.ID, f.plan.Code })
		f.s.Fixtures.Charge(child, f.metric, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.ParentID, c.Code = f.charge.ID, "api" })
		status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/charges", nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		list := decode[restChargesBody](t, body).Charges
		if len(list) != 1 || list[0].ID != f.charge.ID.String() {
			t.Fatalf("got charges = %+v, want = [%s]", list, f.charge.ID)
		}
		status, body = f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/charges/code%3Aapi", nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := decode[restChargeBody](t, body).Charge
		if got.ChargeModel != "CHARGE_MODEL_STANDARD" || got.MinAmount != "12.50" || standardAmount(t, got.Properties) != "10" ||
			got.BillableMetricID != f.metric.ID.String() || len(got.Filters) != 1 || got.Filters[0].Code != "eu" {
			t.Errorf("got %+v, want the stored charge", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		tests := []struct {
			name, source, path string
		}{
			{"plan", "spec/requests/api/v1/plans/charges_controller_spec.rb:98", "/v1/plans/code%3Aunknown/charges/code%3Aapi"},
			{"charge", "spec/requests/api/v1/plans/charges_controller_spec.rb:108", "/v1/plans/code%3Abasic/charges/code%3Aunknown"},
			{"plan list", "spec/requests/api/v1/plans/charges_controller_spec.rb:39", "/v1/plans/code%3Aunknown/charges"},
		}
		for _, tt := range tests {
			t.Logf("source: %s", tt.source)
			if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, tt.path, nil); status != http.StatusNotFound {
				t.Errorf("%s: got status = %d (%s), want = 404", tt.name, status, body)
			}
		}
	})

	t.Run("create", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name   string
			source string
			body   string
			status int
		}{
			{
				"creates", "spec/requests/api/v1/plans/charges_controller_spec.rb:132",
				`{"code":"api_2","billable_metric_id":"code:api","charge_model":"CHARGE_MODEL_GRADUATED",
				"properties":{"graduated":{"ranges":[{"from_value":"0","to_value":"10","per_unit_amount":"1","flat_amount":"0"},
				{"from_value":"10","per_unit_amount":"0.5","flat_amount":"2"}]}},"min_amount":"3","tax_ids":["code:vat"]}`, http.StatusOK,
			},
			{
				"with filters", "spec/requests/api/v1/plans/charges_controller_spec.rb:168",
				`{"code":"api_2","billable_metric_id":"code:api","charge_model":"CHARGE_MODEL_STANDARD","properties":{"standard":{"amount":"1"}},
				"filters":[{"values":{"region":{"values":["us"]}},"properties":{"standard":{"amount":"2"},
				"grouping":{"presentation_group_keys":[{"value":"tier"}]}}}]}`, http.StatusOK,
			},
			{
				"metric not found", "spec/requests/api/v1/plans/charges_controller_spec.rb:152",
				`{"code":"api_2","billable_metric_id":"code:unknown","charge_model":"CHARGE_MODEL_STANDARD"}`, http.StatusNotFound,
			},
			{
				"used code", "spec/services/charges/create_service_spec.rb:33",
				`{"code":"api","billable_metric_id":"code:api","charge_model":"CHARGE_MODEL_STANDARD"}`, http.StatusBadRequest,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				t.Logf("source: %s", tt.source)
				f := newChargeFixture(t)
				vat := f.s.Fixtures.Tax("vat", "20")
				status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/plans/code%3Abasic/charges", tt.body)
				if status != tt.status {
					t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
				}
				if status != http.StatusOK {
					return
				}
				got := decode[restChargeBody](t, body).Charge
				if got.Code != "api_2" || got.PlanID != f.plan.ID.String() {
					t.Errorf("got %+v, want the created charge", got)
				}
				switch tt.name {
				case "creates":
					grad, _ := got.Properties["graduated"].(map[string]any)
					ranges, _ := grad["ranges"].([]any)
					if len(ranges) != 2 || got.MinAmount != "3.00" || len(got.TaxIDs) != 1 || got.TaxIDs[0] != vat.ID.String() {
						t.Errorf("got %+v, want two ranges, min 3.00 and the vat", got)
					}
				case "with filters":
					// source: spec/requests/api/v1/plans/charges_controller_spec.rb:195
					if len(got.Filters) != 1 || got.Filters[0].Properties["grouping"] != nil || standardAmount(t, got.Filters[0].Properties) != "2" {
						t.Errorf("got filters = %+v, want one filter without presentation_group_keys", got.Filters)
					}
				}
			})
		}
	})

	// source: spec/requests/api/v1/plans/charges_controller_spec.rb:398, 418
	t.Run("update", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, "/v1/plans/code%3Abasic/charges/code%3Aapi",
			`{"properties":{"standard":{"amount":"20"}},"invoice_display_name":"API calls"}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := decode[restChargeBody](t, body).Charge
		if standardAmount(t, got.Properties) != "20" || got.InvoiceDisplayName != "API calls" || len(got.Filters) != 1 || got.MinAmount != "12.50" {
			t.Errorf("got %+v, want amount 20, the name and the rest kept", got)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, "/v1/plans/code%3Aunknown/charges/code%3Aapi", `{"prorated":true}`); status != http.StatusNotFound {
			t.Errorf("got status = %d (%s), want = 404", status, body)
		}
	})

	// source: spec/requests/api/v1/plans/charges_controller_spec.rb:322, 503
	t.Run("accepts target wallet", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/plans/code%3Abasic/charges",
			`{"code":"wallet_target_charge","billable_metric_id":"code:api","charge_model":"CHARGE_MODEL_STANDARD",
			"properties":{"standard":{"amount":"100"}},"accepts_target_wallet":true}`)
		if status != http.StatusOK {
			t.Fatalf("create: got status = %d (%s), want = 200", status, body)
		}
		if got := decode[restChargeBody](t, body).Charge; !got.AcceptsTarget {
			t.Errorf("create: got accepts_target_wallet = false, want = true")
		}
		status, body = f.s.REST(t, f.s.APIKey, http.MethodPatch, "/v1/plans/code%3Abasic/charges/code%3Aapi",
			`{"properties":{"standard":{"amount":"200"}},"accepts_target_wallet":true}`)
		if status != http.StatusOK {
			t.Fatalf("update: got status = %d (%s), want = 200", status, body)
		}
		if got := decode[restChargeBody](t, body).Charge; !got.AcceptsTarget {
			t.Errorf("update: got accepts_target_wallet = false, want = true")
		}
	})

	// source: spec/requests/api/v1/plans/charges_controller_spec.rb
	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodDelete, "/v1/plans/code%3Abasic/charges/code%3Aapi", nil); status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/charges/code%3Aapi", nil); status != http.StatusNotFound {
			t.Errorf("got status = %d (%s), want = 404", status, body)
		}
	})
}

func TestPlanChargeFilters(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	base := "/v1/plans/code%3Abasic/charges/code%3Aapi/filters"

	// source: spec/requests/api/v1/plans/charges/filters_controller_spec.rb:12
	status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base, nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if list := decode[restChargeFiltersBody](t, body).Filters; len(list) != 1 || list[0].ID != f.charge.Filters[0].ID.String() {
		t.Fatalf("got filters = %+v, want = [%s]", list, f.charge.Filters[0].ID)
	}

	// source: spec/requests/api/v1/plans/charges/filters_controller_spec.rb:72
	status, body = f.s.REST(t, f.s.APIKey, http.MethodGet, base+"/"+f.charge.Filters[0].ID.String(), nil)
	if status != http.StatusOK {
		t.Fatalf("show: got status = %d (%s), want = 200", status, body)
	}
	if got := decode[restChargeFilterBody](t, body).Filter; got.ID != f.charge.Filters[0].ID.String() {
		t.Errorf("show: got filter = %+v, want = %s", got, f.charge.Filters[0].ID)
	}
	// source: spec/requests/api/v1/plans/charges/filters_controller_spec.rb:94, 104
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/charges/code%3Aunknown/filters/"+f.charge.Filters[0].ID.String(), nil); status != http.StatusNotFound {
		t.Errorf("show of an unknown charge: got status = %d (%s), want = 404", status, body)
	}
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base+"/"+core.NewID[core.ChargeFilterID](f.s.Fixtures.IDs).String(), nil); status != http.StatusNotFound {
		t.Errorf("show of an unknown filter: got status = %d (%s), want = 404", status, body)
	}

	// source: spec/requests/api/v1/plans/charges/filters_controller_spec.rb:52, 152
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/charges/code%3Aunknown/filters", nil); status != http.StatusNotFound {
		t.Errorf("unknown charge: got status = %d (%s), want = 404", status, body)
	}
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, base, `{"values":{},"properties":{"standard":{"amount":"1"}}}`); status != http.StatusBadRequest {
		t.Errorf("missing values: got status = %d (%s), want = 400", status, body)
	}

	// source: spec/requests/api/v1/plans/charges/filters_controller_spec.rb:112, 193
	status, body = f.s.REST(t, f.s.APIKey, http.MethodPost, base,
		`{"values":{"region":{"values":["us"]}},"properties":{"standard":{"amount":"3"},"grouping":{"presentation_group_keys":[{"value":"tier"}]}},"invoice_display_name":"US"}`)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	created := decode[restChargeFilterBody](t, body).Filter
	if created.InvoiceDisplayName != "US" || standardAmount(t, created.Properties) != "3" || created.Properties["grouping"] != nil {
		t.Errorf("got %+v, want the created filter without presentation_group_keys", created)
	}

	// source: spec/requests/api/v1/plans/charges/filters_controller_spec.rb:211, 254
	status, body = f.s.REST(t, f.s.APIKey, http.MethodPatch, base+"/"+created.ID, `{"values":{"region":{"values":["us"]}},"properties":{"standard":{"amount":"4"}}}`)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if got := decode[restChargeFilterBody](t, body).Filter; standardAmount(t, got.Properties) != "4" || got.InvoiceDisplayName != "US" {
		t.Errorf("got %+v, want amount 4 and the name kept", got)
	}
	unknown := core.NewID[core.ChargeFilterID](f.s.Fixtures.IDs).String()
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, base+"/"+unknown, `{"values":{"region":{"values":["us"]}},"invoice_display_name":"x"}`); status != http.StatusNotFound {
		t.Errorf("unknown filter: got status = %d (%s), want = 404", status, body)
	}

	// source: spec/requests/api/v1/plans/charges/filters_controller_spec.rb:280, 324
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodDelete, base+"/"+created.ID, nil); status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodDelete, base+"/"+created.ID, nil); status != http.StatusNotFound {
		t.Errorf("deleted filter: got status = %d (%s), want = 404", status, body)
	}
}

func TestSubscriptionCharges(t *testing.T) {
	t.Parallel()

	// source: spec/requests/api/v1/subscriptions/charges_controller_spec.rb:20, 47, 107, 132
	t.Run("list and get", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		base := "/v1/subscriptions/external_id%3A" + f.sub.ExternalID + "/charges"
		status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base, nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if list := decode[restChargesBody](t, body).Charges; len(list) != 1 || list[0].ID != f.charge.ID.String() {
			t.Fatalf("got charges = %+v, want = [%s]", list, f.charge.ID)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base+"/code%3Aapi", nil); status != http.StatusOK {
			t.Errorf("got status = %d (%s), want = 200", status, body)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base+"/code:unknown", nil); status != http.StatusNotFound {
			t.Errorf("unknown charge: got status = %d (%s), want = 404", status, body)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/subscriptions/external_id%3Aunknown/charges", nil); status != http.StatusNotFound {
			t.Errorf("unknown subscription: got status = %d (%s), want = 404", status, body)
		}
	})

	// source: spec/requests/api/v1/subscriptions/charges_controller_spec.rb:159, 210
	t.Run("update overrides", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		path := "/v1/subscriptions/external_id%3A" + f.sub.ExternalID + "/charges/code%3Aapi"
		status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, path, `{"properties":{"standard":{"amount":"15"}},"min_amount":"5"}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := decode[restChargeBody](t, body).Charge
		if got.ParentID != f.charge.ID.String() || got.PlanID == f.plan.ID.String() || standardAmount(t, got.Properties) != "15" || got.MinAmount != "5.00" {
			t.Fatalf("got %+v, want an override of %s at amount 15", got, f.charge.ID)
		}
		status, body = f.s.REST(t, f.s.APIKey, http.MethodPatch, path, `{"invoice_display_name":"Mine"}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if again := decode[restChargeBody](t, body).Charge; again.ID != got.ID || again.InvoiceDisplayName != "Mine" || standardAmount(t, again.Properties) != "15" {
			t.Errorf("got %+v, want %s renamed", again, got.ID)
		}
		status, body = f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/charges/code%3Aapi", nil)
		if status != http.StatusOK || standardAmount(t, decode[restChargeBody](t, body).Charge.Properties) != "10" {
			t.Errorf("plan charge: got %d %s, want the parent unchanged", status, body)
		}
	})

	// The subscription-scope create and delete have no lago v1.55.0
	// counterpart (app/controllers/api/v1/subscriptions/charges_controller.rb
	// has index, show and update).
	t.Run("create and delete unimplemented", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		base := "/v1/subscriptions/external_id%3A" + f.sub.ExternalID + "/charges"
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, base, `{"code":"x","billable_metric_id":"code:api","charge_model":"CHARGE_MODEL_STANDARD"}`); status != http.StatusNotImplemented {
			t.Errorf("create: got status = %d (%s), want = 501", status, body)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodDelete, base+"/code%3Aapi", nil); status != http.StatusNotImplemented {
			t.Errorf("delete: got status = %d (%s), want = 501", status, body)
		}
	})
}

func TestSubscriptionChargeFilters(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	base := "/v1/subscriptions/external_id%3A" + f.sub.ExternalID + "/charges/code%3Aapi/filters"

	// source: spec/requests/api/v1/subscriptions/charges/filters_controller_spec.rb:22
	status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base, nil)
	if status != http.StatusOK || len(decode[restChargeFiltersBody](t, body).Filters) != 1 {
		t.Fatalf("got %d %s, want one filter", status, body)
	}

	// source: spec/requests/api/v1/subscriptions/charges/filters_controller_spec.rb:105
	status, body = f.s.REST(t, f.s.APIKey, http.MethodGet, base+"/"+f.charge.Filters[0].ID.String(), nil)
	if status != http.StatusOK || decode[restChargeFilterBody](t, body).Filter.ID != f.charge.Filters[0].ID.String() {
		t.Errorf("show: got %d %s, want filter %s", status, body, f.charge.Filters[0].ID)
	}
	// source: spec/requests/api/v1/subscriptions/charges/filters_controller_spec.rb:137
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base+"/"+core.NewID[core.ChargeFilterID](f.s.Fixtures.IDs).String(), nil); status != http.StatusNotFound {
		t.Errorf("show of an unknown filter: got status = %d (%s), want = 404", status, body)
	}

	// source: spec/requests/api/v1/subscriptions/charges/filters_controller_spec.rb:198
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, base, `{"values":{},"properties":{"standard":{"amount":"1"}}}`); status != http.StatusBadRequest {
		t.Errorf("missing values: got status = %d (%s), want = 400", status, body)
	}

	// source: spec/requests/api/v1/subscriptions/charges/filters_controller_spec.rb:145
	status, body = f.s.REST(t, f.s.APIKey, http.MethodPost, base, `{"values":{"region":{"values":["us"]}},"properties":{"standard":{"amount":"3"}}}`)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base, nil); status != http.StatusOK || len(decode[restChargeFiltersBody](t, body).Filters) != 2 {
		t.Errorf("got %d %s, want the override's two filters", status, body)
	}
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/charges/code%3Aapi/filters", nil); status != http.StatusOK || len(decode[restChargeFiltersBody](t, body).Filters) != 1 {
		t.Errorf("got %d %s, want the plan's one filter", status, body)
	}

	// source: spec/requests/api/v1/subscriptions/charges/filters_controller_spec.rb:247, 303
	status, body = f.s.REST(t, f.s.APIKey, http.MethodGet, base, nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	var eu restChargeFilter
	for _, x := range decode[restChargeFiltersBody](t, body).Filters {
		if x.Code == "eu" {
			eu = x
		}
	}
	status, body = f.s.REST(t, f.s.APIKey, http.MethodPatch, base+"/"+eu.ID, `{"values":{"region":{"values":["eu"]}},"properties":{"standard":{"amount":"9"}}}`)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if got := decode[restChargeFilterBody](t, body).Filter; got.ID != eu.ID || standardAmount(t, got.Properties) != "9" {
		t.Errorf("got %+v, want %s at amount 9", got, eu.ID)
	}
	unknown := core.NewID[core.ChargeFilterID](f.s.Fixtures.IDs).String()
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, base+"/"+unknown, `{"values":{"region":{"values":["us"]}},"invoice_display_name":"x"}`); status != http.StatusNotFound {
		t.Errorf("unknown filter: got status = %d (%s), want = 404", status, body)
	}

	// source: spec/requests/api/v1/subscriptions/charges/filters_controller_spec.rb:351
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodDelete, base+"/"+eu.ID, nil); status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base, nil); status != http.StatusOK || len(decode[restChargeFiltersBody](t, body).Filters) != 1 {
		t.Errorf("got %d %s, want one filter left", status, body)
	}
}
