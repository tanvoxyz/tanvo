package invoice

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Invoice is a billing document of a customer (domain §11.1). Amounts are
// in minor units of Currency.
type Invoice struct {
	// ID identifies the invoice.
	ID core.InvoiceID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CustomerID is the billed customer.
	CustomerID core.CustomerID
	// BillingEntityID is the issuer.
	BillingEntityID core.BillingEntityID
	// Type is the kind of invoice.
	Type Type
	// Status is the lifecycle status (§11.2).
	Status Status
	// PaymentStatus is the collection status.
	PaymentStatus core.PaymentStatus
	// TaxStatus is set when a tax provider computes the taxes.
	TaxStatus TaxStatus
	// Number is the document number (§22.11); "<prefix>-DRAFT" until
	// finalized.
	Number string
	// SequentialID is the per-(customer, billing entity) sequence, assigned
	// on finalization.
	SequentialID *int64
	// BillingEntitySequentialID is the per-billing-entity sequence over
	// non-self-billed invoices, assigned on finalization.
	BillingEntitySequentialID *int64
	// Currency is the invoice currency.
	Currency money.Currency

	// FeesAmount is Σ fee amounts.
	FeesAmount int64
	// ProgressiveBillingCreditAmount is the deduction of fees already
	// billed by progressive-billing invoices of the period (§22.16).
	ProgressiveBillingCreditAmount int64
	// CouponsAmount is the coupon deduction.
	CouponsAmount int64
	// SubTotalExcludingTaxesAmount is fees − progressive credits − coupons.
	SubTotalExcludingTaxesAmount int64
	// TaxesAmount is the sum of the invoice applied taxes.
	TaxesAmount int64
	// TaxesRate is the weighted tax rate, 5 decimal places.
	TaxesRate money.Decimal
	// SubTotalIncludingTaxesAmount is the subtotal plus taxes.
	SubTotalIncludingTaxesAmount int64
	// CreditNotesAmount is the credit-note credit applied.
	CreditNotesAmount int64
	// PrepaidCreditAmount is the wallet credit applied.
	PrepaidCreditAmount int64
	// PrepaidGrantedCreditAmount is the granted share of PrepaidCreditAmount.
	PrepaidGrantedCreditAmount int64
	// PrepaidPurchasedCreditAmount is the purchased share of
	// PrepaidCreditAmount.
	PrepaidPurchasedCreditAmount int64
	// TotalAmount is the amount to pay.
	TotalAmount int64
	// TotalPaidAmount is the amount paid so far.
	TotalPaidAmount int64
	// OffsettedAmount is Σ offset amounts of finalized credit notes; a
	// denormalized value maintained with the credit notes (§11.1 derived).
	OffsettedAmount int64

	// IssuingDate is the issuing date in the applicable time zone.
	IssuingDate core.Date
	// PaymentDueDate is IssuingDate + NetPaymentTermDays.
	PaymentDueDate core.Date
	// ExpectedFinalizationDate is the billing date + grace period.
	ExpectedFinalizationDate core.Date
	// AppliedGracePeriodDays is the grace period used.
	AppliedGracePeriodDays int
	// NetPaymentTermDays is the payment term snapshot.
	NetPaymentTermDays int
	// PaymentOverdue is set by the overdue job (§11.5).
	PaymentOverdue bool
	// PaymentDisputeLostAt is set when a payment dispute is lost.
	PaymentDisputeLostAt *time.Time
	// SelfBilled is true for invoices of partner customers.
	SelfBilled bool
	// VoidedInvoiceID is the voided invoice this one regenerates.
	VoidedInvoiceID core.InvoiceID
	// SkipAutomaticPayment disables the automatic payment.
	SkipAutomaticPayment bool
	// PaymentMethodType is how the invoice is collected.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is the payment method to charge.
	PaymentMethodID core.PaymentMethodID
	// Metadata is set after finalization only (≤ 5 items).
	Metadata core.Metadata
	// File is the rendered PDF.
	File core.BlobRef
	// XMLFile is the e-invoice XML.
	XMLFile core.BlobRef
	// ReadyForPaymentProcessing tells whether a payment may be created.
	ReadyForPaymentProcessing bool
	// RefreshRequested flags a draft for the periodic refresh (§22.17).
	RefreshRequested bool
	// FinalizedAt is the finalization instant.
	FinalizedAt *time.Time
	// VoidedAt is the voiding instant.
	VoidedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// TotalDueAmount returns total − paid − offset, never negative; a voided
// invoice owes nothing.
func (inv *Invoice) TotalDueAmount() int64 {
	if inv.Status == StatusVoided {
		return 0
	}
	return max(0, inv.TotalAmount-inv.TotalPaidAmount-inv.OffsettedAmount)
}

// TotalSettledAmount returns paid + offset.
func (inv *Invoice) TotalSettledAmount() int64 { return inv.TotalPaidAmount + inv.OffsettedAmount }

// Visible reports whether the API shows the invoice (every status except
// generating).
func (inv *Invoice) Visible() bool { return inv.Status != StatusGenerating }

// Validate checks the invariants of domain §11.1.
func (inv *Invoice) Validate() error {
	var v core.Validator
	v.Enum("invoice_type", inv.Type)
	v.Enum("status", inv.Status)
	v.Enum("payment_status", inv.PaymentStatus)
	v.OptionalEnum("tax_status", inv.TaxStatus)
	v.Currency("currency", inv.Currency)
	v.NonNegative("fees_amount", inv.FeesAmount)
	v.NonNegative("progressive_billing_credit_amount", inv.ProgressiveBillingCreditAmount)
	v.NonNegative("coupons_amount", inv.CouponsAmount)
	v.NonNegative("sub_total_excluding_taxes_amount", inv.SubTotalExcludingTaxesAmount)
	v.NonNegative("taxes_amount", inv.TaxesAmount)
	v.NonNegative("sub_total_including_taxes_amount", inv.SubTotalIncludingTaxesAmount)
	v.NonNegative("credit_notes_amount", inv.CreditNotesAmount)
	v.NonNegative("prepaid_credit_amount", inv.PrepaidCreditAmount)
	v.NonNegative("total_amount", inv.TotalAmount)
	v.NonNegative("total_paid_amount", inv.TotalPaidAmount)
	v.Check(inv.AppliedGracePeriodDays >= 0, "applied_grace_period_days", core.CodeOutOfRange)
	v.Check(inv.NetPaymentTermDays >= 0, "net_payment_term_days", core.CodeOutOfRange)
	v.OptionalEnum("payment_method_type", inv.PaymentMethodType)
	if len(inv.Metadata) > 0 {
		v.Check(inv.Status != StatusDraft && inv.Status != StatusGenerating, "metadata", core.CodeForbidden)
		v.Merge("metadata", inv.Metadata.Validate(core.InvoiceMetadataLimits()))
	}
	if inv.PaymentDisputeLostAt != nil {
		v.Check(inv.Status == StatusFinalized || inv.Status == StatusVoided, "payment_dispute_lost_at", core.CodeForbidden)
	}
	return v.Err()
}

// CanTransition reports whether an invoice may move from status from to
// status to (domain §11.2). draft → draft is a refresh; closed and voided
// are terminal.
func CanTransition(from, to Status) bool {
	switch from {
	case StatusGenerating:
		return to == StatusDraft || to == StatusOpen || to == StatusFinalized || to == StatusClosed || to == StatusFailed
	case StatusDraft:
		return to == StatusDraft || to == StatusFinalized
	case StatusOpen:
		// open → failed: pulling the provider taxes of an invoice waiting
		// for them failed (lago Invoices::ProviderTaxes::
		// PullTaxesAndApplyService, status = "failed" unless draft).
		return to == StatusFinalized || to == StatusClosed || to == StatusFailed
	case StatusFailed:
		return to == StatusFinalized
	case StatusFinalized:
		return to == StatusVoided
	default:
		return false
	}
}

// Transition moves the invoice to status to at now, setting FinalizedAt or
// VoidedAt; a disallowed change fails with a *core.TransitionError.
func (inv *Invoice) Transition(to Status, now time.Time) error {
	if !CanTransition(inv.Status, to) {
		return &core.TransitionError{Entity: "invoice", From: string(inv.Status), To: string(to)}
	}
	inv.Status = to
	switch to {
	case StatusFinalized:
		inv.FinalizedAt = &now
	case StatusVoided:
		inv.VoidedAt = &now
	}
	inv.UpdatedAt = now
	return nil
}

// Deletable reports whether the invoice can be deleted: only drafts are,
// and deleting removes the row (§11.7).
func (inv *Invoice) Deletable() bool { return inv.Status == StatusDraft }

// VoidFacts are the facts about other aggregates that voidability needs.
type VoidFacts struct {
	// NonVoidedCreditNotes is the number of credit notes of the invoice
	// whose credit status is not voided.
	NonVoidedCreditNotes int
}

// Voidable reports whether the invoice can be voided (§11.4): finalized,
// payment pending or failed, nothing paid, no non-voided credit note and no
// lost dispute.
func (inv *Invoice) Voidable(f VoidFacts) bool {
	if inv.PaymentDisputeLostAt != nil || inv.TotalPaidAmount > 0 || f.NonVoidedCreditNotes > 0 {
		return false
	}
	return inv.Status == StatusFinalized &&
		(inv.PaymentStatus == core.PaymentStatusPending || inv.PaymentStatus == core.PaymentStatusFailed)
}

// Void voids a voidable invoice at now; the restoration of what it consumed
// is done by libs/billing.
func (inv *Invoice) Void(f VoidFacts, now time.Time) error {
	if !inv.Voidable(f) {
		return core.NewRuleError("not_voidable", "invoice cannot be voided")
	}
	return inv.Transition(StatusVoided, now)
}

// MarkDisputeLost records a lost payment dispute on a finalized or voided
// invoice (§11.5).
func (inv *Invoice) MarkDisputeLost(now time.Time) error {
	if inv.Status != StatusFinalized && inv.Status != StatusVoided {
		return core.NewRuleError("not_disputable", "only finalized invoices can lose a dispute")
	}
	inv.PaymentDisputeLostAt = &now
	inv.UpdatedAt = now
	return nil
}

// OverdueCandidate reports whether the overdue job marks the invoice on
// date today (§11.5): finalized, not fully paid, past its due date.
func (inv *Invoice) OverdueCandidate(today core.Date) bool {
	return inv.Status == StatusFinalized && inv.PaymentStatus != core.PaymentStatusSucceeded &&
		!inv.PaymentOverdue && !inv.PaymentDueDate.IsZero() && inv.PaymentDueDate.Before(today)
}

// FinalizationDue reports whether a draft must be finalized on date today
// (§22.12); a draft without an expected date is due.
func (inv *Invoice) FinalizationDue(today core.Date) bool {
	return inv.Status == StatusDraft && !inv.ExpectedFinalizationDate.After(today)
}

// OffsettableAmount returns how much an offset credit note may settle:
// min(total due, creditable), or the total due for credit invoices
// (domain §13.2).
func (inv *Invoice) OffsettableAmount(creditable int64) int64 {
	due := inv.TotalDueAmount()
	if inv.Type == TypeCredit {
		return due
	}
	return min(due, creditable)
}

// CreditableAmount returns how much credit notes may still credit on a
// finalized, non-credit invoice: the sub total including taxes, less the
// amount already credited by non-voided credit notes. Drafts and other
// statuses give 0.
func (inv *Invoice) CreditableAmount(alreadyCredited int64) int64 {
	if inv.Status != StatusFinalized || inv.Type == TypeCredit {
		return 0
	}
	return max(0, inv.SubTotalIncludingTaxesAmount-alreadyCredited)
}

// RefundableAmount returns how much credit notes may still refund: the
// amount paid less what was already refunded, capped by the creditable
// amount (domain §13.2).
func (inv *Invoice) RefundableAmount(creditable, alreadyRefunded int64) int64 {
	if inv.Status != StatusFinalized {
		return 0
	}
	return max(0, min(creditable, inv.TotalPaidAmount-alreadyRefunded))
}
