package fees

import (
	"context"
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// AdjustCopy reprices f, a fee of the invoice regenerated from a voided
// one, by its adjustment a (lago
// Invoices::RegenerateFromVoidedService#adjust_fees,
// app/services/invoices/regenerate_from_voided_service.rb:116-191): a
// charge fee is rebuilt by Fees::InitFromAdjustedChargeFeeService with the
// properties of its filter, else of its charge, a fixed-charge fee by
// Fees::InitFromAdjustedFixedChargeFeeService; both take the adjustment's
// display name, a nil one clearing the fee's. Any other fee takes the
// display name only when set, and the adjusted units at the fee's own unit
// price for a units adjustment, else at the adjusted unit price. The fee
// keeps its periods, grouping and links.
func (s *Service) AdjustCopy(ctx context.Context, r port.Repos, f *invoice.Fee, a *invoice.AdjustedFee) error {
	switch {
	case f.Type == core.FeeTypeCharge && !f.ChargeID.IsZero() && !f.SubscriptionID.IsZero():
		return s.adjustCopiedCharge(ctx, r, f, a)
	case f.Type == core.FeeTypeFixedCharge && !f.FixedChargeID.IsZero() && !f.SubscriptionID.IsZero():
		return s.adjustCopiedFixedCharge(ctx, r, f, a)
	}
	if a.InvoiceDisplayName != nil && strings.TrimSpace(*a.InvoiceDisplayName) != "" {
		f.InvoiceDisplayName = a.InvoiceDisplayName
	}
	sub := f.Currency.SubunitToUnit()
	f.Units = a.Units
	// With the units alone, the fee's unit price in minor units as stored
	// (lago fee.unit_amount_cents).
	unitCents := money.NewFromInt(f.UnitAmount)
	if a.Mode != invoice.AdjustmentModeUnits {
		unitCents = a.UnitPreciseAmount.Mul(sub)
	}
	f.UnitAmount = kernel.RoundCents(unitCents)
	f.PreciseUnitAmount = unitCents.Div(sub)
	f.PreciseAmount = a.Units.Mul(unitCents)
	f.Amount = kernel.RoundCents(f.PreciseAmount)
	return nil
}

// adjustCopiedCharge is [Service.AdjustCopy] for a charge fee.
func (s *Service) adjustCopiedCharge(ctx context.Context, r port.Repos, f *invoice.Fee, a *invoice.AdjustedFee) error {
	org := f.OrganizationID
	env, err := kernel.LoadSubscriptionEnv(ctx, r, org, f.SubscriptionID)
	if err != nil {
		return fmt.Errorf("load subscription of fee %s: %w", f.ID, err)
	}
	c, err := r.Charges.Get(ctx, org, f.ChargeID)
	if err != nil {
		return fmt.Errorf("get charge %s: %w", f.ChargeID, err)
	}
	cc, err := loadCharge(ctx, r, env, c)
	if err != nil {
		return err
	}
	raw := c.Properties
	for i := range c.Filters {
		if c.Filters[i].ID == f.ChargeFilterID {
			raw = c.Filters[i].Properties
			break
		}
	}
	props, err := cc.properties(raw)
	if err != nil {
		return err
	}
	adj := *a
	adj.ChargeFilterID, adj.GroupedBy = f.ChargeFilterID, f.GroupedBy
	nf, err := s.adjustedChargeFee(Scope{Env: env}, cc, bucket{props: props}, &adj)
	if err != nil {
		return err
	}
	f.InvoiceDisplayName = nf.InvoiceDisplayName
	f.Units = nf.Units
	f.UnitAmount, f.PreciseUnitAmount = nf.UnitAmount, nf.PreciseUnitAmount
	f.Amount, f.PreciseAmount = nf.Amount, nf.PreciseAmount
	f.PricingUnitUsage = nf.PricingUnitUsage
	f.AmountDetails = nf.AmountDetails
	return nil
}

// adjustCopiedFixedCharge is [Service.AdjustCopy] for a fixed-charge fee.
func (s *Service) adjustCopiedFixedCharge(ctx context.Context, r port.Repos, f *invoice.Fee, a *invoice.AdjustedFee) error {
	org := f.OrganizationID
	env, err := kernel.LoadSubscriptionEnv(ctx, r, org, f.SubscriptionID)
	if err != nil {
		return fmt.Errorf("load subscription of fee %s: %w", f.ID, err)
	}
	fc, err := r.FixedCharges.Get(ctx, org, f.FixedChargeID)
	if err != nil {
		return fmt.Errorf("get fixed charge %s: %w", f.FixedChargeID, err)
	}
	props, err := pricing.DecodeProperties(pricing.ChargeModel(fc.ChargeModel), fc.Properties.Raw())
	if err != nil {
		return fmt.Errorf("properties of fixed charge %s: %w", fc.ID, err)
	}
	structure := pricing.PricingStructure{Properties: props, Prorated: fc.Prorated, Currency: env.Plan.Currency}
	nf, err := s.adjustedFixedChargeFee(Scope{Env: env}, fc, structure, period.Period{}, 0, a)
	if err != nil {
		return err
	}
	f.InvoiceDisplayName = nf.InvoiceDisplayName
	f.Units = nf.Units
	f.UnitAmount, f.PreciseUnitAmount = nf.UnitAmount, nf.PreciseUnitAmount
	f.Amount, f.PreciseAmount = nf.Amount, nf.PreciseAmount
	f.AmountDetails = nf.AmountDetails
	return nil
}
