package rpc_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
)

// restPrivilege is the JSON of a privilege in a REST response.
type restPrivilege struct {
	Code          string   `json:"code"`
	Name          *string  `json:"name"`
	ValueType     string   `json:"value_type"`
	SelectOptions []string `json:"select_options"`
}

// restFeature is the JSON of a feature in a REST response.
type restFeature struct {
	ID          string          `json:"id"`
	Code        string          `json:"code"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Privileges  []restPrivilege `json:"privileges"`
	CreatedAt   string          `json:"created_at"`
}

// privilege returns the privilege code of f, failing when it is missing.
func (f restFeature) privilege(t *testing.T, code string) restPrivilege {
	t.Helper()
	i := slices.IndexFunc(f.Privileges, func(p restPrivilege) bool { return p.Code == code })
	if i < 0 {
		t.Fatalf("got privileges = %+v, want = %s", f.Privileges, code)
	}
	return f.Privileges[i]
}

func featureDecode(t *testing.T, body []byte) restFeature {
	t.Helper()
	return decode[struct {
		Feature restFeature `json:"feature"`
	}](t, body).Feature
}

// featureSeats is the feature of the request specs: seats, "Number of
// seats", with the integer privileges max ("Maximum") and max_admins
// (named "").
const featureSeats = `{"code":"seats","name":"Number of seats","description":"Number of users of the account","privileges":[
	{"code":"max","name":"Maximum","value_type":"PRIVILEGE_VALUE_TYPE_INTEGER"},
	{"code":"max_admins","name":"","value_type":"PRIVILEGE_VALUE_TYPE_INTEGER"}]}`

// featureCreate creates the feature of the JSON body.
func featureCreate(t *testing.T, s *rpctest.Server, body string) restFeature {
	t.Helper()
	status, out := s.REST(t, s.APIKey, http.MethodPost, "/v1/features", body)
	if status != http.StatusOK {
		t.Fatalf("create feature: got status = %d (%s), want = 200", status, out)
	}
	return featureDecode(t, out)
}

func featureStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestCreateFeature(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		status int
		check  func(t *testing.T, f restFeature)
	}{
		{
			name:   "with privileges",
			source: "spec/requests/api/v1/features_controller_spec.rb:42",
			body: `{"code":"new_feature","name":"New Feature","description":"A new feature","privileges":[
				{"code":"priv1","value_type":"PRIVILEGE_VALUE_TYPE_STRING"},
				{"code":"priv2","name":"Maximum","value_type":"PRIVILEGE_VALUE_TYPE_INTEGER"},
				{"code":"priv3","value_type":"PRIVILEGE_VALUE_TYPE_BOOLEAN"},
				{"code":"priv4","name":"SELECT","value_type":"PRIVILEGE_VALUE_TYPE_SELECT","select_options":["a","b","c"]}]}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if f.ID == "" || f.Code != "new_feature" || featureStr(f.Name) != "New Feature" || featureStr(f.Description) != "A new feature" {
					t.Errorf("got %+v, want the created attributes", f)
				}
				if p := f.privilege(t, "priv1"); p.Name != nil || p.ValueType != "PRIVILEGE_VALUE_TYPE_STRING" {
					t.Errorf("got priv1 = %+v, want an unnamed string", p)
				}
				if p := f.privilege(t, "priv2"); featureStr(p.Name) != "Maximum" || p.ValueType != "PRIVILEGE_VALUE_TYPE_INTEGER" {
					t.Errorf("got priv2 = %+v, want Maximum integer", p)
				}
				if p := f.privilege(t, "priv4"); !slices.Equal(p.SelectOptions, []string{"a", "b", "c"}) {
					t.Errorf("got priv4 options = %v, want = [a b c]", p.SelectOptions)
				}
			},
		},
		{
			name:   "code missing",
			source: "spec/requests/api/v1/features_controller_spec.rb:87",
			body:   `{"name":"Feature"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "code empty",
			source: "spec/requests/api/v1/features_controller_spec.rb:106",
			body:   `{"code":"","name":"Feature"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "invalid value type",
			source: "spec/requests/api/v1/features_controller_spec.rb:126",
			body:   `{"code":"f","privileges":[{"code":"p","value_type":99}]}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "privilege code empty",
			source: "spec/requests/api/v1/features_controller_spec.rb:148",
			body:   `{"code":"f","privileges":[{"code":"","value_type":"PRIVILEGE_VALUE_TYPE_STRING"}]}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "no privileges",
			source: "spec/requests/api/v1/features_controller_spec.rb:167",
			body:   `{"code":"f","name":"Feature"}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if len(f.Privileges) != 0 {
					t.Errorf("got privileges = %+v, want none", f.Privileges)
				}
			},
		},
		{
			name:   "only the code",
			source: "spec/requests/api/v1/features_controller_spec.rb:188",
			body:   `{"code":"f"}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if f.Code != "f" || f.Name != nil || f.Description != nil {
					t.Errorf("got %+v, want code f without name and description", f)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/features", tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, featureDecode(t, body))
			}
		})
	}
}

