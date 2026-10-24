package invoice

import (
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// FeeProperties are the periods a fee bills (domain §12.1 `properties`).
// A zero instant is an unset period bound.
type FeeProperties struct {
	// From is the start of the subscription period.
	From time.Time
	// To is the end (exclusive) of the subscription period.
	To time.Time
	// ChargesFrom is the start of the charges period; unset on
	// fixed-charge fees.
	ChargesFrom time.Time
	// ChargesTo is the end (exclusive) of the charges period.
	ChargesTo time.Time
	// Timestamp is the billing instant.
	Timestamp time.Time
	// ChargesDurationDays is the length of the charges period in days.
	ChargesDurationDays int
	// FixedChargesFrom is the start of the fixed charges period a
	// fixed-charge fee bills: the period of the run, or for a fixed charge
	// paid in advance the period holding Timestamp.
	FixedChargesFrom time.Time
	// FixedChargesTo is the end (exclusive) of the fixed charges period.
	FixedChargesTo time.Time
	// FixedChargesDurationDays is the length of the fixed charges period
	// in days.
	FixedChargesDurationDays int
}

// ChargesPeriod returns the charges period (ChargesFrom, ChargesTo).
func (p FeeProperties) ChargesPeriod() (from, to time.Time) { return p.ChargesFrom, p.ChargesTo }

// FixedChargesPeriod returns the fixed charges period (FixedChargesFrom,
// FixedChargesTo).
func (p FeeProperties) FixedChargesPeriod() (from, to time.Time) {
	return p.FixedChargesFrom, p.FixedChargesTo
}

// PresentationBreakdown is the units of one presentation group of a charge
// fee (domain §12.1): the fee's usage split by the charge's presentation
// group keys.
type PresentationBreakdown struct {
	// PresentationBy are the group's values by presentation key; a nil
	// value is a key whose property is missing or null on the events.
	PresentationBy map[string]*string
	// Units are the group's units.
	Units money.Decimal
}

// ClonePresentationBreakdowns returns a deep copy of bs.
func ClonePresentationBreakdowns(bs []PresentationBreakdown) []PresentationBreakdown {
	if bs == nil {
		return nil
	}
	out := make([]PresentationBreakdown, len(bs))
	for i, b := range bs {
		out[i] = PresentationBreakdown{Units: b.Units}
		if b.PresentationBy != nil {
			out[i].PresentationBy = make(map[string]*string, len(b.PresentationBy))
			for k, v := range b.PresentationBy {
				if v != nil {
					c := *v
					v = &c
				}
				out[i].PresentationBy[k] = v
			}
		}
	}
	return out
}

// PricingUnitUsage records a fee amount in a pricing unit (domain §7.6).
type PricingUnitUsage struct {
	// PricingUnitID is the pricing unit.
	PricingUnitID core.PricingUnitID
	// ShortName is the unit's short name snapshot.
	ShortName string
	// ConversionRate is fiat per pricing unit.
	ConversionRate money.Decimal
	// Amount is the amount in pricing units.
	Amount money.Decimal
	// PreciseAmount is the unrounded amount in pricing units.
	PreciseAmount money.Decimal
	// UnitAmount is the unit price in pricing units.
	UnitAmount money.Decimal
	// PreciseUnitAmount is the unrounded unit price in pricing units.
	PreciseUnitAmount money.Decimal
}

// Fee is one line of an invoice, or an unbilled in-advance fee (domain
// §12). Money amounts are in minor units of Currency.
type Fee struct {
	// ID identifies the fee.
	ID core.FeeID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the invoice; zero for unbilled in-advance fees.
	InvoiceID core.InvoiceID
	// SubscriptionID is the billed subscription, if any.
	SubscriptionID core.SubscriptionID
	// Type is the fee type.
	Type core.FeeType
	// ChargeID is set for charge fees.
	ChargeID core.ChargeID
	// ChargeFilterID is the matched charge filter; zero for the default
	// bucket.
	ChargeFilterID core.ChargeFilterID
	// FixedChargeID is set for fixed-charge fees.
	FixedChargeID core.FixedChargeID
	// AddOnID is set for add-on and fixed-charge fees.
	AddOnID core.AddOnID
	// CommitmentID is set for commitment true-up fees.
	CommitmentID core.CommitmentID
	// WalletTransactionID is the paid wallet transaction of a credit fee.
	WalletTransactionID core.WalletTransactionID
	// SubscriptionRateCardID is the contract rate card of a product fee.
	SubscriptionRateCardID core.SubscriptionRateCardID
	// ProductID is the product of a product fee.
	ProductID core.ProductID
	// ProductFilterID is the product filter a product fee is scoped to;
	// zero for the default bucket.
	ProductFilterID core.ProductFilterID
	// RateCardRateID is the rate in force over a product fee's billing
	// segment, kept when a phase override prices it.
	RateCardRateID core.RateCardRateID
	// RatePhaseID is the phase whose override prices a product fee.
	RatePhaseID core.RatePhaseID
	// GroupedBy holds the pricing group values.
	GroupedBy map[string]string
	// InvoiceDisplayName overrides the printed name.
	InvoiceDisplayName *string
	// Description is the printed description.
	Description *string
	// Amount is the rounded amount; never negative.
	Amount int64
	// PreciseAmount is the unrounded amount in minor units.
	PreciseAmount money.Decimal
	// UnitAmount is the rounded unit price.
	UnitAmount int64
	// PreciseUnitAmount is the unrounded unit price in currency units.
	PreciseUnitAmount money.Decimal
	// Units is the billed quantity.
	Units money.Decimal
	// TotalAggregatedUnits is the aggregated quantity before free units.
	TotalAggregatedUnits money.Decimal
	// EventsCount is the number of events priced.
	EventsCount int64
	// AmountDetails is the per-model breakdown produced by libs/pricing.
	AmountDetails json.RawMessage
	// TaxesAmount is the rounded tax amount.
	TaxesAmount int64
	// TaxesPreciseAmount is the unrounded tax amount in minor units.
	TaxesPreciseAmount money.Decimal
	// TaxesRate is Σ applied tax rates.
	TaxesRate money.Decimal
	// TaxesBaseRate is the taxable share of the amount (1 unless a
	// provider says otherwise).
	TaxesBaseRate money.Decimal
	// PreciseCouponsAmount is the coupon share allocated to the fee, in
	// minor units.
	PreciseCouponsAmount money.Decimal
	// PreciseCreditNotesAmount is the credit-note share allocated, in
	// minor units.
	PreciseCreditNotesAmount money.Decimal
	// Currency is the fee currency.
	Currency money.Currency
	// PayInAdvance tells whether the fee was billed in advance.
	PayInAdvance bool
	// Invoiceable is false for in-advance fees that are never invoiced.
	Invoiceable bool
	// PaymentStatus is managed for unbilled in-advance fees.
	PaymentStatus FeePaymentStatus
	// Properties are the billed periods.
	Properties FeeProperties
	// TrueUpParentFeeID is the charge fee a true-up fee completes.
	TrueUpParentFeeID core.FeeID
	// OriginalFeeID is the root fee of a void and regenerate chain: the
	// fee of the first voided invoice this fee was copied from (lago
	// fees.original_fee_id).
	OriginalFeeID core.FeeID
	// PricingUnitUsage records the amount in a pricing unit.
	PricingUnitUsage *PricingUnitUsage
	// AppliedTaxes are the tax snapshots and amounts of the fee.
	AppliedTaxes []FeeAppliedTax
	// PresentationBreakdowns split the units of a charge fee by the
	// charge's presentation group keys; empty otherwise.
	PresentationBreakdowns []PresentationBreakdown
	// PayInAdvanceEventID is the event of a per-event fee.
	PayInAdvanceEventID core.EventID
	// PayInAdvanceEventTransactionID is that event's transaction ID.
	PayInAdvanceEventTransactionID string
	// SucceededAt is the payment instant of an unbilled fee.
	SucceededAt *time.Time
	// FailedAt is the payment failure instant of an unbilled fee.
	FailedAt *time.Time
	// RefundedAt is the refund instant of an unbilled fee.
	RefundedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
	// DeletedAt is the instant the unbilled fee was discarded;
	// repositories return only fees not discarded, so it is nil on every
	// read.
	DeletedAt *time.Time
}

// FeeAppliedTax is a tax applied to a fee: a snapshot of the tax and the
// amount (domain §12.1, §22.10).
type FeeAppliedTax struct {
	// ID identifies the applied tax.
	ID core.FeeAppliedTaxID
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
	// Amount is the rounded tax amount in minor units.
	Amount int64
	// PreciseAmount is the unrounded tax amount in minor units.
	PreciseAmount money.Decimal
}

// ClampAmount returns a computed fee amount clamped at 0 (domain §12.2).
func ClampAmount(amount int64) int64 { return max(0, amount) }

// ClampPreciseAmount returns a computed precise amount clamped at 0.
func ClampPreciseAmount(amount money.Decimal) money.Decimal {
	if amount.IsNegative() {
		return money.Zero
	}
	return amount
}

// Validate checks the invariants of domain §12.
func (f *Fee) Validate() error {
	var v core.Validator
	v.Enum("fee_type", f.Type)
	v.Currency("currency", f.Currency)
	v.NonNegative("amount", f.Amount)
	v.DecimalNonNegative("precise_amount", f.PreciseAmount)
	v.DecimalNonNegative("units", f.Units)
	v.NonNegative("taxes_amount", f.TaxesAmount)
	v.OptionalEnum("payment_status", f.PaymentStatus)
	switch f.Type {
	case core.FeeTypeCharge:
		v.Check(!f.ChargeID.IsZero(), "charge_id", core.CodeRequired)
	case core.FeeTypeFixedCharge:
		v.Check(!f.FixedChargeID.IsZero(), "fixed_charge_id", core.CodeRequired)
	case core.FeeTypeAddOn:
		v.Check(!f.AddOnID.IsZero(), "add_on_id", core.CodeRequired)
	case core.FeeTypeCommitment:
		v.Check(!f.CommitmentID.IsZero(), "commitment_id", core.CodeRequired)
	case core.FeeTypeSubscription:
		v.Check(!f.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	case core.FeeTypeProduct:
		v.Check(!f.SubscriptionRateCardID.IsZero(), "subscription_rate_card_id", core.CodeRequired)
	}
	if f.InvoiceID.IsZero() {
		v.Check(f.PayInAdvance, "invoice_id", core.CodeRequired)
	}
	return v.Err()
}

// NonZero reports whether the fee has units, an amount or events.
func (f *Fee) NonZero() bool { return f.Units.IsPositive() || f.Amount > 0 || f.EventsCount > 0 }

// Taxable reports whether the fee has an amount left to tax after its
// coupon share.
func (f *Fee) Taxable() bool {
	return f.Amount != 0 && money.NewFromInt(f.Amount).Sub(f.PreciseCouponsAmount).Sign() != 0
}

// SubTotalExcludingTaxesPreciseAmount returns amount − coupon share.
func (f *Fee) SubTotalExcludingTaxesPreciseAmount() money.Decimal {
	return money.NewFromInt(f.Amount).Sub(f.PreciseCouponsAmount)
}

// TotalAmount returns amount + taxes in minor units.
func (f *Fee) TotalAmount() int64 { return f.Amount + f.TaxesAmount }

// CreditableAmount returns how much of the fee credit notes may still
// credit (domain §13.2): amount − already credited by non-voided credit
// notes − coupon share (rounded half up), never negative. For a credit fee
// the result is further capped by walletRemaining, the amount still
// available in the purchased wallet transaction (pass a negative value when
// there is no cap).
func (f *Fee) CreditableAmount(alreadyCredited, walletRemaining int64) int64 {
	coupons, _ := f.PreciseCouponsAmount.Round(0, money.HalfUp).Int64()
	n := max(0, f.Amount-alreadyCredited-coupons)
	if f.Type == core.FeeTypeCredit && walletRemaining >= 0 {
		n = min(n, walletRemaining)
	}
	return n
}

// CloneFee returns a deep copy of f.
func CloneFee(f Fee) Fee {
	f.GroupedBy = maps.Clone(f.GroupedBy)
	f.AmountDetails = slices.Clone(f.AmountDetails)
	f.AppliedTaxes = slices.Clone(f.AppliedTaxes)
	f.PresentationBreakdowns = ClonePresentationBreakdowns(f.PresentationBreakdowns)
	if f.PricingUnitUsage != nil {
		p := *f.PricingUnitUsage
		f.PricingUnitUsage = &p
	}
	return f
}

// AdjustedFee is a manual adjustment of a fee on a draft invoice; it
// survives refreshes until the invoice is finalized (domain §12.3).
type AdjustedFee struct {
	// ID identifies the adjustment.
	ID core.AdjustedFeeID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the draft invoice.
	InvoiceID core.InvoiceID
	// FeeID is the adjusted fee; zero when the adjustment creates a fee for
	// a charge without usage.
	FeeID core.FeeID
	// SubscriptionID is the subscription of the fee.
	SubscriptionID core.SubscriptionID
	// ChargeID is the charge of the fee.
	ChargeID core.ChargeID
	// ChargeFilterID is the charge filter of the fee.
	ChargeFilterID core.ChargeFilterID
	// FixedChargeID is the fixed charge of the fee.
	FixedChargeID core.FixedChargeID
	// FeeType is the type of the fee.
	FeeType core.FeeType
	// GroupedBy holds the pricing group values of the fee.
	GroupedBy map[string]string
	// ChargesFrom and ChargesTo are the charges period of an adjusted
	// charge fee: the adjustment applies to the fee of that period only.
	ChargesFrom, ChargesTo time.Time
	// FixedChargesFrom and FixedChargesTo are the fixed charges period of
	// an adjusted fixed-charge fee.
	FixedChargesFrom, FixedChargesTo time.Time
	// Mode is what the adjustment changes.
	Mode AdjustmentMode
	// InvoiceDisplayName is the new display name.
	InvoiceDisplayName *string
	// Units is the new quantity (units modes).
	Units money.Decimal
	// UnitPreciseAmount is the new unit price in currency units
	// (units_and_unit_price mode).
	UnitPreciseAmount money.Decimal
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// Validate checks the adjustment's mode against its values.
func (a *AdjustedFee) Validate() error {
	var v core.Validator
	v.Enum("mode", a.Mode)
	v.Check(!a.InvoiceID.IsZero(), "invoice_id", core.CodeRequired)
	v.Check(!a.FeeID.IsZero() || !a.ChargeID.IsZero() || !a.FixedChargeID.IsZero(), "fee_id", core.CodeRequired)
	switch a.Mode {
	case AdjustmentModeDisplayName:
		v.Check(a.InvoiceDisplayName != nil, "invoice_display_name", core.CodeRequired)
	case AdjustmentModeUnits:
		v.DecimalNonNegative("units", a.Units)
	case AdjustmentModeUnitsAndUnitPrice:
		v.DecimalNonNegative("units", a.Units)
		v.DecimalNonNegative("unit_precise_amount", a.UnitPreciseAmount)
	}
	return v.Err()
}

// MatchesCharges reports whether the adjustment is for the charges period
// [from, to) (instants compared to the millisecond, as stored).
func (a *AdjustedFee) MatchesCharges(from, to time.Time) bool {
	return sameMilli(a.ChargesFrom, from) && sameMilli(a.ChargesTo, to)
}

// MatchesFixedCharges reports whether the adjustment is for the fixed
// charges period [from, to).
func (a *AdjustedFee) MatchesFixedCharges(from, to time.Time) bool {
	return sameMilli(a.FixedChargesFrom, from) && sameMilli(a.FixedChargesTo, to)
}

func sameMilli(a, b time.Time) bool {
	return a.Truncate(time.Millisecond).Equal(b.Truncate(time.Millisecond))
}

// PreciseAmount returns units × unit price in currency units for the
// units_and_unit_price mode.
func (a *AdjustedFee) PreciseAmount() money.Decimal { return a.Units.Mul(a.UnitPreciseAmount) }
