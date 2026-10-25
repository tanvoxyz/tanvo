package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindIntegration binds the repositories of the integration aggregates.
func (t *txState) bindIntegration(r *port.Repos) {
	r.Integrations = integrationRepo{t}
	r.IntegrationCustomers = integrationCustomerRepo{t}
	r.IntegrationMappings = integrationMappingRepo{t}
	r.IntegrationCollectionMappings = integrationCollectionMappingRepo{t}
	r.IntegrationItems = integrationItemRepo{t}
	r.IntegrationResources = integrationResourceRepo{t}
	r.ObjectConnections = objectConnectionRepo{t}
}

// integrationOptEnum returns nil for the empty enum value (no filter).
func integrationOptEnum[E ~string](e E) *string { return optString(string(e)) }

// integrationRepo implements integration.Repository.
type integrationRepo struct{ t *txState }

var _ integration.Repository = integrationRepo{}

func integrationParams(i *integration.Integration) db.CreateIntegrationParams {
	return db.CreateIntegrationParams{
		OrganizationID:    i.OrganizationID.UUID(),
		ID:                i.ID.UUID(),
		Code:              i.Code,
		Name:              i.Name,
		Category:          string(i.Category),
		Provider:          string(i.Provider),
		IsDefault:         i.IsDefault,
		Settings:          rawJSON(i.Settings),
		Secrets:           integrationBytes(i.Secrets),
		SyncInvoices:      i.SyncInvoices,
		SyncCreditNotes:   i.SyncCreditNotes,
		SyncPayments:      i.SyncPayments,
		SyncSubscriptions: i.SyncSubscriptions,
		CreatedAt:         utc(i.CreatedAt),
		UpdatedAt:         utc(i.UpdatedAt),
	}
}

// integrationBytes returns b, or nil (a NULL column) when b is empty.
func integrationBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

func integrationOf(r db.Integration) (*integration.Integration, error) {
	return &integration.Integration{
		ID:                idOf[core.IntegrationID](r.ID),
		OrganizationID:    idOf[core.OrganizationID](r.OrganizationID),
		Code:              r.Code,
		Name:              r.Name,
		Category:          integration.Category(r.Category),
		Provider:          integration.Provider(r.Provider),
		IsDefault:         r.IsDefault,
		Settings:          rawJSON(r.Settings),
		Secrets:           integrationBytes(r.Secrets),
		SyncInvoices:      r.SyncInvoices,
		SyncCreditNotes:   r.SyncCreditNotes,
		SyncPayments:      r.SyncPayments,
		SyncSubscriptions: r.SyncSubscriptions,
		CreatedAt:         utc(r.CreatedAt),
		UpdatedAt:         utc(r.UpdatedAt),
		DeletedAt:         optUTC(r.DeletedAt),
	}, nil
}

func (r integrationRepo) Create(ctx context.Context, i *integration.Integration) error {
	q, err := r.t.queries(ctx, i.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateIntegration(ctx, integrationParams(i))
	return insertErr("integration", n, err)
}

func (r integrationRepo) Update(ctx context.Context, i *integration.Integration) error {
	q, err := r.t.queries(ctx, i.OrganizationID)
	if err != nil {
		return err
	}
	// The code and the default of the category are unique.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateIntegration(ctx, db.UpdateIntegrationParams(integrationParams(i)))
		return err
	})
	return updateErr("integration", i.ID, n, err)
}

func (r integrationRepo) Get(ctx context.Context, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetIntegration(ctx, db.GetIntegrationParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("integration", id, err)
	}
	return integrationOf(row)
}

func (r integrationRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*integration.Integration, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetIntegrationByCode(ctx, db.GetIntegrationByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("integration", code, err)
	}
	return integrationOf(row)
}

func (r integrationRepo) GetDefault(ctx context.Context, org core.OrganizationID, c integration.Category) (*integration.Integration, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDefaultIntegration(ctx, db.GetDefaultIntegrationParams{OrganizationID: org.UUID(), Category: string(c)})
	if err != nil {
		return nil, getErr("integration", "default "+string(c), err)
	}
	return integrationOf(row)
}

func (r integrationRepo) ListByProvider(ctx context.Context, org core.OrganizationID, p integration.Provider) ([]*integration.Integration, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListIntegrationsByProvider(ctx, db.ListIntegrationsByProviderParams{OrganizationID: org.UUID(), Provider: string(p)})
	if err != nil {
		return nil, fmt.Errorf("list integrations: %w", mapErr(err))
	}
	return convAll(rows, integrationOf)
}

