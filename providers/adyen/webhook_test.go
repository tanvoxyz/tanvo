package adyen

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// signed returns the webhook fixture with every item signed by
// testHMACKey, after edit (when non-nil) changed the items.
func signed(t *testing.T, name string, edit func(item map[string]any)) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(readFixture(t, "webhooks/"+name)))
	dec.UseNumber()
	var n map[string]any
	if err := dec.Decode(&n); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	key, _ := hex.DecodeString(testHMACKey)
	for _, wrapped := range n["notificationItems"].([]any) {
		it := wrapped.(map[string]any)["NotificationRequestItem"].(map[string]any)
		if edit != nil {
			edit(it)
		}
		s := func(k string) string { v, _ := it[k].(string); return v }
		amt := it["amount"].(map[string]any)
		data := strings.Join([]string{
			s("pspReference"), s("originalReference"), s("merchantAccountCode"), s("merchantReference"),
			amt["value"].(json.Number).String(), amt["currency"].(string), s("eventCode"), s("success"),
		}, ":")
		ad, _ := it["additionalData"].(map[string]any)
		if ad == nil {
			ad = map[string]any{}
			it["additionalData"] = ad
		}
		ad["hmacSignature"] = Sign(data, key)
	}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return b
}

func TestVerifyWebhook(t *testing.T) {
	g := New()
	// source: spec/services/payment_providers/adyen/handle_incoming_webhook_service_spec.rb:34
	if err := g.VerifyWebhook(defaultConn(), signed(t, "webhook_authorisation_response.json", nil), nil); err != nil {
		t.Errorf("valid signature: got = %v, want = nil", err)
	}

	// source: spec/services/payment_providers/adyen/handle_incoming_webhook_service_spec.rb:87
	tampered := bytes.Replace(signed(t, "webhook_authorisation_response.json", nil), []byte(`"ABCDEFGH"`), []byte(`"OTHERPSP"`), 1)
	if err := g.VerifyWebhook(defaultConn(), tampered, nil); !errors.Is(err, payment.ErrInvalidSignature) {
		t.Errorf("tampered: got = %v, want = %v", err, payment.ErrInvalidSignature)
	}
	unsigned := readFixture(t, "webhooks/webhook_authorisation_response.json")
	if err := g.VerifyWebhook(defaultConn(), unsigned, nil); !errors.Is(err, payment.ErrInvalidSignature) {
		t.Errorf("unsigned: got = %v, want = %v", err, payment.ErrInvalidSignature)
	}

	// source: spec/services/payment_providers/adyen/handle_incoming_webhook_service_spec.rb:47
	noKey := defaultConn()
	noKey.Secrets = json.RawMessage(`{"api_key":"` + testAPIKey + `"}`)
	if err := g.VerifyWebhook(noKey, signed(t, "webhook_authorisation_response.json", nil), nil); !errors.Is(err, payment.ErrInvalidSignature) {
		t.Errorf("missing hmac key: got = %v, want = %v", err, payment.ErrInvalidSignature)
	}
	badKey := defaultConn()
	badKey.Secrets = json.RawMessage(`{"api_key":"` + testAPIKey + `","hmac_key":"not-hex"}`)
	if err := g.VerifyWebhook(badKey, signed(t, "webhook_authorisation_response.json", nil), nil); !errors.Is(err, payment.ErrInvalidSignature) {
		t.Errorf("non-hex hmac key: got = %v, want = %v", err, payment.ErrInvalidSignature)
	}

	// Every item is verified, not only the first.
	two := signed(t, "webhook_refund_response.json", nil)
	var n map[string]any
	_ = json.Unmarshal(two, &n)
	items := n["notificationItems"].([]any)
	second, _ := json.Marshal(items[0])
	var other map[string]any
	_ = json.Unmarshal(second, &other)
	other["NotificationRequestItem"].(map[string]any)["pspReference"] = "CHANGED"
	n["notificationItems"] = append(items, other)
	both, _ := json.Marshal(n)
	if err := g.VerifyWebhook(defaultConn(), both, nil); !errors.Is(err, payment.ErrInvalidSignature) {
		t.Errorf("second item tampered: got = %v, want = %v", err, payment.ErrInvalidSignature)
	}

	for _, bad := range []string{`not json`, `{"live":"false","notificationItems":[]}`} {
		if err := g.VerifyWebhook(defaultConn(), []byte(bad), nil); !errors.Is(err, payment.ErrInvalidPayload) {
			t.Errorf("payload %q: got = %v, want = %v", bad, err, payment.ErrInvalidPayload)
		}
	}
}

