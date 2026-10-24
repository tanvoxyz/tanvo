package cashfree

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Cashfree payment link statuses (lago CashfreeProvider).
const (
	statusPaid          = "PAID"
	statusPartiallyPaid = "PARTIALLY_PAID"
	statusExpired       = "EXPIRED"
	statusCancelled     = "CANCELLED"
)

// Link note keys the adapter attaches to payment links and reads back from
// events (lago's lago_* keys, renamed). Cashfree keeps at most five notes.
const (
	noteCustomerID  = "tanvo_customer_id"
	noteInvoiceID   = "tanvo_invoice_id"
	notePayableID   = "tanvo_payable_id"
	notePayableType = "tanvo_payable_type"
	noteIssuingDate = "invoice_issuing_date"
	noteRequestDate = "payment_issuing_date"
	notePaymentType = "payment_type"
	paymentTypeOnce = "one-time"
)

const (
	// defaultPhone is sent for a customer without a phone, which Cashfree
	// requires (lago customer.phone || "9999999999").
	defaultPhone = "9999999999"
	// requestLinkLife is the lifetime of a link without an expiry (lago
	// payment_requests/payments/cashfree_service.rb: 10 minutes).
	requestLinkLife = 10 * time.Minute
)

// StatusOf maps a Cashfree payment link status to a payment state (lago
// CashfreeProvider PROCESSING_STATUSES, SUCCESS_STATUSES, FAILED_STATUSES;
// BaseProvider#determine_payment_status); an unknown one is pending.
func StatusOf(status string) payment.State {
	switch {
	case status == statusPartiallyPaid:
		return payment.StateProcessing
	case status == statusPaid:
		return payment.StateSucceeded
	case slices.Contains([]string{statusExpired, statusCancelled}, status):
		return payment.StateFailed
	default:
		return payment.StatePending
	}
}

// amountOf renders minor units as the decimal major-unit amount Cashfree
// takes. Lago sends total_due_amount_cents / 100.to_f, a float that
// assumes two decimals; the exact amount in the currency's exponent is
// sent instead.
func amountOf(minor int64, c money.Currency) json.Number {
	return json.Number(c.FromMinor(minor).StringFixed(c.Exponent()))
}

// link is the part of a payment link answer the adapter reads.
type link struct {
	LinkID  string `json:"link_id"`
	LinkURL string `json:"link_url"`
}

// CreatePaymentURL implements [payment.ChargeGateway]: a payment link for
// the payable's amount due, without partial payments, reminders or
// Cashfree notifications, returning to the connection's success URL (lago
// invoices/payments/cashfree_service.rb and
// payment_requests/payments/cashfree_service.rb generate_payment_url). The
// link notes name the customer and the payable and mark the payment
// one-time, so its webhook creates the payment. The link expires at
// ExpiresAt, else in 10 minutes as lago's payment request links do. A
// customer without a phone is sent lago's placeholder 9999999999, as
// Cashfree requires one. Every call makes a new link with a new ID, as in
// lago; the ID is returned as the session.
func (g *Gateway) CreatePaymentURL(ctx context.Context, conn payment.Connection, p payment.PaymentURLParams) (payment.PaymentURL, error) {
	var amount int64
	for _, l := range p.Lines {
		amount += l.Amount
	}
	var v core.Validator
	v.Merge("", p.Payable.Validate())
	v.Positive("amount", amount)
	v.Currency("currency", p.Currency)
	if err := v.Err(); err != nil {
		return payment.PaymentURL{}, fmt.Errorf("cashfree create payment url: %w", err)
	}
	notes := map[string]string{
		noteCustomerID:  p.CustomerID.String(),
		notePaymentType: paymentTypeOnce,
	}
	payableID := p.Payable.InvoiceID.String()
	if p.Payable.Type == payment.PayableTypePaymentRequest {
		payableID = p.Payable.PaymentRequestID.String()
		notes[notePayableID] = payableID
		notes[notePayableType] = string(payment.PayableTypePaymentRequest)
		if !p.IssuingDate.IsZero() {
			notes[noteRequestDate] = p.IssuingDate.String()
		}
	} else {
		notes[noteInvoiceID] = payableID
		if !p.IssuingDate.IsZero() {
			notes[noteIssuingDate] = p.IssuingDate.String()
		}
	}
	expires := p.ExpiresAt
	if expires.IsZero() {
		expires = g.now().Add(requestLinkLife)
	}
	phone := strings.TrimSpace(p.Customer.Phone)
	if phone == "" {
		phone = defaultPhone
	}
	customer := map[string]string{"customer_phone": phone}
	if p.Customer.Email != "" {
		customer["customer_email"] = p.Customer.Email
	}
	if p.Customer.Name != "" {
		customer["customer_name"] = p.Customer.Name
	}
	id := g.linkID()
	var out link
	err := g.do(ctx, conn, "create_payment_url", func(ctx context.Context, c call) error {
		body := map[string]any{
			"customer_details":      customer,
			"link_notify":           map[string]bool{"send_sms": false, "send_email": false},
			"link_meta":             map[string]any{"upi_intent": true, "return_url": c.settings.successRedirectURL()},
			"link_notes":            notes,
			"link_id":               id,
			"link_amount":           amountOf(amount, p.Currency),
			"link_currency":         strings.ToUpper(p.Currency.Code()),
			"link_purpose":          payableID,
			"link_expiry_time":      expires.UTC().Format(time.RFC3339),
			"link_partial_payments": false,
			"link_auto_reminders":   false,
		}
		return c.send(ctx, http.MethodPost, pathLinks, body, &out)
	})
	if err != nil {
		return payment.PaymentURL{}, err
	}
	if out.LinkURL == "" {
		return payment.PaymentURL{}, fmt.Errorf("cashfree create payment url: %w", &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindPermanent, Message: "no link url",
		})
	}
	if out.LinkID != "" {
		id = out.LinkID
	}
	return payment.PaymentURL{URL: out.LinkURL, SessionID: id}, nil
}