// ListByDomain is not organization-scoped: it routes single sign-on.
func (r integrationRepo) ListByDomain(ctx context.Context, p integration.Provider, domain string) ([]*integration.Integration, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListIntegrationsByDomain(ctx, db.ListIntegrationsByDomainParams{Provider: string(p), Domain: domain})
	if err != nil {
		return nil, fmt.Errorf("list integrations by domain: %w", mapErr(err))
	}
	return convAll(rows, integrationOf)
}

// OrganizationOf is not organization-scoped: it routes provider webhooks.
func (r integrationRepo) OrganizationOf(ctx context.Context, id core.IntegrationID) (core.OrganizationID, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return core.OrganizationID{}, err
	}
	org, err := q.OrganizationOfPaymentIntegration(ctx, id.UUID())
	if err != nil {
		return core.OrganizationID{}, getErr("integration", id, err)
	}
	return idOf[core.OrganizationID](org), nil
}

func (r integrationRepo) List(ctx context.Context, org core.OrganizationID, f integration.Filter, page core.PageRequest) (core.Page[*integration.Integration], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*integration.Integration]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*integration.Integration]{}, err
	}
	rows, err := q.ListIntegrations(ctx, db.ListIntegrationsParams{
		OrganizationID: org.UUID(),
		Category:       integrationOptEnum(f.Category),
		Provider:       integrationOptEnum(f.Provider),
		After:          after,
		Lim:            lim,
	})
	if err != nil {
		return core.Page[*integration.Integration]{}, fmt.Errorf("list integrations: %w", mapErr(err))
	}
	return pageOf(rows, page, integrationOf, func(i *integration.Integration) string { return i.ID.String() })
}

// Discard marks the payment integration deleted with its provider
// customers.
func (r integrationRepo) Discard(ctx context.Context, org core.OrganizationID, id core.IntegrationID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.DiscardIntegration(ctx, db.DiscardIntegrationParams{Now: utc(at), OrganizationID: org.UUID(), ID: id.UUID()})
		return err
	})
	return updateErr("integration", id, n, err)
}

// Delete removes the integration; its customers, mappings, items and
// resources go with it (ON DELETE CASCADE).
func (r integrationRepo) Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.DeleteIntegration(ctx, db.DeleteIntegrationParams{OrganizationID: org.UUID(), ID: id.UUID()})
		return err
	})
	return updateErr("integration", id, n, err)
}

// integrationCustomerRepo implements integration.CustomerRepository.
type integrationCustomerRepo struct{ t *txState }

var _ integration.CustomerRepository = integrationCustomerRepo{}

func integrationCustomerParams(c *integration.IntegrationCustomer) db.CreateIntegrationCustomerParams {
	return db.CreateIntegrationCustomerParams{
		OrganizationID:     c.OrganizationID.UUID(),
		ID:                 c.ID.UUID(),
		IntegrationID:      c.IntegrationID.UUID(),
		CustomerID:         c.CustomerID.UUID(),
		Category:           string(c.Category),
		ExternalCustomerID: c.ExternalCustomerID,
		SubsidiaryID:       c.SubsidiaryID,
		SyncWithProvider:   c.SyncWithProvider,
		IsDefault:          c.IsDefault,
		Settings:           rawJSON(c.Settings),
		CreatedAt:          utc(c.CreatedAt),
		UpdatedAt:          utc(c.UpdatedAt),
	}
}

func integrationCustomerOf(r db.IntegrationCustomer) (*integration.IntegrationCustomer, error) {
	return &integration.IntegrationCustomer{
		ID:                 idOf[core.IntegrationCustomerID](r.ID),
		OrganizationID:     idOf[core.OrganizationID](r.OrganizationID),
		IntegrationID:      idOf[core.IntegrationID](r.IntegrationID),
		CustomerID:         idOf[core.CustomerID](r.CustomerID),
		Category:           integration.Category(r.Category),
		ExternalCustomerID: r.ExternalCustomerID,
		SubsidiaryID:       r.SubsidiaryID,
		SyncWithProvider:   r.SyncWithProvider,
		IsDefault:          r.IsDefault,
		Settings:           rawJSON(r.Settings),
		CreatedAt:          utc(r.CreatedAt),
		UpdatedAt:          utc(r.UpdatedAt),
	}, nil
}

