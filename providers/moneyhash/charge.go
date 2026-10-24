package moneyhash

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// MoneyHash intent and transaction statuses (lago MoneyhashProvider).
const (
	statusPending               = "PENDING"
	statusPendingAuthentication = "PENDING_AUTHENTICATION"
	statusUnprocessed           = "UNPROCESSED"
	statusSuccessful            = "SUCCESSFUL"
	statusProcessed             = "PROCESSED"
	statusFailed                = "FAILED"

	// paymentTypeUnscheduled marks card-on-file payments without a fixed
	// schedule.
	paymentTypeUnscheduled = "UNSCHEDULED"
)

// Statuses grouped by Tanvo payment state (lago MoneyhashProvider
// PROCESSING_STATUSES, SUCCESS_STATUSES, FAILED_STATUSES).
var (
	processingStatuses = []string{statusPending, statusPendingAuthentication, statusUnprocessed}
	succeededStatuses  = []string{statusSuccessful, statusProcessed}
	failedStatuses     = []string{statusFailed}
)

// StatusOf maps a MoneyHash intent or transaction status to a payment
// state; an unknown one is pending (lago
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

// payableFields returns the custom fields naming the payable (lago
// lago_payable_id, lago_payable_type, lago_payable_invoice_type).
func payableFields(fields map[string]any, p payment.Payable, invoiceType string) {
	fields[fieldPayableType] = string(p.Type)
	if p.Type == payment.PayableTypePaymentRequest {
		fields[fieldPayableID] = p.PaymentRequestID.String()
		return
	}
	fields[fieldPayableID] = p.InvoiceID.String()
	if invoiceType != "" {
		fields[fieldInvoiceType] = invoiceType
	}
}

// CreatePayment implements [payment.ChargeGateway]: a merchant-initiated
// unscheduled intent with the customer's saved card token (lago
// moneyhash/payments/create_service.rb and
// payment_requests/payments/moneyhash_service.rb create_moneyhash_payment).
// The card is the caller's method; none is [payment.ErrNoPaymentMethod]
// without calling MoneyHash (lago payment_requests/payments/moneyhash_service.rb:21).
// The intent is keyed by the payment, where lago sends no idempotency key,
// so a retried job cannot charge twice. Lago's plan and subscription
// custom fields are informational and left out.
func (g *Gateway) CreatePayment(ctx context.Context, conn payment.Connection, p payment.PaymentParams) (payment.PaymentResult, error) {
	if err := p.Validate(); err != nil {
		return payment.PaymentResult{}, fmt.Errorf("moneyhash create payment: %w", err)
	}
	if p.ProviderMethodID == "" {
		return payment.PaymentResult{}, fmt.Errorf("moneyhash create payment: %w", payment.ErrNoPaymentMethod)
	}
	fields := customFields(conn, p.Customer, p.ProviderCustomerID)
	payableFields(fields, p.Payable, p.InvoiceType)
	fields[fieldMIT] = true
	fields[fieldRequest] = requestInvoicePayment
	if p.Payable.Type == payment.PayableTypePaymentRequest {
		fields[fieldRequest] = requestRequestPayment
	}
	body := map[string]any{
		"amount":             amountOf(p.Amount, p.Currency),
		"amount_currency":    strings.ToUpper(p.Currency.Code()),
		"billing_data":       billingData(p.Customer),
		"customer":           p.ProviderCustomerID,
		"payment_type":       paymentTypeUnscheduled,
		"merchant_initiated": true,
		"recurring_data":     map[string]string{"agreement_id": p.CustomerID.String()},
		"card_token":         p.ProviderMethodID,
		"custom_fields":      fields,
	}
	in, err := g.createIntent(ctx, conn, "create_payment", p.IdempotencyKey(), body)
	if err != nil {
		return payment.PaymentResult{}, err
	}
	if in.ID == "" {
		return payment.PaymentResult{}, fmt.Errorf("moneyhash create payment: %w", &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindUnavailable, Message: "no intent id",
		})
	}
	status := in.status()
	return payment.PaymentResult{
		ProviderPaymentID: in.ID,
		ProviderStatus:    status,
		Status:            StatusOf(status),
		ProviderMethodID:  p.ProviderMethodID,
		Amount:            p.Amount,
		Currency:          p.Currency,
	}, nil
}

// GetPayment implements [payment.ChargeGateway]. Lago reads MoneyHash
// payment states only from webhooks; [payment.ErrUnsupported].
func (g *Gateway) GetPayment(context.Context, payment.Connection, string) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("moneyhash get payment: %w", payment.ErrUnsupported)
}

