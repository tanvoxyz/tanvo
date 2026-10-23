package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v84"
	stripewebhook "github.com/stripe/stripe-go/v84/webhook"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// sign returns a Stripe-Signature header for payload signed at t.
func sign(t time.Time, payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(t.Unix(), 10) + "." + string(payload)))
	return "t=" + strconv.FormatInt(t.Unix(), 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhook(t *testing.T) {
	now := time.Date(2025, 6, 5, 10, 0, 0, 0, time.UTC)
	payload := readFixture(t, "webhooks/payment_intent_succeeded.json")
	// stripe-go's reference implementation signs the same way.
	ref := "t=" + strconv.FormatInt(now.Unix(), 10) + ",v1=" + hex.EncodeToString(stripewebhook.ComputeSignature(now, payload, testWebhookSecret))
	if got := sign(now, payload, testWebhookSecret); got != ref {
		t.Fatalf("test signer: got = %q, want = %q", got, ref)
	}
	tampered := slices.Clone(payload)
	tampered[len(tampered)-2] ^= 1
	tests := []struct {
		source  string
		name    string
		payload []byte
		header  string
		secrets string
		wantErr bool
	}{
		{source: "spec/services/payment_providers/stripe/validate_incoming_webhook_service_spec.rb:20", name: "valid", payload: payload, header: ref},
		{
			source: "stripe-ruby Webhook::Signature.verify_header", name: "one of several v1 signatures", payload: payload,
			header: ref + ",v1=" + hex.EncodeToString(make([]byte, 32)) + ",v0=deadbeef",
		},
		{
			source: "stripe-ruby Webhook::Signature.verify_header", name: "within the tolerance", payload: payload,
			header: sign(now.Add(-299*time.Second), payload, testWebhookSecret),
		},
		{
			source: "spec/services/payment_providers/stripe/validate_incoming_webhook_service_spec.rb:46", name: "garbage header",
			payload: payload, header: "signature", wantErr: true,
		},
		{source: "stripe-ruby Webhook::Signature.verify_header", name: "tampered payload", payload: tampered, header: ref, wantErr: true},
		{
			source: "stripe-ruby Webhook::Signature.verify_header", name: "other secret", payload: payload,
			header: sign(now, payload, "whsec_other"), wantErr: true,
		},
		{
			source: "stripe-ruby Webhook::Signature.verify_header", name: "too old", payload: payload,
			header: sign(now.Add(-301*time.Second), payload, testWebhookSecret), wantErr: true,
		},
		{
			source: "stripe-ruby Webhook::Signature.verify_header", name: "no v1 signature", payload: payload,
			header: "t=" + strconv.FormatInt(now.Unix(), 10) + ",v0=abc", wantErr: true,
		},
		{source: "stripe-ruby Webhook::Signature.verify_header", name: "no header", payload: payload, wantErr: true},
		{
			source: "app/services/payment_providers/stripe/validate_incoming_webhook_service.rb", name: "no webhook secret",
			payload: payload, header: ref, secrets: `{"secret_key":"sk_test_123"}`, wantErr: true,
		},
	}
	g := New(WithClock(func() time.Time { return now }))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			conn := connection(`{}`)
			if tt.secrets != "" {
				conn.Secrets = json.RawMessage(tt.secrets)
			}
			h := http.Header{}
			if tt.header != "" {
				h.Set(SignatureHeader, tt.header)
			}
			err := g.VerifyWebhook(conn, tt.payload, h)
			if tt.wantErr {
				if !errors.Is(err, payment.ErrInvalidSignature) {
					t.Errorf("VerifyWebhook: got = %v, want = %v", err, payment.ErrInvalidSignature)
				}
				return
			}
			if err != nil {
				t.Errorf("VerifyWebhook: got = %v, want = nil", err)
			}
		})
	}
}

