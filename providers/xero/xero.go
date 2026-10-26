package xero

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/providers/apiclient"
)

// Provider is the vendor this adapter serves.
const Provider = integration.ProviderXero

const (
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/xero"

	// DefaultRate and DefaultBurst approximate Xero's limit of 60 calls a
	// minute per tenant (lago config/initializers/throttling.rb).
	DefaultRate  = 1
	DefaultBurst = 60
	// DailyLimit is Xero's limit of calls a day per tenant, over UTC days
	// (lago config/initializers/throttling.rb).
	DailyLimit = 5000

	// refreshMargin is how long before its expiry an access token is
	// refreshed.
	refreshMargin = time.Minute

	codeUnexpected = "unexpected_error"
)

// Xero's servers.
const (
	LoginURL    = "https://login.xero.com"
	IdentityURL = "https://identity.xero.com"
	APIURL      = "https://api.xero.com"
)

// DefaultScopes are the scopes a connection asks for: the refresh token
// and the accounting scopes of the broker's Xero actions and syncs.
var DefaultScopes = []string{"offline_access", "accounting.transactions", "accounting.contacts", "accounting.settings"}

// Settings and secrets keys of a connection.
const (
	KeyTenantID       = "tenant_id"
	KeyTenantName     = "tenant_name"
	KeyAccessToken    = "access_token"
	KeyRefreshToken   = "refresh_token"
	KeyTokenExpiresAt = "token_expires_at"
)

// OAuthClient is the deployment's Xero app.
type OAuthClient struct {
	// ClientID and ClientSecret identify the app.
	ClientID, ClientSecret string
	// Scopes are the requested scopes; empty is [DefaultScopes].
	Scopes []string
}

// Gateway is the Xero [integration.AccountingGateway]. It is safe for
// concurrent use; one Gateway serves every Xero connection.
type Gateway struct {
	client                     *apiclient.Client
	oauth                      OAuthClient
	store                      integration.ConnectionStore
	loginURL, identityURL, api string
	now                        func() time.Time

	mu     sync.Mutex
	locks  map[core.IntegrationID]*sync.Mutex
	tokens map[core.IntegrationID]grant
}

var (
	_ integration.AccountingGateway = (*Gateway)(nil)
	_ integration.OAuthGateway      = (*Gateway)(nil)
)

type config struct {
	httpClient     *http.Client
	counter        apiclient.Counter
	oauth          OAuthClient
	store          integration.ConnectionStore
	loginURL       string
	identityURL    string
	apiURL         string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	rate           float64
	burst          int
	daily          int
	now            func() time.Time
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithOAuthClient sets the deployment's Xero app; without it connections
// cannot be authorized or refreshed.
func WithOAuthClient(c OAuthClient) Option { return func(o *config) { o.oauth = c } }

// WithConnectionStore sets where refreshed grants are stored.
func WithConnectionStore(s integration.ConnectionStore) Option {
	return func(o *config) { o.store = s }
}

// WithBaseURLs sets the login, identity and API servers (an httptest
// server in tests).
func WithBaseURLs(login, identity, api string) Option {
	return func(o *config) { o.loginURL, o.identityURL, o.apiURL = login, identity, api }
}

// WithTracerProvider sets the OpenTelemetry tracer provider.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *config) { o.tracerProvider = tp }
}

// WithMeterProvider sets the OpenTelemetry meter provider.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *config) { o.meterProvider = mp }
}

// WithCounter counts the rate limit across server processes on c (the
// installation's cache server); without it each process counts its own.
func WithCounter(c apiclient.Counter) Option { return func(o *config) { o.counter = c } }

// WithRateLimit sets the request rate per tenant and its burst; a rate
// of 0 disables throttling.
func WithRateLimit(perSecond float64, burst int) Option {
	return func(o *config) { o.rate, o.burst = perSecond, burst }
}

// WithDailyLimit sets the calls a day per tenant; 0 disables the quota.
func WithDailyLimit(n int) Option { return func(o *config) { o.daily = n } }

// WithClock sets the clock that decides token expiry (tests).
func WithClock(now func() time.Time) Option { return func(o *config) { o.now = now } }

// New returns a Xero gateway.
func New(opts ...Option) *Gateway {
	c := config{
		rate: DefaultRate, burst: DefaultBurst, daily: DailyLimit, now: time.Now,
		loginURL: LoginURL, identityURL: IdentityURL, apiURL: APIURL,
	}
	for _, o := range opts {
		o(&c)
	}
	return &Gateway{
		client: apiclient.New(apiclient.Config{
			Provider: Provider, InstrumentationName: instrumentationName,
			HTTPClient: c.httpClient, TracerProvider: c.tracerProvider, MeterProvider: c.meterProvider, Counter: c.counter,
			Rate: c.rate, Burst: c.burst, Now: c.now,
			Windows: []apiclient.Window{{Limit: c.daily, Period: 24 * time.Hour}},
		}),
		oauth: c.oauth, store: c.store, now: c.now,
		loginURL: strings.TrimSuffix(c.loginURL, "/"), identityURL: strings.TrimSuffix(c.identityURL, "/"),
		api:    strings.TrimSuffix(c.apiURL, "/"),
		locks:  map[core.IntegrationID]*sync.Mutex{},
		tokens: map[core.IntegrationID]grant{},
	}
}

