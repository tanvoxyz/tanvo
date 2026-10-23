package entitlements

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// ValueInput is the value of one privilege.
type ValueInput struct {
	// Code is the privilege's code.
	Code string
	// Value is the value, in the privilege's value type.
	Value string
}

// EntitlementInput grants a feature with privilege values.
type EntitlementInput struct {
	// Feature is the granted feature.
	Feature refs.Ref
	// Values are the privilege values; a code given twice keeps its last
	// value.
	Values []ValueInput
}

// ListPlanEntitlements returns the entitlements of the original plan
// plan.
//
// source: app/controllers/api/v1/plans/entitlements_controller.rb:9-22
func (s *Service) ListPlanEntitlements(ctx context.Context, org core.OrganizationID, plan refs.Ref) ([]Entitlement, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]Entitlement, error) {
		p, err := findPlan(ctx, r, org, plan)
		if err != nil {
			return nil, err
		}
		ents, err := r.Entitlements.ListByPlan(ctx, org, p.ID)
		if err != nil {
			return nil, err
		}
		return planView(ctx, r, org, ents)
	})
}

// GetPlanEntitlement returns the plan's entitlement of feature; a feature
// the plan does not grant is core.NotFound("entitlement").
//
// source: app/controllers/api/v1/plans/entitlements_controller.rb:24-31,111-119
func (s *Service) GetPlanEntitlement(ctx context.Context, org core.OrganizationID, plan, feature refs.Ref) (Entitlement, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (Entitlement, error) {
		_, e, err := planEntitlement(ctx, r, org, plan, feature)
		if err != nil {
			return Entitlement{}, err
		}
		v, err := planView(ctx, r, org, []*catalog.Entitlement{e})
		if err != nil {
			return Entitlement{}, err
		}
		return v[0], nil
	})
}

// ReplacePlanEntitlements makes ents the plan's entitlements: the features
// missing from ents are removed, and a given feature with values loses the
// values not given. It emits plan.updated, even when nothing changed, and
// returns the plan's entitlements. An unknown feature is
// core.NotFound("feature"), an unknown privilege core.NotFound("privilege").
//
// source: app/services/entitlement/plan_entitlements_update_service.rb:28-58
// (partial: false, as Api::V1::Plans::EntitlementsController#create)
func (s *Service) ReplacePlanEntitlements(ctx context.Context, org core.OrganizationID, plan refs.Ref, ents []EntitlementInput) ([]Entitlement, error) {
	out, err := s.updatePlan(ctx, org, plan, ents, false)
	if err != nil {
		return nil, fmt.Errorf("replace plan entitlements: %w", err)
	}
	return out, nil
}

// UpsertPlanEntitlement grants the plan a feature, or changes its values,
// keeping the values not given. It emits plan.updated and returns the
// plan's entitlement of the feature.
//
// source: app/services/entitlement/plan_entitlements_update_service.rb:28-58
// (partial: true, as Api::V1::Plans::EntitlementsController#update)
func (s *Service) UpsertPlanEntitlement(ctx context.Context, org core.OrganizationID, plan refs.Ref, ent EntitlementInput) (Entitlement, error) {
	out, err := s.updatePlan(ctx, org, plan, []EntitlementInput{ent}, true)
	if err != nil {
		return Entitlement{}, fmt.Errorf("upsert plan entitlement: %w", err)
	}
	return out[0], nil
}

// DeletePlanEntitlement removes the plan's entitlement of feature with its
// values and emits plan.updated; a feature the plan does not grant is
// core.NotFound("entitlement").
//
// source: app/services/entitlement/plan_entitlement_destroy_service.rb:18-30
func (s *Service) DeletePlanEntitlement(ctx context.Context, org core.OrganizationID, plan, feature refs.Ref) error {
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, e, err := planEntitlement(ctx, r, org, plan, feature)
		if err != nil {
			return err
		}
		if err := r.Entitlements.Discard(ctx, org, e.ID, s.d.Now()); err != nil {
			return err
		}
		return s.emitPlans(ctx, org, []*catalog.Plan{p})
	})
	if err != nil {
		return fmt.Errorf("delete plan entitlement: %w", err)
	}
	return nil
}

// DeletePlanEntitlementPrivilege removes the value of privilege code from
// the plan's entitlement of feature, emits plan.updated and returns the
// entitlement. A feature the plan does not grant is
// core.NotFound("entitlement"), a privilege without value
// core.NotFound("privilege").
//
// source: app/services/entitlement/plan_entitlement_privilege_destroy_service.rb:19-35
func (s *Service) DeletePlanEntitlementPrivilege(ctx context.Context, org core.OrganizationID, plan, feature refs.Ref, code string) (Entitlement, error) {
	var out Entitlement
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, e, err := planEntitlement(ctx, r, org, plan, feature)
		if err != nil {
			return err
		}
		if _, ok := e.Values[code]; !ok {
			return core.NotFound("privilege")
		}
		delete(e.Values, code)
		e.UpdatedAt = s.d.Now()
		if err := r.Entitlements.Update(ctx, e); err != nil {
			return err
		}
		v, err := planView(ctx, r, org, []*catalog.Entitlement{e})
		if err != nil {
			return err
		}
		out = v[0]
		return s.emitPlans(ctx, org, []*catalog.Plan{p})
	})
	if err != nil {
		return Entitlement{}, fmt.Errorf("delete plan entitlement privilege: %w", err)
	}
	return out, nil
}

