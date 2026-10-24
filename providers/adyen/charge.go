package adyen

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/adyen/adyen-go-api-library/v21/src/checkout"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Payment result codes grouped by Tanvo payment state (lago
// AdyenProvider PROCESSING_STATUSES, SUCCESS_STATUSES, FAILED_STATUSES).
var (
	processingStatuses = []string{"AuthorisedPending", "Received"}
	succeededStatuses  = []string{"Authorised", "SentForSettle", "SettleScheduled", "Settled", "Refunded"}
	failedStatuses     = []string{"Cancelled", "CaptureFailed", "Error", "Expired", "Refused"}
)

// StatusOf maps an Adyen result code to a payment state; an unknown code
// is pending (lago BaseProvider#determine_payment_status).
func StatusOf(resultCode string) payment.State {
	switch {
	case slices.Contains(processingStatuses, resultCode):
		return payment.StateProcessing
	case slices.Contains(succeededStatuses, resultCode):
		return payment.StateSucceeded
	case slices.Contains(failedStatuses, resultCode):
		return payment.StateFailed
	default:
		return payment.StatePending
	}
}

const (
	// MaxReferenceLength is Adyen's limit of a payment reference
	// (lago adyen/payments/create_service.rb REFERENCE_MAX_LENGTH).
	MaxReferenceLength = 80

	// statusCancelled is the status lago gives a payment whose
	// cancellation Adyen confirmed (handle_event_service.rb CANCELLATION).
	statusCancelled = "Cancelled"

	// shopperContAuth marks a merchant-initiated charge of a stored card.
	shopperContAuth = "ContAuth"

	// paymentRequestReference is the reference of a payment request's link
	// (lago payment_requests/payments/adyen_service.rb:87).
	paymentRequestReference = "Overdue invoices"
	// paymentRequestLinkTTL is how long a payment request's link stays
	// valid, Adyen's maximum (payment_requests/payments/adyen_service.rb:97).
	paymentRequestLinkTTL = 70 * 24 * time.Hour
)

// truncateReference keeps the last [MaxReferenceLength] characters: the
// callers end the reference with the invoice number, which reconciliation
// needs (lago create_service.rb:117-127).
func truncateReference(s string) string {
	r := []rune(s)
	if len(r) <= MaxReferenceLength {
		return s
	}
	return string(r[len(r)-MaxReferenceLength:])
}

// paymentKey is a charge's idempotency key, "payment-<payment ID>" as in
// lago (create_service.rb:86): Adyen accepts at most 64 characters, which
// [payment.PaymentParams.IdempotencyKey] exceeds. The payment ID alone
// identifies the attempt.
func paymentKey(p payment.PaymentParams) string { return "payment-" + p.PaymentID.String() }

