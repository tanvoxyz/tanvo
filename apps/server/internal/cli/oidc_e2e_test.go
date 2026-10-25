package cli_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// fakeIdP is a minimal OpenID Connect provider: discovery, its key set, a
// token endpoint checking the client secret and the PKCE verifier, and
// RS256 ID tokens for the identity the test authorizes.
type fakeIdP struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey

	mu     sync.Mutex
	grants map[string]idpGrant
}

type idpGrant struct {
	challenge, nonce string
	claims           map[string]any
}

const idpClientID, idpClientSecret = "tanvo-client", "tanvo-secret"

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIdP{t: t, key: key, grants: map[string]idpGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		u := p.srv.URL
		writeJSONResponse(w, map[string]any{
			"issuer": u, "authorization_endpoint": u + "/authorize", "token_endpoint": u + "/token", "jwks_uri": u + "/keys",
			"userinfo_endpoint": u + "/userinfo", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		writeJSONResponse(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, map[string]any{"sub": "jane", "email": "jane@corp.test", "email_verified": true})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// authorize plays the user consenting at the provider to the
// authorization URL and returns the state and code of the redirect.
func (p *fakeIdP) authorize(authURL string, claims map[string]any) (state, code string) {
	p.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		p.t.Fatal(err)
	}
	q := u.Query()
	if got := u.Scheme + "://" + u.Host + u.Path; got != p.srv.URL+"/authorize" || q.Get("client_id") != idpClientID ||
		q.Get("code_challenge_method") != "S256" {
		p.t.Fatalf("authorization request: got = %s, want = the provider's with PKCE", authURL)
	}
	code = rand.Text()
	p.mu.Lock()
	p.grants[code] = idpGrant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), claims: claims}
	p.mu.Unlock()
	return q.Get("state"), code
}

func (p *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	p.mu.Lock()
	g, found := p.grants[r.PostForm.Get("code")]
	delete(p.grants, r.PostForm.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if id != idpClientID || secret != idpClientSecret || !found || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		w.WriteHeader(http.StatusBadRequest)
		writeJSONResponse(w, map[string]any{"error": "invalid_grant"})
		return
	}
	now := time.Now()
	claims := map[string]any{"iss": p.srv.URL, "aud": idpClientID, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": g.nonce}
	for k, v := range g.claims {
		claims[k] = v
	}
	writeJSONResponse(w, map[string]any{"access_token": rand.Text(), "token_type": "Bearer", "expires_in": 3600, "id_token": p.sign(claims)})
}

// sign returns claims as an RS256 JWT.
func (p *fakeIdP) sign(claims map[string]any) string {
	b64 := base64.RawURLEncoding.EncodeToString
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	payload, _ := json.Marshal(claims)
	input := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return input + "." + b64(sig)
}

// TestScenarioOIDCSignIn signs a user in through an organization's
// generic OpenID Connect connection: the email domain selects the
// connection, the provider authenticates the user, and the user joins
// the organization with the connection's default role.
//
// source: spec/services/auth/okta/authorize_service_spec.rb
// source: spec/services/auth/okta/login_service_spec.rb
func TestScenarioOIDCSignIn(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	s := newScenario(t, time.Now().UTC(), scenarioOptions{environ: []string{"TANVO_OIDC_ALLOW_PRIVATE=true"}})
	s.admin(http.MethodPost, "/v1/sso_connections", map[string]any{
		"provider": "SSO_PROVIDER_OIDC", "domain": "corp.test", "client_id": idpClientID, "client_secret": idpClientSecret,
		"issuer_url": idp.srv.URL, "default_role_code": "finance",
	})
	start := s.do(http.MethodPost, "/v1/auth:start_oidc_sign_in", "", "", map[string]any{
		"provider": "OIDC_PROVIDER_SSO", "intent": "OIDC_INTENT_SIGN_IN", "email": "jane@corp.test",
		"redirect_uri": "https://app.tanvo.test/auth/oidc/callback",
	})
	state, code := idp.authorize(start.str("authorization_url"), map[string]any{
		"sub": "jane", "email": "jane@corp.test", "email_verified": true, "name": "Jane",
	})
	done := s.do(http.MethodPost, "/v1/auth:complete_oidc_sign_in", "", "", map[string]any{"code": code, "state": state})
	if got := done.str("user.email"); got != "jane@corp.test" {
		t.Errorf("signed-in user: got = %s, want = jane@corp.test", got)
	}
	if done.str("session.token") == "" {
		t.Fatal("session: got = none, want = a token")
	}
	orgs := done.list("organizations")
	if len(orgs) != 1 {
		t.Fatalf("organizations: got = %v, want = the connection's", done.at("organizations"))
	}

	// The state is single-use.
	if status, _ := s.send(http.MethodPost, "/v1/auth:complete_oidc_sign_in", "", "", map[string]any{"code": code, "state": state}); status == http.StatusOK {
		t.Errorf("replayed callback: got status = %d, want = an error", status)
	}

	finance := ""
	for _, r := range s.admin(http.MethodGet, "/v1/roles", nil).list("roles") {
		if r.str("code") == "finance" {
			finance = r.str("id")
		}
	}
	var roles []string
	for _, m := range s.admin(http.MethodGet, "/v1/memberships", nil).list("memberships") {
		if m.str("user.email") == "jane@corp.test" {
			for _, r := range m.list("role_ids") {
				roles = append(roles, r.String())
			}
		}
	}
	if finance == "" || len(roles) != 1 || roles[0] != `"`+finance+`"` {
		t.Errorf("roles of jane@corp.test: got = %v, want = [finance %s]", roles, finance)
	}
	s.noFailedJobs()
}
