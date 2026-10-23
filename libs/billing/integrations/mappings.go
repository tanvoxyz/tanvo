package integrations

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Resource names of the mapping not-found errors.
const (
	ResourceMapping           = "integration_mapping"
	ResourceCollectionMapping = "integration_collection_mapping"
	ResourceBillingEntity     = "billing_entity"
)

// CodeMappingNotSupported: the provider has no mappings; lago maps the
// NetSuite, Xero, Anrok and Avalara integrations only
// (integration_mappings/factory.rb, integration_collection_mappings/
// factory.rb).
const CodeMappingNotSupported = "mapping_not_supported"

// CodeRelationMustExist: the mapped object does not exist (lago
// belongs_to :mappable).
const CodeRelationMustExist = "relation_must_exist"

// mappable are the providers with mappings.
var mappable = []integration.Provider{
	integration.ProviderNetsuite, integration.ProviderXero, integration.ProviderAnrok, integration.ProviderAvalara,
}

// MappingInput is an item mapping to store.
type MappingInput struct {
	// MappableType is the kind of the mapped object.
	MappableType integration.MappableType
	// Mappable is the add-on or billable metric.
	Mappable refs.Ref
	// BillingEntity scopes the mapping; zero applies to all.
	BillingEntity refs.Ref
	// ExternalID is the provider's item ID.
	ExternalID string
	// ExternalAccountCode is the provider's account code.
	ExternalAccountCode *string
	// ExternalName is the provider's item name.
	ExternalName *string
}

// MappingFilter narrows [Service.Mappings].
type MappingFilter struct {
	// MappableType keeps one kind; "" keeps all.
	MappableType integration.MappableType
	// Mappable keeps the mappings of one object; zero keeps all.
	Mappable refs.Ref
	// BillingEntity keeps the mappings of one billing entity; zero keeps
	// all.
	BillingEntity refs.Ref
}

// CollectionMappingInput is a collection mapping to store.
type CollectionMappingInput struct {
	// Type is the collection.
	Type integration.CollectionMappingType
	// BillingEntity scopes the mapping; zero applies to all.
	BillingEntity refs.Ref
	// ExternalID is the provider's item ID.
	ExternalID *string
	// ExternalAccountCode is the provider's account code.
	ExternalAccountCode *string
	// ExternalName is the provider's item name.
	ExternalName *string
	// TaxNexus, TaxType and TaxCode are NetSuite's tax fields.
	TaxNexus *string
	// TaxType is the NetSuite tax type.
	TaxType *string
	// TaxCode is the NetSuite tax code.
	TaxCode *string
	// Currencies maps ISO codes to the provider's currency IDs.
	Currencies map[string]string
}

// UpsertMapping creates the mapping of an object, or replaces the one
// with the same object and billing entity (lago IntegrationMappings::
// CreateService and UpdateService).
func (s *Service) UpsertMapping(ctx context.Context, org core.OrganizationID, id core.IntegrationID, in MappingInput) (*integration.Mapping, error) {
	var out *integration.Mapping
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		i, err := mappableIntegration(ctx, r, org, id)
		if err != nil {
			return err
		}
		be, err := billingEntity(ctx, r, org, in.BillingEntity)
		if err != nil {
			return err
		}
		mappableID, err := mappableObject(ctx, r, org, in.MappableType, in.Mappable)
		if err != nil {
			return err
		}
		now := s.d.Now()
		m := &integration.Mapping{
			ID: core.NewID[core.IntegrationMappingID](s.d.IDs), OrganizationID: org, IntegrationID: i.ID,
			MappableType: in.MappableType, MappableID: mappableID, BillingEntityID: be,
			ExternalID: in.ExternalID, ExternalAccountCode: in.ExternalAccountCode, ExternalName: in.ExternalName,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := m.Validate(); err != nil {
			return err
		}
		cur, err := r.IntegrationMappings.Find(ctx, org, i.ID, m.MappableType, m.MappableID, be)
		switch {
		case err == nil:
			m.ID, m.CreatedAt = cur.ID, cur.CreatedAt
			err = r.IntegrationMappings.Update(ctx, m)
		case errors.Is(err, core.ErrNotFound):
			err = r.IntegrationMappings.Create(ctx, m)
		}
		if errors.Is(err, core.ErrConflict) {
			return &core.ValidationError{Errors: []core.FieldError{{Field: "mappable_type", Code: core.CodeAlreadyExists}}}
		}
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// DeleteMapping deletes a mapping of an integration (lago
// IntegrationMappings::DestroyService).
func (s *Service) DeleteMapping(ctx context.Context, org core.OrganizationID, id core.IntegrationID, mapping core.IntegrationMappingID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := get(ctx, r, org, id); err != nil {
			return err
		}
		m, err := r.IntegrationMappings.Get(ctx, org, mapping)
		if errors.Is(err, core.ErrNotFound) || (err == nil && m.IntegrationID != id) {
			return core.NotFound(ResourceMapping)
		}
		if err != nil {
			return err
		}
		return r.IntegrationMappings.Delete(ctx, org, mapping)
	})
}

