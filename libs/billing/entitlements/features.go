package entitlements

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// CodeDuplicated is the field error code of a privilege code given twice
// (field: privilege.code).
const CodeDuplicated = "value_is_duplicated"

// Service manages features and entitlements. It uses d.Tx, d.Clock, d.IDs
// and d.Webhooks only.
type Service struct {
	d *kernel.Deps
}

// New returns the service over d. It fails when a dependency the service
// uses is nil.
func New(d *kernel.Deps) (*Service, error) {
	switch {
	case d == nil, d.Tx == nil:
		return nil, fmt.Errorf("%w: Tx", kernel.ErrMissingDependency)
	case d.Clock == nil:
		return nil, fmt.Errorf("%w: Clock", kernel.ErrMissingDependency)
	case d.IDs == nil:
		return nil, fmt.Errorf("%w: IDs", kernel.ErrMissingDependency)
	case d.Webhooks == nil:
		return nil, fmt.Errorf("%w: Webhooks", kernel.ErrMissingDependency)
	}
	return &Service{d: d}, nil
}

// PrivilegeInput is a privilege of a feature to create or update. Nil
// fields are not given.
type PrivilegeInput struct {
	// Code identifies the privilege within the feature.
	Code string
	// Name is the display name.
	Name *string
	// ValueType is the type of the values; "" is string on creation and
	// is ignored on update.
	ValueType catalog.PrivilegeValueType
	// SelectOptions are the options of a select privilege; on update they
	// are added to the existing ones.
	SelectOptions []string
}

// CreateFeatureInput is a feature to create.
type CreateFeatureInput struct {
	// Code is the unique code; surrounding blanks are stripped.
	Code string
	// Name is the display name.
	Name *string
	// Description is free text.
	Description *string
	// Privileges are the feature's privileges.
	Privileges []PrivilegeInput
}

// CreateFeature creates a feature with its privileges and emits
// feature.created. A privilege code given twice is privilege.code
// value_is_duplicated, a code used by another feature code
// value_already_exist, and an invalid privilege reports its errors under
// "privilege.".
//
// source: app/services/entitlement/feature_create_service.rb:18-55
func (s *Service) CreateFeature(ctx context.Context, org core.OrganizationID, in CreateFeatureInput) (*catalog.Feature, error) {
	if duplicatedPrivilege(in.Privileges) {
		return nil, fieldError("privilege.code", CodeDuplicated)
	}
	now := s.d.Now()
	f := &catalog.Feature{
		ID:             core.NewID[core.FeatureID](s.d.IDs),
		OrganizationID: org,
		Code:           strings.TrimSpace(in.Code),
		Name:           in.Name,
		Description:    in.Description,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	for _, p := range in.Privileges {
		f.Privileges = append(f.Privileges, s.newPrivilege(p))
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := validateFeature(f); err != nil {
			return err
		}
		switch _, err := r.Features.GetByCode(ctx, org, f.Code); {
		case err == nil:
			return fieldError("code", core.CodeAlreadyExists)
		case !errors.Is(err, core.ErrNotFound):
			return err
		}
		if err := r.Features.Create(ctx, f); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return fieldError("code", core.CodeAlreadyExists)
			}
			return err
		}
		if err := s.logFeature(ctx, r, auditlog.ActivityFeatureCreated, nil, f); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeFeatureCreated, "feature", f.ID.UUID(), f)
	})
	if err != nil {
		return nil, fmt.Errorf("create feature: %w", err)
	}
	return f, nil
}

// UpdateFeatureInput is a partial update of a feature: only the set
// fields change.
type UpdateFeatureInput struct {
	// Name is the display name.
	Name core.Optional[*string]
	// Description is free text.
	Description core.Optional[*string]
	// Privileges are merged by code into the feature's: an existing
	// privilege takes the given name and adds the given select options
	// (its value type stays), a new code creates a privilege, and the
	// privileges not given are kept.
	Privileges []PrivilegeInput
}

