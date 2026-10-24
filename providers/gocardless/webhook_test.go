package gocardless

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func signed(body []byte) http.Header {
	h := http.Header{}
	h.Set(SignatureHeader, Sign(body, testWebhookSecret))
	return h
}

func TestVerifyWebhook(t *testing.T) {
	const src = "spec/services/payment_providers/gocardless/handle_incoming_webhook_service_spec.rb:"
	body := readFixture(t, filepath.Join("webhooks", "events.json"))
	g := New()
	// source: spec/services/payment_providers/gocardless/handle_incoming_webhook_service_spec.rb:24
	if err := g.VerifyWebhook(defaultConn(), body, signed(body)); err != nil {
		t.Errorf("valid signature: got = %v, want = nil", err)
	}
	// source: spec/services/payment_providers/gocardless/handle_incoming_webhook_service_spec.rb:49
	bad := http.Header{}
	bad.Set(SignatureHeader, Sign(body, "other-secret"))
	tampered := append(bytes.Clone(body), ' ')
	noSecret := defaultConn()
	noSecret.Secrets = []byte(`{"access_token":"` + testAccessToken + `"}`)
	for name, tt := range map[string]struct {
		conn   payment.Connection
		body   []byte
		header http.Header
	}{
		"wrong secret":   {defaultConn(), body, bad},
		"tampered body":  {defaultConn(), tampered, signed(body)},
		"missing header": {defaultConn(), body, http.Header{}},
		"no secret":      {noSecret, body, signed(body)},
	} {
		if err := g.VerifyWebhook(tt.conn, tt.body, tt.header); !errors.Is(err, payment.ErrInvalidSignature) {
			t.Errorf("%s (%s49): got = %v, want = %v", name, src, err, payment.ErrInvalidSignature)
		}
	}
}

func TestParseWebhookInvalid(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/handle_incoming_webhook_service_spec.rb:36
	for _, body := range []string{"", "invalid", `{"meta":{}}`} {
		if _, err := New().ParseWebhook(context.Background(), defaultConn(), []byte(body)); !errors.Is(err, payment.ErrInvalidPayload) {
			t.Errorf("ParseWebhook(%q): got = %v, want = %v", body, err, payment.ErrInvalidPayload)
		}
	}
}

func parse(t *testing.T, g *Gateway, name string) []payment.Event {
	t.Helper()
	evs, err := g.ParseWebhook(context.Background(), defaultConn(), readFixture(t, filepath.Join("webhooks", name)))
	if err != nil {
		t.Fatalf("ParseWebhook(%s): got = %v, want = nil", name, err)
	}
	return evs
}

func TestParseWebhookPayment(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/handle_event_service_spec.rb:19
	evs := parse(t, New(), "events.json")
	want := []payment.Event{payment.PaymentEvent{
		EventMeta: payment.EventMeta{
			ProviderEventID: "EVTESTYM78PGEM", Type: "payments.paid_out",
			OccurredAt: time.Date(2022, 10, 29, 16, 26, 50, 380_000_000, time.UTC),
		},
		ProviderPaymentID: "PM0068WBTXDQ0Q", ProviderStatus: "paid_out", Status: payment.StateSucceeded,
	}}
	if !reflect.DeepEqual(evs, want) {
		t.Errorf("events: got = %+v, want = %+v", evs, want)
	}

	// source: spec/services/payment_providers/gocardless/handle_event_service_spec.rb:38
	evs = parse(t, New(), "events_payment_request.json")
	if len(evs) != 1 {
		t.Fatalf("events: got = %d, want = 1", len(evs))
	}
	if pe := evs[0].(payment.PaymentEvent); pe.Status != payment.StateSucceeded {
		t.Errorf("payment request event: got = %+v, want = succeeded", pe)
	}
}

