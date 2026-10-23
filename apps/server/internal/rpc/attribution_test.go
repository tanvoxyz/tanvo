package rpc_test

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
)

// restAttributionType is the JSON of a usage attribution type in a REST
// response.
type restAttributionType struct {
	ID              string   `json:"id"`
	Code            string   `json:"code"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	AttributionKeys []string `json:"attribution_keys"`
	Role            string   `json:"role"`
	ParentID        string   `json:"parent_id"`
	ParentCode      string   `json:"parent_code"`
	CreatedAt       string   `json:"created_at"`
}

type restAttributionTypeBody struct {
	UsageAttributionType restAttributionType `json:"usage_attribution_type"`
}

type restAttributionTypeList struct {
	UsageAttributionTypes []restAttributionType `json:"usage_attribution_types"`
}

// createAttributionType creates a type through the API; parent is a
// reference or "".
func createAttributionType(t *testing.T, s *rpctest.Server, code, role, parent string, keys ...string) restAttributionType {
	t.Helper()
	body := map[string]any{"code": code, "name": strings.ToUpper(code), "attribution_keys": keys, "role": role}
	if parent != "" {
		body["parent_id"] = parent
	}
	status, out := s.REST(t, s.APIKey, http.MethodPost, "/v1/usage_attribution_types", body)
	if status != http.StatusOK {
		t.Fatalf("create usage attribution type: got status = %d (%s), want = 200", status, out)
	}
	return decode[restAttributionTypeBody](t, out).UsageAttributionType
}

const (
	roleHierarchical = "USAGE_ATTRIBUTION_ROLE_HIERARCHICAL"
	roleFlat         = "USAGE_ATTRIBUTION_ROLE_FLAT"
)

func TestCreateUsageAttributionType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		status int
		want   string // a substring of the response body
	}{
		{
			"creates", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:26",
			`{"code":"user","name":"User","description":"A person","attribution_keys":["user_id"],"role":"USAGE_ATTRIBUTION_ROLE_HIERARCHICAL"}`,
			http.StatusOK, `"code":"user"`,
		},
		{
			"parent code", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:54",
			`{"code":"user","attribution_keys":["user_id"],"role":"USAGE_ATTRIBUTION_ROLE_HIERARCHICAL","parent_id":"code:department"}`,
			http.StatusOK, `"parent_code":"department"`,
		},
		{
			"unknown parent", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:75",
			`{"code":"user","attribution_keys":["user_id"],"role":"USAGE_ATTRIBUTION_ROLE_HIERARCHICAL","parent_id":"code:unknown"}`,
			http.StatusNotFound, "parent_usage_attribution_type",
		},
		{
			"code taken", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:113",
			`{"code":"department","attribution_keys":["other_id"],"role":"USAGE_ATTRIBUTION_ROLE_HIERARCHICAL"}`,
			http.StatusBadRequest, "value_already_exist",
		},
		{
			"role missing", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:132",
			`{"code":"user","attribution_keys":["user_id"]}`,
			http.StatusBadRequest, "",
		},
		{
			"payload missing", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:143",
			`{}`, http.StatusBadRequest, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			createAttributionType(t, s, "department", roleHierarchical, "", "department_id")
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/usage_attribution_types", tt.body)
			if status != tt.status || !strings.Contains(string(body), tt.want) {
				t.Fatalf("got = %d %s, want = %d with %q", status, body, tt.status, tt.want)
			}
		})
	}
}

func TestListUsageAttributionTypes(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	dep := createAttributionType(t, s, "department", roleHierarchical, "", "department_id")
	s.Clock.Advance(time.Minute)
	createAttributionType(t, s, "user", roleHierarchical, dep.ID, "user_id")
	s.Clock.Advance(time.Minute)
	createAttributionType(t, s, "model", roleFlat, "", "model_name")
	tests := []struct {
		name   string
		source string
		query  string
		status int
		want   []string
	}{
		{"all", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:173", "", http.StatusOK, []string{"model", "user", "department"}},
		{"role", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:184", "filter=" + url.QueryEscape(`role = "flat"`), http.StatusOK, []string{"model"}},
		{"invalid role", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:194", "filter=" + url.QueryEscape(`role = "unknown"`), http.StatusBadRequest, nil},
		{"search", "spec/requests/api/v1/usage_attribution_types_controller_spec.rb:204", "search=depart", http.StatusOK, []string{"department"}},
		{"roots", "spec/queries/usage_attribution_types_query_spec.rb:65", "filter=" + url.QueryEscape(`root = true`), http.StatusOK, []string{"model", "department"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/usage_attribution_types?"+tt.query, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			var codes []string
			for _, x := range decode[restAttributionTypeList](t, body).UsageAttributionTypes {
				codes = append(codes, x.Code)
			}
			if !slices.Equal(codes, tt.want) {
				t.Errorf("codes got = %v, want = %v", codes, tt.want)
			}
		})
	}
}

func TestGetUsageAttributionType(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	dep := createAttributionType(t, s, "department", roleHierarchical, "", "department_id")
	// source: spec/requests/api/v1/usage_attribution_types_controller_spec.rb:229
	for _, ref := range []string{dep.ID, "code%3Adepartment"} {
		status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/usage_attribution_types/"+ref, nil)
		if status != http.StatusOK {
			t.Fatalf("get %s: got status = %d (%s), want = 200", ref, status, body)
		}
		if got := decode[restAttributionTypeBody](t, body).UsageAttributionType; got.ID != dep.ID || got.Code != "department" {
			t.Errorf("get %s: got = %+v", ref, got)
		}
	}
	// source: spec/requests/api/v1/usage_attribution_types_controller_spec.rb:241
	status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/usage_attribution_types/code%3Aunknown", nil)
	if status != http.StatusNotFound {
		t.Errorf("unknown: got status = %d (%s), want = 404", status, body)
	}
}

func TestUpdateUsageAttributionType(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	dep := createAttributionType(t, s, "department", roleHierarchical, "", "department_id")
	user := createAttributionType(t, s, "user", roleHierarchical, "", "user_id")
	patch := func(body string) (int, restAttributionType, string) {
		t.Helper()
		status, out := s.REST(t, s.APIKey, http.MethodPatch, "/v1/usage_attribution_types/"+user.ID, body)
		if status != http.StatusOK {
			return status, restAttributionType{}, string(out)
		}
		return status, decode[restAttributionTypeBody](t, out).UsageAttributionType, string(out)
	}

	// source: spec/requests/api/v1/usage_attribution_types_controller_spec.rb:271
	// source: spec/requests/api/v1/usage_attribution_types_controller_spec.rb:279
	if status, got, body := patch(`{"name":"Seat"}`); status != http.StatusOK || got.Name != "Seat" || !slices.Equal(got.AttributionKeys, []string{"user_id"}) {
		t.Errorf("name: got = %d %s", status, body)
	}
	// source: spec/requests/api/v1/usage_attribution_types_controller_spec.rb:289
	if status, got, body := patch(`{"description":"A seat in the product"}`); status != http.StatusOK || got.Description != "A seat in the product" {
		t.Errorf("description: got = %d %s", status, body)
	}
	// source: spec/requests/api/v1/usage_attribution_types_controller_spec.rb:301
	if status, got, body := patch(`{"parent_id":"code:department"}`); status != http.StatusOK || got.ParentID != dep.ID || got.ParentCode != "department" {
		t.Errorf("parent: got = %d %s", status, body)
	}
	// source: spec/requests/api/v1/usage_attribution_types_controller_spec.rb:315
	status, out := s.REST(t, s.APIKey, http.MethodPatch, "/v1/usage_attribution_types/"+user.ID+"?update_mask=parentId", `{}`)
	if got := decode[restAttributionTypeBody](t, out).UsageAttributionType; status != http.StatusOK || got.ParentID != "" {
		t.Errorf("detach: got = %d %s", status, out)
	}
	// source: spec/requests/api/v1/usage_attribution_types_controller_spec.rb:326
	if status, _, body := patch(`{"parent_id":"code:unknown"}`); status != http.StatusNotFound || !strings.Contains(body, "parent_usage_attribution_type") {
		t.Errorf("unknown parent: got = %d %s", status, body)
	}
	// source: spec/services/usage_attribution_types/update_service_spec.rb:132
	if status, _, body := patch(`{"role":"USAGE_ATTRIBUTION_ROLE_FLAT"}`); status != http.StatusOK {
		t.Errorf("flat: got = %d %s", status, body)
	}
}

func TestDeleteUsageAttributionType(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	dep := createAttributionType(t, s, "department", roleHierarchical, "", "department_id")
	child := createAttributionType(t, s, "user", roleHierarchical, dep.ID, "user_id")
	// source: spec/services/usage_attribution_types/destroy_service_spec.rb:11
	status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/usage_attribution_types/code%3Adepartment", nil)
	if status != http.StatusOK {
		t.Fatalf("delete: got = %d %s, want = 200", status, body)
	}
	status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/usage_attribution_types/"+dep.ID, nil)
	if status != http.StatusNotFound {
		t.Errorf("get deleted: got = %d %s, want = 404", status, body)
	}
	// source: spec/services/usage_attribution_types/destroy_service_spec.rb:46
	status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/usage_attribution_types/"+child.ID, nil)
	if got := decode[restAttributionTypeBody](t, body).UsageAttributionType; status != http.StatusOK || got.ParentID != dep.ID || got.ParentCode != "department" {
		t.Errorf("child: got = %d %s, want = naming its deleted parent", status, body)
	}
	// The code is free again (spec/models/usage_attribution_type_spec.rb:62).
	createAttributionType(t, s, "department", roleHierarchical, "", "department_id")
}
