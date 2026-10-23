package organization

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Repository stores organizations. Organizations are the tenants, so this
// is the only repository whose methods are not scoped by an organization.
type Repository interface {
	// Create inserts o; a slug already in use fails with core.ErrConflict.
	Create(ctx context.Context, o *Organization) error
	// Update replaces the stored organization o.ID with o.
	Update(ctx context.Context, o *Organization) error
	// Get returns the organization id, or core.ErrNotFound.
	Get(ctx context.Context, id core.OrganizationID) (*Organization, error)
	// GetBySlug returns the organization with that slug, or
	// core.ErrNotFound.
	GetBySlug(ctx context.Context, slug string) (*Organization, error)
	// WithWork returns, in ascending order, the organizations with due
	// work w at the instant at ([Work]); an undeclared w or a zero at
	// fails with core.ErrInvalidValue ([CheckWork]).
	WithWork(ctx context.Context, w Work, at time.Time) ([]core.OrganizationID, error)
}

// BillingEntityFilter narrows [BillingEntityRepository.List].
type BillingEntityFilter struct {
	// IncludeArchived also lists archived entities.
	IncludeArchived bool
}

// BillingEntityRepository stores billing entities. Every method is scoped
// to an organization.
type BillingEntityRepository interface {
	// Create inserts b; a code used by another non-archived entity, or a
	// second default entity, fails with core.ErrConflict.
	Create(ctx context.Context, b *BillingEntity) error
	// Update replaces the stored entity b.ID with b (same uniqueness rules
	// as Create).
	Update(ctx context.Context, b *BillingEntity) error
	// Get returns the entity id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.BillingEntityID) (*BillingEntity, error)
	// GetByCode returns the non-archived entity with that code.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*BillingEntity, error)
	// GetDefault returns the organization's default entity, or
	// core.ErrNotFound.
	GetDefault(ctx context.Context, org core.OrganizationID) (*BillingEntity, error)
	// List returns the organization's entities in creation order.
	List(ctx context.Context, org core.OrganizationID, f BillingEntityFilter, page core.PageRequest) (core.Page[*BillingEntity], error)
}
