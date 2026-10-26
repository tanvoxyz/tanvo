package netsuite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/providers/apiclient"
)

// Provider is the vendor this adapter serves.
const Provider = integration.ProviderNetsuite

const (
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/netsuite"

	// DefaultRate is lago's limit of requests per second per client ID
	// (config/initializers/throttling.rb:40-45).
	DefaultRate = 10

	// codeRequestLimit is NetSuite's concurrency or rate limit error
	// (lago aggregator/base_service.rb:9).
	codeRequestLimit = "SSS_REQUEST_LIMIT_EXCEEDED"
	// CodeInvalidLoginAttempt is NetSuite's refused authentication (lago
	// aggregator/invoices/create_service.rb:9).
	CodeInvalidLoginAttempt = "INVALID_LOGIN_ATTEMPT"
	codeUnexpected          = "unexpected_error"
)

// Settings and secrets keys of a connection.
const (
	KeyAccountID         = "account_id"
	KeyClientID          = "client_id"
	KeyClientSecret      = "client_secret"
	KeyTokenID           = "token_id"
	KeyTokenSecret       = "token_secret"
	KeyScriptEndpointURL = "script_endpoint_url"
)

// Gateway is the NetSuite [integration.AccountingGateway]. It is safe for
// concurrent use; one Gateway serves every NetSuite connection.
type Gateway struct {
	client   *apiclient.Client
	suiteURL func(account string) string
	now      func() time.Time
	nonce    func() string
}

var (
	_ integration.AccountingGateway = (*Gateway)(nil)
	_ integration.InvoiceFinder     = (*Gateway)(nil)
	_ integration.SubsidiaryLister  = (*Gateway)(nil)
)

