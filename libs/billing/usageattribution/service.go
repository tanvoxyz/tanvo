package usageattribution

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Resources of the not-found errors.
const (
	// Resource is a usage attribution type.
	Resource = "usage_attribution_type"
	// ParentResource is the parent named by a create or an update.
	ParentResource = "parent_usage_attribution_type"
)

// Service manages usage attribution types. It uses d.Tx, d.Clock and d.IDs
// only: lago emits no webhook for them, and its account_tree feature flag
// is not a Tanvo gate (architecture §14).
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
	}
	return &Service{d: d}, nil
}

// Entry is a type with its parent, deleted or not (nil for a root), whose
// code the API shows (lago V1::UsageAttributionTypeSerializer parent_code).
type Entry struct {
	// Type is the type.
	Type *attribution.Type
	// Parent is the parent type; nil for a root.
	Parent *attribution.Type
}

// CreateInput is a type to create.
type CreateInput struct {
	// Code is unique among the organization's kept types; it is trimmed.
	Code string
	// Name is the display name.
	Name *string
	// Description is free text.
	Description *string
	// AttributionKeys are the event attribution keys of the type; they are
	// trimmed, and blanks and duplicates dropped.
	AttributionKeys []string
	// Role is how the values relate.
	Role attribution.Role
	// ParentID is the parent type; zero for a root.
	ParentID core.UsageAttributionTypeID
}

// Create creates a type. A parent that is not a kept type of the
// organization is core.NotFound(ParentResource).
//
// source: app/services/usage_attribution_types/create_service.rb:14-36
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*Entry, error) {
	now := s.d.Now()
	t := &attribution.Type{
		ID:              core.NewID[core.UsageAttributionTypeID](s.d.IDs),
		OrganizationID:  org,
		Code:            strings.TrimSpace(in.Code),
		Name:            in.Name,
		Description:     in.Description,
		AttributionKeys: attribution.NormalizeKeys(in.AttributionKeys),
		Role:            in.Role,
		ParentID:        in.ParentID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	var out *Entry
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var parent *attribution.Type
		if !t.ParentID.IsZero() {
			p, err := r.UsageAttributionTypes.Get(ctx, org, t.ParentID)
			if err != nil {
				return notFound(err, ParentResource)
			}
			parent = p
		}
		if err := validate(ctx, r, t, parent); err != nil {
			return err
		}
		if err := r.UsageAttributionTypes.Create(ctx, t); err != nil {
			return codeConflict(err)
		}
		out = &Entry{Type: t, Parent: parent}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create usage attribution type: %w", err)
	}
	return out, nil
}

// UpdateInput is a partial update of a type: only the set fields change.
type UpdateInput struct {
	// Code is trimmed.
	Code core.Optional[string]
	// Name is the display name.
	Name core.Optional[*string]
	// Description is free text.
	Description core.Optional[*string]
	// AttributionKeys are normalized as on create.
	AttributionKeys core.Optional[[]string]
	// Role is how the values relate.
	Role core.Optional[attribution.Role]
	// ParentID is the parent type; zero detaches the type.
	ParentID core.Optional[core.UsageAttributionTypeID]
}

