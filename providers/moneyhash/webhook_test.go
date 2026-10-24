package moneyhash

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func TestVerifyWebhook(t *testing.T) {
	// The spec's vector: the body "webhook_payload" as JSON, signed at
	// 1743090080 with test_signature_key.
	body := []byte(`"webhook_payload"`)
	header := func(v string) http.Header {
		h := http.Header{}
		h.Set(SignatureHeader, v)
		return h
	}
	g := New()
	// source: spec/services/payment_providers/moneyhash/validate_incoming_webhook_service_spec.rb:14
	valid := header("t=1743090080,v1=placeholder,v2=placeholder,v3=ca13480c8142f2f2b44822c764909027974e84b3e8c94457a314f129d8d60148")
	if err := g.VerifyWebhook(defaultConn(), body, valid); err != nil {
		t.Errorf("valid signature: got = %v, want = nil", err)
	}
	if got := SignatureValue(body, "1743090080", testSignatureKey); !strings.HasSuffix(valid.Get(SignatureHeader), strings.TrimPrefix(got, "t=1743090080,")) {
		t.Errorf("SignatureValue: got = %q, want = the spec's v3", got)
	}
	// source: spec/services/payment_providers/moneyhash/validate_incoming_webhook_service_spec.rb:19
	noKey := connection(`{"flow_id":"`+testFlowID+`"}`, `{"api_key":"`+testAPIKey+`"}`)
	for name, tt := range map[string]struct {
		conn   payment.Connection
		body   []byte
		header http.Header
	}{
		"invalid signature": {defaultConn(), body, header("Moneyhash-Signature: t=1743090080,v1=placeholder,v2=placeholder,v3=invalid_signature")},
		"other timestamp":   {defaultConn(), body, header("t=1743090081,v3=ca13480c8142f2f2b44822c764909027974e84b3e8c94457a314f129d8d60148")},
		"tampered body":     {defaultConn(), append(bytes.Clone(body), ' '), valid},
		"missing header":    {defaultConn(), body, http.Header{}},
		"no signature key":  {noKey, body, valid},
	} {
		if err := g.VerifyWebhook(tt.conn, tt.body, tt.header); !errors.Is(err, payment.ErrInvalidSignature) {
			t.Errorf("%s: got = %v, want = %v", name, err, payment.ErrInvalidSignature)
		}
	}
}

func parseFixture(t *testing.T, name string) []payment.Event {
	t.Helper()
	evs, err := New().ParseWebhook(context.Background(), defaultConn(), readFixture(t, filepath.Join("webhooks", name)))
	if err != nil {
		t.Fatalf("ParseWebhook(%s): %v", name, err)
	}
	return evs
}

func invoiceRefs(customer, invoice string) payment.Refs {
	return payment.Refs{
		CustomerID: core.MustParseID[core.CustomerID](customer),
		Payable:    payment.InvoicePayable(core.MustParseID[core.InvoiceID](invoice)),
	}
}

