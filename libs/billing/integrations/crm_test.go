package integrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// fakeCRM is a CRM adapter recording what it is sent. Each record sync
// answers id and err, each contact call contact and contactErr; Associate
// answers assocErr and Deploy deployErr.
type fakeCRM struct {
	fakeOAuth

	mu            sync.Mutex
	deploys       []integration.CRMObject
	invoices      []*integration.CRMInvoice
	subscriptions []*integration.CRMSubscription
	associations  []integration.CRMAssociation
	contacts      []*integration.Contact

	id        string
	err       error
	assocErr  error
	deployErr error
	account   string

	contact    integration.ContactResult
	contactErr error
}

func (g *fakeCRM) CreateContact(_ context.Context, _ integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.contacts = append(g.contacts, c)
	return g.contact, g.contactErr
}

func (g *fakeCRM) UpdateContact(_ context.Context, _ integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.contacts = append(g.contacts, c)
	return g.contact, g.contactErr
}

func (g *fakeCRM) Deploy(_ context.Context, _ integration.Connection, o integration.CRMObject, _ integration.CRMSchemaPart) (integration.ConnectionUpdate, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deploys = append(g.deploys, o)
	if g.deployErr != nil {
		return integration.ConnectionUpdate{}, g.deployErr
	}
	// The adapter records what it deployed in the connection's settings.
	return integration.ConnectionUpdate{Settings: map[string]string{
		string(o) + "_object_type_id": "2-" + string(o), string(o) + "_properties_version": "1",
	}}, nil
}

func (g *fakeCRM) AccountID(context.Context, integration.Connection) (string, error) {
	return g.account, nil
}

func (g *fakeCRM) SyncInvoice(_ context.Context, _ integration.Connection, inv *integration.CRMInvoice) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.invoices = append(g.invoices, inv)
	return g.id, g.err
}

func (g *fakeCRM) SyncSubscription(_ context.Context, _ integration.Connection, sub *integration.CRMSubscription) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.subscriptions = append(g.subscriptions, sub)
	return g.id, g.err
}

func (g *fakeCRM) Associate(_ context.Context, _ integration.Connection, a integration.CRMAssociation) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.associations = append(g.associations, a)
	return g.assocErr
}

// blobs serves the URL of every stored file.
type blobs struct{}

func (blobs) Put(context.Context, core.OrganizationID, string, string, io.Reader) (core.BlobRef, error) {
	return "", errors.New("read only")
}

func (blobs) Get(context.Context, core.OrganizationID, core.BlobRef) (io.ReadCloser, error) {
	return nil, core.ErrNotFound
}
func (blobs) Delete(context.Context, core.OrganizationID, core.BlobRef) error { return nil }

func (blobs) URL(_ context.Context, _ core.OrganizationID, ref core.BlobRef) (string, error) {
	return "https://files.test/" + string(ref), nil
}

// crmFixture is a customer linked to the company "501" of a HubSpot
// integration, subscribed to a plan.
type crmFixture struct {
	*acctFixture
	crm *fakeCRM
}

