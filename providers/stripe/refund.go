package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	stripe "github.com/stripe/stripe-go/v84"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// RefundReasonOf maps a credit note reason to Stripe's refund reason; ""
// sends none (lago credit_notes/refunds/stripe_service.rb stripe_reason).
func RefundReasonOf(r creditnote.Reason) string {
	switch r {
	case creditnote.ReasonDuplicatedCharge:
		return "duplicate"
	case creditnote.ReasonProductUnsatisfactory, creditnote.ReasonOrderChange, creditnote.ReasonOrderCancellation:
		return "requested_by_customer"
	case creditnote.ReasonFraudulentCharge:
		return "fraudulent"
	default:
		return ""
	}
}

// RefundStatusOf maps a Stripe refund status. Lago copies Stripe's status
// to the credit note, whose refund_status enum accepts only pending,
// succeeded and failed (credit_notes/refunds/stripe_service.rb
// update_status, credit_note.rb:62), so a requires_action or canceled
// refund fails validation and never updates; Tanvo keeps requires_action
// pending and treats canceled as failed, its final outcome. Any other
// status is [payment.ErrUnknownStatus], as lago's validation failure
// (credit_notes/refunds/stripe_service_spec.rb:360): mapping it to
// pending could hide a final state.
func RefundStatusOf(s string) (core.RefundStatus, error) {
	switch s {
	case "succeeded":
		return core.RefundStatusSucceeded, nil
	case "pending", "requires_action":
		return core.RefundStatusPending, nil
	case "failed", "canceled":
		return core.RefundStatusFailed, nil
	default:
		return "", fmt.Errorf("stripe: refund status %q: %w", s, payment.ErrUnknownStatus)
	}
}

// Refund implements [payment.RefundGateway] (lago
// credit_notes/refunds/stripe_service.rb): a refund of the PaymentIntent
// keyed by the credit note ID. Stripe refusing the request
// (InvalidRequestError, charge_not_refundable included) matches
// [payment.ErrRejected]; lago marks the refund failed for each.
func (g *Gateway) Refund(ctx context.Context, conn payment.Connection, p payment.RefundParams) (payment.RefundResult, error) {
	var res payment.RefundResult
	if err := p.Validate(); err != nil {
		return res, err
	}
	var statusErr error
	err := g.do(ctx, conn, "refund", func(ctx context.Context, c call) error {
		params := &stripe.RefundCreateParams{
			PaymentIntent: stripe.String(p.ProviderPaymentID),
			Amount:        stripe.Int64(p.Amount),
			Reason:        optional(RefundReasonOf(p.Reason)),
			Metadata: map[string]string{
				metaCustomerID:   p.CustomerID.String(),
				metaCreditNoteID: p.CreditNoteID.String(),
				metaInvoiceID:    p.InvoiceID.String(),
			},
		}
		params.SetIdempotencyKey(p.IdempotencyKey())
		r, err := c.client.V1Refunds.Create(ctx, params)
		if err != nil {
			return err
		}
		res = payment.RefundResult{
			ProviderRefundID: r.ID,
			ProviderStatus:   string(r.Status),
			Amount:           r.Amount,
			Currency:         currencyOf(string(r.Currency)),
		}
		res.Status, statusErr = RefundStatusOf(string(r.Status))
		return nil
	})
	if err != nil {
		return res, err
	}
	// The refund exists at Stripe: the result keeps its identifiers, and
	// the caller leaves the refund status as it is until charge.refund.updated.
	return res, statusErr
}

// FundingInstructions implements [payment.FundingGateway] (lago
// payment_provider_customers/stripe/sync_funding_instructions_service.rb):
// the bank details of the customer balance in currency, as Stripe's
// funding instructions object.
func (g *Gateway) FundingInstructions(ctx context.Context, conn payment.Connection, providerCustomerID string, currency money.Currency, customerCountry, billingEntityCountry string) (json.RawMessage, error) {
	var out json.RawMessage
	err := g.do(ctx, conn, "funding_instructions", func(ctx context.Context, c call) error {
		typ, eu, err := transferType(currency.Code(), customerCountry, billingEntityCountry)
		if err != nil {
			return err
		}
		bt := &stripe.CustomerCreateFundingInstructionsBankTransferParams{Type: optional(typ)}
		if eu != "" {
			bt.EUBankTransfer = &stripe.CustomerCreateFundingInstructionsBankTransferEUBankTransferParams{Country: stripe.String(eu)}
		}
		fi, err := c.client.V1Customers.CreateFundingInstructions(ctx, providerCustomerID, &stripe.CustomerCreateFundingInstructionsParams{
			FundingType:  stripe.String("bank_transfer"),
			BankTransfer: bt,
			Currency:     stripe.String(strings.ToLower(currency.Code())),
		})
		if err != nil {
			return err
		}
		if fi.LastResponse != nil {
			out = json.RawMessage(fi.LastResponse.RawJSON)
		}
		return nil
	})
	return out, err
}

// currencyOf parses Stripe's lower-case currency; zero when unknown.
func currencyOf(s string) money.Currency {
	c, err := money.LookupCurrency(strings.ToUpper(s))
	if err != nil {
		return money.Currency{}
	}
	return c
}
