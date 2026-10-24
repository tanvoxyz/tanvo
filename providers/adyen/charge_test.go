package adyen

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
	routePayments = "POST /payments"
	routeCancel   = "POST /payments/PSPREF123/cancels"
)

func invoicePayment() payment.PaymentParams {
	return payment.PaymentParams{
		PaymentID:          testPaymentID,
		Payable:            payment.InvoicePayable(testInvoiceID),
		InvoiceType:        "subscription",
		CustomerID:         testCustomerID,
		BillingEntityID:    testEntityID,
		ProviderCustomerID: testExternalID,
		Amount:             1000,
		Currency:           usd,
		Description:        "Tanvo - Invoice TAN-001",
		Customer:           payment.CustomerParams{CustomerID: testCustomerID, ExternalID: testExternalID, Email: "billing@acme.example"},
	}
}

func TestStatusOf(t *testing.T) {
	// source: app/models/payment_providers/adyen_provider.rb:10-12
	tests := map[string]payment.State{
		"AuthorisedPending": payment.StateProcessing,
		"Received":          payment.StateProcessing,
		"Authorised":        payment.StateSucceeded,
		"SentForSettle":     payment.StateSucceeded,
		"SettleScheduled":   payment.StateSucceeded,
		"Settled":           payment.StateSucceeded,
		"Refunded":          payment.StateSucceeded,
		"Cancelled":         payment.StateFailed,
		"CaptureFailed":     payment.StateFailed,
		"Error":             payment.StateFailed,
		"Expired":           payment.StateFailed,
		"Refused":           payment.StateFailed,
		"RedirectShopper":   payment.StatePending,
		"":                  payment.StatePending,
	}
	for code, want := range tests {
		if got := StatusOf(code); got != want {
			t.Errorf("StatusOf(%q): got = %q, want = %q", code, got, want)
		}
	}
}

