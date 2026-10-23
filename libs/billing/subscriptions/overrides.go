package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Codes and resources of the plan overrides and usage thresholds.
const (
	// CodeIncompatibleParams: usage_thresholds and
	// plan_overrides.usage_thresholds are both given.
	CodeIncompatibleParams = "incompatible_params"
	// ResourceFixedCharge is a fixed charge a plan override names.
	ResourceFixedCharge = "fixed_charge"
)

// checkThresholdsCompatible refuses usage thresholds given both on the
// subscription and in its plan overrides.
//
// source: app/services/subscriptions/create_service.rb:46-52, app/services/subscriptions/update_service.rb:50-55
func checkThresholdsCompatible(own []plans.ThresholdInput, po *plans.OverrideInput) error {
	if len(own) == 0 || po == nil || len(po.UsageThresholds) == 0 {
		return nil
	}
	return &core.ValidationError{Errors: []core.FieldError{
		{Field: "plan_overrides.usage_thresholds", Code: CodeIncompatibleParams},
		{Field: "usage_thresholds", Code: CodeIncompatibleParams},
	}}
}

// unitsOnly reports whether the overrides set only fixed charge units
// (and when to apply them), which an original plan keeps as the
// subscription's own units rather than as an override plan.
//
// source: app/services/subscriptions/concerns/fixed_charge_units_override_detection_concern.rb:13-36
func unitsOnly(in plans.OverrideInput) bool {
	if in.Amount.Set || in.Currency.Set || in.Description.Set || in.InvoiceDisplayName.Set || in.Name.Set ||
		in.TrialPeriodDays.Set || in.Taxes != nil || len(in.Charges) > 0 || in.UsageThresholds != nil ||
		in.MinimumCommitment != nil || len(in.FixedCharges) == 0 {
		return false
	}
	for _, f := range in.FixedCharges {
		if !f.Units.Set || f.Code.Set || f.ChargeModel.Set || f.Properties.Set || f.PayInAdvance.Set ||
			f.Prorated.Set || f.InvoiceDisplayName.Set || f.Taxes.Set {
			return false
		}
	}
	return true
}

// editableSubscription returns the customer's active subscription, or
// its pending one that replaces none, with the external ID; nil when
// there is none.
//
// source: app/services/subscriptions/create_service.rb:272-276
func editableSubscription(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, externalID string) (*subscription.Subscription, error) {
	live, err := r.Subscriptions.ListByCustomer(ctx, org, c, subscription.StatusActive, subscription.StatusPending)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions of customer %s: %w", c, err)
	}
	for _, s := range live {
		if s.ExternalID == externalID && (s.Status == subscription.StatusActive || s.StartingInTheFuture()) {
			return s, nil
		}
	}
	return nil, nil
}

// creationPlan returns the plan a creation with the overrides po
// subscribes to, and whether the subscription keeps plan with its own
// fixed charge units: plan when po is nil, the customer's subscription
// already follows plan or starts in the future, plan with units for a new
// subscription with units only, else a new override plan of plan.
//
// source: app/services/subscriptions/create_service.rb:129-134,280-297, app/services/subscriptions/plan_upgrade_service.rb:57, app/services/subscriptions/plan_downgrade_service.rb:36
func (s *Service) creationPlan(ctx context.Context, r port.Repos, plan *catalog.Plan, customerID core.CustomerID, externalID string, po *plans.OverrideInput) (*catalog.Plan, bool, error) {
	if po == nil {
		return plan, false, nil
	}
	cur, err := editableSubscription(ctx, r, plan.OrganizationID, customerID, externalID)
	if err != nil {
		return nil, false, err
	}
	switch {
	case cur != nil && (cur.PlanID == plan.ID || cur.StartingInTheFuture()):
		// A subscription starting in the future moves to plan itself
		// (plan_upgrade_service.rb:20-22,77-80,
		// plan_downgrade_service.rb:20-22,82-84).
		return plan, false, nil
	case cur == nil && unitsOnly(*po):
		return plan, true, nil
	}
	np, err := s.plans.Override(ctx, r, plan, *po, nil)
	if err != nil {
		return nil, false, err
	}
	return np, false, nil
}