func TestCreateFeatureDuplicateCode(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/requests/api/v1/features_controller_spec.rb:69")
	s := rpctest.New(t)
	featureCreate(t, s, featureSeats)
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/features", featureSeats)
	if status != http.StatusBadRequest {
		t.Fatalf("got status = %d (%s), want = 400", status, body)
	}
	webhooks := s.Webhooks(t)
	if got := slices.Index(webhooks, "feature.created"); got < 0 || slices.Index(webhooks[got+1:], "feature.created") >= 0 {
		t.Errorf("got webhooks = %v, want = one feature.created", webhooks)
	}
}

func TestListFeatures(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	for _, body := range []string{`{"code":"seats","name":"Seats"}`, `{"code":"storage","name":"Storage"}`, `{"code":"sso","name":"SSO","description":"Single sign-on"}`} {
		featureCreate(t, s, body)
		s.Clock.Advance(time.Second)
	}
	type page struct {
		Features      []restFeature `json:"features"`
		NextPageToken string        `json:"next_page_token"`
	}
	tests := []struct {
		name   string
		source string
		query  string
		want   []string
		next   bool
	}{
		{name: "all, newest first", source: "spec/requests/api/v1/features_controller_spec.rb:204", want: []string{"sso", "storage", "seats"}},
		{name: "paginated", source: "spec/requests/api/v1/features_controller_spec.rb:244", query: "?page_size=1", want: []string{"sso"}, next: true},
		{name: "search", source: "spec/requests/api/v1/features_controller_spec.rb:258", query: "?search=sto", want: []string{"storage"}},
		{name: "search on the description", source: "app/queries/entitlement/features_query.rb:18", query: "?search=sign", want: []string{"sso"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/features"+tt.query, nil)
			if status != http.StatusOK {
				t.Fatalf("got status = %d (%s), want = 200", status, body)
			}
			p := decode[page](t, body)
			var got []string
			for _, f := range p.Features {
				got = append(got, f.Code)
			}
			if !slices.Equal(got, tt.want) || (p.NextPageToken != "") != tt.next {
				t.Errorf("got = %v (next %q), want = %v (next %v)", got, p.NextPageToken, tt.want, tt.next)
			}
		})
	}
}

func TestListFeaturesOtherOrganization(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/requests/api/v1/features_controller_spec.rb:230")
	s := rpctest.New(t)
	other := rpctest.New(t)
	featureCreate(t, other, `{"code":"other_feature"}`)
	status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/features", nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if got := decode[struct {
		Features []restFeature `json:"features"`
	}](t, body).Features; len(got) != 0 {
		t.Errorf("got features = %+v, want none", got)
	}
}

