package creditnote

import (
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CreditNote credits, refunds or offsets part of a finalized invoice
// (domain §13.1). Amounts are in minor units of Currency.
type CreditNote struct {
	// ID identifies the credit note.
	ID core.CreditNoteID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the credited invoice.
	InvoiceID core.InvoiceID
	// CustomerID is the invoice's customer.
	CustomerID core.CustomerID
	// Number is "<invoice number>-CN<sequence %03d>", see [Number].
	Number string
	// SequentialID is the per-invoice sequence.
	SequentialID int64
	// Status is the document status.
	Status Status
	// CreditStatus is set when CreditAmount > 0.
	CreditStatus CreditStatus
	// RefundStatus is set when RefundAmount > 0.
	RefundStatus core.RefundStatus
	// Reason is why the credit note is issued.
	Reason Reason
	// Description is free text.
	Description *string

	// CreditAmount is the part credited to the customer's balance.
	CreditAmount int64
	// RefundAmount is the part refunded through the payment provider.
	RefundAmount int64
	// OffsetAmount is the part offset against the invoice's due amount.
	OffsetAmount int64
	// BalanceAmount is the credit left to apply to later invoices.
	BalanceAmount int64
	// SubTotalExcludingTaxesAmount is Σ items − coupons adjustment.
	SubTotalExcludingTaxesAmount int64
	// CouponsAdjustmentAmount is the coupon share removed from the items.
	CouponsAdjustmentAmount int64
	// PreciseCouponsAdjustmentAmount is CouponsAdjustmentAmount unrounded.
	PreciseCouponsAdjustmentAmount money.Decimal
	// TaxesAmount is the sum of the applied taxes.
	TaxesAmount int64
	// PreciseTaxesAmount is TaxesAmount unrounded.
	PreciseTaxesAmount money.Decimal
	// TaxesRate is the weighted tax rate.
	TaxesRate money.Decimal
	// TotalAmount is credit + refund + offset.
	TotalAmount int64
	// Currency is the invoice currency.
	Currency money.Currency

	// Items are the credited fee amounts.
	Items []Item
	// AppliedTaxes are the taxes, grouped by invoice applied tax.
	AppliedTaxes []AppliedTax
	// IssuingDate is the issuing date.
	IssuingDate core.Date
	// Metadata is free key/value data (≤ 5 items).
	Metadata core.Metadata
	// File is the rendered PDF.
	File core.BlobRef
	// XMLFile is the e-invoice XML.
	XMLFile core.BlobRef
	// RefundedAt is the instant the refund succeeded.
	RefundedAt *time.Time
	// VoidedAt is the instant the credit was voided.
	VoidedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Item is the amount credited on one fee.
type Item struct {
	// ID identifies the item.
	ID core.CreditNoteItemID
	// FeeID is the credited fee; zero only while the draft invoice is
	// refreshed ([Repository.DetachFees]).
	FeeID core.FeeID
	// Amount is the credited amount in minor units.
	Amount int64
	// PreciseAmount is Amount unrounded.
	PreciseAmount money.Decimal
}

// AppliedTax is one tax of a credit note.
type AppliedTax struct {
	// ID identifies the applied tax.
	ID core.CreditNoteAppliedTaxID
	// TaxID is the tax applied.
	TaxID core.TaxID
	// TaxCode is the tax code snapshot.
	TaxCode string
	// TaxName is the tax name snapshot.
	TaxName string
	// TaxDescription is the tax description snapshot.
	TaxDescription *string
	// TaxRate is the rate in percent.
	TaxRate money.Decimal
	// BaseAmount is the taxed amount.
	BaseAmount int64
	// Amount is the tax amount.
	Amount int64
	// PreciseAmount is Amount unrounded.
	PreciseAmount money.Decimal
}

// Clone returns a deep copy of cn.
func Clone(cn CreditNote) CreditNote {
	cn.Items = slices.Clone(cn.Items)
	cn.AppliedTaxes = slices.Clone(cn.AppliedTaxes)
	cn.Metadata = slices.Clone(cn.Metadata)
	return cn
}

// Number returns "<invoice number>-CN<sequence %03d>".
func Number(invoiceNumber string, sequentialID int64) string {
	return fmt.Sprintf("%s-CN%03d", invoiceNumber, sequentialID)
}

// Credited reports whether part of the note credits the balance.
func (cn *CreditNote) Credited() bool { return cn.CreditAmount > 0 }

// Refunded reports whether part of the note is refunded.
func (cn *CreditNote) Refunded() bool { return cn.RefundAmount > 0 }

// HasOffset reports whether part of the note offsets the invoice.
func (cn *CreditNote) HasOffset() bool { return cn.OffsetAmount > 0 }

// ItemsAmount returns Σ item amounts.
func (cn *CreditNote) ItemsAmount() int64 {
	var n int64
	for _, it := range cn.Items {
		n += it.Amount
	}
	return n
}

// PreciseTotal returns Σ precise items − precise coupons + precise taxes.
func (cn *CreditNote) PreciseTotal() money.Decimal {
	t := money.Zero
	for _, it := range cn.Items {
		t = t.Add(it.PreciseAmount)
	}
	return t.Sub(cn.PreciseCouponsAdjustmentAmount).Add(cn.PreciseTaxesAmount)
}

// Validate checks the invariants of domain §13.1.
func (cn *CreditNote) Validate() error {
	var v core.Validator
	v.Check(!cn.InvoiceID.IsZero(), "invoice_id", core.CodeRequired)
	v.Check(!cn.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	v.Enum("status", cn.Status)
	v.Enum("reason", cn.Reason)
	v.Currency("currency", cn.Currency)
	v.NonNegative("credit_amount", cn.CreditAmount)
	v.NonNegative("refund_amount", cn.RefundAmount)
	v.NonNegative("offset_amount", cn.OffsetAmount)
	v.NonNegative("balance_amount", cn.BalanceAmount)
	v.NonNegative("total_amount", cn.TotalAmount)
	if cn.CreditAmount > 0 {
		v.Enum("credit_status", cn.CreditStatus)
	} else {
		v.OptionalEnum("credit_status", cn.CreditStatus)
	}
	if cn.RefundAmount > 0 {
		v.Enum("refund_status", cn.RefundStatus)
	} else {
		v.OptionalEnum("refund_status", cn.RefundStatus)
	}
	v.Check(cn.BalanceAmount <= cn.CreditAmount, "balance_amount", core.CodeOutOfRange)
	for i, it := range cn.Items {
		v.Check(!it.FeeID.IsZero(), fmt.Sprintf("items[%d].fee_id", i), core.CodeRequired)
		v.NonNegative(fmt.Sprintf("items[%d].amount", i), it.Amount)
	}
	if len(cn.Metadata) > 0 {
		v.Merge("metadata", cn.Metadata.Validate(core.InvoiceMetadataLimits()))
	}
	return v.Err()
}

// CanTransition reports whether the document status may move from from to
// to: only draft → finalized.
func CanTransition(from, to Status) bool { return from == StatusDraft && to == StatusFinalized }

// CanTransitionCredit reports whether the credit status may move from from
// to to: available → consumed | voided, and consumed → available when a
// consumed credit is given back (recredit, after the invoice it paid is
// voided). voided is terminal.
func CanTransitionCredit(from, to CreditStatus) bool {
	switch from {
	case CreditStatusAvailable:
		return to == CreditStatusConsumed || to == CreditStatusVoided
	case CreditStatusConsumed:
		return to == CreditStatusAvailable
	default:
		return false
	}
}

// CanTransitionRefund reports whether the refund status may move from from
// to to: pending → succeeded | failed.
func CanTransitionRefund(from, to core.RefundStatus) bool {
	return from == core.RefundStatusPending && (to == core.RefundStatusSucceeded || to == core.RefundStatusFailed)
}

// Finalize finalizes a draft credit note at now.
func (cn *CreditNote) Finalize(now time.Time) error {
	if !CanTransition(cn.Status, StatusFinalized) {
		return &core.TransitionError{Entity: "credit_note", From: string(cn.Status), To: string(StatusFinalized)}
	}
	cn.Status = StatusFinalized
	cn.UpdatedAt = now
	return nil
}

// Voidable reports whether the credit can be voided: a balance is left and
// it is not voided.
func (cn *CreditNote) Voidable() bool {
	return cn.CreditStatus != CreditStatusVoided && cn.BalanceAmount > 0
}

// Void voids the remaining credit at now: the balance drops to 0.
func (cn *CreditNote) Void(now time.Time) error {
	if !cn.Voidable() {
		return core.NewRuleError("no_voidable_amount", "credit note has no balance to void")
	}
	if !CanTransitionCredit(cn.CreditStatus, CreditStatusVoided) {
		return &core.TransitionError{Entity: "credit_note", From: string(cn.CreditStatus), To: string(CreditStatusVoided)}
	}
	cn.CreditStatus = CreditStatusVoided
	cn.BalanceAmount = 0
	cn.VoidedAt = &now
	cn.UpdatedAt = now
	return nil
}

// ConsumeBalance applies up to amount of the balance to an invoice at now
// and returns the amount applied; the credit becomes consumed at 0.
func (cn *CreditNote) ConsumeBalance(amount int64, now time.Time) (int64, error) {
	if cn.CreditStatus != CreditStatusAvailable {
		return 0, &core.TransitionError{Entity: "credit_note", From: string(cn.CreditStatus), To: string(CreditStatusConsumed)}
	}
	n := min(max(0, amount), cn.BalanceAmount)
	cn.BalanceAmount -= n
	if cn.BalanceAmount == 0 {
		cn.CreditStatus = CreditStatusConsumed
	}
	cn.UpdatedAt = now
	return n, nil
}

// Recredit gives back amount of consumed credit at now (the invoice it paid
// was voided); a voided credit cannot be recredited.
func (cn *CreditNote) Recredit(amount int64, now time.Time) error {
	if cn.CreditStatus == CreditStatusVoided {
		return core.NewRuleError("credit_note_voided", "credit note is voided")
	}
	cn.BalanceAmount += max(0, amount)
	cn.CreditStatus = CreditStatusAvailable
	cn.UpdatedAt = now
	return nil
}

// SetRefundStatus moves the refund status to to at now; succeeded records
// RefundedAt.
func (cn *CreditNote) SetRefundStatus(to core.RefundStatus, now time.Time) error {
	if !CanTransitionRefund(cn.RefundStatus, to) {
		return &core.TransitionError{Entity: "credit_note_refund", From: string(cn.RefundStatus), To: string(to)}
	}
	cn.RefundStatus = to
	if to == core.RefundStatusSucceeded {
		cn.RefundedAt = &now
	}
	cn.UpdatedAt = now
	return nil
}
