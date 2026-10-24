package customers_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// storeIntegration stores an integration of p named code.
func storeIntegration(h *billingtest.Harness, code string, p integration.Provider) *integration.Integration {
	h.T.Helper()
	cat, _ := integration.CategoryOf(p)
	in := &integration.Integration{
		ID: core.NewID[core.IntegrationID](h.IDs), OrganizationID: h.OrgID(), Code: code, Name: code,
		Category: cat, Provider: p, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Integrations.Create(ctx, in) })
	return in
}

// contactJobs returns the contact syncs enqueued: the integration customer
// of each and whether it updates.
func contactJobs(t *testing.T, h *billingtest.Harness) []kernel.IntegrationCustomerArgs {
	t.Helper()
	var out []kernel.IntegrationCustomerArgs
	for _, j := range jobs(h, kernel.JobSyncContact) {
		var a kernel.IntegrationCustomerArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// TestIntegrationConnectionCreate ports the creation of accounting, tax
// and CRM connections: a linked record is stored at once; a synced one is
// created at its provider by a job, Anrok's and Salesforce's with their
// first document.
func TestIntegrationConnectionCreate(t *testing.T) {
	for _, tt := range []struct {
		name     string
		sources  []string
		provider integration.Provider
		in       customers.ConnectionInput
		sync     bool // a contact creation is enqueued
		synced   bool // the stored sync_with_provider
	}{
		{
			name: "netsuite linked",
			sources: []string{
				"spec/services/integration_customers/create_service_spec.rb:54, 63",
				"spec/services/integration_customers/create_service_spec.rb:156, 162",
				// The connection code waits for the connections row of parity.
				"spec/services/integration_customers/create_connection_service_spec.rb:101, 105",
				// The link's category is its integration's.
				"spec/models/integration_customers/base_customer_spec.rb:28, 228",
			},
			provider: integration.ProviderNetsuite,
			in:       customers.ConnectionInput{ExternalCustomerID: core.Some("ns_1"), SyncWithProvider: core.Some(true)},
		},
		{
			name: "salesforce linked",
			sources: []string{
				"spec/services/integration_customers/create_service_spec.rb:105",
			},
			provider: integration.ProviderSalesforce,
			in:       customers.ConnectionInput{ExternalCustomerID: core.Some("sf_1"), SyncWithProvider: core.Some(true)},
			synced:   true,
		},
		{
			name: "netsuite synced",
			sources: []string{
				"spec/services/integration_customers/create_service_spec.rb:120, 128",
				"spec/services/integration_customers/create_or_update_batch_service_spec.rb:176",
				"spec/services/integration_customers/create_or_update_batch_service_spec.rb:185",
				"spec/services/integration_customers/create_connection_service_spec.rb:208",
			},
			provider: integration.ProviderNetsuite,
			in:       customers.ConnectionInput{SyncWithProvider: core.Some(true), SubsidiaryID: core.Some(new("1"))},
			sync:     true, synced: true,
		},
		{
			name:     "xero synced",
			provider: integration.ProviderXero,
			in:       customers.ConnectionInput{SyncWithProvider: core.Some(true)},
			sync:     true, synced: true,
		},
		{
			name:     "avalara synced",
			provider: integration.ProviderAvalara,
			in:       customers.ConnectionInput{SyncWithProvider: core.Some(true)},
			sync:     true, synced: true,
		},
		{
			name: "anrok synced",
			sources: []string{
				"spec/services/integration_customers/create_service_spec.rb:143",
				"spec/services/integration_customers/anrok_service_spec.rb:14, 24",
				"spec/services/integration_customers/create_connection_service_spec.rb:135",
				"spec/models/integration_customers/base_customer_spec.rb:222",
			},
			provider: integration.ProviderAnrok,
			in:       customers.ConnectionInput{SyncWithProvider: core.Some(true)},
			synced:   true,
		},
		{
			name: "salesforce synced",
			sources: []string{
				"spec/services/integration_customers/salesforce_service_spec.rb:14, 23",
				"spec/services/integration_customers/create_or_update_batch_service_spec.rb:269",
			},
			provider: integration.ProviderSalesforce,
			in:       customers.ConnectionInput{SyncWithProvider: core.Some(true)},
			synced:   true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range tt.sources {
				t.Log("source:", s)
			}
			h, svc := setup(t)
			in := storeIntegration(h, string(tt.provider), tt.provider)
			c := h.Customer()
			got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode(in.Code), tt.in)
			if err != nil {
				t.Fatalf("UpsertConnection: %v", err)
			}
			if got.Integration.ID != in.ID || got.ExternalCustomerID != tt.in.ExternalCustomerID.Value || got.SyncWithProvider != tt.synced {
				t.Errorf("got = %+v, want = %s, external ID %q, synced %v", got, in.Code, tt.in.ExternalCustomerID.Value, tt.synced)
			}
			h.Do(func(ctx context.Context, r port.Repos) error {
				links, err := r.IntegrationCustomers.ListByCustomer(ctx, h.OrgID(), c.ID, in.Category)
				if err != nil || len(links) != 1 || links[0].Category != in.Category {
					t.Errorf("links of %s: got = %d (%v), want = 1", in.Category, len(links), err)
				}
				return nil
			})
			jobs := contactJobs(t, h)
			if n := len(jobs); tt.sync && (n != 1 || jobs[0].Update) || !tt.sync && n != 0 {
				t.Errorf("contact jobs: got = %+v, want creation = %v", jobs, tt.sync)
			}
		})
	}

	t.Run("sync without record", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/create_service_spec.rb:170, 176")
		t.Log("source: spec/graphql/mutations/integration_customers/create_spec.rb:90")
		t.Log("source: spec/services/integration_customers/create_or_update_batch_service_spec.rb:63, 67")
		h, svc := setup(t)
		storeIntegration(h, "netsuite", integration.ProviderNetsuite)
		c := h.Customer()
		t.Log("source: spec/services/integration_customers/create_connection_service_spec.rb:87")
		got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite"), customers.ConnectionInput{SyncWithProvider: core.Some(false)})
		if err != nil || got.Integration != nil {
			t.Errorf("got = %+v %v, want = no connection", got, err)
		}
		if list, _ := svc.ListConnections(h.Ctx, h.OrgID(), c.ID); len(list) != 0 {
			t.Errorf("connections: got = %+v, want = none", list)
		}
		if got := contactJobs(t, h); len(got) != 0 {
			t.Errorf("contact jobs: got = %+v, want = none", got)
		}
	})

	t.Run("unknown integration or customer", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/create_or_update_batch_service_spec.rb:32, 36")
		t.Log("source: spec/services/integration_customers/create_or_update_batch_service_spec.rb:48, 52")
		t.Log("source: spec/services/integration_customers/create_connection_service_spec.rb:28, 37, 46")
		t.Log("source: spec/services/integration_customers/set_as_default_service_spec.rb:55")
		t.Log("source: spec/graphql/mutations/integration_customers/create_spec.rb:111, 131")
		t.Log("source: spec/graphql/mutations/integration_customers/set_as_default_spec.rb:92, 108")
		t.Log("source: spec/graphql/mutations/integration_customers/update_spec.rb:77, 93")
		h, svc := setup(t)
		storeIntegration(h, "netsuite", integration.ProviderNetsuite)
		c := h.Customer()
		in := customers.ConnectionInput{SyncWithProvider: core.Some(true)}
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("xero"), in); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("unknown integration: got = %v, want = not found", err)
		}
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), core.NewID[core.CustomerID](h.IDs), refs.ByCode("netsuite"), in); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("unknown customer: got = %v, want = not found", err)
		}
		if got := contactJobs(t, h); len(got) != 0 {
			t.Errorf("contact jobs: got = %+v, want = none", got)
		}
	})

	t.Run("several integrations", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/create_or_update_batch_service_spec.rb:210")
		t.Log("source: spec/services/integration_customers/create_or_update_batch_service_spec.rb:247")
		h, svc := setup(t)
		storeIntegration(h, "netsuite", integration.ProviderNetsuite)
		storeIntegration(h, "hubspot", integration.ProviderHubspot)
		c := h.Customer()
		for _, code := range []string{"netsuite", "hubspot"} {
			if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode(code), customers.ConnectionInput{SyncWithProvider: core.Some(true)}); err != nil {
				t.Fatalf("UpsertConnection(%s): %v", code, err)
			}
			if got := contactJobs(t, h); len(got) != 1 {
				t.Errorf("contact jobs after %s: got = %+v, want = its creation", code, got)
			}
			h.Store.TakeJobs()
		}
	})

	t.Run("default of the category", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/create_service_spec.rb:93")
		t.Log("source: spec/graphql/mutations/integration_customers/create_spec.rb:69")
		t.Log("source: spec/services/integration_customers/create_connection_service_spec.rb:117")
		t.Log("source: spec/services/integration_customers/create_connection_service_spec.rb:146, 150, 162, 166")
		h, svc := setup(t)
		storeIntegration(h, "xero", integration.ProviderXero)
		storeIntegration(h, "netsuite", integration.ProviderNetsuite)
		storeIntegration(h, "hubspot", integration.ProviderHubspot)
		c := h.Customer()
		if crm, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("hubspot"), customers.ConnectionInput{ExternalCustomerID: core.Some("501")}); err != nil || !crm.IsDefault {
			t.Fatalf("crm: got = %+v %v, want = the crm default", crm, err)
		}
		first, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("xero"), customers.ConnectionInput{ExternalCustomerID: core.Some("x_1")})
		if err != nil || !first.IsDefault {
			t.Fatalf("first: got = %+v %v, want = the default", first, err)
		}
		second, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite"), customers.ConnectionInput{ExternalCustomerID: core.Some("ns_1")})
		if err != nil || second.IsDefault {
			t.Errorf("second: got = %+v %v, want = not the default", second, err)
		}
		list, err := svc.ListConnections(h.Ctx, h.OrgID(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, cn := range list {
			if want := cn.Integration.Code != "netsuite"; cn.IsDefault != want {
				t.Errorf("%s: got = default %v, want = %v", cn.Integration.Code, cn.IsDefault, want)
			}
		}
	})

	t.Run("auth integration", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/create_connection_service_spec.rb:56")
		h, svc := setup(t)
		storeIntegration(h, "okta", integration.ProviderOkta)
		c := h.Customer()
		_, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("okta"), customers.ConnectionInput{ExternalCustomerID: core.Some("o_1")})
		if fieldCode(err, "integration_id") != core.CodeInvalid {
			t.Errorf("got = %v, want = integration_id invalid", err)
		}
	})

	t.Run("one connection per provider", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/create_connection_service_spec.rb:68")
		h, svc := setup(t)
		storeIntegration(h, "netsuite_1", integration.ProviderNetsuite)
		storeIntegration(h, "netsuite_2", integration.ProviderNetsuite)
		c := h.Customer()
		got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite_2"), customers.ConnectionInput{ExternalCustomerID: core.Some("ns_1")})
		if err != nil || got.Integration.Code != "netsuite_2" {
			t.Fatalf("got = %+v %v, want = the netsuite_2 connection", got, err)
		}
		// base_customer.rb:42: customer_id is unique per provider.
		_, err = svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite_1"), customers.ConnectionInput{ExternalCustomerID: core.Some("ns_2")})
		if fieldCode(err, "customer_id") != core.CodeAlreadyExists {
			t.Errorf("got = %v, want = customer_id value_already_exist", err)
		}
	})

	t.Run("one tax integration", func(t *testing.T) {
		t.Log("source: spec/models/integration_customers/base_customer_spec.rb:253")
		t.Log("source: spec/models/integration_customers/base_customer_spec.rb:268")
		h, svc := setup(t)
		storeIntegration(h, "anrok_1", integration.ProviderAnrok)
		storeIntegration(h, "anrok_2", integration.ProviderAnrok)
		storeIntegration(h, "avalara", integration.ProviderAvalara)
		c := h.Customer()
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("anrok_1"), customers.ConnectionInput{ExternalCustomerID: core.Some("an_1")}); err != nil {
			t.Fatalf("UpsertConnection: %v", err)
		}
		_, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("avalara"), customers.ConnectionInput{ExternalCustomerID: core.Some("av_1")})
		if got := fieldCode(err, "integration_id"); got != integration.CodeTaxIntegrationExists || fieldCode(err, "customer_id") != "" {
			t.Errorf("another tax provider: got = %v, want = integration_id tax_integration_exists", err)
		}
		_, err = svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("anrok_2"), customers.ConnectionInput{ExternalCustomerID: core.Some("an_2")})
		if fieldCode(err, "integration_id") != integration.CodeTaxIntegrationExists || fieldCode(err, "customer_id") != core.CodeAlreadyExists {
			t.Errorf("same tax provider: got = %v, want = customer_id value_already_exist and integration_id tax_integration_exists", err)
		}
		// Another customer may link the same external customer.
		other := h.Customer()
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), other.ID, refs.ByCode("anrok_1"), customers.ConnectionInput{ExternalCustomerID: core.Some("an_1")}); err != nil {
			t.Errorf("shared external customer: got = %v, want = nil", err)
		}
	})

	t.Run("partner account", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/create_connection_service_spec.rb:77, 82")
		h, svc := setup(t)
		storeIntegration(h, "netsuite", integration.ProviderNetsuite)
		c := h.Customer(func(c *customer.Customer) { c.AccountType = customer.AccountTypePartner })
		t.Log("source: app/services/integration_customers/create_or_update_batch_service.rb:16")
		got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite"), customers.ConnectionInput{ExternalCustomerID: core.Some("ns_1")})
		if err != nil || got.Integration != nil {
			t.Errorf("got = %+v %v, want = no connection", got, err)
		}
		if list, _ := svc.ListConnections(h.Ctx, h.OrgID(), c.ID); len(list) != 0 {
			t.Errorf("connections: got = %+v, want = none", list)
		}
	})

	t.Run("partner account update", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/update_connection_service_spec.rb:147")
		h, svc := setup(t)
		storeIntegration(h, "netsuite", integration.ProviderNetsuite)
		c := h.Customer()
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite"), customers.ConnectionInput{ExternalCustomerID: core.Some("ns_1")}); err != nil {
			t.Fatalf("create: %v", err)
		}
		c.AccountType = customer.AccountTypePartner
		h.Do(func(ctx context.Context, r port.Repos) error { return r.Customers.Update(ctx, c) })
		h.Store.TakeJobs()
		got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite"), customers.ConnectionInput{ExternalCustomerID: core.Some("ns_2")})
		t.Log("source: app/services/integration_customers/create_or_update_batch_service.rb:16")
		if err != nil || got.ExternalCustomerID != "ns_1" {
			t.Errorf("got = %+v %v, want = unchanged ns_1", got, err)
		}
		if jobs := contactJobs(t, h); len(jobs) != 0 {
			t.Errorf("contact jobs: got = %+v, want = none", jobs)
		}
	})

	t.Run("hubspot targeted object", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/create_service_spec.rb:54")
		h, svc := setup(t)
		storeIntegration(h, "hubspot", integration.ProviderHubspot)
		storeIntegration(h, "netsuite", integration.ProviderNetsuite)
		c := h.Customer()
		got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("hubspot"), customers.ConnectionInput{
			ExternalCustomerID: core.Some("501"), TargetedObject: core.Some(integration.TargetedObjectContacts),
		})
		if err != nil || got.TargetedObject != integration.TargetedObjectContacts {
			t.Errorf("got = %+v %v, want = targeting contacts", got, err)
		}
		got, err = svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("hubspot"), customers.ConnectionInput{TargetedObject: core.Some("")})
		if err != nil || got.TargetedObject != integration.TargetedObjectContacts {
			t.Errorf("blank: got = %+v %v, want = still targeting contacts", got, err)
		}
		t.Log("source: spec/services/integration_customers/update_connection_service_spec.rb:95")
		h.Store.TakeJobs()
		got, err = svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("hubspot"), customers.ConnectionInput{TargetedObject: core.Some(integration.TargetedObjectCompanies)})
		if err != nil || got.TargetedObject != integration.TargetedObjectCompanies {
			t.Errorf("companies: got = %+v %v, want = targeting companies", got, err)
		}
		if jobs := contactJobs(t, h); len(jobs) != 1 || !jobs[0].Update {
			t.Errorf("contact jobs: got = %+v, want = an update", jobs)
		}
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("hubspot"), customers.ConnectionInput{TargetedObject: core.Some("deals")}); fieldCode(err, "targeted_object") != core.CodeInvalid {
			t.Errorf("deals: got = %v, want = targeted_object invalid", err)
		}
		// create_service.rb:84: only HubSpot links target an object.
		got, err = svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite"), customers.ConnectionInput{
			ExternalCustomerID: core.Some("ns_1"), TargetedObject: core.Some(integration.TargetedObjectContacts),
		})
		if err != nil || got.TargetedObject != "" {
			t.Errorf("netsuite: got = %+v %v, want = no targeted object", got, err)
		}
	})
}

