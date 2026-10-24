package x402

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// The CDP facilitator API.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:6-10
const (
	// CDPHost is the host the request tokens are bound to.
	CDPHost = "api.cdp.coinbase.com"
	// CDPBasePath is the path of the x402 API.
	CDPBasePath = "/platform/v2/x402"

	openTimeout       = 5 * time.Second
	readTimeout       = 10 * time.Second
	settleReadTimeout = 20 * time.Second
	maxAnswerBytes    = 4 << 20
)

// Facilitators opens the facilitators of connections; it implements
// [x402.Facilitators].
type Facilitators struct {
	cfg    config
	http   *http.Client
	base   string
	tracer trace.Tracer
}

var _ x402.Facilitators = (*Facilitators)(nil)

// NewFacilitators returns the facilitators.
func NewFacilitators(opts ...Option) *Facilitators {
	c := newConfig(opts)
	base := c.baseURL
	if base == "" {
		base = "https://" + CDPHost + CDPBasePath
	}
	return &Facilitators{
		cfg:    c,
		http:   c.client(),
		base:   strings.TrimSuffix(base, "/"),
		tracer: c.tracerProvider.Tracer(instrumentationName),
	}
}

// For returns the facilitator of kind for creds. The secret is parsed on
// each call, so a secret that is not a key fails the call, before any
// request.
//
// source: app/services/x402/facilitator/client.rb
func (f *Facilitators) For(kind x402.FacilitatorKind, creds x402.Credentials) (x402.Facilitator, error) {
	if kind != x402.FacilitatorCoinbaseCDP {
		return nil, fmt.Errorf("x402 facilitator %q is not supported", string(kind))
	}
	return &cdp{f: f, keyID: creds.APIKeyID, secret: creds.APIKeySecret}, nil
}

// cdp is the Coinbase Developer Platform facilitator.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb
type cdp struct {
	f             *Facilitators
	keyID, secret string
}

// answer is a facilitator answer: parsed when its body is a JSON object.
type answer struct {
	status int
	body   x402.Object
	parsed bool
}

// noResponse is a call that got no answer.
type noResponse struct{ desc string }

func (e *noResponse) Error() string { return e.desc }

// Verify asks whether payment answers requirements.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:21-31
func (c *cdp) Verify(ctx context.Context, payment, requirements x402.Object) (x402.VerifyResult, error) {
	ctx, span := c.start(ctx, "verify")
	defer span.End()
	a, err := c.post(ctx, "/verify", payment, requirements, readTimeout)
	if err != nil {
		return x402.VerifyResult{}, c.fail(ctx, span, c.unanswered(ctx, "verify", err))
	}
	if !screeningDecline(a) {
		if err := c.access(ctx, "verify", a); err != nil {
			return x402.VerifyResult{}, c.fail(ctx, span, err)
		}
	}
	if a.status >= 500 || !a.parsed || malformedVerdict(a) {
		return x402.VerifyResult{}, c.fail(ctx, span, c.unavailable(ctx, "verify", a))
	}
	valid := a.body["isValid"] == true
	var reason string
	if !valid {
		reason = presence(str(a.body["invalidReason"]), str(a.body["errorType"]), "verify_failed")
		c.logFailure(ctx, "verify", reason, "status", a.status, "correlation_id", str(a.body["correlationId"]))
	}
	span.SetAttributes(attribute.Bool("x402.verify.valid", valid))
	return x402.VerifyResult{Valid: valid, Payer: str(a.body["payer"]), InvalidReason: reason, Response: a.body}, nil
}

// Settle submits payment; a call without an answer is the no_response
// outcome.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:33-43,57-87
func (c *cdp) Settle(ctx context.Context, payment, requirements x402.Object) (x402.SettleResult, error) {
	ctx, span := c.start(ctx, "settle")
	defer span.End()
	started := time.Now()
	res, err := c.settle(ctx, payment, requirements)
	if err != nil {
		return x402.SettleResult{}, c.fail(ctx, span, err)
	}
	span.SetAttributes(attribute.String("x402.settle.outcome", string(res.Outcome)))
	if !res.Settled() {
		c.logFailure(ctx, "settle", res.ErrorReason, "outcome", string(res.Outcome), "network", res.Network,
			"correlation_id", str(res.Response["correlationId"]), "duration", time.Since(started).Round(10*time.Millisecond))
	}
	return res, nil
}