// CancelPayment implements [payment.ChargeGateway]. Lago cancels nothing
// at MoneyHash and leaves the payment to its webhooks
// (payment_providers/cancel_payment_service.rb:27-33);
// [payment.ErrUnsupported].
func (g *Gateway) CancelPayment(context.Context, payment.Connection, string) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("moneyhash cancel payment: %w", payment.ErrUnsupported)
}

// UpdatePaymentReference implements [payment.ChargeGateway]. MoneyHash
// intents cannot be updated (lago
// payment_providers/update_payment_reference_service.rb:31);
// [payment.ErrUnsupported].
func (g *Gateway) UpdatePaymentReference(context.Context, payment.Connection, string, string) error {
	return fmt.Errorf("moneyhash update payment reference: %w", payment.ErrUnsupported)
}

// Authorize implements [payment.ChargeGateway]; lago places no MoneyHash
// holds, so it is [payment.ErrUnsupported].
func (g *Gateway) Authorize(context.Context, payment.Connection, payment.AuthorizeParams) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("moneyhash authorize: %w", payment.ErrUnsupported)
}

// CreatePaymentURL implements [payment.ChargeGateway]: a customer-initiated
// intent for the invoice's amount due, keyed by the payment intent and
// expiring with it (lago invoices/payments/moneyhash_service.rb
// generate_payment_url). A customer without a MoneyHash customer also
// tokenizes the card. Payment requests have no MoneyHash payment link in
// lago (payment_requests/payments/moneyhash_service.rb), so theirs is
// [payment.ErrUnsupported].
func (g *Gateway) CreatePaymentURL(ctx context.Context, conn payment.Connection, p payment.PaymentURLParams) (payment.PaymentURL, error) {
	if p.Payable.Type == payment.PayableTypePaymentRequest {
		return payment.PaymentURL{}, fmt.Errorf("moneyhash create payment url: %w", payment.ErrUnsupported)
	}
	var amount int64
	for _, l := range p.Lines {
		amount += l.Amount
	}
	var v core.Validator
	v.Merge("", p.Payable.Validate())
	v.Positive("amount", amount)
	v.Currency("currency", p.Currency)
	if err := v.Err(); err != nil {
		return payment.PaymentURL{}, fmt.Errorf("moneyhash create payment url: %w", err)
	}
	fields := customFields(conn, p.Customer, p.ProviderCustomerID)
	payableFields(fields, p.Payable, p.InvoiceType)
	fields[fieldMIT] = false
	fields[fieldRequest] = requestPaymentURL
	body := map[string]any{
		"amount":             amountOf(amount, p.Currency),
		"amount_currency":    strings.ToUpper(p.Currency.Code()),
		"billing_data":       billingData(p.Customer),
		"merchant_initiated": false,
		"custom_fields":      fields,
	}
	if !p.ExpiresAt.IsZero() {
		if s := int64(p.ExpiresAt.Sub(g.now()).Seconds()); s > 0 {
			body["expires_after_seconds"] = s
		}
	}
	if p.ProviderCustomerID != "" {
		body["customer"] = p.ProviderCustomerID
	} else {
		body["tokenize_card"] = true
		body["payment_type"] = paymentTypeUnscheduled
		body["recurring_data"] = map[string]string{"agreement_id": p.CustomerID.String()}
	}
	in, err := g.createIntent(ctx, conn, "create_payment_url", p.IntentID.String(), body)
	if err != nil {
		return payment.PaymentURL{}, err
	}
	if in.EmbedURL == "" {
		return payment.PaymentURL{}, fmt.Errorf("moneyhash create payment url: %w", &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindPermanent, Message: "no embed url",
		})
	}
	return payment.PaymentURL{URL: in.EmbedURL, SessionID: in.ID}, nil
}

// ExpirePaymentURL implements [payment.ChargeGateway]. A MoneyHash link
// expires by itself (expires_after_seconds) and lago never expires one
// early; [payment.ErrUnsupported].
func (g *Gateway) ExpirePaymentURL(context.Context, payment.Connection, string) error {
	return fmt.Errorf("moneyhash expire payment url: %w", payment.ErrUnsupported)
}

// Refund implements [payment.RefundGateway]. Lago refunds no credit note
// through MoneyHash (credit_notes/refunds has no MoneyHash service);
// [payment.ErrUnsupported].
func (g *Gateway) Refund(context.Context, payment.Connection, payment.RefundParams) (payment.RefundResult, error) {
	return payment.RefundResult{}, fmt.Errorf("moneyhash refund: %w", payment.ErrUnsupported)
}