// updatePlan is PlanEntitlementsUpdateService: it returns the entitlements
// of the given features when partial, all the plan's otherwise.
func (s *Service) updatePlan(ctx context.Context, org core.OrganizationID, plan refs.Ref, in []EntitlementInput, partial bool) ([]Entitlement, error) {
	var out []Entitlement
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := findPlan(ctx, r, org, plan)
		if err != nil {
			return err
		}
		grants, err := resolveGrants(ctx, r, org, in)
		if err != nil {
			return err
		}
		current, err := r.Entitlements.ListByPlan(ctx, org, p.ID)
		if err != nil {
			return err
		}
		now := s.d.Now()
		byFeature := map[core.FeatureID]*catalog.Entitlement{}
		for _, e := range current {
			byFeature[e.FeatureID] = e
		}
		if !partial {
			// source: plan_entitlements_update_service.rb:66-70
			for _, e := range current {
				if !slices.ContainsFunc(grants, func(g grant) bool { return g.feature.ID == e.FeatureID }) {
					if err := r.Entitlements.Discard(ctx, org, e.ID, now); err != nil {
						return err
					}
					delete(byFeature, e.FeatureID)
				}
			}
		}
		var touched []*catalog.Entitlement
		for _, g := range grants {
			e, ok := byFeature[g.feature.ID]
			if !ok {
				e = &catalog.Entitlement{
					ID:             core.NewID[core.EntitlementID](s.d.IDs),
					OrganizationID: org,
					FeatureID:      g.feature.ID,
					PlanID:         p.ID,
					Values:         map[string]string{},
					CreatedAt:      now,
				}
			} else if !partial && len(g.values) > 0 {
				// source: plan_entitlements_update_service.rb:72-76
				maps.DeleteFunc(e.Values, func(code, _ string) bool { return !slices.Contains(g.codes, code) })
			}
			if e.Values == nil {
				e.Values = map[string]string{}
			}
			for _, code := range g.codes {
				priv, ok := g.feature.Privilege(code)
				if !ok {
					return core.NotFound("privilege")
				}
				v, err := validateValue(priv, g.values[code])
				if err != nil {
					return err
				}
				e.Values[code] = v
			}
			e.UpdatedAt = now
			if ok {
				err = r.Entitlements.Update(ctx, e)
			} else {
				err = r.Entitlements.Create(ctx, e)
			}
			if err != nil {
				return err
			}
			byFeature[e.FeatureID] = e
			touched = append(touched, e)
		}
		if err := s.emitPlans(ctx, org, []*catalog.Plan{p}); err != nil {
			return err
		}
		if partial {
			out, err = planView(ctx, r, org, touched)
			return err
		}
		ents, err := r.Entitlements.ListByPlan(ctx, org, p.ID)
		if err != nil {
			return err
		}
		out, err = planView(ctx, r, org, ents)
		return err
	})
	return out, err
}

// grant is an EntitlementInput with its feature resolved and its values
// deduplicated.
type grant struct {
	feature *catalog.Feature
	// codes are the privilege codes in their first order.
	codes  []string
	values map[string]string
}

// resolveGrants resolves the features of in; a feature given twice keeps
// its first position and its last values, as a JSON object would.
func resolveGrants(ctx context.Context, r port.Repos, org core.OrganizationID, in []EntitlementInput) ([]grant, error) {
	var out []grant
	index := map[core.FeatureID]int{}
	for _, ei := range in {
		f, err := refs.Features(r, org).One(ctx, ei.Feature)
		if err != nil {
			return nil, err
		}
		g := grant{feature: f, values: map[string]string{}}
		for _, v := range ei.Values {
			if _, ok := g.values[v.Code]; !ok {
				g.codes = append(g.codes, v.Code)
			}
			g.values[v.Code] = v.Value
		}
		if i, ok := index[f.ID]; ok {
			out[i] = g
			continue
		}
		index[f.ID] = len(out)
		out = append(out, g)
	}
	return out, nil
}

// findPlan resolves an original plan (lago plans.parents.find_by!(code:),
// Api::V1::Plans::BaseController).
func findPlan(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (*catalog.Plan, error) {
	p, err := refs.Plans(r, org).One(ctx, ref)
	if err != nil {
		return nil, err
	}
	if p.IsChild() || p.Deleted() {
		return nil, core.NotFound("plan")
	}
	return p, nil
}

// planEntitlement returns the plan and its entitlement of feature; a
// missing feature or entitlement is core.NotFound("entitlement").
func planEntitlement(ctx context.Context, r port.Repos, org core.OrganizationID, plan, feature refs.Ref) (*catalog.Plan, *catalog.Entitlement, error) {
	p, err := findPlan(ctx, r, org, plan)
	if err != nil {
		return nil, nil, err
	}
	f, err := refs.Features(r, org).One(ctx, feature)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil, core.NotFound("entitlement")
	}
	if err != nil {
		return nil, nil, err
	}
	ents, err := r.Entitlements.ListByPlan(ctx, org, p.ID)
	if err != nil {
		return nil, nil, err
	}
	i := slices.IndexFunc(ents, func(e *catalog.Entitlement) bool { return e.FeatureID == f.ID })
	if i < 0 {
		return nil, nil, core.NotFound("entitlement")
	}
	return p, ents[i], nil
}
