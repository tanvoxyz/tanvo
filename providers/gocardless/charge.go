package gocardless

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	gc "github.com/gocardless/gocardless-pro-go/v6"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Payment statuses grouped by Tanvo payment state (lago
// GocardlessProvider PROCESSING_STATUSES, SUCCESS_STATUSES,
// FAILED_STATUSES).
var (
	processingStatuses = []string{"pending_customer_approval", "pending_submission", "submitted", "confirmed"}
	succeededStatuses  = []string{"paid_out"}
	failedStatuses     = []string{"cancelled", "customer_approval_denied", "failed", "charged_back"}
)

// StatusOf maps a GoCardless payment status, or the action of a payment
// event, to a payment state; an unknown one is pending (lago
// BaseProvider#determine_payment_status).
func StatusOf(status string) payment.State {
	switch {
	case slices.Contains(processingStatuses, status):
		return payment.StateProcessing
	case slices.Contains(succeededStatuses, status):
		return payment.StateSucceeded
	case slices.Contains(failedStatuses, status):
		return payment.StateFailed
	default:
		return payment.StatePending
	}
}

// paymentKey is a charge's idempotency key, "payment-<payment ID>" as in
// lago (gocardless/payments/create_service.rb:99): the payment ID alone
// identifies the attempt.
func paymentKey(p payment.PaymentParams) string { return "payment-" + p.PaymentID.String() }

// paymentMetadata is a charge's metadata, the keys lago sends (renamed
// tanvo_*): GoCardless accepts three, so an invoice charge carries the
// invoice, the customer and the issuing date (lago
// invoices/payments/create_service.rb:61-66 without invoice_type,
// gocardless/payments/create_service.rb:93) and a payment request charge
// the customer and the payable (payment_requests/payments/create_service.rb:61-65).
func paymentMetadata(p payment.PaymentParams) map[string]string {
	m := map[string]string{metaCustomerID: p.CustomerID.String()}
	if p.Payable.Type == payment.PayableTypePaymentRequest {
		m[metaPayableType] = string(payment.PayableTypePaymentRequest)
		m[metaPayableID] = p.Payable.PaymentRequestID.String()
		return m
	}
	m[metaInvoiceID] = p.Payable.InvoiceID.String()
	if !p.IssuingDate.IsZero() {
		m[metaIssuingDate] = p.IssuingDate.String()
	}
	return m
}

// CreatePayment implements [payment.ChargeGateway] (lago
// payment_providers/gocardless/payments/create_service.rb): a payment
// against the requested mandate, else the provider customer's newest
// usable mandate. A customer without one is [payment.ErrNoPaymentMethod]
// with code no_mandate_error, which also matches [payment.ErrRejected],
// as lago fails the payment with MandateNotFoundError. The result's
// ProviderMethodID is the mandate charged, which lago stores as the
// provider customer's mandate (create_service.rb:81).
//
// GoCardless collects asynchronously: the result is processing
// (pending_submission) and the payment's events report the outcome. The
// payment is not retried by GoCardless (retry_if_possible false, the API
// default lago also sends).
func (g *Gateway) CreatePayment(ctx context.Context, conn payment.Connection, p payment.PaymentParams) (payment.PaymentResult, error) {
	var res payment.PaymentResult
	if err := p.Validate(); err != nil {
		return res, err
	}
	err := g.do(ctx, conn, "create_payment", func(ctx context.Context, c call) error {
		mandate := p.ProviderMethodID
		if mandate == "" {
			ms, err := mandates(ctx, c, p.ProviderCustomerID)
			if err != nil {
				return err
			}
			if len(ms) == 0 {
				return fmt.Errorf("%w: %w", payment.ErrNoPaymentMethod, &payment.ProviderError{
					Provider: string(Provider), Kind: payment.KindInvalidRequest, Code: codeNoMandate,
					Message: "No mandate available for payment",
				})
			}
			mandate = ms[0].ID
		}
		pm, err := c.svc.Payments.Create(ctx, gc.PaymentCreateParams{
			Amount:   int(p.Amount),
			Currency: strings.ToUpper(p.Currency.Code()),
			Metadata: paymentMetadata(p),
			Links:    gc.PaymentCreateParamsLinks{Mandate: mandate},
		}, gc.WithIdempotencyKey(paymentKey(p)), noRetries())
		if existing := conflictingResource(err); existing != "" {
			// A retried attempt: the payment its first request created.
			pm, err = c.svc.Payments.Get(ctx, existing, noRetries())
		}
		if err != nil {
			return c.sdk(err)
		}
		res = resultOf(pm)
		res.ProviderMethodID = mandate
		if res.Amount == 0 {
			res.Amount, res.Currency = p.Amount, p.Currency
		}
		return nil
	})
	return res, err
}

// resultOf returns a GoCardless payment as a result.
func resultOf(pm *gc.Payment) payment.PaymentResult {
	return payment.PaymentResult{
		ProviderPaymentID: pm.Id,
		ProviderStatus:    pm.Status,
		Status:            StatusOf(pm.Status),
		ProviderMethodID:  pm.Links.Mandate,
		Amount:            int64(pm.Amount),
		Currency:          currencyOf(pm.Currency),
	}
}

// GetPayment implements [payment.ChargeGateway]: lago reads GoCardless
// payment states only from events, so it is [payment.ErrUnsupported].
func (g *Gateway) GetPayment(context.Context, payment.Connection, string) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("gocardless get payment: %w", payment.ErrUnsupported)
}

