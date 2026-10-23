package app

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// discoveryServer serves a minimal discovery document for its own URL.
func discoveryServer(t *testing.T, tls bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize", "token_endpoint": srv.URL + "/token",
			"jwks_uri": srv.URL + "/keys", "response_types_supported": []string{"code"},
		})
	})
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestOIDCClientRefusesPrivateAddresses(t *testing.T) {
	deps := auth.Deps{Clock: core.SystemClock{}, Logger: slog.New(slog.DiscardHandler)}
	tests := []struct {
		name         string
		allowPrivate bool
		tls          bool
		code         string
	}{
		// An https issuer on a loopback address is refused at dial time.
		{name: "loopback refused", tls: true, code: auth.CodeIssuerUnreachable},
		{name: "plain http refused", code: auth.CodeHTTPSRequired},
		{name: "allowed for development", allowPrivate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := discoveryServer(t, tt.tls)
			o := auth.NewOIDC(deps, nil, nil, auth.OIDCConfig{HTTPClient: newOIDCClient(tt.allowPrivate), AllowInsecure: tt.allowPrivate})
			err := o.CheckIssuer(t.Context(), srv.URL, "")
			got := ""
			if fe := core.FieldErrors(err); len(fe) > 0 {
				got = fe[0].Code
			} else if err != nil {
				got = err.Error()
			}
			if got != tt.code {
				t.Errorf("CheckIssuer() got = %q, want = %q", got, tt.code)
			}
		})
	}
}
