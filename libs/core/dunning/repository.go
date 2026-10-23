package dunning

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Filter narrows [Repository.List].
type Filter struct {
	// Currencies keeps the campaigns with a threshold in one of these
	// currency codes; empty keeps all.
	Currencies []string
}

// Repository stores dunning campaigns with their thresholds.
type Repository interface {
	// Create stores a new campaign; core.ErrConflict when its code is used
	// by a live campaign of the organization.
	Create(ctx context.Context, c *Campaign) error
	// Update replaces a campaign and its thresholds (soft deletion included);
	// core.ErrNotFound when missing, core.ErrConflict on a code clash.
	Update(ctx context.Context, c *Campaign) error
	// Get returns a campaign, deleted or not; core.ErrNotFound when missing.
	Get(ctx context.Context, org core.OrganizationID, id core.DunningCampaignID) (*Campaign, error)
	// GetByCode returns the live campaign with code; core.ErrNotFound when
	// none.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Campaign, error)
	// List returns the live campaigns matching f in creation order.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Campaign], error)
}
