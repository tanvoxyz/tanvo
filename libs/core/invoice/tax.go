package invoice

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// AppliedTax is an invoice tax line grouped by tax code (domain §22.10):
// a snapshot of the tax with the taxed base and the rounded amount.
type AppliedTax struct {
	// ID identifies the applied tax.
	ID core.InvoiceAppliedTaxID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the invoice.
	InvoiceID core.InvoiceID
	// TaxID is the tax; zero for provider taxes.
	TaxID core.TaxID
	// TaxCode is the tax code snapshot.
	TaxCode string
	// TaxName is the tax name snapshot.
	TaxName string
	// TaxDescription is the tax description snapshot.
	TaxDescription *string
	// TaxRate is the rate in percent.
	TaxRate money.Decimal
	// FeesAmount is Σ taxable bases of the fees, in minor units.
	FeesAmount int64
	// TaxableBaseAmount is the part of FeesAmount a tax provider reported
	// as actually taxed, in minor units; zero when not reported (internal
	// taxes).
	TaxableBaseAmount int64
	// Amount is the rounded Σ of the unrounded fee taxes, in minor units.
	Amount int64
	// PreciseAmount is the unrounded Σ, in minor units.
	PreciseAmount money.Decimal
	// Currency is the invoice currency.
	Currency money.Currency
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// TaxableAmount returns the taxable base, falling back to FeesAmount when
// no base was reported.
func (t *AppliedTax) TaxableAmount() int64 {
	if t.TaxableBaseAmount == 0 {
		return t.FeesAmount
	}
	return t.TaxableBaseAmount
}

// IsProviderTax reports whether the tax line was computed by a tax
// provider on a reported taxable base: it has no tax and a positive base.
func (t *AppliedTax) IsProviderTax() bool {
	return t.TaxID.IsZero() && t.TaxableBaseAmount > 0
}

// Validate checks the snapshot of an applied tax.
func (t *AppliedTax) Validate() error {
	var v core.Validator
	v.Check(!t.InvoiceID.IsZero(), "invoice_id", core.CodeRequired)
	v.Code("tax_code", t.TaxCode, 255)
	v.Check(!t.TaxRate.IsNegative() && t.TaxRate.LessThanOrEqual(money.NewFromInt(100)), "tax_rate", core.CodeOutOfRange)
	v.NonNegative("amount", t.Amount)
	v.NonNegative("taxable_base_amount", t.TaxableBaseAmount)
	v.Currency("currency", t.Currency)
	return v.Err()
}
