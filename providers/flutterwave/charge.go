package flutterwave

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Flutterwave transaction statuses (lago FlutterwaveProvider).
const (
	statusPending    = "pending"
	statusSuccessful = "successful"
	statusFailed     = "failed"
	statusCancelled  = "cancelled"
)

// Meta keys the adapter attaches to checkout pages and reads back from
// events (lago's lago_* keys, renamed).
const (
	metaCustomerID       = "tanvo_customer_id"
	metaInvoiceID        = "tanvo_invoice_id"
	metaInvoiceNumber    = "tanvo_invoice_number"
	metaInvoiceIDs       = "tanvo_invoice_ids"
	metaOrganizationID   = "tanvo_organization_id"
	metaPaymentRequestID = "tanvo_payment_request_id"
	metaPayableID        = "tanvo_payable_id"
	metaPayableType      = "tanvo_payable_type"
	metaPaymentType      = "payment_type"
	paymentTypeOnce      = "one-time"
)

// requestTxRefPrefix starts the transaction reference of a payment
// request's page (lago "lago_payment_request_<id>"); an invoice's
// reference is its ID.
const requestTxRefPrefix = "tanvo_payment_request_"

// sessionMinutes is the lifetime of a checkout page (lago
// configuration.session_duration: 30).
const sessionMinutes = 30

// StatusOf maps a Flutterwave transaction status to a payment state (lago
// FlutterwaveProvider PROCESSING_STATUSES, SUCCESS_STATUSES,
// FAILED_STATUSES; BaseProvider#determine_payment_status); an unknown one
// is pending.
func StatusOf(status string) payment.State {
	switch {
	case status == statusPending:
		return payment.StateProcessing
	case status == statusSuccessful:
		return payment.StateSucceeded
	case slices.Contains([]string{statusFailed, statusCancelled}, status):
		return payment.StateFailed
	default:
		return payment.StatePending
	}
}

// amountOf renders minor units as the decimal major-unit amount
// Flutterwave takes. Lago sends Money.from_cents(...).to_f, a float; the
// exact amount in the currency's exponent is sent instead.
func amountOf(minor int64, c money.Currency) json.Number {
	return json.Number(c.FromMinor(minor).StringFixed(c.Exponent()))
}

// checkout is the part of a hosted checkout answer the adapter reads.
type checkout struct {
	Status string `json:"status"`
	Data   struct {
		Link string `json:"link"`
	} `json:"data"`
}

// CreatePaymentURL implements [payment.ChargeGateway]: a hosted checkout
// page of 30 minutes for the payable's amount, returning to the
// connection's success URL (lago invoices/payments/flutterwave_service.rb
// and payment_requests/payments/flutterwave_service.rb
// generate_payment_url). The page's meta names the customer, the
// organization and the payable; its title names the organization and its
// description the invoices. The transaction reference is the invoice ID,
// or tanvo_payment_request_<id> for a payment request. Flutterwave has no
// session ID: the page's SessionID is empty and it expires by itself.
//
// Lago posts a payment request's page to the API root instead of
// /payments and names the request by lago_payment_request_id, which its
// webhook does not read; both payables are posted to /payments here, and a
// payment request's meta also carries the tanvo_payable_id and
// tanvo_payable_type keys its webhook reads.
func (g *Gateway) CreatePaymentURL(ctx context.Context, conn payment.Connection, p payment.PaymentURLParams) (payment.PaymentURL, error) {
	var amount int64
	numbers := make([]string, 0, len(p.Lines))
	ids := make([]string, 0, len(p.Lines))
	for _, l := range p.Lines {
		amount += l.Amount
		numbers = append(numbers, l.Name)
		if !l.InvoiceID.IsZero() {
			ids = append(ids, l.InvoiceID.String())
		}
	}
	var v core.Validator
	v.Merge("", p.Payable.Validate())
	v.Positive("amount", amount)
	v.Currency("currency", p.Currency)
	if err := v.Err(); err != nil {
		return payment.PaymentURL{}, fmt.Errorf("flutterwave create payment url: %w", err)
	}
	meta := map[string]string{
		metaCustomerID:     p.CustomerID.String(),
		metaOrganizationID: conn.OrganizationID.String(),
	}
	var txRef string
	customizations := map[string]string{}
	if p.Payable.Type == payment.PayableTypePaymentRequest {
		id := p.Payable.PaymentRequestID.String()
		txRef = requestTxRefPrefix + id
		meta[metaPaymentRequestID] = id
		meta[metaPayableID] = id
		meta[metaPayableType] = string(payment.PayableTypePaymentRequest)
		meta[metaInvoiceIDs] = strings.Join(ids, ",")
		customizations["title"] = p.OrganizationName + " - Payment Request"
		customizations["description"] = "Payment for invoices: " + strings.Join(numbers, ", ")
	} else {
		id := p.Payable.InvoiceID.String()
		txRef = id
		number := ""
		if len(numbers) > 0 {
			number = numbers[0]
		}
		meta[metaInvoiceID] = id
		meta[metaInvoiceNumber] = number
		meta[metaPaymentType] = paymentTypeOnce
		customizations["title"] = p.OrganizationName + " - Invoice Payment"
		customizations["description"] = "Payment for Invoice #" + number
	}
	if p.OrganizationLogoURL != "" {
		customizations["logo"] = p.OrganizationLogoURL
	}
	name := p.Customer.Name
	if name == "" {
		name = p.Customer.Email
	}
	var out checkout
	err := g.do(ctx, conn, "create_payment_url", func(ctx context.Context, c call) error {
		body := map[string]any{
			"amount":       amountOf(amount, p.Currency),
			"tx_ref":       txRef,
			"currency":     strings.ToUpper(p.Currency.Code()),
			"redirect_url": c.settings.successRedirectURL(),
			"customer": map[string]string{
				"email":        p.Customer.Email,
				"phone_number": strings.TrimSpace(p.Customer.Phone),
				"name":         name,
			},
			"customizations": customizations,
			"configuration":  map[string]int{"session_duration": sessionMinutes},
			"meta":           meta,
		}
		return c.send(ctx, http.MethodPost, pathPayments, body, &out)
	})
	if err != nil {
		return payment.PaymentURL{}, err
	}
	if out.Data.Link == "" {
		return payment.PaymentURL{}, fmt.Errorf("flutterwave create payment url: %w", &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindPermanent, Message: "no checkout link",
		})
	}
	return payment.PaymentURL{URL: out.Data.Link}, nil
}

