package avalara

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/providers/apiclient"
)

// Provider is the vendor this adapter serves.
const Provider = integration.ProviderAvalara

const (
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/avalara"

	// ProductionURL is the AvaTax production API.
	ProductionURL = "https://rest.avatax.com"
	// SandboxURL is the AvaTax sandbox API.
	SandboxURL = "https://sandbox-rest.avatax.com"

	// DefaultRate is lago's limit of requests per second
	// (config/initializers/throttling.rb:49-54).
	DefaultRate = 10

	// clientHeader identifies the integration to AvaTax ("app; version;
	// connector; connector version; machine").
	clientHeader = "Tanvo; 1.0; providers/avalara; 1.0; tanvo"
)

// Settings and secrets keys of a connection.
const (
	KeyAccountID   = "account_id"
	KeyCompanyCode = "company_code"
	KeyCompanyID   = "company_id"
	KeyLicenseKey  = "license_key"
)

// Gateway is the Avalara [integration.TaxGateway]. It is safe for
// concurrent use; one Gateway serves every Avalara connection.
type Gateway struct {
	client  *apiclient.Client
	baseURL string
}

var (
	_ integration.TaxGateway       = (*Gateway)(nil)
	_ integration.TaxCompanyFinder = (*Gateway)(nil)
	_ integration.ContactGateway   = (*Gateway)(nil)
)

type config struct {
	httpClient     *http.Client
	baseURL        string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	rate           float64
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithBaseURL sets the API base URL: [ProductionURL] (the default),
// [SandboxURL], or an httptest server in tests.
func WithBaseURL(u string) Option { return func(o *config) { o.baseURL = u } }

// WithTracerProvider sets the OpenTelemetry tracer provider.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *config) { o.tracerProvider = tp }
}

// WithMeterProvider sets the OpenTelemetry meter provider.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *config) { o.meterProvider = mp }
}

// WithRateLimit sets the request rate per account; 0 disables throttling.
func WithRateLimit(perSecond float64) Option { return func(o *config) { o.rate = perSecond } }

// New returns an Avalara gateway.
func New(opts ...Option) *Gateway {
	c := config{baseURL: ProductionURL, rate: DefaultRate}
	for _, o := range opts {
		o(&c)
	}
	return &Gateway{
		client: apiclient.New(apiclient.Config{
			Provider: Provider, InstrumentationName: instrumentationName,
			HTTPClient: c.httpClient, TracerProvider: c.tracerProvider, MeterProvider: c.meterProvider,
			Rate: c.rate,
		}),
		baseURL: strings.TrimRight(c.baseURL, "/"),
	}
}

// Provider implements [integration.Gateway].
func (g *Gateway) Provider() integration.Provider { return Provider }

// ValidateConnection implements [integration.Gateway]: account_id,
// company_code and license_key are required (lago
// app/models/integrations/avalara_integration.rb:9).
func (g *Gateway) ValidateConnection(conn integration.Connection) error {
	var v core.Validator
	v.Required(KeyAccountID, conn.Setting(KeyAccountID))
	v.Required(KeyCompanyCode, conn.Setting(KeyCompanyCode))
	v.Required(KeyLicenseKey, conn.Secret(KeyLicenseKey))
	return v.Err()
}

// FindCompany implements [integration.TaxCompanyFinder]: the ID of the
// company coded by the connection's company_code (lago
// app/services/integrations/aggregator/taxes/avalara/fetch_company_id_service.rb).
func (g *Gateway) FindCompany(ctx context.Context, conn integration.Connection) (string, error) {
	var id string
	err := g.run(ctx, conn, "findCompany", func(ctx context.Context, auth string) error {
		q := url.Values{"$filter": {"companyCode eq '" + strings.ReplaceAll(conn.Setting(KeyCompanyCode), "'", "''") + "'"}}
		var body struct {
			Value []struct {
				ID json.Number `json:"id"`
			} `json:"value"`
		}
		if err := g.call(ctx, auth, http.MethodGet, "/api/v2/companies?"+q.Encode(), nil, &body); err != nil {
			return err
		}
		if len(body.Value) == 0 {
			return fmt.Errorf("company %q: %w", conn.Setting(KeyCompanyCode), integration.ErrCompanyNotFound)
		}
		id = body.Value[0].ID.String()
		return nil
	})
	return id, err
}

// run executes an operation with the connection's credentials, throttled
// per account.
func (g *Gateway) run(ctx context.Context, conn integration.Connection, op string, f func(ctx context.Context, auth string) error) error {
	account, key := conn.Setting(KeyAccountID), conn.Secret(KeyLicenseKey)
	if account == "" || key == "" {
		return fmt.Errorf("avalara %s: %w", op, &integration.ProviderError{
			Provider: Provider, Kind: integration.KindAuthentication, Message: "missing account id or license key",
		})
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(account+":"+key))
	call := apiclient.Call{Conn: conn, Op: op, LimitKey: "account:" + account}
	if err := g.client.Run(ctx, call, func(ctx context.Context) error { return f(ctx, auth) }); err != nil {
		return fmt.Errorf("avalara %s: %w", op, err)
	}
	return nil
}

// call sends a request and decodes a 2xx answer into out (when non-nil),
// or returns the classified error.
func (g *Gateway) call(ctx context.Context, auth, method, path string, body, out any) error {
	resp, err := g.client.Do(ctx, apiclient.Request{
		Method: method,
		URL:    g.baseURL + path,
		Header: http.Header{"Authorization": {auth}, "X-Avalara-Client": {clientHeader}},
		JSON:   body,
	})
	if err != nil {
		return err
	}
	if !resp.OK() {
		return classify(resp)
	}
	if out == nil {
		return nil
	}
	if err := resp.Decode(out); err != nil {
		return &integration.ProviderError{Provider: Provider, Kind: integration.KindUnavailable, HTTPStatus: resp.Status, Message: err.Error()}
	}
	return nil
}

// errorEnvelope is the AvaTax error body.
type errorEnvelope struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// classify maps a failed answer. An error envelope of a 4xx answer other
// than authentication and rate limits is a rejection coded by its error
// code, with lago's message (taxes/base_service.rb:133-143).
func classify(resp *apiclient.Response) error {
	var env errorEnvelope
	_ = json.Unmarshal(resp.Body, &env)
	pe := resp.Error(Provider, "", resp.Snippet())
	if env.Error == nil || env.Error.Code == "" {
		return pe
	}
	pe.Code = env.Error.Code
	switch pe.Kind {
	case integration.KindAuthentication, integration.KindRequestLimit, integration.KindUnavailable:
		pe.Message = env.Error.Message
		return pe
	}
	pe.Message = "Service failure"
	switch env.Error.Code {
	case "CannotModifyLockedTransaction":
		pe.Kind = integration.KindLocked
	case "EntityNotFoundError", "TransactionNotFoundError":
		pe.Kind = integration.KindNotFound
	default:
		pe.Kind = integration.KindInvalidRequest
	}
	return pe
}