// newCRMFixture returns the fixture; sync sets the integration's invoice
// and subscription syncs.
func newCRMFixture(t *testing.T, sync bool) *crmFixture {
	t.Helper()
	ctx := context.Background()
	h := billingtest.New(t, now)
	gw := &fakeCRM{fakeOAuth: fakeOAuth{integration.ProviderHubspot}, id: "901", account: "12345678"}
	set, err := port.NewIntegrationProviderSet(gw)
	if err != nil {
		t.Fatal(err)
	}
	svc := integrations.New(h.Deps, sealer{}, integrations.WithProviders(set), integrations.WithBlobs(blobs{}))
	integ, err := svc.Create(ctx, h.OrgID(), integrations.Input{
		Code: "hubspot", Name: "HubSpot", Provider: integration.ProviderHubspot, SyncInvoices: sync, SyncSubscriptions: sync,
		Values: map[string]string{integrations.KeyDefaultTargetedObject: integration.TargetedObjectCompanies},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	c := h.Customer()
	ic := &integration.IntegrationCustomer{
		ID: core.NewID[core.IntegrationCustomerID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: integ.ID,
		CustomerID: c.ID, Category: integration.CategoryCRM, ExternalCustomerID: "501", SyncWithProvider: true,
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	if err := ic.SetSetting("targeted_object", integration.TargetedObjectCompanies); err != nil {
		t.Fatal(err)
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.IntegrationCustomers.Create(ctx, ic) })
	m := h.Metric("api", catalog.AggregationTypeCount, "")
	plan := h.Plan(period.Monthly, 0)
	ch := h.Charge(plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	return &crmFixture{
		acctFixture: &acctFixture{h: h, svc: svc, integ: integ, cust: c, ic: ic, sub: h.Subscription(c, plan), charge: ch},
		crm:         gw,
	}
}

// crmInvoice stores an invoice of the customer in status st with its PDF.
func (f *crmFixture) crmInvoice(st invoice.Status, file core.BlobRef) *invoice.Invoice {
	return f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status, i.File = st, file })
}

// crmErrors returns the provider errors of the customer.crm_provider_error
// webhooks.
func (f *crmFixture) crmErrors(t *testing.T) []integrations.ProviderErrorDetails {
	t.Helper()
	var out []integrations.ProviderErrorDetails
	for _, e := range f.h.Emitted.Events() {
		if e.Type != webhook.EventTypeCustomerCRMProviderError {
			continue
		}
		var p integrations.CustomerErrorPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p.ProviderError)
	}
	return out
}

// integrationErrors returns the provider errors of the
// integration.provider_error webhooks.
func (f *crmFixture) integrationErrors(t *testing.T) []integrations.ProviderErrorDetails {
	t.Helper()
	var out []integrations.ProviderErrorDetails
	for _, e := range f.h.Emitted.Events() {
		if e.Type != webhook.EventTypeIntegrationProviderError {
			continue
		}
		var p integrations.ProviderErrorPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p.ProviderError)
	}
	return out
}

func TestSyncCRMInvoiceEnqueue(t *testing.T) {
	ctx := context.Background()
	f := newCRMFixture(t, true)
	inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
	t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:63")
	t.Log("source: spec/graphql/mutations/integrations/hubspot/sync_invoice_spec.rb:53")
	if err := f.svc.SyncCRMInvoice(ctx, f.h.OrgID(), inv.ID); err != nil {
		t.Fatalf("SyncCRMInvoice: %v", err)
	}
	if got := syncJobs(f.h, kernel.JobCreateCRMInvoice); len(got) != 1 {
		t.Errorf("create jobs: got = %d, want = 1", len(got))
	}
	t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:71")
	if err := f.svc.SyncCRMInvoice(ctx, f.h.OrgID(), core.NewID[core.InvoiceID](f.h.IDs)); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown invoice: got = %v, want = %v", err, core.ErrNotFound)
	}
	t.Log("source: spec/graphql/mutations/integrations/hubspot/sync_invoice_spec.rb:61")
	f.h.Store.TakeJobs()
	closed := f.crmInvoice(invoice.StatusClosed, "inv.pdf")
	if err := f.svc.SyncCRMInvoice(ctx, f.h.OrgID(), closed.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("closed invoice: got = %v, want = %v", err, core.ErrNotFound)
	}
	if got := syncJobs(f.h, kernel.JobCreateCRMInvoice); len(got) != 0 {
		t.Errorf("create jobs of the closed invoice: got = %d, want = 0", len(got))
	}
}

