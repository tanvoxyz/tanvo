package payment

import (
	"fmt"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Rule codes refusing a payment request, reusing lago's names.
const (
	// CodeInvoicesNotOverdue: an invoice is not overdue.
	CodeInvoicesNotOverdue = "invoices_not_overdue"
	// CodeInvoicesDifferentCurrencies: the invoices use several currencies.
	CodeInvoicesDifferentCurrencies = "invoices_have_different_currencies"
	// CodeInvoicesDifferentBillingEntities: the invoices come from several
	// billing entities.
	CodeInvoicesDifferentBillingEntities = "invoices_have_different_billing_entities"
	// CodeInvoicesNotReady: an invoice is not ready for payment processing.
	CodeInvoicesNotReady = "invoices_not_ready_for_payment_processing"
	// CodeInvoicesNotFinalized: an invoice is not finalized.
	CodeInvoicesNotFinalized = "invoices_not_finalized"
	// CodeInvoicesAlreadyPaid: an invoice has nothing left to pay.
	CodeInvoicesAlreadyPaid = "invoices_already_paid"
)

// Request asks a customer to pay several overdue invoices at once (domain
// §15.4).
type Request struct {
	// ID identifies the payment request.
	ID core.PaymentRequestID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CustomerID is the asked customer.
	CustomerID core.CustomerID
	// InvoiceIDs are the requested invoices; at least one.
	InvoiceIDs []core.InvoiceID
	// Amount is Σ total_due_amount of the invoices, in minor units of
	// Currency.
	Amount int64
	// Currency is the invoices' currency.
	Currency money.Currency
	// Email is where the request is sent, sanitized with [SanitizeEmail].
	Email *string
	// PaymentStatus is the collection status.
	PaymentStatus core.PaymentStatus
	// PaymentAttempts counts the payment attempts.
	PaymentAttempts int
	// DunningCampaignID is the campaign that created the request.
	DunningCampaignID core.DunningCampaignID
	// PaymentMethodType is how the request is collected; "" uses the
	// customer default.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is the payment method to charge.
	PaymentMethodID core.PaymentMethodID
	// ReadyForPaymentProcessing tells whether the request may be charged.
	ReadyForPaymentProcessing bool
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the invariants of domain §15.4.
func (r *Request) Validate() error {
	var v core.Validator
	v.Check(!r.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	v.Check(len(r.InvoiceIDs) > 0, "invoice_ids", core.CodeRequired)
	v.NonNegative("amount", r.Amount)
	v.Currency("currency", r.Currency)
	if r.Email != nil {
		v.EmailList("email", *r.Email)
	}
	v.Enum("payment_status", r.PaymentStatus)
	v.Check(r.PaymentAttempts >= 0, "payment_attempts", core.CodeOutOfRange)
	v.OptionalEnum("payment_method_type", r.PaymentMethodType)
	return v.Err()
}

// SanitizeEmail replaces dash look-alikes with "-", removes invisible
// characters and trims spaces, as emails are stored.
func SanitizeEmail(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\u2012', '\u2013', '\u2014', '\u2015', '\u2043', '\u2212':
			return '-'
		case '\u200B', '\u200C', '\u200D', '\u00A0', '\u200E', '\u200F', '\uFEFF':
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// RequestInvoice holds the facts of an invoice a payment request is checked
// against.
type RequestInvoice struct {
	// ID identifies the invoice.
	ID core.InvoiceID
	// Finalized reports a finalized invoice.
	Finalized bool
	// Overdue reports an invoice past its due date and unpaid.
	Overdue bool
	// ReadyForPaymentProcessing reports an invoice that may be charged.
	ReadyForPaymentProcessing bool
	// PaymentStatus is the invoice's payment status.
	PaymentStatus core.PaymentStatus
	// Currency is the invoice's currency.
	Currency money.Currency
	// BillingEntityID is the issuing billing entity.
	BillingEntityID core.BillingEntityID
	// TotalDueAmount is what is left to pay, in minor units.
	TotalDueAmount int64
}

// CheckRequestInvoices checks that invoices can be requested together
// (domain §15.4): at least one, all finalized, overdue, unpaid and ready
// for payment processing, with one currency and one billing entity. It
// returns the requested amount (Σ total due) and the currency, or a
// [core.RuleError] (or [core.ErrNotFound] when there are no invoices).
func CheckRequestInvoices(invoices []RequestInvoice) (int64, money.Currency, error) {
	if len(invoices) == 0 {
		return 0, money.Currency{}, fmt.Errorf("payment request invoices: %w", core.ErrNotFound)
	}
	first := invoices[0]
	var total int64
	for _, inv := range invoices {
		switch {
		case !inv.Finalized:
			return 0, money.Currency{}, core.NewRuleError(CodeInvoicesNotFinalized, inv.ID.String())
		case !inv.Overdue:
			return 0, money.Currency{}, core.NewRuleError(CodeInvoicesNotOverdue, inv.ID.String())
		case inv.Currency.Code() != first.Currency.Code():
			return 0, money.Currency{}, core.NewRuleError(CodeInvoicesDifferentCurrencies, inv.ID.String())
		case inv.BillingEntityID != first.BillingEntityID:
			return 0, money.Currency{}, core.NewRuleError(CodeInvoicesDifferentBillingEntities, inv.ID.String())
		case !inv.ReadyForPaymentProcessing:
			return 0, money.Currency{}, core.NewRuleError(CodeInvoicesNotReady, inv.ID.String())
		case inv.PaymentStatus == core.PaymentStatusSucceeded || inv.TotalDueAmount <= 0:
			return 0, money.Currency{}, core.NewRuleError(CodeInvoicesAlreadyPaid, inv.ID.String())
		}
		total += inv.TotalDueAmount
	}
	return total, first.Currency, nil
}

// CanRequestTransition reports whether a payment request's payment status
// may go from one value to another: pending → succeeded or failed, and
// failed → pending (a new attempt) or succeeded.
func CanRequestTransition(from, to core.PaymentStatus) bool {
	switch from {
	case core.PaymentStatusPending:
		return to == core.PaymentStatusSucceeded || to == core.PaymentStatusFailed
	case core.PaymentStatusFailed:
		return to == core.PaymentStatusPending || to == core.PaymentStatusSucceeded
	default:
		return false
	}
}

// TransitionPaymentStatus moves the request's payment status to to at now.
func (r *Request) TransitionPaymentStatus(to core.PaymentStatus, now time.Time) error {
	if !CanRequestTransition(r.PaymentStatus, to) {
		return &core.TransitionError{Entity: "payment_request", From: string(r.PaymentStatus), To: string(to)}
	}
	r.PaymentStatus = to
	r.UpdatedAt = now
	return nil
}

// IncrementPaymentAttempts records one more payment attempt at now.
func (r *Request) IncrementPaymentAttempts(now time.Time) {
	r.PaymentAttempts++
	r.UpdatedAt = now
}
