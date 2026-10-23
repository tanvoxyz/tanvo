package rpc

import (
	"context"
	"maps"
	"slices"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	customerv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1"
	integrationv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/integration/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/integration/v1/integrationv1connect"
)

// Integrations serves tanvo.integration.v1.IntegrationService over the
// integrations service (lago's integration and payment provider GraphQL
// mutations and resolvers). The RPCs that call a provider (the OAuth
// flows, SyncIntegrationItems, ListIntegrationSubsidiaries) stay
// unimplemented.
type Integrations struct {
	integrationv1connect.UnimplementedIntegrationServiceHandler

	svc *integrations.Service
}

var _ integrationv1connect.IntegrationServiceHandler = (*Integrations)(nil)

// NewIntegrations returns the service over svc.
func NewIntegrations(svc *integrations.Service) *Integrations { return &Integrations{svc: svc} }

// IntegrationSecurityLog adapts the security log to the integrations
// service.
func IntegrationSecurityLog(l auth.SecurityLogger) integrations.SecurityLogger {
	return integrationSecurityLog{l}
}

type integrationSecurityLog struct{ l auth.SecurityLogger }

func (g integrationSecurityLog) RecordIntegration(ctx context.Context, org core.OrganizationID, event string, resources map[string]any) error {
	return g.l.Record(ctx, auth.SecurityEvent{
		OrganizationID: org, Type: auth.SecurityLogIntegration, Event: event, Resources: resources,
	})
}

var (
	integrationProviders = NewEnum(map[integration.Provider]integrationv1.IntegrationProvider{
		integration.ProviderStripe:      integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_STRIPE,
		integration.ProviderAdyen:       integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_ADYEN,
		integration.ProviderGocardless:  integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_GOCARDLESS,
		integration.ProviderCashfree:    integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_CASHFREE,
		integration.ProviderMoneyhash:   integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_MONEYHASH,
		integration.ProviderFlutterwave: integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_FLUTTERWAVE,
		integration.ProviderNetsuite:    integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_NETSUITE,
		integration.ProviderXero:        integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_XERO,
		integration.ProviderAnrok:       integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_ANROK,
		integration.ProviderAvalara:     integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_AVALARA,
		integration.ProviderHubspot:     integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_HUBSPOT,
		integration.ProviderSalesforce:  integrationv1.IntegrationProvider_INTEGRATION_PROVIDER_SALESFORCE,
	})
	integrationCategories = NewEnum(map[integration.Category]customerv1.IntegrationCategory{
		integration.CategoryPayment:    customerv1.IntegrationCategory_INTEGRATION_CATEGORY_PAYMENT,
		integration.CategoryAccounting: customerv1.IntegrationCategory_INTEGRATION_CATEGORY_ACCOUNTING,
		integration.CategoryTax:        customerv1.IntegrationCategory_INTEGRATION_CATEGORY_TAX,
		integration.CategoryCRM:        customerv1.IntegrationCategory_INTEGRATION_CATEGORY_CRM,
	})
	hubspotTargetedObjects = NewEnum(map[string]integrationv1.HubspotTargetedObject{
		integrations.TargetedObjectCompanies: integrationv1.HubspotTargetedObject_HUBSPOT_TARGETED_OBJECT_COMPANIES,
		integrations.TargetedObjectContacts:  integrationv1.HubspotTargetedObject_HUBSPOT_TARGETED_OBJECT_CONTACTS,
	})
	mappableTypes = NewEnum(map[integration.MappableType]integrationv1.MappableType{
		integration.MappableTypeAddOn:          integrationv1.MappableType_MAPPABLE_TYPE_ADD_ON,
		integration.MappableTypeBillableMetric: integrationv1.MappableType_MAPPABLE_TYPE_BILLABLE_METRIC,
	})
	collectionMappingTypes = NewEnum(map[integration.CollectionMappingType]integrationv1.CollectionMappingType{
		integration.CollectionMappingTypeFallbackItem:      integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_FALLBACK_ITEM,
		integration.CollectionMappingTypeCoupon:            integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_COUPON,
		integration.CollectionMappingTypeSubscriptionFee:   integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_SUBSCRIPTION_FEE,
		integration.CollectionMappingTypeMinimumCommitment: integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_MINIMUM_COMMITMENT,
		integration.CollectionMappingTypeTax:               integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_TAX,
		integration.CollectionMappingTypePrepaidCredit:     integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_PREPAID_CREDIT,
		integration.CollectionMappingTypeCreditNote:        integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_CREDIT_NOTE,
		integration.CollectionMappingTypeAccount:           integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_ACCOUNT,
		integration.CollectionMappingTypeCurrencies:        integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_CURRENCIES,
	})
	integrationItemTypes = NewEnum(map[integration.ItemType]integrationv1.IntegrationItemType{
		integration.ItemTypeStandard: integrationv1.IntegrationItemType_INTEGRATION_ITEM_TYPE_STANDARD,
		integration.ItemTypeTax:      integrationv1.IntegrationItemType_INTEGRATION_ITEM_TYPE_TAX,
		integration.ItemTypeAccount:  integrationv1.IntegrationItemType_INTEGRATION_ITEM_TYPE_ACCOUNT,
	})
)

