package entitlements

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// ListSubscriptionEntitlements returns the resolved entitlements of the
// subscription: its plan's, minus its removals, overridden by its own.
//
// source: app/controllers/api/v1/subscriptions/entitlements_controller.rb:9-17
func (s *Service) ListSubscriptionEntitlements(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef) ([]Entitlement, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]Entitlement, error) {
		st, err := loadSubscription(ctx, r, org, sub)
		if err != nil {
			return nil, err
		}
		return st.view(ctx, r)
	})
}

// UpsertSubscriptionEntitlement overrides the values of a feature for the
// subscription, keeping the values not given: values equal to the plan's
// drop the override, and granting a feature or a privilege again lifts its
// removal. It emits subscription.updated, even when nothing changed, and
// returns the subscription's resolved entitlement of the feature. An
// unknown feature is core.NotFound("feature"), an unknown privilege
// core.NotFound("privilege").
//
// source: app/services/entitlement/subscription_entitlement_core_update_service.rb:21-139
// (partial: true, as Api::V1::Subscriptions::EntitlementsController#update)
func (s *Service) UpsertSubscriptionEntitlement(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, ent EntitlementInput) (Entitlement, error) {
	var out Entitlement
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		st, err := loadSubscription(ctx, r, org, sub)
		if err != nil {
			return err
		}
		grants, err := resolveGrants(ctx, r, org, []EntitlementInput{ent})
		if err != nil {
			return err
		}
		g := grants[0]
		if err := s.upsertSubscription(ctx, r, st, g); err != nil {
			return err
		}
		if out, err = st.reload(ctx, r, g.feature); err != nil {
			return err
		}
		return s.emitSubscription(ctx, st.sub)
	})
	if err != nil {
		return Entitlement{}, fmt.Errorf("upsert subscription entitlement: %w", err)
	}
	return out, nil
}

// DeleteSubscriptionEntitlement removes feature from the subscription: its
// override and privilege removals are deleted and, when the plan grants
// the feature, a removal of the feature is added. It emits
// subscription.updated. An unknown feature is core.NotFound("feature").
//
// source: app/services/entitlement/subscription_feature_remove_service.rb:18-67
func (s *Service) DeleteSubscriptionEntitlement(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, feature refs.Ref) error {
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		st, err := loadSubscription(ctx, r, org, sub)
		if err != nil {
			return err
		}
		f, err := refs.Features(r, org).One(ctx, feature)
		if err != nil {
			return err
		}
		if se := st.own(f.ID); se != nil {
			if err := r.Entitlements.Discard(ctx, org, se.ID, s.d.Now()); err != nil {
				return err
			}
		}
		if err := st.deleteRemovals(ctx, r, func(rm *catalog.EntitlementRemoval) bool {
			return rm.FeatureID == f.ID && rm.PrivilegeCode != ""
		}); err != nil {
			return err
		}
		if st.planned(f.ID) != nil {
			if err := s.addRemoval(ctx, r, st, f.ID, ""); err != nil {
				return err
			}
		}
		return s.emitSubscription(ctx, st.sub)
	})
	if err != nil {
		return fmt.Errorf("delete subscription entitlement: %w", err)
	}
	return nil
}

// DeleteSubscriptionEntitlementPrivilege removes privilege code of feature
// from the subscription: its override value is deleted and, when the plan
// grants the feature, a removal of the privilege is added. It emits
// subscription.updated and returns the subscription's resolved
// entitlement of the feature. An unknown feature is
// core.NotFound("feature"), an unknown privilege core.NotFound("privilege").
//
// source: app/services/entitlement/subscription_feature_privilege_remove_service.rb:19-68
func (s *Service) DeleteSubscriptionEntitlementPrivilege(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, feature refs.Ref, code string) (Entitlement, error) {
	var out Entitlement
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		st, err := loadSubscription(ctx, r, org, sub)
		if err != nil {
			return err
		}
		f, err := refs.Features(r, org).One(ctx, feature)
		if err != nil {
			return err
		}
		if _, ok := f.Privilege(code); !ok {
			return core.NotFound("privilege")
		}
		if se := st.own(f.ID); se != nil {
			if _, ok := se.Values[code]; ok {
				delete(se.Values, code)
				se.UpdatedAt = s.d.Now()
				if err := r.Entitlements.Update(ctx, se); err != nil {
					return err
				}
			}
		}
		if st.planned(f.ID) != nil {
			if err := s.addRemoval(ctx, r, st, f.ID, code); err != nil {
				return err
			}
		}
		if out, err = st.reload(ctx, r, f); err != nil {
			return err
		}
		return s.emitSubscription(ctx, st.sub)
	})
	if err != nil {
		return Entitlement{}, fmt.Errorf("delete subscription entitlement privilege: %w", err)
	}
	return out, nil
}

