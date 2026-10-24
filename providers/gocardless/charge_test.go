package gocardless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

const (
	routeCancel     = "POST /payments/PM123/actions/cancel"
	routeGetPayment = "GET /payments/PM123"
)

func invoicePayment() payment.PaymentParams {
	return payment.PaymentParams{
		PaymentID:          testPaymentID,
		Payable:            payment.InvoicePayable(testInvoiceID),
		InvoiceType:        "subscription",
		IssuingDate:        core.DateOf(testNow, time.UTC),
		CustomerID:         testCustomerID,
		ProviderCustomerID: testCustomer,
		Amount:             200,
		Currency:           eur,
		Description:        "Tanvo - Invoice TAN-001",
	}
}

func TestStatusOf(t *testing.T) {
	// source: app/models/payment_providers/gocardless_provider.rb:7-9
	tests := map[string]payment.State{
		"pending_customer_approval": payment.StateProcessing,
		"pending_submission":        payment.StateProcessing,
		"submitted":                 payment.StateProcessing,
		"confirmed":                 payment.StateProcessing,
		"paid_out":                  payment.StateSucceeded,
		"cancelled":                 payment.StateFailed,
		"customer_approval_denied":  payment.StateFailed,
		"failed":                    payment.StateFailed,
		"charged_back":              payment.StateFailed,
		"created":                   payment.StatePending,
		"":                          payment.StatePending,
	}
	for status, want := range tests {
		if got := StatusOf(status); got != want {
			t.Errorf("StatusOf(%q): got = %q, want = %q", status, got, want)
		}
	}
}

func TestCreatePayment(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/payments/create_service_spec.rb:73
	rp := newReplay(t, map[string][]reply{
		routeMandates: {fixture("mandates_list_response.json")},
		routePayments: {created("payments_response.json")},
	})
	res, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), invoicePayment())
	if err != nil {
		t.Fatalf("CreatePayment: got = %v, want = nil", err)
	}
	want := payment.PaymentResult{
		ProviderPaymentID: "PM123",
		ProviderStatus:    "pending_submission",
		Status:            payment.StateProcessing,
		ProviderMethodID:  "MD123",
		Amount:            200,
		Currency:          eur,
	}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result: got = %+v, want = %+v", res, want)
	}
	list := rp.only(routeMandates)
	if got := list.Query.Get("customer"); got != testCustomer {
		t.Errorf("mandates customer: got = %q, want = %q", got, testCustomer)
	}
	if got := list.Query["status"]; len(got) != 1 || got[0] != "pending_customer_approval,pending_submission,submitted,active" {
		t.Errorf("mandates status: got = %q, want = the usable statuses, comma-separated", got)
	}
	req := rp.only(routePayments)
	checks := map[string]string{
		"amount":                        "200",
		"currency":                      "EUR",
		"links.mandate":                 "MD123",
		"metadata.tanvo_customer_id":    testCustomerID.String(),
		"metadata.tanvo_invoice_id":     testInvoiceID.String(),
		"metadata.invoice_issuing_date": "2026-05-07",
	}
	for path, w := range checks {
		if got := req.str(append([]string{"payments"}, strings.Split(path, ".")...)...); got != w {
			t.Errorf("payments.%s: got = %q, want = %q", path, got, w)
		}
	}
	// retry_if_possible is left to the API default, false, which lago sends.
	if req.has("payments", "retry_if_possible") {
		t.Errorf("payments.retry_if_possible: got = %q, want = absent", req.str("payments", "retry_if_possible"))
	}
	if meta, _ := req.Body["payments"].(map[string]any)["metadata"].(map[string]any); len(meta) != 3 {
		t.Errorf("metadata: got = %v, want = three keys (GoCardless' limit)", meta)
	}
	if req.IdempotencyKey != "payment-"+testPaymentID.String() {
		t.Errorf("Idempotency-Key: got = %q, want = %q", req.IdempotencyKey, "payment-"+testPaymentID.String())
	}
}

func TestCreatePaymentWithMethod(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/payments/create_service_spec.rb:158
	rp := newReplay(t, map[string][]reply{routePayments: {created("payments_response.json")}})
	p := invoicePayment()
	p.ProviderMethodID = "mandate_id2"
	res, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p)
	if err != nil {
		t.Fatalf("CreatePayment: got = %v, want = nil", err)
	}
	if res.ProviderMethodID != "mandate_id2" {
		t.Errorf("ProviderMethodID: got = %q, want = %q", res.ProviderMethodID, "mandate_id2")
	}
	if got := rp.only(routePayments).str("payments", "links", "mandate"); got != "mandate_id2" {
		t.Errorf("links.mandate: got = %q, want = %q", got, "mandate_id2")
	}
	if n := len(rp.requests(routeMandates)); n != 0 {
		t.Errorf("mandate lists: got = %d, want = 0", n)
	}
}