func (r integrationCustomerRepo) Create(ctx context.Context, c *integration.IntegrationCustomer) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateIntegrationCustomer(ctx, integrationCustomerParams(c))
	return insertErr("integration_customer", n, err)
}

func (r integrationCustomerRepo) Update(ctx context.Context, c *integration.IntegrationCustomer) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateIntegrationCustomer(ctx, db.UpdateIntegrationCustomerParams(integrationCustomerParams(c)))
		return err
	})
	return updateErr("integration_customer", c.ID, n, err)
}

func (r integrationCustomerRepo) Get(ctx context.Context, org core.OrganizationID, id core.IntegrationCustomerID) (*integration.IntegrationCustomer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetIntegrationCustomer(ctx, db.GetIntegrationCustomerParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("integration_customer", id, err)
	}
	return integrationCustomerOf(row)
}

func (r integrationCustomerRepo) ListByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID, c integration.Category) ([]*integration.IntegrationCustomer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListIntegrationCustomersByCustomer(ctx, db.ListIntegrationCustomersByCustomerParams{
		OrganizationID: org.UUID(), CustomerID: customer.UUID(), Category: integrationOptEnum(c),
	})
	if err != nil {
		return nil, fmt.Errorf("list integration customers: %w", mapErr(err))
	}
	return convAll(rows, integrationCustomerOf)
}

func (r integrationCustomerRepo) GetByExternalID(ctx context.Context, org core.OrganizationID, integrationID core.IntegrationID, externalID string) (*integration.IntegrationCustomer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetIntegrationCustomerByExternalID(ctx, db.GetIntegrationCustomerByExternalIDParams{
		OrganizationID: org.UUID(), IntegrationID: integrationID.UUID(), ExternalCustomerID: externalID,
	})
	if err != nil {
		return nil, getErr("integration_customer", externalID, err)
	}
	return integrationCustomerOf(row)
}

func (r integrationCustomerRepo) Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationCustomerID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteIntegrationCustomer(ctx, db.DeleteIntegrationCustomerParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("integration_customer", id, n, err)
}

// integrationMappingRepo implements integration.MappingRepository.
type integrationMappingRepo struct{ t *txState }

var _ integration.MappingRepository = integrationMappingRepo{}

func integrationMappingParams(m *integration.Mapping) db.CreateIntegrationMappingParams {
	return db.CreateIntegrationMappingParams{
		OrganizationID:      m.OrganizationID.UUID(),
		ID:                  m.ID.UUID(),
		IntegrationID:       m.IntegrationID.UUID(),
		MappableType:        string(m.MappableType),
		MappableID:          m.MappableID,
		BillingEntityID:     optUUID(m.BillingEntityID),
		ExternalID:          m.ExternalID,
		ExternalAccountCode: m.ExternalAccountCode,
		ExternalName:        m.ExternalName,
		CreatedAt:           utc(m.CreatedAt),
		UpdatedAt:           utc(m.UpdatedAt),
	}
}

func integrationMappingOf(r db.IntegrationMapping) (*integration.Mapping, error) {
	return &integration.Mapping{
		ID:                  idOf[core.IntegrationMappingID](r.ID),
		OrganizationID:      idOf[core.OrganizationID](r.OrganizationID),
		IntegrationID:       idOf[core.IntegrationID](r.IntegrationID),
		MappableType:        integration.MappableType(r.MappableType),
		MappableID:          r.MappableID,
		BillingEntityID:     optIDOf[core.BillingEntityID](r.BillingEntityID),
		ExternalID:          r.ExternalID,
		ExternalAccountCode: r.ExternalAccountCode,
		ExternalName:        r.ExternalName,
		CreatedAt:           utc(r.CreatedAt),
		UpdatedAt:           utc(r.UpdatedAt),
	}, nil
}

func (r integrationMappingRepo) Create(ctx context.Context, m *integration.Mapping) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateIntegrationMapping(ctx, integrationMappingParams(m))
	return insertErr("integration_mapping", n, err)
}

func (r integrationMappingRepo) Update(ctx context.Context, m *integration.Mapping) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateIntegrationMapping(ctx, db.UpdateIntegrationMappingParams(integrationMappingParams(m)))
		return err
	})
	return updateErr("integration_mapping", m.ID, n, err)
}