// UpdateFeature changes the feature id and emits plan.updated for every
// plan granting it, then feature.updated, even when nothing changed.
//
// source: app/services/entitlement/feature_update_service.rb:20-112
// (partial: true, as Api::V1::FeaturesController#update)
func (s *Service) UpdateFeature(ctx context.Context, org core.OrganizationID, id core.FeatureID, in UpdateFeatureInput) (*catalog.Feature, error) {
	if duplicatedPrivilege(in.Privileges) {
		return nil, fieldError("privilege.code", CodeDuplicated)
	}
	var out *catalog.Feature
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := getFeature(ctx, r, org, id)
		if err != nil {
			return err
		}
		before, err := auditlog.ObjectOf(f)
		if err != nil {
			return err
		}
		in.Name.Apply(&f.Name)
		in.Description.Apply(&f.Description)
		for _, p := range in.Privileges {
			i := slices.IndexFunc(f.Privileges, func(x catalog.Privilege) bool { return x.Code == p.Code })
			if i < 0 {
				f.Privileges = append(f.Privileges, s.newPrivilege(p))
				continue
			}
			if p.Name != nil {
				f.Privileges[i].Name = p.Name
			}
			if p.SelectOptions != nil {
				f.Privileges[i].SelectOptions = union(f.Privileges[i].SelectOptions, p.SelectOptions)
			}
		}
		f.UpdatedAt = s.d.Now()
		if err := validateFeature(f); err != nil {
			return err
		}
		if err := r.Features.Update(ctx, f); err != nil {
			return err
		}
		if err := s.emitFeaturePlans(ctx, r, org, f.ID); err != nil {
			return err
		}
		out = f
		if err := s.logFeature(ctx, r, auditlog.ActivityFeatureUpdated, before, f); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeFeatureUpdated, "feature", f.ID.UUID(), f)
	})
	if err != nil {
		return nil, fmt.Errorf("update feature: %w", err)
	}
	return out, nil
}

// DeleteFeature discards the feature id with its privileges and its
// entitlements, and emits plan.updated for every plan that granted it,
// then feature.deleted. It returns the discarded feature.
//
// source: app/services/entitlement/feature_destroy_service.rb:18-43
func (s *Service) DeleteFeature(ctx context.Context, org core.OrganizationID, id core.FeatureID) (*catalog.Feature, error) {
	var out *catalog.Feature
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := getFeature(ctx, r, org, id)
		if err != nil {
			return err
		}
		plans, err := featurePlans(ctx, r, org, f.ID)
		if err != nil {
			return err
		}
		now := s.d.Now()
		if _, err := r.Entitlements.DiscardByFeature(ctx, org, f.ID, now); err != nil {
			return err
		}
		if err := r.Features.Discard(ctx, org, f.ID, now); err != nil {
			return err
		}
		f.DeletedAt = &now
		if err := s.emitPlans(ctx, r, org, plans); err != nil {
			return err
		}
		out = f
		if err := s.logFeature(ctx, r, auditlog.ActivityFeatureDeleted, nil, f); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeFeatureDeleted, "feature", f.ID.UUID(), f)
	})
	if err != nil {
		return nil, fmt.Errorf("delete feature: %w", err)
	}
	return out, nil
}

// DeleteFeaturePrivilege discards the privilege code of the feature id
// with its entitlement values, and emits plan.updated for every plan
// granting the feature, then feature.updated. It returns the feature. A
// missing privilege is core.NotFound("privilege").
//
// source: app/services/entitlement/privilege_destroy_service.rb:18-38
// (Api::V1::Features::PrivilegesController#destroy)
func (s *Service) DeleteFeaturePrivilege(ctx context.Context, org core.OrganizationID, id core.FeatureID, code string) (*catalog.Feature, error) {
	var out *catalog.Feature
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := getFeature(ctx, r, org, id)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(f.Privileges, func(p catalog.Privilege) bool { return p.Code == code })
		if i < 0 {
			return core.NotFound("privilege")
		}
		before, err := auditlog.ObjectOf(f)
		if err != nil {
			return err
		}
		f.Privileges = slices.Delete(f.Privileges, i, i+1)
		f.UpdatedAt = s.d.Now()
		if err := r.Features.Update(ctx, f); err != nil {
			return err
		}
		if err := s.emitFeaturePlans(ctx, r, org, f.ID); err != nil {
			return err
		}
		out = f
		if err := s.logFeature(ctx, r, auditlog.ActivityFeatureUpdated, before, f); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeFeatureUpdated, "feature", f.ID.UUID(), f)
	})
	if err != nil {
		return nil, fmt.Errorf("delete feature privilege: %w", err)
	}
	return out, nil
}

// GetFeature returns the feature id, or core.NotFound("feature").
func (s *Service) GetFeature(ctx context.Context, org core.OrganizationID, id core.FeatureID) (*catalog.Feature, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Feature, error) {
		return getFeature(ctx, r, org, id)
	})
}

// GetFeatureByCode returns the feature with that code, or
// core.NotFound("feature").
func (s *Service) GetFeatureByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.Feature, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Feature, error) {
		f, err := r.Features.GetByCode(ctx, org, code)
		return f, featureNotFound(err)
	})
}

// ListFeatures returns the features whose name, code or description
// contains search, newest first.
//
// source: app/queries/entitlement/features_query.rb:7-30
func (s *Service) ListFeatures(ctx context.Context, org core.OrganizationID, search string, page core.PageRequest) (core.Page[*catalog.Feature], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.Feature], error) {
		return r.Features.List(ctx, org, catalog.FeatureListFilter{Search: search, Order: core.OrderRecent}, page)
	})
}

