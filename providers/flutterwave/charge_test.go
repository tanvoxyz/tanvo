package flutterwave

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func urlParams(p payment.Payable) payment.PaymentURLParams {
	lines := []payment.PaymentURLLine{{InvoiceID: testInvoiceID, Name: "INV-001", Amount: 50000}}
	if p.Type == payment.PayableTypePaymentRequest {
		lines = append(lines, payment.PaymentURLLine{InvoiceID: testInvoice2ID, Name: "INV-002", Amount: 2500})
	}
	return payment.PaymentURLParams{
		Payable:          p,
		CustomerID:       testCustomerID,
		Lines:            lines,
		Currency:         usd,
		OrganizationName: "Test Organization",
		Customer: payment.CustomerParams{
			CustomerID: testCustomerID, Name: "John Doe", Email: "customer@example.com",
		},
	}
}

func TestStatusOf(t *testing.T) {
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:30
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:34
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:38
	for status, want := range map[string]payment.State{
		"pending":    payment.StateProcessing,
		"successful": payment.StateSucceeded,
		"failed":     payment.StateFailed,
		"cancelled":  payment.StateFailed,
		"other":      payment.StatePending,
	} {
		if got := StatusOf(status); got != want {
			t.Errorf("StatusOf(%q): got = %v, want = %v", status, got, want)
		}
	}
}

func TestAmountOf(t *testing.T) {
	// source: spec/services/invoices/payments/flutterwave_service_spec.rb:284
	// source: spec/services/payment_requests/payments/flutterwave_service_spec.rb:284
	for _, tt := range []struct {
		minor int64
		cur   string
		want  string
	}{{50000, "USD", "500.00"}, {1000000, "NGN", "10000.00"}, {1, "USD", "0.01"}, {500, "JPY", "500"}, {1500, "KWD", "1.500"}} {
		c := map[string]money.Currency{"USD": usd, "NGN": ngn, "JPY": jpy, "KWD": kwd}[tt.cur]
		if got := amountOf(tt.minor, c); string(got) != tt.want {
			t.Errorf("amountOf(%d %s): got = %s, want = %s", tt.minor, tt.cur, got, tt.want)
		}
	}
}

func TestCreatePaymentURL(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payable payment.Payable
		fields  map[string]string
		absent  []string
	}{
		// source: spec/services/invoices/payments/flutterwave_service_spec.rb:222
		// source: spec/services/invoices/payments/flutterwave_service_spec.rb:229
		// source: spec/services/invoices/payments/flutterwave_service_spec.rb:278
		{"invoice", payment.InvoicePayable(testInvoiceID), map[string]string{
			"amount":                     "500.00",
			"tx_ref":                     testInvoiceID.String(),
			"customizations.title":       "Test Organization - Invoice Payment",
			"customizations.description": "Payment for Invoice #INV-001",
			"meta.tanvo_customer_id":     testCustomerID.String(),
			"meta.tanvo_invoice_id":      testInvoiceID.String(),
			"meta.tanvo_organization_id": testOrgID.String(),
			"meta.tanvo_invoice_number":  "INV-001",
			"meta.payment_type":          "one-time",
		}, []string{"meta.tanvo_payable_id", "meta.tanvo_payment_request_id", "customizations.logo"}},
		// source: spec/services/payment_requests/payments/flutterwave_service_spec.rb:42
		// source: spec/services/payment_requests/payments/flutterwave_service_spec.rb:49
		// source: spec/services/payment_requests/payments/flutterwave_service_spec.rb:295
		{"payment request", payment.RequestPayable(testRequestID), map[string]string{
			"amount":                        "525.00",
			"tx_ref":                        "tanvo_payment_request_" + testRequestID.String(),
			"customizations.title":          "Test Organization - Payment Request",
			"customizations.description":    "Payment for invoices: INV-001, INV-002",
			"meta.tanvo_customer_id":        testCustomerID.String(),
			"meta.tanvo_payment_request_id": testRequestID.String(),
			"meta.tanvo_organization_id":    testOrgID.String(),
			"meta.tanvo_invoice_ids":        testInvoiceID.String() + "," + testInvoice2ID.String(),
			"meta.tanvo_payable_id":         testRequestID.String(),
			"meta.tanvo_payable_type":       "payment_request",
		}, []string{"meta.tanvo_invoice_id", "meta.payment_type", "customizations.logo"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routePayments: {fixture("create_checkout_response.json")}})
			got, err := rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), urlParams(tt.payable))
			if err != nil {
				t.Fatalf("CreatePaymentURL: %v", err)
			}
			if got.URL != testLink || got.SessionID != "" {
				t.Errorf("payment url: got = %+v, want = %s without a session", got, testLink)
			}
			r := rp.only(routePayments)
			for h, want := range map[string]string{
				"Authorization": "Bearer " + testSecretKey, "Content-Type": "application/json", "Accept": "application/json",
			} {
				if g := r.Header.Get(h); g != want {
					t.Errorf("header %s: got = %q, want = %q", h, g, want)
				}
			}
			common := map[string]string{
				"currency":                       "USD",
				"redirect_url":                   DefaultSuccessRedirectURL,
				"customer.email":                 "customer@example.com",
				"customer.name":                  "John Doe",
				"customer.phone_number":          "",
				"configuration.session_duration": "30",
			}
			for path, want := range common {
				if g := r.str(strings.Split(path, ".")...); g != want {
					t.Errorf("%s: got = %q, want = %q", path, g, want)
				}
			}
			for path, want := range tt.fields {
				if g := r.str(strings.Split(path, ".")...); g != want {
					t.Errorf("%s: got = %q, want = %q", path, g, want)
				}
			}
			for _, path := range tt.absent {
				if r.has(strings.Split(path, ".")...) {
					t.Errorf("%s: got = present, want = absent", path)
				}
			}
		})
	}
}

