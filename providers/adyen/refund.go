package adyen

import (
	"context"
	"strings"

	"github.com/adyen/adyen-go-api-library/v21/src/checkout"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Refund implements [payment.RefundGateway] (lago
// credit_notes/refunds/adyen_service.rb): a refund of the captured
// payment, pending until its REFUND notification.
//
// The request is keyed by the credit note ID, which lago does not send
// (adyen_service.rb:95-99): the port makes the key mandatory so a retried
// job never refunds twice. Lago marks the refund failed on any AdyenError
// (adyen_service.rb:100-106), which adyen-ruby raises for 401 and 403
// only; other refusals reach lago as an answer without a PSP reference.
// Here a 4xx refusal matches [payment.ErrRejected] and fails the refund,
// while authentication failures, rate limits and 5xx answers are retried,
// as for every provider ([payment.ErrRejected]).
func (g *Gateway) Refund(ctx context.Context, conn payment.Connection, p payment.RefundParams) (payment.RefundResult, error) {
	var res payment.RefundResult
	if err := p.Validate(); err != nil {
		return res, err
	}
	err := g.do(ctx, conn, "refund", func(ctx context.Context, c call) error {
		api := c.checkout.ModificationsApi
		r, hr, err := api.RefundCapturedPayment(ctx, api.RefundCapturedPaymentInput(p.ProviderPaymentID).
			IdempotencyKey(p.IdempotencyKey()).
			PaymentRefundRequest(checkout.PaymentRefundRequest{
				MerchantAccount: c.settings.MerchantAccount,
				Amount:          checkout.Amount{Currency: p.Currency.Code(), Value: p.Amount},
				ApplicationInfo: applicationInfo(),
			}))
		if err != nil {
			return sdkErr(hr, err)
		}
		res = payment.RefundResult{
			ProviderRefundID: r.PspReference,
			ProviderStatus:   r.Status,
			// Adyen acknowledges ("received") and reports the outcome
			// with a notification; lago stores the refund pending
			// (adyen_service.rb:32).
			Status:   core.RefundStatusPending,
			Amount:   r.Amount.Value,
			Currency: currencyOf(r.Amount.Currency),
		}
		if res.Amount == 0 {
			res.Amount, res.Currency = p.Amount, p.Currency
		}
		return nil
	})
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
