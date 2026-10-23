package catalog

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Field error codes of features and entitlement values (lago's names).
const (
	// CodePrivilegeValueInvalid: the value does not fit the privilege's
	// value type.
	CodePrivilegeValueInvalid = "value_is_invalid"
	// CodePrivilegeValueNotInSelectOptions: a select value is not one of
	// the privilege's options.
	CodePrivilegeValueNotInSelectOptions = "value_not_in_select_options"
	// CodePrivilegeNotFound: an entitlement value names no privilege of
	// the feature.
	CodePrivilegeNotFound = "privilege_not_found"
)

// Feature is a capability granted to plans and subscriptions through
// entitlements (domain §7.10). It owns its privileges.
type Feature struct {
	// ID identifies the feature.
	ID core.FeatureID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique in the organization, ≤ 255 characters.
	Code string
	// Name is the display name, ≤ 255 characters.
	Name *string
	// Description is an optional explanation, ≤ 600 characters.
	Description *string
	// Privileges are the feature's kept parameters, with codes unique
	// within the feature.
	Privileges []Privilege
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
	// DeletedAt is the instant the feature was discarded; repositories
	// only return kept (nil) ones (lago app/models/entitlement/feature.rb:5-9).
	DeletedAt *time.Time
}

// Privilege is a typed parameter of a feature, valued per entitlement
// (domain §7.10).
type Privilege struct {
	// ID identifies the privilege.
	ID core.PrivilegeID
	// Code is unique within the feature, ≤ 255 characters.
	Code string
	// Name is the display name, ≤ 255 characters.
	Name *string
	// ValueType is the type of the privilege's values.
	ValueType PrivilegeValueType
	// SelectOptions are the allowed values of a select privilege; required
	// and non-empty for select, empty otherwise.
	SelectOptions []string
	// DeletedAt is the instant the privilege was discarded; a feature read
	// from a repository holds kept (nil) privileges only (lago
	// app/models/entitlement/privilege.rb:5-9).
	DeletedAt *time.Time
}

// Validate checks the feature and its privileges (domain §7.10). Privilege
// errors are reported on "privileges[i].<field>".
func (f *Feature) Validate() error {
	var v core.Validator
	v.Code("code", f.Code, 255)
	v.OptionalString("name", f.Name, 255)
	v.OptionalString("description", f.Description, 600)
	seen := map[string]bool{}
	for i, p := range f.Privileges {
		field := fmt.Sprintf("privileges[%d]", i)
		v.Merge(field, p.Validate())
		if p.Code != "" {
			v.Check(!seen[p.Code], field+".code", core.CodeAlreadyExists)
			seen[p.Code] = true
		}
	}
	return v.Err()
}

// Privilege returns the privilege with that code.
func (f *Feature) Privilege(code string) (Privilege, bool) {
	i := slices.IndexFunc(f.Privileges, func(p Privilege) bool { return p.Code == code })
	if i < 0 {
		return Privilege{}, false
	}
	return f.Privileges[i], true
}

// Validate checks one privilege: code, name and the select options, which
// are required (non-empty, unique, non-blank) for select privileges and
// forbidden otherwise ("select_options": invalid_format, lago's config
// rule).
func (p *Privilege) Validate() error {
	var v core.Validator
	v.Code("code", p.Code, 255)
	v.OptionalString("name", p.Name, 255)
	if !v.Enum("value_type", p.ValueType) {
		return v.Err()
	}
	if p.ValueType == PrivilegeValueTypeSelect {
		ok := len(p.SelectOptions) > 0 && len(part2Unique(p.SelectOptions)) == len(p.SelectOptions) &&
			!slices.Contains(p.SelectOptions, "")
		v.Check(ok, "select_options", core.CodeInvalidFormat)
	} else {
		v.Check(len(p.SelectOptions) == 0, "select_options", core.CodeInvalidFormat)
	}
	return v.Err()
}

// NormalizeValue checks s against the privilege's value type and returns
// its stored form: booleans as "true" or "false", integers in canonical
// decimal form, select and string values unchanged. Errors are field
// errors on "value" with the codes of lago's entitlement services.
func (p *Privilege) NormalizeValue(s string) (string, error) {
	var v core.Validator
	if s == "" {
		v.Add("value", core.CodeRequired)
		return "", v.Err()
	}
	switch p.ValueType {
	case PrivilegeValueTypeString:
		return s, nil
	case PrivilegeValueTypeSelect:
		if slices.Contains(p.SelectOptions, s) {
			return s, nil
		}
		v.Add("value", CodePrivilegeValueNotInSelectOptions)
	case PrivilegeValueTypeBoolean:
		switch s {
		case "true", "t":
			return "true", nil
		case "false", "f":
			return "false", nil
		}
		v.Add("value", CodePrivilegeValueInvalid)
	case PrivilegeValueTypeInteger:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return strconv.FormatInt(n, 10), nil
		}
		v.Add("value", CodePrivilegeValueInvalid)
	default:
		v.Add("value", CodePrivilegeValueInvalid)
	}
	return "", v.Err()
}

