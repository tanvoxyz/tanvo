package moneyhash

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

func paymentParams(p payment.Payable) payment.PaymentParams {
	return payment.PaymentParams{
		PaymentID:          testPaymentID,
		Payable:            p,
		InvoiceType:        "subscription",
		CustomerID:         testCustomerID,
		ProviderCustomerID: testCustomer,
		ProviderMethodID:   testCard,
		Amount:             799,
		Currency:           usd,
		Customer:           testCustomerParams(),
	}
}

func TestStatusOf(t *testing.T) {
	for status, want := range map[string]payment.State{
		"PENDING":                payment.StateProcessing,
		"PENDING_AUTHENTICATION": payment.StateProcessing,
		"UNPROCESSED":            payment.StateProcessing,
		"SUCCESSFUL":             payment.StateSucceeded,
		"PROCESSED":              payment.StateSucceeded,
		"FAILED":                 payment.StateFailed,
		"SOMETHING":              payment.StatePending,
	} {
		if got := StatusOf(status); got != want {
			t.Errorf("StatusOf(%q): got = %v, want = %v", status, got, want)
		}
	}
}

func TestCreatePayment(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payable payment.Payable
		request string
	}{
		// source: spec/services/payment_providers/moneyhash/payments/create_service_spec.rb:35
		// source: spec/services/payment_providers/moneyhash/payments/create_service_spec.rb:76
		{"invoice", payment.InvoicePayable(testInvoiceID), requestInvoicePayment},
		// source: spec/services/payment_requests/payments/moneyhash_service_spec.rb:145
		// source: spec/services/payment_requests/payments/moneyhash_service_spec.rb:190
		// source: spec/services/payment_requests/payments/moneyhash_service_spec.rb:93
		{"payment request", payment.RequestPayable(testRequestID), requestRequestPayment},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routeIntents: {fixture("recurring_mit_payment_success_response.json")}})
			p := paymentParams(tt.payable)
			res, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p)
			if err != nil {
				t.Fatalf("CreatePayment: %v", err)
			}
			want := payment.PaymentResult{
				ProviderPaymentID: "ZdBEBXL", ProviderStatus: "PROCESSED", Status: payment.StateSucceeded,
				ProviderMethodID: testCard, Amount: 799, Currency: usd,
			}
			if res.ProviderPaymentID != want.ProviderPaymentID || res.ProviderStatus != want.ProviderStatus ||
				res.Status != want.Status || res.ProviderMethodID != want.ProviderMethodID ||
				res.Amount != want.Amount || res.Currency != want.Currency {
				t.Errorf("result: got = %+v, want = %+v", res, want)
			}
			r := rp.only(routeIntents)
			payableID := testInvoiceID.String()
			if tt.payable.Type == payment.PayableTypePaymentRequest {
				payableID = testRequestID.String()
			}
			for path, want := range map[string]string{
				"card_token":                         testCard,
				"customer":                           testCustomer,
				"amount":                             "7.99",
				"amount_currency":                    "USD",
				"merchant_initiated":                 "true",
				"payment_type":                       paymentTypeUnscheduled,
				"recurring_data.agreement_id":        testCustomerID.String(),
				"flow_id":                            testFlowID,
				"webhook_url":                        testWebhookURL,
				"billing_data.email":                 "a.shahwan@moneyhash.io",
				"custom_fields." + fieldMIT:          "true",
				"custom_fields." + fieldRequest:      tt.request,
				"custom_fields." + fieldPayableID:    payableID,
				"custom_fields." + fieldPayableType:  string(tt.payable.Type),
				"custom_fields." + fieldCustomerID:   testCustomerID.String(),
				"custom_fields." + fieldConnectionID: testConnID.String(),
			} {
				if got := r.str(strings.Split(path, ".")...); got != want {
					t.Errorf("%s: got = %q, want = %q", path, got, want)
				}
			}
			if r.IdempotencyKey != p.IdempotencyKey() {
				t.Errorf("idempotency key: got = %q, want = %q", r.IdempotencyKey, p.IdempotencyKey())
			}
			if invoice := r.has("custom_fields", fieldInvoiceType); invoice != (tt.payable.Type == payment.PayableTypeInvoice) {
				t.Errorf("invoice type sent: got = %v, want = %v", invoice, !invoice)
			}
		})
	}
}

