package stripe

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

const routeRefunds = "POST /v1/refunds"

func TestRefundReasonOf(t *testing.T) {
	// source: app/services/credit_notes/refunds/stripe_service.rb stripe_reason
	tests := []struct {
		reason creditnote.Reason
		want   string
	}{
		{creditnote.ReasonDuplicatedCharge, "duplicate"},
		{creditnote.ReasonProductUnsatisfactory, "requested_by_customer"},
		{creditnote.ReasonOrderChange, "requested_by_customer"},
		{creditnote.ReasonOrderCancellation, "requested_by_customer"},
		{creditnote.ReasonFraudulentCharge, "fraudulent"},
		{creditnote.ReasonOther, ""},
	}
	for _, tt := range tests {
		if got := RefundReasonOf(tt.reason); got != tt.want {
			t.Errorf("RefundReasonOf(%q): got = %q, want = %q", tt.reason, got, tt.want)
		}
	}
}

func TestRefundStatusOf(t *testing.T) {
	// source: spec/services/credit_notes/refunds/stripe_service_spec.rb:285,360,378
	tests := []struct {
		status  string
		want    core.RefundStatus
		wantErr error
	}{
		{"succeeded", core.RefundStatusSucceeded, nil},
		{"pending", core.RefundStatusPending, nil},
		{"requires_action", core.RefundStatusPending, nil},
		{"failed", core.RefundStatusFailed, nil},
		{"canceled", core.RefundStatusFailed, nil},
		{"invalid", "", payment.ErrUnknownStatus},
		{"", "", payment.ErrUnknownStatus},
	}
	for _, tt := range tests {
		got, err := RefundStatusOf(tt.status)
		if got != tt.want || !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
			t.Errorf("RefundStatusOf(%q): got = %q, %v, want = %q, %v", tt.status, got, err, tt.want, tt.wantErr)
		}
		if err != nil && (errors.Is(err, payment.ErrRejected) || errors.Is(err, payment.ErrRetryable)) {
			t.Errorf("RefundStatusOf(%q): class: got = %v, want = neither rejected nor retryable", tt.status, err)
		}
	}
}

func refundParams() payment.RefundParams {
	return payment.RefundParams{
		CreditNoteID:      testCreditNoteID,
		InvoiceID:         testInvoiceID,
		CustomerID:        testCustomerID,
		ProviderPaymentID: "pi_123456",
		Amount:            134,
		Currency:          money.MustCurrency("CHF"),
		Reason:            creditnote.ReasonDuplicatedCharge,
	}
}

