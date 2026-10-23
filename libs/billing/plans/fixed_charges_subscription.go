package plans

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// overrideFixedCharge copies parent onto the override plan plan of the
// subscription sub with the values of in, nil for none
// (FixedCharges::OverrideService).
func (s *Service) overrideFixedCharge(ctx context.Context, r port.Repos, parent *catalog.FixedCharge, plan *catalog.Plan, in *FixedChargeInput, sub *subscription.Subscription) (*catalog.FixedCharge, error) {
	o, _, err := s.overrideFixedChargeAt(ctx, r, parent, plan, in, sub, s.d.Now())
	return o, err
}

// overrideFixedChargeAt is [Service.overrideFixedCharge] for a change at
// ts; it also returns the units events recorded for sub. The copy keeps
// the parent's add-on and flags, but not its taxes, which only in sets; a
// model other than the parent's is refused.
//
// source: app/services/fixed_charges/override_service.rb:16-54
func (s *Service) overrideFixedChargeAt(ctx context.Context, r port.Repos, parent *catalog.FixedCharge, plan *catalog.Plan, in *FixedChargeInput, sub *subscription.Subscription, ts time.Time) (*catalog.FixedCharge, []*subscription.FixedChargeEvent, error) {
	if in == nil {
		in = &FixedChargeInput{}
	}
	if in.ChargeModel.Set && in.ChargeModel.Value != parent.ChargeModel {
		return nil, nil, core.NewRuleError(codeCannotOverrideChargeModel, "the charge model of a fixed charge cannot be overridden")
	}
	o := parent.NewOverride(core.NewID[core.FixedChargeID](s.d.IDs), plan.ID, s.d.Now())
	o.OrganizationID = plan.OrganizationID
	// fixed_charge.dup copies no applied taxes.
	o.TaxIDs = nil
	if in.Properties.Set {
		var err error
		if o.Properties, err = fixedChargeFilterProperties(o.ChargeModel, in.Properties.Value); err != nil {
			return nil, nil, err
		}
	}
	in.InvoiceDisplayName.Apply(&o.InvoiceDisplayName)
	in.Units.Apply(&o.Units)
	if in.Taxes.Set {
		var err error
		if o.TaxIDs, err = refs.Taxes(r, plan.OrganizationID).IDs(ctx, in.Taxes.Value); err != nil {
			return nil, nil, err
		}
	}
	if err := validateFixedChargeRecord(o); err != nil {
		return nil, nil, err
	}
	if err := r.FixedCharges.Create(ctx, o); err != nil {
		return nil, nil, chargeCodeConflict(err)
	}
	var events []*subscription.FixedChargeEvent
	if sub != nil {
		var err error
		if events, err = s.emitFixedChargeEvents(ctx, r, o, sub, in.ApplyUnitsImmediately, ts); err != nil {
			return nil, nil, err
		}
	}
	return o, events, nil
}

// emitFixedChargeEvents records the units of the fixed charge f changed
// at ts (FixedCharges::EmitEventsService). Without sub, each active or
// incomplete subscription of f's plan without its own units of f takes
// f's units; with sub, sub alone, when active or incomplete, takes its
// units of f, unless the units in force at the change are already those.
// A change applied immediately takes effect at ts, except on an incomplete
// subscription, whose activation bills the units of its first period; any
// other takes effect at the start of the next fixed charges period. It
// returns the recorded events.
//
// source: app/services/fixed_charges/emit_events_service.rb:16-88
func (s *Service) emitFixedChargeEvents(ctx context.Context, r port.Repos, f *catalog.FixedCharge, sub *subscription.Subscription, immediate bool, ts time.Time) ([]*subscription.FixedChargeEvent, error) {
	org := f.OrganizationID
	var subs []*subscription.Subscription
	if sub == nil {
		all, err := kernel.All(func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
			flt := subscription.Filter{PlanID: f.PlanID, Statuses: []subscription.Status{subscription.StatusActive, subscription.StatusIncomplete}}
			return r.Subscriptions.List(ctx, org, flt, p)
		})
		if err != nil {
			return nil, fmt.Errorf("load subscriptions of plan %s: %w", f.PlanID, err)
		}
		own, err := r.FixedChargeUnitsOverrides.ListByFixedCharge(ctx, org, f.ID)
		if err != nil {
			return nil, fmt.Errorf("load units overrides of fixed charge %s: %w", f.ID, err)
		}
		subs = slices.DeleteFunc(all, func(x *subscription.Subscription) bool {
			return slices.ContainsFunc(own, func(o *subscription.FixedChargeUnitsOverride) bool { return o.SubscriptionID == x.ID })
		})
	} else if sub.Status == subscription.StatusActive || sub.Status == subscription.StatusIncomplete {
		subs = []*subscription.Subscription{sub}
	}
	var out []*subscription.FixedChargeEvent
	for _, sb := range subs {
		env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sb)
		if err != nil {
			return nil, err
		}
		units := f.Units
		if sub != nil {
			if units, err = effectiveFixedChargeUnits(ctx, r, f, sb); err != nil {
				return nil, err
			}
		}
		now := s.d.Now()
		ev := &subscription.FixedChargeEvent{
			ID:                    core.NewID[core.FixedChargeEventID](s.d.IDs),
			OrganizationID:        org,
			SubscriptionID:        sb.ID,
			FixedChargeID:         f.ID,
			Units:                 units,
			Timestamp:             ts,
			ApplyUnitsImmediately: immediate && sb.Status != subscription.StatusIncomplete,
			CreatedAt:             now,
			UpdatedAt:             now,
		}
		if sub != nil {
			same, err := fixedChargeUnitsUnchanged(ctx, r, env, f, ev)
			if err != nil {
				return nil, err
			}
			if same {
				continue
			}
		}
		if err := ev.Validate(); err != nil {
			return nil, err
		}
		if err := r.FixedChargeEvents.Record(ctx, ev, env.Location()); err != nil {
			return nil, fmt.Errorf("record units of fixed charge %s: %w", f.ID, err)
		}
		out = append(out, ev)
	}
	return out, nil
}