func TestCreatePaymentRequest(t *testing.T) {
	// source: spec/services/payment_requests/payments/gocardless_service_spec.rb:74
	rp := newReplay(t, map[string][]reply{routePayments: {created("payments_response.json")}})
	p := invoicePayment()
	p.Payable = payment.RequestPayable(testRequestID)
	p.ProviderMethodID = "MD123"
	if _, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p); err != nil {
		t.Fatalf("CreatePayment: got = %v, want = nil", err)
	}
	req := rp.only(routePayments)
	want := map[string]any{
		"tanvo_customer_id":  testCustomerID.String(),
		"tanvo_payable_type": "payment_request",
		"tanvo_payable_id":   testRequestID.String(),
	}
	if got := req.Body["payments"].(map[string]any)["metadata"]; !reflect.DeepEqual(got, want) {
		t.Errorf("metadata: got = %v, want = %v", got, want)
	}
}

func TestCreatePaymentNoMandate(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/payments/create_service_spec.rb:136
	rp := newReplay(t, map[string][]reply{routeMandates: {fixture("mandates_empty_response.json")}})
	_, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), invoicePayment())
	if !errors.Is(err, payment.ErrNoPaymentMethod) || !errors.Is(err, payment.ErrRejected) {
		t.Fatalf("error: got = %v, want = %v and rejected", err, payment.ErrNoPaymentMethod)
	}
	pe, _ := payment.AsProviderError(err)
	if pe.Code != "no_mandate_error" || pe.Message != "No mandate available for payment" {
		t.Errorf("error: got = %q %q, want = no_mandate_error: No mandate available for payment", pe.Code, pe.Message)
	}
	if n := len(rp.requests(routePayments)); n != 0 {
		t.Errorf("payments: got = %d, want = 0", n)
	}
}

func TestCreatePaymentErrors(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/payments/create_service_spec.rb:109
	tests := []struct {
		name      string
		reply     reply
		wantKind  payment.ErrorKind
		wantCode  string
		retryable bool
	}{
		{"validation failed", failFixture(http.StatusUnprocessableEntity, "validation_failed_response.json"), payment.KindInvalidRequest, "mandate_is_inactive", false},
		{"invalid state", failFixture(http.StatusUnprocessableEntity, "invalid_state_response.json"), payment.KindPermanent, "mandate_is_inactive", false},
		{"server error", failFixture(http.StatusInternalServerError, "server_error_response.json"), payment.KindUnavailable, "internal_server_error", true},
		{"rate limited", failFixture(http.StatusTooManyRequests, "rate_limit_response.json"), payment.KindRateLimited, "rate_limit_exceeded", true},
		{"authentication", failFixture(http.StatusUnauthorized, "authentication_failed_response.json"), payment.KindAuthentication, "access_token_not_found", false},
		{"undecodable error", raw(http.StatusBadGateway, "<html>bad gateway</html>"), payment.KindUnavailable, "", true},
		{"undecodable success", raw(http.StatusCreated, "{"), payment.KindUnavailable, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routePayments: {tt.reply}})
			p := invoicePayment()
			p.ProviderMethodID = "MD123"
			_, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p)
			pe, ok := payment.AsProviderError(err)
			if !ok || pe.Kind != tt.wantKind || pe.Code != tt.wantCode {
				t.Fatalf("error: got = %v, want = %s (%s)", err, tt.wantKind, tt.wantCode)
			}
			if errors.Is(err, payment.ErrRetryable) != tt.retryable {
				t.Errorf("retryable: got = %v, want = %v", !tt.retryable, tt.retryable)
			}
			if pe.HTTPStatus != tt.reply.status {
				t.Errorf("status: got = %d, want = %d", pe.HTTPStatus, tt.reply.status)
			}
			if n := len(rp.requests(routePayments)); n != 1 {
				t.Errorf("attempts: got = %d, want = 1 (no in-process retry)", n)
			}
		})
	}
}

func TestCreatePaymentRetryAfter(t *testing.T) {
	r := failFixture(http.StatusTooManyRequests, "rate_limit_response.json")
	r.header = http.Header{"Ratelimit-Reset": {testNow.Add(30 * time.Second).Format(http.TimeFormat)}}
	rp := newReplay(t, map[string][]reply{routePayments: {r}})
	p := invoicePayment()
	p.ProviderMethodID = "MD123"
	_, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p)
	pe, ok := payment.AsProviderError(err)
	if !ok || pe.RetryAfter != 30*time.Second || pe.RequestID != "REQ_RATE" {
		t.Errorf("error: got = %+v, want = a 30s retry delay of request REQ_RATE", pe)
	}
}

