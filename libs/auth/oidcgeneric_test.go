package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

var oidcM = organization.AuthenticationMethodOIDC

// generic adds a generic connection claiming hooli.com, with the defaults
// ConnectionFromIntegration gives one, then edit.
func (e *oidcEnv) generic(edit func(*Connection)) *Connection {
	e.t.Helper()
	c := e.connect(e.org(), oidcM, "hooli.com")
	c.Scopes = append([]string(nil), integration.DefaultScopes...)
	c.EmailVerification = EmailVerifiedRequired
	if edit != nil {
		edit(c)
	}
	return c
}

func verified(addr string) map[string]any {
	return map[string]any{"email": addr, "email_verified": true}
}

func TestOIDCGenericSignIn(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(*Connection)
		claims   map[string]any
		ui       map[string]any
		code     string
		wantName string
		email    string
	}{
		{name: "verified email", claims: verified("gavin@hooli.com"), email: "gavin@hooli.com"},
		{name: "unverified email refused", claims: map[string]any{"email": "gavin@hooli.com", "email_verified": false}, code: CodeOIDCEmailNotVerified},
		{name: "email_verified missing everywhere refused", claims: email("gavin@hooli.com"), ui: map[string]any{}, code: CodeOIDCEmailNotVerified},
		{name: "email_verified from userinfo", claims: email("gavin@hooli.com"), ui: verified("gavin@hooli.com"), email: "gavin@hooli.com"},
		{name: "email_verified as a string", claims: map[string]any{"email": "gavin@hooli.com", "email_verified": "true"}, email: "gavin@hooli.com"},
		{
			name: "unverified email allowed", edit: func(c *Connection) { c.EmailVerification = EmailVerifiedIgnored },
			claims: map[string]any{"email": "gavin@hooli.com", "email_verified": false}, email: "gavin@hooli.com",
		},
		{
			name: "claim mapping", edit: func(c *Connection) { c.EmailClaim, c.NameClaim = "upn", "given_name" },
			claims: map[string]any{"upn": "Gavin@Hooli.com", "given_name": "Gavin", "name": "G. Belson", "email": "other@piper.com", "email_verified": true},
			email:  "gavin@hooli.com", wantName: "Gavin",
		},
		{
			name: "mapped email claim missing", edit: func(c *Connection) { c.EmailClaim = "upn" },
			claims: verified("gavin@hooli.com"), ui: map[string]any{}, code: "oidc_userinfo_error",
		},
		{name: "default name claim", claims: map[string]any{"email": "gavin@hooli.com", "email_verified": true, "name": "Gavin Belson"}, email: "gavin@hooli.com", wantName: "Gavin Belson"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newOIDCEnv(t, "/generic")
			e.generic(tt.edit)
			e.idp.userinfo = tt.ui
			out, err := e.run(BeginRequest{Method: oidcM, Email: "gavin@hooli.com"}, tt.claims)
			if code(err) != tt.code {
				t.Fatalf("got = %s, want = %s", code(err), tt.code)
			}
			if err != nil {
				return
			}
			if out.User.Email != tt.email {
				t.Errorf("email got = %s, want = %s", out.User.Email, tt.email)
			}
			if tt.wantName != "" && (out.User.Name == nil || *out.User.Name != tt.wantName) {
				t.Errorf("name got = %v, want = %s", out.User.Name, tt.wantName)
			}
		})
	}
}