// TestIntegrationConnectionUpdate ports the update of an integration
// connection: a non-blank external ID replaces the stored one and a linked
// record is updated at its provider, Anrok's and Salesforce's excepted, as
// they keep none; the sync flag and the subsidiary are set at creation.
func TestIntegrationConnectionUpdate(t *testing.T) {
	for _, tt := range []struct {
		name     string
		sources  []string
		provider integration.Provider
		linked   string
		in       customers.ConnectionInput
		want     string // the stored external ID
		update   bool
	}{
		{
			name: "external ID",
			sources: []string{
				"spec/services/integration_customers/update_service_spec.rb:58",
				"spec/services/integration_customers/create_or_update_batch_service_spec.rb:158",
				"spec/services/integration_customers/update_connection_service_spec.rb:60",
				"spec/graphql/mutations/integration_customers/update_spec.rb:60",
			},
			provider: integration.ProviderNetsuite, linked: "ns_1",
			in:   customers.ConnectionInput{ExternalCustomerID: core.Some("ns_2"), SyncWithProvider: core.Some(true)},
			want: "ns_2", update: true,
		},
		{
			name: "subsidiary",
			sources: []string{
				"spec/services/integration_customers/update_service_spec.rb:69",
				"spec/services/integration_customers/update_connection_service_spec.rb:76",
			},
			provider: integration.ProviderNetsuite, linked: "ns_1",
			in:   customers.ConnectionInput{SubsidiaryID: core.Some(new("2"))},
			want: "ns_1", update: true,
		},
		{
			name: "blank external ID",
			sources: []string{
				"spec/services/integration_customers/update_service_spec.rb:81",
				"spec/services/integration_customers/update_connection_service_spec.rb:35, 40",
			},
			provider: integration.ProviderNetsuite, linked: "ns_1",
			in:   customers.ConnectionInput{ExternalCustomerID: core.Some(""), SyncWithProvider: core.Some(true)},
			want: "ns_1", update: true,
		},
		{
			name:     "not syncing",
			sources:  []string{"spec/services/integration_customers/update_service_spec.rb:132, 138"},
			provider: integration.ProviderNetsuite, linked: "ns_1",
			in:   customers.ConnectionInput{ExternalCustomerID: core.Some("ns_2"), SyncWithProvider: core.Some(false)},
			want: "ns_2", update: true,
		},
		{
			name:     "not syncing, blank external ID",
			sources:  []string{"spec/services/integration_customers/update_service_spec.rb:148, 152"},
			provider: integration.ProviderNetsuite, linked: "ns_1",
			in:   customers.ConnectionInput{ExternalCustomerID: core.Some(""), SyncWithProvider: core.Some(false)},
			want: "ns_1", update: true,
		},
		{
			name:     "without record",
			provider: integration.ProviderNetsuite,
			in:       customers.ConnectionInput{SubsidiaryID: core.Some(new("2"))},
		},
		{
			name: "anrok",
			sources: []string{
				"spec/services/integration_customers/update_service_spec.rb:94, 100",
				"spec/services/integration_customers/update_connection_service_spec.rb:133",
			},
			provider: integration.ProviderAnrok, linked: "an_1",
			in:   customers.ConnectionInput{ExternalCustomerID: core.Some("an_2")},
			want: "an_1",
		},
		{
			name: "salesforce",
			sources: []string{
				"spec/services/integration_customers/update_service_spec.rb:112, 118",
				"spec/services/integration_customers/update_connection_service_spec.rb:118",
			},
			provider: integration.ProviderSalesforce, linked: "sf_1",
			in:   customers.ConnectionInput{ExternalCustomerID: core.Some("sf_2")},
			want: "sf_1",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range tt.sources {
				t.Log("source:", s)
			}
			h, svc := setup(t)
			in := storeIntegration(h, string(tt.provider), tt.provider)
			c := h.Customer()
			first := customers.ConnectionInput{SyncWithProvider: core.Some(true), SubsidiaryID: core.Some(new("1"))}
			if tt.linked != "" {
				first = customers.ConnectionInput{ExternalCustomerID: core.Some(tt.linked), SubsidiaryID: core.Some(new("1"))}
			}
			before, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode(in.Code), first)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			h.Store.TakeJobs()
			got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode(in.Code), tt.in)
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if got.ExternalCustomerID != tt.want {
				t.Errorf("external ID: got = %q, want = %q", got.ExternalCustomerID, tt.want)
			}
			// update_service.rb assigns neither the sync flag nor the subsidiary.
			if got.SyncWithProvider != before.SyncWithProvider || got.SubsidiaryID == nil || *got.SubsidiaryID != "1" {
				t.Errorf("got = sync %v, subsidiary %v, want = sync %v, subsidiary 1", got.SyncWithProvider, got.SubsidiaryID, before.SyncWithProvider)
			}
			jobs := contactJobs(t, h)
			if n := len(jobs); tt.update && (n != 1 || !jobs[0].Update) || !tt.update && n != 0 {
				t.Errorf("contact jobs: got = %+v, want update = %v", jobs, tt.update)
			}
		})
	}
}

