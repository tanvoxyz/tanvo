package plans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// fixedChargeJobs returns the handlers of the fixed-charge cascade jobs.
func (s *Service) fixedChargeJobs() map[string]func(context.Context, port.Job) error {
	return map[string]func(context.Context, port.Job) error{
		kernel.JobCreateFixedChargeChildren:      s.handleCreateFixedChargeChildren,
		kernel.JobUpdateFixedChargeChildren:      s.handleUpdateFixedChargeChildren,
		kernel.JobDestroyFixedChargeChildren:     s.handleDestroyFixedChargeChildren,
		kernel.JobCascadeFixedCharges:            s.handleCascadeFixedCharges,
		kernel.JobCreateAllInAdvanceFixedCharges: s.handleAllInAdvanceFixedCharges,
	}
}

// handleCreateFixedChargeChildren runs a
// [kernel.JobCreateFixedChargeChildren]. Without plans, it lists the
// override plans with active or pending subscriptions and enqueues one job
// per batch of them; with plans, the fixed charge is created, from the
// payload of its parent, on each plan of the batch that has no copy of it
// yet, as the job may run more than once.
//
// source: app/jobs/fixed_charges/create_children_job.rb:7-18, app/jobs/fixed_charges/create_children_batch_job.rb:7-9, app/services/fixed_charges/create_children_service.rb:14-33
func (s *Service) handleCreateFixedChargeChildren(ctx context.Context, j port.Job) error {
	var a kernel.CreateFixedChargeChildrenArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	org := j.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.FixedCharges.Get(ctx, org, a.FixedChargeID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load fixed charge %s: %w", a.FixedChargeID, err)
		}
		if f.DeletedAt != nil {
			return nil
		}
		if a.PlanIDs == nil {
			plans, err := s.livePlanChildren(ctx, r, org, f.PlanID)
			if err != nil {
				return err
			}
			for batch := range slices.Chunk(plans, kernel.FixedChargeChildrenBatch) {
				b := a
				for _, p := range batch {
					b.PlanIDs = append(b.PlanIDs, p.ID)
				}
				if err := kernel.Enqueue(ctx, r, org, j.Kind, b, "", s.d.Now()); err != nil {
					return err
				}
			}
			return nil
		}
		var in FixedChargeInput
		if err := json.Unmarshal(a.Payload, &in); err != nil {
			return fmt.Errorf("decode %s payload: %w", j.Kind, err)
		}
		in.ID, in.parent = core.FixedChargeID{}, f.ID
		if in.Code.Value == "" {
			in.Code = core.Some(f.Code)
		}
		for _, id := range a.PlanIDs {
			p, err := r.Plans.Get(ctx, org, id)
			if err != nil {
				return fmt.Errorf("load plan %s: %w", id, err)
			}
			existing, err := fixedChargeOverrideOn(ctx, r, p, f.ID)
			if err != nil {
				return err
			}
			if existing != nil {
				continue
			}
			if _, err := s.createFixedCharge(ctx, r, p, in, false); err != nil {
				return fmt.Errorf("create fixed charge %s on plan %s: %w", f.ID, p.ID, err)
			}
		}
		return nil
	})
}

