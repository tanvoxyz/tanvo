package customer

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Filter narrows [Repository.List].
type Filter struct {
	// BillingEntityIDs keeps customers of these billing entities; empty
	// keeps all.
	BillingEntityIDs []core.BillingEntityID
	// AccountType keeps customers of that account type; "" keeps all.
	AccountType AccountType
	// CustomerType keeps customers of that type; "" keeps all.
	CustomerType Type
	// Search keeps customers whose external ID, name, legal name, first
	// name, last name or email contains the text (case-insensitive).
	Search string
	// IncludeDeleted also lists archived customers.
	IncludeDeleted bool
}

// Repository stores customers. Every method is scoped to an organization.
type Repository interface {
	// Create inserts c; an external ID used by another non-deleted
	// customer of the organization fails with core.ErrConflict.
	Create(ctx context.Context, c *Customer) error
	// Update replaces the stored customer c.ID with c (same uniqueness
	// rule as Create).
	Update(ctx context.Context, c *Customer) error
	// Get returns the customer id of org, archived or not, or
	// core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.CustomerID) (*Customer, error)
	// GetByExternalID returns the non-deleted customer with that external
	// ID, or core.ErrNotFound.
	GetByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (*Customer, error)
	// List returns the organization's customers in creation order.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Customer], error)
	// ListForDunning returns the non-deleted customers that dunning may
	// consider (not excluded), in creation order: those with
	// AppliedDunningCampaignID = campaign, or, when campaign is zero, those
	// without a campaign of their own (falling back to their billing
	// entity's).
	ListForDunning(ctx context.Context, org core.OrganizationID, campaign core.DunningCampaignID, page core.PageRequest) (core.Page[*Customer], error)
	// ResetDunningCampaign clears the applied campaign and the attempt
	// counters of every customer that uses campaign (domain §16: deleting a
	// campaign resets its customers), returning how many changed.
	ResetDunningCampaign(ctx context.Context, org core.OrganizationID, campaign core.DunningCampaignID) (int, error)
}
