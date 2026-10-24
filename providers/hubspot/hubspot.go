package hubspot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
const Provider = integration.ProviderHubspot

const (
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/hubspot"

	// DefaultRate and DefaultBurst are HubSpot's limit of 110 requests in
	// 10 seconds per installation (lago config/initializers/throttling.rb).
	DefaultRate  = 11
	DefaultBurst = 110

	// refreshMargin is how long before its expiry an access token is
	// refreshed.
	refreshMargin = time.Minute

	codeUnexpected = "unexpected_error"
)

// HubSpot's servers.
const (
	AppURL = "https://app.hubspot.com"
	APIURL = "https://api.hubapi.com"
)

// DefaultScopes are the scopes a connection asks for: the companies,
// contacts and custom objects Tanvo writes and the schemas it deploys.
var DefaultScopes = []string{
	"oauth",
	"crm.objects.companies.read", "crm.objects.companies.write",
	"crm.objects.contacts.read", "crm.objects.contacts.write",
	"crm.objects.custom.read", "crm.objects.custom.write",
	"crm.schemas.companies.write", "crm.schemas.contacts.write",
	"crm.schemas.custom.read", "crm.schemas.custom.write",
}

// Settings and secrets keys of a connection.
const (
	KeyAccessToken                    = "access_token"
	KeyRefreshToken                   = "refresh_token"
	KeyTokenExpiresAt                 = "token_expires_at"
	KeyInvoicesObjectTypeID           = "invoices_object_type_id"
	KeyInvoicesObjectVersion          = "invoices_object_version"
	KeyInvoicesPropertiesVersion      = "invoices_properties_version"
	KeySubscriptionsObjectTypeID      = "subscriptions_object_type_id"
	KeySubscriptionsObjectVersion     = "subscriptions_object_version"
	KeySubscriptionsPropertiesVersion = "subscriptions_properties_version"
	KeyCompaniesPropertiesVersion     = "companies_properties_version"
	KeyContactsPropertiesVersion      = "contacts_properties_version"
)

// OAuthClient is the deployment's HubSpot app.
type OAuthClient struct {
	// ClientID and ClientSecret identify the app.
	ClientID, ClientSecret string
	// Scopes are the requested scopes; empty is [DefaultScopes].
	Scopes []string
}

// Gateway is the HubSpot [integration.CRMGateway]. It is safe for
// concurrent use; one Gateway serves every HubSpot connection.
type Gateway struct {
	client  *apiclient.Client
	oauth   OAuthClient
	store   integration.ConnectionStore
	app     string
	api     string
	now     func() time.Time
	schemas map[integration.CRMObject]schema

	mu     sync.Mutex
	locks  map[core.IntegrationID]*sync.Mutex
	tokens map[core.IntegrationID]grant
}

var (
	_ integration.CRMGateway   = (*Gateway)(nil)
	_ integration.OAuthGateway = (*Gateway)(nil)
)

type config struct {
	httpClient     *http.Client
	oauth          OAuthClient
	store          integration.ConnectionStore
	appURL, apiURL string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	rate           float64
	burst          int
	now            func() time.Time
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithOAuthClient sets the deployment's HubSpot app; without it
// connections cannot be authorized or refreshed.
func WithOAuthClient(c OAuthClient) Option { return func(o *config) { o.oauth = c } }

// WithConnectionStore sets where refreshed grants are stored.
func WithConnectionStore(s integration.ConnectionStore) Option {
	return func(o *config) { o.store = s }
}

// WithBaseURLs sets the consent and API servers (an httptest server in
// tests).
func WithBaseURLs(app, api string) Option {
	return func(o *config) { o.appURL, o.apiURL = app, api }
}

// WithTracerProvider sets the OpenTelemetry tracer provider.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *config) { o.tracerProvider = tp }
}

// WithMeterProvider sets the OpenTelemetry meter provider.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *config) { o.meterProvider = mp }
}

// WithRateLimit sets the request rate per connection and its burst; a
// rate of 0 disables throttling.
func WithRateLimit(perSecond float64, burst int) Option {
	return func(o *config) { o.rate, o.burst = perSecond, burst }
}

// WithClock sets the clock that decides token expiry (tests).
func WithClock(now func() time.Time) Option { return func(o *config) { o.now = now } }

// New returns a HubSpot gateway.
func New(opts ...Option) *Gateway {
	c := config{rate: DefaultRate, burst: DefaultBurst, now: time.Now, appURL: AppURL, apiURL: APIURL}
	for _, o := range opts {
		o(&c)
	}
	return &Gateway{
		client: apiclient.New(apiclient.Config{
			Provider: Provider, InstrumentationName: instrumentationName,
			HTTPClient: c.httpClient, TracerProvider: c.tracerProvider, MeterProvider: c.meterProvider,
			Rate: c.rate, Burst: c.burst,
		}),
		oauth: c.oauth, store: c.store, now: c.now,
		app: strings.TrimSuffix(c.appURL, "/"), api: strings.TrimSuffix(c.apiURL, "/"),
		schemas: schemas(),
		locks:   map[core.IntegrationID]*sync.Mutex{},
		tokens:  map[core.IntegrationID]grant{},
	}
}

// Provider implements [integration.Gateway].
func (g *Gateway) Provider() integration.Provider { return Provider }

