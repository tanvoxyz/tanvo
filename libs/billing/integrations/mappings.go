package integrations

import (
	"cmp"
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

// MappingUpdate holds the fields of a mapping to update (lago
// IntegrationMappings::UpdateService: the external fields only).
type MappingUpdate struct {
	// ExternalID is the new provider item ID.
	ExternalID core.Optional[string]
	// ExternalAccountCode is the new account code; nil clears it.
	ExternalAccountCode core.Optional[*string]
	// ExternalName is the new item name; nil clears it.
	ExternalName core.Optional[*string]
}

// CreateMapping creates the mapping of an object (lago
// IntegrationMappings::CreateService); a second mapping of the same
// object and billing entity is a mappable_type value_already_exist error
// (base_mapping.rb:16-17).
func (s *Service) CreateMapping(ctx context.Context, org core.OrganizationID, id core.IntegrationID, in MappingInput) (*integration.Mapping, error) {
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
		_, err = r.IntegrationMappings.Find(ctx, org, i.ID, m.MappableType, m.MappableID, be)
		switch {
		case err == nil:
			err = core.ErrConflict
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

// UpdateMapping updates the external fields of a mapping (lago
// IntegrationMappings::UpdateService); the object and billing entity
// stay.
func (s *Service) UpdateMapping(ctx context.Context, org core.OrganizationID, id core.IntegrationID, mapping core.IntegrationMappingID, in MappingUpdate) (*integration.Mapping, error) {
	var out *integration.Mapping
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
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
		in.ExternalID.Apply(&m.ExternalID)
		in.ExternalAccountCode.Apply(&m.ExternalAccountCode)
		in.ExternalName.Apply(&m.ExternalName)
		if err := m.Validate(); err != nil {
			return err
		}
		m.UpdatedAt = s.d.Now()
		if err := r.IntegrationMappings.Update(ctx, m); err != nil {
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

// CollectionMappingUpdate holds the fields of a collection mapping to
// update (lago IntegrationCollectionMappings::UpdateService: every field
// but the type and billing entity).
type CollectionMappingUpdate struct {
	// ExternalID is the new provider item ID; nil clears it.
	ExternalID core.Optional[*string]
	// ExternalAccountCode is the new account code; nil clears it.
	ExternalAccountCode core.Optional[*string]
	// ExternalName is the new item name; nil clears it.
	ExternalName core.Optional[*string]
	// TaxNexus is the new NetSuite tax nexus; nil clears it.
	TaxNexus core.Optional[*string]
	// TaxType is the new NetSuite tax type; nil clears it.
	TaxType core.Optional[*string]
	// TaxCode is the new NetSuite tax code; nil clears it.
	TaxCode core.Optional[*string]
	// Currencies are the new NetSuite currencies; a non-nil empty map is
	// a currencies cannot_be_empty error.
	Currencies core.Optional[map[string]string]
}

// CreateCollectionMapping creates the collection mapping of a type (lago
// IntegrationCollectionMappings::CreateService); a second mapping of the
// same type and billing entity is a mapping_type value_already_exist
// error (base_collection_mapping.rb:27-28).
func (s *Service) CreateCollectionMapping(ctx context.Context, org core.OrganizationID, id core.IntegrationID, in CollectionMappingInput) (*integration.CollectionMapping, error) {
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
			m.TaxNexus, m.TaxType, m.TaxCode, m.Currencies = in.TaxNexus, in.TaxType, in.TaxCode, in.Currencies
		} else {
			nonNetsuiteFields(&v, in.TaxNexus != nil, in.TaxType != nil, in.TaxCode != nil, in.Currencies != nil)
		}
		if err := validateCollectionMapping(&v, i, m); err != nil {
			return err
		}
		existing, err := r.IntegrationCollectionMappings.ListByType(ctx, org, i.ID, m.Type)
		if err != nil {
			return err
		}
		err = core.ErrConflict
		if !slices.ContainsFunc(existing, m.SameKey) {
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

// UpdateCollectionMapping updates a collection mapping (lago
// IntegrationCollectionMappings::UpdateService); the type and billing
// entity stay.
func (s *Service) UpdateCollectionMapping(ctx context.Context, org core.OrganizationID, id core.IntegrationID, mapping core.IntegrationCollectionMappingID, in CollectionMappingUpdate) (*integration.CollectionMapping, error) {
	var out *integration.CollectionMapping
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		i, err := get(ctx, r, org, id)
		if err != nil {
			return err
		}
		m, err := r.IntegrationCollectionMappings.Get(ctx, org, mapping)
		if errors.Is(err, core.ErrNotFound) || (err == nil && m.IntegrationID != id) {
			return core.NotFound(ResourceCollectionMapping)
		}
		if err != nil {
			return err
		}
		in.ExternalID.Apply(&m.ExternalID)
		in.ExternalAccountCode.Apply(&m.ExternalAccountCode)
		in.ExternalName.Apply(&m.ExternalName)
		var v core.Validator
		if i.Provider == integration.ProviderNetsuite {
			in.TaxNexus.Apply(&m.TaxNexus)
			in.TaxType.Apply(&m.TaxType)
			in.TaxCode.Apply(&m.TaxCode)
			in.Currencies.Apply(&m.Currencies)
		} else {
			nonNetsuiteFields(&v, in.TaxNexus.Set, in.TaxType.Set, in.TaxCode.Set, in.Currencies.Set)
		}
		if err := validateCollectionMapping(&v, i, m); err != nil {
			return err
		}
		m.UpdatedAt = s.d.Now()
		if err := r.IntegrationCollectionMappings.Update(ctx, m); err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// nonNetsuiteFields refuses the NetSuite fields on another provider's
// mapping with value_not_allowed, where lago raises NoMethodError: only
// NetsuiteCollectionMapping has their settings accessors
// (netsuite_collection_mapping.rb:5-6, create_service.rb:38-41,
// update_service.rb:22-25).
func nonNetsuiteFields(v *core.Validator, taxNexus, taxType, taxCode, currencies bool) {
	v.Check(!taxNexus, "tax_nexus", core.CodeForbidden)
	v.Check(!taxType, "tax_type", core.CodeForbidden)
	v.Check(!taxCode, "tax_code", core.CodeForbidden)
	v.Check(!currencies, "currencies", core.CodeForbidden)
}

// validateCollectionMapping adds the entity rules and, for NetSuite, the
// currency and organization-level rules of netsuite_collection_mapping.rb:
// 13-40, to v.
func validateCollectionMapping(v *core.Validator, i *integration.Integration, m *integration.CollectionMapping) error {
	if i.Provider == integration.ProviderNetsuite {
		currencies := m.Type == integration.CollectionMappingTypeCurrencies
		v.Check(!currencies || m.BillingEntityID.IsZero(), "billing_entity", CodeMustBeBlank)
		switch {
		case !currencies && m.Currencies == nil:
		case !currencies && len(m.Currencies) > 0:
			v.Check(false, "currencies", CodeMustBeBlank)
		case m.Currencies == nil:
			v.Check(false, "currencies", core.CodeRequired)
		case len(m.Currencies) == 0:
			v.Check(false, "currencies", CodeCannotBeEmpty)
		default:
			v.Check(validCurrencies(m.Currencies), "currencies", CodeInvalidFormat)
		}
	}
	v.Merge("", m.Validate())
	return v.Err()
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

// CollectionMappings lists the collection mappings of an integration,
// newest first, of one type unless t is "" and of one billing entity
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
		// Newest first, then by ID (integration_collection_mappings_query.rb:9,
		// base_query.rb:6, 111).
		slices.SortStableFunc(out, func(a, b *integration.CollectionMapping) int {
			return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(a.ID.String(), b.ID.String()))
		})
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

// Codes of the NetSuite collection mapping rules
// (netsuite_collection_mapping.rb:13-46).
const (
	// CodeInvalidFormat: the currencies are not ISO codes mapped to
	// non-blank IDs.
	CodeInvalidFormat = "invalid_format"
	// CodeMustBeBlank: currencies on a mapping of another type, or a
	// billing entity on the currencies mapping.
	CodeMustBeBlank = "value_must_be_blank"
	// CodeCannotBeEmpty: the currencies are given but empty.
	CodeCannotBeEmpty = "cannot_be_empty"
)

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
