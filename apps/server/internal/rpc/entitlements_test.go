package rpc_test

import (
	"maps"
	"net/http"
	"slices"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// restEntitlement is the JSON of an entitlement in a REST response.
type restEntitlement struct {
	FeatureID   string `json:"feature_id"`
	FeatureCode string `json:"feature_code"`
	FeatureName string `json:"feature_name"`
	Privileges  []struct {
		PrivilegeCode string `json:"privilege_code"`
		Value         string `json:"value"`
		Source        string `json:"source"`
	} `json:"privileges"`
	Source string `json:"source"`
}

// values maps the privilege codes of e to their values.
func (e restEntitlement) values() map[string]string {
	out := map[string]string{}
	for _, p := range e.Privileges {
		out[p.PrivilegeCode] = p.Value
	}
	return out
}

// entitlementFixture is a plan and an active subscription to it, with the
// feature seats: max (integer), reset (boolean) and signin (select of
// password and okta).
type entitlementFixture struct {
	s    *rpctest.Server
	plan *catalog.Plan
	sub  *subscription.Subscription
}

func newEntitlementFixture(t *testing.T) *entitlementFixture {
	t.Helper()
	s := rpctest.New(t)
	featureCreate(t, s, `{"code":"seats","name":"Seats","privileges":[
		{"code":"max","value_type":"PRIVILEGE_VALUE_TYPE_INTEGER"},
		{"code":"reset","value_type":"PRIVILEGE_VALUE_TYPE_BOOLEAN"},
		{"code":"signin","value_type":"PRIVILEGE_VALUE_TYPE_SELECT","select_options":["password","okta"]}]}`)
	p := s.Fixtures.Plan(period.Monthly, 1000)
	return &entitlementFixture{s: s, plan: p, sub: s.Fixtures.Subscription(s.Fixtures.Customer(), p)}
}

// planPath is the plan's entitlements path, with the rest appended.
func (f *entitlementFixture) planPath(rest string) string {
	return "/v1/plans/code%3A" + f.plan.Code + "/entitlements" + rest
}

// subPath is the subscription's entitlements path, with the rest appended.
func (f *entitlementFixture) subPath(rest string) string {
	return "/v1/subscriptions/external_id%3A" + f.sub.ExternalID + "/entitlements" + rest
}

// do sends the request and fails unless the status is want.
func (f *entitlementFixture) do(t *testing.T, method, path string, body any, want int) []byte {
	t.Helper()
	status, out := f.s.REST(t, f.s.APIKey, method, path, body)
	if status != want {
		t.Fatalf("%s %s: got status = %d (%s), want = %d", method, path, status, out, want)
	}
	return out
}

// grant sets the plan's values of seats.
func (f *entitlementFixture) grant(t *testing.T, privileges string) {
	t.Helper()
	f.do(t, http.MethodPatch, f.planPath("/code%3Aseats"), `{"privileges":`+privileges+`}`, http.StatusOK)
}

func (f *entitlementFixture) list(t *testing.T, path string) map[string]restEntitlement {
	t.Helper()
	ents := decode[struct {
		Entitlements []restEntitlement `json:"entitlements"`
	}](t, f.do(t, http.MethodGet, path, nil, http.StatusOK)).Entitlements
	out := map[string]restEntitlement{}
	for _, e := range ents {
		out[e.FeatureCode] = e
	}
	return out
}

func entitlementDecode(t *testing.T, body []byte) restEntitlement {
	t.Helper()
	return decode[struct {
		Entitlement restEntitlement `json:"entitlement"`
	}](t, body).Entitlement
}

func wantValues(t *testing.T, got, want map[string]string) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Errorf("got values = %v, want = %v", got, want)
	}
}

func entitlementsClient(s *rpctest.Server) catalogv1connect.EntitlementServiceClient {
	return catalogv1connect.NewEntitlementServiceClient(s.Connect(s.APIKey))
}

