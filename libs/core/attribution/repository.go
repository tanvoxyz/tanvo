package attribution

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// TypeFilter narrows [TypeRepository.List].
type TypeFilter struct {
	// Role keeps the types of one role; empty keeps all.
	Role Role
	// Root, when set, keeps the types without a kept parent (true) or
	// with one (false).
	Root *bool
	// Search keeps the types whose code or name contains the trimmed term
	// (case-insensitive, with LIKE wildcards matched literally).
	Search string
}

// TypeRepository stores attribution types. Every method is scoped to an
// organization; reads see kept types unless stated otherwise.
type TypeRepository interface {
	// Create inserts t; a code already used by a kept type of the
	// organization fails with core.ErrConflict.
	Create(ctx context.Context, t *Type) error
	// Update replaces the stored type t.ID with t (same rule as Create).
	Update(ctx context.Context, t *Type) error
	// Get returns the kept type id, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.UsageAttributionTypeID) (*Type, error)
	// GetByCode returns the kept type with that code, or core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Type, error)
	// GetMany returns the types ids, deleted ones included, by ID; a
	// missing ID is absent from the map.
	GetMany(ctx context.Context, org core.OrganizationID, ids []core.UsageAttributionTypeID) (map[core.UsageAttributionTypeID]*Type, error)
	// List returns the kept types matching f, newest first, then by ID.
	List(ctx context.Context, org core.OrganizationID, f TypeFilter, page core.PageRequest) (core.Page[*Type], error)
	// KeysTaken reports whether a kept type other than except holds one
	// of keys.
	KeysTaken(ctx context.Context, org core.OrganizationID, keys []string, except core.UsageAttributionTypeID) (bool, error)
	// HasChildren reports whether a kept type has id as its parent.
	HasChildren(ctx context.Context, org core.OrganizationID, id core.UsageAttributionTypeID) (bool, error)
	// Delete marks the kept type id deleted at at, or fails with
	// core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.UsageAttributionTypeID, at time.Time) error
}

// ValueRepository stores attribution values. Every method is scoped to an
// organization.
type ValueRepository interface {
	// Create inserts v; a value already recorded, deleted or not, for the
	// customer and type fails with core.ErrConflict.
	Create(ctx context.Context, v *Value) error
	// Get returns the value id, deleted or not, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.UsageAttributionValueID) (*Value, error)
	// ExistsForType reports whether a value of the type was ever recorded,
	// deleted ones included.
	ExistsForType(ctx context.Context, org core.OrganizationID, typeID core.UsageAttributionTypeID) (bool, error)
	// DeleteForType marks the kept values of the type deleted at at and
	// returns how many.
	DeleteForType(ctx context.Context, org core.OrganizationID, typeID core.UsageAttributionTypeID, at time.Time) (int, error)
}