func TestCRMInvoiceJobs(t *testing.T) {
	args := func(inv *invoice.Invoice) kernel.InvoiceArgs { return kernel.InvoiceArgs{InvoiceID: inv.ID} }
	t.Run("create", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:146, 153, 163")
		t.Log("source: spec/jobs/integrations/aggregator/invoices/hubspot/create_job_spec.rb:27, 33")
		f := newCRMFixture(t, true)
		inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
		if err := f.run(kernel.JobCreateCRMInvoice, args(inv), 1); err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := f.resource(integration.ResourceTypeInvoice, inv.ID.UUID()); got != "901" {
			t.Errorf("record: got = %q, want = 901", got)
		}
		if len(f.crm.invoices) != 1 {
			t.Fatalf("sent: got = %d, want = 1", len(f.crm.invoices))
		}
		doc := f.crm.invoices[0]
		if doc.RecordID != "" || doc.ID != inv.ID || doc.FileURL != "https://files.test/inv.pdf" || doc.Status != "finalized" {
			t.Errorf("document: got = %+v, want = a new record of the invoice with its file", doc)
		}
		// The properties are deployed first.
		if !slices.Equal(f.crm.deploys, []integration.CRMObject{integration.CRMObjectInvoices}) {
			t.Errorf("deploys: got = %v, want = [invoices]", f.crm.deploys)
		}
		if got := syncJobs(f.h, kernel.JobAssociateCRMInvoice); len(got) != 1 {
			t.Errorf("association jobs: got = %d, want = 1", len(got))
		}
	})
	for _, tt := range []struct {
		name, source string
		sync         bool
		status       invoice.Status
	}{
		{"sync off, draft", "spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:89, 96", false, invoice.StatusDraft},
		{"sync off, finalized", "spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:102, 109", false, invoice.StatusFinalized},
		{"sync on, draft", "spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:119, 126", true, invoice.StatusDraft},
	} {
		t.Run("create/"+tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newCRMFixture(t, tt.sync)
			inv := f.crmInvoice(tt.status, "inv.pdf")
			if err := f.run(kernel.JobCreateCRMInvoice, args(inv), 1); err != nil {
				t.Fatalf("create: %v", err)
			}
			if len(f.crm.invoices) != 0 || f.resource(integration.ResourceTypeInvoice, inv.ID.UUID()) != "" {
				t.Errorf("got = %d sent, record %q, want = nothing", len(f.crm.invoices), f.resource(integration.ResourceTypeInvoice, inv.ID.UUID()))
			}
		})
	}
	t.Run("create/no record returned", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:188, 195")
		f := newCRMFixture(t, true)
		f.crm.id = ""
		inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
		if err := f.run(kernel.JobCreateCRMInvoice, args(inv), 1); err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := f.resource(integration.ResourceTypeInvoice, inv.ID.UUID()); got != "" {
			t.Errorf("record: got = %q, want = none", got)
		}
	})
	for _, tt := range []struct {
		name, source string
		status       int
	}{
		{"server error", "spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:218, 222", 500},
		{"client error", "spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:232, 236, 240", 400},
	} {
		t.Run("create/"+tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newCRMFixture(t, true)
			f.crm.err = httpError(integration.ProviderHubspot, tt.status)
			inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
			if err := f.run(kernel.JobCreateCRMInvoice, args(inv), 1); err != nil {
				t.Fatalf("create: got = %v, want = nil", err)
			}
			if got := f.crmErrors(t); len(got) != 1 || got[0].ErrorCode != "action_script_runtime_error" {
				t.Errorf("webhooks: got = %+v, want = one action_script_runtime_error", got)
			}
		})
	}
	t.Run("create/file missing", func(t *testing.T) {
		t.Log("source: spec/jobs/integrations/aggregator/invoices/hubspot/create_job_spec.rb:21")
		t.Log("source: spec/services/integrations/aggregator/invoices/payloads/hubspot_spec.rb:76")
		f := newCRMFixture(t, true)
		inv := f.crmInvoice(invoice.StatusFinalized, "")
		err := f.run(kernel.JobCreateCRMInvoice, args(inv), 1)
		if !errors.Is(err, integrations.ErrSyncPayload) {
			t.Fatalf("create: got = %v, want = %v", err, integrations.ErrSyncPayload)
		}
		if r, ok := firstRule(kernel.JobCreateCRMInvoice, err); !ok || r.Attempts != 10 {
			t.Errorf("retry: got = %+v, %v, want = 10 attempts", r, ok)
		}
	})
	t.Run("update", func(t *testing.T) {
		// source: spec/jobs/integrations/aggregator/invoices/hubspot/update_job_spec.rb:15
		t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/update_service_spec.rb:112, 119")
		f := newCRMFixture(t, true)
		inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
		f.record(integration.ResourceTypeInvoice, inv.ID.UUID(), "901")
		if err := f.run(kernel.JobUpdateCRMInvoice, args(inv), 1); err != nil {
			t.Fatalf("update: %v", err)
		}
		if len(f.crm.invoices) != 1 || f.crm.invoices[0].RecordID != "901" {
			t.Errorf("sent: got = %+v, want = an update of 901", f.crm.invoices)
		}
		if got := syncJobs(f.h, kernel.JobAssociateCRMInvoice); len(got) != 0 {
			t.Errorf("association jobs: got = %d, want = 0", len(got))
		}
	})
	t.Run("update/file missing", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/invoices/payloads/hubspot_spec.rb:118")
		f := newCRMFixture(t, true)
		inv := f.crmInvoice(invoice.StatusFinalized, "")
		f.record(integration.ResourceTypeInvoice, inv.ID.UUID(), "901")
		if err := f.run(kernel.JobUpdateCRMInvoice, args(inv), 1); !errors.Is(err, integrations.ErrSyncPayload) {
			t.Errorf("update: got = %v, want = %v", err, integrations.ErrSyncPayload)
		}
	})
	t.Run("update/sync off", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/update_service_spec.rb:89")
		f := newCRMFixture(t, false)
		inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
		f.record(integration.ResourceTypeInvoice, inv.ID.UUID(), "901")
		if err := f.run(kernel.JobUpdateCRMInvoice, args(inv), 1); err != nil || len(f.crm.invoices) != 0 {
			t.Errorf("update: got = %v with %d sent, want = nothing", err, len(f.crm.invoices))
		}
	})
	t.Run("update/no record returned", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/update_service_spec.rb:144")
		f := newCRMFixture(t, true)
		f.crm.id = ""
		inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
		f.record(integration.ResourceTypeInvoice, inv.ID.UUID(), "901")
		if err := f.run(kernel.JobUpdateCRMInvoice, args(inv), 1); err != nil {
			t.Errorf("update: %v", err)
		}
	})
	for _, tt := range []struct {
		name, source string
		status       int
	}{
		{"server error", "spec/services/integrations/aggregator/invoices/hubspot/update_service_spec.rb:170, 174", 500},
		{"client error", "spec/services/integrations/aggregator/invoices/hubspot/update_service_spec.rb:184, 188, 192", 400},
	} {
		t.Run("update/"+tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newCRMFixture(t, true)
			f.crm.err = httpError(integration.ProviderHubspot, tt.status)
			inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
			f.record(integration.ResourceTypeInvoice, inv.ID.UUID(), "901")
			if err := f.run(kernel.JobUpdateCRMInvoice, args(inv), 1); err != nil {
				t.Fatalf("update: got = %v, want = nil", err)
			}
			if got := f.crmErrors(t); len(got) != 1 {
				t.Errorf("webhooks: got = %+v, want = one", got)
			}
		})
	}
	t.Run("associate", func(t *testing.T) {
		// source: spec/jobs/integrations/aggregator/invoices/hubspot/create_customer_association_job_spec.rb:16
		t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/create_customer_association_service_spec.rb:73, 78")
		f := newCRMFixture(t, true)
		inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
		f.record(integration.ResourceTypeInvoice, inv.ID.UUID(), "901")
		if err := f.run(kernel.JobAssociateCRMInvoice, args(inv), 1); err != nil {
			t.Fatalf("associate: %v", err)
		}
		want := []integration.CRMAssociation{{Object: integration.CRMObjectInvoices, RecordID: "901", TargetedObject: "companies", CustomerRecordID: "501"}}
		if !slices.Equal(f.crm.associations, want) {
			t.Errorf("associations: got = %+v, want = %+v", f.crm.associations, want)
		}
		if !slices.Equal(f.crm.deploys, []integration.CRMObject{integration.CRMObjectInvoices}) {
			t.Errorf("deploys: got = %v, want = [invoices]", f.crm.deploys)
		}
	})
	t.Run("associate/sync off", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/create_customer_association_service_spec.rb:59")
		f := newCRMFixture(t, false)
		inv := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
		f.record(integration.ResourceTypeInvoice, inv.ID.UUID(), "901")
		if err := f.run(kernel.JobAssociateCRMInvoice, args(inv), 1); err != nil || len(f.crm.associations) != 0 {
			t.Errorf("associate: got = %v with %d associations, want = none", err, len(f.crm.associations))
		}
	})
}