func TestParseWebhookPayments(t *testing.T) {
	for _, tt := range []struct {
		fixture string
		want    payment.PaymentEvent
	}{
		{
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:27
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:123
			// source: spec/services/payment_providers/moneyhash/handle_incoming_webhook_service_spec.rb:15
			"intent.processed.json",
			payment.PaymentEvent{
				EventMeta:         payment.EventMeta{ProviderEventID: "g6Q6QJL.intent.processed", Type: EventIntentProcessed},
				ProviderPaymentID: "g6Q6QJL", ProviderStatus: "SUCCESSFUL", Status: payment.StateSucceeded,
				Amount: 500, Currency: usd,
				Refs: invoiceRefs("46275968-4805-44e2-a181-e587b4c13b19", "727bb6ca-9a31-458e-ac05-98bf48967387"),
			},
		},
		{
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:40
			"intent.time_expired.json",
			payment.PaymentEvent{
				EventMeta:         payment.EventMeta{ProviderEventID: "9J0x2Dg.intent.time_expired", Type: EventIntentTimeExpired},
				ProviderPaymentID: "9J0x2Dg", ProviderStatus: "FAILED", Status: payment.StateFailed,
				Amount: 677, Currency: usd,
				Refs: invoiceRefs("e6676e85-9e4f-4fb5-bd2f-590a9c170d0e", "b42828c5-ae57-4716-8281-3317fed01042"),
			},
		},
		{
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:73
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:135
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:143
			"transaction.purchase.successful.json",
			payment.PaymentEvent{
				EventMeta:         payment.EventMeta{ProviderEventID: "9z2l4n9", Type: EventPurchaseSuccessful},
				ProviderPaymentID: "LY0a5W9", ProviderStatus: "SUCCESSFUL", Status: payment.StateSucceeded,
				Amount: 710, Currency: usd,
				Refs: invoiceRefs("53c14b65-3406-4b1b-9d49-77fb389ee92e", "23fd1f3c-922c-4928-b40b-c8a31e4d6664"),
			},
		},
		{
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:87
			"transaction.purchase.pending_authentication.json",
			payment.PaymentEvent{
				EventMeta:         payment.EventMeta{ProviderEventID: "1LkmkZr", Type: EventPurchasePendingAuthen},
				ProviderPaymentID: "wgjowZJ", ProviderStatus: "PENDING", Status: payment.StateProcessing,
				Amount: 5000, Currency: usd,
				Refs: invoiceRefs("53c14b65-3406-4b1b-9d49-77fb389ee92e", "23fd1f3c-922c-4928-b40b-c8a31e4d6664"),
			},
		},
		{
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:101
			"transaction.purchase.failed.json",
			payment.PaymentEvent{
				EventMeta:         payment.EventMeta{ProviderEventID: "Zv2kmzZ", Type: EventPurchaseFailed},
				ProviderPaymentID: "LVR0VXL", ProviderStatus: "FAILED", Status: payment.StateFailed,
				Amount: 500, Currency: usd,
				Refs: invoiceRefs("53c14b65-3406-4b1b-9d49-77fb389ee92e", "2b618048-af9e-48f5-b188-f38642eef389"),
			},
		},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			evs := parseFixture(t, tt.fixture)
			if len(evs) != 1 {
				t.Fatalf("events: got = %d, want = 1", len(evs))
			}
			if got := evs[0]; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("event:\ngot  = %+v\nwant = %+v", got, tt.want)
			}
		})
	}
}

func TestParseWebhookPaymentRequest(t *testing.T) {
	// An intent event's payable type is read from its own custom fields.
	body := `{"type":"intent.processed","data":{"intent_id":"i1","intent":{"amount":"7.99","amount_currency":"usd","custom_fields":{` +
		`"tanvo_payable_type":"payment_request","tanvo_payable_id":"` + testRequestID.String() + `","tanvo_customer_id":"` + testCustomerID.String() + `",` +
		`"tanvo_customer_provider_customer_id":"` + testCustomer + `","tanvo_mit":true}}}}`
	conn := connection(`{"flow_id":"`+testFlowID+`"}`, `{"api_key":"`+testAPIKey+`"}`)
	evs, err := New().ParseWebhook(context.Background(), conn, []byte(body))
	if err != nil || len(evs) != 1 {
		t.Fatalf("ParseWebhook: got = %v, %v, want = one event", evs, err)
	}
	ev := evs[0].(payment.PaymentEvent)
	want := payment.Refs{CustomerID: testCustomerID, Payable: payment.RequestPayable(testRequestID)}
	if ev.Refs != want || ev.Amount != 799 || ev.Currency != usd || ev.ProviderCustomerID != testCustomer || !ev.Livemode {
		t.Errorf("event: got = %+v, want = refs %+v, 799 USD, customer %s, live", ev, want, testCustomer)
	}
}