func (r integrationMappingRepo) Get(ctx context.Context, org core.OrganizationID, id core.IntegrationMappingID) (*integration.Mapping, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetIntegrationMapping(ctx, db.GetIntegrationMappingParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("integration_mapping", id, err)
	}
	return integrationMappingOf(row)
}

func (r integrationMappingRepo) Find(ctx context.Context, org core.OrganizationID, integrationID core.IntegrationID, t integration.MappableType, mappableID uuid.UUID, be core.BillingEntityID) (*integration.Mapping, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.FindIntegrationMapping(ctx, db.FindIntegrationMappingParams{
		OrganizationID: org.UUID(), IntegrationID: integrationID.UUID(),
		MappableType: string(t), MappableID: mappableID, BillingEntityID: optUUID(be),
	})
	if err != nil {
		return nil, getErr("integration_mapping", mappableID, err)
	}
	return integrationMappingOf(row)
}

func (r integrationMappingRepo) ListByIntegration(ctx context.Context, org core.OrganizationID, integrationID core.IntegrationID) ([]*integration.Mapping, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListIntegrationMappings(ctx, db.ListIntegrationMappingsParams{OrganizationID: org.UUID(), IntegrationID: integrationID.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list integration mappings: %w", mapErr(err))
	}
	return convAll(rows, integrationMappingOf)
}

func (r integrationMappingRepo) Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationMappingID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteIntegrationMapping(ctx, db.DeleteIntegrationMappingParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("integration_mapping", id, n, err)
}

// integrationCollectionMappingRepo implements
// integration.CollectionMappingRepository.
type integrationCollectionMappingRepo struct{ t *txState }

var _ integration.CollectionMappingRepository = integrationCollectionMappingRepo{}

func integrationCollectionMappingParams(m *integration.CollectionMapping) (db.CreateIntegrationCollectionMappingParams, error) {
	var currencies []byte
	if len(m.Currencies) > 0 {
		b, err := encodeJSON(m.Currencies)
		if err != nil {
			return db.CreateIntegrationCollectionMappingParams{}, fmt.Errorf("integration_collection_mapping %s: %w", m.ID, err)
		}
		currencies = b
	}
	return db.CreateIntegrationCollectionMappingParams{
		OrganizationID:      m.OrganizationID.UUID(),
		ID:                  m.ID.UUID(),
		IntegrationID:       m.IntegrationID.UUID(),
		MappingType:         string(m.Type),
		BillingEntityID:     optUUID(m.BillingEntityID),
		ExternalID:          m.ExternalID,
		ExternalAccountCode: m.ExternalAccountCode,
		ExternalName:        m.ExternalName,
		TaxNexus:            m.TaxNexus,
		TaxType:             m.TaxType,
		TaxCode:             m.TaxCode,
		Currencies:          currencies,
		CreatedAt:           utc(m.CreatedAt),
		UpdatedAt:           utc(m.UpdatedAt),
	}, nil
}

func integrationCollectionMappingOf(r db.IntegrationCollectionMapping) (*integration.CollectionMapping, error) {
	var currencies map[string]string
	if err := decodeJSON(r.Currencies, &currencies); err != nil {
		return nil, fmt.Errorf("integration_collection_mapping %s: currencies: %w", r.ID, err)
	}
	if len(currencies) == 0 {
		currencies = nil
	}
	return &integration.CollectionMapping{
		ID:                  idOf[core.IntegrationCollectionMappingID](r.ID),
		OrganizationID:      idOf[core.OrganizationID](r.OrganizationID),
		IntegrationID:       idOf[core.IntegrationID](r.IntegrationID),
		Type:                integration.CollectionMappingType(r.MappingType),
		BillingEntityID:     optIDOf[core.BillingEntityID](r.BillingEntityID),
		ExternalID:          r.ExternalID,
		ExternalAccountCode: r.ExternalAccountCode,
		ExternalName:        r.ExternalName,
		TaxNexus:            r.TaxNexus,
		TaxType:             r.TaxType,
		TaxCode:             r.TaxCode,
		Currencies:          currencies,
		CreatedAt:           utc(r.CreatedAt),
		UpdatedAt:           utc(r.UpdatedAt),
	}, nil
}

func (r integrationCollectionMappingRepo) Create(ctx context.Context, m *integration.CollectionMapping) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	p, err := integrationCollectionMappingParams(m)
	if err != nil {
		return err
	}
	n, err := q.CreateIntegrationCollectionMapping(ctx, p)
	return insertErr("integration_collection_mapping", n, err)
}

