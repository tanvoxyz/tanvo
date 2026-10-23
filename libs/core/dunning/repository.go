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
	// Search keeps the campaigns whose name or code contains the trimmed
	// term, case-insensitively.
	Search string
	// Applied, when set, keeps the campaign AppliedID only (true) or every
	// other campaign (false); a zero AppliedID is no campaign.
	Applied *bool
	// AppliedID is the campaign Applied compares with.
	AppliedID core.DunningCampaignID
	// Order is the list order; core.OrderRecent lists by name (byte
	// order), then newest first, then by ID.
	Order core.ListOrder
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
	// List returns the live campaigns matching f in the order f.Order; an
	// invalid order fails with core.ErrInvalidValue.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Campaign], error)
}