// Provider implements [integration.Gateway].
func (g *Gateway) Provider() integration.Provider { return Provider }

// ValidateConnection implements [integration.Gateway]: a Xero connection
// has no settings of its own before its grant (lago
// app/models/integrations/xero_integration.rb).
func (g *Gateway) ValidateConnection(integration.Connection) error { return nil }

func (g *Gateway) scopes() string {
	s := g.oauth.Scopes
	if len(s) == 0 {
		s = DefaultScopes
	}
	return strings.Join(s, " ")
}

// AuthorizationURL implements [integration.OAuthGateway].
func (g *Gateway) AuthorizationURL(_ integration.Connection, redirectURI, state string) (string, error) {
	if g.oauth.ClientID == "" {
		return "", fmt.Errorf("xero: no oauth client: %w", integration.ErrNotConnected)
	}
	q := url.Values{
		"response_type": {"code"}, "client_id": {g.oauth.ClientID}, "redirect_uri": {redirectURI},
		"scope": {g.scopes()}, "state": {state},
	}
	return g.loginURL + "/identity/connect/authorize?" + q.Encode(), nil
}

// grant is an OAuth grant of a connection.
type grant struct {
	access, refresh string
	expiry          time.Time
}

func (t grant) secrets() map[string]string {
	return map[string]string{
		KeyAccessToken: t.access, KeyRefreshToken: t.refresh, KeyTokenExpiresAt: t.expiry.UTC().Format(time.RFC3339),
	}
}

// tokenResponse is the token endpoint's answer (RFC 6749 §5.1).
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// requestToken posts a token request of the authorization code or refresh
// token grant.
func (g *Gateway) requestToken(ctx context.Context, form url.Values) (grant, error) {
	if g.oauth.ClientID == "" {
		return grant{}, fmt.Errorf("xero: no oauth client: %w", integration.ErrNotConnected)
	}
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(g.oauth.ClientID+":"+g.oauth.ClientSecret)))
	resp, err := g.client.Do(ctx, apiclient.Request{
		URL: g.identityURL + "/connect/token", Header: h,
		Body: []byte(form.Encode()), ContentType: "application/x-www-form-urlencoded",
	})
	if err != nil {
		return grant{}, err
	}
	var tr tokenResponse
	_ = json.Unmarshal(resp.Body, &tr)
	if !resp.OK() || tr.AccessToken == "" {
		code := tr.Error
		if code == "" {
			code = codeUnexpected
		}
		msg := tr.Description
		if msg == "" {
			msg = resp.Snippet()
		}
		pe := resp.Error(Provider, code, msg)
		if resp.Status == http.StatusBadRequest {
			// invalid_grant, invalid_client: the grant is gone.
			pe.Kind = integration.KindAuthentication
		}
		return grant{}, pe
	}
	return grant{
		access: tr.AccessToken, refresh: tr.RefreshToken,
		expiry: g.now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Truncate(time.Second),
	}, nil
}

// tenant is an entry of GET /connections.
type tenant struct {
	AuthEventID string `json:"authEventId"`
	TenantID    string `json:"tenantId"`
	TenantType  string `json:"tenantType"`
	TenantName  string `json:"tenantName"`
}

// ExchangeCode implements [integration.OAuthGateway]: it trades the code
// for tokens and picks the tenant the grant authorized, the one of the
// token's authentication event, else the only one (the broker's
// get-tenant-id helper).
func (g *Gateway) ExchangeCode(ctx context.Context, conn integration.Connection, redirectURI, code string) (integration.ConnectionUpdate, error) {
	var u integration.ConnectionUpdate
	err := g.client.Run(ctx, apiclient.Call{Conn: conn, Op: "exchange_code", LimitKey: "oauth", Unmetered: true}, func(ctx context.Context) error {
		t, err := g.requestToken(ctx, url.Values{
			"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		})
		if err != nil {
			return err
		}
		resp, err := g.client.Do(ctx, apiclient.Request{
			Method: http.MethodGet, URL: g.api + "/connections",
			Header: http.Header{"Authorization": {"Bearer " + t.access}},
		})
		if err != nil {
			return err
		}
		if !resp.OK() {
			return classify(resp)
		}
		var tenants []tenant
		if err := resp.Decode(&tenants); err != nil {
			return err
		}
		chosen, err := pickTenant(tenants, authEventID(t.access))
		if err != nil {
			return err
		}
		u = integration.ConnectionUpdate{
			Settings: map[string]string{KeyTenantID: chosen.TenantID, KeyTenantName: chosen.TenantName},
			Secrets:  t.secrets(),
		}
		g.remember(conn.ID, t)
		return nil
	})
	if err != nil {
		return u, fmt.Errorf("xero exchange_code: %w", err)
	}
	return u, nil
}

// pickTenant returns the organisation of the grant's authentication
// event, else the only tenant.
func pickTenant(tenants []tenant, event string) (tenant, error) {
	var matched []tenant
	for _, t := range tenants {
		if event != "" && t.AuthEventID == event {
			matched = append(matched, t)
		}
	}
	if len(matched) == 0 {
		matched = tenants
	}
	if len(matched) == 1 {
		return matched[0], nil
	}
	msg := "Multiple tenants found."
	if len(matched) == 0 {
		msg = "No tenant authorized."
	}
	return tenant{}, &integration.ProviderError{Provider: Provider, Kind: integration.KindInvalidRequest, Code: "tenant_not_found", Message: msg}
}

// authEventID reads the authentication_event_id claim of a Xero access
// token (a JWT); the token came from Xero over TLS, so it is not
// verified.
func authEventID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Event string `json:"authentication_event_id"`
	}
	_ = json.Unmarshal(b, &claims)
	return claims.Event
}

