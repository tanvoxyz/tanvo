package rpc_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// restPricingUnit is the JSON of a pricing unit in a REST response.
type restPricingUnit struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	ShortName   string `json:"short_name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

type restPricingUnitBody struct {
	PricingUnit restPricingUnit `json:"pricing_unit"`
}

// createPricingUnit creates a pricing unit through the API.
func createPricingUnit(t *testing.T, s *rpctest.Server, code, name string) restPricingUnit {
	t.Helper()
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/pricing_units",
		`{"code":"`+code+`","name":"`+name+`","short_name":"CT","description":"desc"}`)
	if status != http.StatusOK {
		t.Fatalf("create pricing unit: got status = %d (%s), want = 200", status, body)
	}
	return decode[restPricingUnitBody](t, body).PricingUnit
}

func TestCreatePricingUnit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		status int
	}{
		{
			"creates", "spec/graphql/mutations/pricing_units/create_spec.rb:48",
			`{"code":"cloud_token","name":"Cloud token","short_name":"CT","description":"desc"}`, http.StatusOK,
		},
		{
			"blank code", "spec/graphql/mutations/pricing_units/create_spec.rb:65",
			`{"code":"","name":"Cloud token","short_name":"CT"}`, http.StatusBadRequest,
		},
		{
			"used code", "spec/services/pricing_units/create_service_spec.rb:52",
			`{"code":"used","name":"Cloud token","short_name":"CT"}`, http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			createPricingUnit(t, s, "used", "Used")
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/pricing_units", tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[restPricingUnitBody](t, body).PricingUnit
			if got.ID == "" || got.CreatedAt == "" || got.Code != "cloud_token" || got.Name != "Cloud token" ||
				got.ShortName != "CT" || got.Description != "desc" {
				t.Errorf("got %+v, want the created attributes", got)
			}
		})
	}
}

func TestUpdatePricingUnit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		// ref is the path reference; "" (or a query alone) is the unit's ID.
		ref    string
		body   string
		status int
	}{
		{
			"updates", "spec/graphql/mutations/pricing_units/update_spec.rb:39", "",
			`{"name":"Updated Name","short_name":"CR","description":"Updated Description"}`, http.StatusOK,
		},
		{"blank name", "spec/graphql/mutations/pricing_units/update_spec.rb:56", "", `{"name":""}`, http.StatusBadRequest},
		{
			"not found", "spec/graphql/mutations/pricing_units/update_spec.rb:69",
			core.NewID[core.PricingUnitID](core.UUIDv7Generator{}).String(), `{"name":"x"}`, http.StatusNotFound,
		},
		{
			"code is immutable", "app/graphql/types/pricing_units/update_input.rb", "?update_mask=code",
			`{"code":"other"}`, http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			u := createPricingUnit(t, s, "cloud_token", "Cloud token")
			ref := tt.ref
			if ref == "" || ref[0] == '?' {
				ref = u.ID + ref
			}
			status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/pricing_units/"+ref, tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[restPricingUnitBody](t, body).PricingUnit
			if got.ID != u.ID || got.Code != "cloud_token" || got.Name != "Updated Name" || got.ShortName != "CR" ||
				got.Description != "Updated Description" {
				t.Errorf("got %+v, want the updated attributes", got)
			}
		})
	}
}

func TestGetPricingUnit(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	u := createPricingUnit(t, s, "cloud_token", "Cloud token")
	tests := []struct {
		name   string
		source string
		ref    string
		status int
	}{
		{"by id", "spec/graphql/resolvers/pricing_unit_resolver_spec.rb:37", u.ID, http.StatusOK},
		{"by code", "spec/graphql/resolvers/pricing_unit_resolver_spec.rb:37", "code%3Acloud_token", http.StatusOK},
		{"not found", "spec/graphql/resolvers/pricing_unit_resolver_spec.rb:52", "code%3Aunknown", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/pricing_units/"+tt.ref, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			if got := decode[restPricingUnitBody](t, body).PricingUnit; got != u {
				t.Errorf("got %+v, want = %+v", got, u)
			}
		})
	}
}

func TestListPricingUnits(t *testing.T) {
	t.Parallel()
	// source: spec/graphql/resolvers/pricing_units_resolver_spec.rb:47
	// source: spec/graphql/resolvers/pricing_units_resolver_spec.rb:12
	s := rpctest.New(t)
	compute := createPricingUnit(t, s, "compute", "Compute token")
	cloud := createPricingUnit(t, s, "cloud", "Cloud token")
	type page struct {
		PricingUnits  []restPricingUnit `json:"pricing_units"`
		NextPageToken string            `json:"next_page_token"`
	}
	list := func(query string) page {
		status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/pricing_units"+query, nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		return decode[page](t, body)
	}
	first := list("?page_size=1")
	if len(first.PricingUnits) != 1 || first.PricingUnits[0].ID != cloud.ID || first.NextPageToken == "" {
		t.Fatalf("got %+v, want [cloud] and a token", first)
	}
	second := list("?page_size=1&page_token=" + first.NextPageToken)
	if len(second.PricingUnits) != 1 || second.PricingUnits[0].ID != compute.ID {
		t.Errorf("got %+v, want = [compute]", second.PricingUnits)
	}
	createPricingUnit(t, s, "seat", "Seat")
	for _, tt := range []struct{ search, want string }{
		{"token", "cloud,compute"},
		{"comp", "compute"},
		{"seat", "seat"},
	} {
		got := list("?search=" + tt.search).PricingUnits
		codes := make([]string, len(got))
		for i, u := range got {
			codes[i] = u.Code
		}
		if g := strings.Join(codes, ","); g != tt.want {
			t.Errorf("search %q: got = %s, want = %s", tt.search, g, tt.want)
		}
	}
}
