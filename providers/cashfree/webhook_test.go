package cashfree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// compact returns a fixture as lago's spec signs it, re-encoded without
// whitespace (JSON.parse(...).to_json).
func compact(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, readFixture(t, name)); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return b.Bytes()
}

const (
	testTimestamp = "1629271506"
	// lagoSignature is lago's recorded signature of its fixture.
	lagoSignature = "MFB3Rkubs4jB97ROS/I4iu9llAAP5ykJ3GZYp95o/Mw="
)

func TestVerifyWebhook(t *testing.T) {
	g := New()
	body := compact(t, "webhooks/payment_link_event_payment.json")
	// The recorded delivery as lago signed it, before its note keys were
	// renamed.
	lagoBody := bytes.ReplaceAll(body, []byte(`"tanvo_`), []byte(`"lago_`))
	header := func(sigH, sig, tsH, ts string) http.Header {
		h := http.Header{}
		if sig != "" {
			h.Set(sigH, sig)
		}
		if ts != "" {
			h.Set(tsH, ts)
		}
		return h
	}
	for _, tt := range []struct {
		name   string
		body   []byte
		header http.Header
		conn   payment.Connection
		ok     bool
	}{
		// source: spec/services/payment_providers/cashfree/handle_incoming_webhook_service_spec.rb:24
		{"recorded signature", lagoBody, header(LegacySignatureHeader, lagoSignature, LegacyTimestampHeader, testTimestamp), defaultConn(), true},
		{"webhook headers", lagoBody, header(SignatureHeader, lagoSignature, TimestampHeader, testTimestamp), defaultConn(), true},
		{"signed body", body, header(SignatureHeader, Sign(body, testTimestamp, testClientSecret), TimestampHeader, testTimestamp), defaultConn(), true},
		// source: spec/services/payment_providers/cashfree/handle_incoming_webhook_service_spec.rb:35
		{"invalid signature", lagoBody, header(SignatureHeader, "signature", TimestampHeader, testTimestamp), defaultConn(), false},
		{"other timestamp", lagoBody, header(SignatureHeader, lagoSignature, TimestampHeader, "1629271507"), defaultConn(), false},
		{"tampered body", body, header(SignatureHeader, lagoSignature, TimestampHeader, testTimestamp), defaultConn(), false},
		{"no signature", lagoBody, header(SignatureHeader, "", TimestampHeader, testTimestamp), defaultConn(), false},
		{"no secret", lagoBody, header(SignatureHeader, lagoSignature, TimestampHeader, testTimestamp), connection(`{}`, `{"client_id":"id"}`), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := g.VerifyWebhook(tt.conn, tt.body, tt.header)
			if tt.ok && err != nil {
				t.Errorf("VerifyWebhook: got = %v, want = nil", err)
			}
			if !tt.ok && !errors.Is(err, payment.ErrInvalidSignature) {
				t.Errorf("VerifyWebhook: got = %v, want = %v", err, payment.ErrInvalidSignature)
			}
		})
	}
	// The X-Cashfree-* pair is read first, as in lago.
	h := header(LegacySignatureHeader, lagoSignature, LegacyTimestampHeader, testTimestamp)
	h.Set(SignatureHeader, "other")
	h.Set(TimestampHeader, "0")
	if err := g.VerifyWebhook(defaultConn(), lagoBody, h); err != nil {
		t.Errorf("both header pairs: got = %v, want = nil", err)
	}
	if err := g.VerifyWebhook(connection(`{`, `{}`), lagoBody, h); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("malformed settings: got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestParseWebhook(t *testing.T) {
	invoiceID := core.MustParseID[core.InvoiceID](fixtureInvoiceID)
	requestID := core.MustParseID[core.PaymentRequestID](fixtureInvoiceID)
	occurred := time.Date(2021, 8, 18, 7, 25, 6, 0, time.UTC)
	for _, tt := range []struct {
		name    string
		fixture string
		payable payment.Payable
	}{
		// source: spec/services/payment_providers/cashfree/webhooks/payment_link_event_service_spec.rb:21
		// source: spec/services/payment_providers/cashfree/webhooks/payment_link_event_service_spec.rb:28
		// source: spec/services/payment_providers/cashfree/handle_event_service_spec.rb:29
		// source: spec/jobs/payment_providers/cashfree/handle_event_job_spec.rb:19
		{"invoice", "webhooks/payment_link_event_payment.json", payment.InvoicePayable(invoiceID)},
		// source: spec/services/payment_providers/cashfree/webhooks/payment_link_event_service_spec.rb:48
		{"payment request", "webhooks/payment_link_event_payment_request.json", payment.RequestPayable(requestID)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := New().ParseWebhook(context.Background(), defaultConn(), readFixture(t, tt.fixture))
			if err != nil || len(evs) != 1 {
				t.Fatalf("ParseWebhook: got = %v, %v, want = one event", evs, err)
			}
			ev, ok := evs[0].(payment.PaymentEvent)
			if !ok {
				t.Fatalf("event: got = %T, want = payment.PaymentEvent", evs[0])
			}
			want := payment.PaymentEvent{
				EventMeta: payment.EventMeta{
					ProviderEventID: "payment_ps11.PAID", Type: EventPaymentLink,
					OccurredAt: occurred, Livemode: false,
				},
				ProviderPaymentID: "payment_ps11",
				ProviderStatus:    "PAID",
				Status:            payment.StateSucceeded,
				Amount:            5500,
				Currency:          inr,
				Refs:              payment.Refs{Payable: tt.payable},
			}
			if ev.EventMeta != want.EventMeta || ev.ProviderPaymentID != want.ProviderPaymentID ||
				ev.ProviderStatus != want.ProviderStatus || ev.Status != want.Status ||
				ev.Amount != want.Amount || ev.Currency != want.Currency || ev.OneTime ||
				ev.Refs != want.Refs {
				t.Errorf("event: got = %+v, want = %+v", ev, want)
			}
		})
	}
}