// outputSettings are the output-only settings fields lago exposes, by
// field and settings key (types/integrations/avalara.rb:11,
// types/integrations/hubspot.rb:14); Anrok's account is derived from the
// API key (types/integrations/anrok.rb:20).
var outputSettings = map[string]string{
	"avalara.company_id":        integrations.KeyCompanyID,
	"hubspot.portal_id":         integrations.KeyPortalID,
	"anrok.external_account_id": integrations.KeyExternalAccountID,
}

// integrationBaseFields are the update_mask paths of UpdateIntegration
// besides the provider settings ("<provider>.<field>").
var integrationBaseFields = []string{"code", "name", "sync_invoices", "sync_credit_notes", "sync_payments", "sync_subscriptions"}

// integrationUpdateFields are all the update_mask paths of
// UpdateIntegration.
var integrationUpdateFields = func() []string {
	out := slices.Clone(integrationBaseFields)
	od := (&integrationv1.Integration{}).ProtoReflect().Descriptor().Oneofs().ByName("settings")
	for i := range od.Fields().Len() {
		f := od.Fields().Get(i)
		for j := range f.Message().Fields().Len() {
			sf := f.Message().Fields().Get(j)
			path := string(f.Name()) + "." + string(sf.Name())
			if _, derived := outputSettings[path]; !derived && !outputOnly(sf) {
				out = append(out, path)
			}
		}
	}
	return out
}()

// outputOnly reports the settings fields the API never takes.
func outputOnly(f protoreflect.FieldDescriptor) bool {
	switch f.FullName() {
	case "tanvo.integration.v1.AnrokSettings.external_account_id",
		"tanvo.integration.v1.AvalaraSettings.company_id", "tanvo.integration.v1.HubspotSettings.portal_id":
		return true
	}
	return false
}

// CreateIntegration implements IntegrationService.CreateIntegration (lago
// Integrations::<Provider>::CreateService and PaymentProviders::
// <Provider>Service#create_or_update).
func (s *Integrations) CreateIntegration(ctx context.Context, req *integrationv1.CreateIntegrationRequest) (*integrationv1.CreateIntegrationResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetIntegration()
	provider, values := settingsInput(b)
	i, err := s.svc.Create(ctx, p.OrganizationID, integrations.Input{
		Code: b.GetCode(), Name: b.GetName(), Provider: provider, Values: values,
		SyncInvoices: b.GetSyncInvoices(), SyncCreditNotes: b.GetSyncCreditNotes(),
		SyncPayments: b.GetSyncPayments(), SyncSubscriptions: b.GetSyncSubscriptions(),
	})
	if err != nil {
		return nil, integrationError(ctx, provider, err)
	}
	out, err := integrationMessage(i)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.CreateIntegrationResponse_builder{Integration: out}.Build(), nil
}

