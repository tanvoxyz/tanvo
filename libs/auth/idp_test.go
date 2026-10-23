package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// idp is a local OpenID Connect provider: discovery, JWKS, an
// authorization step driven by the test, a token endpoint checking client
// credentials and the PKCE verifier, and userinfo.
type idp struct {
	t      *testing.T
	srv    *httptest.Server
	issuer string
	key    *rsa.PrivateKey
	clock  core.Clock

	clientID, clientSecret string

	mu     sync.Mutex
	codes  map[string]idpGrant
	access map[string]map[string]any

	// Knobs changing the next responses.
	nonce    string        // replaces the request's nonce in ID tokens
	audience string        // replaces the client ID in ID tokens
	ttl      time.Duration // ID token lifetime (default one hour)
	tamper   bool          // corrupts the ID token signature
	userinfo map[string]any

	kid          string            // key ID of the signing key (default "k1")
	retired      []jose.JSONWebKey // earlier keys still published
	signKID      string            // replaces the kid header of ID tokens
	unsigned     bool              // issues alg "none" ID tokens
	tokenIssuer  string            // replaces the issuer in ID tokens
	discIssuer   string            // replaces the issuer in discovery
	discovery2   map[string]any    // merged into the discovery document
	jwksFetches  int               // key set requests served
	tokenAuth    string            // how the last token request authenticated: basic or post
	extraClaims  map[string]any    // merged into ID tokens
	authorizeLog []url.Values      // authorization requests seen
}

// rotate makes a new key the signing key, keeping the old one published.
func (p *idp) rotate(kid string) {
	p.t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retired = append(p.retired, jose.JSONWebKey{Key: &p.key.PublicKey, KeyID: p.signingKID(), Algorithm: "RS256", Use: "sig"})
	p.key, p.kid = key, kid
}

func (p *idp) signingKID() string {
	if p.kid == "" {
		return "k1"
	}
	return p.kid
}

type idpGrant struct {
	challenge, nonce, redirect string
	claims                     map[string]any
}

// newIdP starts a provider whose issuer is the server URL followed by
// prefix (Entra ID issuers carry "/<tenant>/v2.0").
func newIdP(t *testing.T, clock core.Clock, prefix string) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{t: t, key: key, clock: clock, clientID: "tanvo-client", clientSecret: "tanvo-secret", codes: map[string]idpGrant{}, access: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+prefix+"/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET "+prefix+"/keys", p.keys)
	mux.HandleFunc("POST "+prefix+"/token", p.token)
	mux.HandleFunc("GET "+prefix+"/userinfo", p.userinfoHandler)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	p.issuer = p.srv.URL + prefix
	return p
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (p *idp) discovery(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	doc := map[string]any{
		"issuer": p.issuer, "authorization_endpoint": p.issuer + "/authorize", "token_endpoint": p.issuer + "/token",
		"jwks_uri": p.issuer + "/keys", "userinfo_endpoint": p.issuer + "/userinfo",
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"},
	}
	if p.discIssuer != "" {
		doc["issuer"] = p.discIssuer
	}
	maps.Copy(doc, p.discovery2)
	writeJSON(w, http.StatusOK, doc)
}

func (p *idp) keys(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jwksFetches++
	keys := append([]jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: p.signingKID(), Algorithm: "RS256", Use: "sig"}}, p.retired...)
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: keys})
}

// authorize plays the user consenting at the provider: it checks the
// authorization URL and returns the state and code the provider would
// redirect back with. claims are the identity's ID token claims.
func (p *idp) authorize(authURL string, claims map[string]any) (state, code string) {
	p.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		p.t.Fatal(err)
	}
	q := u.Query()
	if got := u.Scheme + "://" + u.Host + u.Path; got != p.issuer+"/authorize" {
		p.t.Fatalf("authorization endpoint = %s, want = %s/authorize", got, p.issuer)
	}
	if q.Get("client_id") != p.clientID || q.Get("response_type") != "code" || !strings.Contains(q.Get("scope"), "openid") {
		p.t.Fatalf("authorization request = %v", q)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		p.t.Fatalf("authorization request lacks PKCE, nonce or state: %v", q)
	}
	code = rand.Text()
	p.mu.Lock()
	p.authorizeLog = append(p.authorizeLog, q)
	p.codes[code] = idpGrant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri"), claims: claims}
	p.mu.Unlock()
	return q.Get("state"), code
}

func (p *idp) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	id, secret, ok := r.BasicAuth()
	auth := "basic"
	if !ok {
		id, secret, auth = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret"), "post"
	}
	p.mu.Lock()
	p.tokenAuth = auth
	p.mu.Unlock()
	if id != p.clientID || secret != p.clientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	g, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != g.redirect ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	now := p.clock.Now()
	ttl := p.ttl
	if ttl == 0 {
		ttl = time.Hour
	}
	claims := map[string]any{
		"iss": p.issuer, "aud": p.clientID, "iat": now.Unix(), "exp": now.Add(ttl).Unix(), "nonce": g.nonce,
		"sub": "sub-" + strings.ToLower(asString(g.claims["email"])+asString(g.claims["preferred_username"])),
	}
	if p.nonce != "" {
		claims["nonce"] = p.nonce
	}
	if p.audience != "" {
		claims["aud"] = p.audience
	}
	if p.tokenIssuer != "" {
		claims["iss"] = p.tokenIssuer
	}
	maps.Copy(claims, p.extraClaims)
	maps.Copy(claims, g.claims)
	p.mu.Lock()
	key, kid := p.key, p.signingKID()
	p.mu.Unlock()
	if p.signKID != "" {
		kid = p.signKID
	}
	var raw string
	if p.unsigned {
		h, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
		b, _ := json.Marshal(claims)
		raw = base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(b) + "."
	} else {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
		if err != nil {
			p.t.Error(err)
			return
		}
		if raw, err = jwt.Signed(signer).Claims(claims).Serialize(); err != nil {
			p.t.Error(err)
			return
		}
	}
	if p.tamper {
		raw = raw[:len(raw)-4] + "AAAA"
	}
	at := rand.Text()
	ui := p.userinfo
	if ui == nil {
		ui = g.claims
	}
	ui = maps.Clone(ui)
	if _, ok := ui["sub"]; !ok {
		ui["sub"] = claims["sub"]
	}
	p.mu.Lock()
	p.access[at] = ui
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": 3600, "id_token": raw})
}

func (p *idp) userinfoHandler(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	ui, ok := p.access[tok]
	p.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	writeJSON(w, http.StatusOK, ui)
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
