package anrok

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/providers/apiclient"
)

// Provider is the vendor this adapter serves.
const Provider = integration.ProviderAnrok

const (
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/anrok"

	// DefaultBaseURL is the Anrok API.
	DefaultBaseURL = "https://api.anrok.com"

	// DefaultRate is the requests per second per API key: lago's limit,
	// one under Anrok's documented 10 so draft quotes keep headroom
	// (config/initializers/throttling.rb:42-48).
	DefaultRate = 9

	// maxDaysAhead caps the accounting and tax dates (lago anrok.rb:56-59:
	// "Anrok API requires issuing date to be 30 days in the future at
	// most").
	maxDaysAhead = 30
)

// Gateway is the Anrok [integration.TaxGateway]. It is safe for
// concurrent use; one Gateway serves every Anrok connection.
type Gateway struct {
	client  *apiclient.Client
	baseURL string
	now     func() time.Time
}

var (
	_ integration.TaxGateway = (*Gateway)(nil)
	_ integration.TaxNegator = (*Gateway)(nil)
)

type config struct {
	httpClient     *http.Client
	baseURL        string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	now            func() time.Time
	rate           float64
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithBaseURL sets the API base URL (an httptest server in tests).
func WithBaseURL(u string) Option { return func(o *config) { o.baseURL = u } }

// WithTracerProvider sets the OpenTelemetry tracer provider.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *config) { o.tracerProvider = tp }
}

// WithMeterProvider sets the OpenTelemetry meter provider.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *config) { o.meterProvider = mp }
}

// WithClock sets the time source of the date cap; time.Now by default.
func WithClock(now func() time.Time) Option { return func(o *config) { o.now = now } }

// WithRateLimit sets the request rate per API key; 0 disables throttling.
func WithRateLimit(perSecond float64) Option { return func(o *config) { o.rate = perSecond } }

// New returns an Anrok gateway.
func New(opts ...Option) *Gateway {
	c := config{baseURL: DefaultBaseURL, rate: DefaultRate, now: time.Now}
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
		now:     c.now,
	}
}

// Provider implements [integration.Gateway].
func (g *Gateway) Provider() integration.Provider { return Provider }

// ValidateConnection implements [integration.Gateway]: api_key is
// required (lago app/models/integrations/anrok_integration.rb:9).
func (g *Gateway) ValidateConnection(conn integration.Connection) error {
	var v core.Validator
	v.Required("api_key", conn.Secret("api_key"))
	return v.Err()
}

// QuoteTaxes implements [integration.TaxGateway] with createEphemeral.
func (g *Gateway) QuoteTaxes(ctx context.Context, conn integration.Connection, t *integration.TaxTransaction) (*integration.TaxResult, error) {
	return g.transaction(ctx, conn, "createEphemeral", t, false)
}

// ReportTaxes implements [integration.TaxGateway] with createOrUpdate.
func (g *Gateway) ReportTaxes(ctx context.Context, conn integration.Connection, t *integration.TaxTransaction) (*integration.TaxResult, error) {
	if t.ID == "" {
		return nil, fmt.Errorf("anrok createOrUpdate: transaction id: %w", core.ErrInvalidValue)
	}
	return g.transaction(ctx, conn, "createOrUpdate", t, true)
}

// VoidTaxes implements [integration.TaxGateway]. A transaction frozen for
// filing is [integration.KindLocked].
func (g *Gateway) VoidTaxes(ctx context.Context, conn integration.Connection, id string) error {
	return g.run(ctx, conn, "void", func(ctx context.Context, key string) error {
		_, err := g.post(ctx, key, "/v1/seller/transactions/id:"+url.PathEscape(id)+"/void", struct{}{})
		return err
	})
}

// NegateTaxes implements [integration.TaxNegator] with createNegation.
func (g *Gateway) NegateTaxes(ctx context.Context, conn integration.Connection, id, negationID string) error {
	return g.run(ctx, conn, "createNegation", func(ctx context.Context, key string) error {
		_, err := g.post(ctx, key, "/v1/seller/transactions/createNegation", negationRequest{
			OriginalTransactionID: id, NewTransactionID: negationID,
		})
		return err
	})
}

func (g *Gateway) transaction(ctx context.Context, conn integration.Connection, op string, t *integration.TaxTransaction, withID bool) (*integration.TaxResult, error) {
	req := g.request(t, withID)
	var out *integration.TaxResult
	err := g.run(ctx, conn, op, func(ctx context.Context, key string) error {
		resp, err := g.post(ctx, key, "/v1/seller/transactions/"+op, req)
		if err != nil {
			return err
		}
		var body transactionResponse
		if err := resp.Decode(&body); err != nil {
			return &integration.ProviderError{Provider: Provider, Kind: integration.KindUnavailable, HTTPStatus: resp.Status, Message: err.Error()}
		}
		out = result(t, body)
		if withID {
			out.TransactionID = t.ID
		}
		return nil
	})
	return out, err
}

// run executes an operation with the connection's API key.
func (g *Gateway) run(ctx context.Context, conn integration.Connection, op string, f func(ctx context.Context, key string) error) error {
	key := conn.Secret("api_key")
	if key == "" {
		return fmt.Errorf("anrok %s: %w", op, &integration.ProviderError{
			Provider: Provider, Kind: integration.KindAuthentication, Message: "missing api key",
		})
	}
	sum := sha256.Sum256([]byte(key))
	call := apiclient.Call{Conn: conn, Op: op, LimitKey: hex.EncodeToString(sum[:])}
	if err := g.client.Run(ctx, call, func(ctx context.Context) error { return f(ctx, key) }); err != nil {
		return fmt.Errorf("anrok %s: %w", op, err)
	}
	return nil
}

// post sends body to path and returns a 2xx response, or the classified
// error.
func (g *Gateway) post(ctx context.Context, key, path string, body any) (*apiclient.Response, error) {
	resp, err := g.client.Do(ctx, apiclient.Request{
		URL:    g.baseURL + path,
		Header: http.Header{"Authorization": {"Bearer " + key}},
		JSON:   body,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, classify(resp)
	}
	return resp, nil
}

// classify maps a failed answer (lago taxes/base_service.rb:133-143).
func classify(resp *apiclient.Response) error {
	var conflict struct {
		Type string `json:"type"`
	}
	if resp.Status == http.StatusConflict && json.Unmarshal(resp.Body, &conflict) == nil && conflict.Type != "" {
		pe := resp.Error(Provider, conflict.Type, "Service failure")
		switch conflict.Type {
		case "transactionFrozenForFiling":
			pe.Kind = integration.KindLocked
		case "transactionIdNotFound":
			pe.Kind = integration.KindNotFound
		}
		return pe
	}
	pe := resp.Error(Provider, "", resp.Snippet())
	if pe.Kind == integration.KindInvalidRequest {
		pe.Code = "validationError"
	}
	return pe
}
