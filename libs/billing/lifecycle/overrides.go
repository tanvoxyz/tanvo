package lifecycle

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// CreateWithUnits is [Service.Create] for a subscription that keeps its
// plan with its own units of some of the plan's fixed charges: when sub is
// created, units are stored, each with sub's ID, before it starts, so its
// activation records them as the units in force. They are ignored when
// the external ID designates a current subscription.
//
// source: app/services/subscriptions/create_service.rb:171-174,299-313
func (s *Service) CreateWithUnits(ctx context.Context, sub *subscription.Subscription, units []*subscription.FixedChargeUnitsOverride, rules ...*subscription.ActivationRule) (*subscription.Subscription, error) {
	return s.createWith(ctx, sub, units, rules)
}

// storeUnits stores the units overrides of the new subscription sub.
func storeUnits(ctx context.Context, r port.Repos, sub *subscription.Subscription, units []*subscription.FixedChargeUnitsOverride) error {
	for _, o := range units {
		o.OrganizationID, o.SubscriptionID = sub.OrganizationID, sub.ID
		if err := o.Validate(); err != nil {
			return err
		}
		if err := r.FixedChargeUnitsOverrides.Create(ctx, o); err != nil {
			return fmt.Errorf("create units override of fixed charge %s: %w", o.FixedChargeID, err)
		}
	}
	return nil
}

// emitActivationEvents records the fixed charge units of an activated
// subscription, unless it activates from incomplete: its gating recorded
// them.
//
// source: app/services/subscriptions/activate_service.rb:74,104,130
func (s *Service) emitActivationEvents(ctx context.Context, r port.Repos, a *activation) error {
	if a.fromIncomplete {
		return nil
	}
	return s.emitFixedChargeEvents(ctx, r, a.env)
}

// emitFixedChargeEvents records, one second after the start of env's
// subscription, the units in force of each fixed charge of its plan: the
// subscription's own units, else the fixed charge's. A fixed charge with
// an event on that local day already keeps it. The events apply
// immediately: they set the units of the first period.
//
// source: app/services/subscriptions/emit_fixed_charge_events_service.rb:13-56, app/models/fixed_charge.rb:54-58
func (s *Service) emitFixedChargeEvents(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv) error {
	sub := env.Subscription
	if sub.StartedAt == nil {
		return nil
	}
	org := sub.OrganizationID
	fixed, err := env.FixedCharges(ctx, r)
	if err != nil {
		return err
	}
	if len(fixed) == 0 {
		return nil
	}
	own, err := r.FixedChargeUnitsOverrides.ListBySubscription(ctx, org, sub.ID)
	if err != nil {
		return fmt.Errorf("load units overrides of subscription %s: %w", sub.ID, err)
	}
	emitted, err := r.FixedChargeEvents.ListBySubscription(ctx, org, sub.ID, core.FixedChargeID{})
	if err != nil {
		return fmt.Errorf("load fixed charge events of subscription %s: %w", sub.ID, err)
	}
	loc := env.Location()
	ts := sub.StartedAt.Add(time.Second)
	day := core.DateOf(ts, loc)
	now := s.d.Now()
	for _, f := range fixed {
		if slices.ContainsFunc(emitted, func(e *subscription.FixedChargeEvent) bool {
			return e.FixedChargeID == f.ID && core.DateOf(e.Timestamp, loc) == day
		}) {
			continue
		}
		units := f.Units
		if i := slices.IndexFunc(own, func(o *subscription.FixedChargeUnitsOverride) bool { return o.FixedChargeID == f.ID }); i >= 0 {
			units = own[i].Units
		}
		ev := &subscription.FixedChargeEvent{
			ID:                    core.NewID[core.FixedChargeEventID](s.d.IDs),
			OrganizationID:        org,
			SubscriptionID:        sub.ID,
			FixedChargeID:         f.ID,
			Units:                 units,
			Timestamp:             ts,
			ApplyUnitsImmediately: true,
			CreatedAt:             now,
			UpdatedAt:             now,
		}
		if err := ev.Validate(); err != nil {
			return err
		}
		if err := r.FixedChargeEvents.Record(ctx, ev, loc); err != nil {
			return fmt.Errorf("record units of fixed charge %s: %w", f.ID, err)
		}
	}
	return nil
}

// planChange returns the change from the plan from to the plan to of a
// plan change. An override plan is compared through the plan it
// overrides, as the request names that plan.
//
// source: app/services/subscriptions/create_service.rb:136-148
func planChange(ctx context.Context, r port.Repos, from, to *catalog.Plan) (period.Change, error) {
	if !to.ParentID.IsZero() {
		p, err := r.Plans.Get(ctx, to.OrganizationID, to.ParentID)
		if err != nil {
			return period.NoChange, fmt.Errorf("load plan %s: %w", to.ParentID, err)
		}
		if from.ID != to.ID {
			to = p
		}
	}
	return kernel.PlanChange(from, to), nil
}
