package payment

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// MaxReferenceLength bounds the reference of a manual payment.
const MaxReferenceLength = 40

// Error codes of payments, reusing lago's names.
const (
	// CodeInvalidAmount: a manual payment of a credit invoice differs from
	// its total.
	CodeInvalidAmount = "invalid_amount"
	// CodeGreaterThan: a manual payment exceeds the amount due.
	CodeGreaterThan = "greater_than"
	// CodePaymentRequestSucceeded: the invoice was already paid through a
	// succeeded payment request.
	CodePaymentRequestSucceeded = "payment_request_is_already_succeeded"
	// CodeInvalidPaymentMethod: a payment method choice that cannot be
	// honored.
	CodeInvalidPaymentMethod = "invalid_payment_method"
)

// Payable is the document a payment settles: an invoice or a payment
// request (domain §15.1). Exactly the ID matching Type is set.
type Payable struct {
	// Type is the kind of document.
	Type PayableType
	// InvoiceID is the invoice, when Type is invoice.
	InvoiceID core.InvoiceID
	// PaymentRequestID is the payment request, when Type is
	// payment_request.
	PaymentRequestID core.PaymentRequestID
}

// InvoicePayable returns the payable of an invoice.
func InvoicePayable(id core.InvoiceID) Payable {
	return Payable{Type: PayableTypeInvoice, InvoiceID: id}
}

// RequestPayable returns the payable of a payment request.
func RequestPayable(id core.PaymentRequestID) Payable {
	return Payable{Type: PayableTypePaymentRequest, PaymentRequestID: id}
}

// Validate checks that exactly the ID matching Type is set.
func (p Payable) Validate() error {
	var v core.Validator
	if v.Enum("payable_type", p.Type) {
		switch p.Type {
		case PayableTypeInvoice:
			v.Check(!p.InvoiceID.IsZero() && p.PaymentRequestID.IsZero(), "payable_id", core.CodeInvalid)
		case PayableTypePaymentRequest:
			v.Check(!p.PaymentRequestID.IsZero() && p.InvoiceID.IsZero(), "payable_id", core.CodeInvalid)
		}
	}
	return v.Err()
}

// String returns "<type>:<id>", a stable key of the payable.
func (p Payable) String() string {
	if p.Type == PayableTypePaymentRequest {
		return string(p.Type) + ":" + p.PaymentRequestID.String()
	}
	return string(p.Type) + ":" + p.InvoiceID.String()
}

// MethodDetails describes a payment method as the provider reports it
// (domain §15.5): its type and, for cards, brand, last 4 digits and expiry.
type MethodDetails struct {
	// Type is the provider's method type, such as "card" or "sepa_debit".
	Type string
	// Brand is the card brand, such as "visa".
	Brand string
	// Last4 is the last 4 digits of the card or account.
	Last4 string
	// ExpMonth is the card's expiry month, 1–12; 0 when unknown.
	ExpMonth int
	// ExpYear is the card's expiry year; 0 when unknown.
	ExpYear int
}

// IsZero reports whether no detail is known.
func (d MethodDetails) IsZero() bool { return d == MethodDetails{} }

// CardLastDigits returns "**** <last 4>" for a card, or "" otherwise.
func (d MethodDetails) CardLastDigits() string {
	if d.Type != "card" {
		return ""
	}
	return "**** " + d.Last4
}

// DisplayName returns "<Brand> **** <last 4>" for a card, the titleized
// type otherwise ("bank_transfer" → "Bank Transfer"), or "" when no detail
// is known.
func (d MethodDetails) DisplayName() string {
	if d.IsZero() {
		return ""
	}
	if d.Type == "card" {
		return titleize(d.Brand) + " " + d.CardLastDigits()
	}
	return titleize(d.Type)
}