// Entitlement grants a feature to a plan or to a subscription, with a
// value per privilege (domain §7.10). Exactly one of PlanID and
// SubscriptionID is set.
type Entitlement struct {
	// ID identifies the entitlement.
	ID core.EntitlementID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// FeatureID is the granted feature.
	FeatureID core.FeatureID
	// PlanID is the plan granted the feature; zero for a subscription's
	// entitlement.
	PlanID core.PlanID
	// SubscriptionID is the subscription granted the feature; zero for a
	// plan's entitlement.
	SubscriptionID core.SubscriptionID
	// Values maps privilege codes to their stored values (see
	// [Privilege.NormalizeValue]).
	Values map[string]string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
	// DeletedAt is the instant the entitlement was discarded; repositories
	// only return kept (nil) ones (lago entitlement.rb:9).
	DeletedAt *time.Time
}

// Validate checks the owner and the feature reference.
func (e *Entitlement) Validate() error {
	var v core.Validator
	v.Check(e.PlanID.IsZero() != e.SubscriptionID.IsZero(), "base", CodeOneOfPlanOrSubscriptionRequired)
	v.Check(!e.FeatureID.IsZero(), "feature_id", core.CodeRequired)
	return v.Err()
}

// NormalizeValues validates every value of e against the privileges of f
// (its feature) and rewrites them in stored form. Errors are reported on
// "values.<privilege code>".
func (e *Entitlement) NormalizeValues(f *Feature) error {
	var v core.Validator
	if e.FeatureID != f.ID {
		v.Add("feature_id", core.CodeInvalid)
		return v.Err()
	}
	out := make(map[string]string, len(e.Values))
	for _, code := range slices.Sorted(maps.Keys(e.Values)) {
		field := "values." + code
		p, ok := f.Privilege(code)
		if !ok {
			v.Add(field, CodePrivilegeNotFound)
			continue
		}
		s, err := p.NormalizeValue(e.Values[code])
		if err != nil {
			for _, fe := range core.FieldErrors(err) {
				v.Add(field, fe.Code)
			}
			continue
		}
		out[code] = s
	}
	if !v.Valid() {
		return v.Err()
	}
	e.Values = out
	return nil
}

// EntitlementRemoval removes, for one subscription, a feature or one
// privilege of a feature granted by its plan (domain §7.10,
// subscription_feature_removals).
type EntitlementRemoval struct {
	// ID identifies the removal.
	ID core.EntitlementRemovalID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the subscription the removal applies to.
	SubscriptionID core.SubscriptionID
	// FeatureID is the feature removed, or whose privilege is removed.
	FeatureID core.FeatureID
	// PrivilegeCode is the removed privilege; "" removes the whole
	// feature.
	PrivilegeCode string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// Validate checks the references of the removal.
func (r *EntitlementRemoval) Validate() error {
	var v core.Validator
	v.Check(!r.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	v.Check(!r.FeatureID.IsZero(), "feature_id", core.CodeRequired)
	v.MaxLen("privilege_code", r.PrivilegeCode, 255)
	return v.Err()
}

// EffectiveEntitlement is one feature granted to a subscription after
// overrides, see [EffectiveEntitlements].
type EffectiveEntitlement struct {
	// FeatureID is the granted feature.
	FeatureID core.FeatureID
	// Values maps privilege codes to values.
	Values map[string]string
	// PlanEntitlementID is the plan entitlement the grant comes from, if
	// any.
	PlanEntitlementID core.EntitlementID
	// SubscriptionEntitlementID is the subscription's own entitlement for
	// the feature, if any.
	SubscriptionEntitlementID core.EntitlementID
}

// EffectiveEntitlements returns the entitlements of a subscription
// (domain §7.10): the plan's entitlements, minus the subscription's
// removals (whole features or single privileges), plus the subscription's
// own entitlements, whose values override the plan's privilege by
// privilege. Plan features come first in their order, then the features
// granted only by the subscription.
func EffectiveEntitlements(plan, subscription []Entitlement, removals []EntitlementRemoval) []EffectiveEntitlement {
	removedFeature := map[core.FeatureID]bool{}
	removedPrivilege := map[core.FeatureID]map[string]bool{}
	for _, r := range removals {
		if r.PrivilegeCode == "" {
			removedFeature[r.FeatureID] = true
			continue
		}
		if removedPrivilege[r.FeatureID] == nil {
			removedPrivilege[r.FeatureID] = map[string]bool{}
		}
		removedPrivilege[r.FeatureID][r.PrivilegeCode] = true
	}
	var out []EffectiveEntitlement
	index := map[core.FeatureID]int{}
	for _, e := range plan {
		if removedFeature[e.FeatureID] {
			continue
		}
		values := map[string]string{}
		for code, val := range e.Values {
			if !removedPrivilege[e.FeatureID][code] {
				values[code] = val
			}
		}
		index[e.FeatureID] = len(out)
		out = append(out, EffectiveEntitlement{FeatureID: e.FeatureID, Values: values, PlanEntitlementID: e.ID})
	}
	for _, e := range subscription {
		i, ok := index[e.FeatureID]
		if !ok {
			index[e.FeatureID] = len(out)
			out = append(out, EffectiveEntitlement{FeatureID: e.FeatureID, Values: map[string]string{}})
			i = len(out) - 1
		}
		out[i].SubscriptionEntitlementID = e.ID
		maps.Copy(out[i].Values, e.Values)
	}
	return out
}
