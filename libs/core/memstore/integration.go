package memstore

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// integrationData holds the tables of package integration.
type integrationData struct {
	integrations       *table[core.IntegrationID, integration.Integration]
	customers          *table[core.IntegrationCustomerID, integration.IntegrationCustomer]
	mappings           *table[core.IntegrationMappingID, integration.Mapping]
	collectionMappings *table[core.IntegrationCollectionMappingID, integration.CollectionMapping]
	items              *table[core.IntegrationItemID, integration.Item]
	resources          *table[core.IntegrationResourceID, integration.Resource]
}

func newIntegrationData() *integrationData {
	return &integrationData{
		integrations: newTable[core.IntegrationID](func(i integration.Integration) integration.Integration {
			i.Settings = slices.Clone(i.Settings)
			i.Secrets = slices.Clone(i.Secrets)
			return i
		}),
		customers: newTable[core.IntegrationCustomerID](func(c integration.IntegrationCustomer) integration.IntegrationCustomer {
			c.Settings = slices.Clone(c.Settings)
			return c
		}),
		mappings: newTable[core.IntegrationMappingID, integration.Mapping](nil),
		collectionMappings: newTable[core.IntegrationCollectionMappingID](func(m integration.CollectionMapping) integration.CollectionMapping {
			m.Currencies = maps.Clone(m.Currencies)
			return m
		}),
		items:     newTable[core.IntegrationItemID, integration.Item](nil),
		resources: newTable[core.IntegrationResourceID, integration.Resource](nil),
	}
}

func (d *integrationData) snapshot() *integrationData {
	return &integrationData{
		integrations:       d.integrations.snapshot(),
		customers:          d.customers.snapshot(),
		mappings:           d.mappings.snapshot(),
		collectionMappings: d.collectionMappings.snapshot(),
		items:              d.items.snapshot(),
		resources:          d.resources.snapshot(),
	}
}

// integrationRepo implements integration.Repository.
type integrationRepo struct{ d *integrationData }

func (r integrationRepo) check(i *integration.Integration) error {
	if r.d.integrations.any(func(x *integration.Integration) bool {
		return x.ID != i.ID && x.OrganizationID == i.OrganizationID && x.Code == i.Code
	}) {
		return conflict("integration", "code")
	}
	if integration.DefaultConflict(i, r.d.integrations.find(func(x *integration.Integration) bool {
		return x.OrganizationID == i.OrganizationID
	})) {
		return conflict("integration", "is_default")
	}
	return nil
}

func (r integrationRepo) Create(_ context.Context, i *integration.Integration) error {
	if r.d.integrations.has(i.ID) {
		return conflict("integration", "id")
	}
	if err := r.check(i); err != nil {
		return err
	}
	r.d.integrations.put(i.ID, *i)
	return nil
}

func (r integrationRepo) Update(_ context.Context, i *integration.Integration) error {
	cur, ok := r.d.integrations.get(i.ID)
	if !ok || cur.OrganizationID != i.OrganizationID {
		return notFound("integration", i.ID)
	}
	if err := r.check(i); err != nil {
		return err
	}
	r.d.integrations.put(i.ID, *i)
	return nil
}

func (r integrationRepo) Get(_ context.Context, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, error) {
	i, ok := r.d.integrations.get(id)
	if !ok || i.OrganizationID != org {
		return nil, notFound("integration", id)
	}
	return i, nil
}

func (r integrationRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*integration.Integration, error) {
	i, ok := r.d.integrations.first(func(x *integration.Integration) bool {
		return x.OrganizationID == org && x.Code == code
	})
	if !ok {
		return nil, notFound("integration", stringer(code))
	}
	return i, nil
}

func (r integrationRepo) GetDefault(_ context.Context, org core.OrganizationID, c integration.Category) (*integration.Integration, error) {
	i, ok := r.d.integrations.first(func(x *integration.Integration) bool {
		return x.OrganizationID == org && x.Category == c && x.IsDefault
	})
	if !ok {
		return nil, notFound("integration", stringer("default "+string(c)))
	}
	return i, nil
}

func (r integrationRepo) ListByProvider(_ context.Context, org core.OrganizationID, p integration.Provider) ([]*integration.Integration, error) {
	return r.d.integrations.find(func(x *integration.Integration) bool {
		return x.OrganizationID == org && x.Provider == p
	}), nil
}

