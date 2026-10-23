package rpc_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// restDunningCampaign is the JSON of a dunning campaign in a REST response.
type restDunningCampaign struct {
	ID                  string   `json:"id"`
	Code                string   `json:"code"`
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	DaysBetweenAttempts int      `json:"days_between_attempts"`
	MaxAttempts         int      `json:"max_attempts"`
	BCCEmails           []string `json:"bcc_emails"`
	Thresholds          []struct {
		ID     string  `json:"id"`
		Amount cnMoney `json:"amount"`
	} `json:"thresholds"`
	CustomersCount        int  `json:"customers_count"`
	AppliedToOrganization bool `json:"applied_to_organization"`
}

func dunDecode(t *testing.T, body []byte) restDunningCampaign {
	t.Helper()
	return decode[struct {
		Campaign restDunningCampaign `json:"dunning_campaign"`
	}](t, body).Campaign
}

func dunCreate(t *testing.T, s *rpctest.Server, code, name string, currencies ...string) restDunningCampaign {
	t.Helper()
	var thresholds []map[string]any
	for _, c := range currencies {
		thresholds = append(thresholds, map[string]any{"amount": map[string]any{"currency": c, "amount": "100"}})
	}
	status, body := s.REST(t, s.Session, http.MethodPost, "/v1/dunning_campaigns", map[string]any{
		"code": code, "name": name, "days_between_attempts": 1, "max_attempts": 3, "thresholds": thresholds,
	})
	if status != http.StatusOK {
		t.Fatalf("create %s: got status = %d (%s), want = 200", code, status, body)
	}
	return dunDecode(t, body)
}

func dunID(t *testing.T, id string) core.DunningCampaignID {
	t.Helper()
	v, err := core.ParseID[core.DunningCampaignID](id)
	if err != nil {
		t.Fatalf("parse id %q: %v", id, err)
	}
	return v
}