func TestParseWebhookLinks(t *testing.T) {
	notes := func(n string) string {
		return `{"type":"PAYMENT_LINK_EVENT","event_time":"2021-08-18T12:55:06+05:30","data":{"link_id":"l1","link_status":"PAID",` +
			`"link_currency":"INR","link_amount_paid":55.5,"link_notes":` + n + `}}`
	}
	// A link created by CreatePaymentURL: one-time, with the customer.
	evs, err := New().ParseWebhook(context.Background(), connection(`{}`, `{}`), []byte(notes(
		`{"tanvo_customer_id":"`+testCustomerID.String()+`","tanvo_invoice_id":"`+testInvoiceID.String()+`","payment_type":"one-time"}`)))
	if err != nil || len(evs) != 1 {
		t.Fatalf("ParseWebhook: got = %v, %v, want = one event", evs, err)
	}
	ev := evs[0].(payment.PaymentEvent)
	if !ev.OneTime || ev.Refs.CustomerID != testCustomerID || ev.Refs.Payable != payment.InvoicePayable(testInvoiceID) ||
		ev.Amount != 5550 || !ev.Livemode {
		t.Errorf("event: got = %+v, want = one-time, live, 5550 for the test invoice", ev)
	}
	// An invoice payable type names the invoice by tanvo_payable_id.
	evs, _ = New().ParseWebhook(context.Background(), defaultConn(), []byte(notes(
		`{"tanvo_payable_id":"`+testInvoiceID.String()+`","tanvo_payable_type":"invoice"}`)))
	if len(evs) != 1 || evs[0].(payment.PaymentEvent).Refs.Payable != payment.InvoicePayable(testInvoiceID) {
		t.Errorf("payable id: got = %+v, want = the test invoice", evs)
	}

	for _, tt := range []struct {
		name string
		body string
		err  error
	}{
		{"not paid", strings.Replace(notes(`{"tanvo_invoice_id":"x"}`), `"PAID"`, `"PARTIALLY_PAID"`, 1), nil},
		{"other event", strings.Replace(notes(`{"tanvo_invoice_id":"x"}`), "PAYMENT_LINK_EVENT", "PAYMENT_SUCCESS_WEBHOOK", 1), nil},
		{"no payable", notes(`{}`), nil},
		{"no notes", notes(`null`), nil},
		{"unknown payable type", notes(`{"tanvo_payable_id":"x","tanvo_payable_type":"credit_note"}`), payment.ErrUnknownPayable},
		{"no link", strings.Replace(notes(`{"tanvo_invoice_id":"x"}`), `"link_id":"l1"`, `"link_id":""`, 1), payment.ErrInvalidPayload},
		{"bad currency", strings.Replace(notes(`{"tanvo_invoice_id":"x"}`), `"INR"`, `"XXQ"`, 1), payment.ErrInvalidPayload},
		{"bad amount", strings.Replace(notes(`{"tanvo_invoice_id":"x"}`), `55.5`, `"abc"`, 1), payment.ErrInvalidPayload},
		{"no type", `{"data":{}}`, payment.ErrInvalidPayload},
		{"not json", `{`, payment.ErrInvalidPayload},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := New().ParseWebhook(context.Background(), defaultConn(), []byte(tt.body))
			if tt.err == nil {
				if err != nil || len(evs) != 0 {
					t.Errorf("ParseWebhook: got = %v, %v, want = no event", evs, err)
				}
				return
			}
			if !errors.Is(err, tt.err) {
				t.Errorf("ParseWebhook: got = %v, want = %v", err, tt.err)
			}
		})
	}
	// A malformed ID is no reference; an absent amount is zero.
	evs, err = New().ParseWebhook(context.Background(), defaultConn(), []byte(
		`{"type":"PAYMENT_LINK_EVENT","data":{"link_id":"l1","link_status":"PAID","link_notes":{"tanvo_invoice_id":"x"}}}`))
	if err != nil || len(evs) != 1 {
		t.Fatalf("ParseWebhook: got = %v, %v, want = one event", evs, err)
	}
	if ev := evs[0].(payment.PaymentEvent); !ev.Refs.Payable.InvoiceID.IsZero() || ev.Amount != 0 || !ev.OccurredAt.IsZero() {
		t.Errorf("event: got = %+v, want = no payable, no amount, no time", ev)
	}
}