// unitsOverrides returns the fixed charge units of po for a new
// subscription of the original plan p, its own units once it is created.
// A fixed charge that is not p's is not found.
//
// source: app/services/subscriptions/create_service.rb:299-313
func (s *Service) unitsOverrides(ctx context.Context, r port.Repos, p *catalog.Plan, po plans.OverrideInput) ([]*subscription.FixedChargeUnitsOverride, error) {
	fixed, err := r.FixedCharges.ListByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return nil, fmt.Errorf("load fixed charges of plan %s: %w", p.ID, err)
	}
	now := s.d.Now()
	var out []*subscription.FixedChargeUnitsOverride
	for _, id := range sortedKeys(po.FixedCharges) {
		if !slices.ContainsFunc(fixed, func(f *catalog.FixedCharge) bool { return f.ID == id }) {
			return nil, core.NotFound(ResourceFixedCharge)
		}
		out = append(out, &subscription.FixedChargeUnitsOverride{
			ID:             core.NewID[core.FixedChargeUnitsOverrideID](s.d.IDs),
			OrganizationID: p.OrganizationID,
			FixedChargeID:  id,
			Units:          po.FixedCharges[id].Units.Value,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}
	return out, nil
}

// applyPlanOverrides applies the overrides po to the subscription sub of
// plan, before sub is stored: units only on an original plan are left to
// [Service.applyUnits]; an override plan is updated (see
// childPlanUpdate); an original plan is replaced
// by a new override plan that also takes the subscription's own units,
// which are then discarded. It reports whether the units are left to
// applyUnits.
//
// source: app/services/subscriptions/update_service.rb:92-96,209-259
func (s *Service) applyPlanOverrides(ctx context.Context, r port.Repos, sub *subscription.Subscription, plan *catalog.Plan, po plans.OverrideInput) (bool, error) {
	org := sub.OrganizationID
	if !plan.IsChild() && unitsOnly(po) {
		return true, nil
	}
	if plan.IsChild() {
		in, err := childPlanUpdate(ctx, r, plan, po)
		if err != nil {
			return false, err
		}
		_, err = s.plans.Update(ctx, org, plan.ID, in)
		return false, err
	}
	own, err := r.FixedChargeUnitsOverrides.ListBySubscription(ctx, org, sub.ID)
	if err != nil {
		return false, fmt.Errorf("load units overrides of subscription %s: %w", sub.ID, err)
	}
	// The subscription's own units move onto the override plan
	// (promote_units_overrides_to_fixed_charges_params).
	if len(own) > 0 {
		fixed := make(map[core.FixedChargeID]plans.FixedChargeInput, len(po.FixedCharges)+len(own))
		for id, f := range po.FixedCharges {
			fixed[id] = f
		}
		for _, o := range own {
			if _, ok := fixed[o.FixedChargeID]; !ok {
				fixed[o.FixedChargeID] = plans.FixedChargeInput{Units: core.Some(o.Units)}
			}
		}
		po.FixedCharges = fixed
	}
	np, err := s.plans.Override(ctx, r, plan, po, sub)
	if err != nil {
		return false, err
	}
	now := s.d.Now()
	for _, o := range own {
		if err := r.FixedChargeUnitsOverrides.Discard(ctx, org, o.ID, now); err != nil {
			return false, fmt.Errorf("discard units override %s: %w", o.ID, err)
		}
	}
	sub.PlanID = np.ID
	return false, nil
}

// applyUnits sets the subscription's own units of the fixed charges of
// po, sub being stored.
//
// source: app/services/subscriptions/update_service.rb:234-259
func (s *Service) applyUnits(ctx context.Context, sub *subscription.Subscription, po plans.OverrideInput) error {
	ref := refs.SubscriptionRef{ID: sub.ID}
	for _, id := range sortedKeys(po.FixedCharges) {
		f := po.FixedCharges[id]
		in := plans.FixedChargeInput{Units: f.Units, ApplyUnitsImmediately: f.ApplyUnitsImmediately}
		if _, err := s.plans.UpdateSubscriptionFixedCharge(ctx, sub.OrganizationID, ref, refs.ByID(id), in); err != nil {
			return err
		}
	}
	return nil
}

// childPlanUpdate returns the update of the override plan p by po. Its
// currency and trial are left as they are: the plan has a subscription
// (plans/update_service.rb:40-49). Every fixed charge of p is kept, with
// the values of po over those a key of po names; a key that is not a
// fixed charge of p is not found. The charges of po replace those of p: a
// key naming a charge of p updates it, any other creates a charge, and
// the charges left out are removed.
//
// source: app/services/subscriptions/update_service.rb:209-221,262-288, app/services/plans/update_service.rb:40-49,245-276
func childPlanUpdate(ctx context.Context, r port.Repos, p *catalog.Plan, po plans.OverrideInput) (plans.UpdateInput, error) {
	org := p.OrganizationID
	in := plans.UpdateInput{
		Amount:             po.Amount,
		Description:        po.Description,
		InvoiceDisplayName: po.InvoiceDisplayName,
		Name:               po.Name,
	}
	if po.Taxes != nil {
		in.Taxes = core.Some(po.Taxes)
	}
	if po.UsageThresholds != nil {
		in.UsageThresholds = core.Some(po.UsageThresholds)
	}
	if po.MinimumCommitment != nil {
		in.MinimumCommitment = core.Some(po.MinimumCommitment)
	}
	if len(po.FixedCharges) > 0 {
		fixed, err := r.FixedCharges.ListByPlan(ctx, org, p.ID)
		if err != nil {
			return in, fmt.Errorf("load fixed charges of plan %s: %w", p.ID, err)
		}
		for id := range po.FixedCharges {
			if !slices.ContainsFunc(fixed, func(f *catalog.FixedCharge) bool { return f.ID == id }) {
				return in, core.NotFound(ResourceFixedCharge)
			}
		}
		out := make([]plans.FixedChargeInput, len(fixed))
		for i, f := range fixed {
			v := po.FixedCharges[f.ID]
			v.ID = f.ID
			out[i] = v
		}
		in.FixedCharges = core.Some(out)
	}
	if len(po.Charges) > 0 {
		out := make([]plans.ChargeInput, 0, len(po.Charges))
		for _, id := range sortedKeys(po.Charges) {
			v := po.Charges[id]
			v.ID = id
			out = append(out, v)
		}
		in.Charges = core.Some(out)
	}
	return in, nil
}

// replaceThresholds replaces the usage thresholds of the subscription sub
// by in (Subscriptions::UpdateUsageThresholdsService with partial:
// false): the recurring threshold then the fixed ones; the thresholds of
// its override plan are discarded, and its lifetime usage is flagged for
// an invoiced usage refresh when it has thresholds.
//
// source: app/services/subscriptions/update_usage_thresholds_service.rb:14-28, app/services/usage_thresholds/update_service.rb:15-108
func (s *Service) replaceThresholds(ctx context.Context, r port.Repos, sub *subscription.Subscription, in []plans.ThresholdInput) error {
	org := sub.OrganizationID
	if err := checkThresholds(in); err != nil {
		return err
	}
	now := s.d.Now()
	kept, err := r.UsageThresholds.ListBySubscription(ctx, org, sub.ID)
	if err != nil {
		return fmt.Errorf("load usage thresholds of subscription %s: %w", sub.ID, err)
	}
	for _, t := range kept {
		if err := r.UsageThresholds.Discard(ctx, org, t.ID, now); err != nil {
			return fmt.Errorf("discard usage threshold %s: %w", t.ID, err)
		}
	}
	ordered := slices.Clone(in)
	slices.SortStableFunc(ordered, func(a, b plans.ThresholdInput) int {
		switch {
		case a.Recurring == b.Recurring:
			return 0
		case a.Recurring:
			return -1
		}
		return 1
	})
	for _, t := range ordered {
		u := &catalog.UsageThreshold{
			ID:                   core.NewID[core.UsageThresholdID](s.d.IDs),
			OrganizationID:       org,
			SubscriptionID:       sub.ID,
			Amount:               *t.Amount,
			Recurring:            t.Recurring,
			ThresholdDisplayName: t.ThresholdDisplayName,
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		if err := r.UsageThresholds.Create(ctx, u); err != nil {
			return fmt.Errorf("create usage threshold: %w", err)
		}
	}
	plan, err := r.Plans.Get(ctx, org, sub.PlanID)
	if err != nil {
		return fmt.Errorf("load plan %s: %w", sub.PlanID, err)
	}
	if plan.IsChild() {
		own, err := r.UsageThresholds.ListByPlan(ctx, org, plan.ID)
		if err != nil {
			return fmt.Errorf("load usage thresholds of plan %s: %w", plan.ID, err)
		}
		for _, t := range own {
			if err := r.UsageThresholds.Discard(ctx, org, t.ID, now); err != nil {
				return fmt.Errorf("discard usage threshold %s: %w", t.ID, err)
			}
		}
	}
	if len(in) == 0 {
		return nil
	}
	lu, err := r.LifetimeUsages.GetBySubscription(ctx, org, sub.ID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	} else if err != nil {
		return fmt.Errorf("load lifetime usage of subscription %s: %w", sub.ID, err)
	}
	lu.RecalculateInvoicedUsage, lu.UpdatedAt = true, now
	if err := r.LifetimeUsages.Upsert(ctx, lu); err != nil {
		return fmt.Errorf("update lifetime usage of subscription %s: %w", sub.ID, err)
	}
	return nil
}

// checkThresholds checks the usage thresholds of a replacement: each has
// an amount, no two have the same amount and recurrence, and at most one
// recurs.
//
// source: app/services/usage_thresholds/update_service.rb:18-20,48-62
func checkThresholds(in []plans.ThresholdInput) error {
	type key struct {
		amount    int64
		recurring bool
	}
	fail := func(code string) error {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "usage_thresholds", Code: code}}}
	}
	if slices.ContainsFunc(in, func(t plans.ThresholdInput) bool { return t.Amount == nil }) {
		return fail(plans.CodeMissingAmount)
	}
	seen := map[key]bool{}
	recurring := 0
	for _, t := range in {
		k := key{*t.Amount, t.Recurring}
		if seen[k] {
			return fail(plans.CodeDuplicatedValues)
		}
		seen[k] = true
		if t.Recurring {
			recurring++
		}
	}
	if recurring > 1 {
		return fail(catalog.CodeMultipleRecurringThresholds)
	}
	return nil
}