func titleize(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == ' ' || r == '-' })
	for i, w := range words {
		r := []rune(strings.ToLower(w))
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// Payment is one attempt to collect a payable (domain §15.1).
type Payment struct {
	// ID identifies the payment.
	ID core.PaymentID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Payable is the settled invoice or payment request.
	Payable Payable
	// CustomerID is the paying customer.
	CustomerID core.CustomerID
	// Kind tells how the payment is collected.
	Kind Kind
	// Status is the lifecycle status.
	Status State
	// ProviderStatus is the raw provider status.
	ProviderStatus *string
	// Amount is the collected amount, in minor units of Currency.
	Amount int64
	// Currency is the payable's currency.
	Currency money.Currency
	// Reference identifies a manual payment, ≤ 40 characters; required for
	// manual payments and absent otherwise.
	Reference *string
	// PaidAt is when a manual payment was made.
	PaidAt *time.Time
	// IntegrationID is the provider connection of a provider payment.
	IntegrationID core.IntegrationID
	// ProviderPaymentID is the provider's payment identifier.
	ProviderPaymentID *string
	// ProviderPaymentMethodID is the provider's payment method identifier.
	ProviderPaymentMethodID *string
	// ProviderPaymentData is the provider's raw payment data.
	ProviderPaymentData json.RawMessage
	// ProviderPaymentMethod describes the method the provider charged.
	ProviderPaymentMethod MethodDetails
	// PaymentMethodID is the customer's payment method charged.
	PaymentMethodID core.PaymentMethodID
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the invariants of domain §15.1 that need no other
// aggregate; [Payment.ValidateManual] checks a manual payment against its
// invoice.
func (p *Payment) Validate() error {
	var v core.Validator
	v.Merge("", p.Payable.Validate())
	v.Enum("payment_type", p.Kind)
	v.Enum("status", p.Status)
	v.NonNegative("amount", p.Amount)
	v.Currency("currency", p.Currency)
	switch p.Kind {
	case KindManual:
		if v.Check(p.Reference != nil, "reference", core.CodeRequired) && v.Required("reference", *p.Reference) {
			v.MaxLen("reference", *p.Reference, MaxReferenceLength)
		}
	case KindProvider:
		v.Check(p.Reference == nil, "reference", core.CodeForbidden)
	}
	return v.Err()
}

// InvoiceFacts are the facts of an invoice a manual payment is checked
// against.
type InvoiceFacts struct {
	// Credit reports a credit (wallet top-up) invoice.
	Credit bool
	// TotalAmount is the invoice total, in minor units.
	TotalAmount int64
	// TotalPaidAmount is what was paid so far, in minor units.
	TotalPaidAmount int64
	// FinalizedOffsetAmount is Σ offset_amount of the invoice's finalized
	// credit notes, in minor units (draft credit notes do not count).
	FinalizedOffsetAmount int64
	// SucceededPaymentRequest reports a succeeded payment request including
	// the invoice.
	SucceededPaymentRequest bool
}

// DueAmount returns total − paid − finalized offsets, never below 0.
func (f InvoiceFacts) DueAmount() int64 {
	return max(f.TotalAmount-f.TotalPaidAmount-f.FinalizedOffsetAmount, 0)
}

// ValidateManual checks a new manual payment of an invoice (domain §15.1):
// the invoice was not paid by a succeeded payment request
// ([core.RuleError] payment_request_is_already_succeeded), a credit invoice
// is paid in full, and the amount does not exceed the amount due. Provider
// payments and payment request payables are not checked.
func (p *Payment) ValidateManual(f InvoiceFacts) error {
	if p.Kind != KindManual || p.Payable.Type != PayableTypeInvoice {
		return nil
	}
	if f.SucceededPaymentRequest {
		return core.NewRuleError(CodePaymentRequestSucceeded, "the invoice was paid through a payment request")
	}
	var v core.Validator
	if f.Credit {
		v.Check(p.Amount == f.TotalAmount, "amount", CodeInvalidAmount)
	}
	v.Check(p.Amount <= f.DueAmount(), "amount", CodeGreaterThan)
	return v.Err()
}

// Final reports whether the payment reached a final status.
func (p *Payment) Final() bool { return p.Status == StateSucceeded || p.Status == StateFailed }

// CanTransition reports whether a payment may go from one state to another:
// pending → processing, succeeded or failed; processing → succeeded or
// failed.
func CanTransition(from, to State) bool {
	switch from {
	case StatePending:
		return to == StateProcessing || to == StateSucceeded || to == StateFailed
	case StateProcessing:
		return to == StateSucceeded || to == StateFailed
	default:
		return false
	}
}

// Transition moves the payment to state to at now, recording the raw
// provider status (nil keeps the current one).
func (p *Payment) Transition(to State, providerStatus *string, now time.Time) error {
	if !CanTransition(p.Status, to) {
		return &core.TransitionError{Entity: "payment", From: string(p.Status), To: string(to)}
	}
	p.Status = to
	if providerStatus != nil {
		p.ProviderStatus = providerStatus
	}
	p.UpdatedAt = now
	return nil
}