func TestParseWebhookRefs(t *testing.T) {
	body := `{"events":[{"id":"EV1","created_at":"2026-05-07T10:00:00.000Z","resource_type":"payments","action":"failed",
	"links":{"payment":"PM1"},
	"resource_metadata":{"tanvo_customer_id":"` + testCustomerID.String() + `","tanvo_payable_type":"payment_request","tanvo_payable_id":"` + testRequestID.String() + `"},
	"metadata":{}}]}`
	conn := defaultConn()
	conn.Secrets = []byte(`{"access_token":"live_token"}`)
	evs, err := New().ParseWebhook(context.Background(), conn, []byte(body))
	if err != nil || len(evs) != 1 {
		t.Fatalf("ParseWebhook: got = %v, %v, want = one event", evs, err)
	}
	pe := evs[0].(payment.PaymentEvent)
	want := payment.Refs{CustomerID: testCustomerID, Payable: payment.RequestPayable(testRequestID)}
	if pe.Refs != want || pe.Status != payment.StateFailed || !pe.Livemode {
		t.Errorf("event: got = %+v, want = failed live refs %+v", pe, want)
	}

	// The event's metadata wins over the resource's; invoice refs.
	body = `{"events":[{"id":"EV2","resource_type":"payments","action":"cancelled","links":{"payment":"PM1"},
	"resource_metadata":{"tanvo_invoice_id":"bogus"},
	"metadata":{"tanvo_invoice_id":"` + testInvoiceID.String() + `"}}]}`
	evs, err = New().ParseWebhook(context.Background(), defaultConn(), []byte(body))
	if err != nil || len(evs) != 1 {
		t.Fatalf("ParseWebhook: got = %v, %v, want = one event", evs, err)
	}
	if got := evs[0].(payment.PaymentEvent).Refs; got.InvoiceID != testInvoiceID || got.Payable != payment.InvoicePayable(testInvoiceID) {
		t.Errorf("refs: got = %+v, want = the invoice", got)
	}
}

