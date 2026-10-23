package rpc_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// restFixedCharge is the JSON of a fixed charge in a REST response.
type restFixedCharge struct {
	ID                 string         `json:"id"`
	Code               string         `json:"code"`
	PlanID             string         `json:"plan_id"`
	ParentID           string         `json:"parent_id"`
	AddOnID            string         `json:"add_on_id"`
	ChargeModel        string         `json:"charge_model"`
	Properties         map[string]any `json:"properties"`
	Units              string         `json:"units"`
	PayInAdvance       bool           `json:"pay_in_advance"`
	Prorated           bool           `json:"prorated"`
	InvoiceDisplayName string         `json:"invoice_display_name"`
	TaxIDs             []string       `json:"tax_ids"`
}

type restFixedChargeBody struct {
	FixedCharge restFixedCharge `json:"fixed_charge"`
}

type restFixedChargesBody struct {
	FixedCharges  []restFixedCharge `json:"fixed_charges"`
	NextPageToken string            `json:"next_page_token"`
}

// fixedChargeFixture is a USD plan "basic" with a standard fixed charge
// "seats" of 2 units on the add-on "seats", and a subscription to it.
type fixedChargeFixture struct {
	s     *rpctest.Server
	plan  *catalog.Plan
	addOn *catalog.AddOn
	fc    *catalog.FixedCharge
	sub   *subscription.Subscription
}

func newFixedChargeFixture(t *testing.T) fixedChargeFixture {
	t.Helper()
	s := rpctest.New(t)
	f := s.Fixtures
	p := f.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.Code = "basic" })
	a := f.AddOn(100, func(a *catalog.AddOn) { a.Code = "seats" })
	fc := f.FixedCharge(p, a, catalog.FixedChargeModelStandard, `{"amount":"10"}`, "2", func(fc *catalog.FixedCharge) { fc.Code = "seats" })
	sub := f.Subscription(f.Customer(), p)
	return fixedChargeFixture{s: s, plan: p, addOn: a, fc: fc, sub: sub}
}