// fixedChargeUnitsUnchanged reports whether the latest change of the units
// of f (or of its parent) for the subscription of ev taking effect by the
// time ev does already set ev's units.
//
// source: app/services/fixed_charges/emit_events_service.rb:72-81
func fixedChargeUnitsUnchanged(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, f *catalog.FixedCharge, ev *subscription.FixedChargeEvent) (bool, error) {
	ids := []core.FixedChargeID{f.ID}
	if f.IsChild() {
		ids = append(ids, f.ParentID)
	}
	at := fixedChargeChangeAt(env, ev)
	var last *subscription.FixedChargeEvent
	for _, id := range ids {
		es, err := r.FixedChargeEvents.ListBySubscription(ctx, ev.OrganizationID, ev.SubscriptionID, id)
		if err != nil {
			return false, fmt.Errorf("load units of fixed charge %s: %w", id, err)
		}
		for _, e := range es {
			if fixedChargeChangeAt(env, e).After(at) {
				continue
			}
			if last == nil || e.CreatedAt.After(last.CreatedAt) || (e.CreatedAt.Equal(last.CreatedAt) && e.ID.Compare(last.ID) > 0) {
				last = e
			}
		}
	}
	return last != nil && last.Units.Equal(ev.Units), nil
}

// fixedChargeChangeAt returns the instant the change e takes effect: its
// timestamp when applied immediately, else the start of the next fixed
// charges period.
//
// source: app/services/fixed_charges/emit_events_service.rb:63-70,83-85
func fixedChargeChangeAt(env *kernel.SubscriptionEnv, e *subscription.FixedChargeEvent) time.Time {
	if e.ApplyUnitsImmediately {
		return e.Timestamp
	}
	d, err := env.Dates(e.Timestamp, period.CurrentUsage)
	if err != nil {
		return e.Timestamp
	}
	if end, ok := d.FixedChargesPeriodEnd(); ok {
		return end
	}
	return e.Timestamp
}

// effectiveFixedChargeUnits returns the units of f for the subscription
// sub: its own units of f, else f's (FixedCharge#effective_units_for).
//
// source: app/models/fixed_charge.rb:55-59
func effectiveFixedChargeUnits(ctx context.Context, r port.Repos, f *catalog.FixedCharge, sub *subscription.Subscription) (money.Decimal, error) {
	own, err := unitsOverrideOf(ctx, r, sub, f.ID)
	if err != nil || own == nil {
		return f.Units, err
	}
	return own.Units, nil
}

// unitsOverrideOf returns the kept units override of the fixed charge fc
// for the subscription sub, or nil.
func unitsOverrideOf(ctx context.Context, r port.Repos, sub *subscription.Subscription, fc core.FixedChargeID) (*subscription.FixedChargeUnitsOverride, error) {
	own, err := r.FixedChargeUnitsOverrides.ListBySubscription(ctx, sub.OrganizationID, sub.ID)
	if err != nil {
		return nil, fmt.Errorf("load units overrides of subscription %s: %w", sub.ID, err)
	}
	if i := slices.IndexFunc(own, func(o *subscription.FixedChargeUnitsOverride) bool { return o.FixedChargeID == fc }); i >= 0 {
		return own[i], nil
	}
	return nil, nil
}