func TestCreatePaymentURLDefaults(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routePayments: {fixture("create_checkout_response.json")}})
	p := urlParams(payment.InvoicePayable(testInvoiceID))
	// No name: the email; a phone and a logo are sent; the connection's
	// success URL is the return URL.
	p.Customer = payment.CustomerParams{Email: "customer@example.com", Phone: " +2348000000000 "}
	p.OrganizationLogoURL = "https://tanvo.example/logo.png"
	p.Currency = ngn
	p.Lines[0].Amount = 1000000
	conn := connection(`{"success_redirect_url":"https://example.com/done"}`, `{"secret_key":"`+testSecretKey+`"}`)
	if _, err := rp.gateway().CreatePaymentURL(context.Background(), conn, p); err != nil {
		t.Fatalf("CreatePaymentURL: %v", err)
	}
	r := rp.only(routePayments)
	for path, want := range map[string]string{
		"customer.name":         "customer@example.com",
		"customer.phone_number": "+2348000000000",
		"customizations.logo":   "https://tanvo.example/logo.png",
		"redirect_url":          "https://example.com/done",
		"amount":                "10000.00",
		"currency":              "NGN",
	} {
		if g := r.str(strings.Split(path, ".")...); g != want {
			t.Errorf("%s: got = %q, want = %q", path, g, want)
		}
	}
}

func TestCreatePaymentURLFailures(t *testing.T) {
	// source: spec/services/invoices/payments/flutterwave_service_spec.rb:259
	// source: spec/services/payment_requests/payments/flutterwave_service_spec.rb:76
	rp := newReplay(t, map[string][]reply{routePayments: {raw(http.StatusInternalServerError, `Connection failed`)}})
	_, err := rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), urlParams(payment.InvoicePayable(testInvoiceID)))
	pe, ok := payment.AsProviderError(err)
	if !ok || pe.Kind != payment.KindUnavailable || pe.Code != "500" || pe.Provider != "flutterwave" || !errors.Is(err, payment.ErrRetryable) {
		t.Errorf("CreatePaymentURL: got = %v, want = unavailable 500", err)
	}

	rp = newReplay(t, map[string][]reply{routePayments: {raw(http.StatusBadRequest, `{"status":"error","message":"Invalid currency provided","data":null}`)}})
	_, err = rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), urlParams(payment.InvoicePayable(testInvoiceID)))
	if pe, ok := payment.AsProviderError(err); !ok || pe.Kind != payment.KindInvalidRequest || pe.Message != "Invalid currency provided" {
		t.Errorf("refused page: got = %v, want = invalid_request", err)
	}

	rp = newReplay(t, map[string][]reply{routePayments: {raw(http.StatusOK, `{"status":"success","data":{}}`)}})
	_, err = rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), urlParams(payment.InvoicePayable(testInvoiceID)))
	if pe, ok := payment.AsProviderError(err); !ok || pe.Kind != payment.KindPermanent {
		t.Errorf("no link: got = %v, want = permanent", err)
	}

	rp = newReplay(t, map[string][]reply{routePayments: {raw(http.StatusOK, `not json`)}})
	_, err = rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), urlParams(payment.InvoicePayable(testInvoiceID)))
	if !errors.Is(err, payment.ErrRetryable) {
		t.Errorf("undecodable answer: got = %v, want = retryable", err)
	}

	// Invalid parameters and a missing secret key make no request.
	rp = newReplay(t, nil)
	g := rp.gateway()
	p := urlParams(payment.InvoicePayable(testInvoiceID))
	p.Lines = nil
	var ve *core.ValidationError
	if _, err := g.CreatePaymentURL(context.Background(), defaultConn(), p); !errors.As(err, &ve) {
		t.Errorf("no amount: got = %v, want = a validation error", err)
	}
	_, err = g.CreatePaymentURL(context.Background(), connection(`{}`, `{}`), urlParams(payment.InvoicePayable(testInvoiceID)))
	if pe, ok := payment.AsProviderError(err); !ok || pe.Kind != payment.KindAuthentication {
		t.Errorf("no secret key: got = %v, want = authentication", err)
	}
	if rp.count() != 0 {
		t.Errorf("requests: got = %d, want = 0", rp.count())
	}
}

func TestUnsupportedCharges(t *testing.T) {
	g := New()
	ctx := context.Background()
	conn := defaultConn()
	for name, f := range map[string]func() error{
		"CreatePayment":          func() error { _, err := g.CreatePayment(ctx, conn, payment.PaymentParams{}); return err },
		"GetPayment":             func() error { _, err := g.GetPayment(ctx, conn, "p"); return err },
		"CancelPayment":          func() error { _, err := g.CancelPayment(ctx, conn, "p"); return err },
		"UpdatePaymentReference": func() error { return g.UpdatePaymentReference(ctx, conn, "p", "r") },
		"Authorize":              func() error { _, err := g.Authorize(ctx, conn, payment.AuthorizeParams{}); return err },
		"ExpirePaymentURL":       func() error { return g.ExpirePaymentURL(ctx, conn, "s") },
		"Refund":                 func() error { _, err := g.Refund(ctx, conn, payment.RefundParams{}); return err },
	} {
		if err := f(); !errors.Is(err, payment.ErrUnsupported) {
			t.Errorf("%s: got = %v, want = %v", name, err, payment.ErrUnsupported)
		}
	}
}