func TestListPlanEntitlements(t *testing.T) {
	t.Parallel()
	f := newEntitlementFixture(t)
	f.grant(t, `[{"privilege_code":"max","value":"30"}]`)

	t.Log("source: spec/requests/api/v1/plans/entitlements_controller_spec.rb:22")
	got := f.list(t, f.planPath(""))
	if len(got) != 1 || got["seats"].FeatureName != "Seats" || got["seats"].Source != "" {
		t.Fatalf("got entitlements = %+v, want = seats without a source", got)
	}
	wantValues(t, got["seats"].values(), map[string]string{"max": "30"})

	t.Log("source: spec/requests/api/v1/plans/entitlements_controller_spec.rb:34")
	f.do(t, http.MethodDelete, "/v1/features/code%3Aseats", nil, http.StatusOK)
	if got := f.list(t, f.planPath("")); len(got) != 0 {
		t.Errorf("got entitlements = %+v, want none after the feature is discarded", got)
	}
	if !slices.Contains(f.s.Webhooks(t), "plan.updated") {
		t.Errorf("got webhooks = %v, want = plan.updated", f.s.Webhooks(t))
	}
}

func TestGetPlanEntitlement(t *testing.T) {
	t.Parallel()
	f := newEntitlementFixture(t)
	f.grant(t, `[{"privilege_code":"max","value":"30"}]`)
	featureCreate(t, f.s, `{"code":"sso"}`)
	tests := []struct {
		name   string
		source string
		path   string
		status int
	}{
		{name: "found", source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:71", path: f.planPath("/code%3Aseats"), status: http.StatusOK},
		{name: "plan missing", source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:79", path: "/v1/plans/code%3Ainvalid_plan/entitlements/code%3Aseats", status: http.StatusNotFound},
		{name: "entitlement missing", source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:85", path: f.planPath("/code%3Asso"), status: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			body := f.do(t, http.MethodGet, tt.path, nil, tt.status)
			if tt.status == http.StatusOK {
				wantValues(t, entitlementDecode(t, body).values(), map[string]string{"max": "30"})
			}
		})
	}
}

func TestReplacePlanEntitlements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		// existing are the plan's seats values before.
		existing string
		body     string
		status   int
		want     map[string]map[string]string
		// field and reason are the violation of an InvalidArgument.
		field, reason string
	}{
		{
			name:   "creates",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:110",
			body:   `{"entitlements":[{"feature_id":"code:seats","privileges":[{"privilege_code":"max","value":"25"}]}]}`,
			status: http.StatusOK,
			want:   map[string]map[string]string{"seats": {"max": "25"}},
		},
		{
			name:     "replaces",
			source:   "spec/requests/api/v1/plans/entitlements_controller_spec.rb:129",
			existing: `[{"privilege_code":"max","value":"10"},{"privilege_code":"reset","value":"true"}]`,
			body:     `{"entitlements":[{"feature_id":"code:seats","privileges":[{"privilege_code":"max","value":"25"}]}]}`,
			status:   http.StatusOK,
			want:     map[string]map[string]string{"seats": {"max": "25"}},
		},
		{
			name:   "feature missing",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:148",
			body:   `{"entitlements":[{"feature_id":"code:nonexistent_feature","privileges":[{"privilege_code":"max","value":"25"}]}]}`,
			status: http.StatusNotFound,
		},
		{
			name:   "privilege missing",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:166",
			body:   `{"entitlements":[{"feature_id":"code:seats","privileges":[{"privilege_code":"nonexistent_privilege","value":"25"}]}]}`,
			status: http.StatusNotFound,
		},
		{
			name:   "invalid value",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:184",
			body:   `{"entitlements":[{"feature_id":"code:seats","privileges":[{"privilege_code":"max","value":"12 13"}]}]}`,
			status: http.StatusBadRequest,
			field:  "entitlements.max_privilege_value", reason: "value_is_invalid",
		},
		{
			name:   "not in the select options",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:209",
			body:   `{"entitlements":[{"feature_id":"code:seats","privileges":[{"privilege_code":"signin","value":"slack"}]}]}`,
			status: http.StatusBadRequest,
			field:  "entitlements.signin_privilege_value", reason: "value_not_in_select_options",
		},
		{
			name:     "empty",
			source:   "spec/requests/api/v1/plans/entitlements_controller_spec.rb:233",
			existing: `[{"privilege_code":"max","value":"10"}]`,
			body:     `{"entitlements":[]}`,
			status:   http.StatusOK,
			want:     map[string]map[string]string{},
		},
		{
			name:   "several privileges",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:258",
			body: `{"entitlements":[{"feature_id":"code:seats","privileges":[
				{"privilege_code":"max","value":"25"},{"privilege_code":"reset","value":"false"}]}]}`,
			status: http.StatusOK,
			want:   map[string]map[string]string{"seats": {"max": "25", "reset": "false"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newEntitlementFixture(t)
			if tt.existing != "" {
				f.grant(t, tt.existing)
			}
			body := f.do(t, http.MethodPut, f.planPath(""), tt.body, tt.status)
			if tt.field != "" {
				_, err := entitlementsClient(f.s).ReplaceEntitlements(t.Context(), decodeReplace(t, f, tt.body))
				v := rpc.ErrorViolations(err)
				if connect.CodeOf(err) != connect.CodeInvalidArgument || len(v) != 1 || v[0].Field != tt.field || v[0].Reason != tt.reason {
					t.Errorf("got %v (%+v), want = %s %s", err, v, tt.field, tt.reason)
				}
			}
			if tt.want == nil {
				return
			}
			got := map[string]map[string]string{}
			for _, e := range decode[struct {
				Entitlements []restEntitlement `json:"entitlements"`
			}](t, body).Entitlements {
				got[e.FeatureCode] = e.values()
			}
			if !maps.EqualFunc(got, tt.want, maps.Equal) {
				t.Errorf("got = %v, want = %v", got, tt.want)
			}
		})
	}
	t.Run("plan missing", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/plans/entitlements_controller_spec.rb:219")
		f := newEntitlementFixture(t)
		f.do(t, http.MethodPut, "/v1/plans/code%3Ainvalid_plan/entitlements", `{"entitlements":[]}`, http.StatusNotFound)
	})
}

