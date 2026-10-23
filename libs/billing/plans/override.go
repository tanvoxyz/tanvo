package plans

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// OverrideInput are the values an override plan changes from its parent:
// only the set fields differ.
type OverrideInput struct {
	// Amount is the subscription fee, in minor units of the currency.
	Amount core.Optional[int64]
	// Currency is the plan's currency.
	Currency core.Optional[money.Currency]
	// Description is free text.
	Description core.Optional[*string]
	// InvoiceDisplayName is the invoice line name of the subscription fee.
	InvoiceDisplayName core.Optional[*string]
	// Name is the display name.
	Name core.Optional[string]
	// TrialPeriodDays is the trial period.
	TrialPeriodDays core.Optional[int]
	// Taxes reference the override plan's taxes; nil sets none, as the
	// parent's taxes are not copied.
	Taxes []refs.Ref
	// Charges are the overridden values of the parent's charges, by parent
	// charge; a charge left out is copied as is.
	Charges map[core.ChargeID]ChargeInput
	// FixedCharges are the overridden values of the parent's fixed
	// charges, by parent fixed charge; one left out is copied as is.
	FixedCharges map[core.FixedChargeID]FixedChargeInput
	// UsageThresholds are the override plan's usage thresholds; none by
	// default.
	UsageThresholds []ThresholdInput
	// MinimumCommitment is the override plan's minimum commitment; nil or
	// blank sets none.
	MinimumCommitment *CommitmentInput
}

// Override creates, in the transaction of r, the override plan of plan
// for the subscription sub (nil for none) with the values of in: a copy
// of plan with ParentID = plan, without its taxes and metadata, with every
// charge and fixed charge of plan copied onto it, and with the usage
// thresholds and minimum commitment of in. It emits no webhook. The
// caller moves sub onto the returned plan.
//
// source: app/services/plans/override_service.rb:15-84, app/services/commitments/override_service.rb:14-37, app/services/usage_thresholds/override_service.rb:14-33
func (s *Service) Override(ctx context.Context, r port.Repos, plan *catalog.Plan, in OverrideInput, sub *subscription.Subscription) (*catalog.Plan, error) {
	np, err := s.override(ctx, r, plan, in, sub)
	if err != nil {
		return nil, fmt.Errorf("override plan %s: %w", plan.ID, err)
	}
	return np, nil
}

func (s *Service) override(ctx context.Context, r port.Repos, plan *catalog.Plan, in OverrideInput, sub *subscription.Subscription) (*catalog.Plan, error) {
	org := plan.OrganizationID
	np := plan.NewOverride(core.NewID[core.PlanID](s.d.IDs), s.d.Now())
	// plan.dup copies neither the applied taxes nor the metadata record.
	np.TaxIDs, np.Metadata = nil, nil
	in.Amount.Apply(&np.Amount)
	in.Currency.Apply(&np.Currency)
	in.Description.Apply(&np.Description)
	in.InvoiceDisplayName.Apply(&np.InvoiceDisplayName)
	in.Name.Apply(&np.Name)
	in.TrialPeriodDays.Apply(&np.TrialPeriodDays)
	if err := np.Validate(); err != nil {
		return nil, err
	}
	ids, err := refs.Taxes(r, org).IDs(ctx, in.Taxes)
	if err != nil {
		return nil, err
	}
	np.TaxIDs = ids
	if err := r.Plans.Create(ctx, np); err != nil {
		return nil, fmt.Errorf("create override plan: %w", err)
	}
	charges, err := r.Charges.ListByPlan(ctx, org, plan.ID)
	if err != nil {
		return nil, fmt.Errorf("load charges: %w", err)
	}
	for _, c := range charges {
		var params *ChargeInput
		if v, ok := in.Charges[c.ID]; ok {
			params = &v
		}
		if _, err := s.overrideCharge(ctx, r, c, np, params); err != nil {
			return nil, err
		}
	}
	fixed, err := r.FixedCharges.ListByPlan(ctx, org, plan.ID)
	if err != nil {
		return nil, fmt.Errorf("load fixed charges: %w", err)
	}
	for _, f := range fixed {
		var params *FixedChargeInput
		if v, ok := in.FixedCharges[f.ID]; ok {
			params = &v
		}
		if _, err := s.overrideFixedCharge(ctx, r, f, np, params, sub); err != nil {
			return nil, err
		}
	}
	// source: app/services/plans/override_service.rb:57-62
	if len(in.UsageThresholds) > 0 {
		if err := s.createThresholds(ctx, r, np, in.UsageThresholds); err != nil {
			return nil, err
		}
	}
	// source: app/services/plans/override_service.rb:64-72
	if !in.MinimumCommitment.blank() {
		if err := s.writeCommitment(ctx, r, s.newCommitment(np), *in.MinimumCommitment, true); err != nil {
			return nil, err
		}
	}
	return np, nil
}

// ensurePlanOverride returns the override plan of the subscription sub,
// creating it (with every charge and fixed charge overridden) and moving
// sub onto it when sub is on a parent plan
// (Subscriptions::Concerns::PlanOverrideConcern#ensure_plan_override).
//
// source: app/services/subscriptions/concerns/plan_override_concern.rb:10-24
func (s *Service) ensurePlanOverride(ctx context.Context, r port.Repos, sub *subscription.Subscription) (*catalog.Plan, error) {
	plan, err := r.Plans.Get(ctx, sub.OrganizationID, sub.PlanID)
	if err != nil {
		return nil, fmt.Errorf("load plan %s: %w", sub.PlanID, err)
	}
	if plan.IsChild() {
		return plan, nil
	}
	np, err := s.Override(ctx, r, plan, OverrideInput{}, sub)
	if err != nil {
		return nil, err
	}
	sub.PlanID, sub.UpdatedAt = np.ID, s.d.Now()
	if err := r.Subscriptions.Update(ctx, sub); err != nil {
		return nil, fmt.Errorf("update subscription %s: %w", sub.ID, err)
	}
	return np, nil
}