func TestPlanFixedCharges(t *testing.T) {
	t.Parallel()

	// source: spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:17, 55, 88
	t.Run("list and get", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		child := f.s.Fixtures.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.ParentID, p.Code = f.plan.ID, f.plan.Code })
		f.s.Fixtures.FixedCharge(child, f.addOn, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1", func(fc *catalog.FixedCharge) {
			fc.ParentID, fc.Code = f.fc.ID, "seats"
		})
		status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/fixed_charges", nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if list := decode[restFixedChargesBody](t, body).FixedCharges; len(list) != 1 || list[0].ID != f.fc.ID.String() {
			t.Fatalf("got fixed charges = %+v, want = [%s]", list, f.fc.ID)
		}
		status, body = f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/fixed_charges/code%3Aseats", nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := decode[restFixedChargeBody](t, body).FixedCharge
		if got.ChargeModel != "FIXED_CHARGE_MODEL_STANDARD" || got.Units != "2" || standardAmount(t, got.Properties) != "10" || got.AddOnID != f.addOn.ID.String() {
			t.Errorf("got %+v, want the stored fixed charge", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		tests := []struct {
			name, source, method, path string
		}{
			{"plan list", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:42", http.MethodGet, "/v1/plans/code%3Aunknown/fixed_charges"},
			{"plan", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:101", http.MethodGet, "/v1/plans/code%3Aunknown/fixed_charges/code%3Aseats"},
			{"fixed charge", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:111", http.MethodGet, "/v1/plans/code%3Abasic/fixed_charges/code%3Aunknown"},
			{"update plan", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:255", http.MethodPatch, "/v1/plans/code%3Aunknown/fixed_charges/code%3Aseats"},
			{"update fixed charge", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:265", http.MethodPatch, "/v1/plans/code%3Abasic/fixed_charges/code%3Aunknown"},
			{"delete plan", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:321", http.MethodDelete, "/v1/plans/code%3Aunknown/fixed_charges/code%3Aseats"},
			{"delete fixed charge", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:331", http.MethodDelete, "/v1/plans/code%3Abasic/fixed_charges/code%3Aunknown"},
		}
		for _, tt := range tests {
			t.Logf("source: %s", tt.source)
			var body any
			if tt.method == http.MethodPatch {
				body = `{"units":"3"}`
			}
			if status, resp := f.s.REST(t, f.s.APIKey, tt.method, tt.path, body); status != http.StatusNotFound {
				t.Errorf("%s: got status = %d (%s), want = 404", tt.name, status, resp)
			}
		}
	})

	t.Run("create", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name   string
			source string
			path   string
			body   string
			status int
		}{
			{
				"by add-on ID", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:133", "/v1/plans/code%3Abasic/fixed_charges",
				`{"code":"new_fixed_charge_code","add_on_id":"ADDON","charge_model":"FIXED_CHARGE_MODEL_STANDARD",
				"properties":{"standard":{"amount":"5"}},"units":"3","invoice_display_name":"New"}`, http.StatusOK,
			},
			{
				"by add-on code with taxes", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:155, 202", "/v1/plans/code%3Abasic/fixed_charges",
				`{"code":"new_fixed_charge_code","add_on_id":"code:seats","charge_model":"FIXED_CHARGE_MODEL_GRADUATED",
				"properties":{"graduated":{"ranges":[{"from_value":"0","to_value":"10","per_unit_amount":"1","flat_amount":"0"},
				{"from_value":"10","per_unit_amount":"0.5","flat_amount":"2"}]}},"units":"3","tax_ids":["code:vat"]}`, http.StatusOK,
			},
			{
				"plan not found", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:166", "/v1/plans/code%3Aunknown/fixed_charges",
				`{"code":"x","add_on_id":"code:seats","charge_model":"FIXED_CHARGE_MODEL_STANDARD","units":"1"}`, http.StatusNotFound,
			},
			{
				"add-on not found", "spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:182", "/v1/plans/code%3Abasic/fixed_charges",
				`{"code":"x","add_on_id":"code:unknown","charge_model":"FIXED_CHARGE_MODEL_STANDARD","units":"1"}`, http.StatusNotFound,
			},
			{
				"used code", "spec/services/fixed_charges/create_service_spec.rb:62", "/v1/plans/code%3Abasic/fixed_charges",
				`{"code":"seats","add_on_id":"code:seats","charge_model":"FIXED_CHARGE_MODEL_STANDARD","units":"1"}`, http.StatusBadRequest,
			},
			{
				"missing units", "app/controllers/api/v1/plans/fixed_charges_controller.rb:101-115", "/v1/plans/code%3Abasic/fixed_charges",
				`{"code":"x","add_on_id":"code:seats","charge_model":"FIXED_CHARGE_MODEL_STANDARD"}`, http.StatusBadRequest,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				t.Logf("source: %s", tt.source)
				f := newFixedChargeFixture(t)
				vat := f.s.Fixtures.Tax("vat", "20")
				body := strings.ReplaceAll(tt.body, "ADDON", f.addOn.ID.String())
				status, resp := f.s.REST(t, f.s.APIKey, http.MethodPost, tt.path, body)
				if status != tt.status {
					t.Fatalf("got status = %d (%s), want = %d", status, resp, tt.status)
				}
				if status != http.StatusOK {
					return
				}
				got := decode[restFixedChargeBody](t, resp).FixedCharge
				if got.Code != "new_fixed_charge_code" || got.PlanID != f.plan.ID.String() || got.AddOnID != f.addOn.ID.String() || got.Units != "3" {
					t.Errorf("got %+v, want the created fixed charge", got)
				}
				if tt.name == "by add-on code with taxes" {
					grad, _ := got.Properties["graduated"].(map[string]any)
					ranges, _ := grad["ranges"].([]any)
					if len(ranges) != 2 || len(got.TaxIDs) != 1 || got.TaxIDs[0] != vat.ID.String() {
						t.Errorf("got %+v, want two ranges and the vat", got)
					}
				}
			})
		}
	})

	// source: spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:221
	t.Run("create cascades", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		child := f.s.Fixtures.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.ParentID, p.Code = f.plan.ID, f.plan.Code })
		f.s.Fixtures.Subscription(f.s.Fixtures.Customer(), child)
		status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/plans/code%3Abasic/fixed_charges?cascade_updates=true",
			`{"code":"extra","add_on_id":"code:seats","charge_model":"FIXED_CHARGE_MODEL_STANDARD","units":"1","properties":{"standard":{"amount":"1"}}}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if got := decode[restFixedChargeBody](t, body).FixedCharge; got.PlanID != f.plan.ID.String() || got.ParentID != "" {
			t.Errorf("got %+v, want the parent plan's fixed charge", got)
		}
	})

	// source: spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:243, 277
	t.Run("update", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, "/v1/plans/code%3Abasic/fixed_charges/code%3Aseats",
			`{"properties":{"standard":{"amount":"20"}},"units":"4","invoice_display_name":"Seats","charge_model":"FIXED_CHARGE_MODEL_GRADUATED"}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := decode[restFixedChargeBody](t, body).FixedCharge
		if standardAmount(t, got.Properties) != "20" || got.Units != "4" || got.InvoiceDisplayName != "Seats" || got.ChargeModel != "FIXED_CHARGE_MODEL_STANDARD" {
			t.Errorf("got %+v, want amount 20, 4 units, the name and the model kept on an attached plan", got)
		}
	})

	// source: spec/requests/api/v1/plans/fixed_charges_controller_spec.rb:310
	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodDelete, "/v1/plans/code%3Abasic/fixed_charges/code%3Aseats", nil); status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/fixed_charges/code%3Aseats", nil); status != http.StatusNotFound {
			t.Errorf("got status = %d (%s), want = 404", status, body)
		}
	})
}