// CreatePayment implements [payment.ChargeGateway] (lago
// payment_providers/adyen/payments/create_service.rb): a card-on-file
// charge of a stored card of the shopper, the provider customer ID.
//
// The requested method is charged, else the shopper's first stored method
// (/paymentMethods); a shopper with none is [payment.ErrNoPaymentMethod],
// which also matches [payment.ErrRejected]: lago sends the charge without
// a method and fails on Adyen's refusal. Lago lists the stored methods on
// every charge to store the first one on the provider customer
// (create_service.rb:69-79); Tanvo lists them only when it needs one, and
// the caller keeps its methods from the setup notification.
//
// The reference is the payment description truncated to its last 80
// characters, the shopper email the customer's first address (lago sends
// the whole list, create_service.rb:113), and every request carries the
// return URL the API version requires. A Refused or other failed result
// code is a failed result, not an error, as lago stores the result code
// (create_service.rb:35-38).
func (g *Gateway) CreatePayment(ctx context.Context, conn payment.Connection, p payment.PaymentParams) (payment.PaymentResult, error) {
	var res payment.PaymentResult
	if err := p.Validate(); err != nil {
		return res, err
	}
	err := g.do(ctx, conn, "create_payment", func(ctx context.Context, c call) error {
		method := p.ProviderMethodID
		if method == "" {
			ms, err := storedMethods(ctx, c, p.ProviderCustomerID)
			if err != nil {
				return err
			}
			if len(ms) == 0 {
				return fmt.Errorf("%w: %w", payment.ErrNoPaymentMethod, &payment.ProviderError{
					Provider: string(Provider), Kind: payment.KindInvalidRequest, Code: codeNoPaymentMethod,
					Message: "the shopper has no stored payment method",
				})
			}
			method = ms[0].ID
		}
		req := checkout.PaymentRequest{
			Amount:    checkout.Amount{Currency: p.Currency.Code(), Value: p.Amount},
			Reference: truncateReference(p.Description),
			PaymentMethod: checkout.CheckoutPaymentMethod{CardDetails: &checkout.CardDetails{
				Type:                  ptr(methodScheme),
				StoredPaymentMethodId: ptr(method),
			}},
			ShopperReference:         ptr(p.ProviderCustomerID),
			MerchantAccount:          c.settings.MerchantAccount,
			ShopperInteraction:       ptr(shopperContAuth),
			RecurringProcessingModel: ptr(recurringModel),
			ShopperEmail:             optional(p.Customer.Email),
			ReturnUrl:                c.settings.successURL(),
			ApplicationInfo:          applicationInfo(),
		}
		api := c.checkout.PaymentsApi
		pr, hr, err := api.Payments(ctx, api.PaymentsInput().IdempotencyKey(paymentKey(p)).PaymentRequest(req))
		if err != nil {
			return sdkErr(hr, err)
		}
		res = payment.PaymentResult{
			ProviderPaymentID: deref(pr.PspReference),
			ProviderStatus:    deref(pr.ResultCode),
			Status:            StatusOf(deref(pr.ResultCode)),
			ProviderMethodID:  method,
			Amount:            p.Amount,
			Currency:          p.Currency,
		}
		if pr.Amount != nil {
			res.Amount = pr.Amount.Value
		}
		return nil
	})
	return res, err
}

// GetPayment implements [payment.ChargeGateway]: Adyen reports payment
// states only through notifications, so it is [payment.ErrUnsupported].
func (g *Gateway) GetPayment(context.Context, payment.Connection, string) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("adyen get payment: %w", payment.ErrUnsupported)
}

// CancelPayment implements [payment.ChargeGateway] (lago
// adyen/payments/cancel_service.rb): a cancellation by PSP reference.
// Adyen acknowledges it and confirms with a CANCELLATION notification, so
// the result is zero. A 422 answer (a payment no longer cancelable) is a
// logged no-op, as in lago; other failures are errors.
//
// The idempotency key is "cancel-<PSP reference>": lago reuses the
// charge's "payment-<payment ID>" (cancel_service.rb:18), which the port
// does not pass; the PSP reference identifies the payment as uniquely.
func (g *Gateway) CancelPayment(ctx context.Context, conn payment.Connection, providerPaymentID string) (payment.PaymentResult, error) {
	err := g.do(ctx, conn, "cancel_payment", func(ctx context.Context, c call) error {
		api := c.checkout.ModificationsApi
		_, hr, err := api.CancelAuthorisedPaymentByPspReference(ctx, api.CancelAuthorisedPaymentByPspReferenceInput(providerPaymentID).
			IdempotencyKey("cancel-"+providerPaymentID).
			PaymentCancelRequest(checkout.PaymentCancelRequest{
				MerchantAccount: c.settings.MerchantAccount,
				ApplicationInfo: applicationInfo(),
			}))
		err = sdkErr(hr, err)
		if statusOf(err) == http.StatusUnprocessableEntity {
			pe, _ := payment.AsProviderError(classify(err))
			g.logger.InfoContext(ctx, "adyen payment not cancelable",
				slog.String("provider_payment_id", providerPaymentID),
				slog.Int("status", pe.HTTPStatus), slog.String("message", pe.Message))
			return nil
		}
		return err
	})
	return payment.PaymentResult{}, err
}

// UpdatePaymentReference implements [payment.ChargeGateway]: Adyen
// payments keep their reference; lago updates Stripe's only
// (update_payment_reference_service.rb).
func (g *Gateway) UpdatePaymentReference(context.Context, payment.Connection, string, string) error {
	return fmt.Errorf("adyen update payment reference: %w", payment.ErrUnsupported)
}