func (r integrationRepo) ListByDomain(_ context.Context, p integration.Provider, domain string) ([]*integration.Integration, error) {
	return r.d.integrations.find(func(x *integration.Integration) bool {
		return x.Category == integration.CategoryAuth && x.Provider == p && x.ClaimsDomain(domain)
	}), nil
}

func (r integrationRepo) List(_ context.Context, org core.OrganizationID, f integration.Filter, page core.PageRequest) (core.Page[*integration.Integration], error) {
	rows := r.d.integrations.find(func(x *integration.Integration) bool {
		return x.OrganizationID == org && (f.Category == "" || x.Category == f.Category) && (f.Provider == "" || x.Provider == f.Provider)
	})
	return paginate(rows, func(x *integration.Integration) core.IntegrationID { return x.ID }, page)
}

func (r integrationRepo) Delete(_ context.Context, org core.OrganizationID, id core.IntegrationID) error {
	i, ok := r.d.integrations.get(id)
	if !ok || i.OrganizationID != org {
		return notFound("integration", id)
	}
	r.d.integrations.del(id)
	r.d.customers.deleteWhere(func(x *integration.IntegrationCustomer) bool { return x.IntegrationID == id })
	r.d.mappings.deleteWhere(func(x *integration.Mapping) bool { return x.IntegrationID == id })
	r.d.collectionMappings.deleteWhere(func(x *integration.CollectionMapping) bool { return x.IntegrationID == id })
	r.d.items.deleteWhere(func(x *integration.Item) bool { return x.IntegrationID == id })
	r.d.resources.deleteWhere(func(x *integration.Resource) bool { return x.IntegrationID == id })
	return nil
}

// integrationCustomerRepo implements integration.CustomerRepository.
type integrationCustomerRepo struct{ d *integrationData }

func (r integrationCustomerRepo) check(c *integration.IntegrationCustomer) error {
	if c.ExternalCustomerID != "" && r.d.customers.any(func(x *integration.IntegrationCustomer) bool {
		return x.ID != c.ID && x.IntegrationID == c.IntegrationID && x.ExternalCustomerID == c.ExternalCustomerID
	}) {
		return conflict("integration_customer", "external_customer_id")
	}
	existing := r.d.customers.find(func(x *integration.IntegrationCustomer) bool {
		return x.OrganizationID == c.OrganizationID && x.CustomerID == c.CustomerID
	})
	if err := integration.CheckCustomerLinks(c, existing); err != nil {
		return conflict("integration_customer", err.Error())
	}
	return nil
}

