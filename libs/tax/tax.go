package tax

import (
	"errors"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Errors returned by the computations.
var (
	// ErrInvoiceAppliedTaxNotFound reports a fee tax that resolves to no tax
	// of its invoice, so a credit note cannot know what to credit.
	ErrInvoiceAppliedTaxNotFound = errors.New("tax: invoice applied tax not found")
	// ErrFeeMissingFromTaxResponse reports a taxable fee for which the tax
	// provider returned no result.
	ErrFeeMissingFromTaxResponse = errors.New("tax: fee missing from tax response")
	// ErrFeeOutOfRange reports a credit note item pointing to no fee of the
	// invoice.
	ErrFeeOutOfRange = errors.New("tax: credit note item fee out of range")
)

// Tax is a tax definition: one of an organization's taxes, or one generated
// for EU VAT.
type Tax struct {
	// Code identifies the tax within its organization.
	Code string
	// Name is the display name.
	Name string
	// Description is an optional description.
	Description string
	// Rate is the rate in percent.
	Rate money.Decimal
}

// FeeType is the kind of a fee; it decides which tax sources apply.
type FeeType string

// Fee types.
const (
	FeeCharge       FeeType = "charge"
	FeeAddOn        FeeType = "add_on"
	FeeSubscription FeeType = "subscription"
	FeeCredit       FeeType = "credit"
	FeeCommitment   FeeType = "commitment"
	FeeFixedCharge  FeeType = "fixed_charge"
	FeeProduct      FeeType = "product"
)

// FeeAppliedTax is a tax applied to a fee.
type FeeAppliedTax struct {
	// TaxCode, TaxName, TaxDescription and TaxRate (percent) copy the tax.
	// For a provider tax the code is the parameterized jurisdiction name and
	// the description the provider's tax type.
	TaxCode        string
	TaxName        string
	TaxDescription string
	TaxRate        money.Decimal
	// Provider is true for a tax computed by a tax provider (no tax
	// definition behind it).
	Provider bool
	// AmountCents is the booked tax amount.
	AmountCents int64
	// PreciseAmountCents is the unrounded tax amount.
	PreciseAmountCents money.Decimal
}

// FeeTaxes are the tax fields of a fee.
type FeeTaxes struct {
	// AppliedTaxes are the taxes applied to the fee, in order.
	AppliedTaxes []FeeAppliedTax
	// TaxesAmountCents is the rounded sum of the unrounded tax amounts
	// (internal taxes), or the sum of the booked amounts (provider taxes).
	TaxesAmountCents int64
	// TaxesPreciseAmountCents is the unrounded sum.
	TaxesPreciseAmountCents money.Decimal
	// TaxesRate is the sum of the applied rates, in percent.
	TaxesRate money.Decimal
	// TaxesBaseRate is the taxable share of the fee: 1 for a fully taxable
	// fee, less when a provider taxed only part of it. It has no implicit
	// default: a zero value means nothing is taxable. [ApplyFeeTaxes] sets
	// it to 1.
	TaxesBaseRate money.Decimal
}

// Fee is the part of a fee that taxes depend on.
type Fee struct {
	// Type is the fee type.
	Type FeeType
	// AmountCents is the rounded fee amount.
	AmountCents int64
	// PreciseAmountCents is the unrounded fee amount.
	PreciseAmountCents money.Decimal
	// PreciseCouponsAmountCents is the share of the invoice's coupons
	// allocated to the fee, unrounded.
	PreciseCouponsAmountCents money.Decimal
	FeeTaxes
}

// SubTotalExcludingTaxesAmountCents is the taxable amount of the fee: its
// rounded amount less its coupons share.
func (f Fee) SubTotalExcludingTaxesAmountCents() money.Decimal {
	return money.NewFromInt(f.AmountCents).Sub(f.PreciseCouponsAmountCents)
}

// SubTotalExcludingTaxesPreciseAmountCents is the unrounded amount of the
// fee less its coupons share.
func (f Fee) SubTotalExcludingTaxesPreciseAmountCents() money.Decimal {
	return f.PreciseAmountCents.Sub(f.PreciseCouponsAmountCents)
}

// Taxable reports whether the fee has a taxable amount (positive or
// negative).
func (f Fee) Taxable() bool {
	return !f.SubTotalExcludingTaxesAmountCents().IsZero()
}

// WholeInvoiceTaxCodes are provider tax codes that apply to the whole invoice
// rather than to a jurisdiction (zero-tax rulings).
var wholeInvoiceTaxCodes = []string{
	"not_collecting", "juris_not_taxed", "reverse_charge", "customer_exempt",
	"transaction_exempt", "juris_has_no_tax", "unknown_taxation",
}

// InvoiceAppliedTax is a tax applied to an invoice: one per tax (internal
// taxes) or per jurisdiction (provider taxes), summing the fees it applies
// to.
type InvoiceAppliedTax struct {
	TaxCode        string
	TaxName        string
	TaxDescription string
	// TaxRate is the rate in percent.
	TaxRate money.Decimal
	// Provider is true for a tax computed by a tax provider.
	Provider bool
	// AmountCents is the booked tax amount.
	AmountCents int64
	// FeesAmountCents is the taxable amount of the fees the tax applies to
	// (after coupons), rounded half up.
	FeesAmountCents int64
	// TaxableBaseAmountCents is the part of FeesAmountCents a provider
	// actually taxed, rounded half up; zero when not reported.
	TaxableBaseAmountCents int64
}

// TaxableAmountCents is the taxable base, falling back to the fees amount
// when no base was reported.
func (t InvoiceAppliedTax) TaxableAmountCents() int64 {
	if t.TaxableBaseAmountCents == 0 {
		return t.FeesAmountCents
	}
	return t.TaxableBaseAmountCents
}

// IsProviderTax reports whether the tax was computed by a tax provider on a
// reported taxable base. Provider rows without a base (legacy rows) are
// credited like internal taxes, at their rate.
func (t InvoiceAppliedTax) IsProviderTax() bool {
	return t.Provider && t.TaxableBaseAmountCents > 0
}

// AppliedOnWholeInvoice reports whether the tax is a provider ruling that
// covers the whole invoice (not collecting, reverse charge, exemptions…).
func (t InvoiceAppliedTax) AppliedOnWholeInvoice() bool {
	for _, c := range wholeInvoiceTaxCodes {
		if t.TaxCode == c {
			return true
		}
	}
	return false
}

// Invoice is the part of an invoice that tax splits and credit notes depend
// on.
type Invoice struct {
	// TaxesAmountCents is the invoice's booked tax total.
	TaxesAmountCents int64
	// AppliedTaxes are the invoice's taxes, in order.
	AppliedTaxes []InvoiceAppliedTax
	// Fees are the invoice's fees with their applied taxes, ordered by
	// creation (then identifier); the order decides where rounding cents
	// land.
	Fees []Fee
}

// ProviderTaxes reports whether any of the invoice's taxes is a provider
// tax.
func (inv Invoice) ProviderTaxes() bool {
	for _, t := range inv.AppliedTaxes {
		if t.IsProviderTax() {
			return true
		}
	}
	return false
}

// AppliedTaxFor returns the index of the invoice tax a fee tax resolves to:
// the one with the same code and rate, or else the only one with the same
// code. Several invoice taxes sharing a code (provider multi-rate) are told
// apart only by their rate, so no guess is made among them.
func (inv Invoice) AppliedTaxFor(t FeeAppliedTax) (int, bool) {
	for i, it := range inv.AppliedTaxes {
		if it.TaxCode == t.TaxCode && it.TaxRate.Equal(t.TaxRate) {
			return i, true
		}
	}
	found := -1
	for i, it := range inv.AppliedTaxes {
		if it.TaxCode == t.TaxCode {
			if found >= 0 {
				return 0, false
			}
			found = i
		}
	}
	return found, found >= 0
}

// roundCents rounds d half up to whole cents.
func roundCents(d money.Decimal) int64 {
	n, _ := d.Round(0, money.HalfUp).Int64()
	return n
}

// percentOf returns base × rate ÷ 100.
func percentOf(base, rate money.Decimal) money.Decimal {
	return base.Mul(rate).Div(money.Hundred)
}

// rateKey is a map key for a decimal rate, equal for equal values.
func rateKey(d money.Decimal) string {
	return d.Reduce().String()
}