// CreatePayment implements [payment.ChargeGateway]. Cashfree takes no
// merchant-initiated charge: lago records the payment without calling
// Cashfree and waits for a webhook that never names it
// (payment_providers/cashfree/payments/create_service.rb), so it is
// [payment.ErrUnsupported]; customers pay through CreatePaymentURL.
func (g *Gateway) CreatePayment(context.Context, payment.Connection, payment.PaymentParams) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("cashfree create payment: %w", payment.ErrUnsupported)
}

// GetPayment implements [payment.ChargeGateway]. Lago reads Cashfree
// payment states only from webhooks; [payment.ErrUnsupported].
func (g *Gateway) GetPayment(context.Context, payment.Connection, string) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("cashfree get payment: %w", payment.ErrUnsupported)
}

// CancelPayment implements [payment.ChargeGateway]. Lago cancels nothing
// at Cashfree (payment_providers/cancel_payment_service.rb:27);
// [payment.ErrUnsupported].
func (g *Gateway) CancelPayment(context.Context, payment.Connection, string) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("cashfree cancel payment: %w", payment.ErrUnsupported)
}

// UpdatePaymentReference implements [payment.ChargeGateway]. Cashfree has
// no metadata update (lago
// payment_providers/update_payment_reference_service.rb:31);
// [payment.ErrUnsupported].
func (g *Gateway) UpdatePaymentReference(context.Context, payment.Connection, string, string) error {
	return fmt.Errorf("cashfree update payment reference: %w", payment.ErrUnsupported)
}

// Authorize implements [payment.ChargeGateway]; lago places no Cashfree
// holds, so it is [payment.ErrUnsupported].
func (g *Gateway) Authorize(context.Context, payment.Connection, payment.AuthorizeParams) (payment.PaymentResult, error) {
	return payment.PaymentResult{}, fmt.Errorf("cashfree authorize: %w", payment.ErrUnsupported)
}

// ExpirePaymentURL implements [payment.ChargeGateway]. A Cashfree link
// expires by itself (link_expiry_time) and lago never cancels one early;
// [payment.ErrUnsupported].
func (g *Gateway) ExpirePaymentURL(context.Context, payment.Connection, string) error {
	return fmt.Errorf("cashfree expire payment url: %w", payment.ErrUnsupported)
}

// Refund implements [payment.RefundGateway]. Lago refunds no credit note
// through Cashfree (credit_notes/refunds has no Cashfree service);
// [payment.ErrUnsupported].
func (g *Gateway) Refund(context.Context, payment.Connection, payment.RefundParams) (payment.RefundResult, error) {
	return payment.RefundResult{}, fmt.Errorf("cashfree refund: %w", payment.ErrUnsupported)
}