func TestParseWebhookCards(t *testing.T) {
	details := payment.MethodDetails{Type: "card", Brand: "Visa", Last4: "0000", ExpMonth: 2, ExpYear: 2026}
	customer := core.MustParseID[core.CustomerID]("53c14b65-3406-4b1b-9d49-77fb389ee92e")
	for _, tt := range []struct {
		fixture string
		want    payment.Event
	}{
		{
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:163
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:171
			// source: spec/services/payment_provider_customers/moneyhash_service_spec.rb:77
			// source: spec/services/payment_provider_customers/moneyhash_service_spec.rb:189
			"card_token.created.json",
			payment.PaymentMethodAttached{
				EventMeta:        payment.EventMeta{Type: EventCardTokenCreated},
				ProviderMethodID: "419cb5f7-20fc-4571-a64d-4a64f0a0412f", Details: details, SetAsDefault: true,
				Refs: payment.Refs{CustomerID: customer},
			},
		},
		{
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:189
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:197
			// source: spec/services/payment_provider_customers/moneyhash_service_spec.rb:98
			"card_token.updated.json",
			payment.PaymentMethodAttached{
				EventMeta:        payment.EventMeta{Type: EventCardTokenUpdated},
				ProviderMethodID: "419cb5f7-20fc-4571-a64d-4a64f0a0412f", Details: details, SetAsDefault: true,
				Refs: payment.Refs{CustomerID: customer},
			},
		},
		{
			// source: spec/services/payment_providers/moneyhash/handle_event_service_spec.rb:212
			// source: spec/services/payment_provider_customers/moneyhash_service_spec.rb:87
			"card_token.deleted.json",
			payment.PaymentMethodDetached{
				EventMeta:        payment.EventMeta{Type: EventCardTokenDeleted},
				ProviderMethodID: "bf05e221-1305-44cb-ac04-f227f0a8b67a",
				Refs:             payment.Refs{CustomerID: core.MustParseID[core.CustomerID]("2f2f4867-1ac9-4529-b03b-4f7133897b82")},
			},
		},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			evs := parseFixture(t, tt.fixture)
			if len(evs) != 1 || !reflect.DeepEqual(evs[0], tt.want) {
				t.Errorf("events:\ngot  = %+v\nwant = [%+v]", evs, tt.want)
			}
		})
	}
}

func TestParseWebhookInvalid(t *testing.T) {
	g := New()
	ctx := context.Background()
	for _, body := range []string{
		"", "invalid", `{}`,
		`{"type":"intent.processed","data":{}}`,
		`{"type":"transaction.purchase.failed"}`,
		`{"type":"card_token.created","data":{}}`,
		`{"type":"transaction.purchase.failed","intent":{"id":"i1","amount":{"value":1,"currency":"XXQ"}}}`,
		`{"type":"intent.processed","data":{"intent_id":"i1","intent":{"amount":"abc","amount_currency":"USD"}}}`,
	} {
		if _, err := g.ParseWebhook(ctx, defaultConn(), []byte(body)); !errors.Is(err, payment.ErrInvalidPayload) {
			t.Errorf("ParseWebhook(%q): got = %v, want = %v", body, err, payment.ErrInvalidPayload)
		}
	}
	// lago raises NameError on an unknown payable type.
	unknown := `{"type":"transaction.purchase.successful","status_id":"s","intent":{"id":"i1","custom_fields":{"tanvo_payable_type":"Subscription"}}}`
	if _, err := g.ParseWebhook(ctx, defaultConn(), []byte(unknown)); !errors.Is(err, payment.ErrUnknownPayable) {
		t.Errorf("unknown payable: got = %v, want = %v", err, payment.ErrUnknownPayable)
	}
	// Other event types yield no event.
	if evs, err := g.ParseWebhook(ctx, defaultConn(), []byte(`{"type":"intent.refunded","data":{}}`)); err != nil || len(evs) != 0 {
		t.Errorf("other event: got = %v, %v, want = none", evs, err)
	}
	// An absent amount is zero.
	evs, err := g.ParseWebhook(ctx, defaultConn(), []byte(`{"type":"transaction.purchase.failed","status_id":"s","intent":{"id":"i1"}}`))
	if err != nil || len(evs) != 1 || evs[0].(payment.PaymentEvent).Amount != 0 {
		t.Errorf("no amount: got = %v, %v, want = one event of 0", evs, err)
	}
}