// decodeReplace is the Connect request of a replace of the plan's
// entitlements with the REST body.
func decodeReplace(t *testing.T, f *entitlementFixture, body string) *catalogv1.ReplaceEntitlementsRequest {
	t.Helper()
	in := decode[struct {
		Entitlements []struct {
			FeatureID  string `json:"feature_id"`
			Privileges []struct {
				PrivilegeCode string `json:"privilege_code"`
				Value         string `json:"value"`
			} `json:"privileges"`
		} `json:"entitlements"`
	}](t, []byte(body))
	b := catalogv1.ReplaceEntitlementsRequest_builder{PlanId: new(f.plan.ID.String())}
	for _, e := range in.Entitlements {
		eb := catalogv1.Entitlement_builder{FeatureId: new(e.FeatureID)}
		for _, p := range e.Privileges {
			eb.Privileges = append(eb.Privileges, catalogv1.PrivilegeValue_builder{PrivilegeCode: new(p.PrivilegeCode), Value: new(p.Value)}.Build())
		}
		b.Entitlements = append(b.Entitlements, eb.Build())
	}
	return b.Build()
}

func TestUpsertPlanEntitlement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		// feature is the path's feature reference; "" is code:seats.
		feature string
		body    string
		status  int
		want    map[string]string
	}{
		{
			name:   "updates a value",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:299",
			body:   `{"privileges":[{"privilege_code":"max","value":"60"}]}`,
			status: http.StatusOK,
			want:   map[string]string{"max": "60"},
		},
		{
			name:   "adds a value",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:330",
			body:   `{"privileges":[{"privilege_code":"reset","value":"true"}]}`,
			status: http.StatusOK,
			want:   map[string]string{"max": "10", "reset": "true"},
		},
		{
			name:   "invalid value",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:355",
			body:   `{"privileges":[{"privilege_code":"max","value":"many"}]}`,
			status: http.StatusBadRequest,
		},
		{
			name:    "feature missing",
			source:  "spec/requests/api/v1/plans/entitlements_controller_spec.rb:405",
			feature: "code%3Anonexistent_feature",
			body:    `{"privileges":[{"privilege_code":"max","value":"60"}]}`,
			status:  http.StatusNotFound,
		},
		{
			name:   "privilege missing",
			source: "spec/requests/api/v1/plans/entitlements_controller_spec.rb:423",
			body:   `{"privileges":[{"privilege_code":"nonexistent_privilege","value":"60"}]}`,
			status: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newEntitlementFixture(t)
			f.grant(t, `[{"privilege_code":"max","value":"10"}]`)
			ref := tt.feature
			if ref == "" {
				ref = "code%3Aseats"
			}
			body := f.do(t, http.MethodPatch, f.planPath("/"+ref), tt.body, tt.status)
			if tt.want != nil {
				wantValues(t, entitlementDecode(t, body).values(), tt.want)
				wantValues(t, f.list(t, f.planPath(""))["seats"].values(), tt.want)
			}
		})
	}
	t.Run("new entitlement", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/plans/entitlements_controller_spec.rb:381")
		f := newEntitlementFixture(t)
		body := f.do(t, http.MethodPatch, f.planPath("/code%3Aseats"), `{"privileges":[{"privilege_code":"max","value":"60"}]}`, http.StatusOK)
		wantValues(t, entitlementDecode(t, body).values(), map[string]string{"max": "60"})
	})
	t.Run("plan missing", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/plans/entitlements_controller_spec.rb:431")
		f := newEntitlementFixture(t)
		f.do(t, http.MethodPatch, "/v1/plans/code%3Ainvalid_plan/entitlements/code%3Aseats", `{"privileges":[]}`, http.StatusNotFound)
	})
}