func TestSubscriptionFixedCharges(t *testing.T) {
	t.Parallel()

	// source: spec/requests/api/v1/subscriptions/fixed_charges_controller_spec.rb:28, 115
	t.Run("list and get", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		base := "/v1/subscriptions/external_id%3A" + f.sub.ExternalID + "/fixed_charges"
		status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base, nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if list := decode[restFixedChargesBody](t, body).FixedCharges; len(list) != 1 || list[0].ID != f.fc.ID.String() {
			t.Fatalf("got fixed charges = %+v, want = [%s]", list, f.fc.ID)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base+"/code%3Aseats", nil); status != http.StatusOK {
			t.Errorf("got status = %d (%s), want = 200", status, body)
		}
		// source: spec/requests/api/v1/subscriptions/fixed_charges_controller_spec.rb:138
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, base+"/code%3Aunknown", nil); status != http.StatusNotFound {
			t.Errorf("unknown fixed charge: got status = %d (%s), want = 404", status, body)
		}
		// source: spec/requests/api/v1/subscriptions/fixed_charges_controller_spec.rb:103, 128
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/subscriptions/external_id%3Aunknown/fixed_charges", nil); status != http.StatusNotFound {
			t.Errorf("unknown subscription: got status = %d (%s), want = 404", status, body)
		}
	})

	// source: spec/requests/api/v1/subscriptions/fixed_charges_controller_spec.rb:76, 168
	t.Run("units override", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		path := "/v1/subscriptions/external_id%3A" + f.sub.ExternalID + "/fixed_charges/code%3Aseats"
		status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, path, `{"units":"7"}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if got := decode[restFixedChargeBody](t, body).FixedCharge; got.ID != f.fc.ID.String() || got.Units != "7" {
			t.Errorf("got %+v, want %s with 7 units", got, f.fc.ID)
		}
		status, body = f.s.REST(t, f.s.APIKey, http.MethodGet, path, nil)
		if status != http.StatusOK || decode[restFixedChargeBody](t, body).FixedCharge.Units != "7" {
			t.Errorf("get: got %d %s, want 7 units", status, body)
		}
		status, body = f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/plans/code%3Abasic/fixed_charges/code%3Aseats", nil)
		if status != http.StatusOK || decode[restFixedChargeBody](t, body).FixedCharge.Units != "2" {
			t.Errorf("plan fixed charge: got %d %s, want 2 units", status, body)
		}
	})

	// source: spec/requests/api/v1/subscriptions/fixed_charges_controller_spec.rb:190, 199, 246, 250, 269
	t.Run("update overrides", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		vat := f.s.Fixtures.Tax("vat", "20")
		path := "/v1/subscriptions/external_id%3A" + f.sub.ExternalID + "/fixed_charges/code%3Aseats"
		status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, path, `{"invoice_display_name":"Mine","units":"5","tax_ids":["code:vat"]}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := decode[restFixedChargeBody](t, body).FixedCharge
		if got.ParentID != f.fc.ID.String() || got.PlanID == f.plan.ID.String() || got.InvoiceDisplayName != "Mine" || got.Units != "5" ||
			len(got.TaxIDs) != 1 || got.TaxIDs[0] != vat.ID.String() {
			t.Fatalf("got %+v, want an override of %s with the name, 5 units and the vat", got, f.fc.ID)
		}
		status, body = f.s.REST(t, f.s.APIKey, http.MethodPatch, path, `{"properties":{"standard":{"amount":"15"}}}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if again := decode[restFixedChargeBody](t, body).FixedCharge; again.ID != got.ID || again.PlanID != got.PlanID || standardAmount(t, again.Properties) != "15" {
			t.Errorf("got %+v, want %s updated in place", again, got.ID)
		}
		// source: spec/requests/api/v1/subscriptions/fixed_charges_controller_spec.rb:209, 219
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, "/v1/subscriptions/external_id%3Aunknown/fixed_charges/code%3Aseats", `{"units":"1"}`); status != http.StatusNotFound {
			t.Errorf("unknown subscription: got status = %d (%s), want = 404", status, body)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, "/v1/subscriptions/external_id%3A"+f.sub.ExternalID+"/fixed_charges/code%3Aunknown", `{"units":"1"}`); status != http.StatusNotFound {
			t.Errorf("unknown fixed charge: got status = %d (%s), want = 404", status, body)
		}
	})

	// source: spec/requests/api/v1/subscriptions/fixed_charges_controller_spec.rb:231
	t.Run("incomplete subscription", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		f.s.Fixtures.UpdateSubscription(f.sub, func(s *subscription.Subscription) { s.Status = subscription.StatusIncomplete })
		path := "/v1/subscriptions/" + f.sub.ID.String() + "/fixed_charges/code%3Aseats"
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, path, `{"units":"3"}`); status != http.StatusBadRequest {
			t.Errorf("got status = %d (%s), want = 400", status, body)
		}
	})

	// The subscription-scope create and delete have no lago v1.55.0
	// counterpart (app/controllers/api/v1/subscriptions/fixed_charges_controller.rb
	// has index, show and update), nor have the plan-only fields.
	t.Run("unimplemented and plan-only fields", func(t *testing.T) {
		t.Parallel()
		f := newFixedChargeFixture(t)
		base := "/v1/subscriptions/external_id%3A" + f.sub.ExternalID + "/fixed_charges"
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, base, `{"code":"x","add_on_id":"code:seats","charge_model":"FIXED_CHARGE_MODEL_STANDARD","units":"1"}`); status != http.StatusNotImplemented {
			t.Errorf("create: got status = %d (%s), want = 501", status, body)
		}
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodDelete, base+"/code%3Aseats", nil); status != http.StatusNotImplemented {
			t.Errorf("delete: got status = %d (%s), want = 501", status, body)
		}
		// source: app/controllers/api/v1/subscriptions/fixed_charges_controller.rb:70-78
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, base+"/code%3Aseats?update_mask=pay_in_advance", `{"pay_in_advance":true}`); status != http.StatusBadRequest {
			t.Errorf("plan-only field: got status = %d (%s), want = 400", status, body)
		}
	})
}
