package integration

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Mapping maps an add-on or billable metric to an external item, optionally
// per billing entity (domain §17.3). It is unique per (integration,
// mappable type, mappable id, billing entity).
type Mapping struct {
	// ID identifies the mapping.
	ID core.IntegrationMappingID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// IntegrationID is the connection.
	IntegrationID core.IntegrationID
	// MappableType is the kind of the mapped object.
	MappableType MappableType
	// MappableID is the id of the add-on or billable metric.
	MappableID uuid.UUID
	// BillingEntityID scopes the mapping to one billing entity; zero
	// applies to all.
	BillingEntityID core.BillingEntityID
	// ExternalID is the external item id.
	ExternalID string
	// ExternalAccountCode is the external ledger account.
	ExternalAccountCode *string
	// ExternalName is the external item name.
	ExternalName *string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the entity-local invariants of domain §17.3.
func (m *Mapping) Validate() error {
	var v core.Validator
	v.Check(!m.IntegrationID.IsZero(), "integration_id", core.CodeRequired)
	v.Enum("mappable_type", m.MappableType)
	v.Check(m.MappableID != uuid.Nil, "mappable_id", core.CodeRequired)
	v.String("external_id", m.ExternalID, 255)
	v.OptionalString("external_account_code", m.ExternalAccountCode, 255)
	v.OptionalString("external_name", m.ExternalName, 255)
	return v.Err()
}

// SameKey reports whether m and o share the uniqueness key (integration,
// mappable type and id, billing entity).
func (m *Mapping) SameKey(o *Mapping) bool {
	return m.OrganizationID == o.OrganizationID && m.IntegrationID == o.IntegrationID &&
		m.MappableType == o.MappableType && m.MappableID == o.MappableID && m.BillingEntityID == o.BillingEntityID
}

// CollectionMapping is the mapping of one [CollectionMappingType] of an
// integration, optionally per billing entity (domain §17.3). It is unique
// per (integration, type, billing entity).
type CollectionMapping struct {
	// ID identifies the mapping.
	ID core.IntegrationCollectionMappingID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// IntegrationID is the connection.
	IntegrationID core.IntegrationID
	// Type is the collection mapped.
	Type CollectionMappingType
	// BillingEntityID scopes the mapping to one billing entity; zero
	// applies to all.
	BillingEntityID core.BillingEntityID
	// ExternalID is the external item id.
	ExternalID *string
	// ExternalAccountCode is the external ledger account.
	ExternalAccountCode *string
	// ExternalName is the external item name.
	ExternalName *string
	// TaxNexus is the NetSuite tax nexus.
	TaxNexus *string
	// TaxType is the NetSuite tax type.
	TaxType *string
	// TaxCode is the NetSuite tax code.
	TaxCode *string
	// Currencies maps ISO 4217 codes to external currency codes (type
	// currencies only).
	Currencies map[string]string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the entity-local invariants of domain §17.3.
func (m *CollectionMapping) Validate() error {
	var v core.Validator
	v.Check(!m.IntegrationID.IsZero(), "integration_id", core.CodeRequired)
	v.Enum("mapping_type", m.Type)
	if m.Type == CollectionMappingTypeCurrencies {
		v.Check(len(m.Currencies) > 0, "currencies", core.CodeRequired)
	} else {
		v.Check(len(m.Currencies) == 0, "currencies", core.CodeForbidden)
	}
	v.OptionalString("external_id", m.ExternalID, 255)
	v.OptionalString("external_account_code", m.ExternalAccountCode, 255)
	v.OptionalString("external_name", m.ExternalName, 255)
	v.OptionalString("tax_nexus", m.TaxNexus, 255)
	v.OptionalString("tax_type", m.TaxType, 255)
	v.OptionalString("tax_code", m.TaxCode, 255)
	return v.Err()
}

// SameKey reports whether m and o share the uniqueness key (integration,
// type, billing entity).
func (m *CollectionMapping) SameKey(o *CollectionMapping) bool {
	return m.OrganizationID == o.OrganizationID && m.IntegrationID == o.IntegrationID &&
		m.Type == o.Type && m.BillingEntityID == o.BillingEntityID
}

// ExternalItem is the result of mapping resolution: the external item a
// local object syncs as.
type ExternalItem struct {
	// ExternalID is the external item id.
	ExternalID string
	// ExternalAccountCode is the external ledger account.
	ExternalAccountCode *string
	// ExternalName is the external item name.
	ExternalName *string
}

// ErrNoMapping is wrapped by [Resolve] when nothing maps an object; the
// sync then fails with integration.provider_error (domain §17.3).
var ErrNoMapping = fmt.Errorf("no integration mapping: %w", core.ErrNotFound)

// ResolveInput is what [Resolve] needs: the mappings and collection
// mappings of one integration, and the object to map.
type ResolveInput struct {
	// Mappings are the integration's item mappings.
	Mappings []*Mapping
	// CollectionMappings are the integration's collection mappings.
	CollectionMappings []*CollectionMapping
	// MappableType and MappableID identify an add-on or metric; leave
	// MappableID nil for objects that only have a collection (taxes,
	// coupons, …).
	MappableType MappableType
	// MappableID is the id of the add-on or billable metric.
	MappableID uuid.UUID
	// Collection is the collection mapping type of the object (for a fee:
	// subscription_fee, minimum_commitment, prepaid_credit…; for a charge
	// or add-on fee, leave empty to fall back to fallback_item only).
	Collection CollectionMappingType
	// BillingEntityID is the billing entity of the document.
	BillingEntityID core.BillingEntityID
}

// Resolve finds the external item of an object (domain §17.3): the
// object's own mapping → the collection mapping of its type →
// fallback_item. At each step a mapping of the document's billing entity
// wins over one without billing entity. It wraps [ErrNoMapping] when
// nothing maps.
func Resolve(in ResolveInput) (ExternalItem, error) {
	if in.MappableID != uuid.Nil {
		if m := pickMapping(in); m != nil {
			return ExternalItem{m.ExternalID, m.ExternalAccountCode, m.ExternalName}, nil
		}
	}
	types := []CollectionMappingType{}
	if in.Collection != "" {
		types = append(types, in.Collection)
	}
	if in.Collection != CollectionMappingTypeFallbackItem {
		types = append(types, CollectionMappingTypeFallbackItem)
	}
	for _, t := range types {
		if c := pickCollection(in, t); c != nil && c.ExternalID != nil {
			return ExternalItem{*c.ExternalID, c.ExternalAccountCode, c.ExternalName}, nil
		}
	}
	return ExternalItem{}, fmt.Errorf("%s %s: %w", in.MappableType, in.MappableID, ErrNoMapping)
}

func pickMapping(in ResolveInput) *Mapping {
	var general *Mapping
	for _, m := range in.Mappings {
		if m.MappableType != in.MappableType || m.MappableID != in.MappableID {
			continue
		}
		switch {
		case !in.BillingEntityID.IsZero() && m.BillingEntityID == in.BillingEntityID:
			return m
		case m.BillingEntityID.IsZero():
			general = m
		}
	}
	return general
}

func pickCollection(in ResolveInput, t CollectionMappingType) *CollectionMapping {
	var general *CollectionMapping
	for _, c := range in.CollectionMappings {
		if c.Type != t {
			continue
		}
		switch {
		case !in.BillingEntityID.IsZero() && c.BillingEntityID == in.BillingEntityID:
			return c
		case c.BillingEntityID.IsZero():
			general = c
		}
	}
	return general
}

// Item caches one item of the external catalog (domain §17.3).
type Item struct {
	// ID identifies the cached item.
	ID core.IntegrationItemID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// IntegrationID is the connection.
	IntegrationID core.IntegrationID
	// ItemType is the kind of item.
	ItemType ItemType
	// ExternalID is the external id; unique per (integration, item type).
	ExternalID string
	// ExternalAccountCode is the external ledger account.
	ExternalAccountCode *string
	// ExternalName is the external display name.
	ExternalName *string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the entity-local invariants of domain §17.3.
func (i *Item) Validate() error {
	var v core.Validator
	v.Check(!i.IntegrationID.IsZero(), "integration_id", core.CodeRequired)
	v.Enum("item_type", i.ItemType)
	v.String("external_id", i.ExternalID, 255)
	return v.Err()
}

// Resource records that a local object was synced, which makes syncs
// idempotent (domain §17.3). It is unique per (integration, resource type,
// syncable id).
type Resource struct {
	// ID identifies the record.
	ID core.IntegrationResourceID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// IntegrationID is the connection.
	IntegrationID core.IntegrationID
	// ResourceType is the kind of local object.
	ResourceType ResourceType
	// SyncableID is the id of the local invoice, payment, credit note or
	// subscription.
	SyncableID uuid.UUID
	// ExternalID is the id of the object in the external system.
	ExternalID string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the entity-local invariants of domain §17.3.
func (r *Resource) Validate() error {
	var v core.Validator
	v.Check(!r.IntegrationID.IsZero(), "integration_id", core.CodeRequired)
	v.Enum("resource_type", r.ResourceType)
	v.Check(r.SyncableID != uuid.Nil, "syncable_id", core.CodeRequired)
	v.String("external_id", r.ExternalID, 255)
	return v.Err()
}