// newPrivilege returns the privilege p creates; the value type defaults
// to string (lago entitlement_privileges.value_type default).
func (s *Service) newPrivilege(p PrivilegeInput) catalog.Privilege {
	t := p.ValueType
	if t == "" {
		t = catalog.PrivilegeValueTypeString
	}
	return catalog.Privilege{
		ID:            core.NewID[core.PrivilegeID](s.d.IDs),
		Code:          strings.TrimSpace(p.Code),
		Name:          p.Name,
		ValueType:     t,
		SelectOptions: p.SelectOptions,
	}
}

// emitFeaturePlans emits plan.updated for every plan granting feature.
func (s *Service) emitFeaturePlans(ctx context.Context, r port.Repos, org core.OrganizationID, feature core.FeatureID) error {
	plans, err := featurePlans(ctx, r, org, feature)
	if err != nil {
		return err
	}
	return s.emitPlans(ctx, r, org, plans)
}

// emitPlans emits plan.updated for every plan, with its activity log
// (without changes, as lago produces it after the change:
// feature_update_service.rb:34, feature_destroy_service.rb:31,
// privilege_destroy_service.rb:26; and with the entitlement services'
// activity_loggable, which change no plan attribute).
func (s *Service) emitPlans(ctx context.Context, r port.Repos, org core.OrganizationID, plans []*catalog.Plan) error {
	for _, p := range plans {
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypePlanUpdated, "plan", p.ID.UUID(), p); err != nil {
			return err
		}
		if err := s.d.LogChange(ctx, r, kernel.Change{
			Type: auditlog.ActivityPlanUpdated, OrganizationID: org, ResourceType: auditlog.ResourcePlan, ResourceID: p.ID.UUID(), Object: p,
		}); err != nil {
			return err
		}
	}
	return nil
}

// logFeature records the activity log t of f, whose attributes before an
// update are before (lago activity_loggable of feature_create_service.rb:13,
// feature_update_service.rb:14, feature_destroy_service.rb:12 and
// privilege_destroy_service.rb:12).
func (s *Service) logFeature(ctx context.Context, r port.Repos, t auditlog.ActivityType, before map[string]any, f *catalog.Feature) error {
	return s.d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: f.OrganizationID, ResourceType: auditlog.ResourceFeature, ResourceID: f.ID.UUID(),
		Object: f, Before: before,
	})
}

// featurePlans returns the plans granting feature (lago feature.plans).
func featurePlans(ctx context.Context, r port.Repos, org core.OrganizationID, feature core.FeatureID) ([]*catalog.Plan, error) {
	ents, err := r.Entitlements.ListByFeature(ctx, org, feature)
	if err != nil {
		return nil, err
	}
	var out []*catalog.Plan
	seen := map[core.PlanID]bool{}
	for _, e := range ents {
		if e.PlanID.IsZero() || seen[e.PlanID] {
			continue
		}
		seen[e.PlanID] = true
		p, err := r.Plans.Get(ctx, org, e.PlanID)
		if err != nil {
			return nil, fmt.Errorf("get plan %s: %w", e.PlanID, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// validateFeature checks the feature, then its privileges one by one: the
// first invalid privilege reports its errors under "privilege." (lago
// feature_create_service.rb:44-49).
func validateFeature(f *catalog.Feature) error {
	bare := *f
	bare.Privileges = nil
	if err := bare.Validate(); err != nil {
		return err
	}
	for _, p := range f.Privileges {
		if err := p.Validate(); err != nil {
			var v core.Validator
			v.Merge("privilege", err)
			return v.Err()
		}
	}
	return nil
}

func getFeature(ctx context.Context, r port.Repos, org core.OrganizationID, id core.FeatureID) (*catalog.Feature, error) {
	f, err := r.Features.Get(ctx, org, id)
	return f, featureNotFound(err)
}

func featureNotFound(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound("feature")
	}
	return err
}

// duplicatedPrivilege reports whether a privilege code is given twice
// (lago Utils::Entitlement.privilege_code_is_duplicated?).
func duplicatedPrivilege(ps []PrivilegeInput) bool {
	seen := map[string]bool{}
	for _, p := range ps {
		if seen[p.Code] {
			return true
		}
		seen[p.Code] = true
	}
	return false
}

// union returns a followed by the values of b missing from it (Ruby |=).
func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, v := range b {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func fieldError(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}

func read[T any](ctx context.Context, d *kernel.Deps, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = fn(ctx, r)
		return err
	})
	return out, err
}