func TestRegisterWebhook(t *testing.T) {
	const route = "POST /v1/webhook_endpoints"
	const endpoint = "https://billing.example.com/webhooks/stripe/" + "0199a000-0000-7000-8000-000000000001"
	tests := []struct {
		source   string
		name     string
		reply    reply
		wantKind payment.ErrorKind
	}{
		{source: "spec/services/payment_providers/stripe/register_webhook_service_spec.rb:33", name: "registers", reply: fixture("webhook_endpoint_create_response.json")},
		{
			source: "spec/services/payment_providers/stripe/register_webhook_service_spec.rb:48", name: "authentication fails",
			reply:    fail(http.StatusUnauthorized, "invalid_request_error", "", "This API call cannot be made with a publishable API key."),
			wantKind: payment.KindAuthentication,
		},
		{
			source: "spec/services/payment_providers/stripe/register_webhook_service_spec.rb:76", name: "endpoint limit reached",
			reply:    fail(http.StatusBadRequest, "invalid_request_error", "", "You have reached the maximum of 16 test webhook endpoints."),
			wantKind: payment.KindInvalidRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, map[string][]reply{route: {tt.reply}})
			conn := connection(`{"success_redirect_url":"https://acme.example"}`)
			up, err := rp.gateway().RegisterWebhook(context.Background(), conn, endpoint)
			if k := payment.ErrorKindOf(err); k != tt.wantKind {
				t.Fatalf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
			}
			req := rp.only(route)
			want := map[string]string{"url": endpoint, "api_version": stripe.APIVersion}
			for i, e := range WebhookEvents {
				want["enabled_events["+strconv.Itoa(i)+"]"] = e
			}
			checkForm(t, req.Form, want)
			if tt.wantKind != "" {
				return
			}
			var st map[string]string
			var sc map[string]string
			if err := json.Unmarshal(up.Settings, &st); err != nil {
				t.Fatalf("settings: %v", err)
			}
			if err := json.Unmarshal(up.Secrets, &sc); err != nil {
				t.Fatalf("secrets: %v", err)
			}
			if st["webhook_endpoint_id"] != "we_1RWGXDQ8iJWBZFaMFV5KoJxR" || st["success_redirect_url"] != "https://acme.example" {
				t.Errorf("settings: got = %v, want = the endpoint ID added to the existing settings", st)
			}
			if sc["webhook_secret"] != "whsec_SB**************************RyvM" || sc["secret_key"] != "sk_test_123" {
				t.Errorf("secrets: got = %v, want = the signing secret added to the existing secrets", sc)
			}
		})
	}
}

func TestWebhookEvents(t *testing.T) {
	// source: app/models/payment_providers/stripe_provider.rb WEBHOOKS_EVENTS, plus the two Tanvo additions
	lago := []string{
		"setup_intent.succeeded", "payment_intent.payment_failed", "payment_intent.succeeded", "payment_intent.canceled",
		"payment_method.detached", "charge.refund.updated", "customer.updated", "charge.dispute.closed",
		"customer_cash_balance_transaction.created",
	}
	if !slices.Equal(WebhookEvents[:len(lago)], lago) {
		t.Errorf("events: got = %v, want = prefix %v", WebhookEvents, lago)
	}
}

func TestRefreshWebhook(t *testing.T) {
	const route = "POST /v1/webhook_endpoints/we_123"
	const endpoint = "https://billing.example.com/webhooks/stripe/x"
	tests := []struct {
		source   string
		name     string
		settings string
		reply    reply
		wantKind payment.ErrorKind
		wantErr  error
	}{
		{
			source: "spec/services/payment_providers/stripe/refresh_webhook_service_spec.rb:32", name: "updates the endpoint",
			settings: `{"webhook_endpoint_id":"we_123"}`, reply: fixture("webhook_endpoint_update_response.json"),
		},
		{
			source: "spec/services/payment_providers/stripe/refresh_webhook_service_spec.rb:47", name: "authentication fails",
			settings: `{"webhook_endpoint_id":"we_123"}`, reply: fail(http.StatusUnauthorized, "invalid_request_error", "", "Invalid API Key provided"),
			wantKind: payment.KindAuthentication,
		},
		{
			source: "spec/services/payment_providers/stripe/refresh_webhook_service_spec.rb:75", name: "endpoint limit reached",
			settings: `{"webhook_endpoint_id":"we_123"}`, reply: fail(http.StatusBadRequest, "invalid_request_error", "", "You have reached the maximum of 16 test webhook endpoints."),
			wantKind: payment.KindInvalidRequest,
		},
		{
			source: "app/services/payment_providers/stripe/refresh_webhook_service.rb", name: "no endpoint registered",
			settings: `{}`, wantErr: core.ErrNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, map[string][]reply{route: {tt.reply}})
			err := rp.gateway().RefreshWebhook(context.Background(), connection(tt.settings), endpoint)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
				}
				if rp.count() != 0 {
					t.Errorf("requests: got = %d, want = 0", rp.count())
				}
				return
			}
			if k := payment.ErrorKindOf(err); k != tt.wantKind {
				t.Fatalf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
			}
			checkForm(t, rp.only(route).Form, map[string]string{"url": endpoint, "enabled_events[0]": WebhookEvents[0], "api_version": ""})
		})
	}
}