// TestIntegrationConnectionDelete ports the removal of an integration
// connection, the batch's of the links it no longer lists included.
func TestIntegrationConnectionDelete(t *testing.T) {
	t.Log("source: spec/services/integration_customers/destroy_service_spec.rb:19, 24")
	t.Log("source: spec/graphql/mutations/integration_customers/destroy_spec.rb:27")
	t.Log("source: spec/services/integration_customers/create_or_update_batch_service_spec.rb:83")
	t.Log("source: spec/services/integration_customers/create_or_update_batch_service_spec.rb:106, 110")
	t.Log("source: spec/services/integration_customers/create_or_update_batch_service_spec.rb:130, 134")
	h, svc := setup(t)
	storeIntegration(h, "netsuite", integration.ProviderNetsuite)
	storeIntegration(h, "hubspot", integration.ProviderHubspot)
	c := h.Customer()
	for code, ext := range map[string]string{"netsuite": "ns_1", "hubspot": "501"} {
		if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode(code), customers.ConnectionInput{ExternalCustomerID: core.Some(ext)}); err != nil {
			t.Fatalf("UpsertConnection(%s): %v", code, err)
		}
	}
	if err := svc.DeleteConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite")); err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("hubspot"), customers.ConnectionInput{ExternalCustomerID: core.Some("502")}); err != nil {
		t.Fatalf("UpsertConnection(hubspot): %v", err)
	}
	list, err := svc.ListConnections(h.Ctx, h.OrgID(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Integration.Code != "hubspot" || list[0].ExternalCustomerID != "502" {
		t.Errorf("connections: got = %+v, want = hubspot 502", list)
	}
	t.Log("source: spec/services/integration_customers/destroy_service_spec.rb:35")
	t.Log("source: spec/graphql/mutations/integration_customers/destroy_spec.rb:42")
	if err := svc.DeleteConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("netsuite")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("got = %v, want = not found", err)
	}
}