func (r integrationCustomerRepo) Create(_ context.Context, c *integration.IntegrationCustomer) error {
	if r.d.customers.has(c.ID) {
		return conflict("integration_customer", "id")
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.customers.put(c.ID, *c)
	return nil
}

func (r integrationCustomerRepo) Update(_ context.Context, c *integration.IntegrationCustomer) error {
	cur, ok := r.d.customers.get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID {
		return notFound("integration_customer", c.ID)
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.customers.put(c.ID, *c)
	return nil
}

func (r integrationCustomerRepo) Get(_ context.Context, org core.OrganizationID, id core.IntegrationCustomerID) (*integration.IntegrationCustomer, error) {
	c, ok := r.d.customers.get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("integration_customer", id)
	}
	return c, nil
}

func (r integrationCustomerRepo) ListByCustomer(_ context.Context, org core.OrganizationID, customer core.CustomerID, c integration.Category) ([]*integration.IntegrationCustomer, error) {
	return r.d.customers.find(func(x *integration.IntegrationCustomer) bool {
		return x.OrganizationID == org && x.CustomerID == customer && (c == "" || x.Category == c)
	}), nil
}

func (r integrationCustomerRepo) GetByExternalID(_ context.Context, org core.OrganizationID, integrationID core.IntegrationID, externalID string) (*integration.IntegrationCustomer, error) {
	c, ok := r.d.customers.first(func(x *integration.IntegrationCustomer) bool {
		return x.OrganizationID == org && x.IntegrationID == integrationID && x.ExternalCustomerID == externalID
	})
	if !ok {
		return nil, notFound("integration_customer", stringer(externalID))
	}
	return c, nil
}

func (r integrationCustomerRepo) Delete(_ context.Context, org core.OrganizationID, id core.IntegrationCustomerID) error {
	c, ok := r.d.customers.get(id)
	if !ok || c.OrganizationID != org {
		return notFound("integration_customer", id)
	}
	r.d.customers.del(id)
	return nil
}

// integrationMappingRepo implements integration.MappingRepository.
type integrationMappingRepo struct{ d *integrationData }

func (r integrationMappingRepo) Create(_ context.Context, m *integration.Mapping) error {
	if r.d.mappings.has(m.ID) {
		return conflict("integration_mapping", "id")
	}
	if r.d.mappings.any(func(x *integration.Mapping) bool { return x.SameKey(m) }) {
		return conflict("integration_mapping", "mappable")
	}
	r.d.mappings.put(m.ID, *m)
	return nil
}

func (r integrationMappingRepo) Update(_ context.Context, m *integration.Mapping) error {
	cur, ok := r.d.mappings.get(m.ID)
	if !ok || cur.OrganizationID != m.OrganizationID {
		return notFound("integration_mapping", m.ID)
	}
	if r.d.mappings.any(func(x *integration.Mapping) bool { return x.ID != m.ID && x.SameKey(m) }) {
		return conflict("integration_mapping", "mappable")
	}
	r.d.mappings.put(m.ID, *m)
	return nil
}

func (r integrationMappingRepo) Get(_ context.Context, org core.OrganizationID, id core.IntegrationMappingID) (*integration.Mapping, error) {
	m, ok := r.d.mappings.get(id)
	if !ok || m.OrganizationID != org {
		return nil, notFound("integration_mapping", id)
	}
	return m, nil
}

func (r integrationMappingRepo) Find(_ context.Context, org core.OrganizationID, integrationID core.IntegrationID, t integration.MappableType, mappableID uuid.UUID, be core.BillingEntityID) (*integration.Mapping, error) {
	m, ok := r.d.mappings.first(func(x *integration.Mapping) bool {
		return x.OrganizationID == org && x.IntegrationID == integrationID && x.MappableType == t && x.MappableID == mappableID && x.BillingEntityID == be
	})
	if !ok {
		return nil, notFound("integration_mapping", mappableID)
	}
	return m, nil
}

func (r integrationMappingRepo) ListByIntegration(_ context.Context, org core.OrganizationID, integrationID core.IntegrationID) ([]*integration.Mapping, error) {
	return r.d.mappings.find(func(x *integration.Mapping) bool {
		return x.OrganizationID == org && x.IntegrationID == integrationID
	}), nil
}

func (r integrationMappingRepo) Delete(_ context.Context, org core.OrganizationID, id core.IntegrationMappingID) error {
	m, ok := r.d.mappings.get(id)
	if !ok || m.OrganizationID != org {
		return notFound("integration_mapping", id)
	}
	r.d.mappings.del(id)
	return nil
}

// integrationCollectionMappingRepo implements
// integration.CollectionMappingRepository.
type integrationCollectionMappingRepo struct{ d *integrationData }

func (r integrationCollectionMappingRepo) Create(_ context.Context, m *integration.CollectionMapping) error {
	if r.d.collectionMappings.has(m.ID) {
		return conflict("integration_collection_mapping", "id")
	}
	if r.d.collectionMappings.any(func(x *integration.CollectionMapping) bool { return x.SameKey(m) }) {
		return conflict("integration_collection_mapping", "mapping_type")
	}
	r.d.collectionMappings.put(m.ID, *m)
	return nil
}

func (r integrationCollectionMappingRepo) Update(_ context.Context, m *integration.CollectionMapping) error {
	cur, ok := r.d.collectionMappings.get(m.ID)
	if !ok || cur.OrganizationID != m.OrganizationID {
		return notFound("integration_collection_mapping", m.ID)
	}
	if r.d.collectionMappings.any(func(x *integration.CollectionMapping) bool { return x.ID != m.ID && x.SameKey(m) }) {
		return conflict("integration_collection_mapping", "mapping_type")
	}
	r.d.collectionMappings.put(m.ID, *m)
	return nil
}

func (r integrationCollectionMappingRepo) Get(_ context.Context, org core.OrganizationID, id core.IntegrationCollectionMappingID) (*integration.CollectionMapping, error) {
	m, ok := r.d.collectionMappings.get(id)
	if !ok || m.OrganizationID != org {
		return nil, notFound("integration_collection_mapping", id)
	}
	return m, nil
}

func (r integrationCollectionMappingRepo) ListByType(_ context.Context, org core.OrganizationID, integrationID core.IntegrationID, t integration.CollectionMappingType) ([]*integration.CollectionMapping, error) {
	return r.d.collectionMappings.find(func(x *integration.CollectionMapping) bool {
		return x.OrganizationID == org && x.IntegrationID == integrationID && (t == "" || x.Type == t)
	}), nil
}

func (r integrationCollectionMappingRepo) Delete(_ context.Context, org core.OrganizationID, id core.IntegrationCollectionMappingID) error {
	m, ok := r.d.collectionMappings.get(id)
	if !ok || m.OrganizationID != org {
		return notFound("integration_collection_mapping", id)
	}
	r.d.collectionMappings.del(id)
	return nil
}

// integrationItemRepo implements integration.ItemRepository.
type integrationItemRepo struct{ d *integrationData }

func (r integrationItemRepo) Upsert(_ context.Context, i *integration.Item) error {
	if cur, ok := r.d.items.first(func(x *integration.Item) bool {
		return x.OrganizationID == i.OrganizationID && x.IntegrationID == i.IntegrationID && x.ItemType == i.ItemType && x.ExternalID == i.ExternalID
	}); ok && cur.ID != i.ID {
		r.d.items.del(cur.ID)
		c := *i
		c.ID, c.CreatedAt = cur.ID, cur.CreatedAt
		r.d.items.put(c.ID, c)
		return nil
	}
	if cur, ok := r.d.items.get(i.ID); ok && cur.OrganizationID != i.OrganizationID {
		return conflict("integration_item", "id")
	}
	r.d.items.put(i.ID, *i)
	return nil
}

func (r integrationItemRepo) List(_ context.Context, org core.OrganizationID, integrationID core.IntegrationID, f integration.ItemFilter, page core.PageRequest) (core.Page[*integration.Item], error) {
	q := strings.ToLower(f.Search)
	rows := r.d.items.find(func(x *integration.Item) bool {
		if x.OrganizationID != org || x.IntegrationID != integrationID || (f.ItemType != "" && x.ItemType != f.ItemType) {
			return false
		}
		return q == "" || strings.Contains(strings.ToLower(x.ExternalID), q) ||
			(x.ExternalName != nil && strings.Contains(strings.ToLower(*x.ExternalName), q))
	})
	return paginate(rows, func(x *integration.Item) core.IntegrationItemID { return x.ID }, page)
}

func (r integrationItemRepo) DeleteByType(_ context.Context, org core.OrganizationID, integrationID core.IntegrationID, t integration.ItemType) (int, error) {
	return r.d.items.deleteWhere(func(x *integration.Item) bool {
		return x.OrganizationID == org && x.IntegrationID == integrationID && x.ItemType == t
	}), nil
}

// integrationResourceRepo implements integration.ResourceRepository.
type integrationResourceRepo struct{ d *integrationData }

func (r integrationResourceRepo) Create(_ context.Context, res *integration.Resource) error {
	if r.d.resources.has(res.ID) {
		return conflict("integration_resource", "id")
	}
	if r.d.resources.any(func(x *integration.Resource) bool {
		return x.OrganizationID == res.OrganizationID && x.IntegrationID == res.IntegrationID && x.ResourceType == res.ResourceType && x.SyncableID == res.SyncableID
	}) {
		return conflict("integration_resource", "syncable")
	}
	r.d.resources.put(res.ID, *res)
	return nil
}

func (r integrationResourceRepo) Find(_ context.Context, org core.OrganizationID, integrationID core.IntegrationID, t integration.ResourceType, syncableID uuid.UUID) (*integration.Resource, error) {
	res, ok := r.d.resources.first(func(x *integration.Resource) bool {
		return x.OrganizationID == org && x.IntegrationID == integrationID && x.ResourceType == t && x.SyncableID == syncableID
	})
	if !ok {
		return nil, notFound("integration_resource", syncableID)
	}
	return res, nil
}

func (r integrationResourceRepo) Delete(_ context.Context, org core.OrganizationID, id core.IntegrationResourceID) error {
	res, ok := r.d.resources.get(id)
	if !ok || res.OrganizationID != org {
		return notFound("integration_resource", id)
	}
	r.d.resources.del(id)
	return nil
}

var (
	_ integration.Repository                  = integrationRepo{}
	_ integration.CustomerRepository          = integrationCustomerRepo{}
	_ integration.MappingRepository           = integrationMappingRepo{}
	_ integration.CollectionMappingRepository = integrationCollectionMappingRepo{}
	_ integration.ItemRepository              = integrationItemRepo{}
	_ integration.ResourceRepository          = integrationResourceRepo{}
)
