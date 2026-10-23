package invoice

import (
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Deductions are the amounts deducted from an invoice's fees, in the order
// of domain §22.7, in minor units. Each is capped at the amount left when
// [ComputeTotals] applies it.
type Deductions struct {
	// ProgressiveBillingCredit is step 1.
	ProgressiveBillingCredit int64
	// Coupons is step 2.
	Coupons int64
	// Taxes is step 3: Σ per tax code of the rounded fee taxes.
	Taxes int64
	// CreditNotes is step 4.
	CreditNotes int64
	// PrepaidCredit is step 5.
	PrepaidCredit int64
}

// Totals are the totals of an invoice (domain §11.3), in minor units.
type Totals struct {
	// FeesAmount is Σ fee amounts.
	FeesAmount int64
	// ProgressiveBillingCreditAmount is the applied step 1 deduction.
	ProgressiveBillingCreditAmount int64
	// CouponsAmount is the applied coupon deduction.
	CouponsAmount int64
	// SubTotalExcludingTaxesAmount is fees − progressive credits − coupons.
	SubTotalExcludingTaxesAmount int64
	// TaxesAmount is the tax amount.
	TaxesAmount int64
	// SubTotalIncludingTaxesAmount is the subtotal plus taxes.
	SubTotalIncludingTaxesAmount int64
	// CreditNotesAmount is the applied credit-note deduction.
	CreditNotesAmount int64
	// PrepaidCreditAmount is the applied wallet deduction.
	PrepaidCreditAmount int64
	// TotalAmount is the amount to pay.
	TotalAmount int64
}

// ComputeTotals computes the totals of §11.3 from the fee amounts and the
// requested deductions. Negative inputs count as 0, every deduction is
// capped at what is left, and no total is negative.
func ComputeTotals(feeAmounts []int64, d Deductions) Totals {
	var t Totals
	for _, a := range feeAmounts {
		t.FeesAmount += max(0, a)
	}
	left := t.FeesAmount
	take := func(want int64) int64 {
		n := min(max(0, want), left)
		left -= n
		return n
	}
	t.ProgressiveBillingCreditAmount = take(d.ProgressiveBillingCredit)
	t.CouponsAmount = take(d.Coupons)
	t.SubTotalExcludingTaxesAmount = left
	t.TaxesAmount = max(0, d.Taxes)
	left += t.TaxesAmount
	t.SubTotalIncludingTaxesAmount = left
	t.CreditNotesAmount = take(d.CreditNotes)
	t.PrepaidCreditAmount = take(d.PrepaidCredit)
	t.TotalAmount = left
	return t
}

// ApplyTotals copies t onto the invoice; a zero total marks the invoice
// paid (§22.7).
func (inv *Invoice) ApplyTotals(t Totals) {
	inv.FeesAmount = t.FeesAmount
	inv.ProgressiveBillingCreditAmount = t.ProgressiveBillingCreditAmount
	inv.CouponsAmount = t.CouponsAmount
	inv.SubTotalExcludingTaxesAmount = t.SubTotalExcludingTaxesAmount
	inv.TaxesAmount = t.TaxesAmount
	inv.SubTotalIncludingTaxesAmount = t.SubTotalIncludingTaxesAmount
	inv.CreditNotesAmount = t.CreditNotesAmount
	inv.PrepaidCreditAmount = t.PrepaidCreditAmount
	inv.TotalAmount = t.TotalAmount
	if t.TotalAmount == 0 {
		inv.PaymentStatus = core.PaymentStatusSucceeded
	}
}

// FinalStatusFacts are the inputs of [FinalStatus].
type FinalStatusFacts struct {
	// GracePeriodDays is the applicable grace period; > 0 gives a draft
	// for subscription invoices.
	GracePeriodDays int
	// UsesGracePeriod is false for one-off, credit and in-advance-charge
	// invoices, which never use a grace period (§22.12).
	UsesGracePeriod bool
	// Gated is true when an activation rule gates the invoice (§9.3).
	Gated bool
	// TotalAmount is the invoice total; a gated invoice with a zero total
	// is not held open.
	TotalAmount int64
	// TaxesPending is true while provider or VIES taxes are pending.
	TaxesPending bool
	// FeesAmount is Σ fee amounts.
	FeesAmount int64
	// FinalizeZeroAmount is the resolved zero-amount finalization setting
	// (customer finalize/skip, else the billing entity's flag).
	FinalizeZeroAmount bool
}

// FinalStatus decides the status an invoice leaves generating with (domain
// §22.12): draft during a grace period, open when gated or waiting for
// taxes, finalized when it has fees or zero-amount invoices are finalized,
// closed otherwise.
func FinalStatus(f FinalStatusFacts) Status {
	switch {
	case f.UsesGracePeriod && f.GracePeriodDays > 0:
		return StatusDraft
	case (f.Gated && f.TotalAmount > 0) || f.TaxesPending:
		return StatusOpen
	case f.FeesAmount != 0 || f.FinalizeZeroAmount:
		return StatusFinalized
	default:
		return StatusClosed
	}
}

// IssuingDateFacts are the inputs of [IssuingDateOffset].
type IssuingDateFacts struct {
	// Recurring is true for periodic invoices; starting, terminating and
	// upgrade invoices are non-recurring.
	Recurring bool
	// UsesBillingDate is true for gated and in-advance-charge invoices,
	// which are issued on the billing date.
	UsesBillingDate bool
	// Anchor is the resolved issuing date anchor.
	Anchor core.IssuingDateAnchor
	// Adjustment is the resolved issuing date adjustment.
	Adjustment core.IssuingDateAdjustment
	// GracePeriodDays is the applicable grace period.
	GracePeriodDays int
}

// IssuingDateOffset returns the number of days added to the local billing
// date to get the issuing date (the table of domain §22.12).
func IssuingDateOffset(f IssuingDateFacts) int {
	switch {
	case f.UsesBillingDate:
		return 0
	case !f.Recurring:
		return f.GracePeriodDays
	case f.Anchor == core.IssuingDateAnchorCurrentPeriodEnd && f.Adjustment == core.IssuingDateAdjustmentKeepAnchor:
		return -1
	case f.Anchor == core.IssuingDateAnchorCurrentPeriodEnd:
		if f.GracePeriodDays == 0 {
			return -1
		}
		return f.GracePeriodDays
	case f.Adjustment == core.IssuingDateAdjustmentKeepAnchor:
		return 0
	default:
		return f.GracePeriodDays
	}
}

// IssuingDate returns the issuing date of an invoice billed on billingDate.
func IssuingDate(billingDate core.Date, f IssuingDateFacts) core.Date {
	return billingDate.AddDays(IssuingDateOffset(f))
}

// KeepsIssuingDateOnFinalization reports whether finalizing a draft (or
// completing pending taxes) keeps the issuing date: only recurring
// keep_anchor invoices do; others are issued on the finalization day.
func KeepsIssuingDateOnFinalization(f IssuingDateFacts) bool {
	return f.Recurring && f.Adjustment == core.IssuingDateAdjustmentKeepAnchor
}

// PaymentDueDate returns issuing + net payment term days (§22.12).
func PaymentDueDate(issuing core.Date, netPaymentTermDays int) core.Date {
	return issuing.AddDays(netPaymentTermDays)
}

// ExpectedFinalizationDate returns billing date + grace period.
func ExpectedFinalizationDate(billingDate core.Date, gracePeriodDays int) core.Date {
	return billingDate.AddDays(gracePeriodDays)
}

// DraftNumber returns the number of a draft invoice: "<prefix>-DRAFT".
func DraftNumber(prefix string) string { return prefix + "-DRAFT" }

// PerCustomerNumber returns "<prefix>-<customer seq %03d>-<seq %03d>", the
// number of invoices of per_customer entities and of self-billed invoices.
func PerCustomerNumber(prefix string, customerSequentialID, sequentialID int64) string {
	return fmt.Sprintf("%s-%03d-%03d", prefix, customerSequentialID, sequentialID)
}

// PerBillingEntityNumber returns "<prefix>-<YYYYMM>-<seq %03d>", with
// YYYYMM the month of finalizedAt in the billing entity's time zone loc.
func PerBillingEntityNumber(prefix string, finalizedAt time.Time, loc *time.Location, billingEntitySequentialID int64) string {
	return fmt.Sprintf("%s-%s-%03d", prefix, finalizedAt.In(loc).Format("200601"), billingEntitySequentialID)
}
