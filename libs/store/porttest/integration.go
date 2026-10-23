package porttest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// integrationFixture returns an integration of org with the category of
// provider p, not stored.
func integrationFixture(e *env, org core.OrganizationID, code string, p integration.Provider) *integration.Integration {
	c, _ := integration.CategoryOf(p)
	return &integration.Integration{
		ID: core.NewID[core.IntegrationID](e.ids), OrganizationID: org, Code: code, Name: "Integration " + code,
		Category: c, Provider: p, CreatedAt: now, UpdatedAt: now,
	}
}

// integrationFixtureCreate stores integrations in one transaction.
func integrationFixtureCreate(e *env, is ...*integration.Integration) {
	e.t.Helper()
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, i := range is {
			if err := r.Integrations.Create(ctx, i); err != nil {
				return err
			}
		}
		return nil
	})
}

// integrationFixtureCustomers stores a customer of be's organization per
// external id and returns their ids. The test is skipped while the
// customer repository is not bound.
func integrationFixtureCustomers(e *env, org core.OrganizationID, be core.BillingEntityID, externalIDs ...string) []core.CustomerID {
	e.t.Helper()
	var bound bool
	e.tx(func(_ context.Context, r port.Repos) error {
		bound = r.Customers != nil
		return nil
	})
	if !bound {
		e.t.Skip("customer repository not bound")
	}
	out := make([]core.CustomerID, len(externalIDs))
	e.tx(func(ctx context.Context, r port.Repos) error {
		for i, x := range externalIDs {
			c := customer.New(org, core.NewID[core.CustomerID](e.ids), x, be, now)
			if err := r.Customers.Create(ctx, c); err != nil {
				return err
			}
			out[i] = c.ID
		}
		return nil
	})
	return out
}

func integrationID(i *integration.Integration) core.IntegrationID { return i.ID }

func integrationItemID(i *integration.Item) core.IntegrationItemID { return i.ID }

