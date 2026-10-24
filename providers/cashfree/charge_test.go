package cashfree

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

func urlParams(p payment.Payable) payment.PaymentURLParams {
	return payment.PaymentURLParams{
		IntentID:    testIntentID,
		Payable:     p,
		IssuingDate: core.NewDate(2023, time.January, 1),
		CustomerID:  testCustomerID,
		Lines:       []payment.PaymentURLLine{{Name: "INV-1", Amount: 20000}, {Name: "INV-2", Amount: 12}},
		Currency:    inr,
		ExpiresAt:   time.Date(2023, 1, 1, 18, 30, 0, 0, time.FixedZone("IST", 5*3600+1800)),
		Customer: payment.CustomerParams{
			CustomerID: testCustomerID, Name: "John", Email: "john@gmail.com", Phone: "9000000000",
		},
	}
}

func TestStatusOf(t *testing.T) {
	for status, want := range map[string]payment.State{
		"PARTIALLY_PAID": payment.StateProcessing,
		"PAID":           payment.StateSucceeded,
		"EXPIRED":        payment.StateFailed,
		"CANCELLED":      payment.StateFailed,
		"ACTIVE":         payment.StatePending,
	} {
		if got := StatusOf(status); got != want {
			t.Errorf("StatusOf(%q): got = %v, want = %v", status, got, want)
		}
	}
}

func TestAmountOf(t *testing.T) {
	for _, tt := range []struct {
		minor int64
		cur   string
		want  string
	}{{20012, "INR", "200.12"}, {500, "INR", "5.00"}, {1, "INR", "0.01"}, {500, "JPY", "500"}, {1500, "KWD", "1.500"}} {
		c := inr
		switch tt.cur {
		case "JPY":
			c = jpy
		case "KWD":
			c = kwd
		}
		if got := amountOf(tt.minor, c); string(got) != tt.want {
			t.Errorf("amountOf(%d %s): got = %s, want = %s", tt.minor, tt.cur, got, tt.want)
		}
	}
}

func TestCreatePaymentURL(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payable payment.Payable
		notes   map[string]string
		absent  []string
	}{
		// source: spec/services/invoices/payments/cashfree_service_spec.rb:239
		// source: spec/services/invoices/payments/cashfree_service_spec.rb:245
		// source: spec/services/invoices/payments/cashfree_service_spec.rb:270
		{"invoice", payment.InvoicePayable(testInvoiceID), map[string]string{
			"tanvo_customer_id":    testCustomerID.String(),
			"tanvo_invoice_id":     testInvoiceID.String(),
			"invoice_issuing_date": "2023-01-01",
			"payment_type":         "one-time",
		}, []string{"tanvo_payable_id", "tanvo_payable_type", "payment_issuing_date"}},
		// source: spec/services/payment_requests/payments/cashfree_service_spec.rb:329
		{"payment request", payment.RequestPayable(testRequestID), map[string]string{
			"tanvo_customer_id":    testCustomerID.String(),
			"tanvo_payable_id":     testRequestID.String(),
			"tanvo_payable_type":   "payment_request",
			"payment_issuing_date": "2023-01-01",
			"payment_type":         "one-time",
		}, []string{"tanvo_invoice_id", "invoice_issuing_date"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routeLinks: {fixture("create_link_response.json")}})
			got, err := rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), urlParams(tt.payable))
			if err != nil {
				t.Fatalf("CreatePaymentURL: %v", err)
			}
			if got.URL != testLinkURL || got.SessionID != testLinkID {
				t.Errorf("payment url: got = %+v, want = %s %s", got, testLinkURL, testLinkID)
			}
			r := rp.only(routeLinks)
			for h, want := range map[string]string{
				"x-client-id": testClientID, "x-client-secret": testClientSecret, "x-api-version": "2023-08-01",
			} {
				if g := r.Header.Get(h); g != want {
					t.Errorf("header %s: got = %q, want = %q", h, g, want)
				}
			}
			payableID := tt.payable.InvoiceID.String()
			if tt.payable.Type == payment.PayableTypePaymentRequest {
				payableID = tt.payable.PaymentRequestID.String()
			}
			for path, want := range map[string]string{
				"customer_details.customer_phone": "9000000000",
				"customer_details.customer_email": "john@gmail.com",
				"customer_details.customer_name":  "John",
				"link_notify.send_sms":            "false",
				"link_notify.send_email":          "false",
				"link_meta.upi_intent":            "true",
				"link_meta.return_url":            DefaultSuccessRedirectURL,
				"link_id":                         testLinkID,
				"link_amount":                     "200.12",
				"link_currency":                   "INR",
				"link_purpose":                    payableID,
				"link_expiry_time":                "2023-01-01T13:00:00Z",
				"link_partial_payments":           "false",
				"link_auto_reminders":             "false",
			} {
				if g := r.str(strings.Split(path, ".")...); g != want {
					t.Errorf("%s: got = %q, want = %q", path, g, want)
				}
			}
			for k, want := range tt.notes {
				if g := r.str("link_notes", k); g != want {
					t.Errorf("link_notes.%s: got = %q, want = %q", k, g, want)
				}
			}
			for _, k := range tt.absent {
				if r.has("link_notes", k) {
					t.Errorf("link_notes.%s: got = present, want = absent", k)
				}
			}
		})
	}
}