func TestCRMSubscriptionJobs(t *testing.T) {
	args := func(s *subscription.Subscription) kernel.SubscriptionArgs {
		return kernel.SubscriptionArgs{SubscriptionID: s.ID}
	}
	t.Run("create", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/subscriptions/hubspot/create_service_spec.rb:98, 105")
		t.Log("source: spec/jobs/integrations/aggregator/subscriptions/hubspot/create_job_spec.rb:27, 33")
		f := newCRMFixture(t, true)
		f.crm.id = "801"
		if err := f.run(kernel.JobCreateCRMSubscription, args(f.sub), 1); err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := f.resource(integration.ResourceTypeSubscription, f.sub.ID.UUID()); got != "801" {
			t.Errorf("record: got = %q, want = 801", got)
		}
		if len(f.crm.subscriptions) != 1 || f.crm.subscriptions[0].ExternalID != f.sub.ExternalID || f.crm.subscriptions[0].PlanCode == "" {
			t.Errorf("sent: got = %+v, want = the subscription with its plan", f.crm.subscriptions)
		}
		if got := syncJobs(f.h, kernel.JobAssociateCRMSubscription); len(got) != 1 {
			t.Errorf("association jobs: got = %d, want = 1", len(got))
		}
	})
	t.Run("create/sync off", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/subscriptions/hubspot/create_service_spec.rb:70, 77")
		f := newCRMFixture(t, false)
		if err := f.run(kernel.JobCreateCRMSubscription, args(f.sub), 1); err != nil || len(f.crm.subscriptions) != 0 {
			t.Errorf("create: got = %v with %d sent, want = nothing", err, len(f.crm.subscriptions))
		}
	})
	t.Run("create/no record returned", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/subscriptions/hubspot/create_service_spec.rb:124, 131")
		f := newCRMFixture(t, true)
		f.crm.id = ""
		if err := f.run(kernel.JobCreateCRMSubscription, args(f.sub), 1); err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := f.resource(integration.ResourceTypeSubscription, f.sub.ID.UUID()); got != "" {
			t.Errorf("record: got = %q, want = none", got)
		}
	})
	t.Run("create/failure", func(t *testing.T) {
		// The job fails only on what it retries; the service's other
		// failures are webhooks.
		t.Log("source: spec/jobs/integrations/aggregator/subscriptions/hubspot/create_job_spec.rb:21")
		f := newCRMFixture(t, true)
		f.crm.err = &integration.ProviderError{Provider: integration.ProviderHubspot, Kind: integration.KindRequestLimit, HTTPStatus: 429}
		err := f.run(kernel.JobCreateCRMSubscription, args(f.sub), 1)
		if r, ok := firstRule(kernel.JobCreateCRMSubscription, err); !ok || r.Attempts != 100 {
			t.Errorf("retry of %v: got = %+v, %v, want = 100 attempts", err, r, ok)
		}
	})
	for _, tt := range []struct {
		name, create, update string
		status               int
	}{
		{
			"server error", "spec/services/integrations/aggregator/subscriptions/hubspot/create_service_spec.rb:155, 159",
			"spec/services/integrations/aggregator/subscriptions/hubspot/update_service_spec.rb:141, 145", 500,
		},
		{
			"client error", "spec/services/integrations/aggregator/subscriptions/hubspot/create_service_spec.rb:169, 173, 177",
			"spec/services/integrations/aggregator/subscriptions/hubspot/update_service_spec.rb:155, 159, 163", 400,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.create, tt.update)
			f := newCRMFixture(t, true)
			f.crm.err = httpError(integration.ProviderHubspot, tt.status)
			if err := f.run(kernel.JobCreateCRMSubscription, args(f.sub), 1); err != nil {
				t.Fatalf("create: got = %v, want = nil", err)
			}
			f.record(integration.ResourceTypeSubscription, f.sub.ID.UUID(), "801")
			if err := f.run(kernel.JobUpdateCRMSubscription, args(f.sub), 1); err != nil {
				t.Fatalf("update: got = %v, want = nil", err)
			}
			if got := f.crmErrors(t); len(got) != 2 {
				t.Errorf("webhooks: got = %+v, want = two", got)
			}
		})
	}
	t.Run("update", func(t *testing.T) {
		// source: spec/jobs/integrations/aggregator/subscriptions/hubspot/update_job_spec.rb:15
		t.Log("source: spec/services/integrations/aggregator/subscriptions/hubspot/update_service_spec.rb:98")
		f := newCRMFixture(t, true)
		f.crm.id = "801"
		f.record(integration.ResourceTypeSubscription, f.sub.ID.UUID(), "801")
		if err := f.run(kernel.JobUpdateCRMSubscription, args(f.sub), 1); err != nil {
			t.Fatalf("update: %v", err)
		}
		if len(f.crm.subscriptions) != 1 || f.crm.subscriptions[0].RecordID != "801" {
			t.Errorf("sent: got = %+v, want = an update of 801", f.crm.subscriptions)
		}
	})
	t.Run("update/sync off", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/subscriptions/hubspot/update_service_spec.rb:74")
		f := newCRMFixture(t, false)
		f.record(integration.ResourceTypeSubscription, f.sub.ID.UUID(), "801")
		if err := f.run(kernel.JobUpdateCRMSubscription, args(f.sub), 1); err != nil || len(f.crm.subscriptions) != 0 {
			t.Errorf("update: got = %v with %d sent, want = nothing", err, len(f.crm.subscriptions))
		}
	})
	t.Run("update/no record returned", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/subscriptions/hubspot/update_service_spec.rb:114")
		f := newCRMFixture(t, true)
		f.crm.id = ""
		f.record(integration.ResourceTypeSubscription, f.sub.ID.UUID(), "801")
		if err := f.run(kernel.JobUpdateCRMSubscription, args(f.sub), 1); err != nil {
			t.Errorf("update: %v", err)
		}
	})
	t.Run("associate", func(t *testing.T) {
		// source: spec/jobs/integrations/aggregator/subscriptions/hubspot/create_customer_association_job_spec.rb:16
		t.Log("source: spec/services/integrations/aggregator/subscriptions/hubspot/create_customer_association_service_spec.rb:62, 67")
		f := newCRMFixture(t, true)
		f.record(integration.ResourceTypeSubscription, f.sub.ID.UUID(), "801")
		if err := f.run(kernel.JobAssociateCRMSubscription, args(f.sub), 1); err != nil {
			t.Fatalf("associate: %v", err)
		}
		want := []integration.CRMAssociation{{Object: integration.CRMObjectSubscriptions, RecordID: "801", TargetedObject: "companies", CustomerRecordID: "501"}}
		if !slices.Equal(f.crm.associations, want) || !slices.Equal(f.crm.deploys, []integration.CRMObject{integration.CRMObjectSubscriptions}) {
			t.Errorf("got = %+v after deploying %v, want = %+v after deploying subscriptions", f.crm.associations, f.crm.deploys, want)
		}
	})
	t.Run("associate/sync off", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/subscriptions/hubspot/create_customer_association_service_spec.rb:48")
		f := newCRMFixture(t, false)
		f.record(integration.ResourceTypeSubscription, f.sub.ID.UUID(), "801")
		if err := f.run(kernel.JobAssociateCRMSubscription, args(f.sub), 1); err != nil || len(f.crm.associations) != 0 {
			t.Errorf("associate: got = %v with %d associations, want = none", err, len(f.crm.associations))
		}
	})
}