// ListSubscriptionFixedCharges lists the fixed charges of the
// subscription's plan, most recent first, each with the subscription's
// units. The subscription is found by ID, or by external ID among the
// active ones.
//
// source: app/controllers/api/v1/subscriptions/fixed_charges_controller.rb:11-29,89-91
func (s *Service) ListSubscriptionFixedCharges(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, page core.PageRequest) (core.Page[*catalog.FixedCharge], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.FixedCharge], error) {
		sb, err := refs.Subscription(ctx, r, org, sub)
		if err != nil {
			return core.Page[*catalog.FixedCharge]{}, err
		}
		fcs, err := r.FixedCharges.ListByPlan(ctx, org, sb.PlanID)
		if err != nil {
			return core.Page[*catalog.FixedCharge]{}, fmt.Errorf("load fixed charges of plan %s: %w", sb.PlanID, err)
		}
		slices.Reverse(fcs)
		for i, f := range fcs {
			if fcs[i], err = withSubscriptionUnits(ctx, r, f, sb); err != nil {
				return core.Page[*catalog.FixedCharge]{}, err
			}
		}
		return chargePage(fcs, func(f *catalog.FixedCharge) string { return f.ID.String() }, page)
	})
}

// GetSubscriptionFixedCharge returns the fixed charge of the
// subscription's plan, by ID or code, with the subscription's units.
//
// source: app/controllers/api/v1/subscriptions/fixed_charges_controller.rb:31-40,108-111
func (s *Service) GetSubscriptionFixedCharge(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, fixedCharge refs.Ref) (*catalog.FixedCharge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.FixedCharge, error) {
		sb, err := refs.Subscription(ctx, r, org, sub)
		if err != nil {
			return nil, err
		}
		f, err := fixedChargeOnPlan(ctx, r, org, sb.PlanID, fixedCharge, false)
		if err != nil {
			return nil, err
		}
		return withSubscriptionUnits(ctx, r, f, sb)
	})
}

// UpdateSubscriptionFixedCharge sets the subscription's own values of the
// fixed charge (lago Subscriptions::UpdateOrOverrideFixedChargeService),
// returned with the subscription's units. On a subscription of an
// original plan, a change of units alone is kept as the subscription's
// units of the fixed charge. Any other change moves the subscription to
// an override plan, with its own units of the other fixed charges copied
// there, and the override of the fixed charge takes the properties,
// display name, units and taxes set in in; the other fields of in are
// ignored. Units applied immediately to a pay-in-advance fixed charge are
// billed. An incomplete subscription is refused with
// subscription_incomplete.
//
// source: app/services/subscriptions/update_or_override_fixed_charge_service.rb:20-147
func (s *Service) UpdateSubscriptionFixedCharge(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, fixedCharge refs.Ref, in FixedChargeInput) (*catalog.FixedCharge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.FixedCharge, error) {
		sb, err := refs.Subscription(ctx, r, org, sub)
		if err != nil {
			return nil, err
		}
		if sb.Status == subscription.StatusIncomplete {
			var v core.Validator
			v.Add("base", codeSubscriptionIncomplete)
			return nil, v.Err()
		}
		f, err := fixedChargeOnPlan(ctx, r, org, sb.PlanID, fixedCharge, false)
		if err != nil {
			return nil, err
		}
		plan, err := r.Plans.Get(ctx, org, sb.PlanID)
		if err != nil {
			return nil, fmt.Errorf("load plan %s: %w", sb.PlanID, err)
		}
		parent, err := fixedChargeParent(ctx, r, f)
		if err != nil {
			return nil, err
		}
		ts := s.d.Now()
		if !plan.IsChild() && fixedChargeUnitsOnly(in) {
			if err := s.writeUnitsOverride(ctx, r, sb, parent, in.Units.Value, in.ApplyUnitsImmediately, ts); err != nil {
				return nil, err
			}
			return withSubscriptionUnits(ctx, r, parent, sb)
		}
		target, events, err := s.overrideFixedChargeViaPlan(ctx, r, sb, plan, parent, in, ts)
		if err != nil {
			return nil, err
		}
		// A payment-gated subscription is incomplete, so refused above.
		if in.Units.Set && in.ApplyUnitsImmediately && target.PayInAdvance && len(events) > 0 {
			if err := s.enqueueInAdvanceFixedCharges(ctx, r, sb, events[len(events)-1].Timestamp); err != nil {
				return nil, err
			}
		}
		return withSubscriptionUnits(ctx, r, target, sb)
	})
}

