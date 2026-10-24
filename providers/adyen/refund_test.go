package adyen

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

const routeRefund = "POST /payments/PSP_AUTHORISED_1/refunds"

func refundParams() payment.RefundParams {
	return payment.RefundParams{
		CreditNoteID:      testCreditNoteID,
		InvoiceID:         testInvoiceID,
		CustomerID:        testCustomerID,
		ProviderPaymentID: "PSP_AUTHORISED_1",
		Amount:            134,
		Currency:          chf,
	}
}

func TestRefund(t *testing.T) {
	// source: spec/services/credit_notes/refunds/adyen_service_spec.rb:55
	// source: spec/services/credit_notes/refunds/adyen_service_spec.rb:105
	rp := newReplay(t, map[string][]reply{routeRefund: {fixture("refunds_response.json")}})
	res, err := rp.gateway().Refund(context.Background(), defaultConn(), refundParams())
	if err != nil {
		t.Fatalf("Refund: got = %v, want = nil", err)
	}
	want := payment.RefundResult{ProviderRefundID: "REFUND_PSP_1", ProviderStatus: "received", Status: core.RefundStatusPending, Amount: 134, Currency: chf}
	if res != want {
		t.Errorf("result: got = %+v, want = %+v", res, want)
	}
	r := rp.only(routeRefund)
	if r.str("merchantAccount") != testMerchantAccount || r.str("amount") != `{"currency":"CHF","value":134}` {
		t.Errorf("body: got = %v, want = the merchant account and 134 CHF", r.Body)
	}
	if r.IdempotencyKey != testCreditNoteID.String() {
		t.Errorf("Idempotency-Key: got = %q, want = the credit note ID", r.IdempotencyKey)
	}
	if r.str("applicationInfo", "merchantApplication", "name") != "Tanvo" {
		t.Errorf("applicationInfo: got = %s, want = Tanvo", r.str("applicationInfo"))
	}
}

func TestRefundAmountFallback(t *testing.T) {
	// Adyen answers without an amount: the requested one is kept.
	rp := newReplay(t, map[string][]reply{routeRefund: {ok(`{"pspReference":"REFUND_PSP_2","status":"received"}`)}})
	res, err := rp.gateway().Refund(context.Background(), defaultConn(), refundParams())
	if err != nil || res.Amount != 134 || res.Currency != chf {
		t.Errorf("result: got = %+v (%v), want = 134 CHF", res, err)
	}
}

func TestRefundErrors(t *testing.T) {
	tests := []struct {
		source   string
		reply    reply
		wantErr  error
		wantKind payment.ErrorKind
	}{
		// The refund fails on a refusal and is retried otherwise.
		{"spec/services/credit_notes/refunds/adyen_service_spec.rb:121", fail(http.StatusUnprocessableEntity, "validation", "137", "Invalid amount specified"), payment.ErrRejected, payment.KindInvalidRequest},
		{"app/services/credit_notes/refunds/adyen_service.rb:100 (authentication)", fail(http.StatusUnauthorized, "security", "000", "HTTP Status Response - Unauthorized"), payment.ErrAuthentication, payment.KindAuthentication},
		{"app/services/credit_notes/refunds/adyen_service.rb:100 (server error)", fail(http.StatusInternalServerError, "internal", "901", "boom"), payment.ErrRetryable, payment.KindUnavailable},
	}
	for _, tt := range tests {
		rp := newReplay(t, map[string][]reply{routeRefund: {tt.reply}})
		_, err := rp.gateway().Refund(context.Background(), defaultConn(), refundParams())
		if !errors.Is(err, tt.wantErr) || payment.ErrorKindOf(err) != tt.wantKind {
			t.Errorf("%s: error: got = %v (%q), want = %v (%q)", tt.source, err, payment.ErrorKindOf(err), tt.wantErr, tt.wantKind)
		}
	}

	// source: spec/services/credit_notes/refunds/adyen_service_spec.rb:155
	none := newReplay(t, map[string][]reply{})
	p := refundParams()
	p.Amount = 0
	var ve *core.ValidationError
	if _, err := none.gateway().Refund(context.Background(), defaultConn(), p); !errors.As(err, &ve) || none.count() != 0 {
		t.Errorf("zero amount: got = %v with %d requests, want = a validation error and none", err, none.count())
	}
}
