package rpc_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// restAddOn is the JSON of an add-on in a REST response.
type restAddOn struct {
	ID                 string `json:"id"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	InvoiceDisplayName string `json:"invoice_display_name"`
	Description        string `json:"description"`
	Amount             struct {
		Currency string `json:"currency"`
		Amount   string `json:"amount"`
	} `json:"amount"`
	TaxIDs    []string `json:"tax_ids"`
	CreatedAt string   `json:"created_at"`
}

type restAddOnBody struct {
	AddOn restAddOn `json:"add_on"`
}

func TestCreateAddOn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		status int
	}{
		{
			"creates", "spec/requests/api/v1/add_ons_controller_spec.rb:26",
			`{"name":"add_on1","invoice_display_name":"Addon 1 invoice name","code":"add_on1_code",
			"amount":{"currency":"EUR","amount":"1.23"},"description":"description","tax_ids":["code:vat"]}`, http.StatusOK,
		},
		{
			"used code", "spec/services/add_ons/create_service_spec.rb:71",
			`{"name":"x","code":"used","amount":{"currency":"EUR","amount":"1"}}`, http.StatusBadRequest,
		},
		{
			"missing tax", "spec/services/add_ons/apply_taxes_service_spec.rb:38",
			`{"name":"x","code":"x","amount":{"currency":"EUR","amount":"1"},"tax_ids":["code:unknown"]}`, http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			vat := s.Fixtures.Tax("vat", "20")
			s.Fixtures.AddOn(100, func(a *catalog.AddOn) { a.Code = "used" })
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/add_ons", tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[restAddOnBody](t, body).AddOn
			if got.ID == "" || got.CreatedAt == "" || got.Code != "add_on1_code" || got.Name != "add_on1" ||
				got.InvoiceDisplayName != "Addon 1 invoice name" || got.Amount.Currency != "EUR" || got.Amount.Amount != "1.23" {
				t.Errorf("got %+v, want the created attributes", got)
			}
			if len(got.TaxIDs) != 1 || got.TaxIDs[0] != vat.ID.String() {
				t.Errorf("got tax_ids = %v, want = [%s]", got.TaxIDs, vat.ID)
			}
		})
	}
}

func TestUpdateAddOn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		ref    string
		body   string
		status int
	}{
		{
			"updates", "spec/requests/api/v1/add_ons_controller_spec.rb:67", "code%3Abeer",
			`{"name":"add_on1","invoice_display_name":"Addon 1 updated invoice name","code":"add_on_code",
			"amount":{"currency":"EUR","amount":"1.23"},"description":"description","tax_ids":["code:gst"]}`, http.StatusOK,
		},
		{"not found", "spec/requests/api/v1/add_ons_controller_spec.rb:80", "code%3Aunknown", `{"name":"x"}`, http.StatusNotFound},
		{"duplicate code", "spec/requests/api/v1/add_ons_controller_spec.rb:90", "code%3Abeer", `{"code":"wine"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			vat := s.Fixtures.Tax("vat", "20")
			gst := s.Fixtures.Tax("gst", "10")
			a := s.Fixtures.AddOn(500, func(a *catalog.AddOn) { a.Code = "beer"; a.TaxIDs = []core.TaxID{vat.ID} })
			s.Fixtures.AddOn(500, func(a *catalog.AddOn) { a.Code = "wine" })
			status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/add_ons/"+tt.ref, tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[restAddOnBody](t, body).AddOn
			if got.ID != a.ID.String() || got.Code != "add_on_code" || got.InvoiceDisplayName != "Addon 1 updated invoice name" ||
				got.Amount.Currency != "EUR" || got.Amount.Amount != "1.23" {
				t.Errorf("got %+v, want the updated attributes", got)
			}
			if len(got.TaxIDs) != 1 || got.TaxIDs[0] != gst.ID.String() {
				t.Errorf("got tax_ids = %v, want = [%s]", got.TaxIDs, gst.ID)
			}
		})
	}
}

