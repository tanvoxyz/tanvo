package entitlements

import (
	"context"
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Source says where a feature or a privilege value of a subscription comes
// from.
type Source string

// Source values; plan entitlements have none.
const (
	// SourcePlan is the subscription's plan.
	SourcePlan Source = "plan"
	// SourceSubscription is the subscription's own override.
	SourceSubscription Source = "subscription"
)

// Entitlement is a feature granted with its privilege values, as the API
// presents it.
type Entitlement struct {
	// Feature is the granted feature.
	Feature *catalog.Feature
	// Values are the privilege values, in the feature's privilege order.
	Values []Value
	// Source is where the feature comes from: the plan when it grants the
	// feature, else the subscription; "" for a plan's entitlement.
	Source Source
}

// Value is the value of one privilege.
type Value struct {
	// Privilege is the valued privilege.
	Privilege catalog.Privilege
	// Value is the stored value.
	Value string
	// Source is where the value comes from; "" for a plan's entitlement.
	Source Source
}

// planView returns the views of a plan's entitlements (lago
// V1::Entitlement::PlanEntitlementSerializer).
func planView(ctx context.Context, r port.Repos, org core.OrganizationID, ents []*catalog.Entitlement) ([]Entitlement, error) {
	ids := make([]core.FeatureID, len(ents))
	for i, e := range ents {
		ids[i] = e.FeatureID
	}
	features, err := r.Features.ListByIDs(ctx, org, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Entitlement, len(ents))
	for i, e := range ents {
		out[i] = Entitlement{Feature: features[i]}
		for _, p := range features[i].Privileges {
			if v, ok := e.Values[p.Code]; ok {
				out[i].Values = append(out[i].Values, Value{Privilege: p, Value: v})
			}
		}
	}
	return out, nil
}

// subscriptionView returns the resolved entitlements of a subscription
// from its plan's entitlements, its own and its removals (lago
// Entitlement::SubscriptionEntitlementQuery).
func subscriptionView(ctx context.Context, r port.Repos, org core.OrganizationID, plan, own []*catalog.Entitlement, removals []*catalog.EntitlementRemoval) ([]Entitlement, error) {
	eff := catalog.EffectiveEntitlements(deref(plan), deref(own), deref(removals))
	ids := make([]core.FeatureID, len(eff))
	for i, e := range eff {
		ids[i] = e.FeatureID
	}
	features, err := r.Features.ListByIDs(ctx, org, ids)
	if err != nil {
		return nil, err
	}
	ownValues := map[core.FeatureID]map[string]string{}
	for _, e := range own {
		ownValues[e.FeatureID] = e.Values
	}
	out := make([]Entitlement, len(eff))
	for i, e := range eff {
		out[i] = Entitlement{Feature: features[i], Source: SourceSubscription}
		if !e.PlanEntitlementID.IsZero() {
			out[i].Source = SourcePlan
		}
		for _, p := range features[i].Privileges {
			v, ok := e.Values[p.Code]
			if !ok {
				continue
			}
			src := SourcePlan
			if _, ok := ownValues[e.FeatureID][p.Code]; ok {
				src = SourceSubscription
			}
			out[i].Values = append(out[i].Values, Value{Privilege: p, Value: v, Source: src})
		}
	}
	return out, nil
}

func deref[T any](in []*T) []T {
	out := make([]T, len(in))
	for i, x := range in {
		out[i] = *x
	}
	return out
}

// validateValue checks a value against its privilege, as lago's
// CreateOrUpdateConcern#validate_value does, and returns it unchanged: a
// missing one is privilege.value value_is_mandatory (EntitlementValue
// presence), an invalid one "<code>_privilege_value" value_is_invalid or
// value_not_in_select_options.
func validateValue(p catalog.Privilege, v string) (string, error) {
	if v == "" {
		return "", fieldError("privilege.value", core.CodeRequired)
	}
	field := p.Code + "_privilege_value"
	ok := false
	switch p.ValueType {
	case catalog.PrivilegeValueTypeSelect:
		for _, o := range p.SelectOptions {
			ok = ok || o == v
		}
		if !ok {
			return "", fieldError(field, catalog.CodePrivilegeValueNotInSelectOptions)
		}
	case catalog.PrivilegeValueTypeBoolean:
		ok = v == "true" || v == "false"
	case catalog.PrivilegeValueTypeInteger:
		n, err := strconv.ParseInt(v, 10, 64)
		ok = err == nil && strconv.FormatInt(n, 10) == v
	case catalog.PrivilegeValueTypeString:
		ok = true
	}
	if !ok {
		return "", fieldError(field, catalog.CodePrivilegeValueInvalid)
	}
	return v, nil
}

// sameValue reports whether a and b are the same value of type t (lago
// Utils::Entitlement.same_value?).
func sameValue(t catalog.PrivilegeValueType, a, b string) bool {
	switch t {
	case catalog.PrivilegeValueTypeInteger:
		x, errA := strconv.ParseInt(a, 10, 64)
		y, errB := strconv.ParseInt(b, 10, 64)
		if errA == nil && errB == nil {
			return x == y
		}
	case catalog.PrivilegeValueTypeBoolean:
		x, errA := strconv.ParseBool(a)
		y, errB := strconv.ParseBool(b)
		if errA == nil && errB == nil {
			return x == y
		}
	}
	return a == b
}