func TestCreatePaymentConflict(t *testing.T) {
	// gocardless-pro returns the resource an idempotent creation conflict
	// links (services/payments_service.rb create).
	rp := newReplay(t, map[string][]reply{
		routePayments:   {failFixture(http.StatusConflict, "payment_conflict_response.json")},
		routeGetPayment: {fixture("payments_response.json")},
	})
	p := invoicePayment()
	p.ProviderMethodID = "MD123"
	res, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p)
	if err != nil || res.ProviderPaymentID != "PM123" || res.Status != payment.StateProcessing {
		t.Errorf("CreatePayment: got = %+v, %v, want = the existing payment", res, err)
	}

	// A conflict without a linked resource is an idempotency error.
	rp = newReplay(t, map[string][]reply{routePayments: {raw(http.StatusConflict,
		`{"error":{"type":"invalid_state","code":409,"message":"conflict","errors":[{"reason":"idempotent_creation_conflict"}]}}`)}})
	_, err = rp.gateway().CreatePayment(context.Background(), defaultConn(), p)
	if k := payment.ErrorKindOf(err); k != payment.KindIdempotency {
		t.Errorf("error: got = %v, want = %s", err, payment.KindIdempotency)
	}
}

func TestCreatePaymentInvalid(t *testing.T) {
	rp := newReplay(t, map[string][]reply{})
	p := invoicePayment()
	p.Amount = 0
	var ve *core.ValidationError
	if _, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p); !errors.As(err, &ve) {
		t.Errorf("error: got = %v, want = a validation error", err)
	}
	if rp.count() != 0 {
		t.Errorf("requests: got = %d, want = 0", rp.count())
	}
}

func TestConnectionError(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/payments/cancel_service_spec.rb:131
	rp := newReplay(t, map[string][]reply{})
	rp.srv.Close()
	_, err := rp.gateway().CancelPayment(context.Background(), defaultConn(), "PM123")
	if k := payment.ErrorKindOf(err); k != payment.KindUnavailable || !errors.Is(err, payment.ErrRetryable) {
		t.Errorf("error: got = %q (%v), want = unavailable and retryable", k, err)
	}
}

func TestCancelPayment(t *testing.T) {
	const src = "spec/services/payment_providers/gocardless/payments/cancel_service_spec.rb:"
	cancelled := payment.PaymentResult{
		ProviderPaymentID: "PM123", ProviderStatus: "cancelled", Status: payment.StateFailed,
		ProviderMethodID: "MD123", Amount: 200, Currency: eur,
	}
	tests := []struct {
		sources  []string
		name     string
		reply    reply
		want     payment.PaymentResult
		wantKind payment.ErrorKind
	}{
		{[]string{src + "36", src + "51", src + "56", src + "62"}, "cancelled", fixture("payments_cancelled_response.json"), cancelled, ""},
		{[]string{src + "78", src + "92"}, "not cancelable", failFixture(http.StatusUnprocessableEntity, "cancellation_failed_response.json"), payment.PaymentResult{}, ""},
		{[]string{src + "106"}, "other invalid state", failFixture(http.StatusUnprocessableEntity, "invalid_state_response.json"), payment.PaymentResult{}, payment.KindPermanent},
		{[]string{src + "120"}, "server error", failFixture(http.StatusInternalServerError, "server_error_response.json"), payment.PaymentResult{}, payment.KindUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routeCancel: {tt.reply}})
			res, err := rp.gateway().CancelPayment(context.Background(), defaultConn(), "PM123")
			if k := payment.ErrorKindOf(err); k != tt.wantKind || (tt.wantKind == "" && err != nil) {
				t.Fatalf("%v: error: got = %v, want kind = %q", tt.sources, err, tt.wantKind)
			}
			if !reflect.DeepEqual(res, tt.want) {
				t.Errorf("%v: result: got = %+v, want = %+v", tt.sources, res, tt.want)
			}
			rp.only(routeCancel)
		})
	}
}

func TestCancelPaymentLogs(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/payments/cancel_service_spec.rb:83
	var buf bytes.Buffer
	rp := newReplay(t, map[string][]reply{routeCancel: {failFixture(http.StatusUnprocessableEntity, "cancellation_failed_response.json")}})
	g := rp.gateway(WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	if _, err := g.CancelPayment(context.Background(), defaultConn(), "PM123"); err != nil {
		t.Fatalf("CancelPayment: got = %v, want = nil", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log: got = %q, want = one JSON record", buf.String())
	}
	if rec["level"] != "INFO" || rec["msg"] != "gocardless payment not cancelable" ||
		rec["message"] != "This payment cannot be cancelled, its status is submitted" || rec["provider_payment_id"] != "PM123" {
		t.Errorf("log: got = %v, want = an Info record with the message", rec)
	}
}
