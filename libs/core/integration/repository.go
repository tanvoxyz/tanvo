package integration

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Filter narrows [Repository.List].
type Filter struct {
	// Category keeps the integrations of one category; "" keeps all.
	Category Category
	// Provider keeps the integrations of one provider; "" keeps all.
	Provider Provider
}

// Repository stores integrations. Every method is scoped to an
// organization.
type Repository interface {
	// Create inserts i; a code already used in the organization by a live
	// payment integration (for a payment integration) or by another
	// integration (for the others), or a second default of the category,
	// fails with core.ErrConflict.
	Create(ctx context.Context, i *Integration) error
	// Update replaces the stored integration i.ID with i (same uniqueness
	// rules as Create).
	Update(ctx context.Context, i *Integration) error
	// Get returns the integration id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.IntegrationID) (*Integration, error)
	// GetByCode returns the integration with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Integration, error)
	// GetDefault returns the default integration of the category, or
	// core.ErrNotFound.
	GetDefault(ctx context.Context, org core.OrganizationID, c Category) (*Integration, error)
	// ListByProvider returns the integrations of a provider in creation
	// order.
	ListByProvider(ctx context.Context, org core.OrganizationID, p Provider) ([]*Integration, error)
	// ListByDomain returns the integrations of the auth provider p, in
	// every organization, whose settings claim the email domain
	// case-insensitively (AuthSettings.Domains), in creation order. It
	// routes single sign-on by email domain.
	ListByDomain(ctx context.Context, p Provider, domain string) ([]*Integration, error)
	// List returns the organization's integrations in creation order.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Integration], error)
	// Discard marks the payment integration id deleted at at, with its
	// live payment provider customers, or fails with core.ErrNotFound;
	// every read leaves discarded integrations out and their code can be
	// reused.
	Discard(ctx context.Context, org core.OrganizationID, id core.IntegrationID, at time.Time) error
	// Delete removes the integration id with its customers, mappings,
	// items and resources, or fails with core.ErrNotFound; one still
	// referenced by payment provider customers, payment methods, payments,
	// refunds or inbound webhooks fails with core.ErrConflict.
	Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationID) error
}

// CustomerRepository stores integration customers.
type CustomerRepository interface {
	// Create inserts c; a second tax integration customer of the
	// customer, a second default of the category, or an external id
	// already linked in the integration fails with core.ErrConflict.
	Create(ctx context.Context, c *IntegrationCustomer) error
	// Update replaces the stored link c.ID with c (same uniqueness rules
	// as Create).
	Update(ctx context.Context, c *IntegrationCustomer) error
	// Get returns the link id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.IntegrationCustomerID) (*IntegrationCustomer, error)
	// ListByCustomer returns the links of a customer, of one category
	// unless c is "", in creation order.
	ListByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID, c Category) ([]*IntegrationCustomer, error)
	// GetByExternalID returns the link of an external customer id in an
	// integration, or core.ErrNotFound.
	GetByExternalID(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, externalID string) (*IntegrationCustomer, error)
	// Delete removes the link id, or fails with core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationCustomerID) error
}

// MappingRepository stores item mappings.
type MappingRepository interface {
	// Create inserts m; a mapping with the same [Mapping.SameKey] fails
	// with core.ErrConflict.
	Create(ctx context.Context, m *Mapping) error
	// Update replaces the stored mapping m.ID with m (same uniqueness rule
	// as Create).
	Update(ctx context.Context, m *Mapping) error
	// Get returns the mapping id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.IntegrationMappingID) (*Mapping, error)
	// Find returns the mapping of (integration, mappable, billing entity)
	// exactly, or core.ErrNotFound; a zero billing entity finds the
	// mapping without billing entity.
	Find(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, t MappableType, mappableID uuid.UUID, be core.BillingEntityID) (*Mapping, error)
	// ListByIntegration returns the mappings of an integration in creation
	// order, for [Resolve].
	ListByIntegration(ctx context.Context, org core.OrganizationID, integration core.IntegrationID) ([]*Mapping, error)
	// Delete removes the mapping id, or fails with core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationMappingID) error
}

// CollectionMappingRepository stores collection mappings.
type CollectionMappingRepository interface {
	// Create inserts m; a mapping with the same [CollectionMapping.SameKey]
	// fails with core.ErrConflict.
	Create(ctx context.Context, m *CollectionMapping) error
	// Update replaces the stored mapping m.ID with m (same uniqueness rule
	// as Create).
	Update(ctx context.Context, m *CollectionMapping) error
	// Get returns the mapping id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.IntegrationCollectionMappingID) (*CollectionMapping, error)
	// ListByType returns the mappings of an integration with type t (all
	// types when t is ""), in creation order.
	ListByType(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, t CollectionMappingType) ([]*CollectionMapping, error)
	// Delete removes the mapping id, or fails with core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationCollectionMappingID) error
}

// ItemFilter narrows [ItemRepository.List].
type ItemFilter struct {
	// ItemType keeps one item type; "" keeps all.
	ItemType ItemType
	// Search keeps items whose external id, name or account code contains
	// it (case-insensitive; lago IntegrationItemsQuery); "" keeps all.
	Search string
}

// ItemRepository stores the cached external catalog.
type ItemRepository interface {
	// Upsert inserts i or replaces the item with the same (integration,
	// item type, external id), which keeps its ID and creation instant; a
	// new item with the ID of another item fails with core.ErrConflict.
	Upsert(ctx context.Context, i *Item) error
	// List returns the items of an integration by external name (byte
	// order, unnamed last), then newest first, then by ID (lago
	// IntegrationItemsQuery).
	List(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, f ItemFilter, page core.PageRequest) (core.Page[*Item], error)
	// DeleteByType removes the items of one type of an integration (before
	// a catalog refresh) and returns how many.
	DeleteByType(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, t ItemType) (int, error)
}

// ResourceRepository stores the records of synced objects.
type ResourceRepository interface {
	// Create inserts r; a record with the same (integration, resource
	// type, syncable id) fails with core.ErrConflict.
	Create(ctx context.Context, r *Resource) error
	// Find returns the record of a local object, or core.ErrNotFound.
	Find(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, t ResourceType, syncableID uuid.UUID) (*Resource, error)
	// Delete removes the record id, or fails with core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationResourceID) error
	// ExistsForSyncable reports whether the local object syncableID is
	// synced to any integration.
	ExistsForSyncable(ctx context.Context, org core.OrganizationID, syncableID uuid.UUID) (bool, error)
}