func TestDeletePlanEntitlement(t *testing.T) {
	t.Parallel()
	f := newEntitlementFixture(t)
	f.grant(t, `[{"privilege_code":"max","value":"10"}]`)
	featureCreate(t, f.s, `{"code":"sso"}`)

	t.Log("source: spec/requests/api/v1/plans/entitlements_controller_spec.rb:466")
	f.do(t, http.MethodDelete, f.planPath("/code%3Aseats"), nil, http.StatusOK)
	if got := f.list(t, f.planPath("")); len(got) != 0 {
		t.Errorf("got entitlements = %+v, want none", got)
	}
	t.Log("source: spec/requests/api/v1/plans/entitlements_controller_spec.rb:475")
	f.do(t, http.MethodDelete, "/v1/plans/code%3Ainvalid_plan/entitlements/code%3Aseats", nil, http.StatusNotFound)
	t.Log("source: spec/requests/api/v1/plans/entitlements_controller_spec.rb:481")
	f.do(t, http.MethodDelete, f.planPath("/code%3Asso"), nil, http.StatusNotFound)
}

func TestDeletePlanEntitlementPrivilege(t *testing.T) {
	t.Parallel()
	f := newEntitlementFixture(t)
	f.grant(t, `[{"privilege_code":"max","value":"10"},{"privilege_code":"reset","value":"true"}]`)
	featureCreate(t, f.s, `{"code":"sso"}`)

	t.Log("source: spec/requests/api/v1/plans/entitlements/privileges_controller_spec.rb:25")
	body := f.do(t, http.MethodDelete, f.planPath("/code%3Aseats/privileges/max"), nil, http.StatusOK)
	t.Log("source: spec/requests/api/v1/plans/entitlements/privileges_controller_spec.rb:50")
	wantValues(t, entitlementDecode(t, body).values(), map[string]string{"reset": "true"})
	tests := []struct {
		name   string
		source string
		path   string
	}{
		{name: "plan missing", source: "spec/requests/api/v1/plans/entitlements/privileges_controller_spec.rb:32", path: "/v1/plans/code%3Ainvalid_plan/entitlements/code%3Aseats/privileges/reset"},
		{name: "entitlement missing", source: "spec/requests/api/v1/plans/entitlements/privileges_controller_spec.rb:38", path: f.planPath("/code%3Asso/privileges/reset")},
		{name: "privilege missing", source: "spec/requests/api/v1/plans/entitlements/privileges_controller_spec.rb:44", path: f.planPath("/code%3Aseats/privileges/max")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f.do(t, http.MethodDelete, tt.path, nil, http.StatusNotFound)
		})
	}
}

func TestListSubscriptionEntitlements(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:27")
	f := newEntitlementFixture(t)
	f.grant(t, `[{"privilege_code":"max","value":"30"}]`)
	f.do(t, http.MethodPatch, f.subPath("/code%3Aseats"), `{"privileges":[{"privilege_code":"reset","value":"true"}]}`, http.StatusOK)

	got := f.list(t, f.subPath(""))
	e, ok := got["seats"]
	if len(got) != 1 || !ok || e.Source != "ENTITLEMENT_SOURCE_PLAN" {
		t.Fatalf("got entitlements = %+v, want = seats from the plan", got)
	}
	wantValues(t, e.values(), map[string]string{"max": "30", "reset": "true"})
	sources := map[string]string{}
	for _, p := range e.Privileges {
		sources[p.PrivilegeCode] = p.Source
	}
	wantValues(t, sources, map[string]string{"max": "ENTITLEMENT_SOURCE_PLAN", "reset": "ENTITLEMENT_SOURCE_SUBSCRIPTION"})

	if by := f.list(t, "/v1/subscriptions/"+f.sub.ID.String()+"/entitlements"); len(by) != 1 {
		t.Errorf("got entitlements by ID = %+v, want = seats", by)
	}
	t.Log("source: spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:57")
	f.do(t, http.MethodGet, "/v1/subscriptions/external_id%3Ainvalid_subscription/entitlements", nil, http.StatusNotFound)
}

