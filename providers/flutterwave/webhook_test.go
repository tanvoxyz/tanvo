package flutterwave

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func TestVerifyWebhook(t *testing.T) {
	g := New()
	body := readFixture(t, "webhooks/charge_completed.json")
	hash := func(v string) http.Header {
		h := http.Header{}
		if v != "" {
			h.Set(SignatureHeader, v)
		}
		return h
	}
	for _, tt := range []struct {
		name   string
		header http.Header
		conn   payment.Connection
		ok     bool
	}{
		// source: spec/services/payment_providers/flutterwave/handle_incoming_webhook_service_spec.rb:39
		// source: spec/services/payment_providers/flutterwave/handle_incoming_webhook_service_spec.rb:43
		{"valid secret", hash(testWebhookSecret), defaultConn(), true},
		// source: spec/services/payment_providers/flutterwave/handle_incoming_webhook_service_spec.rb:53
		{"invalid secret", hash("invalid_secret"), defaultConn(), false},
		{"no header", hash(""), defaultConn(), false},
		// source: spec/services/payment_providers/flutterwave/handle_incoming_webhook_service_spec.rb:71
		{"no webhook secret", hash(testWebhookSecret), connection(`{}`, `{"secret_key":"`+testSecretKey+`"}`), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := g.VerifyWebhook(tt.conn, body, tt.header)
			if tt.ok && err != nil {
				t.Errorf("VerifyWebhook: got = %v, want = nil", err)
			}
			if !tt.ok && !errors.Is(err, payment.ErrInvalidSignature) {
				t.Errorf("VerifyWebhook: got = %v, want = %v", err, payment.ErrInvalidSignature)
			}
		})
	}
	if err := g.VerifyWebhook(connection(`{`, `{}`), body, hash(testWebhookSecret)); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("malformed settings: got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestParseWebhook(t *testing.T) {
	occurred := time.Date(2020, 7, 6, 19, 17, 4, 0, time.UTC)
	for _, tt := range []struct {
		name    string
		fixture string
		verify  string
		payable payment.Payable
		amount  int64
		cur     string
	}{
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:104
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:118
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:132
		// source: spec/services/payment_providers/flutterwave/handle_event_service_spec.rb:30
		// source: spec/jobs/payment_providers/flutterwave/handle_event_job_spec.rb:12
		{"invoice", "webhooks/charge_completed.json", "verify_transaction_response.json", payment.InvoicePayable(testInvoiceID), 1000000, "NGN"},
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:343
		{"payment request", "webhooks/charge_completed_payment_request.json", "verify_transaction_response.json", payment.RequestPayable(testRequestID), 1000000, "NGN"},
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:439
		{"two decimals", "webhooks/charge_completed.json", "verify_transaction_usd_response.json", payment.InvoicePayable(testInvoiceID), 10050, "USD"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routeVerify: {fixture(tt.verify)}})
			evs, err := rp.gateway().ParseWebhook(context.Background(), defaultConn(), readFixture(t, tt.fixture))
			if err != nil || len(evs) != 1 {
				t.Fatalf("ParseWebhook: got = %v, %v, want = one event", evs, err)
			}
			ev, ok := evs[0].(payment.PaymentEvent)
			if !ok {
				t.Fatalf("event: got = %T, want = payment.PaymentEvent", evs[0])
			}
			if r := rp.only(routeVerify); r.Header.Get("Authorization") != "Bearer "+testSecretKey {
				t.Errorf("Authorization: got = %q, want = %q", r.Header.Get("Authorization"), "Bearer "+testSecretKey)
			}
			want := payment.EventMeta{
				ProviderEventID: testTransactionID + ".successful", Type: EventChargeCompleted,
				OccurredAt: occurred, Livemode: false,
			}
			if ev.EventMeta != want || ev.ProviderPaymentID != testTransactionID || ev.ProviderStatus != "successful" ||
				ev.Status != payment.StateSucceeded || ev.Amount != tt.amount || ev.Currency.Code() != tt.cur ||
				!ev.OneTime || ev.Refs != (payment.Refs{Payable: tt.payable}) {
				t.Errorf("event: got = %+v, want = %s %d %s for %v", ev, testTransactionID, tt.amount, tt.cur, tt.payable)
			}
		})
	}
}

func TestParseWebhookNoEvent(t *testing.T) {
	charge := string(readFixture(t, "webhooks/charge_completed.json"))
	noMeta := `{"event":"charge.completed","data":{"id":285959875,"tx_ref":"lago_invoice_12345","amount":10000,"currency":"NGN","status":"successful"}}`
	for _, tt := range []struct {
		name   string
		body   string
		verify []reply
	}{
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:191
		{"failed charge", string(readFixture(t, "webhooks/charge_failed.json")), nil},
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:224
		{"no payable", `{"event":"charge.completed","data":{"id":285959875,"amount":10000,"currency":"NGN","status":"successful"}}`, nil},
		// source: spec/services/payment_providers/flutterwave/handle_event_service_spec.rb:58
		{"other event", strings.Replace(charge, "charge.completed", "transfer.completed", 1), nil},
		// source: spec/services/payment_providers/flutterwave/handle_event_service_spec.rb:85
		{"no event", `{"data":{"status":"successful"}}`, nil},
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:521
		// A reference that is not a Tanvo ID names no payable: nothing is verified.
		{"no meta", noMeta, nil},
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:247
		{"verification fails", charge, []reply{fixture("verify_transaction_failure_response.json")}},
		{"transaction not successful", charge, []reply{raw(http.StatusOK, `{"status":"success","data":{"id":285959875,"status":"pending"}}`)}},
		{"verification refused", charge, []reply{raw(http.StatusNotFound, `{"status":"error","message":"No transaction was found for this id"}`)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			routes := map[string][]reply{}
			if tt.verify != nil {
				routes[routeVerify] = tt.verify
			}
			rp := newReplay(t, routes)
			evs, err := rp.gateway().ParseWebhook(context.Background(), defaultConn(), []byte(tt.body))
			if err != nil || len(evs) != 0 {
				t.Errorf("ParseWebhook: got = %v, %v, want = no event", evs, err)
			}
			if tt.verify == nil && rp.count() != 0 {
				t.Errorf("requests: got = %d, want = 0", rp.count())
			}
		})
	}
	// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:263
	// Without a secret key nothing is verified.
	rp := newReplay(t, nil)
	evs, err := rp.gateway().ParseWebhook(context.Background(), connection(`{}`, `{}`), []byte(charge))
	if err != nil || len(evs) != 0 || rp.count() != 0 {
		t.Errorf("no secret key: got = %v, %v, %d requests, want = no event and no request", evs, err, rp.count())
	}
}

