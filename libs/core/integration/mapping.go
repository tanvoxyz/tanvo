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
	v.MaxLen("external_id", m.ExternalID, 255)
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
	// Collection is the collection mapping type of an object without its
	// own mapping (subscription_fee, minimum_commitment, coupon…); empty
	// with a MappableID.
	Collection CollectionMappingType
	// BillingEntityID is the billing entity of the document.
	BillingEntityID core.BillingEntityID
}

// Resolve finds the external item of an object (domain §17.3). For an
// add-on or metric: its mapping for the document's billing entity → the
// entity's fallback_item → its mapping without billing entity → the
// fallback_item without billing entity. For a collection: the same order
// with the collection mapping of the type in place of the object's
// mapping, and no fallback_item for tax (lago
// app/services/integrations/aggregator/base_payload.rb:71-106). It wraps
// [ErrNoMapping] when nothing maps.
func Resolve(in ResolveInput) (ExternalItem, error) {
	if in.MappableID != uuid.Nil {
		var scoped, general *Mapping
		for _, m := range in.Mappings {
			if m.MappableType != in.MappableType || m.MappableID != in.MappableID {
				continue
			}
			switch {
			case !in.BillingEntityID.IsZero() && m.BillingEntityID == in.BillingEntityID:
				scoped = cmpOr(scoped, m)
			case m.BillingEntityID.IsZero():
				general = cmpOr(general, m)
			}
		}
		if scoped != nil {
			return ExternalItem{scoped.ExternalID, scoped.ExternalAccountCode, scoped.ExternalName}, nil
		}
		if c := pickCollection(in.CollectionMappings, in.BillingEntityID, CollectionMappingTypeFallbackItem, true); c != nil {
			return c.Item(), nil
		}
		if general != nil {
			return ExternalItem{general.ExternalID, general.ExternalAccountCode, general.ExternalName}, nil
		}
		if c := pickCollection(in.CollectionMappings, core.BillingEntityID{}, CollectionMappingTypeFallbackItem, false); c != nil {
			return c.Item(), nil
		}
		return ExternalItem{}, fmt.Errorf("%s %s: %w", in.MappableType, in.MappableID, ErrNoMapping)
	}
	if c := LookupCollection(in.CollectionMappings, in.BillingEntityID, in.Collection); c != nil {
		return c.Item(), nil
	}
	return ExternalItem{}, fmt.Errorf("%s: %w", in.Collection, ErrNoMapping)
}

// LookupCollection returns the collection mapping of type t for the
// billing entity be, in the order of [Resolve]: the entity's mapping of
// the type → the entity's fallback_item → the mapping of the type without
// billing entity → the fallback_item without billing entity; tax,
// currencies and fallback_item take no fallback_item. It returns nil when
// nothing maps.
func LookupCollection(mappings []*CollectionMapping, be core.BillingEntityID, t CollectionMappingType) *CollectionMapping {
	fallback := t != CollectionMappingTypeTax && t != CollectionMappingTypeCurrencies &&
		t != CollectionMappingTypeFallbackItem
	if c := pickCollection(mappings, be, t, true); c != nil {
		return c
	}
	if fallback {
		if c := pickCollection(mappings, be, CollectionMappingTypeFallbackItem, true); c != nil {
			return c
		}
	}
	if c := pickCollection(mappings, be, t, false); c != nil {
		return c
	}
	if fallback {
		return pickCollection(mappings, be, CollectionMappingTypeFallbackItem, false)
	}
	return nil
}

// Item returns the external item of the mapping.
func (m *CollectionMapping) Item() ExternalItem {
	var id string
	if m.ExternalID != nil {
		id = *m.ExternalID
	}
	return ExternalItem{id, m.ExternalAccountCode, m.ExternalName}
}

// pickCollection returns the first mapping of type t of the billing
// entity be when scoped, or the first one without billing entity.
func pickCollection(mappings []*CollectionMapping, be core.BillingEntityID, t CollectionMappingType, scoped bool) *CollectionMapping {
	if scoped && be.IsZero() {
		return nil
	}
	for _, c := range mappings {
		if c.Type != t {
			continue
		}
		if scoped && c.BillingEntityID == be || !scoped && c.BillingEntityID.IsZero() {
			return c
		}
	}
	return nil
}

func cmpOr[T any](a, b *T) *T {
	if a != nil {
		return a
	}
	return b
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