func TestGetFeature(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	f := featureCreate(t, s, featureSeats)
	deleted := featureCreate(t, s, `{"code":"deleted"}`)
	if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/features/"+deleted.ID, nil); status != http.StatusOK {
		t.Fatalf("delete: got status = %d (%s), want = 200", status, body)
	}
	tests := []struct {
		name   string
		source string
		ref    string
		status int
	}{
		{name: "by code", source: "spec/requests/api/v1/features_controller_spec.rb:273", ref: "code%3Aseats", status: http.StatusOK},
		{name: "by ID", source: "spec/requests/api/v1/features_controller_spec.rb:273", ref: f.ID, status: http.StatusOK},
		{name: "missing", source: "spec/requests/api/v1/features_controller_spec.rb:289", ref: "code%3Anon_existent", status: http.StatusNotFound},
		{name: "deleted", source: "spec/requests/api/v1/features_controller_spec.rb:298", ref: "code%3Adeleted", status: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/features/"+tt.ref, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.status != http.StatusOK {
				return
			}
			got := featureDecode(t, body)
			if got.ID != f.ID || len(got.Privileges) != 2 || featureStr(got.privilege(t, "max").Name) != "Maximum" {
				t.Errorf("got %+v, want the seats feature", got)
			}
		})
	}
}

func TestUpdateFeature(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		// ref is the path reference; "" is code:seats.
		ref    string
		body   string
		status int
		check  func(t *testing.T, f restFeature)
	}{
		{
			name:   "feature and privilege attributes",
			source: "spec/requests/api/v1/features_controller_spec.rb:321",
			body:   `{"name":"Updated Feature Name","description":"Updated feature description","privileges":[{"code":"max","name":"Max."}]}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if featureStr(f.Name) != "Updated Feature Name" || featureStr(f.Description) != "Updated feature description" {
					t.Errorf("got %+v, want the updated name and description", f)
				}
				if got := featureStr(f.privilege(t, "max").Name); got != "Max." {
					t.Errorf("got max name = %q, want = Max.", got)
				}
				if got := featureStr(f.privilege(t, "max_admins").Name); got != "" {
					t.Errorf("got max_admins name = %q, want = \"\" (unchanged)", got)
				}
				if got := f.privilege(t, "max").ValueType; got != "PRIVILEGE_VALUE_TYPE_INTEGER" {
					t.Errorf("got max value type = %s, want = integer (unchanged)", got)
				}
			},
		},
		{
			name:   "only the attributes given",
			source: "spec/requests/api/v1/features_controller_spec.rb:331",
			body:   `{"description":"Updated feature description"}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if featureStr(f.Name) != "Number of seats" || featureStr(f.Description) != "Updated feature description" {
					t.Errorf("got %+v, want the name unchanged", f)
				}
			},
		},
		{
			name:   "new privilege",
			source: "spec/requests/api/v1/features_controller_spec.rb:342",
			body:   `{"privileges":[{"code":"max","name":"Max."},{"code":"nonexistent","name":"New Name"}]}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if got := featureStr(f.privilege(t, "nonexistent").Name); got != "New Name" {
					t.Errorf("got nonexistent name = %q, want = New Name", got)
				}
				if got := f.privilege(t, "nonexistent").ValueType; got != "PRIVILEGE_VALUE_TYPE_STRING" {
					t.Errorf("got nonexistent value type = %s, want = string", got)
				}
			},
		},
		{
			name:   "only feature attributes",
			source: "spec/requests/api/v1/features_controller_spec.rb:362",
			body:   `{"name":"Updated Feature Name","description":"Updated feature description"}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if got := featureStr(f.privilege(t, "max").Name); got != "Maximum" {
					t.Errorf("got max name = %q, want = Maximum (unchanged)", got)
				}
			},
		},
		{
			name:   "only privilege names",
			source: "spec/requests/api/v1/features_controller_spec.rb:384",
			body:   `{"privileges":[{"code":"max","name":"Max."},{"code":"max_admins","name":"Max Admins"}]}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if featureStr(f.Name) != "Number of seats" || featureStr(f.Description) != "Number of users of the account" {
					t.Errorf("got %+v, want the feature attributes unchanged", f)
				}
				if featureStr(f.privilege(t, "max").Name) != "Max." || featureStr(f.privilege(t, "max_admins").Name) != "Max Admins" {
					t.Errorf("got privileges = %+v, want Max. and Max Admins", f.Privileges)
				}
			},
		},
		{
			name:   "missing",
			source: "spec/requests/api/v1/features_controller_spec.rb:398",
			ref:    "code%3Anon_existent",
			body:   `{"name":"x"}`,
			status: http.StatusNotFound,
		},
		{
			name:   "empty privilege name",
			source: "spec/requests/api/v1/features_controller_spec.rb:426",
			body:   `{"privileges":[{"code":"max","name":""}]}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if got := featureStr(f.privilege(t, "max").Name); got != "" {
					t.Errorf("got max name = %q, want = \"\"", got)
				}
			},
		},
		{
			name:   "empty feature name",
			source: "spec/requests/api/v1/features_controller_spec.rb:443",
			body:   `{"name":""}`,
			status: http.StatusOK,
			check: func(t *testing.T, f restFeature) {
				if got := featureStr(f.Name); got != "" {
					t.Errorf("got name = %q, want = \"\"", got)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			featureCreate(t, s, featureSeats)
			ref := tt.ref
			if ref == "" {
				ref = "code%3Aseats"
			}
			status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/features/"+ref, tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, featureDecode(t, body))
			}
		})
	}
}

