package invoicing

import (
	"context"
	"fmt"
	"maps"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// RegeneratedFee is one fee of the invoice regenerated from a voided one
// (lago Types::Invoices::VoidedInvoiceFeeInput): a copy of the voided
// invoice's fee FeeID, else the fee of the subscription's charge (and
// filter), adjusted by Units, UnitAmount and InvoiceDisplayName.
//
// The fee's units become Units, zero when nil; with Units alone a charge
// or fixed-charge fee is repriced by its charge model, any other fee keeps
// its unit price; otherwise the fee costs its units times UnitAmount, zero
// when nil. InvoiceDisplayName replaces the line name of a charge or
// fixed-charge fee, a nil one clearing it; it renames another fee only
// when set.
type RegeneratedFee struct {
	// FeeID is a fee of the voided invoice, copied onto the new one; an
	// unknown fee is left out and SubscriptionID, ChargeID and
	// ChargeFilterID select the fee instead.
	FeeID core.FeeID
	// SubscriptionID is a subscription billed by the voided invoice.
	SubscriptionID core.SubscriptionID
	// ChargeID is a charge of the subscription's plan whose fee is
	// adjusted, created empty over the invoice subscription's charges
	// period when the new invoice has none.
	ChargeID core.ChargeID
	// ChargeFilterID is a filter of the charge.
	ChargeFilterID core.ChargeFilterID
	// Units are the new units; nil gives zero units.
	Units *money.Decimal
	// UnitAmount is the new unit price in currency units.
	UnitAmount *money.Decimal
	// InvoiceDisplayName is the new line name.
	InvoiceDisplayName *string
}

// validateRegeneratedFees checks fs: nonnegative units and unit prices.
func validateRegeneratedFees(fs []RegeneratedFee) error {
	var v core.Validator
	for i, f := range fs {
		field := fmt.Sprintf("fees[%d]", i)
		if f.Units != nil {
			v.DecimalNonNegative(field+".units", *f.Units)
		}
		if f.UnitAmount != nil {
			v.DecimalNonNegative(field+".unit_amount", *f.UnitAmount)
		}
		if f.InvoiceDisplayName != nil {
			v.Check(len(*f.InvoiceDisplayName) <= 255, field+".invoice_display_name", core.CodeInvalid)
		}
	}
	return v.Err()
}

// regeneratedFees builds the fees of inv, the invoice regenerated from
// the voided invoice whose fees are old, from params (lago
// Invoices::RegenerateFromVoidedService#process_fees, #duplicate_fee and
// #adjust_fees, app/services/invoices/regenerate_from_voided_service.rb:116-249,
// with AdjustedFees::CreateService and regenerating_voided). Only the
// fees in params are kept: each param copies its voided fee (a fee listed
// twice is copied twice), pending, without taxes or coupons, with
// OriginalFeeID set to the root of the void and regenerate chain, and
// with its presentation breakdowns when the param keeps the fee's units;
// a param without a voided fee selects or creates the fee of its charge.
// Each fee gets an adjustment, recorded on inv, which reprices it. The
// fees are stored.
func (s *Service) regeneratedFees(ctx context.Context, r port.Repos, inv *invoice.Invoice, old []*invoice.Fee, params []RegeneratedFee) ([]*invoice.Fee, error) {
	byID := make(map[core.FeeID]*invoice.Fee, len(old))
	for _, f := range old {
		byID[f.ID] = f
	}
	now := s.d.Now()
	var out []*invoice.Fee
	adjusted := map[core.FeeID]*invoice.AdjustedFee{}
	for _, p := range params {
		var target *invoice.Fee
		if of := byID[p.FeeID]; of != nil && !p.FeeID.IsZero() {
			f := invoice.CloneFee(*of)
			f.ID = core.NewID[core.FeeID](s.d.IDs)
			f.InvoiceID = inv.ID
			f.PaymentStatus = invoice.FeePaymentStatusPending
			f.CreatedAt, f.UpdatedAt = now, now
			f.AppliedTaxes = nil
			f.TaxesAmount, f.TaxesPreciseAmount = 0, money.Zero
			f.TaxesRate, f.TaxesBaseRate = money.Zero, money.Zero
			f.PreciseCouponsAmount = money.Zero
			f.OriginalFeeID = of.OriginalFeeID
			if f.OriginalFeeID.IsZero() {
				f.OriginalFeeID = of.ID
			}
			if p.Units == nil || !p.Units.Equal(of.Units) {
				f.PresentationBreakdowns = nil
			}
			target = &f
			out = append(out, target)
		} else {
			t, err := s.adjustedTarget(ctx, r, inv, out, FeeAdjustment{
				InvoiceID: inv.ID, SubscriptionID: p.SubscriptionID, ChargeID: p.ChargeID, ChargeFilterID: p.ChargeFilterID,
			})
			if err != nil {
				return nil, err
			}
			if t.ID.IsZero() {
				t.ID = core.NewID[core.FeeID](s.d.IDs)
				t.Invoiceable = true
				t.CreatedAt, t.UpdatedAt = now, now
				out = append(out, t)
			}
			target = t
		}
		if adjusted[target.ID] != nil {
			var v core.Validator
			v.Add("adjusted_fee", core.CodeAlreadyExists)
			return nil, v.Err()
		}
		a, err := s.regeneratedAdjustment(ctx, r, inv, target, p)
		if err != nil {
			return nil, err
		}
		adjusted[target.ID] = a
	}
	for _, f := range out {
		a := adjusted[f.ID]
		if err := s.fees.AdjustCopy(ctx, r, f, a); err != nil {
			return nil, err
		}
		if err := r.Fees.Create(ctx, f); err != nil {
			return nil, fmt.Errorf("create fee: %w", err)
		}
		if err := r.AdjustedFees.Create(ctx, a); err != nil {
			return nil, fmt.Errorf("create adjusted fee: %w", err)
		}
	}
	return out, nil
}

// regeneratedAdjustment returns the adjustment p records for the fee f of
// the regenerated invoice inv (lago AdjustedFees::CreateService#call):
// units alone reprice the fee by its charge model, which a percentage or
// prorated graduated charge rejects with [CodeInvalidChargeModel]; units
// with a unit price price the units; anything else, the units (zero when
// nil) times the unit price (zero when nil), recorded with the
// display_name mode, whose display name may be nil.
func (s *Service) regeneratedAdjustment(ctx context.Context, r port.Repos, inv *invoice.Invoice, f *invoice.Fee, p RegeneratedFee) (*invoice.AdjustedFee, error) {
	a := &invoice.AdjustedFee{
		ID: core.NewID[core.AdjustedFeeID](s.d.IDs), OrganizationID: inv.OrganizationID, InvoiceID: inv.ID, FeeID: f.ID,
		SubscriptionID: f.SubscriptionID, ChargeID: f.ChargeID, ChargeFilterID: f.ChargeFilterID,
		FixedChargeID: f.FixedChargeID, FeeType: f.Type, GroupedBy: maps.Clone(f.GroupedBy),
		ChargesFrom: f.Properties.ChargesFrom, ChargesTo: f.Properties.ChargesTo,
		FixedChargesFrom: f.Properties.FixedChargesFrom, FixedChargesTo: f.Properties.FixedChargesTo,
		Mode: invoice.AdjustmentModeDisplayName, InvoiceDisplayName: p.InvoiceDisplayName,
		Units: money.Zero, UnitPreciseAmount: money.Zero, CreatedAt: s.d.Clock.Now(),
	}
	if a.GroupedBy == nil {
		a.GroupedBy = map[string]string{}
	}
	if p.Units != nil {
		a.Units = *p.Units
	}
	if p.UnitAmount != nil {
		a.UnitPreciseAmount = *p.UnitAmount
	}
	switch {
	case p.Units != nil && p.UnitAmount != nil:
		a.Mode = invoice.AdjustmentModeUnitsAndUnitPrice
	case p.Units != nil:
		a.Mode = invoice.AdjustmentModeUnits
		if !f.ChargeID.IsZero() {
			c, err := r.Charges.Get(ctx, inv.OrganizationID, f.ChargeID)
			if err := ignoreNotFound(err); err != nil {
				return nil, fmt.Errorf("get charge %s: %w", f.ChargeID, err)
			}
			if c != nil && (c.ChargeModel == catalog.ChargeModelPercentage ||
				(c.Prorated && c.ChargeModel == catalog.ChargeModelGraduated)) {
				var v core.Validator
				v.Add("charge", CodeInvalidChargeModel)
				return nil, v.Err()
			}
		}
	}
	return a, nil
}