// overrideFixedChargeViaPlan returns the override of parent on the
// subscription's override plan, with the values of in, and the units
// events recorded for it. A subscription of the original plan p moves to
// a new override plan whose copy of parent takes in, and whose copies of
// the fixed charges with the subscription's own units take those units;
// the subscription's units overrides are then discarded.
//
// source: app/services/subscriptions/update_or_override_fixed_charge_service.rb:58-130, app/services/subscriptions/concerns/fixed_charge_units_override_promotion_concern.rb:8-27
func (s *Service) overrideFixedChargeViaPlan(ctx context.Context, r port.Repos, sb *subscription.Subscription, p *catalog.Plan, parent *catalog.FixedCharge, in FixedChargeInput, ts time.Time) (*catalog.FixedCharge, []*subscription.FixedChargeEvent, error) {
	org := sb.OrganizationID
	if p.IsChild() {
		target, err := s.ensurePlanOverride(ctx, r, sb)
		if err != nil {
			return nil, nil, err
		}
		existing, err := fixedChargeOverrideOn(ctx, r, target, parent.ID)
		if err != nil {
			return nil, nil, err
		}
		if existing == nil {
			return s.overrideFixedChargeAt(ctx, r, parent, target, &in, sb, ts)
		}
		return s.updateFixedChargeOverride(ctx, r, existing, in, sb, ts)
	}
	promoted := map[core.FixedChargeID]FixedChargeInput{parent.ID: in}
	own, err := r.FixedChargeUnitsOverrides.ListBySubscription(ctx, org, sb.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load units overrides of subscription %s: %w", sb.ID, err)
	}
	for _, o := range own {
		if _, ok := promoted[o.FixedChargeID]; !ok {
			promoted[o.FixedChargeID] = FixedChargeInput{Units: core.Some(o.Units)}
		}
		if err := r.FixedChargeUnitsOverrides.Discard(ctx, org, o.ID, s.d.Now()); err != nil {
			return nil, nil, fmt.Errorf("discard units override %s: %w", o.ID, err)
		}
	}
	// ensurePlanOverride takes no values, so the override plan is created
	// here with them (Subscriptions::Concerns::PlanOverrideConcern
	// #ensure_plan_override(params:)).
	target, err := s.Override(ctx, r, p, OverrideInput{FixedCharges: promoted}, sb)
	if err != nil {
		return nil, nil, err
	}
	sb.PlanID, sb.UpdatedAt = target.ID, s.d.Now()
	if err := r.Subscriptions.Update(ctx, sb); err != nil {
		return nil, nil, fmt.Errorf("update subscription %s: %w", sb.ID, err)
	}
	existing, err := fixedChargeOverrideOn(ctx, r, target, parent.ID)
	if err != nil {
		return nil, nil, err
	}
	if existing == nil {
		return s.overrideFixedChargeAt(ctx, r, parent, target, &in, sb, ts)
	}
	// The plan override already recorded the units of its copy.
	events, err := r.FixedChargeEvents.ListBySubscription(ctx, org, sb.ID, existing.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load units of fixed charge %s: %w", existing.ID, err)
	}
	return existing, events, nil
}

// updateFixedChargeOverride changes the override fixed charge f of the
// subscription's plan in place with the subscription-level fields of in,
// and records the subscription's units.
//
// source: app/services/subscriptions/update_or_override_fixed_charge_service.rb:110-132
func (s *Service) updateFixedChargeOverride(ctx context.Context, r port.Repos, f *catalog.FixedCharge, in FixedChargeInput, sb *subscription.Subscription, ts time.Time) (*catalog.FixedCharge, []*subscription.FixedChargeEvent, error) {
	f = f.Clone()
	var err error
	if in.Properties.Set {
		if f.Properties, err = fixedChargeFilterProperties(f.ChargeModel, in.Properties.Value); err != nil {
			return nil, nil, err
		}
	}
	in.InvoiceDisplayName.Apply(&f.InvoiceDisplayName)
	in.Units.Apply(&f.Units)
	if in.Taxes.Set {
		if f.TaxIDs, err = refs.Taxes(r, f.OrganizationID).IDs(ctx, in.Taxes.Value); err != nil {
			return nil, nil, err
		}
	}
	if err := validateFixedChargeRecord(f); err != nil {
		return nil, nil, err
	}
	f.UpdatedAt = s.d.Now()
	if err := r.FixedCharges.Update(ctx, f); err != nil {
		return nil, nil, chargeCodeConflict(err)
	}
	events, err := s.emitFixedChargeEvents(ctx, r, f, sb, in.ApplyUnitsImmediately, ts)
	if err != nil {
		return nil, nil, err
	}
	return f, events, nil
}