func TestCreateDunningCampaign(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)

	// source: spec/graphql/mutations/dunning_campaigns/create_spec.rb:52
	status, body := s.REST(t, s.Session, http.MethodPost, "/v1/dunning_campaigns", map[string]any{
		"code": "dunning-campaign", "name": "Dunning campaign name", "description": "Dunning campaign description",
		"bcc_emails": []string{"earl@example.com"}, "days_between_attempts": 1, "max_attempts": 3,
		"thresholds": []map[string]any{{"amount": map[string]any{"currency": "EUR", "amount": "100.00"}}},
	})
	if status != http.StatusOK {
		t.Fatalf("create: got status = %d (%s), want = 200", status, body)
	}
	got := dunDecode(t, body)
	if got.ID == "" || got.Code != "dunning-campaign" || got.Name != "Dunning campaign name" || got.Description != "Dunning campaign description" ||
		got.DaysBetweenAttempts != 1 || got.MaxAttempts != 3 || !slices.Equal(got.BCCEmails, []string{"earl@example.com"}) {
		t.Errorf("got %+v, want the created campaign", got)
	}
	if len(got.Thresholds) != 1 || got.Thresholds[0].ID == "" || got.Thresholds[0].Amount != (cnMoney{"EUR", "100.00"}) || got.AppliedToOrganization {
		t.Errorf("thresholds: got = %+v, applied = %v, want = [EUR 100.00] with an ID, not applied", got.Thresholds, got.AppliedToOrganization)
	}

	// source: spec/services/dunning_campaigns/create_service_spec.rb:96
	status, body = s.REST(t, s.Session, http.MethodPost, "/v1/dunning_campaigns", map[string]any{
		"code": "applied", "name": "Applied", "days_between_attempts": 1, "max_attempts": 3, "applied_to_organization": true,
		"thresholds": []map[string]any{{"amount": map[string]any{"currency": "EUR", "amount": "1"}}},
	})
	if got := dunDecode(t, body); status != http.StatusOK || !got.AppliedToOrganization {
		t.Errorf("applied: got status = %d, %+v (%s), want = 200, applied", status, got, body)
	}

	tests := []struct {
		name   string
		source string
		body   string
	}{
		{"code taken", "spec/services/dunning_campaigns/create_service_spec.rb:130", `{"code":"dunning-campaign","name":"x","days_between_attempts":1,"max_attempts":1,"thresholds":[{"amount":{"currency":"USD","amount":"1"}}]}`},
		{"no thresholds", "spec/services/dunning_campaigns/create_service_spec.rb:142", `{"code":"other","name":"x","days_between_attempts":1,"max_attempts":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, body := s.REST(t, s.Session, http.MethodPost, "/v1/dunning_campaigns", tt.body); status != http.StatusBadRequest {
				t.Errorf("status (%s): got = %d (%s), want = 400", tt.source, status, body)
			}
		})
	}
}

func TestGetDunningCampaign(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := dunCreate(t, s, "dun", "Dun", "USD")
	another := dunCreate(t, s, "another", "Another", "USD")
	cid, aid := dunID(t, c.ID), dunID(t, another.ID)

	// source: spec/graphql/resolvers/dunning_campaign_resolver_spec.rb:53
	status, body := s.REST(t, s.Session, http.MethodGet, "/v1/dunning_campaigns/code%3Adun", nil)
	if status != http.StatusOK || dunDecode(t, body).ID != c.ID {
		t.Fatalf("get by code: got status = %d (%s), want = 200 with %s", status, body, c.ID)
	}

	count := func() int {
		t.Helper()
		status, body := s.REST(t, s.Session, http.MethodGet, "/v1/dunning_campaigns/"+c.ID, nil)
		if status != http.StatusOK {
			t.Fatalf("get: got status = %d (%s), want = 200", status, body)
		}
		return dunDecode(t, body).CustomersCount
	}
	h.Customer(func(cu *customer.Customer) { cu.ExcludeFromDunningCampaign = true })
	h.Customer(func(cu *customer.Customer) { cu.AppliedDunningCampaignID = cid })
	h.Customer(func(cu *customer.Customer) { cu.AppliedDunningCampaignID = aid })
	h.Customer()
	// source: spec/graphql/resolvers/dunning_campaign_resolver_spec.rb:112
	if got := count(); got != 1 {
		t.Errorf("customers count without billing entity: got = %d, want = 1", got)
	}

	entity := func(code string, campaign core.DunningCampaignID) core.BillingEntityID {
		be := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), code, code, time.Now())
		be.AppliedDunningCampaignID = campaign
		h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
		return be.ID
	}
	be2, be3 := entity("be2", cid), entity("be3", aid)
	h.Customer(func(cu *customer.Customer) { cu.BillingEntityID = be2 })
	h.Customer(func(cu *customer.Customer) { cu.BillingEntityID = be3 })
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = cid })
	// source: spec/graphql/resolvers/dunning_campaign_resolver_spec.rb:96
	if got := count(); got != 3 {
		t.Errorf("customers count with billing entities: got = %d, want = 3", got)
	}

	// source: spec/graphql/resolvers/dunning_campaign_resolver_spec.rb:118
	missing := core.NewID[core.DunningCampaignID](core.UUIDv7Generator{}).String()
	if status, _ := s.REST(t, s.Session, http.MethodGet, "/v1/dunning_campaigns/"+missing, nil); status != http.StatusNotFound {
		t.Errorf("missing: got status = %d, want = 404", status)
	}
}

func TestUpdateDunningCampaign(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := dunCreate(t, s, "dun", "Dun", "USD")
	s.Fixtures.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = dunID(t, c.ID) })

	// source: spec/graphql/mutations/dunning_campaigns/update_spec.rb:56
	status, body := s.REST(t, s.Session, http.MethodPatch, "/v1/dunning_campaigns/"+c.ID, map[string]any{
		"name": "Updated Dunning campaign name", "code": "updated-dunning-campaign", "description": "Updated description",
		"days_between_attempts": 2, "max_attempts": 4, "applied_to_organization": false,
		"thresholds": []map[string]any{{"id": c.Thresholds[0].ID, "amount": map[string]any{"currency": "EUR", "amount": "100.00"}}},
	})
	if status != http.StatusOK {
		t.Fatalf("update: got status = %d (%s), want = 200", status, body)
	}
	got := dunDecode(t, body)
	if got.Name != "Updated Dunning campaign name" || got.Code != "updated-dunning-campaign" || got.Description != "Updated description" ||
		got.DaysBetweenAttempts != 2 || got.MaxAttempts != 4 || got.AppliedToOrganization || len(got.Thresholds) != 1 ||
		got.Thresholds[0].ID != c.Thresholds[0].ID || got.Thresholds[0].Amount != (cnMoney{"EUR", "100.00"}) {
		t.Errorf("got %+v, want the updated campaign, not applied, with its threshold in EUR", got)
	}

	// Only an update mask naming the thresholds removes them all
	// (spec/services/dunning_campaigns/update_service_spec.rb:420).
	status, body = s.REST(t, s.Session, http.MethodPatch, "/v1/dunning_campaigns/"+c.ID+"?update_mask=thresholds", map[string]any{"thresholds": []any{}})
	if got := dunDecode(t, body); status != http.StatusOK || len(got.Thresholds) != 0 {
		t.Errorf("no thresholds: got status = %d, %+v (%s), want = 200 without thresholds", status, got, body)
	}
	status, body = s.REST(t, s.Session, http.MethodPatch, "/v1/dunning_campaigns/"+c.ID, map[string]any{
		"thresholds": []map[string]any{{"id": "nope", "amount": map[string]any{"currency": "EUR", "amount": "1"}}},
	})
	if status != http.StatusBadRequest {
		t.Errorf("invalid threshold id: got status = %d (%s), want = 400", status, body)
	}

	// An update mask keeps the fields outside it.
	status, body = s.REST(t, s.Session, http.MethodPatch, "/v1/dunning_campaigns/code%3Aupdated-dunning-campaign?update_mask=maxAttempts",
		map[string]any{"name": "ignored", "max_attempts": 5})
	if got := dunDecode(t, body); status != http.StatusOK || got.MaxAttempts != 5 || got.Name != "Updated Dunning campaign name" {
		t.Errorf("masked update: got status = %d, %+v (%s), want = 200 with max attempts 5 and the name kept", status, got, body)
	}
}

func TestDeleteDunningCampaign(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := dunCreate(t, s, "dun", "Dun", "USD")
	cid := dunID(t, c.ID)
	cust := h.Customer(func(cu *customer.Customer) {
		cu.AppliedDunningCampaignID = cid
		cu.DunningCampaignAttempts = map[string]int{"USD": 1}
	})

	// source: spec/graphql/mutations/dunning_campaigns/destroy_spec.rb:25
	if status, body := s.REST(t, s.Session, http.MethodDelete, "/v1/dunning_campaigns/"+c.ID, nil); status != http.StatusOK {
		t.Fatalf("delete: got status = %d (%s), want = 200", status, body)
	}
	if got := h.ReloadCustomer(cust.ID); !got.AppliedDunningCampaignID.IsZero() || len(got.DunningCampaignAttempts) != 0 {
		t.Errorf("customer: got campaign = %s, attempts = %v, want = none", got.AppliedDunningCampaignID, got.DunningCampaignAttempts)
	}
	// source: spec/graphql/mutations/dunning_campaigns/destroy_spec.rb:43
	if status, _ := s.REST(t, s.Session, http.MethodDelete, "/v1/dunning_campaigns/"+c.ID, nil); status != http.StatusNotFound {
		t.Errorf("delete again: got status = %d, want = 404", status)
	}
}

func TestListDunningCampaigns(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	first := dunCreate(t, s, "11", "defgh", "USD", "GBP")
	second := dunCreate(t, s, "22", "abcde", "CHF")
	third := dunCreate(t, s, "33", "presuv", "EUR")
	s.Fixtures.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = dunID(t, first.ID) })

	tests := []struct {
		name   string
		source string
		query  string
		want   []string
	}{
		{"by name", "spec/graphql/resolvers/dunning_campaigns_resolver_spec.rb:28", "", []string{second.ID, first.ID, third.ID}},
		{"search", "spec/queries/dunning_campaigns_query_spec.rb:89", "search=de", []string{second.ID, first.ID}},
		{"currency", "spec/graphql/resolvers/dunning_campaigns_resolver_spec.rb:66", "filter=currency%3D%22EUR%22+OR+currency%3D%22GBP%22", []string{first.ID, third.ID}},
		{"not applied", "spec/queries/dunning_campaigns_query_spec.rb:97", "filter=applied_to_organization%3Dfalse", []string{second.ID, third.ID}},
		{"applied", "spec/queries/dunning_campaigns_query_spec.rb:105", "filter=applied_to_organization%3Dtrue", []string{first.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := s.REST(t, s.Session, http.MethodGet, "/v1/dunning_campaigns?"+tt.query, nil)
			if status != http.StatusOK {
				t.Fatalf("list: got status = %d (%s), want = 200", status, body)
			}
			var ids []string
			for _, c := range decode[struct {
				Campaigns []restDunningCampaign `json:"dunning_campaigns"`
			}](t, body).Campaigns {
				ids = append(ids, c.ID)
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("ids (%s): got = %v, want = %v", tt.source, ids, tt.want)
			}
		})
	}
	if status, _ := s.REST(t, s.Session, http.MethodGet, "/v1/dunning_campaigns?filter=currency%3D%22XXX1%22", nil); status != http.StatusBadRequest {
		t.Errorf("unknown currency: got status = %d, want = 400", status)
	}
}