// CreatePayment implements [payment.ChargeGateway]. Lago charges no saved
// Flutterwave method (invoices/payments/flutterwave_service.rb has no
// automatic payment), so it is [payment.ErrUnsupported]; customers pay
// through CreatePaymentURL.
func (g *Gateway) CreatePayment(context.Context, payment.Connection, payment.PaymentParams) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("flutterwave create payment: %w", payment.ErrUnsupported)
}

// GetPayment implements [payment.ChargeGateway]. Lago reads Flutterwave
// payment states only from verified webhooks; [payment.ErrUnsupported].
func (g *Gateway) GetPayment(context.Context, payment.Connection, string) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("flutterwave get payment: %w", payment.ErrUnsupported)
}

// CancelPayment implements [payment.ChargeGateway]. Lago cancels nothing
// at Flutterwave (payment_providers/cancel_payment_service.rb:27);
// [payment.ErrUnsupported].
func (g *Gateway) CancelPayment(context.Context, payment.Connection, string) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("flutterwave cancel payment: %w", payment.ErrUnsupported)
}

// UpdatePaymentReference implements [payment.ChargeGateway]. Flutterwave
// has no metadata update (lago
// payment_providers/update_payment_reference_service.rb:31);
// [payment.ErrUnsupported].
func (g *Gateway) UpdatePaymentReference(context.Context, payment.Connection, string, string) error {
	return fmt.Errorf("flutterwave update payment reference: %w", payment.ErrUnsupported)
}

// Authorize implements [payment.ChargeGateway]; lago places no Flutterwave
// holds, so it is [payment.ErrUnsupported].
func (g *Gateway) Authorize(context.Context, payment.Connection, payment.AuthorizeParams) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("flutterwave authorize: %w", payment.ErrUnsupported)
}

// ExpirePaymentURL implements [payment.ChargeGateway]. A checkout page
// expires by itself after 30 minutes and has no ID to cancel it by;
// [payment.ErrUnsupported].
func (g *Gateway) ExpirePaymentURL(context.Context, payment.Connection, string) error {
	return fmt.Errorf("flutterwave expire payment url: %w", payment.ErrUnsupported)
}

// Refund implements [payment.RefundGateway]. Lago refunds no credit note
// through Flutterwave (credit_notes/refunds has no Flutterwave service);
// [payment.ErrUnsupported].
func (g *Gateway) Refund(context.Context, payment.Connection, payment.RefundParams) (payment.RefundResult, error) {
	return payment.RefundResult{}, fmt.Errorf("flutterwave refund: %w", payment.ErrUnsupported)
}