// editEvent returns the fixture with its data.object edited.
func editEvent(t *testing.T, name string, edit func(obj map[string]any)) []byte {
	t.Helper()
	var ev map[string]any
	if err := json.Unmarshal(readFixture(t, name), &ev); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	edit(ev["data"].(map[string]any)["object"].(map[string]any))
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return b
}

func TestParseWebhook(t *testing.T) {
	invoice := core.MustParseID[core.InvoiceID]("0d0b6c24-c5bd-44db-98ae-e8beeb4560bb")
	tests := []struct {
		source  string
		name    string
		payload func(t *testing.T) []byte
		want    []payment.Event
		wantErr error
	}{
		{
			source:  "spec/services/payment_providers/stripe/webhooks/payment_intent_succeeded_service_spec.rb:22",
			name:    "payment_intent.succeeded",
			payload: func(t *testing.T) []byte { return readFixture(t, "webhooks/payment_intent_succeeded.json") },
			want: []payment.Event{payment.PaymentEvent{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_3RTkpYQ8iJWBZFaM1G1JtOIT", Type: EventPaymentIntentSucceeded, OccurredAt: time.Unix(1748441984, 0).UTC()},
				ProviderPaymentID:  "pi_3RTkpYQ8iJWBZFaM1LBHNo0J",
				ProviderStatus:     "succeeded",
				Status:             payment.StateSucceeded,
				Amount:             4620,
				Currency:           eur,
				ProviderCustomerID: "cus_SOXv8CncjHqSEv",
				ProviderMethodID:   "pm_1RTkpWQ8iJWBZFaMdDm6wKzg",
				Refs: payment.Refs{
					CustomerID: core.MustParseID[core.CustomerID]("1342ce9f-c8cb-4b16-b9d4-7add8ec0cf5d"),
					PaymentID:  core.MustParseID[core.PaymentID]("de7602d6-fbd7-40e4-891e-c9937d2d4aa9"),
					InvoiceID:  invoice,
					Payable:    payment.InvoicePayable(invoice),
				},
			}},
		},
		{
			source: "spec/services/payment_providers/stripe/webhooks/payment_intent_succeeded_service_spec.rb:115",
			name:   "one-time payment request payment",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/payment_intent_succeeded.json", func(o map[string]any) {
					o["metadata"] = map[string]any{
						"tanvo_payable_type": "payment_request", "tanvo_payable_id": testRequestID.String(),
						"tanvo_customer_id": "not-a-uuid", "payment_type": "one-time",
					}
				})
			},
			want: []payment.Event{payment.PaymentEvent{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_3RTkpYQ8iJWBZFaM1G1JtOIT", Type: EventPaymentIntentSucceeded, OccurredAt: time.Unix(1748441984, 0).UTC()},
				ProviderPaymentID:  "pi_3RTkpYQ8iJWBZFaM1LBHNo0J",
				ProviderStatus:     "succeeded",
				Status:             payment.StateSucceeded,
				Amount:             4620,
				Currency:           eur,
				ProviderCustomerID: "cus_SOXv8CncjHqSEv",
				ProviderMethodID:   "pm_1RTkpWQ8iJWBZFaMdDm6wKzg",
				OneTime:            true,
				Refs:               payment.Refs{Payable: payment.RequestPayable(testRequestID)},
			}},
		},
		{
			source:  "spec/services/payment_providers/stripe/webhooks/payment_intent_payment_failed_service_spec.rb:15",
			name:    "payment_intent.payment_failed",
			payload: func(t *testing.T) []byte { return readFixture(t, "webhooks/payment_intent_payment_failed.json") },
			want:    nil, // checked below
		},
		{
			source:  "spec/services/payment_providers/stripe/webhooks/payment_intent_payment_failed_service_spec.rb:37",
			name:    "payment_intent.canceled",
			payload: func(t *testing.T) []byte { return readFixture(t, "webhooks/payment_intent_canceled.json") },
		},
		{
			source:  "spec/services/payment_providers/stripe/handle_event_service_spec.rb:30",
			name:    "setup_intent.succeeded without a customer",
			payload: func(t *testing.T) []byte { return readFixture(t, "webhooks/setup_intent_succeeded.json") },
		},
		{
			source: "spec/services/payment_providers/stripe/webhooks/setup_intent_succeeded_service_spec.rb:73",
			name:   "setup_intent.succeeded",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/setup_intent_succeeded.json", func(o map[string]any) { o["customer"] = "cus_123" })
			},
			want: []payment.Event{payment.PaymentMethodAttached{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_1RWYtwQ8iJWBZFaMBOWfjn6y", Type: EventSetupIntentSucceeded, OccurredAt: time.Unix(1749111112, 0).UTC()},
				ProviderCustomerID: "cus_123",
				ProviderMethodID:   "pm_1RWYtwQ8iJWBZFaMCbtQuuSy",
				SetAsDefault:       true,
			}},
		},
		{
			source:  "spec/services/payment_providers/stripe/handle_event_service_spec.rb:66",
			name:    "payment_method.detached",
			payload: func(t *testing.T) []byte { return readFixture(t, "webhooks/payment_method_detached.json") },
			want: []payment.Event{payment.PaymentMethodDetached{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_1RWGX6Q8iJWBZFaMeBzMFOpP", Type: EventPaymentMethodDetached, OccurredAt: time.Unix(1749040504, 0).UTC()},
				ProviderCustomerID: "cus_SR8o2vcGRkVb6v",
				ProviderMethodID:   "pm_1RWGX3Q8iJWBZFaM6l9fORzc",
			}},
		},
		{
			source: "app/services/payment_providers/stripe/handle_event_service.rb (Tanvo subscribes to payment_method.attached)",
			name:   "payment_method.attached",
			payload: func(t *testing.T) []byte {
				b := editEvent(t, "webhooks/payment_method_detached.json", func(o map[string]any) {
					o["customer"] = "cus_SR8o2vcGRkVb6v"
					o["card"] = map[string]any{"display_brand": "visa", "last4": "4242", "exp_month": 12, "exp_year": 2030}
				})
				var ev map[string]any
				_ = json.Unmarshal(b, &ev)
				ev["type"] = EventPaymentMethodAttached
				b, _ = json.Marshal(ev)
				return b
			},
			want: []payment.Event{payment.PaymentMethodAttached{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_1RWGX6Q8iJWBZFaMeBzMFOpP", Type: EventPaymentMethodAttached, OccurredAt: time.Unix(1749040504, 0).UTC()},
				ProviderCustomerID: "cus_SR8o2vcGRkVb6v",
				ProviderMethodID:   "pm_1RWGX3Q8iJWBZFaM6l9fORzc",
				Details:            payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2030},
			}},
		},
		{
			source:  "spec/services/payment_providers/stripe/webhooks/customer_updated_service_spec.rb:26",
			name:    "customer.updated without a default",
			payload: func(t *testing.T) []byte { return readFixture(t, "webhooks/customer_updated.json") },
			want: []payment.Event{payment.CustomerUpdated{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_1RWGXCQ8iJWBZFaMVlV0UVdh", Type: EventCustomerUpdated, OccurredAt: time.Unix(1749040510, 0).UTC()},
				ProviderCustomerID: "cus_SR8o2vcGRkVb6v",
				Refs:               payment.Refs{CustomerID: core.MustParseID[core.CustomerID]("2c0f0f00-c378-4915-8ea4-c26304e1968a")},
			}},
		},
		{
			source: "spec/services/payment_providers/stripe/webhooks/customer_updated_service_spec.rb:38",
			name:   "customer.updated with a default method",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/customer_updated.json", func(o map[string]any) {
					o["invoice_settings"].(map[string]any)["default_payment_method"] = "pm_default"
					o["default_source"] = "card_source"
				})
			},
			want: []payment.Event{payment.CustomerUpdated{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_1RWGXCQ8iJWBZFaMVlV0UVdh", Type: EventCustomerUpdated, OccurredAt: time.Unix(1749040510, 0).UTC()},
				ProviderCustomerID: "cus_SR8o2vcGRkVb6v",
				DefaultMethodID:    "pm_default",
				Refs:               payment.Refs{CustomerID: core.MustParseID[core.CustomerID]("2c0f0f00-c378-4915-8ea4-c26304e1968a")},
			}},
		},
		{
			source: "app/services/payment_providers/stripe/webhooks/customer_updated_service.rb (default_source fallback)",
			name:   "customer.updated with a default source",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/customer_updated.json", func(o map[string]any) {
					o["default_source"] = map[string]any{"id": "card_source", "object": "card"}
					o["metadata"] = map[string]any{}
				})
			},
			want: []payment.Event{payment.CustomerUpdated{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_1RWGXCQ8iJWBZFaMVlV0UVdh", Type: EventCustomerUpdated, OccurredAt: time.Unix(1749040510, 0).UTC()},
				ProviderCustomerID: "cus_SR8o2vcGRkVb6v",
				DefaultMethodID:    "card_source",
			}},
		},
		{
			source:  "spec/services/payment_providers/stripe/handle_event_service_spec.rb:89",
			name:    "charge.refund.updated",
			payload: func(t *testing.T) []byte { return readFixture(t, "webhooks/charge_refund_updated.json") },
			want: []payment.Event{payment.RefundEvent{
				EventMeta:        payment.EventMeta{ProviderEventID: "evt_3RWYCFQ8iJWBZFaM1z10b8SX", Type: EventChargeRefundUpdated, OccurredAt: time.Unix(1749108405, 0).UTC()},
				ProviderRefundID: "re_3RWYCFQ8iJWBZFaM191ZqGD9",
				ProviderStatus:   "succeeded",
				Status:           core.RefundStatusSucceeded,
			}},
		},
		{
			source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:330",
			name:   "charge.refund.updated with Tanvo metadata",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/charge_refund_updated.json", func(o map[string]any) {
					o["status"] = "failed"
					o["metadata"] = map[string]any{"tanvo_credit_note_id": testCreditNoteID.String(), "tanvo_invoice_id": testInvoiceID.String()}
				})
			},
			want: []payment.Event{payment.RefundEvent{
				EventMeta:        payment.EventMeta{ProviderEventID: "evt_3RWYCFQ8iJWBZFaM1z10b8SX", Type: EventChargeRefundUpdated, OccurredAt: time.Unix(1749108405, 0).UTC()},
				ProviderRefundID: "re_3RWYCFQ8iJWBZFaM191ZqGD9",
				ProviderStatus:   "failed",
				Status:           core.RefundStatusFailed,
				Refs:             payment.Refs{CreditNoteID: testCreditNoteID, InvoiceID: testInvoiceID, Payable: payment.InvoicePayable(testInvoiceID)},
			}},
		},
		{
			source:  "spec/services/payment_providers/stripe/webhooks/charge_dispute_closed_service_spec.rb:101",
			name:    "charge.dispute.closed won",
			payload: func(t *testing.T) []byte { return readFixture(t, "webhooks/charge_dispute_closed.json") },
			want: []payment.Event{payment.DisputeClosed{
				EventMeta:         payment.EventMeta{ProviderEventID: "evt_3RWXidQ8iJWBZFaM0N9UpCe2", Type: EventChargeDisputeClosed, OccurredAt: time.Unix(1749106569, 0).UTC()},
				ProviderPaymentID: "pi_3RWXidQ8iJWBZFaM0hwfe7LL",
				ProviderStatus:    "warning_closed",
				Reason:            "fraudulent",
				ClosedAt:          time.Unix(1749106569, 0).UTC(),
			}},
		},
		{
			source: "spec/services/payment_providers/stripe/webhooks/charge_dispute_closed_service_spec.rb:53",
			name:   "charge.dispute.closed lost",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/charge_dispute_closed.json", func(o map[string]any) { o["status"] = "lost" })
			},
			want: []payment.Event{payment.DisputeClosed{
				EventMeta:         payment.EventMeta{ProviderEventID: "evt_3RWXidQ8iJWBZFaM0N9UpCe2", Type: EventChargeDisputeClosed, OccurredAt: time.Unix(1749106569, 0).UTC()},
				ProviderPaymentID: "pi_3RWXidQ8iJWBZFaM0hwfe7LL",
				ProviderStatus:    "lost",
				Lost:              true,
				Reason:            "fraudulent",
				ClosedAt:          time.Unix(1749106569, 0).UTC(),
			}},
		},
		{
			source: "spec/services/payment_providers/stripe/webhooks/customer_cash_balance_transaction_created_service_spec.rb:48",
			name:   "customer_cash_balance_transaction.created funded",
			payload: func(t *testing.T) []byte {
				return readFixture(t, "webhooks/customer_cash_balance_transaction_created.json")
			},
			want: []payment.Event{payment.CustomerBalanceFunded{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_1RWGXCQ8iJWBZFaMVlV0UVdh", Type: EventCustomerCashBalanceTxCreated, OccurredAt: time.Unix(1749040510, 0).UTC()},
				ProviderCustomerID: "cus_SR8o2vcGRkVb6v",
				EndingBalance:      10000,
				Currency:           eur,
			}},
		},
		{
			source: "spec/services/payment_providers/stripe/webhooks/customer_cash_balance_transaction_created_service_spec.rb:153",
			name:   "a cash balance transaction that is not a funding",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/customer_cash_balance_transaction_created.json", func(o map[string]any) { o["type"] = "applied_to_payment" })
			},
		},
		{
			source: "spec/services/payment_providers/stripe/webhooks/customer_cash_balance_transaction_created_service_spec.rb:168",
			name:   "funds already allocated",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/customer_cash_balance_transaction_created.json", func(o map[string]any) { o["ending_balance"] = 0 })
			},
		},
		{
			source: "spec/services/payment_providers/stripe/handle_event_service_spec.rb:110",
			name:   "an unhandled event type",
			payload: func(t *testing.T) []byte {
				return []byte(`{"id":"evt_1","type":"invoice.created","created":1,"data":{"object":{"id":"in_1"}}}`)
			},
		},
		{
			source:  "spec/services/payment_providers/stripe/handle_incoming_webhook_service_spec.rb:21",
			name:    "an unreadable payload",
			payload: func(t *testing.T) []byte { return []byte(`webhook_payload`) },
			wantErr: payment.ErrInvalidPayload,
		},
		{
			source: "spec/services/payment_providers/stripe/webhooks/payment_intent_payment_failed_service_spec.rb:89",
			name:   "payment_failed of an unknown payable type",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/payment_intent_payment_failed.json", func(o map[string]any) {
					o["metadata"] = map[string]any{"tanvo_payable_type": "InvalidPayableTypeName"}
				})
			},
			wantErr: payment.ErrUnknownPayable,
		},
		{
			source: "spec/services/payment_providers/stripe/webhooks/payment_intent_succeeded_service_spec.rb:187",
			name:   "succeeded of an unknown payable type",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/payment_intent_succeeded.json", func(o map[string]any) {
					o["metadata"] = map[string]any{"tanvo_payable_type": "InvalidPayableTypeName"}
				})
			},
			wantErr: payment.ErrUnknownPayable,
		},
		{
			source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:360",
			name:   "charge.refund.updated with an unknown status",
			payload: func(t *testing.T) []byte {
				return editEvent(t, "webhooks/charge_refund_updated.json", func(o map[string]any) { o["status"] = "invalid" })
			},
			wantErr: payment.ErrUnknownStatus,
		},
	}
	g := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			got, err := g.ParseWebhook(context.Background(), connection(`{}`), tt.payload(t))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
				}
				if got != nil {
					t.Errorf("events: got = %#v, want = none", got)
				}
				if errors.Is(err, payment.ErrRetryable) || errors.Is(err, payment.ErrUnknownPayable) != errors.Is(err, payment.ErrRejected) {
					t.Errorf("class: got = %v, want = not retryable, rejected only for an unknown payable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseWebhook: got = %v, want = nil", err)
			}
			switch tt.name {
			case "payment_intent.payment_failed", "payment_intent.canceled":
				checkFailedPayment(t, got)
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("events: got = %#v, want = %#v", got, tt.want)
			}
			for i := range got {
				if !eventEqual(got[i], tt.want[i]) {
					t.Errorf("event %d: got = %#v, want = %#v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// checkFailedPayment checks a payment_failed or canceled event: lago
// reports both failed whatever the intent's status
// (webhooks/payment_intent_payment_failed_service_spec.rb:15,37).
func checkFailedPayment(t *testing.T, got []payment.Event) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("events: got = %d, want = 1", len(got))
	}
	e, ok := got[0].(payment.PaymentEvent)
	if !ok {
		t.Fatalf("event: got = %T, want = payment.PaymentEvent", got[0])
	}
	if e.Status != payment.StateFailed || e.ProviderPaymentID == "" || e.Refs.PaymentID.IsZero() || e.Refs.Payable.Type != payment.PayableTypeInvoice {
		t.Errorf("event: got = %+v, want = failed with its payment and invoice refs", e)
	}
	if e.Type == EventPaymentIntentPaymentFailed && e.ErrorCode != "authentication_required" {
		t.Errorf("error code: got = %q, want = authentication_required", e.ErrorCode)
	}
}

func eventEqual(a, b payment.Event) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return a.Meta() == b.Meta() && string(ja) == string(jb)
}