// sortedKeys returns the keys of m in ascending order, for a
// deterministic processing order.
func sortedKeys[K interface {
	comparable
	String() string
}, V any](m map[K]V,
) []K {
	return slices.SortedFunc(maps.Keys(m), func(a, b K) int { return strings.Compare(a.String(), b.String()) })
}

// PlanCurrency returns the currency of the original plan ref, in which
// the plan overrides and usage thresholds of a creation are expressed.
func (s *Service) PlanCurrency(ctx context.Context, org core.OrganizationID, ref refs.Ref) (money.Currency, error) {
	var cur money.Currency
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := findParentPlan(ctx, r, org, ref)
		if err == nil {
			cur = p.Currency
		}
		return err
	})
	return cur, err
}

// SubscriptionCurrency returns the currency of the plan of the
// subscription ref (as [Service.Update] finds it), in which the plan
// overrides and usage thresholds of an update are expressed.
func (s *Service) SubscriptionCurrency(ctx context.Context, org core.OrganizationID, ref SubscriptionRef) (money.Currency, error) {
	var cur money.Currency
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		sub, err := findSubscription(ctx, r, org, ref)
		if err != nil {
			return err
		}
		p, err := r.Plans.Get(ctx, org, sub.PlanID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", sub.PlanID, err)
		}
		cur = p.Currency
		return nil
	})
	return cur, err
}
