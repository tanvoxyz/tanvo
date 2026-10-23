package rpc_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// restPlan is the JSON of a plan in a REST response.
type restPlan struct {
	ID                 string `json:"id"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	InvoiceDisplayName string `json:"invoice_display_name"`
	Interval           string `json:"interval"`
	Status             string `json:"status"`
	Amount             struct {
		Currency string `json:"currency"`
		Amount   string `json:"amount"`
	} `json:"amount"`
	TaxIDs            []string `json:"tax_ids"`
	MinimumCommitment *struct {
		Amount             string `json:"amount"`
		InvoiceDisplayName string `json:"invoice_display_name"`
	} `json:"minimum_commitment"`
	UsageThresholds []struct {
		Amount    string `json:"amount"`
		Recurring bool   `json:"recurring"`
	} `json:"usage_thresholds"`
	Metadata []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"metadata"`
	ActiveSubscriptionsCount int    `json:"active_subscriptions_count"`
	CreatedAt                string `json:"created_at"`
}

type restPlanBody struct {
	Plan restPlan `json:"plan"`
}

func TestCreatePlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		status int
	}{
		{
			"creates", "spec/requests/api/v1/plans_controller_spec.rb:88",
			`{"name":"P1","code":"plan_code","invoice_display_name":"Plan 1","interval":"INTERVAL_WEEKLY",
			"amount":{"currency":"EUR","amount":"1.00"},"tax_ids":["code:vat"],
			"minimum_commitment":{"commitment_type":"COMMITMENT_TYPE_MINIMUM_COMMITMENT","amount":"10.00","invoice_display_name":"Minimum"},
			"usage_thresholds":[{"amount":"5.00"},{"amount":"1.00","recurring":true}],
			"metadata":[{"key":"foo","value":"bar"}]}`, http.StatusOK,
		},
		{
			"empty interval", "spec/requests/api/v1/plans_controller_spec.rb:75",
			`{"name":"P1","code":"plan_code","amount":{"currency":"EUR","amount":"1"}}`, http.StatusBadRequest,
		},
		{
			"used code", "spec/services/plans/create_service_spec.rb:528",
			`{"name":"P1","code":"used","interval":"INTERVAL_MONTHLY","amount":{"currency":"EUR","amount":"1"}}`, http.StatusBadRequest,
		},
		{
			"unknown tax", "spec/requests/api/v1/plans_controller_spec.rb:365",
			`{"name":"P1","code":"plan_code","interval":"INTERVAL_MONTHLY","amount":{"currency":"EUR","amount":"1"},"tax_ids":["code:unknown"]}`, http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			vat := s.Fixtures.Tax("vat", "20")
			s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "used" })
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/plans", tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[restPlanBody](t, body).Plan
			if got.ID == "" || got.CreatedAt == "" || got.Code != "plan_code" || got.Name != "P1" || got.InvoiceDisplayName != "Plan 1" ||
				got.Interval != "INTERVAL_WEEKLY" || got.Amount.Currency != "EUR" || got.Amount.Amount != "1.00" {
				t.Errorf("got %+v, want the created attributes", got)
			}
			if len(got.TaxIDs) != 1 || got.TaxIDs[0] != vat.ID.String() {
				t.Errorf("got tax_ids = %v, want = [%s]", got.TaxIDs, vat.ID)
			}
			// source: spec/requests/api/v1/plans_controller_spec.rb:176
			if c := got.MinimumCommitment; c == nil || c.Amount != "10.00" || c.InvoiceDisplayName != "Minimum" {
				t.Errorf("got minimum_commitment = %+v, want = 10.00 Minimum", c)
			}
			// source: spec/requests/api/v1/plans_controller_spec.rb:201
			if u := got.UsageThresholds; len(u) != 2 || u[0].Amount != "5.00" || !u[1].Recurring {
				t.Errorf("got usage_thresholds = %+v, want = 5.00 then the recurring 1.00", u)
			}
			// source: spec/requests/api/v1/plans_controller_spec.rb:1151
			if m := got.Metadata; len(m) != 1 || m[0].Key != "foo" || m[0].Value != "bar" {
				t.Errorf("got metadata = %+v, want = foo=bar", m)
			}
		})
	}
}

func TestUpdatePlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		ref    string
		body   string
		status int
	}{
		{
			"updates", "spec/requests/api/v1/plans_controller_spec.rb:473",
			"code%3Aplan", `{"name":"P2","code":"plan_new","amount":{"currency":"USD","amount":"2.00"}}`, http.StatusOK,
		},
		{
			"not found", "spec/requests/api/v1/plans_controller_spec.rb:485",
			"code%3Aunknown", `{"name":"P2"}`, http.StatusNotFound,
		},
		{
			"code in use", "spec/requests/api/v1/plans_controller_spec.rb:495",
			"code%3Aplan", `{"code":"used"}`, http.StatusBadRequest,
		},
		{
			"removes the minimum commitment", "spec/requests/api/v1/plans_controller_spec.rb:665",
			"code%3Aplan", `{"minimum_commitment":{}}`, http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			p := s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "plan" })
			s.Fixtures.Commitment(p, 500)
			s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "used" })
			status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/plans/"+tt.ref, tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[restPlanBody](t, body).Plan
			switch tt.name {
			case "updates":
				if got.Name != "P2" || got.Code != "plan_new" || got.Amount.Amount != "2.00" {
					t.Errorf("got %+v, want = P2 plan_new 2.00", got)
				}
				if got.MinimumCommitment == nil || got.MinimumCommitment.Amount != "5.00" {
					t.Errorf("got minimum_commitment = %+v, want = kept 5.00", got.MinimumCommitment)
				}
			default:
				if got.MinimumCommitment != nil {
					t.Errorf("got minimum_commitment = %+v, want = none", got.MinimumCommitment)
				}
			}
		})
	}

	t.Run("update_subscriptions_amount is not supported", func(t *testing.T) {
		t.Parallel()
		s := rpctest.New(t)
		s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "plan" })
		status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/plans/code%3Aplan?update_subscriptions_amount=true", `{"name":"x"}`)
		if status != http.StatusNotImplemented {
			t.Errorf("got status = %d (%s), want = %d", status, body, http.StatusNotImplemented)
		}
	})
}

func TestGetPlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		ref    func(p, deleted *catalog.Plan) string
		status int
	}{
		{"by code", "spec/requests/api/v1/plans_controller_spec.rb:955", func(p, _ *catalog.Plan) string { return "code%3Aplan" }, http.StatusOK},
		{"by id", "spec/requests/api/v1/plans_controller_spec.rb:955", func(p, _ *catalog.Plan) string { return p.ID.String() }, http.StatusOK},
		{"discarded", "spec/requests/api/v1/plans_controller_spec.rb:968", func(_, d *catalog.Plan) string { return d.ID.String() }, http.StatusNotFound},
		{"unknown", "spec/requests/api/v1/plans_controller_spec.rb:1026", func(_, _ *catalog.Plan) string { return "code%3Aunknown" }, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			p := s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "plan" })
			s.Fixtures.Commitment(p, 500)
			s.Fixtures.UsageThreshold(p, 1000, false)
			s.Fixtures.Subscription(s.Fixtures.Customer(), p)
			deleted := s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Status = catalog.PlanStatusDeleted })
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/plans/"+tt.ref(p, deleted), nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[restPlanBody](t, body).Plan
			if got.ID != p.ID.String() || got.Status != "PLAN_STATUS_ACTIVE" {
				t.Errorf("got %s %s, want = %s active", got.ID, got.Status, p.ID)
			}
			// source: spec/requests/api/v1/plans_controller_spec.rb:977
			if got.MinimumCommitment == nil || got.MinimumCommitment.Amount != "5.00" {
				t.Errorf("got minimum_commitment = %+v, want = 5.00", got.MinimumCommitment)
			}
			// source: spec/requests/api/v1/plans_controller_spec.rb:993
			if len(got.UsageThresholds) != 1 || got.UsageThresholds[0].Amount != "10.00" {
				t.Errorf("got usage_thresholds = %+v, want = [10.00]", got.UsageThresholds)
			}
			if got.ActiveSubscriptionsCount != 1 {
				t.Errorf("got active_subscriptions_count = %d, want = 1", got.ActiveSubscriptionsCount)
			}
		})
	}
}