func TestWebhookAcknowledgement(t *testing.T) {
	// source: app/controllers/webhooks_controller.rb:47 (adyen renders "[accepted]")
	ct, body := New().WebhookAcknowledgement()
	if ct != "application/json" || string(body) != "[accepted]" {
		t.Errorf("acknowledgement: got = %s %q, want = application/json \"[accepted]\"", ct, body)
	}
}

func parse(t *testing.T, payload []byte) ([]payment.Event, error) {
	t.Helper()
	return New().ParseWebhook(context.Background(), defaultConn(), payload)
}

func TestParseWebhook(t *testing.T) {
	const he = "spec/services/payment_providers/adyen/handle_event_service_spec.rb:"
	const pc = "spec/services/payment_provider_customers/adyen_service_spec.rb:"
	customerID := core.MustParseID[core.CustomerID]("a5488a6c-d2ed-44fd-8c97-7fcca4a6a84a")
	payableID := "ec82efeb-88bb-44f8-ba30-0d55b3fd583a"
	invoiceID := core.MustParseID[core.InvoiceID](payableID)
	requestID := core.MustParseID[core.PaymentRequestID](payableID)
	at := func(s string) time.Time { tm, _ := time.Parse(time.RFC3339, s); return tm.UTC() }
	eur := currencyOf("EUR")

	oneTime := payment.PaymentEvent{
		EventMeta:         payment.EventMeta{ProviderEventID: "AUTHORISATION:SGVWRSNQLDQ2WN82:true", Type: EventAuthorisation, OccurredAt: at("2024-01-26T14:06:02+01:00")},
		ProviderPaymentID: "SGVWRSNQLDQ2WN82",
		ProviderStatus:    "succeeded",
		Status:            payment.StateSucceeded,
		Amount:            71,
		Currency:          eur,
		OneTime:           true,
		Refs:              payment.Refs{CustomerID: customerID, InvoiceID: invoiceID, Payable: payment.InvoicePayable(invoiceID)},
	}
	failedOneTime := oneTime
	failedOneTime.ProviderEventID = "AUTHORISATION:SGVWRSNQLDQ2WN82:false"
	failedOneTime.ProviderStatus, failedOneTime.Status, failedOneTime.ErrorMessage = "failed", payment.StateFailed, "Refused"
	requestPayment := oneTime
	requestPayment.Refs = payment.Refs{CustomerID: customerID, Payable: payment.RequestPayable(requestID)}

	cancelled := payment.PaymentEvent{
		EventMeta:         payment.EventMeta{ProviderEventID: "CANCELLATION:MOD_REF_456:true", Type: EventCancellation, OccurredAt: at("2026-05-07T10:15:30+02:00")},
		ProviderPaymentID: "PSPREF123",
		ProviderStatus:    "Cancelled",
		Status:            payment.StateFailed,
	}
	refund := payment.RefundEvent{
		EventMeta:        payment.EventMeta{ProviderEventID: "REFUND:ABCDEFGH:true", Type: EventRefund, OccurredAt: at("2023-05-13T18:02:30+02:00")},
		ProviderRefundID: "ABCDEFGH",
		ProviderStatus:   "succeeded",
		Status:           core.RefundStatusSucceeded,
	}
	failedRefund := refund
	failedRefund.ProviderEventID = "REFUND:ABCDEFGH:false"
	failedRefund.ProviderStatus, failedRefund.Status = "failed", core.RefundStatusFailed
	refundFailed := refund
	refundFailed.EventMeta = payment.EventMeta{ProviderEventID: "REFUND_FAILED:ABCDEFGH:true", Type: EventRefundFailed, OccurredAt: refund.OccurredAt}
	refundFailed.ProviderStatus, refundFailed.Status = "failed", core.RefundStatusFailed

	attached := payment.PaymentMethodAttached{
		EventMeta:          payment.EventMeta{ProviderEventID: "AUTHORISATION:ABCDEFGH:true", Type: EventAuthorisation, OccurredAt: at("2023-05-13T12:17:10+02:00")},
		ProviderCustomerID: "test_1",
		ProviderMethodID:   "pm_adyen_123456",
		Details:            payment.MethodDetails{Type: "card", Brand: "amex", Last4: "0002", ExpMonth: 3, ExpYear: 2030},
		SetAsDefault:       true,
	}
	setupFailed := payment.PaymentMethodSetupFailed{
		EventMeta:          payment.EventMeta{ProviderEventID: "AUTHORISATION:ABCDEFGH:false", Type: EventAuthorisation, OccurredAt: attached.OccurredAt},
		ProviderCustomerID: "test_1",
		ErrorCode:          "AUTHORISATION",
		ErrorMessage:       "Refused",
	}
	lost := payment.DisputeClosed{
		EventMeta:         payment.EventMeta{ProviderEventID: "CHARGEBACK:9915555555555555:true", Type: EventChargeback, Livemode: true, OccurredAt: at("2020-03-13T11:35:42+01:00")},
		ProviderPaymentID: "9915555555555555",
		ProviderStatus:    "Lost",
		Lost:              true,
		Reason:            "Merchandise/Services Not Received",
		ClosedAt:          at("2020-03-13T11:35:42+01:00"),
	}

	set := func(kv ...string) func(map[string]any) {
		return func(it map[string]any) {
			for i := 0; i < len(kv); i += 2 {
				if k, ok := strings.CutPrefix(kv[i], "additionalData."); ok {
					it["additionalData"].(map[string]any)[k] = kv[i+1]
					continue
				}
				it[kv[i]] = kv[i+1]
			}
		}
	}
	tests := []struct {
		sources []string
		fixture string
		edit    func(map[string]any)
		want    []payment.Event
		wantErr error
	}{
		{
			// A zero authorisation of a setup link saves the card.
			sources: []string{he + "32", pc + "213", pc + "220", pc + "226", pc + "248", pc + "252", pc + "263"},
			fixture: "webhook_authorisation_response.json",
			edit:    set("additionalData.recurring.recurringDetailReference", "pm_adyen_123456"),
			want:    []payment.Event{attached},
		},
		{
			sources: []string{pc + "283", pc + "289"},
			fixture: "webhook_authorisation_response.json",
			edit:    set("success", "false", "reason", "Refused"),
			want:    []payment.Event{setupFailed},
		},
		{
			// An authorisation from no Tanvo shopper changes nothing.
			sources: []string{pc + "306"},
			fixture: "webhook_authorisation_response.json",
			edit:    func(it map[string]any) { delete(it["additionalData"].(map[string]any), "shopperReference") },
		},
		{sources: []string{he + "50", he + "56", he + "64"}, fixture: "webhook_authorisation_payment_response.json", want: []payment.Event{oneTime}},
		{
			sources: []string{"spec/services/invoices/payments/adyen_service_spec.rb:106"},
			fixture: "webhook_authorisation_payment_response.json", edit: set("success", "false", "reason", "Refused"),
			want: []payment.Event{failedOneTime},
		},
		{sources: []string{he + "89"}, fixture: "webhook_authorisation_payment_response_payment_request.json", want: []payment.Event{requestPayment}},
		{sources: []string{he + "107"}, fixture: "webhook_authorisation_payment_response_invalid_payable.json", wantErr: payment.ErrUnknownPayable},
		{
			// A charge's own authorisation: CreatePayment returned its state.
			sources: []string{"app/services/payment_providers/adyen/handle_event_service.rb:33-37"},
			fixture: "webhook_authorisation_payment_response.json",
			edit:    func(it map[string]any) { delete(it["additionalData"].(map[string]any), "metadata.payment_type") },
		},
		{sources: []string{he + "134", "spec/services/credit_notes/refunds/adyen_service_spec.rb:246"}, fixture: "webhook_refund_response.json", want: []payment.Event{refund}},
		{sources: []string{"spec/services/credit_notes/refunds/adyen_service_spec.rb:339"}, fixture: "webhook_refund_response.json", edit: set("success", "false"), want: []payment.Event{failedRefund}},
		{sources: []string{"app/services/payment_providers/adyen/handle_event_service.rb:49-53 (REFUND_FAILED)"}, fixture: "webhook_refund_response.json", edit: set("eventCode", "REFUND_FAILED"), want: []payment.Event{refundFailed}},
		{sources: []string{"app/services/payment_providers/adyen/handle_event_service.rb:49-53 (REFUND_FAILED unsuccessful)"}, fixture: "webhook_refund_response.json", edit: set("eventCode", "REFUND_FAILED", "success", "false")},
		{sources: []string{he + "161", he + "204", he + "234"}, fixture: "webhook_cancellation_response.json", want: []payment.Event{cancelled}},
		{sources: []string{he + "186"}, fixture: "webhook_cancellation_response.json", edit: set("success", "false")},
		{sources: []string{"spec/services/payment_providers/adyen/webhooks/chargeback_service_spec.rb:42", "spec/services/payment_providers/adyen/webhooks/chargeback_service_spec.rb:49"}, fixture: "chargeback_lost_event.json", want: []payment.Event{lost}},
		{sources: []string{"spec/services/payment_providers/adyen/webhooks/chargeback_service_spec.rb:86", "spec/services/payment_providers/adyen/webhooks/chargeback_service_spec.rb:93", "spec/services/payment_providers/adyen/webhooks/chargeback_service_spec.rb:71", "spec/services/payment_providers/adyen/webhooks/chargeback_service_spec.rb:78"}, fixture: "chargeback_won_event.json"},
		{sources: []string{he + "266 (offer_closed)"}, fixture: "webhook_offer_closed_response.json"},
		{sources: []string{he + "266 (report_available)"}, fixture: "webhook_report_available_response.json"},
		{sources: []string{he + "266 (recurring_contract)"}, fixture: "webhook_recurring_contract_response.json"},
		{sources: []string{"app/services/payment_providers/adyen/handle_event_service.rb:24-28 (unknown event)"}, fixture: "webhook_refund_response.json", edit: set("eventCode", "SOMETHING_NEW")},
	}
	for _, tt := range tests {
		t.Run(tt.sources[0], func(t *testing.T) {
			payload := signed(t, tt.fixture, tt.edit)
			if err := New().VerifyWebhook(defaultConn(), payload, nil); err != nil {
				t.Fatalf("VerifyWebhook: got = %v, want = nil", err)
			}
			got, err := parse(t, payload)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("%v: error: got = %v, want = %v", tt.sources, err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%v: events:\ngot  = %#v\nwant = %#v", tt.sources, got, tt.want)
			}
		})
	}
}

func TestParseWebhookItems(t *testing.T) {
	// Every item is parsed (lago handles the first only).
	one := signed(t, "webhook_refund_response.json", nil)
	var n map[string]any
	_ = json.Unmarshal(one, &n)
	items := n["notificationItems"].([]any)
	n["notificationItems"] = append(items, items[0])
	both, _ := json.Marshal(n)
	evs, err := parse(t, both)
	if err != nil || len(evs) != 2 {
		t.Errorf("events: got = %d (%v), want = 2", len(evs), err)
	}
	if _, err := parse(t, []byte(`{`)); !errors.Is(err, payment.ErrInvalidPayload) {
		t.Errorf("invalid payload: got = %v, want = %v", err, payment.ErrInvalidPayload)
	}
}