func TestOIDCGenericAuthorizationRequest(t *testing.T) {
	e := newOIDCEnv(t, "")
	e.generic(func(c *Connection) {
		c.Scopes = []string{"openid", "email", "groups"}
		c.AuthParams = map[string]string{"prompt": "login", "acr_values": "mfa"}
	})
	raw, err := e.oidc.Begin(e.ctx, BeginRequest{Method: oidcM, Email: "gavin@hooli.com"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("scope") != "openid email groups" || q.Get("prompt") != "login" || q.Get("acr_values") != "mfa" {
		t.Errorf("query got = %v, want scopes and extra parameters", q)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" || q.Get("login_hint") != "gavin@hooli.com" {
		t.Errorf("query got = %v, want PKCE, nonce, state and login_hint", q)
	}
}

// TestOIDCIDTokenValidation covers the ID token checks on a generic
// connection: each case changes one claim or the signature.
func TestOIDCIDTokenValidation(t *testing.T) {
	tests := []struct {
		name string
		knob func(e *oidcEnv)
		code string
	}{
		{name: "valid", knob: func(*oidcEnv) {}},
		{name: "issuer of another provider", knob: func(e *oidcEnv) { e.idp.tokenIssuer = "https://evil.test" }, code: CodeOIDCInvalidToken},
		{name: "audience of another client", knob: func(e *oidcEnv) { e.idp.audience = "someone-else" }, code: CodeOIDCInvalidToken},
		{name: "expired", knob: func(e *oidcEnv) { e.idp.ttl = -time.Minute }, code: CodeOIDCInvalidToken},
		{name: "expired within the leeway", knob: func(e *oidcEnv) { e.idp.ttl = -ClockSkew / 2 }},
		{name: "wrong nonce", knob: func(e *oidcEnv) { e.idp.nonce = "replayed" }, code: CodeOIDCInvalidToken},
		{name: "alg none", knob: func(e *oidcEnv) { e.idp.unsigned = true }, code: CodeOIDCInvalidToken},
		{name: "forged signature", knob: func(e *oidcEnv) { e.idp.tamper = true }, code: CodeOIDCInvalidToken},
		{name: "not yet valid", knob: func(e *oidcEnv) {
			e.idp.extraClaims = map[string]any{"nbf": e.clock.Now().Add(10 * time.Minute).Unix()}
		}, code: CodeOIDCInvalidToken},
		{name: "issued in the future", knob: func(e *oidcEnv) {
			e.idp.extraClaims = map[string]any{"iat": e.clock.Now().Add(10 * time.Minute).Unix()}
		}, code: CodeOIDCInvalidToken},
		{name: "several audiences without azp", knob: func(e *oidcEnv) {
			e.idp.extraClaims = map[string]any{"aud": []string{e.idp.clientID, "other"}}
		}, code: CodeOIDCInvalidToken},
		{name: "several audiences with azp", knob: func(e *oidcEnv) {
			e.idp.extraClaims = map[string]any{"aud": []string{e.idp.clientID, "other"}, "azp": e.idp.clientID}
		}},
		{name: "azp of another client", knob: func(e *oidcEnv) { e.idp.extraClaims = map[string]any{"azp": "other"} }, code: CodeOIDCInvalidToken},
		{name: "unknown key", knob: func(e *oidcEnv) { e.idp.signKID = "ghost" }, code: CodeOIDCInvalidToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newOIDCEnv(t, "")
			e.generic(nil)
			tt.knob(e)
			_, err := e.run(BeginRequest{Method: oidcM, Email: "gavin@hooli.com"}, verified("gavin@hooli.com"))
			if code(err) != tt.code {
				t.Errorf("got = %s, want = %s", code(err), tt.code)
			}
		})
	}
}

func TestOIDCKeyRotation(t *testing.T) {
	e := newOIDCEnv(t, "")
	e.generic(nil)
	signIn := func() error {
		_, err := e.run(BeginRequest{Method: oidcM, Email: "gavin@hooli.com"}, verified("gavin@hooli.com"))
		return err
	}
	fetches := func(want int) {
		t.Helper()
		e.idp.mu.Lock()
		got := e.idp.jwksFetches
		e.idp.mu.Unlock()
		if got != want {
			t.Errorf("jwks fetches got = %d, want = %d", got, want)
		}
	}
	if err := signIn(); err != nil {
		t.Fatal(err)
	}
	if err := signIn(); err != nil {
		t.Fatal(err)
	}
	fetches(1)
	// The provider rotates its key: the unknown kid refetches the set.
	e.idp.rotate("k2")
	if err := signIn(); err != nil {
		t.Fatalf("after rotation got = %v, want = nil", err)
	}
	fetches(2)
	// Tokens naming keys that do not exist refetch at most once per
	// interval.
	e.idp.signKID = "ghost"
	e.clock.Advance(DefaultJWKSMinRefresh)
	for range 3 {
		if code(signIn()) != CodeOIDCInvalidToken {
			t.Fatal("ghost key accepted")
		}
	}
	fetches(3)
	e.clock.Advance(DefaultJWKSMinRefresh)
	_ = signIn()
	fetches(4)
	// After the TTL the set is refetched even for known keys.
	e.idp.signKID = ""
	e.clock.Advance(DefaultJWKSTTL)
	if err := signIn(); err != nil {
		t.Fatal(err)
	}
	fetches(5)
}

func TestOIDCTokenEndpointAuth(t *testing.T) {
	tests := []struct {
		name       string
		advertised []string
		method     integration.TokenAuthMethod
		want       string
	}{
		{name: "nothing advertised is basic", want: "basic"},
		{name: "first advertised", advertised: []string{"private_key_jwt", "client_secret_post", "client_secret_basic"}, want: "post"},
		{name: "configured", advertised: []string{"client_secret_basic", "client_secret_post"}, method: integration.TokenAuthClientSecretPost, want: "post"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newOIDCEnv(t, "")
			if tt.advertised != nil {
				e.idp.discovery2 = map[string]any{"token_endpoint_auth_methods_supported": tt.advertised}
			}
			e.generic(func(c *Connection) { c.TokenAuthMethod = tt.method })
			if _, err := e.run(BeginRequest{Method: oidcM, Email: "gavin@hooli.com"}, verified("gavin@hooli.com")); err != nil {
				t.Fatal(err)
			}
			if e.idp.tokenAuth != tt.want {
				t.Errorf("token auth got = %s, want = %s", e.idp.tokenAuth, tt.want)
			}
		})
	}
}