func TestCreatePaymentFailures(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		// source: spec/services/payment_providers/moneyhash/payments/create_service_spec.rb:53
		rp := newReplay(t, map[string][]reply{routeIntents: {failFixture(http.StatusBadRequest, "recurring_mit_payment_failure_response.json")}})
		_, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), paymentParams(payment.InvoicePayable(testInvoiceID)))
		pe, ok := payment.AsProviderError(err)
		if !ok || pe.Kind != payment.KindInvalidRequest || pe.Code != "400" || pe.Message != "card_token: This field may not be null." {
			t.Fatalf("CreatePayment: got = %v, want = invalid_request 400 with the field error", err)
		}
	})
	t.Run("unprocessable", func(t *testing.T) {
		// source: spec/services/payment_requests/payments/moneyhash_service_spec.rb:164
		rp := newReplay(t, map[string][]reply{routeIntents: {raw(http.StatusUnprocessableEntity, `error`)}})
		_, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), paymentParams(payment.RequestPayable(testRequestID)))
		if pe, ok := payment.AsProviderError(err); !ok || pe.Kind != payment.KindInvalidRequest || pe.HTTPStatus != 422 {
			t.Fatalf("CreatePayment: got = %v, want = invalid_request 422", err)
		}
	})
	t.Run("no provider customer", func(t *testing.T) {
		// source: spec/services/payment_requests/payments/moneyhash_service_spec.rb:64
		rp := newReplay(t, nil)
		p := paymentParams(payment.RequestPayable(testRequestID))
		p.ProviderCustomerID = ""
		var ve *core.ValidationError
		if _, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p); !errors.As(err, &ve) {
			t.Fatalf("CreatePayment: got = %v, want = a validation error", err)
		}
		if rp.count() != 0 {
			t.Errorf("requests: got = %d, want = 0", rp.count())
		}
	})
	t.Run("no payment method", func(t *testing.T) {
		// source: spec/services/payment_requests/payments/moneyhash_service_spec.rb:76
		rp := newReplay(t, nil)
		p := paymentParams(payment.RequestPayable(testRequestID))
		p.ProviderMethodID = ""
		if _, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), p); !errors.Is(err, payment.ErrNoPaymentMethod) {
			t.Fatalf("CreatePayment: got = %v, want = %v", err, payment.ErrNoPaymentMethod)
		}
		if rp.count() != 0 {
			t.Errorf("requests: got = %d, want = 0", rp.count())
		}
	})
	t.Run("server error", func(t *testing.T) {
		rp := newReplay(t, map[string][]reply{routeIntents: {raw(http.StatusBadGateway, `bad gateway`)}})
		_, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), paymentParams(payment.InvoicePayable(testInvoiceID)))
		if !errors.Is(err, payment.ErrRetryable) {
			t.Fatalf("CreatePayment: got = %v, want = retryable", err)
		}
	})
	t.Run("no intent id", func(t *testing.T) {
		rp := newReplay(t, map[string][]reply{routeIntents: {raw(http.StatusOK, `{"status":{"code":200},"data":{"status":"PROCESSED"}}`)}})
		_, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), paymentParams(payment.InvoicePayable(testInvoiceID)))
		if pe, ok := payment.AsProviderError(err); !ok || pe.Kind != payment.KindUnavailable {
			t.Fatalf("CreatePayment: got = %v, want = unavailable", err)
		}
	})
}

func TestCreatePaymentStatusFallback(t *testing.T) {
	for body, want := range map[string]string{
		`{"data":{"id":"i1","active_transaction":{"status":"FAILED"}}}`: "FAILED",
		`{"data":{"id":"i1"}}`: "PENDING",
	} {
		rp := newReplay(t, map[string][]reply{routeIntents: {raw(http.StatusCreated, body)}})
		res, err := rp.gateway().CreatePayment(context.Background(), defaultConn(), paymentParams(payment.InvoicePayable(testInvoiceID)))
		if err != nil || res.ProviderStatus != want {
			t.Errorf("CreatePayment(%s): got = %q, %v, want = %q", body, res.ProviderStatus, err, want)
		}
	}
}