func (r integrationCollectionMappingRepo) Update(ctx context.Context, m *integration.CollectionMapping) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	p, err := integrationCollectionMappingParams(m)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateIntegrationCollectionMapping(ctx, db.UpdateIntegrationCollectionMappingParams(p))
		return err
	})
	return updateErr("integration_collection_mapping", m.ID, n, err)
}

func (r integrationCollectionMappingRepo) Get(ctx context.Context, org core.OrganizationID, id core.IntegrationCollectionMappingID) (*integration.CollectionMapping, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetIntegrationCollectionMapping(ctx, db.GetIntegrationCollectionMappingParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("integration_collection_mapping", id, err)
	}
	return integrationCollectionMappingOf(row)
}

func (r integrationCollectionMappingRepo) ListByType(ctx context.Context, org core.OrganizationID, integrationID core.IntegrationID, t integration.CollectionMappingType) ([]*integration.CollectionMapping, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListIntegrationCollectionMappings(ctx, db.ListIntegrationCollectionMappingsParams{
		OrganizationID: org.UUID(), IntegrationID: integrationID.UUID(), MappingType: integrationOptEnum(t),
	})
	if err != nil {
		return nil, fmt.Errorf("list integration collection mappings: %w", mapErr(err))
	}
	return convAll(rows, integrationCollectionMappingOf)
}

func (r integrationCollectionMappingRepo) Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationCollectionMappingID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteIntegrationCollectionMapping(ctx, db.DeleteIntegrationCollectionMappingParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("integration_collection_mapping", id, n, err)
}

// integrationItemRepo implements integration.ItemRepository.
type integrationItemRepo struct{ t *txState }

var _ integration.ItemRepository = integrationItemRepo{}

func integrationItemOf(r db.IntegrationItem) (*integration.Item, error) {
	return &integration.Item{
		ID:                  idOf[core.IntegrationItemID](r.ID),
		OrganizationID:      idOf[core.OrganizationID](r.OrganizationID),
		IntegrationID:       idOf[core.IntegrationID](r.IntegrationID),
		ItemType:            integration.ItemType(r.ItemType),
		ExternalID:          r.ExternalID,
		ExternalAccountCode: r.ExternalAccountCode,
		ExternalName:        r.ExternalName,
		CreatedAt:           utc(r.CreatedAt),
		UpdatedAt:           utc(r.UpdatedAt),
	}, nil
}

// Upsert inserts i, or updates the account code, name and update instant
// of the item cached under the same (integration, item type, external
// id), which keeps its id and creation instant. An id already used by an
// item of another key fails with core.ErrConflict.
func (r integrationItemRepo) Upsert(ctx context.Context, i *integration.Item) error {
	q, err := r.t.queries(ctx, i.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpsertIntegrationItem(ctx, db.UpsertIntegrationItemParams{
			OrganizationID:      i.OrganizationID.UUID(),
			ID:                  i.ID.UUID(),
			IntegrationID:       i.IntegrationID.UUID(),
			ItemType:            string(i.ItemType),
			ExternalID:          i.ExternalID,
			ExternalAccountCode: i.ExternalAccountCode,
			ExternalName:        i.ExternalName,
			CreatedAt:           utc(i.CreatedAt),
			UpdatedAt:           utc(i.UpdatedAt),
		})
		return err
	})
	return updateErr("integration_item", i.ID, n, err)
}

func (r integrationItemRepo) List(ctx context.Context, org core.OrganizationID, integrationID core.IntegrationID, f integration.ItemFilter, page core.PageRequest) (core.Page[*integration.Item], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*integration.Item]{}, err
	}
	after, afterName, afterAt, lim, err := itemCursorArgs(page)
	if err != nil {
		return core.Page[*integration.Item]{}, err
	}
	var search *string
	if f.Search != "" {
		s := likePattern(f.Search)
		search = &s
	}
	rows, err := q.ListIntegrationItems(ctx, db.ListIntegrationItemsParams{
		OrganizationID: org.UUID(), IntegrationID: integrationID.UUID(),
		ItemType: integrationOptEnum(f.ItemType), Search: search,
		After: after, AfterName: afterName, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*integration.Item]{}, fmt.Errorf("list integration items: %w", mapErr(err))
	}
	return pageOf(rows, page, integrationItemOf, func(i *integration.Item) string {
		return itemToken(i.ExternalName, i.CreatedAt, i.ID.UUID())
	})
}