// writeUnitsOverride sets the units of the fixed charge f for the
// subscription sb, records them, and bills them when applied immediately
// to a pay-in-advance fixed charge of an active subscription
// (Subscriptions::FixedChargeUnitsOverrides::WriteService).
//
// source: app/services/subscriptions/fixed_charge_units_overrides/write_service.rb:17-44
func (s *Service) writeUnitsOverride(ctx context.Context, r port.Repos, sb *subscription.Subscription, f *catalog.FixedCharge, units money.Decimal, immediate bool, ts time.Time) error {
	o, err := unitsOverrideOf(ctx, r, sb, f.ID)
	if err != nil {
		return err
	}
	now := s.d.Now()
	create := o == nil
	if create {
		o = &subscription.FixedChargeUnitsOverride{
			ID:             core.NewID[core.FixedChargeUnitsOverrideID](s.d.IDs),
			OrganizationID: sb.OrganizationID,
			SubscriptionID: sb.ID,
			FixedChargeID:  f.ID,
			CreatedAt:      now,
		}
	}
	o.Units, o.UpdatedAt = units, now
	if err := o.Validate(); err != nil {
		return err
	}
	if create {
		err = r.FixedChargeUnitsOverrides.Create(ctx, o)
	} else {
		err = r.FixedChargeUnitsOverrides.Update(ctx, o)
	}
	if err != nil {
		return fmt.Errorf("write units override of fixed charge %s: %w", f.ID, err)
	}
	events, err := s.emitFixedChargeEvents(ctx, r, f, sb, immediate, ts)
	if err != nil {
		return err
	}
	if len(events) > 0 && immediate && f.PayInAdvance && sb.Status == subscription.StatusActive {
		return s.enqueueInAdvanceFixedCharges(ctx, r, sb, ts)
	}
	return nil
}

// enqueueInAdvanceFixedCharges enqueues the billing of the units of the
// subscription sb changed at ts (lago
// Invoices::CreatePayInAdvanceFixedChargesJob, unique until executed).
func (s *Service) enqueueInAdvanceFixedCharges(ctx context.Context, r port.Repos, sb *subscription.Subscription, ts time.Time) error {
	args := kernel.InAdvanceFixedChargesArgs{SubscriptionID: sb.ID, Timestamp: ts}
	key := kernel.InAdvanceFixedChargesKey(sb.ID, ts)
	return kernel.Enqueue(ctx, r, sb.OrganizationID, kernel.JobCreateInAdvanceFixedCharges, args, key, s.d.Now())
}

// fixedChargeUnitsOnly reports whether in changes the units alone (lago
// FixedChargeUnitsOverrideDetectionConcern#units_only_fixed_charge_params?).
//
// source: app/services/subscriptions/concerns/fixed_charge_units_override_detection_concern.rb:27-33
func fixedChargeUnitsOnly(in FixedChargeInput) bool {
	return in.Units.Set && !in.Properties.Set && !in.InvoiceDisplayName.Set && !in.Taxes.Set
}

// withSubscriptionUnits returns f with the units of the subscription sb:
// its own units of f when it has them (lago
// Subscription::FixedChargeUnitsOverride.units_map_for).
func withSubscriptionUnits(ctx context.Context, r port.Repos, f *catalog.FixedCharge, sb *subscription.Subscription) (*catalog.FixedCharge, error) {
	units, err := effectiveFixedChargeUnits(ctx, r, f, sb)
	if err != nil {
		return nil, err
	}
	if units.Equal(f.Units) {
		return f, nil
	}
	out := f.Clone()
	out.Units = units
	return out, nil
}

// fixedChargeParent returns the parent of the override fixed charge f, or
// f itself (FixedCharge#parent_or_self).
//
// source: app/models/fixed_charge.rb:61-67
func fixedChargeParent(ctx context.Context, r port.Repos, f *catalog.FixedCharge) (*catalog.FixedCharge, error) {
	if !f.IsChild() {
		return f, nil
	}
	p, err := r.FixedCharges.Get(ctx, f.OrganizationID, f.ParentID)
	if err != nil {
		return nil, fmt.Errorf("load parent fixed charge %s: %w", f.ParentID, err)
	}
	return p, nil
}