// upsertSubscription is SubscriptionEntitlementCoreUpdateService#
// process_single_entitlement with partial: true.
func (s *Service) upsertSubscription(ctx context.Context, r port.Repos, st *subState, g grant) error {
	f := g.feature
	pe, se := st.planned(f.ID), st.own(f.ID)
	featureRemoval := func(rm *catalog.EntitlementRemoval) bool {
		return rm.FeatureID == f.ID && rm.PrivilegeCode == ""
	}
	privilegeRemoval := func(code string) func(*catalog.EntitlementRemoval) bool {
		return func(rm *catalog.EntitlementRemoval) bool { return rm.FeatureID == f.ID && rm.PrivilegeCode == code }
	}
	now := s.d.Now()
	switch {
	case pe != nil && sameAsPlan(f, g, pe):
		// Restores the plan's defaults (core_update_service.rb:50-58).
		if se != nil {
			if err := r.Entitlements.Discard(ctx, st.org, se.ID, now); err != nil {
				return err
			}
		}
		return st.deleteRemovals(ctx, r, func(rm *catalog.EntitlementRemoval) bool { return rm.FeatureID == f.ID })
	case pe == nil && se == nil:
		// source: core_update_service.rb:46-48,72-86
		if err := st.deleteRemovals(ctx, r, featureRemoval); err != nil {
			return err
		}
		e := s.newOverride(st, f, now)
		for _, code := range g.codes {
			p, ok := f.Privilege(code)
			if !ok {
				return core.NotFound("privilege")
			}
			v, err := validateValue(p, g.values[code])
			if err != nil {
				return err
			}
			e.Values[code] = v
		}
		return r.Entitlements.Create(ctx, e)
	}
	// source: core_update_service.rb:59-65,107-128
	if err := st.deleteRemovals(ctx, r, featureRemoval); err != nil {
		return err
	}
	create := se == nil
	if create {
		se = s.newOverride(st, f, now)
	}
	if se.Values == nil {
		se.Values = map[string]string{}
	}
	for _, code := range g.codes {
		p, ok := f.Privilege(code)
		if !ok {
			return core.NotFound("privilege")
		}
		value := g.values[code]
		planVal, inPlan := "", false
		if pe != nil {
			planVal, inPlan = pe.Values[code]
		}
		subVal, inSub := se.Values[code]
		switch {
		case inPlan && sameValue(p.ValueType, value, planVal):
			delete(se.Values, code)
			if err := st.deleteRemovals(ctx, r, privilegeRemoval(code)); err != nil {
				return err
			}
		case !inSub:
			if err := st.deleteRemovals(ctx, r, privilegeRemoval(code)); err != nil {
				return err
			}
			fallthrough
		case !sameValue(p.ValueType, value, subVal):
			v, err := validateValue(p, value)
			if err != nil {
				return err
			}
			se.Values[code] = v
		}
	}
	se.UpdatedAt = now
	if create {
		return r.Entitlements.Create(ctx, se)
	}
	return r.Entitlements.Update(ctx, se)
}