func TestCreatePayment(t *testing.T) {
	const src = "spec/services/payment_providers/adyen/payments/create_service_spec.rb:"
	long := strings.Repeat("A", 60) + " - Overdue invoices: TAN-001"
	tests := []struct {
		source     string
		name       string
		edit       func(p *payment.PaymentParams)
		routes     map[string][]reply
		wantStatus payment.State
		wantPSP    string
		wantErr    error
		wantKind   payment.ErrorKind
		check      func(t *testing.T, rp *replay)
	}{
		{
			source: src + "72", name: "charges the first stored method",
			routes:     map[string][]reply{routePaymentMethods: {fixture("payment_methods_response.json")}, routePayments: {fixture("payments_authorised_response.json")}},
			wantStatus: payment.StateSucceeded, wantPSP: "PSP_AUTHORISED_1",
			check: func(t *testing.T, rp *replay) {
				r := rp.only(routePayments)
				want := map[string]string{
					"reference":                "Tanvo - Invoice TAN-001",
					"merchantAccount":          testMerchantAccount,
					"shopperReference":         testExternalID,
					"shopperInteraction":       "ContAuth",
					"recurringProcessingModel": "UnscheduledCardOnFile",
					"shopperEmail":             "billing@acme.example",
				}
				for k, w := range want {
					if got := r.str(k); got != w {
						t.Errorf("%s: got = %q, want = %q", k, got, w)
					}
				}
				if got := r.str("paymentMethod"); got != `{"storedPaymentMethodId":"STORED_PM_1","type":"scheme"}` {
					t.Errorf("paymentMethod: got = %s, want = the stored scheme method", got)
				}
				if got := r.str("amount"); got != `{"currency":"USD","value":1000}` {
					t.Errorf("amount: got = %s, want = 1000 USD", got)
				}
				// source: app/services/payment_providers/adyen/payments/create_service.rb:86
				if want := "payment-" + testPaymentID.String(); r.IdempotencyKey != want {
					t.Errorf("Idempotency-Key: got = %q, want = %q", r.IdempotencyKey, want)
				}
				if got := rp.only(routePaymentMethods).str("shopperReference"); got != testExternalID {
					t.Errorf("paymentMethods shopperReference: got = %q, want = %q", got, testExternalID)
				}
			},
		},
		{
			source: src + "94", name: "uses the requested method",
			edit:       func(p *payment.PaymentParams) { p.ProviderMethodID = "pm_test" },
			routes:     map[string][]reply{routePayments: {fixture("payments_authorised_response.json")}},
			wantStatus: payment.StateSucceeded, wantPSP: "PSP_AUTHORISED_1",
			check: func(t *testing.T, rp *replay) {
				if got := rp.only(routePayments).str("paymentMethod", "storedPaymentMethodId"); got != "pm_test" {
					t.Errorf("storedPaymentMethodId: got = %q, want = pm_test", got)
				}
				if n := len(rp.requests(routePaymentMethods)); n != 0 {
					t.Errorf("paymentMethods calls: got = %d, want = 0", n)
				}
			},
		},
		{
			source: src + "111", name: "reference within the limit",
			edit: func(p *payment.PaymentParams) {
				p.ProviderMethodID, p.Description = "pm_test", "Hooli - Overdue invoices: TAN-001"
			},
			routes:     map[string][]reply{routePayments: {fixture("payments_authorised_response.json")}},
			wantStatus: payment.StateSucceeded, wantPSP: "PSP_AUTHORISED_1",
			check: func(t *testing.T, rp *replay) {
				if got := rp.only(routePayments).str("reference"); got != "Hooli - Overdue invoices: TAN-001" {
					t.Errorf("reference: got = %q, want = unchanged", got)
				}
			},
		},
		{
			source: src + "124", name: "long reference clamped",
			edit:       func(p *payment.PaymentParams) { p.ProviderMethodID, p.Description = "pm_test", long },
			routes:     map[string][]reply{routePayments: {fixture("payments_authorised_response.json")}},
			wantStatus: payment.StateSucceeded, wantPSP: "PSP_AUTHORISED_1",
			check: func(t *testing.T, rp *replay) {
				got := rp.only(routePayments).str("reference")
				if len(got) != MaxReferenceLength {
					t.Errorf("reference length: got = %d, want = %d", len(got), MaxReferenceLength)
				}
				// source: spec/services/payment_providers/adyen/payments/create_service_spec.rb:136
				if !strings.HasSuffix(got, "TAN-001") || strings.HasPrefix(got, strings.Repeat("A", 60)) {
					t.Errorf("reference: got = %q, want = the tail with the invoice number", got)
				}
			},
		},
		{
			source: src + "153", name: "error response fails the payment",
			edit:    func(p *payment.PaymentParams) { p.ProviderMethodID = "pm_test" },
			routes:  map[string][]reply{routePayments: {failFixture(http.StatusUnprocessableEntity, "payments_error_response.json")}},
			wantErr: payment.ErrRejected, wantKind: payment.KindInvalidRequest,
		},
		{
			source: src + "190", name: "listing stored methods refused",
			routes:  map[string][]reply{routePaymentMethods: {fail(http.StatusUnprocessableEntity, "validation", "14_030", "Invalid card number")}},
			wantErr: payment.ErrRejected, wantKind: payment.KindInvalidRequest,
		},
		{
			source: src + "210", name: "payment refused by validation",
			edit:    func(p *payment.PaymentParams) { p.ProviderMethodID = "pm_test" },
			routes:  map[string][]reply{routePayments: {fail(http.StatusUnprocessableEntity, "validation", "14_030", "Invalid card number")}},
			wantErr: payment.ErrRejected, wantKind: payment.KindInvalidRequest,
		},
		{
			// Lago fails the payment and re-raises any other AdyenError
			// (create_service.rb:44-45); a permission error fails it.
			source: src + "243", name: "permission error",
			edit:    func(p *payment.PaymentParams) { p.ProviderMethodID = "pm_test" },
			routes:  map[string][]reply{routePayments: {fail(http.StatusForbidden, "security", "010", "Not allowed")}},
			wantErr: payment.ErrAuthentication, wantKind: payment.KindAuthentication,
		},
		{
			source: "app/services/payment_providers/adyen/payments/create_service.rb:35-37 (Refused)", name: "refused result",
			edit:       func(p *payment.PaymentParams) { p.ProviderMethodID = "pm_test" },
			routes:     map[string][]reply{routePayments: {ok(`{"pspReference":"PSP_REFUSED","resultCode":"Refused","refusalReason":"Refused"}`)}},
			wantStatus: payment.StateFailed, wantPSP: "PSP_REFUSED",
		},
		{
			source: "app/services/payment_providers/adyen/payments/create_service.rb:69-79 (no stored method)", name: "no stored method",
			routes:  map[string][]reply{routePaymentMethods: {fixture("payment_methods_empty_response.json")}},
			wantErr: payment.ErrNoPaymentMethod, wantKind: payment.KindInvalidRequest,
		},
		{
			source: "Adyen error code 704 (idempotency key in progress)", name: "idempotency conflict",
			edit:     func(p *payment.PaymentParams) { p.ProviderMethodID = "pm_test" },
			routes:   map[string][]reply{routePayments: {fail(http.StatusUnprocessableEntity, "validation", "704", "request already processed or in progress")}},
			wantKind: payment.KindIdempotency,
		},
		{
			source: "app/services/payment_providers/adyen/payments/create_service.rb:46 (retried by the job)", name: "server error retried",
			edit:    func(p *payment.PaymentParams) { p.ProviderMethodID = "pm_test" },
			routes:  map[string][]reply{routePayments: {fail(http.StatusInternalServerError, "internal", "901", "boom")}},
			wantErr: payment.ErrRetryable, wantKind: payment.KindUnavailable,
		},
		{
			source: "Adyen rate limit", name: "rate limited",
			edit:    func(p *payment.PaymentParams) { p.ProviderMethodID = "pm_test" },
			routes:  map[string][]reply{routePayments: {fail(http.StatusTooManyRequests, "throttling", "", "slow down")}},
			wantErr: payment.ErrRetryable, wantKind: payment.KindRateLimited,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, tt.routes)
			p := invoicePayment()
			if tt.edit != nil {
				tt.edit(&p)
			}
			res, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p)
			if tt.wantKind != "" || tt.wantErr != nil {
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Errorf("%s: error: got = %v, want = %v", tt.source, err, tt.wantErr)
				}
				if k := payment.ErrorKindOf(err); k != tt.wantKind {
					t.Errorf("%s: kind: got = %q, want = %q", tt.source, k, tt.wantKind)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: CreatePayment: got = %v, want = nil", tt.source, err)
			}
			if res.Status != tt.wantStatus || res.ProviderPaymentID != tt.wantPSP {
				t.Errorf("%s: result: got = %s %q, want = %s %q", tt.source, res.Status, res.ProviderPaymentID, tt.wantStatus, tt.wantPSP)
			}
			if tt.check != nil {
				tt.check(t, rp)
			}
		})
	}
}

