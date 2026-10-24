package gocardless

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

var testClient = OAuthClient{ClientID: "client_1", ClientSecret: "secret_1", RedirectURI: "https://tanvo.example/oauth/gocardless", Sandbox: true}

func TestAuthorizeURL(t *testing.T) {
	tests := []struct {
		sandbox bool
		host    string
	}{
		{true, "connect-sandbox.gocardless.com"},
		{false, "connect.gocardless.com"},
	}
	for _, tt := range tests {
		c := testClient
		c.Sandbox = tt.sandbox
		u, err := url.Parse(New().AuthorizeURL(c, "state_1"))
		if err != nil {
			t.Fatalf("AuthorizeURL: got = %v, want = a URL", err)
		}
		if u.Host != tt.host || u.Path != "/oauth/authorize" {
			t.Errorf("sandbox %v: got = %s%s, want = %s/oauth/authorize", tt.sandbox, u.Host, u.Path, tt.host)
		}
		q := u.Query()
		for k, want := range map[string]string{
			"client_id": "client_1", "redirect_uri": testClient.RedirectURI, "scope": "read_write",
			"state": "state_1", "response_type": "code",
		} {
			if got := q.Get(k); got != want {
				t.Errorf("%s: got = %q, want = %q", k, got, want)
			}
		}
	}
}

func TestExchangeCode(t *testing.T) {
	// source: spec/services/payment_providers/gocardless_service_spec.rb:33
	rp := newReplay(t, map[string][]reply{routeToken: {raw(http.StatusOK,
		`{"access_token":"access_token_554","token_type":"bearer","scope":"read_write","organisation_id":"OR123"}`)}})
	tok, err := rp.gateway().ExchangeCode(context.Background(), testClient, "1234567!abc")
	if err != nil || tok != "access_token_554" {
		t.Fatalf("ExchangeCode: got = %q, %v, want = access_token_554", tok, err)
	}
	reqs := rp.requests(routeToken)
	if len(reqs) != 1 {
		t.Fatalf("requests: got = %d, want = 1", len(reqs))
	}
}

func TestExchangeCodeForm(t *testing.T) {
	var form url.Values
	var auth string
	srv := newFormServer(t, func(f url.Values, a string) { form, auth = f, a })
	if _, err := New(WithOAuthURL(srv)).ExchangeCode(context.Background(), testClient, "code_1"); err != nil {
		t.Fatalf("ExchangeCode: got = %v, want = nil", err)
	}
	for k, want := range map[string]string{
		"grant_type": "authorization_code", "code": "code_1", "redirect_uri": testClient.RedirectURI,
		"client_id": "client_1", "client_secret": "secret_1",
	} {
		if got := form.Get(k); got != want {
			t.Errorf("%s: got = %q, want = %q", k, got, want)
		}
	}
	if auth != "" {
		t.Errorf("Authorization: got = %q, want = the credentials in the body", auth)
	}
}

func TestExchangeCodeErrors(t *testing.T) {
	tests := []struct {
		reply reply
		kind  payment.ErrorKind
		code  string
	}{
		{raw(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"The authorization code is invalid"}`), payment.KindInvalidRequest, "invalid_grant"},
		{raw(http.StatusTooManyRequests, `{"error":"rate_limited"}`), payment.KindRateLimited, "rate_limited"},
		{raw(http.StatusServiceUnavailable, `{}`), payment.KindUnavailable, ""},
	}
	for _, tt := range tests {
		rp := newReplay(t, map[string][]reply{routeToken: {tt.reply}})
		_, err := rp.gateway().ExchangeCode(context.Background(), testClient, "bad")
		pe, ok := payment.AsProviderError(err)
		if !ok || pe.Kind != tt.kind || pe.Code != tt.code {
			t.Errorf("error: got = %v, want = %s %s", err, tt.kind, tt.code)
		}
	}
	// A refused code carries the OAuth error's description.
	rp := newReplay(t, map[string][]reply{routeToken: {tests[0].reply}})
	_, err := rp.gateway().ExchangeCode(context.Background(), testClient, "bad")
	if pe, _ := payment.AsProviderError(err); pe == nil || pe.Message != "The authorization code is invalid" || !errors.Is(err, payment.ErrRejected) {
		t.Errorf("error: got = %v, want = the rejected description", err)
	}

	// An unreachable server is unavailable.
	g := New(WithOAuthURL("http://127.0.0.1:1"))
	if _, err := g.ExchangeCode(context.Background(), testClient, "code"); payment.ErrorKindOf(err) != payment.KindUnavailable {
		t.Errorf("unreachable: got = %v, want = %s", err, payment.KindUnavailable)
	}
	// Cancellation passes through.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.ExchangeCode(ctx, testClient, "code"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: got = %v, want = %v", err, context.Canceled)
	}
}

// newFormServer serves a token endpoint that hands each request's form and
// Authorization header to got; it returns the server's URL.
func newFormServer(t *testing.T, got func(url.Values, string)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		got(r.PostForm, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