func (c *cdp) settle(ctx context.Context, payment, requirements x402.Object) (x402.SettleResult, error) {
	a, err := c.post(ctx, "/settle", payment, requirements, settleReadTimeout)
	if err != nil {
		if nr := (*noResponse)(nil); errors.As(err, &nr) {
			return outcome(x402.SettleOutcomeNoResponse, "", nil, "no_response"), nil
		}
		return x402.SettleResult{}, err
	}
	if !screeningDecline(a) {
		if err := c.access(ctx, "settle", a); err != nil {
			return x402.SettleResult{}, err
		}
	}
	body := a.body
	switch {
	case !a.parsed:
		return outcome(x402.SettleOutcomeServerError, "", nil, "malformed_response"), nil
	case body["errorReason"] == "settlement_pending":
		return outcome(x402.SettleOutcomeSettlementPending, str(body["transaction"]), body, "settlement_pending"), nil
	case a.status >= 500:
		return outcome(x402.SettleOutcomeServerError, "", body, "server_error"), nil
	case body["success"] == true:
		if tx := str(body["transaction"]); present(tx) {
			return outcome(x402.SettleOutcomeSettled, tx, body, ""), nil
		}
		return outcome(x402.SettleOutcomeServerError, "", body, "malformed_response"), nil
	}
	reason := presence(str(body["errorReason"]), str(body["errorType"]), "unexpected_settle_error")
	return outcome(x402.SettleOutcomeUnconfirmedFailure, "", body, reason), nil
}

func outcome(o x402.SettleOutcome, tx string, body x402.Object, reason string) x402.SettleResult {
	if body == nil {
		body = x402.Object{}
	}
	return x402.SettleResult{
		Outcome: o, Transaction: tx, Network: str(body["network"]), Payer: str(body["payer"]),
		ErrorReason: reason, Response: body,
	}
}

// Supported lists the supported payment kinds.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:45-51
func (c *cdp) Supported(ctx context.Context) (x402.SupportedResult, error) {
	ctx, span := c.start(ctx, "supported")
	defer span.End()
	a, err := c.do(ctx, http.MethodGet, "/supported", nil, readTimeout)
	if err != nil {
		return x402.SupportedResult{}, c.fail(ctx, span, c.unanswered(ctx, "supported", err))
	}
	if err := c.access(ctx, "supported", a); err != nil {
		return x402.SupportedResult{}, c.fail(ctx, span, err)
	}
	kinds, ok := supportedKinds(a.body["kinds"])
	if a.status >= 500 || !a.parsed || !ok {
		return x402.SupportedResult{}, c.fail(ctx, span, c.unavailable(ctx, "supported", a))
	}
	return x402.SupportedResult{Kinds: kinds, Response: a.body}, nil
}

func supportedKinds(v any) ([]x402.SupportedKind, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	kinds := make([]x402.SupportedKind, 0, len(list))
	for _, k := range list {
		o, ok := k.(map[string]any)
		if !ok {
			return nil, false
		}
		kinds = append(kinds, o)
	}
	return kinds, true
}

// unanswered turns a call without an answer into an unavailable error;
// other failures (a secret that is not a key) stay as they are.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:126-131
func (c *cdp) unanswered(ctx context.Context, op string, err error) error {
	nr := (*noResponse)(nil)
	if !errors.As(err, &nr) {
		return err
	}
	c.logFailure(ctx, op, "no_response", "error", nr.desc)
	return &x402.FacilitatorError{Kind: x402.ErrFacilitatorUnavailable, Message: op + ": " + nr.desc}
}

// access returns the credential or rate-limit error of a refused call.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:133-144
func (c *cdp) access(ctx context.Context, op string, a answer) error {
	var kind error
	var reason string
	switch a.status {
	case http.StatusUnauthorized:
		kind, reason = x402.ErrFacilitatorCredentials, "unauthorized"
	case http.StatusPaymentRequired:
		kind, reason = x402.ErrFacilitatorCredentials, "payment_required"
	case http.StatusForbidden:
		kind, reason = x402.ErrFacilitatorCredentials, "forbidden"
	case http.StatusTooManyRequests:
		kind, reason = x402.ErrFacilitatorRateLimited, "rate_limited"
	default:
		return nil
	}
	errorType, correlation := str(a.body["errorType"]), str(a.body["correlationId"])
	c.logFailure(ctx, op, reason, "status", a.status, "error_type", errorType, "correlation_id", correlation)
	return &x402.FacilitatorError{
		Kind: kind, Message: fmt.Sprintf("%s: HTTP %d", op, a.status),
		HTTPStatus: a.status, ErrorType: errorType, CorrelationID: correlation,
	}
}

// unavailable returns the unavailable error of an unusable answer.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:159-162
func (c *cdp) unavailable(ctx context.Context, op string, a answer) error {
	reason := "malformed_response"
	if a.parsed && a.status >= 500 {
		reason = "server_error"
	}
	correlation := str(a.body["correlationId"])
	c.logFailure(ctx, op, reason, "status", a.status, "correlation_id", correlation)
	return &x402.FacilitatorError{
		Kind: x402.ErrFacilitatorUnavailable, Message: fmt.Sprintf("%s: HTTP %d", op, a.status),
		HTTPStatus: a.status, ErrorType: str(a.body["errorType"]), CorrelationID: correlation,
	}
}