func TestCreatePaymentResult(t *testing.T) {
	// source: spec/services/payment_providers/adyen/payments/create_service_spec.rb:72
	rp := newReplay(t, map[string][]reply{routePaymentMethods: {fixture("payment_methods_response.json")}, routePayments: {fixture("payments_authorised_response.json")}})
	res, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), invoicePayment())
	if err != nil {
		t.Fatalf("CreatePayment: got = %v, want = nil", err)
	}
	want := payment.PaymentResult{
		ProviderPaymentID: "PSP_AUTHORISED_1",
		ProviderStatus:    "Authorised",
		Status:            payment.StateSucceeded,
		ProviderMethodID:  "STORED_PM_1",
		Amount:            1000,
		Currency:          usd,
	}
	if res.ProviderPaymentID != want.ProviderPaymentID || res.ProviderStatus != want.ProviderStatus || res.Status != want.Status ||
		res.ProviderMethodID != want.ProviderMethodID || res.Amount != want.Amount || res.Currency != want.Currency {
		t.Errorf("result: got = %+v, want = %+v", res, want)
	}
	if res.RequiresAction || res.StaleMethodID != "" {
		t.Errorf("result: got = %+v, want = no action and no stale method", res)
	}

	// Invalid parameters are refused before any call.
	none := newReplay(t, map[string][]reply{})
	p := invoicePayment()
	p.Amount = 0
	var ve *core.ValidationError
	if _, err := none.gateway().CreatePayment(context.Background(), defaultConn(), p); !errors.As(err, &ve) || none.count() != 0 {
		t.Errorf("invalid params: got = %v with %d requests, want = a validation error and none", err, none.count())
	}
}

func TestConnectionError(t *testing.T) {
	// source: spec/services/payment_providers/adyen/payments/cancel_service_spec.rb:156
	rp := newReplay(t, map[string][]reply{})
	rp.srv.Close()
	_, err := rp.gateway().CancelPayment(context.Background(), defaultConn(), "PSPREF123")
	if k := payment.ErrorKindOf(err); k != payment.KindUnavailable || !errors.Is(err, payment.ErrRetryable) {
		t.Errorf("error: got = %q (%v), want = unavailable and retryable", k, err)
	}
}