// GetIntegration implements IntegrationService.GetIntegration.
func (s *Integrations) GetIntegration(ctx context.Context, req *integrationv1.GetIntegrationRequest) (*integrationv1.GetIntegrationResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := integrationMessage(i)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.GetIntegrationResponse_builder{Integration: out}.Build(), nil
}

// ListIntegrations implements IntegrationService.ListIntegrations, in
// creation order, filtered on category and provider (lago
// IntegrationsResolver types and PaymentProvidersResolver type).
func (s *Integrations) ListIntegrations(ctx context.Context, req *integrationv1.ListIntegrationsRequest) (*integrationv1.ListIntegrationsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := integrationListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*integrationv1.Integration, len(page.Items))
	for k, i := range page.Items {
		if out[k], err = integrationMessage(i); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return integrationv1.ListIntegrationsResponse_builder{Integrations: out, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdateIntegration implements IntegrationService.UpdateIntegration (lago
// Integrations::<Provider>::UpdateService and the payment providers'
// create_or_update). The fields updated are the update_mask paths or,
// without one, the fields set; a masked settings field left empty clears
// it.
func (s *Integrations) UpdateIntegration(ctx context.Context, req *integrationv1.UpdateIntegrationRequest) (*integrationv1.UpdateIntegrationResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetIntegration()
	msg := b.ProtoReflect()
	paths, err := UpdatePaths(req.GetUpdateMask(), integrationUpdateFields, func(path string) bool {
		f, sub := settingsField(msg, path)
		if f == nil {
			return false
		}
		if sub == nil {
			return msg.Has(f)
		}
		return msg.Has(f) && msg.Get(f).Message().Has(sub)
	})
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.IntegrationID]("integration.id", b.GetId())
	if err != nil {
		return nil, err
	}
	var in integrations.Update
	settingsPaths := map[string]bool{}
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "sync_invoices":
			in.SyncInvoices = core.Some(b.GetSyncInvoices())
		case "sync_credit_notes":
			in.SyncCreditNotes = core.Some(b.GetSyncCreditNotes())
		case "sync_payments":
			in.SyncPayments = core.Some(b.GetSyncPayments())
		case "sync_subscriptions":
			in.SyncSubscriptions = core.Some(b.GetSyncSubscriptions())
		default:
			settingsPaths[path] = true
		}
	}
	if len(settingsPaths) > 0 {
		providers := map[integration.Provider]bool{}
		in.Values = map[string]string{}
		for path := range settingsPaths {
			f, sub := settingsField(msg, path)
			providers[integration.Provider(f.Name())] = true
			in.Provider = integration.Provider(f.Name())
			in.Values[string(sub.Name())] = settingValue(msg, f, sub)
		}
		if len(providers) > 1 {
			return nil, InvalidArgument("invalid update_mask", []FieldViolation{{
				Field: "update_mask", Reason: core.CodeInvalid, Description: "settings of one provider only",
			}})
		}
	}
	i, err := s.svc.Update(ctx, p.OrganizationID, id, in)
	if err != nil {
		return nil, integrationError(ctx, in.Provider, err)
	}
	out, err := integrationMessage(i)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.UpdateIntegrationResponse_builder{Integration: out}.Build(), nil
}

// DeleteIntegration implements IntegrationService.DeleteIntegration (lago
// Integrations::DestroyService and PaymentProviders::DestroyService).
func (s *Integrations) DeleteIntegration(ctx context.Context, req *integrationv1.DeleteIntegrationRequest) (*integrationv1.DeleteIntegrationResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	if _, err := s.svc.Delete(ctx, p.OrganizationID, i.ID); err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.DeleteIntegrationResponse_builder{}.Build(), nil
}