func TestRefund(t *testing.T) {
	tests := []struct {
		source   string
		name     string
		edit     func(p *payment.RefundParams)
		reply    reply
		want     payment.RefundResult
		form     map[string]string
		wantKind payment.ErrorKind
		wantErr  error
		noCall   bool
	}{
		{
			source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:52",
			name:   "creates the refund",
			reply:  ok(`{"id":"re_123456","object":"refund","status":"succeeded","amount":134,"currency":"chf"}`),
			want: payment.RefundResult{
				ProviderRefundID: "re_123456", ProviderStatus: "succeeded", Status: core.RefundStatusSucceeded,
				Amount: 134, Currency: money.MustCurrency("CHF"),
			},
			form: map[string]string{
				"payment_intent":                 "pi_123456",
				"amount":                         "134",
				"reason":                         "duplicate",
				"metadata[tanvo_customer_id]":    testCustomerID.String(),
				"metadata[tanvo_credit_note_id]": testCreditNoteID.String(),
				"metadata[tanvo_invoice_id]":     testInvoiceID.String(),
			},
		},
		{
			source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:105",
			name:   "a pending refund of an other reason",
			edit:   func(p *payment.RefundParams) { p.Reason = creditnote.ReasonOther },
			reply:  ok(`{"id":"re_123456","object":"refund","status":"pending","amount":134,"currency":"chf"}`),
			want: payment.RefundResult{
				ProviderRefundID: "re_123456", ProviderStatus: "pending", Status: core.RefundStatusPending,
				Amount: 134, Currency: money.MustCurrency("CHF"),
			},
			form: map[string]string{"reason": ""},
		},
		{
			source:   "spec/services/credit_notes/refunds/stripe_service_spec.rb:151",
			name:     "Stripe refuses the refund",
			reply:    fail(http.StatusBadRequest, "invalid_request_error", "error", "error"),
			wantKind: payment.KindInvalidRequest,
			wantErr:  payment.ErrRejected,
		},
		{
			source:   "spec/services/credit_notes/refunds/stripe_service_spec.rb:179",
			name:     "a non-refundable payment method",
			reply:    fail(http.StatusBadRequest, "invalid_request_error", "charge_not_refundable", "charge_not_refundable"),
			wantKind: payment.KindInvalidRequest,
			wantErr:  payment.ErrRejected,
		},
		{
			source:   "app/services/credit_notes/refunds/stripe_service.rb (other errors raise and retry)",
			name:     "a rate limit is retried",
			reply:    fail(http.StatusTooManyRequests, "invalid_request_error", "rate_limit", "Too many requests"),
			wantKind: payment.KindRateLimited,
			wantErr:  payment.ErrRetryable,
		},
		{
			source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:360",
			name:   "an unknown status keeps the refund but not its status",
			reply:  ok(`{"id":"re_123456","object":"refund","status":"invalid","amount":134,"currency":"chf"}`),
			want: payment.RefundResult{
				ProviderRefundID: "re_123456", ProviderStatus: "invalid",
				Amount: 134, Currency: money.MustCurrency("CHF"),
			},
			wantErr: payment.ErrUnknownStatus,
		},
		{
			source:  "libs/core/payment RefundParams: the credit note ID is the mandatory idempotency key",
			name:    "no credit note",
			edit:    func(p *payment.RefundParams) { p.CreditNoteID = core.CreditNoteID{} },
			wantErr: core.ErrInvalidValue,
			noCall:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, map[string][]reply{routeRefunds: {tt.reply}})
			p := refundParams()
			if tt.edit != nil {
				tt.edit(&p)
			}
			got, err := rp.gateway().Refund(context.Background(), connection(`{}`), p)
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
			}
			if k := payment.ErrorKindOf(err); k != tt.wantKind {
				t.Fatalf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
			}
			if tt.wantErr == nil && err != nil {
				t.Fatalf("Refund: got = %v, want = nil", err)
			}
			if tt.noCall {
				if rp.count() != 0 {
					t.Errorf("requests: got = %d, want = 0", rp.count())
				}
				return
			}
			req := rp.only(routeRefunds)
			if req.IdempotencyKey != testCreditNoteID.String() {
				t.Errorf("Idempotency-Key: got = %q, want = the credit note ID", req.IdempotencyKey)
			}
			if err != nil && !errors.Is(err, payment.ErrUnknownStatus) {
				return
			}
			if got != tt.want {
				t.Errorf("result: got = %+v, want = %+v", got, tt.want)
			}
			checkForm(t, req.Form, tt.form)
		})
	}
}

func TestFundingInstructions(t *testing.T) {
	const route = "POST /v1/customers/cus_123/funding_instructions"
	const body = `{"object":"funding_instructions","bank_transfer":{"country":"DE","type":"eu_bank_transfer","financial_addresses":[]},"currency":"eur","funding_type":"bank_transfer"}`
	tests := []struct {
		source          string
		name            string
		currency        money.Currency
		customerCountry string
		entityCountry   string
		form            map[string]string
		wantErr         error
	}{
		{
			source: "spec/services/payment_provider_customers/stripe/sync_funding_instructions_service_spec.rb:49", name: "USD",
			currency: usd, customerCountry: "US",
			form: map[string]string{"funding_type": "bank_transfer", "currency": "usd", "bank_transfer[type]": "us_bank_transfer", "bank_transfer[eu_bank_transfer][country]": ""},
		},
		{
			source: "spec/services/payment_provider_customers/stripe/sync_funding_instructions_service_spec.rb:77", name: "EUR with the billing entity's country",
			currency: eur, customerCountry: "US", entityCountry: "DE",
			form: map[string]string{"currency": "eur", "bank_transfer[type]": "eu_bank_transfer", "bank_transfer[eu_bank_transfer][country]": "DE"},
		},
		{
			source: "app/services/payment_provider_customers/stripe/sync_funding_instructions_service.rb", name: "EUR without a supported country",
			currency: eur, customerCountry: "US", entityCountry: "US", wantErr: payment.ErrMissingCountry,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, map[string][]reply{route: {ok(body)}})
			got, err := rp.gateway().FundingInstructions(context.Background(), connection(`{}`), "cus_123", tt.currency, tt.customerCountry, tt.entityCountry)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
				}
				if rp.count() != 0 {
					t.Errorf("requests: got = %d, want = 0", rp.count())
				}
				return
			}
			if err != nil {
				t.Fatalf("FundingInstructions: got = %v, want = nil", err)
			}
			if string(got) != body {
				t.Errorf("instructions: got = %s, want = %s", got, body)
			}
			checkForm(t, rp.only(route).Form, tt.form)
		})
	}
}