// ValidateConnection implements [integration.Gateway]: a HubSpot
// connection has no credentials of its own before its grant (lago
// app/models/integrations/hubspot_integration.rb).
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
		return "", fmt.Errorf("hubspot: no oauth client: %w", integration.ErrNotConnected)
	}
	q := url.Values{
		"client_id": {g.oauth.ClientID}, "redirect_uri": {redirectURI}, "scope": {g.scopes()}, "state": {state},
	}
	return g.app + "/oauth/authorize?" + q.Encode(), nil
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

// tokenResponse is the token endpoint's answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Status       string `json:"status"`
	Message      string `json:"message"`
}

// requestToken posts a token request of the authorization code or
// refresh token grant.
func (g *Gateway) requestToken(ctx context.Context, form url.Values) (grant, error) {
	if g.oauth.ClientID == "" {
		return grant{}, fmt.Errorf("hubspot: no oauth client: %w", integration.ErrNotConnected)
	}
	form.Set("client_id", g.oauth.ClientID)
	form.Set("client_secret", g.oauth.ClientSecret)
	resp, err := g.client.Do(ctx, apiclient.Request{
		URL: g.api + "/oauth/v1/token", Body: []byte(form.Encode()), ContentType: "application/x-www-form-urlencoded",
	})
	if err != nil {
		return grant{}, err
	}
	var tr tokenResponse
	_ = json.Unmarshal(resp.Body, &tr)
	if !resp.OK() || tr.AccessToken == "" {
		code := tr.Status
		if code == "" {
			code = codeUnexpected
		}
		msg := tr.Message
		if msg == "" {
			msg = resp.Snippet()
		}
		pe := resp.Error(Provider, code, msg)
		if resp.Status == http.StatusBadRequest {
			// BAD_AUTH_CODE, BAD_REFRESH_TOKEN, BAD_CLIENT_ID: the grant
			// is gone.
			pe.Kind = integration.KindAuthentication
		}
		return grant{}, pe
	}
	return grant{
		access: tr.AccessToken, refresh: tr.RefreshToken,
		expiry: g.now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Truncate(time.Second),
	}, nil
}

// ExchangeCode implements [integration.OAuthGateway]: it trades the code
// for the grant of the portal the user installed the app in.
func (g *Gateway) ExchangeCode(ctx context.Context, conn integration.Connection, redirectURI, code string) (integration.ConnectionUpdate, error) {
	var u integration.ConnectionUpdate
	err := g.client.Run(ctx, apiclient.Call{Conn: conn, Op: "exchange_code", LimitKey: "oauth"}, func(ctx context.Context) error {
		t, err := g.requestToken(ctx, url.Values{
			"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		})
		if err != nil {
			return err
		}
		u = integration.ConnectionUpdate{Secrets: t.secrets()}
		g.remember(conn.ID, t)
		return nil
	})
	if err != nil {
		return u, fmt.Errorf("hubspot exchange_code: %w", err)
	}
	return u, nil
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
		return "", fmt.Errorf("hubspot: no grant: %w", integration.ErrNotConnected)
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

// call sends one CRM API request for conn inside the operation op and
// returns its 2xx answer, or the classified error.
func (g *Gateway) call(ctx context.Context, conn integration.Connection, op, method, path string, body any) (*apiclient.Response, error) {
	var out *apiclient.Response
	err := g.client.Run(ctx, apiclient.Call{Conn: conn, Op: op, LimitKey: "integration:" + conn.ID.String()}, func(ctx context.Context) error {
		token, err := g.accessToken(ctx, conn)
		if err != nil {
			return err
		}
		resp, err := g.client.Do(ctx, apiclient.Request{
			Method: method, URL: g.api + path,
			Header: http.Header{"Authorization": {"Bearer " + token}}, JSON: body,
		})
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
		return nil, fmt.Errorf("hubspot %s: %w", op, err)
	}
	return out, nil
}

// apiError is the body of a failed CRM API answer.
type apiError struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Category string `json:"category"`
	Errors   []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// classify maps a failed answer: the error category is the code and the
// message, else the first detailed error, the message; 429 is a request
// limit.
func classify(resp *apiclient.Response) error {
	var e apiError
	_ = json.Unmarshal(resp.Body, &e)
	code := e.Category
	if code == "" {
		code = codeUnexpected
	}
	msg := e.Message
	if msg == "" && len(e.Errors) > 0 {
		msg = e.Errors[0].Message
	}
	if msg == "" {
		msg = resp.Snippet()
	}
	return resp.Error(Provider, code, msg)
}

// AccountID implements [integration.CRMGateway]: the portal ID of the
// connection (lago Integrations::Aggregator::AccountInformationService).
func (g *Gateway) AccountID(ctx context.Context, conn integration.Connection) (string, error) {
	resp, err := g.call(ctx, conn, "account_information", http.MethodGet, "/account-info/v3/details", nil)
	if err != nil {
		return "", err
	}
	var out struct {
		PortalID json.Number `json:"portalId"`
	}
	if err := resp.Decode(&out); err != nil {
		return "", fmt.Errorf("hubspot account_information: %w", err)
	}
	if _, err := strconv.ParseInt(out.PortalID.String(), 10, 64); err != nil {
		return "", fmt.Errorf("hubspot account_information: portal id %q: %w", out.PortalID, core.ErrInvalidValue)
	}
	return out.PortalID.String(), nil
}
