package gocardless

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func refundParams() payment.RefundParams {
	return payment.RefundParams{
		CreditNoteID:      testCreditNoteID,
		InvoiceID:         testInvoiceID,
		CustomerID:        testCustomerID,
		ProviderPaymentID: "PM123",
		Amount:            134,
		Currency:          chf,
		Reason:            creditnote.ReasonOther,
	}
}

func TestRefundStatusOf(t *testing.T) {
	// source: app/services/credit_notes/refunds/gocardless_service.rb:9-11
	tests := map[string]core.RefundStatus{
		"created":            core.RefundStatusPending,
		"pending_submission": core.RefundStatusPending,
		"submitted":          core.RefundStatusPending,
		"refund_settled":     core.RefundStatusPending,
		"paid":               core.RefundStatusSucceeded,
		"cancelled":          core.RefundStatusFailed,
		"bounced":            core.RefundStatusFailed,
		"funds_returned":     core.RefundStatusFailed,
		"failed":             core.RefundStatusFailed,
	}
	for s, want := range tests {
		if got, ok := RefundStatusOf(s); !ok || got != want {
			t.Errorf("RefundStatusOf(%q): got = %q, %v, want = %q", s, got, ok, want)
		}
	}
	// source: spec/services/credit_notes/refunds/gocardless_service_spec.rb:315
	if _, ok := RefundStatusOf("invalid"); ok {
		t.Errorf("RefundStatusOf(invalid): got = mapped, want = unknown")
	}
}

func TestRefund(t *testing.T) {
	// source: spec/services/credit_notes/refunds/gocardless_service_spec.rb:56
	// source: spec/services/credit_notes/refunds/gocardless_service_spec.rb:105
	rp := newReplay(t, map[string][]reply{routeRefunds: {created("refunds_response.json")}})
	res, err := rp.gateway().Refund(context.Background(), defaultConn(), refundParams())
	if err != nil {
		t.Fatalf("Refund: got = %v, want = nil", err)
	}
	want := payment.RefundResult{
		ProviderRefundID: "re_123456", ProviderStatus: "paid", Status: core.RefundStatusSucceeded,
		Amount: 134, Currency: chf,
	}
	if res != want {
		t.Errorf("result: got = %+v, want = %+v", res, want)
	}
	req := rp.only(routeRefunds)
	if got := req.str("refunds", "amount"); got != "134" {
		t.Errorf("amount: got = %q, want = 134", got)
	}
	if got := req.str("refunds", "total_amount_confirmation"); got != "134" {
		t.Errorf("total_amount_confirmation: got = %q, want = 134", got)
	}
	if got := req.str("refunds", "links", "payment"); got != "PM123" {
		t.Errorf("links.payment: got = %q, want = PM123", got)
	}
	wantMeta := map[string]any{
		"tanvo_credit_note_id": testCreditNoteID.String(),
		"tanvo_invoice_id":     testInvoiceID.String(),
		"reason":               "other",
	}
	if got := req.Body["refunds"].(map[string]any)["metadata"]; !reflect.DeepEqual(got, wantMeta) {
		t.Errorf("metadata: got = %v, want = %v", got, wantMeta)
	}
	if req.IdempotencyKey != testCreditNoteID.String() {
		t.Errorf("Idempotency-Key: got = %q, want = the credit note ID", req.IdempotencyKey)
	}
}

func TestRefundErrors(t *testing.T) {
	const src = "spec/services/credit_notes/refunds/gocardless_service_spec.rb:"
	tests := []struct {
		sources   []string
		reply     reply
		rejected  bool
		retryable bool
		code      string
	}{
		{[]string{src + "151"}, failFixture(http.StatusUnprocessableEntity, "validation_failed_response.json"), true, false, "mandate_is_inactive"},
		{[]string{src + "121", src + "137"}, failFixture(http.StatusInternalServerError, "server_error_response.json"), false, true, "internal_server_error"},
	}
	for _, tt := range tests {
		rp := newReplay(t, map[string][]reply{routeRefunds: {tt.reply}})
		_, err := rp.gateway().Refund(context.Background(), defaultConn(), refundParams())
		pe, ok := payment.AsProviderError(err)
		if !ok || errors.Is(err, payment.ErrRejected) != tt.rejected || errors.Is(err, payment.ErrRetryable) != tt.retryable || pe.Code != tt.code {
			t.Errorf("%v: error: got = %v, want = rejected %v, retryable %v, code %s", tt.sources, err, tt.rejected, tt.retryable, tt.code)
		}
	}
}

func TestRefundConflict(t *testing.T) {
	rp := newReplay(t, map[string][]reply{
		routeRefunds:             {failFixture(http.StatusConflict, "refund_conflict_response.json")},
		"GET /refunds/re_123456": {fixture("refunds_response.json")},
	})
	res, err := rp.gateway().Refund(context.Background(), defaultConn(), refundParams())
	if err != nil || res.ProviderRefundID != "re_123456" {
		t.Errorf("Refund: got = %+v, %v, want = the existing refund", res, err)
	}
}

func TestRefundUnknownStatus(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routeRefunds: {raw(http.StatusCreated, `{"refunds":{"id":"re_1","amount":0,"currency":"","status":"mystery"}}`)}})
	res, err := rp.gateway().Refund(context.Background(), defaultConn(), refundParams())
	if !errors.Is(err, payment.ErrUnknownStatus) || res.ProviderRefundID != "re_1" || res.ProviderStatus != "mystery" {
		t.Errorf("Refund: got = %+v, %v, want = the refund with %v", res, err, payment.ErrUnknownStatus)
	}
	if res.Amount != 134 || res.Currency != chf {
		t.Errorf("amount: got = %d %v, want = the requested amount", res.Amount, res.Currency)
	}
}

func TestRefundInvalid(t *testing.T) {
	// source: spec/services/credit_notes/refunds/gocardless_service_spec.rb:177
	rp := newReplay(t, map[string][]reply{})
	p := refundParams()
	p.Amount = 0
	var ve *core.ValidationError
	if _, err := rp.gateway().Refund(context.Background(), defaultConn(), p); !errors.As(err, &ve) {
		t.Errorf("error: got = %v, want = a validation error", err)
	}
	if rp.count() != 0 {
		t.Errorf("requests: got = %d, want = 0", rp.count())
	}
}