func TestCancelPayment(t *testing.T) {
	const src = "spec/services/payment_providers/adyen/payments/cancel_service_spec.rb:"
	tests := []struct {
		source   string
		name     string
		reply    reply
		wantKind payment.ErrorKind
	}{
		{src + "51", "accepted", fixture("cancels_response.json"), ""},
		{src + "91", "not cancelable (422)", fail(http.StatusUnprocessableEntity, "validation", "167", "Original pspReference required for this operation"), ""},
		{src + "136", "already cancelled (validation)", fail(http.StatusUnprocessableEntity, "validation", "167", "payment already cancelled"), ""},
		{src + "125", "other 4xx", fail(http.StatusUnauthorized, "security", "000", "Invalid Merchant Account"), payment.KindAuthentication},
		{"app/services/payment_providers/adyen/payments/cancel_service.rb:37", "bad request", fail(http.StatusBadRequest, "validation", "702", "Unexpected input"), payment.KindInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routeCancel: {tt.reply}})
			res, err := rp.gateway().CancelPayment(context.Background(), defaultConn(), "PSPREF123")
			if k := payment.ErrorKindOf(err); k != tt.wantKind || (tt.wantKind == "" && err != nil) {
				t.Fatalf("%s: error: got = %v, want kind = %q", tt.source, err, tt.wantKind)
			}
			// source: spec/services/payment_providers/adyen/payments/cancel_service_spec.rb:65
			// source: spec/services/payment_providers/adyen/payments/cancel_service_spec.rb:70
			// source: spec/services/payment_providers/adyen/payments/cancel_service_spec.rb:104
			if !reflect.DeepEqual(res, payment.PaymentResult{}) {
				t.Errorf("%s: result: got = %+v, want = zero (the webhook reports the state)", tt.source, res)
			}
			r := rp.only(routeCancel)
			if r.str("merchantAccount") != testMerchantAccount {
				t.Errorf("%s: merchantAccount: got = %q, want = %q", tt.source, r.str("merchantAccount"), testMerchantAccount)
			}
			// source: spec/services/payment_providers/adyen/payments/cancel_service_spec.rb:58
			if r.IdempotencyKey != "cancel-PSPREF123" {
				t.Errorf("%s: Idempotency-Key: got = %q, want = cancel-PSPREF123", tt.source, r.IdempotencyKey)
			}
		})
	}
}

func invoiceURLParams() payment.PaymentURLParams {
	return payment.PaymentURLParams{
		IntentID:           testIntentID,
		Payable:            payment.InvoicePayable(testInvoiceID),
		InvoiceType:        "subscription",
		IssuingDate:        core.DateOf(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), time.UTC),
		CustomerID:         testCustomerID,
		ProviderCustomerID: testExternalID,
		Lines:              []payment.PaymentURLLine{{Name: "TAN-001", Amount: 1000}},
		Currency:           usd,
		ExpiresAt:          time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC),
		Customer:           payment.CustomerParams{CustomerID: testCustomerID, ExternalID: testExternalID, Email: "test@example.com"},
	}
}