func TestParseWebhookUnknownPayable(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/handle_event_service_spec.rb:55
	var logs bytes.Buffer
	g := New(WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if evs := parse(t, g, "events_invalid_payable_type.json"); len(evs) != 0 {
		t.Errorf("events: got = %+v, want = none", evs)
	}
	if !strings.Contains(logs.String(), "unknown payable") {
		t.Errorf("logs: got = %q, want = a warning", logs.String())
	}
}

func TestParseWebhookRefund(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/handle_event_service_spec.rb:69
	evs := parse(t, New(), "events_refund.json")
	want := []payment.Event{payment.RefundEvent{
		EventMeta: payment.EventMeta{
			ProviderEventID: "EVTESTJZ4276PM", Type: "refunds.paid",
			OccurredAt: time.Date(2022, 11, 24, 14, 8, 43, 756_000_000, time.UTC),
		},
		ProviderRefundID: "index_ID_123", ProviderStatus: "paid", Status: core.RefundStatusSucceeded,
	}}
	if !reflect.DeepEqual(evs, want) {
		t.Errorf("events: got = %+v, want = %+v", evs, want)
	}
}

func TestParseWebhookIgnored(t *testing.T) {
	body := `{"events":[
	{"id":"EV1","resource_type":"payments","action":"submitted","links":{"payment":"PM1"}},
	{"id":"EV2","resource_type":"refunds","action":"refund_settled_partially","links":{"refund":"RF1"}},
	{"id":"EV3","resource_type":"mandates","action":"expired","links":{"mandate":"MD1"}},
	{"id":"EV4","resource_type":"payouts","action":"paid","links":{}}]}`
	evs, err := New().ParseWebhook(context.Background(), defaultConn(), []byte(body))
	if err != nil || len(evs) != 0 {
		t.Errorf("ParseWebhook: got = %+v, %v, want = no events", evs, err)
	}
}

func TestParseWebhookMandateCreated(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/handle_event_service_spec.rb:88
	// source: spec/services/payment_providers/gocardless/webhooks/mandate_created_service_spec.rb:37
	// source: spec/services/payment_providers/gocardless/webhooks/mandate_created_service_spec.rb:41
	rp := newReplay(t, map[string][]reply{"GET /mandates/index_ID_123": {fixture("mandate_response.json")}})
	evs := parse(t, rp.gateway(), "events_mandate_created.json")
	want := []payment.Event{payment.PaymentMethodAttached{
		EventMeta: payment.EventMeta{
			ProviderEventID: "EVTEST6XZZGP6E", Type: "mandates.created",
			OccurredAt: time.Date(2026, 2, 3, 10, 53, 48, 287_000_000, time.UTC),
		},
		ProviderCustomerID: "CU123456", ProviderMethodID: "index_ID_123",
		Details: payment.MethodDetails{Type: "bacs"}, SetAsDefault: true,
	}}
	if !reflect.DeepEqual(evs, want) {
		t.Errorf("events: got = %+v, want = %+v", evs, want)
	}
	if got := rp.only("GET /mandates/index_ID_123").Authorization; got != "Bearer "+testAccessToken {
		t.Errorf("Authorization: got = %q, want = the connection's token", got)
	}
}

func TestParseWebhookMandateCreatedFetchFails(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/webhooks/mandate_created_service_spec.rb:62
	// source: spec/services/payment_providers/gocardless/webhooks/mandate_created_service_spec.rb:66
	rp := newReplay(t, map[string][]reply{"GET /mandates/index_ID_123": {failFixture(http.StatusNotFound, "not_found_response.json")}})
	if evs := parse(t, rp.gateway(), "events_mandate_created.json"); len(evs) != 0 {
		t.Errorf("events: got = %+v, want = none", evs)
	}
	// Other failures fail the delivery so GoCardless redelivers it.
	rp = newReplay(t, map[string][]reply{"GET /mandates/index_ID_123": {failFixture(http.StatusInternalServerError, "server_error_response.json")}})
	_, err := rp.gateway().ParseWebhook(context.Background(), defaultConn(), readFixture(t, filepath.Join("webhooks", "events_mandate_created.json")))
	if !errors.Is(err, payment.ErrRetryable) {
		t.Errorf("ParseWebhook: got = %v, want = retryable", err)
	}
}

func TestParseWebhookMandateCancelled(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/handle_event_service_spec.rb:105
	// source: spec/services/payment_providers/gocardless/webhooks/mandate_cancelled_service_spec.rb:39
	// source: spec/services/payment_providers/gocardless/webhooks/mandate_cancelled_service_spec.rb:43
	evs := parse(t, New(), "events_mandate_cancelled.json")
	want := []payment.Event{payment.PaymentMethodDetached{
		EventMeta: payment.EventMeta{
			ProviderEventID: "EVTEST7YZZGP7F", Type: "mandates.cancelled",
			OccurredAt: time.Date(2026, 2, 3, 10, 53, 48, 287_000_000, time.UTC),
		},
		ProviderMethodID: "index_ID_123",
	}}
	if !reflect.DeepEqual(evs, want) {
		t.Errorf("events: got = %+v, want = %+v", evs, want)
	}
	// source: spec/services/payment_providers/gocardless/handle_event_service_spec.rb:122
	if evs := parse(t, New(), "events_mandate_cancelled_by_bank.json"); len(evs) != 0 {
		t.Errorf("bank cancellation: got = %+v, want = none", evs)
	}
}

func TestParseWebhookBatch(t *testing.T) {
	body := `{"events":[
	{"id":"EV1","resource_type":"payments","action":"paid_out","links":{"payment":"PM1"}},
	{"id":"EV2","resource_type":"payments","action":"paid_out","links":{"payment":"PM2"},"metadata":{"tanvo_payable_type":"Nope"}},
	{"id":"EV3","resource_type":"refunds","action":"failed","links":{"refund":"RF1"}}]}`
	evs, err := New(WithLogger(slog.New(slog.DiscardHandler))).ParseWebhook(context.Background(), defaultConn(), []byte(body))
	if err != nil || len(evs) != 2 {
		t.Fatalf("ParseWebhook: got = %+v, %v, want = two events", evs, err)
	}
	if got := evs[1].(payment.RefundEvent).Status; got != core.RefundStatusFailed {
		t.Errorf("refund status: got = %q, want = failed", got)
	}
}