// CancelPayment implements [payment.ChargeGateway] (lago
// gocardless/payments/cancel_service.rb): a synchronous cancellation that
// returns the payment's new status. A payment no longer cancelable (an
// invalid_state error of reason cancellation_failed) is a logged no-op
// with a zero result, as in lago; other failures are errors.
//
// Lago compares the error's code with "cancellation_failed"
// (cancel_service.rb:34), but gocardless-pro fills code with the HTTP
// status and the reason in errors[].reason; the reason is compared here.
func (g *Gateway) CancelPayment(ctx context.Context, conn payment.Connection, providerPaymentID string) (payment.PaymentResult, error) {
	var res payment.PaymentResult
	err := g.do(ctx, conn, "cancel_payment", func(ctx context.Context, c call) error {
		pm, err := c.svc.Payments.Cancel(ctx, providerPaymentID, gc.PaymentCancelParams{}, noRetries())
		if err != nil {
			if ae, ok := apiError(err); ok && ae.Type == typeInvalidState && hasReason(err, reasonCancellationFailed) {
				g.logger.InfoContext(ctx, "gocardless payment not cancelable",
					slog.String("provider_payment_id", providerPaymentID), slog.String("message", ae.Message))
				return nil
			}
			return c.sdk(err)
		}
		res = resultOf(pm)
		return nil
	})
	return res, err
}

// UpdatePaymentReference implements [payment.ChargeGateway]: lago updates
// Stripe's references only (update_payment_reference_service.rb).
func (g *Gateway) UpdatePaymentReference(context.Context, payment.Connection, string, string) error {
	return fmt.Errorf("gocardless update payment reference: %w", payment.ErrUnsupported)
}

// Authorize implements [payment.ChargeGateway]: lago pre-authorizes
// payment-gated subscriptions through Stripe only, so it is
// [payment.ErrUnsupported].
func (g *Gateway) Authorize(context.Context, payment.Connection, payment.AuthorizeParams) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("gocardless authorize: %w", payment.ErrUnsupported)
}

// CreatePaymentURL implements [payment.ChargeGateway]: GoCardless has no
// payment page; lago refuses payment URLs for GoCardless
// (invoices/payments/generate_payment_url_service.rb), so it is
// [payment.ErrUnsupported].
func (g *Gateway) CreatePaymentURL(context.Context, payment.Connection, payment.PaymentURLParams) (payment.PaymentURL, error) {
	return payment.PaymentURL{}, fmt.Errorf("gocardless create payment url: %w", payment.ErrUnsupported)
}

// ExpirePaymentURL implements [payment.ChargeGateway]; see
// [Gateway.CreatePaymentURL].
func (g *Gateway) ExpirePaymentURL(context.Context, payment.Connection, string) error {
	return fmt.Errorf("gocardless expire payment url: %w", payment.ErrUnsupported)
}