func TestUpdateFeatureOtherOrganization(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/requests/api/v1/features_controller_spec.rb:409")
	s := rpctest.New(t)
	other := rpctest.New(t)
	f := featureCreate(t, other, `{"code":"other_feature"}`)
	if status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/features/"+f.ID, `{"name":"x"}`); status != http.StatusNotFound {
		t.Fatalf("got status = %d (%s), want = 404", status, body)
	}
}

func TestDeleteFeature(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/requests/api/v1/features_controller_spec.rb:457")
	s := rpctest.New(t)
	f := featureCreate(t, s, featureSeats)
	status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/features/code%3Aseats", nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	t.Log("source: spec/requests/api/v1/features_controller_spec.rb:465")
	if got := featureDecode(t, body); got.ID != f.ID || len(got.Privileges) != 2 {
		t.Errorf("got %+v, want the discarded feature with its privileges", got)
	}
	if !slices.Contains(s.Webhooks(t), "feature.deleted") {
		t.Errorf("got webhooks = %v, want = feature.deleted", s.Webhooks(t))
	}
	tests := []struct {
		name   string
		source string
		ref    string
	}{
		{name: "missing", source: "spec/requests/api/v1/features_controller_spec.rb:477", ref: "code%3Anon_existent"},
		{name: "already discarded", source: "spec/requests/api/v1/features_controller_spec.rb:497", ref: f.ID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/features/"+tt.ref, nil); status != http.StatusNotFound {
				t.Fatalf("got status = %d (%s), want = 404", status, body)
			}
		})
	}
}

func TestDeleteFeaturePrivilege(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	featureCreate(t, s, featureSeats)
	featureCreate(t, s, `{"code":"storage","privileges":[{"code":"quota","value_type":"PRIVILEGE_VALUE_TYPE_INTEGER"}]}`)
	t.Log("source: spec/requests/api/v1/features/privileges_controller_spec.rb:21, 25")
	status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/features/code%3Aseats/privileges/max_admins", nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if got := featureDecode(t, body); got.Code != "seats" || len(got.Privileges) != 1 || got.Privileges[0].Code != "max" {
		t.Errorf("got %+v, want seats with the max privilege only", got)
	}
	tests := []struct {
		name   string
		source string
		path   string
	}{
		{name: "missing feature", source: "spec/requests/api/v1/features/privileges_controller_spec.rb:36", path: "code%3Anone/privileges/max"},
		{name: "missing privilege", source: "spec/requests/api/v1/features/privileges_controller_spec.rb:45", path: "code%3Aseats/privileges/none"},
		{name: "privilege of another feature", source: "spec/requests/api/v1/features/privileges_controller_spec.rb:65", path: "code%3Aseats/privileges/quota"},
		{name: "already discarded", source: "spec/requests/api/v1/features/privileges_controller_spec.rb:76", path: "code%3Aseats/privileges/max_admins"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/features/"+tt.path, nil); status != http.StatusNotFound {
				t.Fatalf("got status = %d (%s), want = 404", status, body)
			}
		})
	}
	t.Run("feature of another organization", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/features/privileges_controller_spec.rb:54")
		other := rpctest.New(t)
		f := featureCreate(t, other, featureSeats)
		if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/features/"+f.ID+"/privileges/max", nil); status != http.StatusNotFound {
			t.Fatalf("got status = %d (%s), want = 404", status, body)
		}
	})
}
