package stripe

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func invoiceURLParams() payment.PaymentURLParams {
	return payment.PaymentURLParams{
		IntentID:           testIntentID,
		Payable:            payment.InvoicePayable(testInvoiceID),
		InvoiceType:        "subscription",
		IssuingDate:        core.NewDate(2025, 5, 28),
		CustomerID:         testCustomerID,
		ProviderCustomerID: "cus_123",
		MethodTypes:        []string{MethodCard},
		CustomerCountry:    "FR",
		Description:        "Acme - Invoice ACM-001",
		Lines:              []payment.PaymentURLLine{{Name: "ACM-001", Amount: 1999}},
		Currency:           eur,
		ExpiresAt:          time.Date(2025, 6, 4, 12, 0, 0, 0, time.UTC),
	}
}

func requestURLParams() payment.PaymentURLParams {
	return payment.PaymentURLParams{
		Payable:            payment.RequestPayable(testRequestID),
		CustomerID:         testCustomerID,
		ProviderCustomerID: "cus_123",
		MethodTypes:        []string{MethodCard, MethodSEPADebit},
		Description:        "Acme - Overdue invoices",
		Lines:              []payment.PaymentURLLine{{Name: "ACM-001", Amount: 1000}, {Name: "ACM-002", Amount: 2500}},
		Currency:           usd,
	}
}

func TestCreatePaymentURL(t *testing.T) {
	session := ok(`{"id":"cs_123","object":"checkout.session","url":"https://example.com"}`)
	tests := []struct {
		source   string
		name     string
		settings string
		params   func() payment.PaymentURLParams
		edit     func(p *payment.PaymentURLParams)
		reply    reply
		form     map[string]string
		key      string
		wantKind payment.ErrorKind
	}{
		{
			source: "spec/services/invoices/payments/stripe_service_spec.rb:41,47,110,116",
			name:   "invoice payload",
			params: invoiceURLParams,
			reply:  session,
			key:    "payment-intent:" + testIntentID.String(),
			form: map[string]string{
				"line_items[0][quantity]":                       "1",
				"line_items[0][price_data][currency]":           "eur",
				"line_items[0][price_data][unit_amount]":        "1999",
				"line_items[0][price_data][product_data][name]": "ACM-001",
				"mode":                             "payment",
				"success_url":                      DefaultSuccessRedirectURL,
				"customer":                         "cus_123",
				"payment_method_types[0]":          "card",
				"expires_at":                       strconv.FormatInt(time.Date(2025, 6, 4, 12, 0, 0, 0, time.UTC).Unix(), 10),
				"payment_intent_data[description]": "Acme - Invoice ACM-001",
				"payment_intent_data[setup_future_usage]":             "off_session",
				"payment_intent_data[metadata][tanvo_customer_id]":    testCustomerID.String(),
				"payment_intent_data[metadata][tanvo_invoice_id]":     testInvoiceID.String(),
				"payment_intent_data[metadata][invoice_issuing_date]": "2025-05-28",
				"payment_intent_data[metadata][invoice_type]":         "subscription",
				"payment_intent_data[metadata][payment_type]":         "one-time",
				// source: spec/services/invoices/payments/stripe_service_spec.rb:93 (no consent collection by default)
				"consent_collection[terms_of_service]": "",
			},
		},
		{
			source:   "spec/services/invoices/payments/stripe_service_spec.rb:102",
			name:     "invoice with terms of service consent",
			settings: `{"require_terms_of_service_consent":true}`,
			params:   invoiceURLParams,
			reply:    session,
			key:      "payment-intent:" + testIntentID.String(),
			form:     map[string]string{"consent_collection[terms_of_service]": "required"},
		},
		{
			source: "spec/services/invoices/payments/stripe_service_spec.rb:124",
			name:   "Indian customers do not save the card",
			params: invoiceURLParams,
			edit:   func(p *payment.PaymentURLParams) { p.CustomerCountry = "IN" },
			reply:  session,
			key:    "payment-intent:" + testIntentID.String(),
			form:   map[string]string{"payment_intent_data[setup_future_usage]": ""},
		},
		{
			source: "spec/services/invoices/payments/stripe_service_spec.rb:130",
			name:   "crypto customers do not save the card",
			params: invoiceURLParams,
			edit:   func(p *payment.PaymentURLParams) { p.MethodTypes = []string{MethodCard, MethodCrypto} },
			reply:  session,
			key:    "payment-intent:" + testIntentID.String(),
			form:   map[string]string{"payment_intent_data[setup_future_usage]": ""},
		},
		{
			source:   "spec/services/invoices/payments/stripe_service_spec.rb:144",
			name:     "invoice Stripe error",
			params:   invoiceURLParams,
			reply:    fail(http.StatusBadRequest, "invalid_request_error", "", "error"),
			key:      "payment-intent:" + testIntentID.String(),
			wantKind: payment.KindInvalidRequest,
		},
		{
			source: "spec/services/payment_requests/payments/stripe_service_spec.rb:60,103,134",
			name:   "payment request payload",
			params: requestURLParams,
			reply:  session,
			form: map[string]string{
				"line_items[0][price_data][unit_amount]":            "1000",
				"line_items[0][price_data][currency]":               "usd",
				"line_items[1][price_data][unit_amount]":            "2500",
				"line_items[1][price_data][product_data][name]":     "ACM-002",
				"payment_method_types[1]":                           "sepa_debit",
				"payment_intent_data[description]":                  "Acme - Overdue invoices",
				"payment_intent_data[metadata][tanvo_customer_id]":  testCustomerID.String(),
				"payment_intent_data[metadata][tanvo_payable_id]":   testRequestID.String(),
				"payment_intent_data[metadata][tanvo_payable_type]": "payment_request",
				"payment_intent_data[metadata][payment_type]":       "one-time",
				"payment_intent_data[metadata][tanvo_invoice_id]":   "",
				"payment_intent_data[setup_future_usage]":           "",
				"expires_at":                           "",
				"consent_collection[terms_of_service]": "",
			},
		},
		{
			source:   "spec/services/payment_requests/payments/stripe_service_spec.rb:119",
			name:     "payment request with terms of service consent",
			settings: `{"require_terms_of_service_consent":true}`,
			params:   requestURLParams,
			reply:    session,
			form:     map[string]string{"consent_collection[terms_of_service]": "required"},
		},
		{
			source:   "spec/services/payment_requests/payments/stripe_service_spec.rb:176",
			name:     "payment request Stripe error",
			params:   requestURLParams,
			reply:    fail(http.StatusBadRequest, "invalid_request_error", "", "error"),
			wantKind: payment.KindInvalidRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			settings := tt.settings
			if settings == "" {
				settings = `{}`
			}
			rp := newReplay(t, map[string][]reply{routeSessions: {tt.reply}})
			p := tt.params()
			if tt.edit != nil {
				tt.edit(&p)
			}
			got, err := rp.gateway().CreatePaymentURL(context.Background(), connection(settings), p)
			if k := payment.ErrorKindOf(err); k != tt.wantKind {
				t.Fatalf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
			}
			req := rp.only(routeSessions)
			if req.IdempotencyKey != tt.key && tt.key != "" {
				t.Errorf("Idempotency-Key: got = %q, want = %q", req.IdempotencyKey, tt.key)
			}
			if tt.wantKind != "" {
				return
			}
			if got.URL != "https://example.com" || got.SessionID != "cs_123" {
				t.Errorf("payment URL: got = %+v, want = https://example.com of cs_123", got)
			}
			checkForm(t, req.Form, tt.form)
		})
	}
}