func TestCreatePaymentURL(t *testing.T) {
	const inv = "spec/services/invoices/payments/adyen_service_spec.rb:"
	const pr = "spec/services/payment_requests/payments/adyen_service_spec.rb:"
	requestParams := func() payment.PaymentURLParams {
		p := invoiceURLParams()
		p.IntentID, p.Payable, p.InvoiceType, p.IssuingDate, p.ExpiresAt = payment.IntentID{}, payment.RequestPayable(testRequestID), "", core.Date{}, time.Time{}
		p.Lines = []payment.PaymentURLLine{{Name: "TAN-001", Amount: 200}, {Name: "TAN-002", Amount: 599}}
		return p
	}
	tests := []struct {
		source   string
		name     string
		params   payment.PaymentURLParams
		reply    reply
		wantKind payment.ErrorKind
		want     map[string]string // body path (dotted) → value; "" wants absent
		wantKey  string
	}{
		{
			source: inv + "269", name: "invoice link",
			params: invoiceURLParams(), reply: fixture("payment_links_response.json"),
			want: map[string]string{
				"reference":                                "TAN-001",
				"amount.value":                             "1000",
				"amount.currency":                          "USD",
				"merchantAccount":                          testMerchantAccount,
				"returnUrl":                                DefaultSuccessRedirectURL,
				"shopperReference":                         testExternalID,
				"storePaymentMethodMode":                   "enabled",
				"recurringProcessingModel":                 "UnscheduledCardOnFile",
				"expiresAt":                                "2026-05-08T10:00:00Z",
				"metadata." + metaCustomerID:               testCustomerID.String(),
				"metadata." + metaInvoiceID:                testInvoiceID.String(),
				"metadata." + metaIssuingDate:              "2026-05-01",
				"metadata." + metaInvoiceType:              "subscription",
				"metadata." + metaPaymentType:              "one-time",
				"metadata." + metaPayableType:              "invoice",
				"metadata." + metaPayableID:                testInvoiceID.String(),
				"shopperEmail":                             "test@example.com",
				"applicationInfo.merchantApplication.name": "Tanvo",
			},
			wantKey: testIntentID.String(),
		},
		{source: inv + "277", name: "invoice link, nothing paid yet", params: invoiceURLParams(), reply: fixture("payment_links_response.json"), want: map[string]string{"amount.value": "1000"}, wantKey: testIntentID.String()},
		{source: inv + "287", name: "with email", params: invoiceURLParams(), reply: fixture("payment_links_response.json"), want: map[string]string{"shopperEmail": "test@example.com"}, wantKey: testIntentID.String()},
		{
			source: inv + "295", name: "without email",
			params: func() payment.PaymentURLParams { p := invoiceURLParams(); p.Customer.Email = ""; return p }(),
			reply:  fixture("payment_links_response.json"), want: map[string]string{"shopperEmail": ""}, wantKey: testIntentID.String(),
		},
		{source: inv + "318", name: "invoice link generated", params: invoiceURLParams(), reply: fixture("payment_links_response.json"), wantKey: testIntentID.String()},
		{source: inv + "330", name: "invoice link error", params: invoiceURLParams(), reply: failFixture(http.StatusUnprocessableEntity, "payment_links_error_response.json"), wantKind: payment.KindInvalidRequest},
		{
			source: pr + "68", name: "payment request link",
			params: requestParams(), reply: fixture("payment_links_response.json"),
			want: map[string]string{
				"reference":                   "Overdue invoices",
				"amount.value":                "799",
				"amount.currency":             "USD",
				"expiresAt":                   "2026-07-16T10:00:00Z",
				"metadata." + metaCustomerID:  testCustomerID.String(),
				"metadata." + metaPayableID:   testRequestID.String(),
				"metadata." + metaPayableType: "payment_request",
				"metadata." + metaPaymentType: "one-time",
				"metadata." + metaInvoiceID:   "",
				"shopperReference":            testExternalID,
				"storePaymentMethodMode":      "enabled",
			},
		},
		{source: pr + "109", name: "payment request link error", params: requestParams(), reply: fail(http.StatusInternalServerError, "internal", "901", "error"), wantKind: payment.KindUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routePaymentLinks: {tt.reply}})
			out, err := rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), tt.params)
			if k := payment.ErrorKindOf(err); k != tt.wantKind || (tt.wantKind == "" && err != nil) {
				t.Fatalf("%s: error: got = %v, want kind = %q", tt.source, err, tt.wantKind)
			}
			if tt.wantKind != "" {
				return
			}
			if out.URL != "https://test.adyen.link/test" || out.SessionID != "" {
				t.Errorf("%s: URL: got = %+v, want = the link and no session", tt.source, out)
			}
			r := rp.only(routePaymentLinks)
			for path, w := range tt.want {
				keys := strings.Split(path, ".")
				if keys[0] == "metadata" {
					keys = []string{"metadata", strings.TrimPrefix(path, "metadata.")}
				}
				switch {
				case w == "" && r.has(keys...):
					t.Errorf("%s: %s: got = %q, want = absent", tt.source, path, r.str(keys...))
				case w != "" && r.str(keys...) != w:
					t.Errorf("%s: %s: got = %q, want = %q", tt.source, path, r.str(keys...), w)
				}
			}
			if r.IdempotencyKey != tt.wantKey {
				t.Errorf("%s: Idempotency-Key: got = %q, want = %q", tt.source, r.IdempotencyKey, tt.wantKey)
			}
		})
	}
}

func TestCancelPaymentLogs(t *testing.T) {
	// source: spec/services/payment_providers/adyen/payments/cancel_service_spec.rb:95
	// source: spec/services/payment_providers/adyen/payments/cancel_service_spec.rb:140
	var buf bytes.Buffer
	rp := newReplay(t, map[string][]reply{routeCancel: {fail(http.StatusUnprocessableEntity, "validation", "167", "Original pspReference required for this operation")}})
	g := rp.gateway(WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	if _, err := g.CancelPayment(context.Background(), defaultConn(), "PSPREF123"); err != nil {
		t.Fatalf("CancelPayment: got = %v, want = nil", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log: got = %q, want = one JSON record", buf.String())
	}
	if rec["level"] != "INFO" || rec["status"] != float64(422) || rec["message"] != "Original pspReference required for this operation" || rec["provider_payment_id"] != "PSPREF123" {
		t.Errorf("log: got = %v, want = an Info record with the status and message", rec)
	}
}