func TestGetAddOn(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	a := s.Fixtures.AddOn(500, func(a *catalog.AddOn) { a.Code = "beer" })
	tests := []struct {
		name   string
		source string
		ref    string
		status int
	}{
		{"by code", "spec/requests/api/v1/add_ons_controller_spec.rb:105", "code%3Abeer", http.StatusOK},
		{"by id", "spec/requests/api/v1/add_ons_controller_spec.rb:105", a.ID.String(), http.StatusOK},
		{"not found", "spec/requests/api/v1/add_ons_controller_spec.rb:117", "code%3Aunknown", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/add_ons/"+tt.ref, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			if got := decode[restAddOnBody](t, body).AddOn; got.ID != a.ID.String() || got.Code != "beer" {
				t.Errorf("got %s/%s, want = %s/beer", got.ID, got.Code, a.ID)
			}
		})
	}
}

func TestDeleteAddOn(t *testing.T) {
	t.Parallel()
	t.Run("deletes it and its fixed charges", func(t *testing.T) {
		t.Parallel()
		// source: spec/requests/api/v1/add_ons_controller_spec.rb:132
		// source: spec/requests/api/v1/add_ons_controller_spec.rb:136
		s := rpctest.New(t)
		a := s.Fixtures.AddOn(500, func(a *catalog.AddOn) { a.Code = "beer" })
		p := s.Fixtures.Plan(period.Monthly, 1000)
		s.Fixtures.FixedCharge(p, a, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1")
		status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/add_ons/code%3Abeer", nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if got := decode[restAddOnBody](t, body).AddOn; got.ID != a.ID.String() || got.Code != "beer" {
			t.Errorf("got %s/%s, want = %s/beer", got.ID, got.Code, a.ID)
		}
		// Discarded rows are kept for history; count the kept ones.
		for table, want := range map[string]int{"add_ons WHERE deleted_at IS NULL AND": 0, "fixed_charges WHERE deleted_at IS NULL AND": 0} {
			var n int
			if err := s.Admin.QueryRow(t.Context(), "SELECT count(*) FROM "+table+" organization_id = $1", s.Org.ID.UUID()).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != want {
				t.Errorf("got %s = %d, want = %d", table, n, want)
			}
		}
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		// source: spec/requests/api/v1/add_ons_controller_spec.rb:147
		s := rpctest.New(t)
		status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/add_ons/code%3Aunknown", nil)
		if status != http.StatusNotFound {
			t.Fatalf("got status = %d (%s), want = 404", status, body)
		}
	})
}

func TestListAddOns(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	vat := s.Fixtures.Tax("vat", "20")
	beer := s.Fixtures.AddOn(100, func(a *catalog.AddOn) { a.Code, a.Name = "beer", "Beer"; a.TaxIDs = []core.TaxID{vat.ID} })
	s.Clock.Advance(time.Second)
	wine := s.Fixtures.AddOn(100, func(a *catalog.AddOn) { a.Code, a.Name = "wine", "Wine" })

	type page struct {
		AddOns        []restAddOn `json:"add_ons"`
		NextPageToken string      `json:"next_page_token"`
	}
	list := func(t *testing.T, query string) page {
		t.Helper()
		status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/add_ons"+query, nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		return decode[page](t, body)
	}
	t.Run("all", func(t *testing.T) {
		// source: spec/requests/api/v1/add_ons_controller_spec.rb:164
		got := list(t, "").AddOns
		if len(got) != 2 || got[0].ID != wine.ID.String() || got[1].ID != beer.ID.String() {
			t.Fatalf("got = %+v, want = [wine beer]", got)
		}
		if len(got[1].TaxIDs) != 1 || got[1].TaxIDs[0] != vat.ID.String() {
			t.Errorf("got tax_ids = %v, want = [%s]", got[1].TaxIDs, vat.ID)
		}
	})
	t.Run("search", func(t *testing.T) {
		// source: spec/queries/add_ons_query_spec.rb:70
		if got := list(t, "?search=BEE").AddOns; len(got) != 1 || got[0].ID != beer.ID.String() {
			t.Errorf("got = %+v, want = [beer]", got)
		}
	})
	t.Run("pagination", func(t *testing.T) {
		// source: spec/requests/api/v1/add_ons_controller_spec.rb:180
		first := list(t, "?page_size=1")
		if len(first.AddOns) != 1 || first.NextPageToken == "" {
			t.Fatalf("got %d add-ons, token %q, want 1 and a token", len(first.AddOns), first.NextPageToken)
		}
		if got := list(t, "?page_size=1&page_token="+first.NextPageToken).AddOns; len(got) != 1 || got[0].ID != beer.ID.String() {
			t.Errorf("got = %+v, want = [beer]", got)
		}
	})
}
