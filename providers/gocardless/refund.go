package gocardless

import (
	"context"
	"slices"
	"strings"

	gc "github.com/gocardless/gocardless-pro-go/v6"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Refund statuses grouped by Tanvo refund state (lago
// credit_notes/refunds/gocardless_service.rb PENDING_STATUSES,
// SUCCESS_STATUSES, FAILED_STATUSES).
var (
	refundPendingStatuses   = []string{"created", "pending_submission", "submitted", "refund_settled"}
	refundSucceededStatuses = []string{"paid"}
	refundFailedStatuses    = []string{"cancelled", "bounced", "funds_returned", "failed"}
)

// RefundStatusOf maps a GoCardless refund status, or the action of a
// refund event, to a refund state; ok is false for an unknown one, which
// lago stores as is (gocardless_service.rb credit_note_status).
func RefundStatusOf(status string) (core.RefundStatus, bool) {
	switch {
	case slices.Contains(refundPendingStatuses, status):
		return core.RefundStatusPending, true
	case slices.Contains(refundSucceededStatuses, status):
		return core.RefundStatusSucceeded, true
	case slices.Contains(refundFailedStatuses, status):
		return core.RefundStatusFailed, true
	default:
		return "", false
	}
}

// Refund implements [payment.RefundGateway] (lago
// credit_notes/refunds/gocardless_service.rb create_gocardless_refund): a
// refund of the payment confirming the refunded total, keyed by the
// credit note ID as in lago, with the credit note, the invoice and the
// reason as metadata (GoCardless accepts three keys). A refund already
// created with the key is returned. An unknown status is
// [payment.ErrUnknownStatus] with the result.
//
// Lago fails the refund on ValidationError and on other errors fails it
// and re-raises (gocardless_service.rb:48-57). Here a 4xx refusal matches
// [payment.ErrRejected] and fails the refund, while authentication
// failures, rate limits and 5xx answers are retried with the same key.
func (g *Gateway) Refund(ctx context.Context, conn payment.Connection, p payment.RefundParams) (payment.RefundResult, error) {
	var res payment.RefundResult
	if err := p.Validate(); err != nil {
		return res, err
	}
	var unknown bool
	err := g.do(ctx, conn, "refund", func(ctx context.Context, c call) error {
		r, err := c.svc.Refunds.Create(ctx, gc.RefundCreateParams{
			Amount:                  int(p.Amount),
			TotalAmountConfirmation: int(p.Amount),
			Links:                   gc.RefundCreateParamsLinks{Payment: p.ProviderPaymentID},
			Metadata: map[string]string{
				metaCreditNoteID: p.CreditNoteID.String(),
				metaInvoiceID:    p.InvoiceID.String(),
				metaReason:       string(p.Reason),
			},
		}, gc.WithIdempotencyKey(p.IdempotencyKey()), noRetries())
		if existing := conflictingResource(err); existing != "" {
			r, err = c.svc.Refunds.Get(ctx, existing, noRetries())
		}
		if err != nil {
			return c.sdk(err)
		}
		status, ok := RefundStatusOf(r.Status)
		unknown = !ok
		res = payment.RefundResult{
			ProviderRefundID: r.Id,
			ProviderStatus:   r.Status,
			Status:           status,
			Amount:           int64(r.Amount),
			Currency:         currencyOf(r.Currency),
		}
		if res.Amount == 0 {
			res.Amount, res.Currency = p.Amount, p.Currency
		}
		return nil
	})
	if err == nil && unknown {
		err = payment.ErrUnknownStatus
	}
	return res, err
}

// currencyOf parses an ISO currency code; zero when unknown.
func currencyOf(s string) money.Currency {
	c, err := money.LookupCurrency(strings.ToUpper(s))
	if err != nil {
		return money.Currency{}
	}
	return c
}