func (g *Gateway) remember(id core.IntegrationID, t grant) {
	g.mu.Lock()
	g.tokens[id] = t
	g.mu.Unlock()
}

func (g *Gateway) lock(id core.IntegrationID) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	l, ok := g.locks[id]
	if !ok {
		l = &sync.Mutex{}
		g.locks[id] = l
	}
	return l
}

// accessToken returns a live access token of conn, refreshing and
// storing the grant when it expires within [refreshMargin].
func (g *Gateway) accessToken(ctx context.Context, conn integration.Connection) (string, error) {
	l := g.lock(conn.ID)
	l.Lock()
	defer l.Unlock()
	t := grant{access: conn.Secret(KeyAccessToken), refresh: conn.Secret(KeyRefreshToken)}
	t.expiry, _ = time.Parse(time.RFC3339, conn.Secret(KeyTokenExpiresAt))
	g.mu.Lock()
	if c, ok := g.tokens[conn.ID]; ok && c.expiry.After(t.expiry) {
		t = c
	}
	g.mu.Unlock()
	if t.access != "" && t.expiry.After(g.now().Add(refreshMargin)) {
		return t.access, nil
	}
	if t.refresh == "" {
		return "", fmt.Errorf("xero: no grant: %w", integration.ErrNotConnected)
	}
	n, err := g.requestToken(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.refresh}})
	if err != nil {
		return "", fmt.Errorf("refresh token: %w", err)
	}
	if n.refresh == "" {
		n.refresh = t.refresh
	}
	g.remember(conn.ID, n)
	if g.store != nil {
		if err := g.store.UpdateConnection(ctx, conn, integration.ConnectionUpdate{Secrets: n.secrets()}); err != nil {
			return "", fmt.Errorf("store refreshed grant: %w", err)
		}
	}
	return n.access, nil
}

// call sends one Accounting API request for conn inside the operation op
// and returns its 2xx answer, or the classified error.
func (g *Gateway) call(ctx context.Context, conn integration.Connection, op, method, path string, query url.Values, body any) (*apiclient.Response, error) {
	tenantID := conn.Setting(KeyTenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("xero %s: no tenant: %w", op, integration.ErrNotConnected)
	}
	var out *apiclient.Response
	err := g.client.Run(ctx, apiclient.Call{Conn: conn, Op: op, LimitKey: "tenant:" + tenantID}, func(ctx context.Context) error {
		token, err := g.accessToken(ctx, conn)
		if err != nil {
			return err
		}
		u := g.api + "/api.xro/2.0/" + path
		if len(query) > 0 {
			u += "?" + query.Encode()
		}
		h := http.Header{}
		h.Set("Authorization", "Bearer "+token)
		h.Set("Xero-Tenant-Id", tenantID)
		resp, err := g.client.Do(ctx, apiclient.Request{Method: method, URL: u, Header: h, JSON: body})
		if err != nil {
			return err
		}
		if !resp.OK() {
			return classify(resp)
		}
		out = resp
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("xero %s: %w", op, err)
	}
	return out, nil
}

// apiError is the body of a failed Accounting API answer: an API
// exception (Type, Message, Elements) or a problem (Title, Detail).
type apiError struct {
	Type     string    `json:"Type"`
	Message  string    `json:"Message"`
	Title    string    `json:"Title"`
	Detail   string    `json:"Detail"`
	Elements []element `json:"Elements"`
}

type element struct {
	ValidationErrors []validationError `json:"ValidationErrors"`
}

type validationError struct {
	Message string `json:"Message"`
}

func joinValidation(errs []validationError) string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Message)
	}
	return strings.Join(msgs, ". ")
}

// classify maps a failed answer: the exception type (or the problem
// title) is the code and the validation errors, else the message, the
// message; 429 is a request limit.
func classify(resp *apiclient.Response) error {
	var e apiError
	_ = json.Unmarshal(resp.Body, &e)
	code := e.Type
	if code == "" {
		code = e.Title
	}
	if code == "" {
		code = codeUnexpected
	}
	var verrs []validationError
	for _, el := range e.Elements {
		verrs = append(verrs, el.ValidationErrors...)
	}
	msg := joinValidation(verrs)
	for _, m := range []string{e.Message, e.Detail, e.Title, resp.Snippet()} {
		if msg == "" {
			msg = m
		}
	}
	return resp.Error(Provider, code, msg)
}