// screeningDecline reports a 403 of the facilitator's risk screening: a
// verdict on the payment, not on the credentials.
func screeningDecline(a answer) bool {
	return a.status == http.StatusForbidden && a.body["errorType"] == "kyt_risk_detected"
}

func malformedVerdict(a answer) bool {
	v := a.body["isValid"]
	return a.status < 300 && v != true && v != false
}

// post sends payment and requirements in the payment's x402 version.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:89-100
func (c *cdp) post(ctx context.Context, path string, payment, requirements x402.Object, read time.Duration) (answer, error) {
	if err := serialisable(payment); err != nil {
		return answer{}, err
	}
	if err := serialisable(requirements); err != nil {
		return answer{}, err
	}
	body, err := json.Marshal(struct {
		X402Version         any         `json:"x402Version"`
		PaymentPayload      x402.Object `json:"paymentPayload"`
		PaymentRequirements x402.Object `json:"paymentRequirements"`
	}{payment["x402Version"], payment, requirements})
	if err != nil {
		return answer{}, fmt.Errorf("x402 cdp: encode %s: %w", strings.TrimPrefix(path, "/"), err)
	}
	return c.do(ctx, http.MethodPost, path, body, read)
}

// do signs and sends one request; a transport failure is a *noResponse.
func (c *cdp) do(ctx context.Context, method, path string, body []byte, read time.Duration) (answer, error) {
	token, err := cdpToken{
		keyID: c.keyID, secret: c.secret, method: method, host: CDPHost, path: CDPBasePath + path,
		now: c.f.cfg.now(), random: c.f.cfg.random,
	}.sign()
	if err != nil {
		return answer{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, openTimeout+read)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.f.base+path, rd)
	if err != nil {
		return answer{}, fmt.Errorf("x402 cdp: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.f.http.Do(req)
	if err != nil {
		return answer{}, &noResponse{desc: describe(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return answer{}, &noResponse{desc: describe(err)}
	}
	return parsedAnswer(resp.StatusCode, raw), nil
}

// parsedAnswer parses a body; an empty body is an empty object.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:120-124
func parsedAnswer(status int, raw []byte) answer {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	v, err := decodeJSON(raw)
	if o, ok := v.(map[string]any); err == nil && ok {
		return answer{status: status, body: o, parsed: true}
	}
	return answer{status: status, body: x402.Object{}}
}

// serialisable rejects a body JSON could not carry as given: a string
// that is not valid UTF-8.
func serialisable(v any) error {
	switch x := v.(type) {
	case string:
		if !utf8.ValidString(x) {
			return errors.New("x402 cdp: the payment holds a string that is not valid UTF-8")
		}
	case map[string]any:
		for k, e := range x {
			if err := serialisable(k); err != nil {
				return err
			}
			if err := serialisable(e); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range x {
			if err := serialisable(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *cdp) start(ctx context.Context, op string) (context.Context, trace.Span) {
	return c.f.tracer.Start(ctx, "x402.cdp."+op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("x402.facilitator", string(x402.FacilitatorCoinbaseCDP))))
}

func (c *cdp) fail(_ context.Context, span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	if fe := (*x402.FacilitatorError)(nil); errors.As(err, &fe) {
		span.SetAttributes(attribute.String("x402.facilitator.error", fe.Reason()))
	}
	return err
}

// logFailure logs a failed call with its non-empty details.
//
// source: app/services/x402/facilitator/coinbase_cdp_adapter.rb:164-167
func (c *cdp) logFailure(ctx context.Context, op, reason string, details ...any) {
	attrs := []any{"facilitator", string(x402.FacilitatorCoinbaseCDP), "operation", op, "reason", reason}
	for i := 0; i+1 < len(details); i += 2 {
		if s, ok := details[i+1].(string); ok && s == "" {
			continue
		}
		attrs = append(attrs, details[i], details[i+1])
	}
	c.f.cfg.logger.Log(ctx, slog.LevelWarn, "x402 facilitator call failed", attrs...)
}

// present reports a string that is not blank.
func present(s string) bool { return strings.TrimSpace(s) != "" }

// presence returns the first present string.
func presence(ss ...string) string {
	for _, s := range ss {
		if present(s) {
			return s
		}
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func object(v any) x402.Object {
	if o, ok := v.(map[string]any); ok {
		return o
	}
	return x402.Object{}
}

// decodeJSON decodes one JSON value, keeping numbers exact.
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}