// Update changes the type id. Once a value of the type was recorded,
// deleted ones included, its code, role and parent are frozen: changing
// any is code usage_already_attributed on each. A new parent that is not a
// kept type is core.NotFound(ParentResource); a type with kept children
// cannot turn flat (cannot_be_flat_with_children).
//
// source: app/services/usage_attribution_types/update_service.rb:13-46
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.UsageAttributionTypeID, in UpdateInput) (*Entry, error) {
	var out *Entry
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		old, err := r.UsageAttributionTypes.Get(ctx, org, id)
		if err != nil {
			return notFound(err, Resource)
		}
		t := *old
		in.Name.Apply(&t.Name)
		in.Description.Apply(&t.Description)
		if in.Code.Set {
			t.Code = strings.TrimSpace(in.Code.Value)
		}
		if in.AttributionKeys.Set {
			t.AttributionKeys = attribution.NormalizeKeys(in.AttributionKeys.Value)
		}
		in.Role.Apply(&t.Role)
		in.ParentID.Apply(&t.ParentID)

		// changed - %i[name description attribution_keys], in assignment
		// order.
		var frozen []string
		if t.Code != old.Code {
			frozen = append(frozen, "code")
		}
		if t.Role != old.Role {
			frozen = append(frozen, "role")
		}
		if t.ParentID != old.ParentID {
			frozen = append(frozen, "parent_id")
		}
		if len(frozen) > 0 {
			attributed, err := r.UsageAttributionValues.ExistsForType(ctx, org, id)
			if err != nil {
				return fmt.Errorf("check values: %w", err)
			}
			if attributed {
				var v core.Validator
				for _, f := range frozen {
					v.Add(f, attribution.CodeUsageAlreadyAttributed)
				}
				return v.Err()
			}
		}

		var parent *attribution.Type
		if t.ParentID != old.ParentID && !t.ParentID.IsZero() {
			p, err := r.UsageAttributionTypes.Get(ctx, org, t.ParentID)
			if err != nil {
				return notFound(err, ParentResource)
			}
			parent = p
		} else if !t.ParentID.IsZero() {
			// The current parent, which may have been deleted since
			// (belongs_to :parent, -> { with_discarded }).
			if parent, err = getAny(ctx, r, org, t.ParentID); err != nil {
				return err
			}
		}

		if t.Role == attribution.RoleFlat {
			has, err := r.UsageAttributionTypes.HasChildren(ctx, org, id)
			if err != nil {
				return fmt.Errorf("check children: %w", err)
			}
			if has {
				var v core.Validator
				v.Add("role", attribution.CodeCannotBeFlatWithChildren)
				return v.Err()
			}
		}

		if err := validate(ctx, r, &t, parent); err != nil {
			return err
		}
		t.UpdatedAt = s.d.Now()
		if err := r.UsageAttributionTypes.Update(ctx, &t); err != nil {
			return codeConflict(err)
		}
		out = &Entry{Type: &t, Parent: parent}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update usage attribution type %s: %w", id, err)
	}
	return out, nil
}

// Delete deletes the type id and its values; its children stay, naming
// the deleted parent. It returns the deleted type.
//
// source: app/services/usage_attribution_types/destroy_service.rb:12-23
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.UsageAttributionTypeID) (*Entry, error) {
	var out *Entry
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := r.UsageAttributionTypes.Get(ctx, org, id)
		if err != nil {
			return notFound(err, Resource)
		}
		now := s.d.Now()
		if _, err := r.UsageAttributionValues.DeleteForType(ctx, org, id, now); err != nil {
			return fmt.Errorf("delete values: %w", err)
		}
		if err := r.UsageAttributionTypes.Delete(ctx, org, id, now); err != nil {
			return notFound(err, Resource)
		}
		t.DeletedAt = &now
		e, err := entries(ctx, r, org, []*attribution.Type{t})
		if err != nil {
			return err
		}
		out = e[0]
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("delete usage attribution type %s: %w", id, err)
	}
	return out, nil
}

// Get returns the kept type id.
//
// source: app/controllers/api/v1/usage_attribution_types_controller.rb:57-62
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.UsageAttributionTypeID) (*Entry, error) {
	return s.one(ctx, org, func(ctx context.Context, r port.Repos) (*attribution.Type, error) {
		return r.UsageAttributionTypes.Get(ctx, org, id)
	})
}

// GetByCode returns the kept type with code.
//
// source: app/controllers/api/v1/usage_attribution_types_controller.rb:89-91
func (s *Service) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Entry, error) {
	return s.one(ctx, org, func(ctx context.Context, r port.Repos) (*attribution.Type, error) {
		return r.UsageAttributionTypes.GetByCode(ctx, org, code)
	})
}

func (s *Service) one(ctx context.Context, org core.OrganizationID, get func(context.Context, port.Repos) (*attribution.Type, error)) (*Entry, error) {
	var out *Entry
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := get(ctx, r)
		if err != nil {
			return notFound(err, Resource)
		}
		e, err := entries(ctx, r, org, []*attribution.Type{t})
		if err != nil {
			return err
		}
		out = e[0]
		return nil
	})
	return out, err
}

// List returns a page of the organization's kept types matching f, newest
// first, then by ID.
//
// source: app/queries/usage_attribution_types_query.rb:4-51
func (s *Service) List(ctx context.Context, org core.OrganizationID, f attribution.TypeFilter, page core.PageRequest) (core.Page[*Entry], error) {
	var out core.Page[*Entry]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if f.Role != "" && !f.Role.Valid() {
			var v core.Validator
			v.Add("role", core.CodeInvalid)
			return v.Err()
		}
		p, err := r.UsageAttributionTypes.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list usage attribution types: %w", err)
		}
		items, err := entries(ctx, r, org, p.Items)
		if err != nil {
			return err
		}
		out = core.Page[*Entry]{Items: items, NextToken: p.NextToken}
		return nil
	})
	return out, err
}