func TestOIDCCheckIssuer(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"` + strings.Repeat("x", MaxProviderResponseBytes) + `"}`))
	}))
	t.Cleanup(big.Close)
	tests := []struct {
		name     string
		insecure bool
		knob     func(e *oidcEnv) string // returns the issuer to check
		method   integration.TokenAuthMethod
		field    string
		code     string
	}{
		{name: "valid", insecure: true, knob: func(e *oidcEnv) string { return e.idp.issuer }},
		{name: "plain http refused", knob: func(e *oidcEnv) string { return e.idp.issuer }, field: "issuer_url", code: CodeHTTPSRequired},
		{name: "issuer mismatch", insecure: true, knob: func(e *oidcEnv) string {
			e.idp.discIssuer = e.idp.issuer + "/"
			return e.idp.issuer
		}, field: "issuer_url", code: CodeIssuerMismatch},
		{name: "no discovery document", insecure: true, knob: func(e *oidcEnv) string { return e.idp.issuer + "/nope" }, field: "issuer_url", code: CodeIssuerUnreachable},
		{name: "oversized document", insecure: true, knob: func(*oidcEnv) string { return big.URL }, field: "issuer_url", code: CodeIssuerUnreachable},
		{name: "missing jwks", insecure: true, knob: func(e *oidcEnv) string {
			e.idp.discovery2 = map[string]any{"jwks_uri": ""}
			return e.idp.issuer
		}, field: "issuer_url", code: CodeDiscoveryIncomplete},
		{name: "only HMAC signatures", insecure: true, knob: func(e *oidcEnv) string {
			e.idp.discovery2 = map[string]any{"id_token_signing_alg_values_supported": []string{"HS256", "none"}}
			return e.idp.issuer
		}, field: "issuer_url", code: CodeDiscoveryIncomplete},
		{name: "unsupported token auth method", insecure: true, method: integration.TokenAuthClientSecretPost, knob: func(e *oidcEnv) string {
			e.idp.discovery2 = map[string]any{"token_endpoint_auth_methods_supported": []string{"client_secret_basic"}}
			return e.idp.issuer
		}, field: "token_endpoint_auth_method", code: CodeUnsupportedByProvider},
		{name: "refused address", insecure: true, knob: func(e *oidcEnv) string {
			e.oidc.client.Transport = limitTransport{base: refusingTransport{}, max: MaxProviderResponseBytes}
			return e.idp.issuer
		}, field: "issuer_url", code: CodeIssuerUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newOIDCEnv(t, "")
			e.oidc.cfg.AllowInsecure = tt.insecure
			issuer := tt.knob(e)
			err := e.oidc.CheckIssuer(e.ctx, issuer, tt.method)
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Errorf("got = %s on %s, want = %s on %s", code(err), fieldOf(err), tt.code, tt.field)
			}
		})
	}
}

// refusingTransport stands for the guarded client of apps/server, which
// refuses private addresses at dial time.
type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("destination address is not allowed")
}

func TestOIDCSignInRequiresHTTPS(t *testing.T) {
	e := newOIDCEnv(t, "")
	e.oidc.cfg.AllowInsecure = false
	e.generic(nil)
	if _, err := e.oidc.Begin(e.ctx, BeginRequest{Method: oidcM, Email: "gavin@hooli.com"}); code(err) != CodeOIDCRequestError {
		t.Errorf("got = %s, want = %s", code(err), CodeOIDCRequestError)
	}
}

func TestSSOConnectionsGeneric(t *testing.T) {
	e := newOIDCEnv(t, "")
	o := e.org(organization.AuthenticationMethodEmailPassword)
	s := e.ssoConnections().WithIssuerChecker(e.oidc)
	in := SSOConnectionInput{
		Provider: integration.ProviderOIDC, Domain: "Hooli.com", AdditionalDomains: []string{"piper.com"},
		ClientID: e.idp.clientID, ClientSecret: e.idp.clientSecret, DefaultRoleCode: "finance",
		Generic: GenericOIDC{
			IssuerURL: e.idp.issuer, Scopes: []string{"openid", "email"}, EmailClaim: "upn",
			AuthorizationParams: map[string]string{"prompt": "login"},
		},
	}
	bad := in
	bad.Generic.IssuerURL = e.idp.issuer + "/nope"
	if _, err := s.Create(e.ctx, o.ID, bad); code(err) != CodeIssuerUnreachable || fieldOf(err) != "issuer_url" {
		t.Fatalf("create with a bad issuer got = %v, want = issuer_url %s", err, CodeIssuerUnreachable)
	}
	reserved := in
	reserved.Generic.AuthorizationParams = map[string]string{"state": "x"}
	if _, err := s.Create(e.ctx, o.ID, reserved); code(err) != integration.CodeAuthorizationParamReserved {
		t.Fatalf("create with a reserved parameter got = %v, want = %s", err, integration.CodeAuthorizationParamReserved)
	}
	c, err := s.Create(e.ctx, o.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Domain != "hooli.com" || c.Generic.IssuerURL != e.idp.issuer || c.Generic.EmailClaim != "upn" {
		t.Errorf("created got = %+v", c)
	}
	if ms := e.methods(o); len(ms) != 2 || ms[1] != oidcM {
		t.Errorf("methods got = %v, want = email_password and oidc", ms)
	}
	// The stored integration reads back as a connection with the generic
	// defaults.
	conns := IntegrationConnections{Tx: e.deps.Tx, Secret: s.Secret, RedirectURL: func(organization.AuthenticationMethod) string { return "https://app.tanvo.test/auth/oidc/callback" }}
	got, err := conns.ByDomain(e.ctx, oidcM, "piper.com")
	if err != nil || len(got) != 1 {
		t.Fatalf("ByDomain() got = %v, %v", got, err)
	}
	g := got[0]
	if g.Issuer != e.idp.issuer || g.ClientSecret != e.idp.clientSecret || g.EmailClaim != "upn" || g.EmailVerification != EmailVerifiedRequired ||
		g.AuthParams["prompt"] != "login" || len(g.Scopes) != 2 {
		t.Errorf("connection got = %+v", g)
	}
	// Changing the issuer checks it again; other fields do not.
	if _, err := s.Update(e.ctx, o.ID, c.ID, SSOConnectionUpdate{IssuerURL: core.Some(e.idp.issuer + "/nope")}); code(err) != CodeIssuerUnreachable {
		t.Errorf("update with a bad issuer got = %v, want = %s", err, CodeIssuerUnreachable)
	}
	up, err := s.Update(e.ctx, o.ID, c.ID, SSOConnectionUpdate{AllowUnverifiedEmail: core.Some(true), NameClaim: core.Some("given_name")})
	if err != nil {
		t.Fatal(err)
	}
	if !up.Generic.AllowUnverifiedEmail || up.Generic.NameClaim != "given_name" || up.Generic.IssuerURL != e.idp.issuer {
		t.Errorf("updated got = %+v", up.Generic)
	}
	if list, err := s.List(e.ctx, o.ID); err != nil || len(list) != 1 || list[0].Provider != integration.ProviderOIDC {
		t.Errorf("List() got = %v, %v", list, err)
	}
	// A preset refuses the generic fields.
	okta := oktaInput()
	okta.Generic.IssuerURL = "https://id.hooli.com"
	if _, err := s.Create(e.ctx, o.ID, okta); code(err) != core.CodeInvalid || fieldOf(err) != "issuer_url" {
		t.Errorf("okta with an issuer got = %v, want = issuer_url %s", err, core.CodeInvalid)
	}
}

func TestConnectionFromGenericIntegration(t *testing.T) {
	role := roleID(permission.BuiltInRoleFinance)
	i := &integration.Integration{
		ID: core.NewID[core.IntegrationID](core.UUIDv7Generator{}), OrganizationID: core.NewID[core.OrganizationID](core.UUIDv7Generator{}),
		Category: integration.CategoryAuth, Provider: integration.ProviderOIDC, CreatedAt: time.Now(),
	}
	if err := i.SetAuthSettings(integration.AuthSettings{ClientID: "c", Domain: "hooli.com", IssuerURL: "https://id.hooli.com", DefaultRoleID: role}); err != nil {
		t.Fatal(err)
	}
	c, err := ConnectionFromIntegration(i, "s", "https://app.tanvo.test/auth/oidc/callback")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Scopes, " ") != "openid email profile" || c.EmailVerification != EmailVerifiedRequired || c.TokenAuthMethod != "" ||
		c.EmailClaim != "" || c.Method != oidcM {
		t.Errorf("defaults got = %+v", c)
	}
}