func TestParseWebhookErrors(t *testing.T) {
	charge := string(readFixture(t, "webhooks/charge_completed.json"))
	for _, tt := range []struct {
		name   string
		body   string
		verify []reply
		err    error
	}{
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:385
		{"invalid payable type", strings.Replace(charge, `"tanvo_payable_type": "invoice"`, `"tanvo_payable_type": "InvalidType"`, 1), nil, payment.ErrUnknownPayable},
		// source: spec/services/payment_providers/flutterwave/handle_event_service_spec.rb:73
		{"invalid json", `{`, nil, payment.ErrInvalidPayload},
		{"no transaction id", strings.Replace(charge, `"id": 285959875,`, ``, 1), nil, payment.ErrInvalidPayload},
		// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:281
		{"verification unavailable", charge, []reply{raw(http.StatusInternalServerError, `Connection failed`)}, payment.ErrRetryable},
		{"bad currency", charge, []reply{raw(http.StatusOK, `{"status":"success","data":{"id":285959875,"status":"successful","amount":1,"currency":"XXQ"}}`)}, payment.ErrInvalidPayload},
		{"bad amount", charge, []reply{raw(http.StatusOK, `{"status":"success","data":{"id":285959875,"status":"successful","amount":"abc","currency":"NGN"}}`)}, payment.ErrInvalidPayload},
	} {
		t.Run(tt.name, func(t *testing.T) {
			routes := map[string][]reply{}
			if tt.verify != nil {
				routes[routeVerify] = tt.verify
			}
			rp := newReplay(t, routes)
			_, err := rp.gateway().ParseWebhook(context.Background(), defaultConn(), []byte(tt.body))
			if !errors.Is(err, tt.err) {
				t.Errorf("ParseWebhook: got = %v, want = %v", err, tt.err)
			}
		})
	}
}

func TestParseWebhookRefs(t *testing.T) {
	// source: spec/services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:485
	// A page made by CreatePaymentURL: meta with the customer, a live key,
	// string IDs; the event.type field is ignored.
	body := `{"event":"charge.completed","event.type":"CARD_TRANSACTION","data":{"id":"285959875","tx_ref":"x","status":"successful",` +
		`"meta":{"tanvo_customer_id":"` + testCustomerID.String() + `","tanvo_invoice_id":"` + testInvoiceID.String() + `","payment_type":"one-time","tanvo_organization_id":"` + testOrgID.String() + `"}}}`
	rp := newReplay(t, map[string][]reply{routeVerify: {raw(http.StatusOK, `{"status":"success","data":{"id":285959875,"status":"successful"}}`)}})
	evs, err := rp.gateway().ParseWebhook(context.Background(), connection(`{}`, `{"secret_key":"FLWSECK-live"}`), []byte(body))
	if err != nil || len(evs) != 1 {
		t.Fatalf("ParseWebhook: got = %v, %v, want = one event", evs, err)
	}
	ev := evs[0].(payment.PaymentEvent)
	if ev.Refs != (payment.Refs{CustomerID: testCustomerID, Payable: payment.InvoicePayable(testInvoiceID)}) ||
		!ev.Livemode || ev.Amount != 0 || !ev.OccurredAt.IsZero() {
		t.Errorf("event: got = %+v, want = the customer and invoice, live, no amount", ev)
	}

	for _, tt := range []struct {
		name  string
		meta  map[string]string
		txRef string
		want  payment.Payable
		ok    bool
	}{
		{"invoice by payable id", map[string]string{metaPayableID: testInvoiceID.String(), metaPayableType: "invoice"}, "", payment.InvoicePayable(testInvoiceID), true},
		{"invoice by reference", nil, testInvoiceID.String(), payment.InvoicePayable(testInvoiceID), true},
		{"request by reference", nil, requestTxRefPrefix + testRequestID.String(), payment.RequestPayable(testRequestID), true},
		{"malformed id", nil, "12345", payment.Payable{}, true},
		{"nothing", nil, "", payment.Payable{}, false},
	} {
		r, ok, err := payableRef(tt.meta, tt.txRef)
		if err != nil || ok != tt.ok || r.Payable != tt.want {
			t.Errorf("payableRef(%s): got = %v, %v, %v, want = %v, %v", tt.name, r.Payable, ok, err, tt.want, tt.ok)
		}
	}
}