func TestUpsertSubscriptionEntitlement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		// planned are the plan's seats values; "" grants none.
		planned string
		// feature is the path's feature reference; "" is code:seats.
		feature string
		body    string
		status  int
		want    map[string]string
	}{
		{
			name:    "overrides a plan value",
			source:  "spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:148",
			planned: `[{"privilege_code":"max","value":"10"}]`,
			body:    `{"privileges":[{"privilege_code":"max","value":"60"}]}`,
			status:  http.StatusOK,
			want:    map[string]string{"max": "60"},
		},
		{
			name:    "adds a value",
			source:  "spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:186",
			planned: `[{"privilege_code":"max","value":"10"}]`,
			body:    `{"privileges":[{"privilege_code":"reset","value":"true"}]}`,
			status:  http.StatusOK,
			want:    map[string]string{"max": "10", "reset": "true"},
		},
		{
			name:   "new entitlement",
			source: "spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:237",
			body:   `{"privileges":[{"privilege_code":"max","value":"60"}]}`,
			status: http.StatusOK,
			want:   map[string]string{"max": "60"},
		},
		{
			name:    "feature missing",
			source:  "spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:257",
			feature: "code%3Anonexistent_feature",
			body:    `{"privileges":[{"privilege_code":"max","value":"60"}]}`,
			status:  http.StatusNotFound,
		},
		{
			name:   "privilege missing",
			source: "spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:275",
			body:   `{"privileges":[{"privilege_code":"nonexistent_privilege","value":"60"}]}`,
			status: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newEntitlementFixture(t)
			if tt.planned != "" {
				f.grant(t, tt.planned)
			}
			ref := tt.feature
			if ref == "" {
				ref = "code%3Aseats"
			}
			body := f.do(t, http.MethodPatch, f.subPath("/"+ref), tt.body, tt.status)
			if tt.want == nil {
				return
			}
			wantValues(t, entitlementDecode(t, body).values(), tt.want)
			wantValues(t, f.list(t, f.subPath(""))["seats"].values(), tt.want)
			if !slices.Contains(f.s.Webhooks(t), "subscription.updated") {
				t.Errorf("got webhooks = %v, want = subscription.updated", f.s.Webhooks(t))
			}
		})
	}
	t.Run("restores a removed feature", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:211")
		f := newEntitlementFixture(t)
		f.grant(t, `[{"privilege_code":"max","value":"10"}]`)
		f.do(t, http.MethodDelete, f.subPath("/code%3Aseats"), nil, http.StatusOK)
		if got := f.list(t, f.subPath("")); len(got) != 0 {
			t.Fatalf("got entitlements = %+v, want none after the removal", got)
		}
		f.do(t, http.MethodPatch, f.subPath("/code%3Aseats"), `{"privileges":[{"privilege_code":"max","value":"60"}]}`, http.StatusOK)
		wantValues(t, f.list(t, f.subPath(""))["seats"].values(), map[string]string{"max": "60"})
	})
	t.Run("subscription missing", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:283")
		f := newEntitlementFixture(t)
		f.do(t, http.MethodPatch, "/v1/subscriptions/external_id%3Ainvalid_subscription/entitlements/code%3Aseats", `{"privileges":[]}`, http.StatusNotFound)
	})
}