// TestCRMLink checks that the record jobs use the customer's first HubSpot
// link, and skip a customer without one.
func TestCRMLink(t *testing.T) {
	t.Log("source: spec/services/integrations/aggregator/invoices/hubspot/base_service_spec.rb:14, 25")
	t.Log("source: spec/services/integrations/aggregator/subscriptions/hubspot/base_service_spec.rb:15, 26")
	f := newCRMFixture(t, true)
	other := f.h.Customer(func(c *customer.Customer) { c.ExternalID = "other" })
	inv := f.h.StoredInvoice(other, func(i *invoice.Invoice) { i.Status, i.File = invoice.StatusFinalized, "inv.pdf" })
	if err := f.run(kernel.JobCreateCRMInvoice, kernel.InvoiceArgs{InvoiceID: inv.ID}, 1); err != nil || len(f.crm.invoices) != 0 {
		t.Errorf("unlinked customer: got = %v with %d sent, want = nothing", err, len(f.crm.invoices))
	}
	mine := f.crmInvoice(invoice.StatusFinalized, "inv.pdf")
	if err := f.run(kernel.JobCreateCRMInvoice, kernel.InvoiceArgs{InvoiceID: mine.ID}, 1); err != nil || len(f.crm.invoices) != 1 {
		t.Errorf("linked customer: got = %v with %d sent, want = one", err, len(f.crm.invoices))
	}
}