type config struct {
	httpClient     *http.Client
	counter        apiclient.Counter
	suiteURL       func(string) string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	rate           float64
	now            func() time.Time
	nonce          func() string
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithSuiteTalkURL sets the SuiteTalk REST base URL of an account (an
// httptest server in tests); the default is
// https://<account>.suitetalk.api.netsuite.com.
func WithSuiteTalkURL(f func(account string) string) Option {
	return func(o *config) { o.suiteURL = f }
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

// WithRateLimit sets the request rate per client ID; 0 disables
// throttling.
func WithRateLimit(perSecond float64) Option { return func(o *config) { o.rate = perSecond } }

// WithSigningClock sets the clock and nonce source of the OAuth
// signatures (tests).
func WithSigningClock(now func() time.Time, nonce func() string) Option {
	return func(o *config) { o.now, o.nonce = now, nonce }
}

// New returns a NetSuite gateway.
func New(opts ...Option) *Gateway {
	c := config{rate: DefaultRate, now: time.Now, nonce: randomNonce, suiteURL: func(account string) string {
		return "https://" + account + ".suitetalk.api.netsuite.com"
	}}
	for _, o := range opts {
		o(&c)
	}
	return &Gateway{
		client: apiclient.New(apiclient.Config{
			Provider: Provider, InstrumentationName: instrumentationName,
			HTTPClient: c.httpClient, TracerProvider: c.tracerProvider, MeterProvider: c.meterProvider, Counter: c.counter,
			Rate: c.rate,
		}),
		suiteURL: c.suiteURL, now: c.now, nonce: c.nonce,
	}
}

func randomNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Provider implements [integration.Gateway].
func (g *Gateway) Provider() integration.Provider { return Provider }

// ValidateConnection implements [integration.Gateway]: the account, the
// client credentials and the RESTlet URL are required (lago
// app/models/integrations/netsuite_integration.rb:5). The token is not:
// without it, calls fail with [integration.ErrNotConnected].
func (g *Gateway) ValidateConnection(conn integration.Connection) error {
	var v core.Validator
	v.Required(KeyAccountID, conn.Setting(KeyAccountID))
	v.Required(KeyClientID, conn.Setting(KeyClientID))
	v.Required(KeyClientSecret, conn.Secret(KeyClientSecret))
	v.Required(KeyScriptEndpointURL, conn.Setting(KeyScriptEndpointURL))
	return v.Err()
}

// creds returns the signing keys of conn.
func creds(conn integration.Connection) (credentials, error) {
	c := credentials{
		realm:       strings.ToUpper(strings.ReplaceAll(conn.Setting(KeyAccountID), "-", "_")),
		consumerKey: conn.Setting(KeyClientID), consumerSecret: conn.Secret(KeyClientSecret),
		token: conn.Setting(KeyTokenID), tokenSecret: conn.Secret(KeyTokenSecret),
	}
	if c.realm == "" || c.consumerKey == "" || c.consumerSecret == "" || c.token == "" || c.tokenSecret == "" {
		return c, fmt.Errorf("netsuite: %w", integration.ErrNotConnected)
	}
	return c, nil
}

// run executes an operation throttled per client ID (lago throttle_key:
// the SHA-256 of the client ID).
func (g *Gateway) run(ctx context.Context, conn integration.Connection, op string, f func(ctx context.Context, c credentials) error) error {
	c, err := creds(conn)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(c.consumerKey))
	call := apiclient.Call{Conn: conn, Op: op, LimitKey: "client:" + hex.EncodeToString(sum[:])}
	if err := g.client.Run(ctx, call, func(ctx context.Context) error { return f(ctx, c) }); err != nil {
		return fmt.Errorf("netsuite %s: %w", op, err)
	}
	return nil
}

// send signs and sends a request and returns a 2xx answer, or the
// classified error.
func (g *Gateway) send(ctx context.Context, c credentials, method, url string, body any, header http.Header) (*apiclient.Response, error) {
	resp, err := g.client.Do(ctx, apiclient.Request{
		Method: method, URL: url, JSON: body, Header: header,
		Authorize: func(r *http.Request) error {
			c.sign(r, strconv.FormatInt(g.now().Unix(), 10), g.nonce())
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, classify(resp)
	}
	return resp, nil
}

// restlet POSTs (or PUTs) a record body to the connection's RESTlet and
// returns the record ID it answers.
func (g *Gateway) restlet(ctx context.Context, conn integration.Connection, op, method string, body any) (string, error) {
	var id string
	err := g.run(ctx, conn, op, func(ctx context.Context, c credentials) error {
		resp, err := g.send(ctx, c, method, conn.Setting(KeyScriptEndpointURL), body, nil)
		if err != nil {
			return err
		}
		id, err = recordID(resp.Body)
		return err
	})
	return id, err
}

// recordID reads the RESTlet's answer: the record ID as a JSON string or
// number, or an object with an id (lago process_string_result and
// process_hash_result).
func recordID(b []byte) (string, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return "", nil
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return strings.Trim(string(b), `"`), nil
	}
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case map[string]any:
		if id, ok := x["id"]; ok {
			return fmt.Sprint(id), nil
		}
	}
	return "", nil
}

// classify maps a failed answer, reading the code and message where lago
// reads them (aggregator/base_service.rb:159-176).
func classify(resp *apiclient.Response) error {
	var body any
	_ = json.Unmarshal(resp.Body, &body)
	code := firstString(body, []string{"type"}, []string{"error", "payload", "name"}, []string{"error", "payload", "error", "code"}, []string{"error", "code"})
	msg := firstString(body, []string{"payload", "message"}, []string{"error", "payload", "message"}, []string{"error", "payload", "error"},
		[]string{"error", "payload", "error", "message"}, []string{"error", "message"})
	if msg == "" {
		msg = resp.Snippet()
	}
	if code == "" {
		code = codeUnexpected
	}
	pe := resp.Error(Provider, code, msg)
	switch {
	case strings.Contains(string(resp.Body), codeRequestLimit):
		pe.Kind = integration.KindRequestLimit
	case strings.Contains(string(resp.Body), CodeInvalidLoginAttempt):
		pe.Kind, pe.Code = integration.KindAuthentication, CodeInvalidLoginAttempt
	}
	return pe
}

// firstString returns the first non-empty string at one of the paths
// (lago safe_dig_str).
func firstString(v any, paths ...[]string) string {
	for _, p := range paths {
		cur := v
		for _, k := range p {
			m, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = m[k]
		}
		if s, ok := cur.(string); ok && s != "" {
			return s
		}
	}
	return ""
}