func TestCreatePaymentURLDefaults(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routeLinks: {raw(http.StatusOK, `{"link_url":"`+testLinkURL+`"}`)}})
	p := urlParams(payment.RequestPayable(testRequestID))
	// No expiry: 10 minutes; no phone: the placeholder; no email or name:
	// absent; no issuing date: no date note.
	p.ExpiresAt = time.Time{}
	p.IssuingDate = core.Date{}
	p.Customer = payment.CustomerParams{CustomerID: testCustomerID}
	conn := connection(`{"success_redirect_url":"https://example.com/done"}`,
		`{"client_id":"`+testClientID+`","client_secret":"`+testClientSecret+`"}`)
	got, err := rp.gateway().CreatePaymentURL(context.Background(), conn, p)
	if err != nil {
		t.Fatalf("CreatePaymentURL: %v", err)
	}
	// The answer has no link ID: the requested one is the session.
	if got.SessionID != testLinkID {
		t.Errorf("session: got = %q, want = %q", got.SessionID, testLinkID)
	}
	r := rp.only(routeLinks)
	for path, want := range map[string]string{
		"customer_details.customer_phone": defaultPhone,
		"link_expiry_time":                "2023-01-01T12:10:00Z",
		"link_meta.return_url":            "https://example.com/done",
	} {
		if g := r.str(strings.Split(path, ".")...); g != want {
			t.Errorf("%s: got = %q, want = %q", path, g, want)
		}
	}
	for _, path := range []string{"customer_details.customer_email", "customer_details.customer_name", "link_notes.payment_issuing_date"} {
		if r.has(strings.Split(path, ".")...) {
			t.Errorf("%s: got = present, want = absent", path)
		}
	}
}

func TestCreatePaymentURLFailures(t *testing.T) {
	// source: spec/services/invoices/payments/cashfree_service_spec.rb:295
	// source: spec/services/payment_requests/payments/cashfree_service_spec.rb:352
	rp := newReplay(t, map[string][]reply{routeLinks: {failFixture(http.StatusBadRequest, "create_link_failure_response.json")}})
	_, err := rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), urlParams(payment.InvoicePayable(testInvoiceID)))
	pe, ok := payment.AsProviderError(err)
	if !ok || pe.Kind != payment.KindInvalidRequest || pe.Code != "400" ||
		pe.Message != "Currency USD is not enabled (link_post_failed)" || pe.Provider != "cashfree" {
		t.Errorf("CreatePaymentURL: got = %v, want = invalid_request 400", err)
	}

	rp = newReplay(t, map[string][]reply{routeLinks: {raw(http.StatusOK, `{"link_id":"x"}`)}})
	_, err = rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), urlParams(payment.InvoicePayable(testInvoiceID)))
	if pe, ok := payment.AsProviderError(err); !ok || pe.Kind != payment.KindPermanent {
		t.Errorf("no link url: got = %v, want = permanent", err)
	}

	rp = newReplay(t, map[string][]reply{routeLinks: {raw(http.StatusOK, `not json`)}})
	_, err = rp.gateway().CreatePaymentURL(context.Background(), defaultConn(), urlParams(payment.InvoicePayable(testInvoiceID)))
	if !errors.Is(err, payment.ErrRetryable) {
		t.Errorf("undecodable answer: got = %v, want = retryable", err)
	}

	// Invalid parameters and missing credentials make no request.
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
		t.Errorf("no credentials: got = %v, want = authentication", err)
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
		// source: spec/services/payment_providers/cashfree/payments/create_service_spec.rb:44
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