func TestDeletePlan(t *testing.T) {
	t.Parallel()
	// source: spec/requests/api/v1/plans_controller_spec.rb:1042
	t.Run("marks the plan and its override plans pending deletion", func(t *testing.T) {
		t.Parallel()
		s := rpctest.New(t)
		p := s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "plan" })
		child := s.Fixtures.Plan(period.Monthly, 90, func(c *catalog.Plan) { c.ParentID, c.Code = p.ID, p.Code })
		status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/plans/code%3Aplan", nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		// source: spec/requests/api/v1/plans_controller_spec.rb:1053
		if got := decode[restPlanBody](t, body).Plan; got.ID != p.ID.String() || got.Status != "PLAN_STATUS_PENDING_DELETION" {
			t.Errorf("got %s %s, want = %s pending deletion", got.ID, got.Status, p.ID)
		}
		// source: spec/requests/api/v1/plans_controller_spec.rb:1046
		st := billingtest.Read(s.Fixtures, func(ctx context.Context, r port.Repos) (catalog.PlanStatus, error) {
			c, err := r.Plans.Get(ctx, s.Fixtures.OrgID(), child.ID)
			if err != nil {
				return "", err
			}
			return c.Status, nil
		})
		if st != catalog.PlanStatusPendingDeletion {
			t.Errorf("got child status = %s, want = pending_deletion", st)
		}
	})

	// source: spec/requests/api/v1/plans_controller_spec.rb:1067
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		s := rpctest.New(t)
		if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/plans/code%3Aunknown", nil); status != http.StatusNotFound {
			t.Errorf("got status = %d (%s), want = 404", status, body)
		}
	})
}

func TestListPlans(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	p := s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Name = "Gold" })
	s.Fixtures.Plan(period.Monthly, 90, func(c *catalog.Plan) { c.ParentID, c.Code = p.ID, p.Code })
	pending := s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Name = "Silver"; p.Status = catalog.PlanStatusPendingDeletion })
	s.Fixtures.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Status = catalog.PlanStatusDeleted })
	type list struct {
		Plans         []restPlan `json:"plans"`
		NextPageToken string     `json:"next_page_token"`
	}
	// source: spec/requests/api/v1/plans_controller_spec.rb:1083, spec/requests/api/v1/plans_controller_spec.rb:1104
	status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/plans", nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	got := map[string]bool{}
	for _, x := range decode[list](t, body).Plans {
		got[x.ID] = true
	}
	if len(got) != 2 || !got[p.ID.String()] || !got[pending.ID.String()] {
		t.Errorf("got plans = %v, want = %s and %s", got, p.ID, pending.ID)
	}
	// source: spec/requests/api/v1/plans_controller_spec.rb:1117
	status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/plans?page_size=1", nil)
	if l := decode[list](t, body); status != http.StatusOK || len(l.Plans) != 1 || l.NextPageToken == "" {
		t.Errorf("got status = %d, %d plans, token %q, want = 200, 1 plan and a token", status, len(l.Plans), l.NextPageToken)
	}
	status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/plans?search=silv", nil)
	if l := decode[list](t, body); status != http.StatusOK || len(l.Plans) != 1 || l.Plans[0].ID != pending.ID.String() {
		t.Errorf("got status = %d, plans = %+v, want = [%s]", status, l.Plans, pending.ID)
	}
	if status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/plans?filter=code%3D%22x%22", nil); status != http.StatusBadRequest {
		t.Errorf("got filter status = %d (%s), want = 400", status, body)
	}
}
