package moneyhash

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func TestAnswerError(t *testing.T) {
	now := testNow
	for _, tt := range []struct {
		status  int
		body    string
		header  string
		kind    payment.ErrorKind
		message string
		retry   time.Duration
	}{
		{401, `{"status":{"code":401,"message":"Invalid API key."}}`, "", payment.KindAuthentication, "Invalid API key.", 0},
		{403, ``, "", payment.KindAuthentication, "Forbidden", 0},
		{429, ``, "7", payment.KindRateLimited, "Too Many Requests", 7 * time.Second},
		{429, ``, now.Add(3 * time.Second).Format(http.TimeFormat), payment.KindRateLimited, "Too Many Requests", 3 * time.Second},
		{500, `oops`, "", payment.KindUnavailable, "Internal Server Error", 0},
		{404, ``, "", payment.KindNotFound, "Not Found", 0},
		{409, ``, "", payment.KindIdempotency, "Conflict", 0},
		{
			400, `{"status":{"code":400,"message":"Bad request","errors":[{"card_token":"This field may not be null.","amount":["Required.","Positive."]},"flow invalid"]}}`, "",
			payment.KindInvalidRequest, "Bad request; amount: Required. Positive.; card_token: This field may not be null.; flow invalid", 0,
		},
		{302, ``, "", payment.KindPermanent, "Found", 0},
	} {
		res := &http.Response{StatusCode: tt.status, Header: http.Header{}}
		if tt.header != "" {
			res.Header.Set("Retry-After", tt.header)
		}
		err := answerError(res, []byte(tt.body), now)
		pe, ok := payment.AsProviderError(err)
		if !ok {
			t.Fatalf("answerError(%d): got = %v, want = a ProviderError", tt.status, err)
		}
		if pe.Kind != tt.kind || pe.Message != tt.message || pe.RetryAfter != tt.retry || pe.HTTPStatus != tt.status {
			t.Errorf("answerError(%d): got = %s %q %v, want = %s %q %v", tt.status, pe.Kind, pe.Message, pe.RetryAfter, tt.kind, tt.message, tt.retry)
		}
	}
}

func TestTransportError(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := transportError(err); got != err { //nolint:errorlint // identity is the point
			t.Errorf("transportError(%v): got = %v, want = the error itself", err, got)
		}
	}
	if pe, ok := payment.AsProviderError(transportError(errors.New("connection refused"))); !ok || pe.Kind != payment.KindUnavailable {
		t.Errorf("transportError: got = %v, want = unavailable", pe)
	}
	// A refused connection is retryable.
	g := New(WithBaseURL("http://127.0.0.1:1"))
	_, err := g.CreatePayment(context.Background(), defaultConn(), paymentParams(payment.InvoicePayable(testInvoiceID)))
	if !errors.Is(err, payment.ErrRetryable) {
		t.Errorf("CreatePayment: got = %v, want = retryable", err)
	}
}

func TestRateLimit(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routeCustomers: {fixture("create_customer.json")}})
	// 20 requests per second with a burst of 20: the 22nd call waits for
	// two tokens, about 100ms.
	g := rp.gateway(WithRateLimit(20))
	start := time.Now()
	for range 22 {
		if _, err := g.CreateCustomer(context.Background(), defaultConn(), testCustomerParams()); err != nil {
			t.Fatalf("CreateCustomer: got = %v, want = nil", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Errorf("elapsed: got = %v, want = at least 80ms of throttling", elapsed)
	}
	before := rp.count()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.CreateCustomer(ctx, defaultConn(), testCustomerParams()); !errors.Is(err, context.Canceled) {
		t.Errorf("error: got = %v, want = %v", err, context.Canceled)
	}
	if rp.count() != before {
		t.Errorf("requests: got = %d, want = %d", rp.count(), before)
	}
}

func TestLimiters(t *testing.T) {
	l := newLimiters(100)
	other := core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-0000000000ff")
	get := func(ctx context.Context) any { return ctx.Value(limiterCtxKey{}) }
	ctx := context.Background()
	a, b := get(l.with(ctx, testConnID)), get(l.with(ctx, testConnID))
	if a == nil || a != b {
		t.Errorf("same connection: got = %p and %p, want = one limiter", a, b)
	}
	if c := get(l.with(ctx, other)); c == a {
		t.Errorf("two connections: got = one limiter, want = two")
	}
	if c := get(newLimiters(0).with(ctx, testConnID)); c != nil {
		t.Errorf("disabled: got = %v, want = no limiter", c)
	}
}

func TestTracing(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	rp := newReplay(t, map[string][]reply{routeIntents: {failFixture(http.StatusBadRequest, "recurring_mit_payment_failure_response.json")}})
	_, _ = rp.gateway(WithTracerProvider(tp)).CreatePayment(context.Background(), defaultConn(), paymentParams(payment.InvoicePayable(testInvoiceID)))
	var op sdktrace.ReadOnlySpan
	httpSpans := 0
	for _, s := range rec.Ended() {
		if s.Name() == "moneyhash.create_payment" {
			op = s
		} else {
			httpSpans++
		}
	}
	if op == nil {
		t.Fatalf("spans: got = %d without moneyhash.create_payment, want = the operation span", len(rec.Ended()))
	}
	if httpSpans == 0 {
		t.Errorf("HTTP spans: got = 0, want = the instrumented request")
	}
	attrs := map[attribute.Key]string{}
	for _, a := range op.Attributes() {
		attrs[a.Key] = a.Value.String()
	}
	for k, w := range map[attribute.Key]string{
		"payment.provider":      "moneyhash",
		"tanvo.organization.id": testOrgID.String(),
		"tanvo.integration.id":  testConnID.String(),
		"payment.error.kind":    "invalid_request",
		"payment.error.code":    "400",
	} {
		if attrs[k] != w {
			t.Errorf("attribute %s: got = %q, want = %q", k, attrs[k], w)
		}
	}
	if op.Status().Code.String() != "Error" {
		t.Errorf("status: got = %v, want = Error", op.Status())
	}
	if New().Provider() != Provider {
		t.Errorf("Provider: got = %q, want = %q", New().Provider(), Provider)
	}
}