func TestParseRequiresAction(t *testing.T) {
	// source: spec/services/payment_providers/stripe/payments/create_service_spec.rb:629 (payment.requires_action)
	b := readFixture(t, "payment_intent_requires_action_response.json")
	payload := []byte(`{"id":"evt_ra","type":"payment_intent.requires_action","created":1763738710,"livemode":false,"data":{"object":` + string(b) + `}}`)
	got, err := New().ParseWebhook(context.Background(), connection(`{}`), payload)
	if err != nil || len(got) != 1 {
		t.Fatalf("ParseWebhook: got = %v, %v, want = one event", got, err)
	}
	e := got[0].(payment.PaymentEvent)
	if e.Status != payment.StateProcessing || !e.RequiresAction || e.NextActionURL == "" || len(e.NextAction) == 0 {
		t.Errorf("event: got = %+v, want = processing with its next action", e)
	}
}

func TestParseFailedWithoutCode(t *testing.T) {
	// source: spec/services/payment_providers/stripe/webhooks/payment_intent_payment_failed_service_spec.rb:102
	payload := editEvent(t, "webhooks/payment_intent_payment_failed.json", func(o map[string]any) {
		o["last_payment_error"] = map[string]any{"message": "error"}
	})
	got, err := New().ParseWebhook(context.Background(), connection(`{}`), payload)
	if err != nil || len(got) != 1 {
		t.Fatalf("ParseWebhook: got = %v, %v, want = one event", got, err)
	}
	e, ok := got[0].(payment.PaymentEvent)
	if !ok || e.Status != payment.StateFailed || e.ErrorCode != "" || e.ProviderPaymentID == "" {
		t.Errorf("event: got = %+v, want = failed without an error code", got[0])
	}
}