// entries returns ts with their parents.
func entries(ctx context.Context, r port.Repos, org core.OrganizationID, ts []*attribution.Type) ([]*Entry, error) {
	var parentIDs []core.UsageAttributionTypeID
	for _, t := range ts {
		if !t.ParentID.IsZero() && !slices.Contains(parentIDs, t.ParentID) {
			parentIDs = append(parentIDs, t.ParentID)
		}
	}
	parents, err := r.UsageAttributionTypes.GetMany(ctx, org, parentIDs)
	if err != nil {
		return nil, fmt.Errorf("load parent types: %w", err)
	}
	out := make([]*Entry, len(ts))
	for i, t := range ts {
		out[i] = &Entry{Type: t, Parent: parents[t.ParentID]}
	}
	return out, nil
}

// getAny returns the type id, deleted or not.
func getAny(ctx context.Context, r port.Repos, org core.OrganizationID, id core.UsageAttributionTypeID) (*attribution.Type, error) {
	m, err := r.UsageAttributionTypes.GetMany(ctx, org, []core.UsageAttributionTypeID{id})
	if err != nil {
		return nil, fmt.Errorf("load type %s: %w", id, err)
	}
	t, ok := m[id]
	if !ok {
		return nil, core.NotFound(ParentResource)
	}
	return t, nil
}

// validate adds to the type's own checks those against the other types:
// the code and the keys taken by a kept type, and a parent that is
// hierarchical and not a descendant of t.
//
// source: app/models/usage_attribution_type.rb:22-77
func validate(ctx context.Context, r port.Repos, t, parent *attribution.Type) error {
	var v core.Validator
	v.Merge("", t.Validate())
	if t.Code != "" {
		other, err := r.UsageAttributionTypes.GetByCode(ctx, t.OrganizationID, t.Code)
		switch {
		case err == nil:
			v.Check(other.ID == t.ID, "code", core.CodeAlreadyExists)
		case !errors.Is(err, core.ErrNotFound):
			return fmt.Errorf("check code: %w", err)
		}
	}
	if len(t.AttributionKeys) > 0 {
		taken, err := r.UsageAttributionTypes.KeysTaken(ctx, t.OrganizationID, t.AttributionKeys, t.ID)
		if err != nil {
			return fmt.Errorf("check attribution keys: %w", err)
		}
		v.Check(!taken, "attribution_keys", core.CodeAlreadyExists)
	}
	// A flat type with a parent is forbidden_for_flat_role (t.Validate),
	// and the other parent rules are then not checked.
	if parent != nil && t.Role != attribution.RoleFlat {
		v.Check(parent.Role == attribution.RoleHierarchical, "parent_id", attribution.CodeMustBeHierarchical)
		cycle, err := formsCycle(ctx, r, t, parent)
		if err != nil {
			return err
		}
		v.Check(!cycle, "parent_id", attribution.CodeCannotFormACycle)
	}
	return v.Err()
}

// formsCycle reports whether t is parent or one of its ancestors, deleted
// ones included.
//
// source: app/models/usage_attribution_type.rb:67-77
func formsCycle(ctx context.Context, r port.Repos, t, parent *attribution.Type) (bool, error) {
	visited := map[core.UsageAttributionTypeID]bool{}
	node := parent
	for node != nil && !visited[node.ID] {
		if node.ID == t.ID {
			return true, nil
		}
		visited[node.ID] = true
		if node.ParentID.IsZero() {
			return false, nil
		}
		m, err := r.UsageAttributionTypes.GetMany(ctx, t.OrganizationID, []core.UsageAttributionTypeID{node.ParentID})
		if err != nil {
			return false, fmt.Errorf("load ancestor %s: %w", node.ParentID, err)
		}
		node = m[node.ParentID]
	}
	return false, nil
}

// notFound turns a missing type into core.NotFound(resource).
func notFound(err error, resource string) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(resource)
	}
	return err
}

// codeConflict turns a uniqueness violation, which a concurrent write can
// still cause after validate, into code value_already_exist.
func codeConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "code", Code: core.CodeAlreadyExists}}}
	}
	return err
}