// Mappings lists the mappings of an integration in creation order,
// filtered by f (lago integration_mappings fields of the add-on and
// billable metric types).
func (s *Service) Mappings(ctx context.Context, org core.OrganizationID, id core.IntegrationID, f MappingFilter, page core.PageRequest) (core.Page[*integration.Mapping], error) {
	var out core.Page[*integration.Mapping]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := get(ctx, r, org, id); err != nil {
			return err
		}
		be, err := billingEntity(ctx, r, org, f.BillingEntity)
		if err != nil {
			return err
		}
		var obj uuid.UUID
		if !f.Mappable.IsZero() {
			if obj, err = mappableObject(ctx, r, org, f.MappableType, f.Mappable); err != nil {
				return err
			}
		}
		all, err := r.IntegrationMappings.ListByIntegration(ctx, org, id)
		if err != nil {
			return err
		}
		all = slices.DeleteFunc(all, func(m *integration.Mapping) bool {
			return (f.MappableType != "" && m.MappableType != f.MappableType) ||
				(obj != uuid.Nil && m.MappableID != obj) || (!be.IsZero() && m.BillingEntityID != be)
		})
		out, err = pageOf(all, func(m *integration.Mapping) uuid.UUID { return m.ID.UUID() }, page)
		return err
	})
	return out, err
}

// UpsertCollectionMapping creates the collection mapping of a type, or
// replaces the one with the same type and billing entity (lago
// IntegrationCollectionMappings::CreateService and UpdateService).
func (s *Service) UpsertCollectionMapping(ctx context.Context, org core.OrganizationID, id core.IntegrationID, in CollectionMappingInput) (*integration.CollectionMapping, error) {
	var out *integration.CollectionMapping
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		i, err := mappableIntegration(ctx, r, org, id)
		if err != nil {
			return err
		}
		be, err := billingEntity(ctx, r, org, in.BillingEntity)
		if err != nil {
			return err
		}
		now := s.d.Now()
		m := &integration.CollectionMapping{
			ID: core.NewID[core.IntegrationCollectionMappingID](s.d.IDs), OrganizationID: org, IntegrationID: i.ID,
			Type: in.Type, BillingEntityID: be, ExternalID: in.ExternalID, ExternalAccountCode: in.ExternalAccountCode,
			ExternalName: in.ExternalName, CreatedAt: now, UpdatedAt: now,
		}
		var v core.Validator
		if i.Provider == integration.ProviderNetsuite {
			// netsuite_collection_mapping.rb:5-17: the tax fields and
			// currencies are NetSuite's; currencies apply to the whole
			// organization.
			m.TaxNexus, m.TaxType, m.TaxCode, m.Currencies = in.TaxNexus, in.TaxType, in.TaxCode, in.Currencies
			v.Check(m.Type != integration.CollectionMappingTypeCurrencies || be.IsZero(), "billing_entity", "value_must_be_blank")
			v.Check(validCurrencies(in.Currencies), "currencies", CodeInvalidFormat)
		} else {
			v.Check(in.TaxNexus == nil, "tax_nexus", core.CodeForbidden)
			v.Check(in.TaxType == nil, "tax_type", core.CodeForbidden)
			v.Check(in.TaxCode == nil, "tax_code", core.CodeForbidden)
			v.Check(len(in.Currencies) == 0, "currencies", core.CodeForbidden)
		}
		v.Merge("", m.Validate())
		if err := v.Err(); err != nil {
			return err
		}
		existing, err := r.IntegrationCollectionMappings.ListByType(ctx, org, i.ID, m.Type)
		if err != nil {
			return err
		}
		if k := slices.IndexFunc(existing, m.SameKey); k >= 0 {
			m.ID, m.CreatedAt = existing[k].ID, existing[k].CreatedAt
			err = r.IntegrationCollectionMappings.Update(ctx, m)
		} else {
			err = r.IntegrationCollectionMappings.Create(ctx, m)
		}
		if errors.Is(err, core.ErrConflict) {
			return &core.ValidationError{Errors: []core.FieldError{{Field: "mapping_type", Code: core.CodeAlreadyExists}}}
		}
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// DeleteCollectionMapping deletes a collection mapping of an integration
// (lago IntegrationCollectionMappings::DestroyService).
func (s *Service) DeleteCollectionMapping(ctx context.Context, org core.OrganizationID, id core.IntegrationID, mapping core.IntegrationCollectionMappingID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := get(ctx, r, org, id); err != nil {
			return err
		}
		m, err := r.IntegrationCollectionMappings.Get(ctx, org, mapping)
		if errors.Is(err, core.ErrNotFound) || (err == nil && m.IntegrationID != id) {
			return core.NotFound(ResourceCollectionMapping)
		}
		if err != nil {
			return err
		}
		return r.IntegrationCollectionMappings.Delete(ctx, org, mapping)
	})
}