func testIntegration(t *testing.T, h Harness) {
	t.Run("integrations", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		ns := integrationFixture(e, org, "netsuite", integration.ProviderNetsuite)
		ns.IsDefault = true
		ns.Settings = json.RawMessage(`{"account_id":"123","script_endpoint_url":"https://ns.test/x"}`)
		ns.Secrets = []byte{0x00, 0x01, 0xfe, 0xff}
		ns.SyncInvoices, ns.SyncCreditNotes, ns.SyncPayments = true, true, true
		xero := integrationFixture(e, org, "xero", integration.ProviderXero)
		hub := integrationFixture(e, org, "hubspot", integration.ProviderHubspot)
		hub.SyncSubscriptions = true
		ns2 := integrationFixture(e, org, "netsuite-2", integration.ProviderNetsuite)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, i := range []*integration.Integration{ns, xero, hub, ns2} {
				noErr(t, "Create()", r.Integrations.Create(ctx, i))
			}
			wantErr(t, "Create() with the same ID", r.Integrations.Create(ctx, ns), core.ErrConflict)
			got, err := r.Integrations.Get(ctx, org, ns.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, ns)
			got, err = r.Integrations.Get(ctx, org, xero.ID)
			noErr(t, "Get() without settings", err)
			equal(t, "Get() without settings", got, xero)
			if got.Settings != nil || got.Secrets != nil {
				t.Errorf("Get() settings, secrets got = %s, %v, want = nil, nil", got.Settings, got.Secrets)
			}
			got, err = r.Integrations.GetByCode(ctx, org, "hubspot")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, hub)
			_, err = r.Integrations.GetByCode(ctx, org, "nope")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)
			_, err = r.Integrations.Get(ctx, org, core.NewID[core.IntegrationID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			got, err = r.Integrations.GetDefault(ctx, org, integration.CategoryAccounting)
			noErr(t, "GetDefault()", err)
			equal(t, "GetDefault()", got, ns)
			_, err = r.Integrations.GetDefault(ctx, org, integration.CategoryCRM)
			wantErr(t, "GetDefault() without default", err, core.ErrNotFound)

			// Isolation.
			_, err = r.Integrations.Get(ctx, other, ns.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Integrations.GetByCode(ctx, other, "netsuite")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			_, err = r.Integrations.GetDefault(ctx, other, integration.CategoryAccounting)
			wantErr(t, "GetDefault() from another organization", err, core.ErrNotFound)
			moved := *ns
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.Integrations.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Delete() from another organization", r.Integrations.Delete(ctx, other, ns.ID), core.ErrNotFound)
			byProvider, err := r.Integrations.ListByProvider(ctx, other, integration.ProviderNetsuite)
			noErr(t, "ListByProvider() of another organization", err)
			equal(t, "ListByProvider() of another organization", byProvider, []*integration.Integration{})

			// The same code is free in another organization.
			noErr(t, "Create() with a code of another organization", r.Integrations.Create(ctx, integrationFixture(e, other, "netsuite", integration.ProviderNetsuite)))

			// Uniqueness: code, one default per category.
			dup := integrationFixture(e, org, "xero", integration.ProviderAnrok)
			wantErr(t, "Create() with a taken code", r.Integrations.Create(ctx, dup), core.ErrConflict)
			dup.Code = "anrok"
			dup.IsDefault = true
			noErr(t, "Create() default of another category", r.Integrations.Create(ctx, dup))
			xero.IsDefault = true
			wantErr(t, "Update() to a second default", r.Integrations.Update(ctx, xero), core.ErrConflict)
			xero.IsDefault = false
			xero.Code = "netsuite"
			wantErr(t, "Update() to a taken code", r.Integrations.Update(ctx, xero), core.ErrConflict)
			xero.Code = "xero"
			second := integrationFixture(e, org, "xero-2", integration.ProviderXero)
			second.IsDefault = true
			wantErr(t, "Create() of a second default", r.Integrations.Create(ctx, second), core.ErrConflict)

			// Update with every field changed, settings and secrets cleared.
			ns.Name = "NetSuite EU"
			ns.Settings, ns.Secrets = nil, nil
			ns.IsDefault = false
			ns.SyncPayments = false
			ns.UpdatedAt = at(time.Minute)
			noErr(t, "Update() after conflicts", r.Integrations.Update(ctx, ns))
			got, err = r.Integrations.Get(ctx, org, ns.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, ns)
			xero.IsDefault = true
			noErr(t, "Update() to the freed default", r.Integrations.Update(ctx, xero))
			missing := integrationFixture(e, org, "missing", integration.ProviderXero)
			wantErr(t, "Update() unknown", r.Integrations.Update(ctx, missing), core.ErrNotFound)

			byProvider, err = r.Integrations.ListByProvider(ctx, org, integration.ProviderNetsuite)
			noErr(t, "ListByProvider()", err)
			equal(t, "ListByProvider()", byProvider, []*integration.Integration{ns, ns2})

			all := listAll(t, 2, func(p core.PageRequest) (core.Page[*integration.Integration], error) {
				return r.Integrations.List(ctx, org, integration.Filter{}, p)
			})
			equal(t, "List()", ids(all, integrationID), []core.IntegrationID{ns.ID, xero.ID, hub.ID, ns2.ID, dup.ID})
			acc := listAll(t, 1, func(p core.PageRequest) (core.Page[*integration.Integration], error) {
				return r.Integrations.List(ctx, org, integration.Filter{Category: integration.CategoryAccounting}, p)
			})
			equal(t, "List() of a category", acc, []*integration.Integration{ns, xero, ns2})
			nsOnly := listAll(t, 5, func(p core.PageRequest) (core.Page[*integration.Integration], error) {
				return r.Integrations.List(ctx, org, integration.Filter{Category: integration.CategoryAccounting, Provider: integration.ProviderNetsuite}, p)
			})
			equal(t, "List() of a category and provider", ids(nsOnly, integrationID), []core.IntegrationID{ns.ID, ns2.ID})
			none := listAll(t, 5, func(p core.PageRequest) (core.Page[*integration.Integration], error) {
				return r.Integrations.List(ctx, org, integration.Filter{Category: integration.CategoryCRM, Provider: integration.ProviderXero}, p)
			})
			equal(t, "List() without match", none, []*integration.Integration{})

			noErr(t, "Delete()", r.Integrations.Delete(ctx, org, ns2.ID))
			wantErr(t, "Delete() again", r.Integrations.Delete(ctx, org, ns2.ID), core.ErrNotFound)
			_, err = r.Integrations.Get(ctx, org, ns2.ID)
			wantErr(t, "Get() deleted", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("integration delete cascades", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, _ := e.twoOrgs()
		org := a.OrganizationID
		ns := integrationFixture(e, org, "netsuite", integration.ProviderNetsuite)
		keep := integrationFixture(e, org, "xero", integration.ProviderXero)
		integrationFixtureCreate(e, ns, keep)
		mappable := uuid.New()
		syncable := uuid.New()
		mk := func(i *integration.Integration) (*integration.Mapping, *integration.CollectionMapping, *integration.Item, *integration.Resource) {
			return &integration.Mapping{
				ID: core.NewID[core.IntegrationMappingID](e.ids), OrganizationID: org, IntegrationID: i.ID,
				MappableType: integration.MappableTypeAddOn, MappableID: mappable, ExternalID: "item-1", CreatedAt: now, UpdatedAt: now,
			}, &integration.CollectionMapping{
				ID: core.NewID[core.IntegrationCollectionMappingID](e.ids), OrganizationID: org, IntegrationID: i.ID,
				Type: integration.CollectionMappingTypeFallbackItem, ExternalID: ptr("fallback"), CreatedAt: now, UpdatedAt: now,
			}, &integration.Item{
				ID: core.NewID[core.IntegrationItemID](e.ids), OrganizationID: org, IntegrationID: i.ID,
				ItemType: integration.ItemTypeStandard, ExternalID: "item-1", CreatedAt: now, UpdatedAt: now,
			}, &integration.Resource{
				ID: core.NewID[core.IntegrationResourceID](e.ids), OrganizationID: org, IntegrationID: i.ID,
				ResourceType: integration.ResourceTypeInvoice, SyncableID: syncable, ExternalID: "inv-1", CreatedAt: now, UpdatedAt: now,
			}
		}
		m1, c1, i1, r1 := mk(ns)
		m2, c2, i2, r2 := mk(keep)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range []struct {
				m *integration.Mapping
				c *integration.CollectionMapping
				i *integration.Item
				r *integration.Resource
			}{{m1, c1, i1, r1}, {m2, c2, i2, r2}} {
				noErr(t, "Mappings.Create()", r.IntegrationMappings.Create(ctx, x.m))
				noErr(t, "CollectionMappings.Create()", r.IntegrationCollectionMappings.Create(ctx, x.c))
				noErr(t, "Items.Upsert()", r.IntegrationItems.Upsert(ctx, x.i))
				noErr(t, "Resources.Create()", r.IntegrationResources.Create(ctx, x.r))
			}
			noErr(t, "Delete()", r.Integrations.Delete(ctx, org, ns.ID))

			_, err := r.IntegrationMappings.Get(ctx, org, m1.ID)
			wantErr(t, "Mappings.Get() of a deleted integration", err, core.ErrNotFound)
			_, err = r.IntegrationCollectionMappings.Get(ctx, org, c1.ID)
			wantErr(t, "CollectionMappings.Get() of a deleted integration", err, core.ErrNotFound)
			items, err := r.IntegrationItems.List(ctx, org, ns.ID, integration.ItemFilter{}, core.PageRequest{})
			noErr(t, "Items.List() of a deleted integration", err)
			equal(t, "Items.List() of a deleted integration", items.Items, []*integration.Item{})
			_, err = r.IntegrationResources.Find(ctx, org, ns.ID, integration.ResourceTypeInvoice, syncable)
			wantErr(t, "Resources.Find() of a deleted integration", err, core.ErrNotFound)

			// The other integration keeps its children.
			gotM, err := r.IntegrationMappings.Get(ctx, org, m2.ID)
			noErr(t, "Mappings.Get() of the other integration", err)
			equal(t, "Mappings.Get() of the other integration", gotM, m2)
			gotC, err := r.IntegrationCollectionMappings.Get(ctx, org, c2.ID)
			noErr(t, "CollectionMappings.Get() of the other integration", err)
			equal(t, "CollectionMappings.Get() of the other integration", gotC, c2)
			items, err = r.IntegrationItems.List(ctx, org, keep.ID, integration.ItemFilter{}, core.PageRequest{})
			noErr(t, "Items.List() of the other integration", err)
			equal(t, "Items.List() of the other integration", items.Items, []*integration.Item{i2})
			gotR, err := r.IntegrationResources.Find(ctx, org, keep.ID, integration.ResourceTypeInvoice, syncable)
			noErr(t, "Resources.Find() of the other integration", err)
			equal(t, "Resources.Find() of the other integration", gotR, r2)
			return nil
		})
	})

	t.Run("integration customers", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		ns := integrationFixture(e, org, "netsuite", integration.ProviderNetsuite)
		xero := integrationFixture(e, org, "xero", integration.ProviderXero)
		anrok := integrationFixture(e, org, "anrok", integration.ProviderAnrok)
		avalara := integrationFixture(e, org, "avalara", integration.ProviderAvalara)
		hub := integrationFixture(e, org, "hubspot", integration.ProviderHubspot)
		integrationFixtureCreate(e, ns, xero, anrok, avalara, hub)
		custs := integrationFixtureCustomers(e, org, a.ID, "cust-1", "cust-2")
		c1, c2 := custs[0], custs[1]
		link := func(i *integration.Integration, c core.CustomerID, ext string) *integration.IntegrationCustomer {
			return &integration.IntegrationCustomer{
				ID: core.NewID[core.IntegrationCustomerID](e.ids), OrganizationID: org, IntegrationID: i.ID, CustomerID: c,
				Category: i.Category, ExternalCustomerID: ext, CreatedAt: now, UpdatedAt: now,
			}
		}
		l1 := link(ns, c1, "ns-1")
		l1.SubsidiaryID = ptr("sub-1")
		l1.SyncWithProvider = true
		l1.IsDefault = true
		l1.Settings = json.RawMessage(`{"targeted_object":"companies","email":"a@b.test"}`)
		l2 := link(anrok, c1, "anrok-1")
		l3 := link(hub, c1, "")
		l3.SyncWithProvider = true
		l4 := link(xero, c1, "xero-1")
		l5 := link(ns, c2, "ns-2")
		l5.IsDefault = true
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, l := range []*integration.IntegrationCustomer{l1, l2, l3, l4, l5} {
				noErr(t, "Create()", r.IntegrationCustomers.Create(ctx, l))
			}
			wantErr(t, "Create() with the same ID", r.IntegrationCustomers.Create(ctx, l1), core.ErrConflict)
			got, err := r.IntegrationCustomers.Get(ctx, org, l1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, l1)
			got, err = r.IntegrationCustomers.Get(ctx, org, l3.ID)
			noErr(t, "Get() without external id", err)
			equal(t, "Get() without external id", got, l3)
			_, err = r.IntegrationCustomers.Get(ctx, org, core.NewID[core.IntegrationCustomerID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			got, err = r.IntegrationCustomers.GetByExternalID(ctx, org, ns.ID, "ns-2")
			noErr(t, "GetByExternalID()", err)
			equal(t, "GetByExternalID()", got, l5)
			_, err = r.IntegrationCustomers.GetByExternalID(ctx, org, xero.ID, "ns-2")
			wantErr(t, "GetByExternalID() of another integration", err, core.ErrNotFound)

			byCustomer, err := r.IntegrationCustomers.ListByCustomer(ctx, org, c1, "")
			noErr(t, "ListByCustomer()", err)
			equal(t, "ListByCustomer()", byCustomer, []*integration.IntegrationCustomer{l1, l2, l3, l4})
			byCustomer, err = r.IntegrationCustomers.ListByCustomer(ctx, org, c1, integration.CategoryAccounting)
			noErr(t, "ListByCustomer() of a category", err)
			equal(t, "ListByCustomer() of a category", byCustomer, []*integration.IntegrationCustomer{l1, l4})

			// Isolation.
			_, err = r.IntegrationCustomers.Get(ctx, other, l1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.IntegrationCustomers.GetByExternalID(ctx, other, ns.ID, "ns-1")
			wantErr(t, "GetByExternalID() from another organization", err, core.ErrNotFound)
			byCustomer, err = r.IntegrationCustomers.ListByCustomer(ctx, other, c1, "")
			noErr(t, "ListByCustomer() from another organization", err)
			equal(t, "ListByCustomer() from another organization", byCustomer, []*integration.IntegrationCustomer{})
			moved := *l1
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.IntegrationCustomers.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Delete() from another organization", r.IntegrationCustomers.Delete(ctx, other, l1.ID), core.ErrNotFound)

			// Uniqueness: external id per integration, one tax link, one
			// default per category.
			wantErr(t, "Create() with a linked external id", r.IntegrationCustomers.Create(ctx, link(ns, c2, "ns-1")), core.ErrConflict)
			wantErr(t, "Create() of a second tax link", r.IntegrationCustomers.Create(ctx, link(avalara, c1, "ava-1")), core.ErrConflict)
			dflt := link(xero, c2, "xero-2")
			dflt.IsDefault = true
			wantErr(t, "Create() of a second default", r.IntegrationCustomers.Create(ctx, dflt), core.ErrConflict)
			blank := link(hub, c2, "")
			blank.SyncWithProvider = true
			noErr(t, "Create() of a second link without external id", r.IntegrationCustomers.Create(ctx, blank))
			noErr(t, "Create() of a tax link of another customer", r.IntegrationCustomers.Create(ctx, link(avalara, c2, "ava-2")))
			l4.ExternalCustomerID = "ns-1"
			l4.IntegrationID = ns.ID
			wantErr(t, "Update() to a linked external id", r.IntegrationCustomers.Update(ctx, l4), core.ErrConflict)
			l4.IntegrationID = xero.ID
			l4.ExternalCustomerID = "xero-1"
			l4.IsDefault = true
			wantErr(t, "Update() to a second default", r.IntegrationCustomers.Update(ctx, l4), core.ErrConflict)

			l1.IsDefault = false
			l1.SubsidiaryID = nil
			l1.Settings = nil
			l1.ExternalCustomerID = "ns-1b"
			l1.UpdatedAt = at(time.Minute)
			noErr(t, "Update() after conflicts", r.IntegrationCustomers.Update(ctx, l1))
			got, err = r.IntegrationCustomers.Get(ctx, org, l1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, l1)
			noErr(t, "Update() to the freed default", r.IntegrationCustomers.Update(ctx, l4))
			wantErr(t, "Update() unknown", r.IntegrationCustomers.Update(ctx, link(ns, c1, "nope")), core.ErrNotFound)

			noErr(t, "Delete()", r.IntegrationCustomers.Delete(ctx, org, l2.ID))
			wantErr(t, "Delete() again", r.IntegrationCustomers.Delete(ctx, org, l2.ID), core.ErrNotFound)
			noErr(t, "Create() of the freed tax link", r.IntegrationCustomers.Create(ctx, link(avalara, c1, "ava-1")))

			// Deleting the integration removes its links.
			noErr(t, "Integrations.Delete()", r.Integrations.Delete(ctx, org, ns.ID))
			_, err = r.IntegrationCustomers.Get(ctx, org, l5.ID)
			wantErr(t, "Get() of a deleted integration", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("integration mappings", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		ns := integrationFixture(e, org, "netsuite", integration.ProviderNetsuite)
		xero := integrationFixture(e, org, "xero", integration.ProviderXero)
		integrationFixtureCreate(e, ns, xero)
		addOn, metric := uuid.New(), uuid.New()
		mapping := func(i *integration.Integration, t integration.MappableType, id uuid.UUID, be core.BillingEntityID, ext string) *integration.Mapping {
			return &integration.Mapping{
				ID: core.NewID[core.IntegrationMappingID](e.ids), OrganizationID: org, IntegrationID: i.ID,
				MappableType: t, MappableID: id, BillingEntityID: be, ExternalID: ext, CreatedAt: now, UpdatedAt: now,
			}
		}
		general := mapping(ns, integration.MappableTypeAddOn, addOn, core.BillingEntityID{}, "ext-general")
		general.ExternalAccountCode = ptr("4000")
		general.ExternalName = ptr("Setup fee")
		perEntity := mapping(ns, integration.MappableTypeAddOn, addOn, a.ID, "ext-entity")
		metricMap := mapping(ns, integration.MappableTypeBillableMetric, metric, core.BillingEntityID{}, "ext-metric")
		onXero := mapping(xero, integration.MappableTypeAddOn, addOn, core.BillingEntityID{}, "xero-item")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, m := range []*integration.Mapping{general, perEntity, metricMap, onXero} {
				noErr(t, "Create()", r.IntegrationMappings.Create(ctx, m))
			}
			got, err := r.IntegrationMappings.Get(ctx, org, general.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, general)
			got, err = r.IntegrationMappings.Get(ctx, org, perEntity.ID)
			noErr(t, "Get() per billing entity", err)
			equal(t, "Get() per billing entity", got, perEntity)
			_, err = r.IntegrationMappings.Get(ctx, org, core.NewID[core.IntegrationMappingID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			got, err = r.IntegrationMappings.Find(ctx, org, ns.ID, integration.MappableTypeAddOn, addOn, core.BillingEntityID{})
			noErr(t, "Find() without billing entity", err)
			equal(t, "Find() without billing entity", got, general)
			got, err = r.IntegrationMappings.Find(ctx, org, ns.ID, integration.MappableTypeAddOn, addOn, a.ID)
			noErr(t, "Find() of a billing entity", err)
			equal(t, "Find() of a billing entity", got, perEntity)
			_, err = r.IntegrationMappings.Find(ctx, org, ns.ID, integration.MappableTypeBillableMetric, addOn, core.BillingEntityID{})
			wantErr(t, "Find() of another mappable type", err, core.ErrNotFound)
			_, err = r.IntegrationMappings.Find(ctx, org, ns.ID, integration.MappableTypeBillableMetric, metric, a.ID)
			wantErr(t, "Find() of a billing entity without mapping", err, core.ErrNotFound)

			list, err := r.IntegrationMappings.ListByIntegration(ctx, org, ns.ID)
			noErr(t, "ListByIntegration()", err)
			equal(t, "ListByIntegration()", list, []*integration.Mapping{general, perEntity, metricMap})

			// Isolation.
			_, err = r.IntegrationMappings.Get(ctx, other, general.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.IntegrationMappings.Find(ctx, other, ns.ID, integration.MappableTypeAddOn, addOn, core.BillingEntityID{})
			wantErr(t, "Find() from another organization", err, core.ErrNotFound)
			list, err = r.IntegrationMappings.ListByIntegration(ctx, other, ns.ID)
			noErr(t, "ListByIntegration() from another organization", err)
			equal(t, "ListByIntegration() from another organization", list, []*integration.Mapping{})
			moved := *general
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.IntegrationMappings.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Delete() from another organization", r.IntegrationMappings.Delete(ctx, other, general.ID), core.ErrNotFound)

			// Uniqueness per (integration, mappable, billing entity), with
			// and without billing entity.
			wantErr(t, "Create() of a taken general key", r.IntegrationMappings.Create(ctx, mapping(ns, integration.MappableTypeAddOn, addOn, core.BillingEntityID{}, "x")), core.ErrConflict)
			wantErr(t, "Create() of a taken billing entity key", r.IntegrationMappings.Create(ctx, mapping(ns, integration.MappableTypeAddOn, addOn, a.ID, "x")), core.ErrConflict)
			metricMap.MappableType = integration.MappableTypeAddOn
			metricMap.MappableID = addOn
			wantErr(t, "Update() to a taken key", r.IntegrationMappings.Update(ctx, metricMap), core.ErrConflict)
			metricMap.MappableType = integration.MappableTypeBillableMetric
			metricMap.MappableID = metric
			metricMap.BillingEntityID = a.ID
			metricMap.ExternalID = "ext-metric-2"
			metricMap.ExternalName = ptr("Usage")
			metricMap.UpdatedAt = at(time.Minute)
			noErr(t, "Update() after conflicts", r.IntegrationMappings.Update(ctx, metricMap))
			got, err = r.IntegrationMappings.Find(ctx, org, ns.ID, integration.MappableTypeBillableMetric, metric, a.ID)
			noErr(t, "Find() after Update()", err)
			equal(t, "Find() after Update()", got, metricMap)
			wantErr(t, "Update() unknown", r.IntegrationMappings.Update(ctx, mapping(ns, integration.MappableTypeAddOn, uuid.New(), core.BillingEntityID{}, "x")), core.ErrNotFound)

			noErr(t, "Delete()", r.IntegrationMappings.Delete(ctx, org, general.ID))
			wantErr(t, "Delete() again", r.IntegrationMappings.Delete(ctx, org, general.ID), core.ErrNotFound)
			noErr(t, "Create() of a freed key", r.IntegrationMappings.Create(ctx, mapping(ns, integration.MappableTypeAddOn, addOn, core.BillingEntityID{}, "x")))
			return nil
		})
	})

	t.Run("integration collection mappings", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		ns := integrationFixture(e, org, "netsuite", integration.ProviderNetsuite)
		xero := integrationFixture(e, org, "xero", integration.ProviderXero)
		integrationFixtureCreate(e, ns, xero)
		mapping := func(i *integration.Integration, t integration.CollectionMappingType, be core.BillingEntityID) *integration.CollectionMapping {
			return &integration.CollectionMapping{
				ID: core.NewID[core.IntegrationCollectionMappingID](e.ids), OrganizationID: org, IntegrationID: i.ID,
				Type: t, BillingEntityID: be, CreatedAt: now, UpdatedAt: now,
			}
		}
		fallback := mapping(ns, integration.CollectionMappingTypeFallbackItem, core.BillingEntityID{})
		fallback.ExternalID = ptr("fb-1")
		fallback.ExternalAccountCode = ptr("4000")
		fallback.ExternalName = ptr("Fallback")
		tax := mapping(ns, integration.CollectionMappingTypeTax, a.ID)
		tax.TaxNexus, tax.TaxType, tax.TaxCode = ptr("nexus"), ptr("type"), ptr("code")
		currencies := mapping(ns, integration.CollectionMappingTypeCurrencies, core.BillingEntityID{})
		currencies.Currencies = map[string]string{"EUR": "1", "USD": "2"}
		fallbackEntity := mapping(ns, integration.CollectionMappingTypeFallbackItem, a.ID)
		fallbackEntity.ExternalID = ptr("fb-entity")
		onXero := mapping(xero, integration.CollectionMappingTypeFallbackItem, core.BillingEntityID{})
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, m := range []*integration.CollectionMapping{fallback, tax, currencies, fallbackEntity, onXero} {
				noErr(t, "Create()", r.IntegrationCollectionMappings.Create(ctx, m))
			}
			for _, m := range []*integration.CollectionMapping{fallback, tax, currencies, onXero} {
				got, err := r.IntegrationCollectionMappings.Get(ctx, org, m.ID)
				noErr(t, "Get()", err)
				equal(t, "Get()", got, m)
			}
			_, err := r.IntegrationCollectionMappings.Get(ctx, org, core.NewID[core.IntegrationCollectionMappingID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			list, err := r.IntegrationCollectionMappings.ListByType(ctx, org, ns.ID, "")
			noErr(t, "ListByType() of every type", err)
			equal(t, "ListByType() of every type", list, []*integration.CollectionMapping{fallback, tax, currencies, fallbackEntity})
			list, err = r.IntegrationCollectionMappings.ListByType(ctx, org, ns.ID, integration.CollectionMappingTypeFallbackItem)
			noErr(t, "ListByType()", err)
			equal(t, "ListByType()", list, []*integration.CollectionMapping{fallback, fallbackEntity})
			list, err = r.IntegrationCollectionMappings.ListByType(ctx, org, ns.ID, integration.CollectionMappingTypeCoupon)
			noErr(t, "ListByType() without match", err)
			equal(t, "ListByType() without match", list, []*integration.CollectionMapping{})

			// Isolation.
			_, err = r.IntegrationCollectionMappings.Get(ctx, other, fallback.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			list, err = r.IntegrationCollectionMappings.ListByType(ctx, other, ns.ID, "")
			noErr(t, "ListByType() from another organization", err)
			equal(t, "ListByType() from another organization", list, []*integration.CollectionMapping{})
			moved := *fallback
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.IntegrationCollectionMappings.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Delete() from another organization", r.IntegrationCollectionMappings.Delete(ctx, other, fallback.ID), core.ErrNotFound)

			// Uniqueness per (integration, type, billing entity).
			wantErr(t, "Create() of a taken general key", r.IntegrationCollectionMappings.Create(ctx, mapping(ns, integration.CollectionMappingTypeFallbackItem, core.BillingEntityID{})), core.ErrConflict)
			wantErr(t, "Create() of a taken billing entity key", r.IntegrationCollectionMappings.Create(ctx, mapping(ns, integration.CollectionMappingTypeTax, a.ID)), core.ErrConflict)
			fallbackEntity.BillingEntityID = core.BillingEntityID{}
			wantErr(t, "Update() to a taken key", r.IntegrationCollectionMappings.Update(ctx, fallbackEntity), core.ErrConflict)
			fallbackEntity.BillingEntityID = a.ID

			currencies.Currencies = map[string]string{"GBP": "3"}
			currencies.ExternalName = ptr("Currencies")
			currencies.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.IntegrationCollectionMappings.Update(ctx, currencies))
			fallback.ExternalID, fallback.ExternalAccountCode, fallback.ExternalName = nil, nil, nil
			noErr(t, "Update() clearing fields", r.IntegrationCollectionMappings.Update(ctx, fallback))
			for _, m := range []*integration.CollectionMapping{currencies, fallback} {
				got, err := r.IntegrationCollectionMappings.Get(ctx, org, m.ID)
				noErr(t, "Get() after Update()", err)
				equal(t, "Get() after Update()", got, m)
			}
			wantErr(t, "Update() unknown", r.IntegrationCollectionMappings.Update(ctx, mapping(ns, integration.CollectionMappingTypeCoupon, core.BillingEntityID{})), core.ErrNotFound)

			noErr(t, "Delete()", r.IntegrationCollectionMappings.Delete(ctx, org, tax.ID))
			wantErr(t, "Delete() again", r.IntegrationCollectionMappings.Delete(ctx, org, tax.ID), core.ErrNotFound)
			noErr(t, "Create() of a freed key", r.IntegrationCollectionMappings.Create(ctx, mapping(ns, integration.CollectionMappingTypeTax, a.ID)))
			return nil
		})
	})

	t.Run("integration items", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		ns := integrationFixture(e, org, "netsuite", integration.ProviderNetsuite)
		xero := integrationFixture(e, org, "xero", integration.ProviderXero)
		integrationFixtureCreate(e, ns, xero)
		item := func(i *integration.Integration, t integration.ItemType, ext string, name *string) *integration.Item {
			return &integration.Item{
				ID: core.NewID[core.IntegrationItemID](e.ids), OrganizationID: org, IntegrationID: i.ID,
				ItemType: t, ExternalID: ext, ExternalName: name, CreatedAt: now, UpdatedAt: now,
			}
		}
		i1 := item(ns, integration.ItemTypeStandard, "SKU-100", ptr("Consulting Hours"))
		i1.ExternalAccountCode = ptr("4000")
		i2 := item(ns, integration.ItemTypeStandard, "SKU-200", ptr("Hosting 50%_off"))
		i3 := item(ns, integration.ItemTypeTax, "TAX-1", nil)
		i4 := item(ns, integration.ItemTypeAccount, "ACC-1", ptr("Revenue"))
		i5 := item(xero, integration.ItemTypeStandard, "SKU-100", nil)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, i := range []*integration.Item{i1, i2, i3, i4, i5} {
				noErr(t, "Upsert()", r.IntegrationItems.Upsert(ctx, i))
			}
			all := listAll(t, 2, func(p core.PageRequest) (core.Page[*integration.Item], error) {
				return r.IntegrationItems.List(ctx, org, ns.ID, integration.ItemFilter{}, p)
			})
			equal(t, "List()", all, []*integration.Item{i1, i2, i3, i4})

			// Upsert of a cached (integration, type, external id) replaces
			// it and keeps its id and creation instant.
			again := item(ns, integration.ItemTypeStandard, "SKU-100", ptr("Consulting"))
			again.CreatedAt, again.UpdatedAt = at(time.Hour), at(time.Hour)
			noErr(t, "Upsert() of a cached item", r.IntegrationItems.Upsert(ctx, again))
			want := *i1
			want.ExternalAccountCode = nil
			want.ExternalName = ptr("Consulting")
			want.UpdatedAt = at(time.Hour)
			std := listAll(t, 1, func(p core.PageRequest) (core.Page[*integration.Item], error) {
				return r.IntegrationItems.List(ctx, org, ns.ID, integration.ItemFilter{ItemType: integration.ItemTypeStandard}, p)
			})
			equal(t, "List() of a type after Upsert()", std, []*integration.Item{&want, i2})

			// Search is case-insensitive on the external id and name, with
			// LIKE wildcards matched literally.
			for _, tc := range []struct {
				search string
				want   []core.IntegrationItemID
			}{
				{"sku", []core.IntegrationItemID{i1.ID, i2.ID}},
				{"REVEN", []core.IntegrationItemID{i4.ID}},
				{"50%_", []core.IntegrationItemID{i2.ID}},
				{"%", []core.IntegrationItemID{i2.ID}},
				{"_", []core.IntegrationItemID{i2.ID}},
				{"nothing", []core.IntegrationItemID{}},
			} {
				got := listAll(t, 1, func(p core.PageRequest) (core.Page[*integration.Item], error) {
					return r.IntegrationItems.List(ctx, org, ns.ID, integration.ItemFilter{Search: tc.search}, p)
				})
				equal(t, "List() searching "+tc.search, ids(got, integrationItemID), tc.want)
			}
			got := listAll(t, 5, func(p core.PageRequest) (core.Page[*integration.Item], error) {
				return r.IntegrationItems.List(ctx, org, ns.ID, integration.ItemFilter{ItemType: integration.ItemTypeTax, Search: "tax"}, p)
			})
			equal(t, "List() of a type searching", ids(got, integrationItemID), []core.IntegrationItemID{i3.ID})

			// Isolation.
			p, err := r.IntegrationItems.List(ctx, other, ns.ID, integration.ItemFilter{}, core.PageRequest{})
			noErr(t, "List() from another organization", err)
			equal(t, "List() from another organization", p.Items, []*integration.Item{})
			n, err := r.IntegrationItems.DeleteByType(ctx, other, ns.ID, integration.ItemTypeStandard)
			noErr(t, "DeleteByType() from another organization", err)
			if n != 0 {
				t.Errorf("DeleteByType() from another organization got = %d, want = 0", n)
			}

			n, err = r.IntegrationItems.DeleteByType(ctx, org, ns.ID, integration.ItemTypeStandard)
			noErr(t, "DeleteByType()", err)
			if n != 2 {
				t.Errorf("DeleteByType() got = %d, want = 2", n)
			}
			all = listAll(t, 10, func(p core.PageRequest) (core.Page[*integration.Item], error) {
				return r.IntegrationItems.List(ctx, org, ns.ID, integration.ItemFilter{}, p)
			})
			equal(t, "List() after DeleteByType()", all, []*integration.Item{i3, i4})
			all = listAll(t, 10, func(p core.PageRequest) (core.Page[*integration.Item], error) {
				return r.IntegrationItems.List(ctx, org, xero.ID, integration.ItemFilter{}, p)
			})
			equal(t, "List() of another integration after DeleteByType()", all, []*integration.Item{i5})
			return nil
		})
	})

	t.Run("integration resources", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		ns := integrationFixture(e, org, "netsuite", integration.ProviderNetsuite)
		xero := integrationFixture(e, org, "xero", integration.ProviderXero)
		integrationFixtureCreate(e, ns, xero)
		invoiceID := uuid.New()
		resource := func(i *integration.Integration, t integration.ResourceType, id uuid.UUID, ext string) *integration.Resource {
			return &integration.Resource{
				ID: core.NewID[core.IntegrationResourceID](e.ids), OrganizationID: org, IntegrationID: i.ID,
				ResourceType: t, SyncableID: id, ExternalID: ext, CreatedAt: now, UpdatedAt: at(time.Second),
			}
		}
		r1 := resource(ns, integration.ResourceTypeInvoice, invoiceID, "ns-inv-1")
		r2 := resource(ns, integration.ResourceTypePayment, invoiceID, "ns-pay-1")
		r3 := resource(xero, integration.ResourceTypeInvoice, invoiceID, "xero-inv-1")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, res := range []*integration.Resource{r1, r2, r3} {
				noErr(t, "Create()", r.IntegrationResources.Create(ctx, res))
			}
			wantErr(t, "Create() of a recorded object", r.IntegrationResources.Create(ctx, resource(ns, integration.ResourceTypeInvoice, invoiceID, "other")), core.ErrConflict)
			for _, res := range []*integration.Resource{r1, r2, r3} {
				got, err := r.IntegrationResources.Find(ctx, org, res.IntegrationID, res.ResourceType, res.SyncableID)
				noErr(t, "Find()", err)
				equal(t, "Find()", got, res)
			}
			_, err := r.IntegrationResources.Find(ctx, org, ns.ID, integration.ResourceTypeCreditNote, invoiceID)
			wantErr(t, "Find() unknown", err, core.ErrNotFound)
			_, err = r.IntegrationResources.Find(ctx, other, ns.ID, integration.ResourceTypeInvoice, invoiceID)
			wantErr(t, "Find() from another organization", err, core.ErrNotFound)
			wantErr(t, "Delete() from another organization", r.IntegrationResources.Delete(ctx, other, r1.ID), core.ErrNotFound)

			noErr(t, "Delete()", r.IntegrationResources.Delete(ctx, org, r1.ID))
			wantErr(t, "Delete() again", r.IntegrationResources.Delete(ctx, org, r1.ID), core.ErrNotFound)
			_, err = r.IntegrationResources.Find(ctx, org, ns.ID, integration.ResourceTypeInvoice, invoiceID)
			wantErr(t, "Find() deleted", err, core.ErrNotFound)
			noErr(t, "Create() of a freed object", r.IntegrationResources.Create(ctx, resource(ns, integration.ResourceTypeInvoice, invoiceID, "ns-inv-2")))
			return nil
		})
	})
}