func TestDeleteSubscriptionEntitlement(t *testing.T) {
	t.Parallel()
	t.Run("own entitlement", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:318")
		f := newEntitlementFixture(t)
		f.do(t, http.MethodPatch, f.subPath("/code%3Aseats"), `{"privileges":[{"privilege_code":"max","value":"60"}]}`, http.StatusOK)
		f.do(t, http.MethodDelete, f.subPath("/code%3Aseats"), nil, http.StatusOK)
		if got := f.list(t, f.subPath("")); len(got) != 0 {
			t.Errorf("got entitlements = %+v, want none", got)
		}
	})
	t.Run("plan feature", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:332")
		f := newEntitlementFixture(t)
		f.grant(t, `[{"privilege_code":"max","value":"10"}]`)
		f.do(t, http.MethodDelete, f.subPath("/code%3Aseats"), nil, http.StatusOK)
		if got := f.list(t, f.subPath("")); len(got) != 0 {
			t.Errorf("got entitlements = %+v, want none", got)
		}
		if got := f.list(t, f.planPath("")); len(got) != 1 {
			t.Errorf("got plan entitlements = %+v, want = seats (unchanged)", got)
		}
		t.Log("source: spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:352")
		f.do(t, http.MethodDelete, f.subPath("/code%3Aseats"), nil, http.StatusOK)
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		f := newEntitlementFixture(t)
		t.Log("source: spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:362")
		f.do(t, http.MethodDelete, "/v1/subscriptions/external_id%3Ainvalid_subscription/entitlements/code%3Aseats", nil, http.StatusNotFound)
		t.Log("source: spec/requests/api/v1/subscriptions/entitlements_controller_spec.rb:368")
		f.do(t, http.MethodDelete, f.subPath("/code%3Ainvalid_feature"), nil, http.StatusNotFound)
	})
}

func TestDeleteSubscriptionEntitlementPrivilege(t *testing.T) {
	t.Parallel()
	f := newEntitlementFixture(t)
	f.grant(t, `[{"privilege_code":"max","value":"10"}]`)
	f.do(t, http.MethodPatch, f.subPath("/code%3Aseats"), `{"privileges":[{"privilege_code":"reset","value":"true"}]}`, http.StatusOK)

	t.Log("source: spec/requests/api/v1/subscriptions/entitlements/privileges_controller_spec.rb:23")
	body := f.do(t, http.MethodDelete, f.subPath("/code%3Aseats/privileges/reset"), nil, http.StatusOK)
	t.Log("source: spec/requests/api/v1/subscriptions/entitlements/privileges_controller_spec.rb:29")
	wantValues(t, entitlementDecode(t, body).values(), map[string]string{"max": "10"})

	t.Log("source: spec/requests/api/v1/subscriptions/entitlements/privileges_controller_spec.rb:55")
	body = f.do(t, http.MethodDelete, f.subPath("/code%3Aseats/privileges/max"), nil, http.StatusOK)
	wantValues(t, entitlementDecode(t, body).values(), map[string]string{})
	wantValues(t, f.list(t, f.planPath(""))["seats"].values(), map[string]string{"max": "10"})
	t.Log("source: spec/requests/api/v1/subscriptions/entitlements/privileges_controller_spec.rb:65")
	f.do(t, http.MethodDelete, f.subPath("/code%3Aseats/privileges/max"), nil, http.StatusOK)

	tests := []struct {
		name   string
		source string
		path   string
	}{
		{name: "subscription missing", source: "spec/requests/api/v1/subscriptions/entitlements/privileges_controller_spec.rb:33", path: "/v1/subscriptions/external_id%3Ainvalid/entitlements/code%3Aseats/privileges/max"},
		{name: "feature missing", source: "spec/requests/api/v1/subscriptions/entitlements/privileges_controller_spec.rb:39", path: f.subPath("/code%3Ainvalid/privileges/max")},
		{name: "privilege missing", source: "spec/requests/api/v1/subscriptions/entitlements/privileges_controller_spec.rb:45", path: f.subPath("/code%3Aseats/privileges/invalid")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f.do(t, http.MethodDelete, tt.path, nil, http.StatusNotFound)
		})
	}
}

// TestSubscriptionEntitlementsUnimplemented checks the RPCs without a
// subscription route: no single read (lago routes index only) and no
// replace (lago updates subscriptions with partial: true only).
func TestSubscriptionEntitlementsUnimplemented(t *testing.T) {
	t.Parallel()
	f := newEntitlementFixture(t)
	f.do(t, http.MethodGet, f.subPath("/code%3Aseats"), nil, http.StatusNotImplemented)
	f.do(t, http.MethodPut, f.subPath(""), `{"entitlements":[]}`, http.StatusNotImplemented)
	f.do(t, http.MethodGet, "/v1/subscriptions/code%3Ax/entitlements", nil, http.StatusBadRequest)
}