// Authorize implements [payment.ChargeGateway]: lago pre-authorizes
// payment-gated subscriptions through Stripe only
// (payment_providers/stripe/payments/authorize_service.rb), so it is
// [payment.ErrUnsupported].
func (g *Gateway) Authorize(context.Context, payment.Connection, payment.AuthorizeParams) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("adyen authorize: %w", payment.ErrUnsupported)
}

// CreatePaymentURL implements [payment.ChargeGateway]: a payment link that
// also saves the card for the shopper.
//
// For an invoice (lago invoices/payments/adyen_service.rb
// generate_payment_url) the link is keyed by the checkout link, expires
// with it and is referenced by the invoice number. For a payment request
// (lago payment_requests/payments/adyen_service.rb) it has no key, expires
// after 70 days and is referenced "Overdue invoices". Both mark the
// payment one-time, so its AUTHORISATION notification creates the
// payment. The link has no session to expire.
func (g *Gateway) CreatePaymentURL(ctx context.Context, conn payment.Connection, p payment.PaymentURLParams) (payment.PaymentURL, error) {
	var out payment.PaymentURL
	var v core.Validator
	v.Required("external_id", p.Customer.ExternalID)
	v.Currency("currency", p.Currency)
	if err := v.Err(); err != nil {
		return out, err
	}
	err := g.do(ctx, conn, "create_payment_url", func(ctx context.Context, c call) error {
		var amount int64
		for _, l := range p.Lines {
			amount += l.Amount
		}
		meta := map[string]string{
			metaCustomerID:  p.CustomerID.String(),
			metaPayableType: string(p.Payable.Type),
			metaPayableID:   payableID(p.Payable),
			metaPaymentType: paymentTypeOneTime,
		}
		req := checkout.PaymentLinkRequest{
			Amount:                   checkout.Amount{Currency: p.Currency.Code(), Value: amount},
			MerchantAccount:          c.settings.MerchantAccount,
			ReturnUrl:                ptr(c.settings.successURL()),
			ShopperReference:         ptr(p.Customer.ExternalID),
			StorePaymentMethodMode:   ptr(storeEnabled),
			RecurringProcessingModel: ptr(recurringModel),
			ShopperEmail:             optional(p.Customer.Email),
			Metadata:                 &meta,
			ApplicationInfo:          applicationInfo(),
		}
		in := c.checkout.PaymentLinksApi.PaymentLinksInput()
		switch p.Payable.Type {
		case payment.PayableTypePaymentRequest:
			req.Reference = paymentRequestReference
			req.ExpiresAt = ptr(g.now().UTC().Add(paymentRequestLinkTTL).Truncate(time.Second))
		default:
			if len(p.Lines) > 0 {
				req.Reference = p.Lines[0].Name
			}
			meta[metaInvoiceID] = p.Payable.InvoiceID.String()
			if !p.IssuingDate.IsZero() {
				meta[metaIssuingDate] = p.IssuingDate.String()
			}
			if p.InvoiceType != "" {
				meta[metaInvoiceType] = p.InvoiceType
			}
			if !p.ExpiresAt.IsZero() {
				req.ExpiresAt = ptr(p.ExpiresAt.UTC())
			}
			if !p.IntentID.IsZero() {
				in = in.IdempotencyKey(p.IntentID.String())
			}
		}
		res, hr, err := c.checkout.PaymentLinksApi.PaymentLinks(ctx, in.PaymentLinkRequest(req))
		if err != nil {
			return sdkErr(hr, err)
		}
		out = payment.PaymentURL{URL: res.Url}
		return nil
	})
	return out, err
}

// ExpirePaymentURL implements [payment.ChargeGateway]: Adyen links have no
// session to expire (CreatePaymentURL returns no session ID, and lago
// expires Stripe's sessions only), so it is [payment.ErrUnsupported].
func (g *Gateway) ExpirePaymentURL(context.Context, payment.Connection, string) error {
	return fmt.Errorf("adyen expire payment url: %w", payment.ErrUnsupported)
}

func payableID(p payment.Payable) string {
	if p.Type == payment.PayableTypePaymentRequest {
		return p.PaymentRequestID.String()
	}
	return p.InvoiceID.String()
}