// ListIntegrationItems implements IntegrationService.ListIntegrationItems,
// filtered on item_type and searched on the external ID, name and account
// code (lago IntegrationItemsResolver and IntegrationItemsQuery).
func (s *Integrations) ListIntegrationItems(ctx context.Context, req *integrationv1.ListIntegrationItemsRequest) (*integrationv1.ListIntegrationItemsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	eq, err := filterFields(req.GetFilter(), "item_type")
	if err != nil {
		return nil, err
	}
	var f integration.ItemFilter
	t, err := singleEnum[integrationv1.IntegrationItemType]("item_type", eq["item_type"], integrationv1.IntegrationItemType(0).Descriptor())
	if err != nil {
		return nil, err
	}
	f.ItemType = integrationItemTypes.Core(t)
	f.Search = req.GetSearch()
	page, err := s.svc.Items(ctx, p.OrganizationID, i.ID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.ListIntegrationItemsResponse_builder{
		Items: Items(page, integrationItem), NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// ListIntegrationMappings implements
// IntegrationService.ListIntegrationMappings, filtered on mappable_type,
// mappable_id and billing_entity_id (lago integration_mappings fields of
// the add-on and billable metric types).
func (s *Integrations) ListIntegrationMappings(ctx context.Context, req *integrationv1.ListIntegrationMappingsRequest) (*integrationv1.ListIntegrationMappingsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	eq, err := filterFields(req.GetFilter(), "mappable_type", "mappable_id", "billing_entity_id")
	if err != nil {
		return nil, err
	}
	var f integrations.MappingFilter
	t, err := singleEnum[integrationv1.MappableType]("mappable_type", eq["mappable_type"], integrationv1.MappableType(0).Descriptor())
	if err != nil {
		return nil, err
	}
	f.MappableType = mappableTypes.Core(t)
	if f.Mappable, err = filterRef("mappable_id", eq["mappable_id"]); err != nil {
		return nil, err
	}
	if !f.Mappable.IsZero() && f.MappableType == "" {
		return nil, filterError("field \"mappable_id\" needs mappable_type")
	}
	if f.BillingEntity, err = filterRef("billing_entity_id", eq["billing_entity_id"]); err != nil {
		return nil, err
	}
	page, err := s.svc.Mappings(ctx, p.OrganizationID, i.ID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.ListIntegrationMappingsResponse_builder{
		Mappings: Items(page, integrationMapping), NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpsertIntegrationMapping implements
// IntegrationService.UpsertIntegrationMapping (lago
// IntegrationMappings::CreateService and UpdateService).
func (s *Integrations) UpsertIntegrationMapping(ctx context.Context, req *integrationv1.UpsertIntegrationMappingRequest) (*integrationv1.UpsertIntegrationMappingResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	b := req.GetMapping()
	mappable, err := CatalogRef("mapping.mappable_id", b.GetMappableId())
	if err != nil {
		return nil, err
	}
	var be refs.Ref
	if b.HasBillingEntityId() {
		if be, err = CatalogRef("mapping.billing_entity_id", b.GetBillingEntityId()); err != nil {
			return nil, err
		}
	}
	m, err := s.svc.UpsertMapping(ctx, p.OrganizationID, i.ID, integrations.MappingInput{
		MappableType: mappableTypes.Core(b.GetMappableType()), Mappable: mappable, BillingEntity: be,
		ExternalID:          b.GetExternalId(),
		ExternalAccountCode: Opt(b.HasExternalAccountCode(), b.GetExternalAccountCode()),
		ExternalName:        Opt(b.HasExternalName(), b.GetExternalName()),
	})
	if err != nil {
		return nil, prefixedError(ctx, "mapping.", "invalid mapping", err)
	}
	return integrationv1.UpsertIntegrationMappingResponse_builder{Mapping: integrationMapping(m)}.Build(), nil
}

// DeleteIntegrationMapping implements
// IntegrationService.DeleteIntegrationMapping (lago
// IntegrationMappings::DestroyService).
func (s *Integrations) DeleteIntegrationMapping(ctx context.Context, req *integrationv1.DeleteIntegrationMappingRequest) (*integrationv1.DeleteIntegrationMappingResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	id, err := ParseID[core.IntegrationMappingID]("mapping_id", req.GetMappingId())
	if err != nil {
		return nil, err
	}
	if err := s.svc.DeleteMapping(ctx, p.OrganizationID, i.ID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.DeleteIntegrationMappingResponse_builder{}.Build(), nil
}

// ListCollectionMappings implements IntegrationService.ListCollectionMappings,
// filtered on mapping_type and billing_entity_id (lago
// IntegrationCollectionMappingsResolver).
func (s *Integrations) ListCollectionMappings(ctx context.Context, req *integrationv1.ListCollectionMappingsRequest) (*integrationv1.ListCollectionMappingsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	eq, err := filterFields(req.GetFilter(), "mapping_type", "billing_entity_id")
	if err != nil {
		return nil, err
	}
	t, err := singleEnum[integrationv1.CollectionMappingType]("mapping_type", eq["mapping_type"], integrationv1.CollectionMappingType(0).Descriptor())
	if err != nil {
		return nil, err
	}
	be, err := filterRef("billing_entity_id", eq["billing_entity_id"])
	if err != nil {
		return nil, err
	}
	ms, err := s.svc.CollectionMappings(ctx, p.OrganizationID, i.ID, collectionMappingTypes.Core(t), be)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*integrationv1.CollectionMapping, len(ms))
	for k, m := range ms {
		out[k] = collectionMapping(m)
	}
	return integrationv1.ListCollectionMappingsResponse_builder{CollectionMappings: out}.Build(), nil
}

// UpsertCollectionMapping implements
// IntegrationService.UpsertCollectionMapping (lago
// IntegrationCollectionMappings::CreateService and UpdateService).
func (s *Integrations) UpsertCollectionMapping(ctx context.Context, req *integrationv1.UpsertCollectionMappingRequest) (*integrationv1.UpsertCollectionMappingResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	b := req.GetCollectionMapping()
	var be refs.Ref
	if b.HasBillingEntityId() {
		if be, err = CatalogRef("collection_mapping.billing_entity_id", b.GetBillingEntityId()); err != nil {
			return nil, err
		}
	}
	m, err := s.svc.UpsertCollectionMapping(ctx, p.OrganizationID, i.ID, integrations.CollectionMappingInput{
		Type: collectionMappingTypes.Core(b.GetMappingType()), BillingEntity: be,
		ExternalID:          Opt(b.HasExternalId(), b.GetExternalId()),
		ExternalAccountCode: Opt(b.HasExternalAccountCode(), b.GetExternalAccountCode()),
		ExternalName:        Opt(b.HasExternalName(), b.GetExternalName()),
		TaxNexus:            Opt(b.HasTaxNexus(), b.GetTaxNexus()),
		TaxType:             Opt(b.HasTaxType(), b.GetTaxType()),
		TaxCode:             Opt(b.HasTaxCode(), b.GetTaxCode()),
		Currencies:          maps.Clone(b.GetCurrencies()),
	})
	if err != nil {
		return nil, prefixedError(ctx, "collection_mapping.", "invalid collection mapping", err)
	}
	return integrationv1.UpsertCollectionMappingResponse_builder{CollectionMapping: collectionMapping(m)}.Build(), nil
}

// DeleteCollectionMapping implements
// IntegrationService.DeleteCollectionMapping (lago
// IntegrationCollectionMappings::DestroyService).
func (s *Integrations) DeleteCollectionMapping(ctx context.Context, req *integrationv1.DeleteCollectionMappingRequest) (*integrationv1.DeleteCollectionMappingResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	id, err := ParseID[core.IntegrationCollectionMappingID]("collection_mapping_id", req.GetCollectionMappingId())
	if err != nil {
		return nil, err
	}
	if err := s.svc.DeleteCollectionMapping(ctx, p.OrganizationID, i.ID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.DeleteCollectionMappingResponse_builder{}.Build(), nil
}

// find resolves an integration reference: an ID or code:<code>.
func (s *Integrations) find(ctx context.Context, org core.OrganizationID, ref string) (*integration.Integration, error) {
	return Lookup[core.IntegrationID, *integration.Integration]{
		ByID: func(ctx context.Context, id core.IntegrationID) (*integration.Integration, error) {
			return s.svc.Get(ctx, org, id)
		},
		ByCode: func(ctx context.Context, code string) (*integration.Integration, error) {
			return s.svc.GetByCode(ctx, org, code)
		},
	}.Find(ctx, "integration_id", ref)
}

// settingsField resolves an update path: a top-level field (sub nil) or
// a "<provider>.<field>" settings field.
func settingsField(msg protoreflect.Message, path string) (f, sub protoreflect.FieldDescriptor) {
	fields := msg.Descriptor().Fields()
	for k := range len(path) {
		if path[k] == '.' {
			f = fields.ByName(protoreflect.Name(path[:k]))
			if f == nil || f.Message() == nil {
				return nil, nil
			}
			return f, f.Message().Fields().ByName(protoreflect.Name(path[k+1:]))
		}
	}
	return fields.ByName(protoreflect.Name(path)), nil
}

// settingValue returns the lago value of a settings field of msg.
func settingValue(msg protoreflect.Message, f, sub protoreflect.FieldDescriptor) string {
	v := msg.Get(f).Message().Get(sub)
	if sub.Kind() == protoreflect.EnumKind {
		return hubspotTargetedObjects.Core(integrationv1.HubspotTargetedObject(v.Enum()))
	}
	return v.String()
}

// settingsInput returns the provider of the settings set on b and their
// values by lago key.
func settingsInput(b *integrationv1.Integration) (integration.Provider, map[string]string) {
	msg := b.ProtoReflect()
	f := msg.WhichOneof(msg.Descriptor().Oneofs().ByName("settings"))
	if f == nil {
		return "", nil
	}
	values := map[string]string{}
	sm := msg.Get(f).Message()
	sm.Range(func(sub protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if !outputOnly(sub) {
			values[string(sub.Name())] = settingValue(msg, f, sub)
		}
		return true
	})
	return integration.Provider(f.Name()), values
}

// integrationError maps the service errors: the settings keys onto the
// request's provider settings, the other fields onto integration.
func integrationError(ctx context.Context, provider integration.Provider, err error) error {
	v, ok := validationViolations("", err)
	if !ok {
		return Error(ctx, err)
	}
	var sub protoreflect.FieldDescriptors
	if provider != "" {
		if f := (&integrationv1.Integration{}).ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(provider)); f != nil {
			sub = f.Message().Fields()
		}
	}
	for k := range v {
		if sub != nil && sub.ByName(protoreflect.Name(v[k].Field)) != nil {
			v[k].Field = "integration." + string(provider) + "." + v[k].Field
		} else {
			v[k].Field = "integration." + v[k].Field
		}
	}
	return withReason(InvalidArgument("invalid integration", v), ReasonValidationErrors)
}

// prefixedError maps the service errors onto the request message at
// prefix.
func prefixedError(ctx context.Context, prefix, msg string, err error) error {
	if v, ok := validationViolations(prefix, err); ok {
		return withReason(InvalidArgument(msg, v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func filterFields(s string, allowed ...string) (map[string][]string, error) {
	e, err := ParseFilter(s)
	if err != nil {
		return nil, err
	}
	return Equalities(e, allowed...)
}

// singleEnum parses a filter field taking one value of the enumeration e;
// absent is 0.
func singleEnum[P ~int32](field string, values []string, e protoreflect.EnumDescriptor) (P, error) {
	v, err := FilterSingle(field, values)
	if err != nil || v == "" {
		return 0, err
	}
	out, err := FilterEnum[P](field, []string{v}, e)
	if err != nil {
		return 0, err
	}
	return out[0], nil
}

// filterRef parses a filter field taking one reference; absent is the
// zero reference.
func filterRef(field string, values []string) (refs.Ref, error) {
	v, err := FilterSingle(field, values)
	if err != nil || v == "" {
		return refs.Ref{}, err
	}
	return CatalogRef(field, v)
}

func integrationListFilter(s string) (integration.Filter, error) {
	var f integration.Filter
	eq, err := filterFields(s, "category", "provider")
	if err != nil {
		return f, err
	}
	c, err := singleEnum[customerv1.IntegrationCategory]("category", eq["category"], customerv1.IntegrationCategory(0).Descriptor())
	if err != nil {
		return f, err
	}
	p, err := singleEnum[integrationv1.IntegrationProvider]("provider", eq["provider"], integrationv1.IntegrationProvider(0).Descriptor())
	if err != nil {
		return f, err
	}
	f.Category, f.Provider = integrationCategories.Core(c), integrationProviders.Core(p)
	return f, nil
}

// integrationMessage converts an integration; only its non-secret
// settings are returned.
func integrationMessage(i *integration.Integration) (*integrationv1.Integration, error) {
	settings, err := integrations.Settings(i)
	if err != nil {
		return nil, err
	}
	out := integrationv1.Integration_builder{
		Id: new(i.ID.String()), Code: new(i.Code), Name: new(i.Name),
		SyncInvoices: new(i.SyncInvoices), SyncCreditNotes: new(i.SyncCreditNotes),
		SyncPayments: new(i.SyncPayments), SyncSubscriptions: new(i.SyncSubscriptions),
		CreatedAt: Timestamp(i.CreatedAt), UpdatedAt: Timestamp(i.UpdatedAt),
	}.Build()
	if p := integrationProviders.Proto(i.Provider); p != 0 {
		out.SetProvider(p)
	}
	if c := integrationCategories.Proto(i.Category); c != 0 {
		out.SetCategory(c)
	}
	msg := out.ProtoReflect()
	f := msg.Descriptor().Fields().ByName(protoreflect.Name(i.Provider))
	if f == nil || f.Message() == nil {
		return out, nil
	}
	sm := msg.Mutable(f).Message()
	fields := f.Message().Fields()
	for k := range fields.Len() {
		sub := fields.Get(k)
		key := string(sub.Name())
		if alt, ok := outputSettings[string(f.Name())+"."+key]; ok {
			key = alt
		} else if outputOnly(sub) {
			continue
		}
		val, ok := settings[key]
		if !ok || val == "" {
			continue
		}
		switch sub.Kind() {
		case protoreflect.EnumKind:
			if e := hubspotTargetedObjects.Proto(val); e != 0 {
				sm.Set(sub, protoreflect.ValueOfEnum(protoreflect.EnumNumber(e)))
			}
		case protoreflect.BoolKind:
			sm.Set(sub, protoreflect.ValueOfBool(val == "true"))
		default:
			sm.Set(sub, protoreflect.ValueOfString(val))
		}
	}
	return out, nil
}

func integrationItem(i *integration.Item) *integrationv1.IntegrationItem {
	b := integrationv1.IntegrationItem_builder{
		Id: new(i.ID.String()), ExternalId: new(i.ExternalID),
		ExternalAccountCode: i.ExternalAccountCode, ExternalName: i.ExternalName,
	}
	if t := integrationItemTypes.Proto(i.ItemType); t != 0 {
		b.ItemType = &t
	}
	return b.Build()
}

func integrationMapping(m *integration.Mapping) *integrationv1.IntegrationMapping {
	b := integrationv1.IntegrationMapping_builder{
		Id: new(m.ID.String()), MappableId: new(m.MappableID.String()), ExternalId: new(m.ExternalID),
		ExternalAccountCode: m.ExternalAccountCode, ExternalName: m.ExternalName,
	}
	if t := mappableTypes.Proto(m.MappableType); t != 0 {
		b.MappableType = &t
	}
	if !m.BillingEntityID.IsZero() {
		b.BillingEntityId = new(m.BillingEntityID.String())
	}
	return b.Build()
}

func collectionMapping(m *integration.CollectionMapping) *integrationv1.CollectionMapping {
	b := integrationv1.CollectionMapping_builder{
		Id: new(m.ID.String()), ExternalId: m.ExternalID, ExternalAccountCode: m.ExternalAccountCode,
		ExternalName: m.ExternalName, TaxNexus: m.TaxNexus, TaxType: m.TaxType, TaxCode: m.TaxCode,
		Currencies: maps.Clone(m.Currencies),
	}
	if t := collectionMappingTypes.Proto(m.Type); t != 0 {
		b.MappingType = &t
	}
	if !m.BillingEntityID.IsZero() {
		b.BillingEntityId = new(m.BillingEntityID.String())
	}
	return b.Build()
}