// handleUpdateFixedChargeChildren runs a
// [kernel.JobUpdateFixedChargeChildren]. Without children, it lists the
// overrides of the fixed charge on plans with active or pending
// subscriptions and enqueues one job per batch of them; with children,
// each override of the batch takes the new pricing in cascade mode,
// without billing.
//
// source: app/jobs/fixed_charges/update_children_job.rb:7-18, app/jobs/fixed_charges/update_children_batch_job.rb:7-20, app/services/fixed_charges/update_children_service.rb:16-38
func (s *Service) handleUpdateFixedChargeChildren(ctx context.Context, j port.Job) error {
	var a kernel.UpdateFixedChargeChildrenArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	org := j.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if a.ChildIDs == nil {
			children, err := s.liveFixedChargeChildren(ctx, r, org, a.FixedChargeID)
			if err != nil {
				return err
			}
			for batch := range slices.Chunk(children, kernel.FixedChargeChildrenBatch) {
				b := a
				for _, c := range batch {
					b.ChildIDs = append(b.ChildIDs, c.ID)
				}
				if err := kernel.Enqueue(ctx, r, org, j.Kind, b, "", s.d.Now()); err != nil {
					return err
				}
			}
			return nil
		}
		old := &catalog.FixedCharge{ChargeModel: a.OldChargeModel, Properties: a.OldProperties, Units: a.OldUnits}
		in := FixedChargeInput{
			ChargeModel: core.Some(a.ChargeModel),
			Properties:  core.Some(a.Properties),
			Units:       core.Some(a.Units),
		}
		if a.Code != "" {
			in.Code = core.Some(a.Code)
		}
		ts := s.d.Now()
		for _, id := range a.ChildIDs {
			child, err := r.FixedCharges.Get(ctx, org, id)
			if err != nil {
				return fmt.Errorf("load override fixed charge %s: %w", id, err)
			}
			if child.DeletedAt != nil {
				continue
			}
			u := fixedChargeUpdate{cascade: true, equalProperties: old.EqualProperties(child)}
			if _, err := s.updateFixedCharge(ctx, r, child, in, u, ts); err != nil {
				return fmt.Errorf("update override fixed charge %s: %w", child.ID, err)
			}
		}
		return nil
	})
}

// handleDestroyFixedChargeChildren runs a
// [kernel.JobDestroyFixedChargeChildren]: once the fixed charge is
// discarded, its overrides on plans with active or pending subscriptions
// are discarded too.
//
// source: app/jobs/fixed_charges/destroy_children_job.rb:7-10, app/services/fixed_charges/destroy_children_service.rb:12-27
func (s *Service) handleDestroyFixedChargeChildren(ctx context.Context, j port.Job) error {
	var a kernel.FixedChargeArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	org := j.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.FixedCharges.Get(ctx, org, a.FixedChargeID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load fixed charge %s: %w", a.FixedChargeID, err)
		}
		if f.DeletedAt == nil {
			return nil
		}
		children, err := s.liveFixedChargeChildren(ctx, r, org, f.ID)
		if err != nil {
			return err
		}
		for _, child := range children {
			if _, err := s.discardFixedCharge(ctx, r, child, false); err != nil {
				return err
			}
		}
		return nil
	})
}

// handleCascadeFixedCharges runs a [kernel.JobCascadeFixedCharges]. On the
// parent plan, it enqueues one job per override plan with active or
// pending subscriptions; on an override plan, it creates the new fixed
// charges it has no copy of yet, updates the copies of the updated ones in
// cascade mode, without billing, and then bills its active subscriptions
// when it has pay-in-advance fixed charges.
//
// source: app/jobs/fixed_charges/cascade_plan_update_job.rb:7-21, app/jobs/fixed_charges/cascade_child_plan_update_job.rb, app/services/fixed_charges/cascade_child_plan_update_service.rb:15-57
func (s *Service) handleCascadeFixedCharges(ctx context.Context, j port.Job) error {
	var a kernel.CascadeFixedChargesArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	org := j.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if !a.Child {
			children, err := s.livePlanChildren(ctx, r, org, a.PlanID)
			if err != nil {
				return err
			}
			for _, p := range children {
				b := a
				b.PlanID, b.Child = p.ID, true
				if err := kernel.Enqueue(ctx, r, org, j.Kind, b, "", s.d.Now()); err != nil {
					return err
				}
			}
			return nil
		}
		var payload []fixedChargeCascade
		if err := json.Unmarshal(a.Payload, &payload); err != nil {
			return fmt.Errorf("decode %s payload: %w", j.Kind, err)
		}
		if len(payload) == 0 {
			return nil
		}
		p, err := r.Plans.Get(ctx, org, a.PlanID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", a.PlanID, err)
		}
		for _, c := range payload {
			existing, err := fixedChargeOverrideOn(ctx, r, p, c.FixedChargeID)
			if err != nil {
				return err
			}
			switch c.Action {
			case fixedChargeCascadeCreate:
				if existing != nil {
					continue
				}
				in := c.Input
				in.ID, in.parent = core.FixedChargeID{}, c.FixedChargeID
				if _, err := s.createFixedChargeAt(ctx, r, p, in, false, a.Timestamp); err != nil {
					return fmt.Errorf("create fixed charge %s on plan %s: %w", c.FixedChargeID, p.ID, err)
				}
			case fixedChargeCascadeUpdate:
				if existing == nil {
					continue
				}
				old := &catalog.FixedCharge{ChargeModel: c.OldChargeModel, Properties: c.OldProperties, Units: c.OldUnits}
				u := fixedChargeUpdate{cascade: true, equalProperties: old.EqualProperties(existing)}
				if _, err := s.updateFixedCharge(ctx, r, existing, c.Input, u, a.Timestamp); err != nil {
					return fmt.Errorf("update override fixed charge %s: %w", existing.ID, err)
				}
			default:
				return fmt.Errorf("unknown fixed charge cascade action %q", c.Action)
			}
		}
		return s.billInAdvanceFixedCharges(ctx, r, p, a.Timestamp)
	})
}