// itemCursorArgs returns the cursor and row limit of the items list, by
// external name (NULLs last), then by creation instant, newest first,
// then by ID (see [itemToken]); afterName is nil after an unnamed item.
func itemCursorArgs(p core.PageRequest) (after *uuid.UUID, afterName *string, afterAt *time.Time, lim int32, err error) {
	lim = int32(p.Limit()) + 1 // Limit is at most core.MaxPageSize
	if p.Token == "" {
		return nil, nil, nil, lim, nil
	}
	c, err := core.DecodeCursor(p.Token)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	at, name, ok := strings.Cut(c.Key, " ")
	t, perr := time.Parse(time.RFC3339Nano, at)
	if !ok || perr != nil || name == "" || (name[0] != '=' && name != "!") {
		return nil, nil, nil, 0, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	if name[0] == '=' {
		n := name[1:]
		afterName = &n
	}
	return &c.ID, afterName, &t, lim, nil
}

// itemToken returns the page token of the items list after the item with
// that name, creation instant and ID; its key is the instant (RFC 3339),
// a space, then "=" and the name, or "!" for no name.
func itemToken(name *string, at time.Time, id uuid.UUID) string {
	n := "!"
	if name != nil {
		n = "=" + *name
	}
	return core.Cursor{Key: at.UTC().Format(time.RFC3339Nano) + " " + n, ID: id}.Encode()
}

func (r integrationItemRepo) DeleteByType(ctx context.Context, org core.OrganizationID, integrationID core.IntegrationID, t integration.ItemType) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.DeleteIntegrationItemsByType(ctx, db.DeleteIntegrationItemsByTypeParams{
		OrganizationID: org.UUID(), IntegrationID: integrationID.UUID(), ItemType: string(t),
	})
	if err != nil {
		return 0, fmt.Errorf("delete integration items: %w", mapErr(err))
	}
	return int(n), nil
}

// integrationResourceRepo implements integration.ResourceRepository.
type integrationResourceRepo struct{ t *txState }

var _ integration.ResourceRepository = integrationResourceRepo{}

func integrationResourceOf(r db.IntegrationResource) (*integration.Resource, error) {
	return &integration.Resource{
		ID:             idOf[core.IntegrationResourceID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		IntegrationID:  idOf[core.IntegrationID](r.IntegrationID),
		ResourceType:   integration.ResourceType(r.ResourceType),
		SyncableID:     r.SyncableID,
		ExternalID:     r.ExternalID,
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r integrationResourceRepo) Create(ctx context.Context, res *integration.Resource) error {
	q, err := r.t.queries(ctx, res.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateIntegrationResource(ctx, db.CreateIntegrationResourceParams{
		OrganizationID: res.OrganizationID.UUID(),
		ID:             res.ID.UUID(),
		IntegrationID:  res.IntegrationID.UUID(),
		ResourceType:   string(res.ResourceType),
		SyncableID:     res.SyncableID,
		ExternalID:     res.ExternalID,
		CreatedAt:      utc(res.CreatedAt),
		UpdatedAt:      utc(res.UpdatedAt),
	})
	return insertErr("integration_resource", n, err)
}

func (r integrationResourceRepo) Find(ctx context.Context, org core.OrganizationID, integrationID core.IntegrationID, t integration.ResourceType, syncableID uuid.UUID) (*integration.Resource, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.FindIntegrationResource(ctx, db.FindIntegrationResourceParams{
		OrganizationID: org.UUID(), IntegrationID: integrationID.UUID(), ResourceType: string(t), SyncableID: syncableID,
	})
	if err != nil {
		return nil, getErr("integration_resource", syncableID, err)
	}
	return integrationResourceOf(row)
}

func (r integrationResourceRepo) Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationResourceID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteIntegrationResource(ctx, db.DeleteIntegrationResourceParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("integration_resource", id, n, err)
}

func (r integrationResourceRepo) ExistsForSyncable(ctx context.Context, org core.OrganizationID, syncableID uuid.UUID) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	ok, err := q.IntegrationResourceExistsForSyncable(ctx, db.IntegrationResourceExistsForSyncableParams{OrganizationID: org.UUID(), SyncableID: syncableID})
	if err != nil {
		return false, fmt.Errorf("find integration resources of %s: %w", syncableID, mapErr(err))
	}
	return ok, nil
}