func TestCreatePaymentURL(t *testing.T) {
	// source: spec/services/invoices/payments/moneyhash_service_spec.rb:101
	params := payment.PaymentURLParams{
		IntentID:    testIntentID,
		Payable:     payment.InvoicePayable(testInvoiceID),
		InvoiceType: "one_off",
		CustomerID:  testCustomerID,
		Lines:       []payment.PaymentURLLine{{Name: "INV-1", Amount: 5000}},
		Currency:    usd,
		ExpiresAt:   testNow.Add(24 * time.Hour),
		Customer:    testCustomerParams(),
	}
	t.Run("new card", func(t *testing.T) {
		rp := newReplay(t, map[string][]reply{routeIntents: {fixture("checkout_url_response.json")}})
		u, err := rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), params)
		if err != nil {
			t.Fatalf("CreatePaymentURL: %v", err)
		}
		if want := (payment.PaymentURL{URL: "https://stg-embed.moneyhash.io/embed/payment/LYPoo59", SessionID: "LYPoo59"}); u != want {
			t.Errorf("url: got = %+v, want = %+v", u, want)
		}
		r := rp.only(routeIntents)
		for path, want := range map[string]string{
			"amount":                            "50.00",
			"amount_currency":                   "USD",
			"merchant_initiated":                "false",
			"tokenize_card":                     "true",
			"payment_type":                      paymentTypeUnscheduled,
			"recurring_data.agreement_id":       testCustomerID.String(),
			"expires_after_seconds":             "86400",
			"custom_fields." + fieldMIT:         "false",
			"custom_fields." + fieldRequest:     requestPaymentURL,
			"custom_fields." + fieldPayableID:   testInvoiceID.String(),
			"custom_fields." + fieldInvoiceType: "one_off",
		} {
			if got := r.str(strings.Split(path, ".")...); got != want {
				t.Errorf("%s: got = %q, want = %q", path, got, want)
			}
		}
		if r.IdempotencyKey != testIntentID.String() || r.has("customer") {
			t.Errorf("request: got = key %q, customer %v, want = key %q, no customer", r.IdempotencyKey, r.has("customer"), testIntentID)
		}
	})
	t.Run("known customer", func(t *testing.T) {
		rp := newReplay(t, map[string][]reply{routeIntents: {fixture("checkout_url_response.json")}})
		p := params
		p.ProviderCustomerID = testCustomer
		p.ExpiresAt = time.Time{}
		if _, err := rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), p); err != nil {
			t.Fatalf("CreatePaymentURL: %v", err)
		}
		r := rp.only(routeIntents)
		if r.str("customer") != testCustomer || r.has("tokenize_card") || r.has("expires_after_seconds") {
			t.Errorf("request: got = %v, want = the customer without tokenization or expiry", r.Body)
		}
	})
	t.Run("payment request", func(t *testing.T) {
		p := params
		p.Payable = payment.RequestPayable(testRequestID)
		if _, err := New().CreatePaymentURL(context.Background(), defaultConn(), p); !errors.Is(err, payment.ErrUnsupported) {
			t.Errorf("CreatePaymentURL: got = %v, want = %v", err, payment.ErrUnsupported)
		}
	})
	t.Run("nothing due", func(t *testing.T) {
		p := params
		p.Lines = nil
		var ve *core.ValidationError
		if _, err := New().CreatePaymentURL(context.Background(), defaultConn(), p); !errors.As(err, &ve) {
			t.Errorf("CreatePaymentURL: got = %v, want = a validation error", err)
		}
	})
}

func TestChargeUnsupported(t *testing.T) {
	g := New()
	ctx, conn := context.Background(), defaultConn()
	_, e1 := g.GetPayment(ctx, conn, "i1")
	_, e2 := g.CancelPayment(ctx, conn, "i1")
	e3 := g.UpdatePaymentReference(ctx, conn, "i1", "ref")
	_, e4 := g.Authorize(ctx, conn, payment.AuthorizeParams{})
	e5 := g.ExpirePaymentURL(ctx, conn, "i1")
	_, e6 := g.Refund(ctx, conn, payment.RefundParams{})
	for i, err := range []error{e1, e2, e3, e4, e5, e6} {
		if !errors.Is(err, payment.ErrUnsupported) {
			t.Errorf("call %d: got = %v, want = %v", i+1, err, payment.ErrUnsupported)
		}
	}
}