// handleAllInAdvanceFixedCharges runs a
// [kernel.JobCreateAllInAdvanceFixedCharges]: it enqueues the billing at
// the instant of the change of each active subscription of the plan,
// except, with a fixed charge, those with their own units of it.
//
// source: app/jobs/invoices/create_all_pay_in_advance_fixed_charges_job.rb:18-27
func (s *Service) handleAllInAdvanceFixedCharges(ctx context.Context, j port.Job) error {
	var a kernel.AllInAdvanceFixedChargesArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	org := j.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		subs, err := kernel.All(func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
			f := subscription.Filter{PlanID: a.PlanID, Statuses: []subscription.Status{subscription.StatusActive}}
			return r.Subscriptions.List(ctx, org, f, p)
		})
		if err != nil {
			return fmt.Errorf("load subscriptions of plan %s: %w", a.PlanID, err)
		}
		own := map[core.SubscriptionID]bool{}
		if !a.FixedChargeID.IsZero() {
			overrides, err := r.FixedChargeUnitsOverrides.ListByFixedCharge(ctx, org, a.FixedChargeID)
			if err != nil {
				return fmt.Errorf("load units overrides of fixed charge %s: %w", a.FixedChargeID, err)
			}
			for _, o := range overrides {
				own[o.SubscriptionID] = true
			}
		}
		for _, sub := range subs {
			if own[sub.ID] {
				continue
			}
			args := kernel.InAdvanceFixedChargesArgs{SubscriptionID: sub.ID, Timestamp: a.Timestamp}
			key := kernel.InAdvanceFixedChargesKey(sub.ID, a.Timestamp)
			if err := kernel.Enqueue(ctx, r, org, kernel.JobCreateInAdvanceFixedCharges, args, key, s.d.Now()); err != nil {
				return err
			}
		}
		return nil
	})
}

// liveFixedChargeChildren returns the overrides of the fixed charge id on
// plans that have active or pending subscriptions.
func (s *Service) liveFixedChargeChildren(ctx context.Context, r port.Repos, org core.OrganizationID, id core.FixedChargeID) ([]*catalog.FixedCharge, error) {
	children, err := r.FixedCharges.ListChildren(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load overrides of fixed charge %s: %w", id, err)
	}
	live := map[core.PlanID]bool{}
	out := children[:0]
	for _, c := range children {
		ok, seen := live[c.PlanID]
		if !seen {
			if ok, err = planLive(ctx, r, org, c.PlanID); err != nil {
				return nil, err
			}
			live[c.PlanID] = ok
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// fixedChargeOverrideOn returns the kept override of the fixed charge
// parent on the plan p, or nil.
func fixedChargeOverrideOn(ctx context.Context, r port.Repos, p *catalog.Plan, parent core.FixedChargeID) (*catalog.FixedCharge, error) {
	fcs, err := r.FixedCharges.ListByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return nil, fmt.Errorf("load fixed charges of plan %s: %w", p.ID, err)
	}
	if i := slices.IndexFunc(fcs, func(f *catalog.FixedCharge) bool { return f.ParentID == parent }); i >= 0 {
		return fcs[i], nil
	}
	return nil, nil
}