func TestExpirePaymentURL(t *testing.T) {
	const (
		get    = "GET /v1/checkout/sessions/cs_123"
		expire = "POST /v1/checkout/sessions/cs_123/expire"
	)
	tests := []struct {
		source      string
		name        string
		session     string
		routes      map[string][]reply
		wantExpires int
	}{
		{
			source: "spec/services/invoices/payments/stripe_service_spec.rb:170", name: "expires an open session", session: "cs_123",
			routes: map[string][]reply{
				get:    {ok(`{"id":"cs_123","object":"checkout.session","status":"open"}`)},
				expire: {ok(`{"id":"cs_123","object":"checkout.session","status":"expired"}`)},
			},
			wantExpires: 1,
		},
		{
			source: "spec/services/invoices/payments/stripe_service_spec.rb:180", name: "leaves a completed session", session: "cs_123",
			routes: map[string][]reply{get: {ok(`{"id":"cs_123","object":"checkout.session","status":"complete"}`)}},
		},
		{
			source: "spec/services/invoices/payments/stripe_service_spec.rb:193", name: "a session that can no longer expire", session: "cs_123",
			routes: map[string][]reply{
				get:    {ok(`{"id":"cs_123","object":"checkout.session","status":"open"}`)},
				expire: {fail(http.StatusBadRequest, "invalid_request_error", "", "Only Checkout Sessions with a status in [\"open\"] can be expired.")},
			},
			wantExpires: 1,
		},
		{
			source: "spec/services/invoices/payments/stripe_service_spec.rb:203", name: "no session", routes: map[string][]reply{},
		},
		{
			source: "app/services/invoices/payments/stripe_service.rb expire_payment_url", name: "an unknown session", session: "cs_123",
			routes: map[string][]reply{get: {fail(http.StatusNotFound, "invalid_request_error", "resource_missing", "No such checkout.session")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, tt.routes)
			if err := rp.gateway().ExpirePaymentURL(context.Background(), connection(`{}`), tt.session); err != nil {
				t.Fatalf("ExpirePaymentURL: got = %v, want = nil", err)
			}
			if n := len(rp.requests(expire)); n != tt.wantExpires {
				t.Errorf("expirations: got = %d, want = %d", n, tt.wantExpires)
			}
		})
	}
}
