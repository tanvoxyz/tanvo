package cli_test

import (
	"bytes"
	"io"
	"mime/quotedprintable"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestScenarioAdminEmails sends the organization emails through SMTP: the
// API key notices to the admins, the login method notice when the
// authentication methods change, and the export ready email to the member
// who asked for the export, with its file served by the server.
//
// source: spec/services/api_keys/create_service_spec.rb
// source: spec/services/api_keys/rotate_service_spec.rb
// source: spec/services/api_keys/destroy_service_spec.rb
// source: spec/services/organizations/update_service_spec.rb
// source: spec/services/data_exports/combine_parts_service_spec.rb
func TestScenarioAdminEmails(t *testing.T) {
	t.Parallel()
	smtp := newFakeSMTP(t)
	environ := append(smtp.environ(), "TANVO_BLOB_BACKEND=local", "TANVO_BLOB_LOCAL_ROOT="+t.TempDir())
	s := newScenario(t, time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC), scenarioOptions{environ: environ, selfAPIURL: true})

	key := s.admin(http.MethodPost, "/v1/api_keys", map[string]any{"name": "second"}).str("api_key.id")
	s.admin(http.MethodPost, "/v1/api_keys/"+key+":rotate", map[string]any{})
	s.admin(http.MethodDelete, "/v1/api_keys/"+key, nil)
	s.admin(http.MethodPatch, "/v1/organization?update_mask=authenticationMethods", map[string]any{
		"authentication_methods": []string{"AUTHENTICATION_METHOD_EMAIL_PASSWORD"},
	})
	// An export of nothing stays processing, as in lago: it has no part
	// to combine.
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer", "currency": "EUR"})
	s.api(http.MethodPost, "/v1/add_ons", map[string]any{"name": "Add-on", "code": "addon", "amount": money("2")})
	s.api(http.MethodPost, "/v1/invoices", map[string]any{
		"customer_id": "external_id:cust", "currency": "EUR", "fees": []map[string]any{{"add_on_id": "code:addon", "units": "1"}},
	})
	s.idle()
	s.admin(http.MethodPost, "/v1/invoices:export", map[string]any{})
	s.idle()

	var subjects []string
	for _, m := range smtp.messages() {
		subjects = append(subjects, m.Subject())
		if !slices.Equal(m.To, []string{"admin@acme.test"}) {
			t.Errorf("email %q recipients: got = %v, want = [admin@acme.test]", m.Subject(), m.To)
		}
	}
	for _, tt := range []struct {
		name, subject string
		n             int
	}{
		// The sign-up's key and the second one.
		{"api key created", "A new Tanvo API key has been created", 2},
		{"api key rotated", "Your Tanvo API key has been rolled", 1},
		{"api key deleted", "A Tanvo API key has been deleted", 1},
		{"login method updated", "Login method updated in your Tanvo workspace", 1},
	} {
		if got := countEqual(subjects, tt.subject); got != tt.n {
			t.Errorf("%s emails: got = %d (subjects %q), want = %d", tt.name, got, subjects, tt.n)
		}
	}
	var export []smtpMessage
	for _, m := range smtp.messages() {
		if strings.HasPrefix(m.Subject(), "Your Tanvo") && strings.HasSuffix(m.Subject(), "export is ready!") {
			export = append(export, m)
		}
	}
	if len(export) != 1 {
		t.Fatalf("export ready emails: got = %d (subjects %q), want = 1", len(export), subjects)
	}
	// The email links the export file, which the server serves.
	plain, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(export[0].Raw)))
	if err != nil {
		t.Fatal(err)
	}
	link := regexp.MustCompile(`http://127\.0\.0\.1:\d+/files/[^\s"<>]+`).Find(plain)
	if link == nil {
		t.Fatalf("export email: got = no file link, want = one in %s", plain)
	}
	res, err := http.Get(string(link)) //nolint:noctx // a test download
	if err != nil {
		t.Fatal(err)
	}
	file, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !bytes.Contains(file, []byte("number")) {
		t.Errorf("export file: got status = %d, %q, want = 200 and the invoices CSV", res.StatusCode, file)
	}
	s.noFailedJobs()
}

// countEqual returns how many of ss equal s.
func countEqual(ss []string, s string) int {
	n := 0
	for _, v := range ss {
		if v == s {
			n++
		}
	}
	return n
}