// TestCRMConnectionJobs ports the deployment and portal jobs of a HubSpot
// connection.
func TestCRMConnectionJobs(t *testing.T) {
	ctx := context.Background()
	args := func(f *crmFixture) kernel.IntegrationArgs { return kernel.IntegrationArgs{IntegrationID: f.integ.ID} }
	t.Run("deploy", func(t *testing.T) {
		// source: spec/jobs/integrations/hubspot/companies/deploy_properties_job_spec.rb:16
		// source: spec/jobs/integrations/hubspot/contacts/deploy_properties_job_spec.rb:16
		// source: spec/jobs/integrations/hubspot/invoices/deploy_object_job_spec.rb:16
		// source: spec/jobs/integrations/hubspot/subscriptions/deploy_object_job_spec.rb:16
		t.Log("source: spec/jobs/integrations/aggregator/sync_custom_objects_and_properties_job_spec.rb:18")
		f := newCRMFixture(t, true)
		if err := f.run(kernel.JobDeployCRM, args(f), 1); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		want := []integration.CRMObject{
			integration.CRMObjectInvoices, integration.CRMObjectSubscriptions, integration.CRMObjectCompanies, integration.CRMObjectContacts,
		}
		if got := slices.Clone(f.crm.deploys); !sameObjects(got, want) {
			t.Errorf("deploys: got = %v, want = %v", got, want)
		}
		// The deployed object types and property versions are stored.
		// source: spec/models/integrations/hubspot_integration_spec.rb:54, 61, 68, 75, 82, 89
		i, err := f.svc.Get(ctx, f.h.OrgID(), f.integ.ID)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := f.svc.Connection(ctx, i)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range want {
			if got := conn.Setting(string(o) + "_object_type_id"); got != "2-"+string(o) {
				t.Errorf("%s object type: got = %q, want = 2-%s", o, got, o)
			}
			if got := conn.Setting(string(o) + "_properties_version"); got != "1" {
				t.Errorf("%s properties version: got = %q, want = 1", o, got)
			}
		}
	})
	t.Run("deploy failure", func(t *testing.T) {
		t.Log("source: spec/services/integrations/hubspot/companies/deploy_properties_service_spec.rb:58")
		t.Log("source: spec/services/integrations/hubspot/contacts/deploy_properties_service_spec.rb:58")
		t.Log("source: spec/services/integrations/hubspot/invoices/deploy_properties_service_spec.rb:58")
		t.Log("source: spec/services/integrations/hubspot/subscriptions/deploy_properties_service_spec.rb:58")
		t.Log("source: spec/services/integrations/hubspot/subscriptions/deploy_object_service_spec.rb:70")
		f := newCRMFixture(t, true)
		f.crm.deployErr = &integration.ProviderError{Provider: integration.ProviderHubspot, Kind: integration.KindInvalidRequest, HTTPStatus: 400, Message: "unknown failure"}
		if err := f.run(kernel.JobDeployCRM, args(f), 1); err != nil {
			t.Fatalf("deploy: got = %v, want = nil", err)
		}
		got := f.integrationErrors(t)
		if len(got) != len(f.crm.deploys) || len(got) == 0 {
			t.Fatalf("webhooks: got = %d, want = one per deployment (%d)", len(got), len(f.crm.deploys))
		}
		for _, d := range got {
			if d.ErrorCode != "integration_error" || d.Message != "unknown failure" {
				t.Errorf("webhook: got = %+v, want = integration_error unknown failure", d)
			}
		}
	})
	t.Run("portal", func(t *testing.T) {
		t.Log("source: spec/services/integrations/hubspot/save_portal_id_service_spec.rb:19, 23")
		// source: spec/models/integrations/hubspot_integration_spec.rb:33
		t.Log("source: spec/jobs/integrations/hubspot/save_portal_id_job_spec.rb:17")
		f := newCRMFixture(t, true)
		if err := f.run(kernel.JobSaveCRMAccount, args(f), 1); err != nil {
			t.Fatalf("save portal: %v", err)
		}
		i, err := f.svc.Get(ctx, f.h.OrgID(), f.integ.ID)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := f.svc.Connection(ctx, i)
		if err != nil {
			t.Fatal(err)
		}
		if got := conn.Setting(integrations.KeyPortalID); got != "12345678" {
			t.Errorf("portal: got = %q, want = 12345678", got)
		}
	})
}

func sameObjects(got, want []integration.CRMObject) bool {
	slices.Sort(got)
	w := slices.Clone(want)
	slices.Sort(w)
	return slices.Equal(got, w)
}