func (s *Service) newOverride(st *subState, f *catalog.Feature, now time.Time) *catalog.Entitlement {
	return &catalog.Entitlement{
		ID:             core.NewID[core.EntitlementID](s.d.IDs),
		OrganizationID: st.org,
		FeatureID:      f.ID,
		SubscriptionID: st.sub.ID,
		Values:         map[string]string{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// addRemoval adds the removal of the feature, or of its privilege code,
// unless the subscription has it already (lago insert_all unique_by).
func (s *Service) addRemoval(ctx context.Context, r port.Repos, st *subState, feature core.FeatureID, code string) error {
	if slices.ContainsFunc(st.removals, func(rm *catalog.EntitlementRemoval) bool {
		return rm.FeatureID == feature && rm.PrivilegeCode == code
	}) {
		return nil
	}
	rm := &catalog.EntitlementRemoval{
		ID:             core.NewID[core.EntitlementRemovalID](s.d.IDs),
		OrganizationID: st.org,
		SubscriptionID: st.sub.ID,
		FeatureID:      feature,
		PrivilegeCode:  code,
		CreatedAt:      s.d.Now(),
	}
	if err := r.Entitlements.CreateRemoval(ctx, rm); err != nil && !errors.Is(err, core.ErrConflict) {
		return err
	}
	return nil
}

func (s *Service) emitSubscription(ctx context.Context, sub *subscription.Subscription) error {
	return kernel.Emit(ctx, s.d, sub.OrganizationID, webhook.EventTypeSubscriptionUpdated, "subscription", sub.ID.UUID(), sub)
}

// sameAsPlan reports whether the values given are exactly the plan's
// (lago privilege_params_same_as_plan?).
func sameAsPlan(f *catalog.Feature, g grant, pe *catalog.Entitlement) bool {
	if len(g.codes) != len(pe.Values) {
		return false
	}
	for code, planVal := range pe.Values {
		v, ok := g.values[code]
		if !ok {
			return false
		}
		p, _ := f.Privilege(code)
		if !sameValue(p.ValueType, planVal, v) {
			return false
		}
	}
	return true
}

// subState is a subscription with its plan's entitlements, its own and
// its removals.
type subState struct {
	org      core.OrganizationID
	sub      *subscription.Subscription
	planID   core.PlanID
	plan     []*catalog.Entitlement
	owned    []*catalog.Entitlement
	removals []*catalog.EntitlementRemoval
}

// loadSubscription resolves sub and loads its entitlements; the plan is
// the original plan of an override plan (lago subscription.plan.parent ||
// subscription.plan).
func loadSubscription(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.SubscriptionRef) (*subState, error) {
	sub, err := refs.Subscription(ctx, r, org, ref)
	if err != nil {
		return nil, err
	}
	p, err := r.Plans.Get(ctx, org, sub.PlanID)
	if err != nil {
		return nil, fmt.Errorf("get plan %s: %w", sub.PlanID, err)
	}
	st := &subState{org: org, sub: sub, planID: p.ID}
	if p.IsChild() {
		st.planID = p.ParentID
	}
	return st, st.load(ctx, r)
}

func (st *subState) load(ctx context.Context, r port.Repos) error {
	var err error
	if st.plan, err = r.Entitlements.ListByPlan(ctx, st.org, st.planID); err != nil {
		return err
	}
	if st.owned, err = r.Entitlements.ListBySubscription(ctx, st.org, st.sub.ID); err != nil {
		return err
	}
	st.removals, err = r.Entitlements.ListRemovalsBySubscription(ctx, st.org, st.sub.ID)
	return err
}

func (st *subState) planned(f core.FeatureID) *catalog.Entitlement { return find(st.plan, f) }

func (st *subState) own(f core.FeatureID) *catalog.Entitlement { return find(st.owned, f) }

// deleteRemovals deletes the subscription's removals matching pred.
func (st *subState) deleteRemovals(ctx context.Context, r port.Repos, pred func(*catalog.EntitlementRemoval) bool) error {
	var kept []*catalog.EntitlementRemoval
	for _, rm := range st.removals {
		if !pred(rm) {
			kept = append(kept, rm)
			continue
		}
		if err := r.Entitlements.DeleteRemoval(ctx, st.org, rm.ID); err != nil {
			return err
		}
	}
	st.removals = kept
	return nil
}

func (st *subState) view(ctx context.Context, r port.Repos) ([]Entitlement, error) {
	return subscriptionView(ctx, r, st.org, st.plan, st.owned, st.removals)
}

// reload loads the state again and returns the resolved entitlement of f;
// a feature the subscription is not granted has no values.
func (st *subState) reload(ctx context.Context, r port.Repos, f *catalog.Feature) (Entitlement, error) {
	if err := st.load(ctx, r); err != nil {
		return Entitlement{}, err
	}
	all, err := st.view(ctx, r)
	if err != nil {
		return Entitlement{}, err
	}
	if i := slices.IndexFunc(all, func(e Entitlement) bool { return e.Feature.ID == f.ID }); i >= 0 {
		return all[i], nil
	}
	return Entitlement{Feature: f}, nil
}

func find(ents []*catalog.Entitlement, f core.FeatureID) *catalog.Entitlement {
	if i := slices.IndexFunc(ents, func(e *catalog.Entitlement) bool { return e.FeatureID == f }); i >= 0 {
		return ents[i]
	}
	return nil
}
