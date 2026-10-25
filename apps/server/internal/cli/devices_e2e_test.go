package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestScenarioNewDeviceSecurityLog signs in with a password from several
// clients: the first sign-in from a user agent never seen is logged as
// user.new_device_logged_in with the client's description, a sign-in from
// a known one is not (lago UserDevices::RegisterService). The sign-up
// registers its device without the log (users_service.rb:84).
//
// source: spec/services/user_devices/register_service_spec.rb:51
// source: spec/services/user_devices/register_service_spec.rb:95
func TestScenarioNewDeviceSecurityLog(t *testing.T) {
	t.Parallel()
	s := newScenario(t, time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC), scenarioOptions{})

	const firefox = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14.5; rv:128.0) Gecko/20100101 Firefox/128.0"
	signIn := func(agent string) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"email": "admin@acme.test", "password": "correct-horse-battery-staple"})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.base+"/v1/auth:sign_in_with_password", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", agent)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if body, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK {
			t.Fatalf("sign in from %q: got = %d %s, want = 200", agent, resp.StatusCode, body)
		}
	}
	newDevices := func() []jsonValue {
		t.Helper()
		filter := `log_event = "user.new_device_logged_in" AND logged_at <= "2030-01-01T00:00:00Z"`
		return s.api(http.MethodGet, "/v1/security_logs?filter="+url.QueryEscape(filter), nil).list("security_logs")
	}

	// The sign-up's client is known: signing in from it logs nothing.
	signIn(userAgent)
	if got := newDevices(); len(got) != 0 {
		t.Fatalf("after a sign-in from the sign-up's client: got = %v, want = no new device log", got)
	}
	s.at(time.Date(2025, 3, 2, 9, 0, 0, 0, time.UTC))
	signIn(firefox)
	got := newDevices()
	if len(got) != 1 {
		t.Fatalf("after a sign-in from a new client: got = %d new device logs, want = 1", len(got))
	}
	// DeviceDetector names macOS "Mac" (Utils::DeviceInfo, os_name).
	l := got[0]
	for path, want := range map[string]string{
		"log_type": "user", "user_email": "admin@acme.test", "logged_at": "2025-03-02T09:00:00Z",
		"device_info.user_agent": firefox, "device_info.browser": "Firefox 128.0", "device_info.os": "Mac",
		"device_info.device_type": "desktop",
	} {
		if g := l.str(path); g != want {
			t.Errorf("new device log %s: got = %q, want = %q", path, g, want)
		}
	}
	// The client is now known.
	s.at(time.Date(2025, 3, 3, 9, 0, 0, 0, time.UTC))
	signIn(firefox)
	if got := newDevices(); len(got) != 1 {
		t.Errorf("after a second sign-in from the client: got = %d new device logs, want = 1", len(got))
	}
}