// CollectionMappings lists the collection mappings of an integration in
// creation order, of one type unless t is "" and of one billing entity
// unless be is zero (lago IntegrationCollectionMappingsQuery).
func (s *Service) CollectionMappings(ctx context.Context, org core.OrganizationID, id core.IntegrationID, t integration.CollectionMappingType, beRef refs.Ref) ([]*integration.CollectionMapping, error) {
	var out []*integration.CollectionMapping
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := get(ctx, r, org, id); err != nil {
			return err
		}
		be, err := billingEntity(ctx, r, org, beRef)
		if err != nil {
			return err
		}
		if out, err = r.IntegrationCollectionMappings.ListByType(ctx, org, id, t); err != nil {
			return err
		}
		if !be.IsZero() {
			out = slices.DeleteFunc(out, func(m *integration.CollectionMapping) bool { return m.BillingEntityID != be })
		}
		return nil
	})
	return out, err
}

// Items lists the provider items fetched for an integration (lago
// IntegrationItemsResolver).
func (s *Service) Items(ctx context.Context, org core.OrganizationID, id core.IntegrationID, f integration.ItemFilter, page core.PageRequest) (core.Page[*integration.Item], error) {
	var out core.Page[*integration.Item]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := get(ctx, r, org, id); err != nil {
			return err
		}
		f.Search = core.SearchTerm(f.Search)
		var err error
		out, err = r.IntegrationItems.List(ctx, org, id, f, page)
		return err
	})
	return out, err
}

func mappableIntegration(ctx context.Context, r port.Repos, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, error) {
	i, err := get(ctx, r, org, id)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(mappable, i.Provider) {
		return nil, core.NewRuleError(CodeMappingNotSupported, string(i.Provider)+" integrations have no mappings")
	}
	return i, nil
}

// billingEntity resolves an optional billing entity reference; a missing
// one is core.NotFound("billing_entity").
func billingEntity(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (core.BillingEntityID, error) {
	if ref.IsZero() {
		return core.BillingEntityID{}, nil
	}
	f := refs.Finder[core.BillingEntityID, *organization.BillingEntity]{
		Resource: ResourceBillingEntity,
		Get: func(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
			return r.BillingEntities.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*organization.BillingEntity, error) {
			return r.BillingEntities.GetByCode(ctx, org, code)
		},
		ID: func(b *organization.BillingEntity) core.BillingEntityID { return b.ID },
	}
	b, err := f.One(ctx, ref)
	if err != nil {
		return core.BillingEntityID{}, err
	}
	return b.ID, nil
}

// mappableObject resolves the add-on or billable metric of a mapping; a
// missing one is a mappable relation_must_exist error.
func mappableObject(ctx context.Context, r port.Repos, org core.OrganizationID, t integration.MappableType, ref refs.Ref) (uuid.UUID, error) {
	var (
		id  uuid.UUID
		err error
	)
	switch t {
	case integration.MappableTypeAddOn:
		a, e := refs.AddOns(r, org).One(ctx, ref)
		if e == nil {
			id = a.ID.UUID()
		}
		err = e
	case integration.MappableTypeBillableMetric:
		m, e := refs.Metrics(r, org).One(ctx, ref)
		if e == nil {
			id = m.ID.UUID()
		}
		err = e
	default:
		return uuid.Nil, &core.ValidationError{Errors: []core.FieldError{{Field: "mappable_type", Code: core.CodeInvalid}}}
	}
	if errors.Is(err, core.ErrNotFound) {
		return uuid.Nil, &core.ValidationError{Errors: []core.FieldError{{Field: "mappable", Code: CodeRelationMustExist}}}
	}
	return id, err
}

// pageOf pages rows in their order; the token is the last ID.
func pageOf[T any](rows []T, id func(T) uuid.UUID, page core.PageRequest) (core.Page[T], error) {
	var out core.Page[T]
	start := 0
	if page.Token != "" {
		after, err := core.ParsePageToken(page.Token)
		if err != nil {
			return out, err
		}
		start = slices.IndexFunc(rows, func(x T) bool { return id(x) == after }) + 1
	}
	rows = rows[start:]
	if n := page.Limit(); len(rows) > n {
		rows = rows[:n]
		out.NextToken = id(rows[n-1]).String()
	}
	out.Items = rows
	return out, nil
}

// CodeInvalidFormat: the NetSuite currencies are not ISO codes mapped to
// non-blank IDs (netsuite_collection_mapping.rb:33-46).
const CodeInvalidFormat = "invalid_format"

// validCurrencies reports whether every key is an ISO currency and every
// value non-blank.
func validCurrencies(c map[string]string) bool {
	for k, v := range c {
		if _, err := money.LookupCurrency(k); err != nil || strings.TrimSpace(v) == "" {
			return false
		}
	}
	return true
}
