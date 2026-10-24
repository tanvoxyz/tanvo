package integrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// fakeAccounting is an accounting adapter recording the documents it is
// sent. Each sync answers id and err, each contact call contact and
// contactErr.
type fakeAccounting struct {
	provider integration.Provider

	mu          sync.Mutex
	invoices    []*integration.AccountingInvoice
	creditNotes []*integration.AccountingCreditNote
	payments    []*integration.AccountingPayment
	finds       []string
	contacts    []*integration.Contact

	id  string
	err error

	contact    integration.ContactResult
	contactErr error

	found   string
	findErr error

	// items and accounts answer the fetches; nil accounts are
	// unsupported.
	items, accounts []integration.ExternalRecord

	// subsidiaries answer the subsidiary list; nil is unsupported.
	subsidiaries []integration.ExternalRecord
}

func (g *fakeAccounting) Provider() integration.Provider                  { return g.provider }
func (g *fakeAccounting) ValidateConnection(integration.Connection) error { return nil }

func (g *fakeAccounting) CreateContact(_ context.Context, _ integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.contacts = append(g.contacts, c)
	return g.contact, g.contactErr
}

func (g *fakeAccounting) UpdateContact(_ context.Context, _ integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.contacts = append(g.contacts, c)
	return g.contact, g.contactErr
}

func (g *fakeAccounting) FetchItems(context.Context, integration.Connection) ([]integration.ExternalRecord, error) {
	return g.items, nil
}

func (g *fakeAccounting) FetchAccounts(context.Context, integration.Connection) ([]integration.ExternalRecord, error) {
	if g.accounts == nil {
		return nil, integration.ErrUnsupported
	}
	return g.accounts, nil
}

func (g *fakeAccounting) Subsidiaries(context.Context, integration.Connection) ([]integration.ExternalRecord, error) {
	if g.subsidiaries == nil {
		return nil, integration.ErrUnsupported
	}
	return g.subsidiaries, nil
}

func (g *fakeAccounting) SyncInvoice(_ context.Context, _ integration.Connection, inv *integration.AccountingInvoice) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.invoices = append(g.invoices, inv)
	return g.id, g.err
}

func (g *fakeAccounting) SyncCreditNote(_ context.Context, _ integration.Connection, cn *integration.AccountingCreditNote) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.creditNotes = append(g.creditNotes, cn)
	return g.id, g.err
}

func (g *fakeAccounting) SyncPayment(_ context.Context, _ integration.Connection, p *integration.AccountingPayment) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.payments = append(g.payments, p)
	return g.id, g.err
}

// fakeFinder is a [fakeAccounting] that also finds invoices, as NetSuite
// does.
type fakeFinder struct{ *fakeAccounting }

// FindInvoice implements [integration.InvoiceFinder].
func (g fakeFinder) FindInvoice(_ context.Context, _ integration.Connection, number string) (string, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.finds = append(g.finds, number)
	return g.found, g.found != "", g.findErr
}

// taxContacts is a tax adapter whose contact calls answer as acct's, as
// Avalara's do.
type taxContacts struct {
	*fakeTax
	acct *fakeAccounting
}

func (g taxContacts) CreateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	return g.acct.CreateContact(ctx, conn, c)
}

func (g taxContacts) UpdateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	return g.acct.UpdateContact(ctx, conn, c)
}

// acctFixture is a customer linked to an accounting integration syncing
// invoices, credit notes and payments, whose fallback item is "1", with a
// charge of a plan it subscribed to.
type acctFixture struct {
	h      *billingtest.Harness
	svc    *integrations.Service
	gw     *fakeAccounting
	integ  *integration.Integration
	cust   *customer.Customer
	ic     *integration.IntegrationCustomer
	sub    *subscription.Subscription
	charge *catalog.Charge
}

// newAcctFixture returns the fixture of p; fallback adds the fallback
// item "1".
func newAcctFixture(t *testing.T, p integration.Provider, fallback bool) *acctFixture {
	t.Helper()
	ctx := context.Background()
	h := billingtest.New(t, now)
	gw := &fakeAccounting{provider: p, id: "ext_1"}
	var adapter integration.Gateway = gw
	in := integrations.Input{Code: string(p), Name: string(p), Provider: p, SyncInvoices: true, SyncCreditNotes: true, SyncPayments: true}
	switch p {
	case integration.ProviderNetsuite:
		adapter = fakeFinder{gw}
		in.Values = netsuiteInput().Values
	case integration.ProviderAvalara:
		adapter = taxContacts{&fakeTax{provider: p}, gw}
		in.SyncInvoices, in.SyncCreditNotes, in.SyncPayments = false, false, false
		in.Values = map[string]string{
			integrations.KeyAccountID: "acc", integrations.KeyCompanyCode: "DEFAULT", integrations.KeyLicenseKey: "lic",
		}
	}
	set, err := port.NewIntegrationProviderSet(adapter)
	if err != nil {
		t.Fatal(err)
	}
	svc := integrations.New(h.Deps, sealer{}, integrations.WithProviders(set))
	integ, err := svc.Create(ctx, h.OrgID(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	c := h.Customer()
	ic := &integration.IntegrationCustomer{
		ID: core.NewID[core.IntegrationCustomerID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: integ.ID,
		CustomerID: c.ID, Category: integration.CategoryAccounting, ExternalCustomerID: "cus_ext", SyncWithProvider: true,
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error {
		if err := r.IntegrationCustomers.Create(ctx, ic); err != nil {
			return err
		}
		if !fallback {
			return nil
		}
		one := "1"
		return r.IntegrationCollectionMappings.Create(ctx, &integration.CollectionMapping{
			ID: core.NewID[core.IntegrationCollectionMappingID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: integ.ID,
			Type: integration.CollectionMappingTypeFallbackItem, ExternalID: &one, CreatedAt: h.Now(), UpdatedAt: h.Now(),
		})
	})
	m := h.Metric("api", catalog.AggregationTypeCount, "")
	plan := h.Plan(period.Monthly, 0)
	ch := h.Charge(plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	return &acctFixture{h: h, svc: svc, gw: gw, integ: integ, cust: c, ic: ic, sub: h.Subscription(c, plan), charge: ch}
}

// invoice stores a finalized invoice with a charge fee.
func (f *acctFixture) invoice() *invoice.Invoice {
	inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
	f.h.StoredFee(inv, 1000, billingtest.ChargeFee(f.sub, f.charge.ID))
	return inv
}

// creditNote stores a finalized credit note crediting the fees of inv.
func (f *acctFixture) creditNote(inv *invoice.Invoice) *creditnote.CreditNote {
	fees := f.h.Fees(inv.ID)
	return f.h.StoredCreditNote(inv, 100, func(cn *creditnote.CreditNote) {
		for _, fe := range fees {
			cn.Items = append(cn.Items, creditnote.Item{
				ID: core.NewID[core.CreditNoteItemID](f.h.IDs), FeeID: fe.ID, Amount: 100,
			})
		}
	})
}

func (f *acctFixture) run(kind string, args any, attempt int) error {
	b, err := json.Marshal(args)
	if err != nil {
		f.h.T.Fatal(err)
	}
	ok, err := f.svc.HandleJob(context.Background(), port.Job{Kind: kind, OrganizationID: f.h.OrgID(), Args: b}, attempt)
	if !ok {
		f.h.T.Fatalf("HandleJob(%s): not handled", kind)
	}
	return err
}

// resource returns the external ID recorded for syncable, "" when none.
func (f *acctFixture) resource(rt integration.ResourceType, syncable uuid.UUID) string {
	var out string
	f.h.Do(func(ctx context.Context, r port.Repos) error {
		res, err := r.IntegrationResources.Find(ctx, f.h.OrgID(), f.integ.ID, rt, syncable)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		out = res.ExternalID
		return nil
	})
	return out
}

// record stores the external ID ext for syncable.
func (f *acctFixture) record(rt integration.ResourceType, syncable uuid.UUID, ext string) {
	f.h.Do(func(ctx context.Context, r port.Repos) error {
		return r.IntegrationResources.Create(ctx, &integration.Resource{
			ID: core.NewID[core.IntegrationResourceID](f.h.IDs), OrganizationID: f.h.OrgID(), IntegrationID: f.integ.ID,
			ResourceType: rt, SyncableID: syncable, ExternalID: ext, CreatedAt: f.h.Now(),
		})
	})
}

// customerErrors returns the provider errors of the
// customer.accounting_provider_error webhooks.
func (f *acctFixture) customerErrors(t *testing.T) []integrations.ProviderErrorDetails {
	t.Helper()
	var out []integrations.ProviderErrorDetails
	for _, e := range f.h.Emitted.Events() {
		if e.Type != webhook.EventTypeCustomerAccountingProviderError {
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

func syncJobs(h *billingtest.Harness, kind string) []port.Job {
	var out []port.Job
	for _, j := range h.Store.Jobs() {
		if j.Kind == kind {
			out = append(out, j)
		}
	}
	return out
}

func httpError(p integration.Provider, status int) error {
	kind := integration.KindInvalidRequest
	if status >= 500 {
		kind = integration.KindUnavailable
	}
	return &integration.ProviderError{
		Provider: p, Kind: kind, HTTPStatus: status,
		Code: "action_script_runtime_error", Message: "submitFields: Missing a required argument: type",
	}
}

// firstRule returns the first retry rule of kind matching err.
func firstRule(kind string, err error) (integrations.Retry, bool) {
	for _, r := range integrations.RetryRules(kind) {
		if r.Match(err) {
			return r, true
		}
	}
	return integrations.Retry{}, false
}

func TestSyncEnqueue(t *testing.T) {
	ctx := context.Background()
	t.Run("finalized invoice", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:312
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		inv := f.invoice()
		err := f.svc.Triggers(f.h.Deps.Webhooks).Emit(ctx, webhook.Event{
			ID: core.NewID[core.AuditEventID](f.h.IDs), OrganizationID: f.h.OrgID(), Type: webhook.EventTypeInvoiceCreated,
			ObjectType: "invoice", ObjectID: inv.ID.UUID(), Payload: json.RawMessage(`{}`), OccurredAt: f.h.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		jobs := syncJobs(f.h, kernel.JobSyncInvoice)
		if len(jobs) != 1 {
			t.Fatalf("jobs: got = %d, want = 1", len(jobs))
		}
		var a kernel.SyncInvoiceArgs
		if err := json.Unmarshal(jobs[0].Args, &a); err != nil {
			t.Fatal(err)
		}
		if a != (kernel.SyncInvoiceArgs{InvoiceID: inv.ID}) {
			t.Errorf("args: got = %+v, want = find_first false", a)
		}
	})
	t.Run("resync of an invoice", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:321
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		inv := f.invoice()
		if err := f.svc.SyncInvoice(ctx, f.h.OrgID(), inv.ID); err != nil {
			t.Fatal(err)
		}
		jobs := syncJobs(f.h, kernel.JobSyncInvoice)
		var a kernel.SyncInvoiceArgs
		if len(jobs) != 1 || json.Unmarshal(jobs[0].Args, &a) != nil || a != (kernel.SyncInvoiceArgs{InvoiceID: inv.ID, FindFirst: true}) {
			t.Errorf("jobs: got = %v, want = one with find_first", jobs)
		}
	})
	t.Run("resync of a credit note", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/credit_notes/create_service_spec.rb:250
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		cn := f.creditNote(f.invoice())
		if err := f.svc.SyncCreditNote(ctx, f.h.OrgID(), cn.ID); err != nil {
			t.Fatal(err)
		}
		if got := len(syncJobs(f.h, kernel.JobSyncCreditNote)); got != 1 {
			t.Errorf("jobs: got = %d, want = 1", got)
		}
	})
	t.Run("payment", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/payments/create_service_spec.rb:69
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		inv := f.invoice()
		p := f.h.StoredPayment(payment.InvoicePayable(inv.ID), f.cust.ID, 1000, payment.StateSucceeded, core.IntegrationID{})
		f.h.Do(func(ctx context.Context, r port.Repos) error { return f.svc.EnqueuePaymentSync(ctx, r, p, inv) })
		if got := len(syncJobs(f.h, kernel.JobSyncPayment)); got != 1 {
			t.Errorf("jobs: got = %d, want = 1", got)
		}
	})
	t.Run("missing invoice", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:332
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		if err := f.svc.SyncInvoice(ctx, f.h.OrgID(), core.NewID[core.InvoiceID](f.h.IDs)); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("SyncInvoice() error = %v, want = not found", err)
		}
	})
	t.Run("missing credit note", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/credit_notes/create_service_spec.rb:258
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		if err := f.svc.SyncCreditNote(ctx, f.h.OrgID(), core.NewID[core.CreditNoteID](f.h.IDs)); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("SyncCreditNote() error = %v, want = not found", err)
		}
	})
}

func TestSyncInvoiceJob(t *testing.T) {
	sync := func(f *acctFixture, inv *invoice.Invoice, findFirst bool, attempt int) error {
		return f.run(kernel.JobSyncInvoice, kernel.SyncInvoiceArgs{InvoiceID: inv.ID, FindFirst: findFirst}, attempt)
	}
	t.Run("already synced", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:351
		// source: spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:92, 97, 101, 105
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		inv := f.invoice()
		f.record(integration.ResourceTypeInvoice, inv.ID.UUID(), "old")
		if err := sync(f, inv, true, 2); err != nil {
			t.Fatal(err)
		}
		if len(f.gw.finds) != 0 || len(f.gw.invoices) != 0 || f.resource(integration.ResourceTypeInvoice, inv.ID.UUID()) != "old" {
			t.Errorf("got = %d lookups, %d creations, want = none", len(f.gw.finds), len(f.gw.invoices))
		}
	})
	for _, tt := range []struct {
		name, source string
		findFirst    bool
		attempt      int
		found        string
		wantFinds    int
		want         string
	}{
		{"first execution", "spec/jobs/integrations/aggregator/invoices/create_job_spec.rb:43", false, 1, "", 0, "ext_1"},
		{"find first, not found", "spec/jobs/integrations/aggregator/invoices/create_job_spec.rb:21", true, 1, "", 1, "ext_1"},
		{"find first, found", "spec/jobs/integrations/aggregator/invoices/create_job_spec.rb:32", true, 1, "12345", 1, "12345"},
		{"retry, not found", "spec/jobs/integrations/aggregator/invoices/create_job_spec.rb:57", false, 2, "", 1, "ext_1"},
		{"retry, found", "spec/jobs/integrations/aggregator/invoices/create_job_spec.rb:68", false, 2, "12345", 1, "12345"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newAcctFixture(t, integration.ProviderNetsuite, true)
			f.gw.found = tt.found
			inv := f.invoice()
			if err := sync(f, inv, tt.findFirst, tt.attempt); err != nil {
				t.Fatal(err)
			}
			wantCreated := 1
			if tt.found != "" {
				wantCreated = 0
			}
			if len(f.gw.finds) != tt.wantFinds || len(f.gw.invoices) != wantCreated {
				t.Errorf("got = %d lookups, %d creations, want = %d, %d", len(f.gw.finds), len(f.gw.invoices), tt.wantFinds, wantCreated)
			}
			if tt.wantFinds > 0 && f.gw.finds[0] != inv.Number {
				t.Errorf("lookup: got = %q, want = %q", f.gw.finds[0], inv.Number)
			}
			if got := f.resource(integration.ResourceTypeInvoice, inv.ID.UUID()); got != tt.want {
				t.Errorf("resource: got = %q, want = %q", got, tt.want)
			}
		})
	}
	t.Run("not found records nothing", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:73, 77, 81
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		f.gw.id = ""
		inv := f.invoice()
		if err := sync(f, inv, true, 1); err != nil {
			t.Fatal(err)
		}
		if got := f.resource(integration.ResourceTypeInvoice, inv.ID.UUID()); len(f.gw.finds) != 1 || got != "" {
			t.Errorf("got = %d lookups, resource %q, want = 1, none", len(f.gw.finds), got)
		}
	})
	for _, tt := range []struct {
		name, source string
		err          error
		want         error
	}{
		{
			"server error", "spec/jobs/integrations/aggregator/invoices/create_job_spec.rb:83; spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:155, 159",
			httpError(integration.ProviderNetsuite, 500), integrations.ErrSyncRetryable,
		},
		{
			"client error", "spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:168, 172",
			httpError(integration.ProviderNetsuite, 400), integrations.ErrSyncRetryable,
		},
		{
			"request limit", "spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:187",
			&integration.ProviderError{Provider: integration.ProviderNetsuite, Kind: integration.KindRequestLimit, HTTPStatus: 429}, integration.ErrRequestLimit,
		},
	} {
		t.Run("lookup "+tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newAcctFixture(t, integration.ProviderNetsuite, true)
			f.gw.findErr = tt.err
			inv := f.invoice()
			err := sync(f, inv, false, 2)
			if !errors.Is(err, tt.want) {
				t.Errorf("error: got = %v, want = %v", err, tt.want)
			}
			if _, ok := firstRule(kernel.JobSyncInvoice, err); !ok {
				t.Errorf("rule: got = none, want = a retry")
			}
			if len(f.gw.invoices) != 0 || f.resource(integration.ResourceTypeInvoice, inv.ID.UUID()) != "" {
				t.Errorf("got = %d creations, want = none", len(f.gw.invoices))
			}
		})
	}
	t.Run("lookup failure not retried", func(t *testing.T) {
		// source: spec/jobs/integrations/aggregator/invoices/create_job_spec.rb:93
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		f.gw.findErr = errors.New("bad request")
		inv := f.invoice()
		err := sync(f, inv, false, 2)
		if err == nil {
			t.Fatal("error: got = nil, want = the failure")
		}
		if r, ok := firstRule(kernel.JobSyncInvoice, err); ok {
			t.Errorf("rule: got = %+v, want = none", r)
		}
		if len(f.gw.invoices) != 0 {
			t.Errorf("got = %d creations, want = none", len(f.gw.invoices))
		}
	})
	t.Run("no lookup outside NetSuite", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:116, 121, 125
		f := newAcctFixture(t, integration.ProviderXero, true)
		inv := f.invoice()
		if err := sync(f, inv, true, 2); err != nil {
			t.Fatal(err)
		}
		if len(f.gw.finds) != 0 || len(f.gw.invoices) != 1 {
			t.Errorf("got = %d lookups, %d creations, want = 0, 1", len(f.gw.finds), len(f.gw.invoices))
		}
	})
	t.Run("no accounting integration", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:133
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		f.h.Do(func(ctx context.Context, r port.Repos) error {
			return r.IntegrationCustomers.Delete(ctx, f.h.OrgID(), f.ic.ID)
		})
		if err := sync(f, f.invoice(), true, 2); err != nil {
			t.Fatal(err)
		}
		if len(f.gw.finds) != 0 || len(f.gw.invoices) != 0 {
			t.Errorf("got = %d lookups, %d creations, want = none", len(f.gw.finds), len(f.gw.invoices))
		}
	})
	for _, tt := range []struct {
		name, source string
		provider     integration.Provider
		delay        time.Duration
	}{
		{"NetSuite timeout", "spec/jobs/integrations/aggregator/invoices/create_job_spec.rb:111", integration.ProviderNetsuite, 6 * time.Minute},
		{"Xero timeout", "spec/jobs/integrations/aggregator/invoices/create_job_spec.rb:128", integration.ProviderXero, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newAcctFixture(t, tt.provider, true)
			f.gw.err = &integration.ProviderError{Provider: tt.provider, Kind: integration.KindUnavailable, Message: "read timeout"}
			err := sync(f, f.invoice(), false, 1)
			if !errors.Is(err, integrations.ErrSyncTimeout) {
				t.Fatalf("error: got = %v, want = timeout", err)
			}
			r, ok := firstRule(kernel.JobSyncInvoice, err)
			if !ok || r.Attempts != 6 || r.Delay != tt.delay {
				t.Errorf("rule: got = %+v, want = 6 attempts, delay %v", r, tt.delay)
			}
			if got := len(f.customerErrors(t)); got != 0 {
				t.Errorf("webhooks: got = %d, want = none", got)
			}
		})
	}
}

func TestSyncCreditNoteJob(t *testing.T) {
	// source: spec/jobs/integrations/aggregator/credit_notes/create_job_spec.rb:15
	sync := func(f *acctFixture, cn *creditnote.CreditNote) error {
		return f.run(kernel.JobSyncCreditNote, kernel.CreditNoteArgs{CreditNoteID: cn.ID}, 1)
	}
	t.Run("already synced", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/credit_notes/create_service_spec.rb:280
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		cn := f.creditNote(f.invoice())
		f.record(integration.ResourceTypeCreditNote, cn.ID.UUID(), "old")
		if err := sync(f, cn); err != nil {
			t.Fatal(err)
		}
		if len(f.gw.creditNotes) != 0 {
			t.Errorf("got = %d creations, want = none", len(f.gw.creditNotes))
		}
	})
	t.Run("created", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/credit_notes/create_service_spec.rb:304, 311, 352, 359
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		f.gw.id = "456"
		cn := f.creditNote(f.invoice())
		if err := sync(f, cn); err != nil {
			t.Fatal(err)
		}
		if got := f.resource(integration.ResourceTypeCreditNote, cn.ID.UUID()); got != "456" {
			t.Errorf("resource: got = %q, want = 456", got)
		}
	})
	t.Run("not created", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/credit_notes/create_service_spec.rb:331, 338
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		f.gw.err = &integration.ProviderError{
			Provider: integration.ProviderNetsuite, Kind: integration.KindInvalidRequest, HTTPStatus: 200,
			Code: integration.CodeValidationError, Message: "Account code is not valid",
		}
		cn := f.creditNote(f.invoice())
		if err := sync(f, cn); err != nil {
			t.Fatal(err)
		}
		if got := f.resource(integration.ResourceTypeCreditNote, cn.ID.UUID()); got != "" {
			t.Errorf("resource: got = %q, want = none", got)
		}
	})
	for _, tt := range []struct {
		name, source string
		status       int
		want         error
	}{
		{"server error", "spec/services/integrations/aggregator/credit_notes/create_service_spec.rb:389, 395", 500, integrations.ErrSyncRetryable},
		{"client error", "spec/services/integrations/aggregator/credit_notes/create_service_spec.rb:403, 407", 400, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newAcctFixture(t, integration.ProviderNetsuite, true)
			f.gw.err = httpError(integration.ProviderNetsuite, tt.status)
			cn := f.creditNote(f.invoice())
			err := sync(f, cn)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("error: got = %v, want = %v", err, tt.want)
			}
			want := []integrations.ProviderErrorDetails{{ErrorCode: "action_script_runtime_error", Message: "submitFields: Missing a required argument: type"}}
			if got := f.customerErrors(t); !slices.Equal(got, want) {
				t.Errorf("webhooks: got = %v, want = %v", got, want)
			}
		})
	}
	t.Run("payload error", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/credit_notes/create_service_spec.rb:446, 450
		f := newAcctFixture(t, integration.ProviderXero, false)
		cn := f.creditNote(f.invoice())
		if err := sync(f, cn); err != nil {
			t.Fatal(err)
		}
		want := []integrations.ProviderErrorDetails{{ErrorCode: "invalid_mapping", Message: "Invalid mapping"}}
		if got := f.customerErrors(t); !slices.Equal(got, want) || len(f.gw.creditNotes) != 0 {
			t.Errorf("got = %v, %d creations, want = %v, none", got, len(f.gw.creditNotes), want)
		}
	})
}

func TestSyncPaymentJob(t *testing.T) {
	// newPayment returns a succeeded payment of a synced invoice.
	newPayment := func(f *acctFixture) *payment.Payment {
		inv := f.invoice()
		f.record(integration.ResourceTypeInvoice, inv.ID.UUID(), "inv_ext")
		return f.h.StoredPayment(payment.InvoicePayable(inv.ID), f.cust.ID, 1000, payment.StateSucceeded, core.IntegrationID{})
	}
	// source: spec/jobs/integrations/aggregator/payments/create_job_spec.rb:15
	sync := func(f *acctFixture, p *payment.Payment) error {
		return f.run(kernel.JobSyncPayment, kernel.PaymentArgs{PaymentID: p.ID}, 1)
	}
	t.Run("already synced", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/payments/create_service_spec.rb:99
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		p := newPayment(f)
		f.record(integration.ResourceTypePayment, p.ID.UUID(), "old")
		if err := sync(f, p); err != nil {
			t.Fatal(err)
		}
		if len(f.gw.payments) != 0 {
			t.Errorf("got = %d creations, want = none", len(f.gw.payments))
		}
	})
	t.Run("created", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/payments/create_service_spec.rb:122, 129, 149, 156
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		f.gw.id = "999"
		p := newPayment(f)
		if err := sync(f, p); err != nil {
			t.Fatal(err)
		}
		if got := f.resource(integration.ResourceTypePayment, p.ID.UUID()); got != "999" {
			t.Errorf("resource: got = %q, want = 999", got)
		}
		if d := f.gw.payments[0]; d.InvoiceExternalID != "inv_ext" || d.ExternalCustomerID != "cus_ext" || d.AmountCents != 1000 {
			t.Errorf("document: got = %+v, want = invoice inv_ext, customer cus_ext, 1000", d)
		}
	})
	t.Run("not created", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/payments/create_service_spec.rb:175, 182
		f := newAcctFixture(t, integration.ProviderNetsuite, true)
		f.gw.err = &integration.ProviderError{
			Provider: integration.ProviderNetsuite, Kind: integration.KindInvalidRequest, HTTPStatus: 200,
			Code: integration.CodeValidationError, Message: "Payment amount exceeds the invoice",
		}
		p := newPayment(f)
		if err := sync(f, p); err != nil {
			t.Fatal(err)
		}
		if got := f.resource(integration.ResourceTypePayment, p.ID.UUID()); got != "" {
			t.Errorf("resource: got = %q, want = none", got)
		}
		want := []integrations.ProviderErrorDetails{{ErrorCode: "Validation error", Message: "Payment amount exceeds the invoice"}}
		if got := f.customerErrors(t); !slices.Equal(got, want) {
			t.Errorf("webhooks: got = %v, want = %v", got, want)
		}
	})
	for _, tt := range []struct {
		name, source string
		status       int
		want         error
	}{
		{"server error", "spec/services/integrations/aggregator/payments/create_service_spec.rb:206, 212", 500, integrations.ErrSyncRetryable},
		{"client error", "spec/services/integrations/aggregator/payments/create_service_spec.rb:220, 224, 228", 400, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newAcctFixture(t, integration.ProviderNetsuite, true)
			f.gw.err = httpError(integration.ProviderNetsuite, tt.status)
			err := sync(f, newPayment(f))
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("error: got = %v, want = %v", err, tt.want)
			}
			if got := len(f.customerErrors(t)); got != 1 {
				t.Errorf("webhooks: got = %d, want = 1", got)
			}
		})
	}
}

// TestAccountingMappingFallback checks the item of each kind of fee
// synced to NetSuite: the mapping of the billing entity, then its fallback
// item, then the organization's mapping, then its fallback item (lago
// spec/support/shared_examples/an_integration_payload.rb).
func TestAccountingMappingFallback(t *testing.T) {
	type kind int
	const (
		addOn kind = iota
		fixedCharge
		metric
		commitment
		subscriptionFee
	)
	kinds := []kind{addOn, fixedCharge, metric, commitment, subscriptionFee}
	names := []string{"add_on", "fixed_charge", "billable_metric", "minimum_commitment", "subscription"}
	for _, tt := range []struct {
		name                string
		mappings, fallbacks []bool // on the billing entity, on the organization
		want                func(k kind) string
	}{
		{"mappings of the billing entity", []bool{true, true}, []bool{true, true}, func(k kind) string { return names[k] + "_on_billing_entity" }},
		{"fallback item of the billing entity", []bool{false, true}, []bool{true, true}, func(kind) string { return "fallback_item_on_billing_entity" }},
		{"mappings of the organization", []bool{false, true}, []bool{false, true}, func(k kind) string { return names[k] + "_on_organization" }},
		{"fallback item of the organization", []bool{false, false}, []bool{false, true}, func(kind) string { return "fallback_item_on_organization" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/integrations/aggregator/credit_notes/payloads/netsuite_spec.rb:51
			// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:728
			f := newAcctFixture(t, integration.ProviderNetsuite, false)
			h := f.h
			be := f.cust.BillingEntityID
			fcAddOn := h.AddOn(0)
			fc := h.FixedCharge(h.Plan(period.Monthly, 0), fcAddOn, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1")
			ao := h.AddOn(0)
			objects := map[kind]struct {
				t  integration.MappableType
				id uuid.UUID
			}{
				addOn:       {integration.MappableTypeAddOn, ao.ID.UUID()},
				fixedCharge: {integration.MappableTypeAddOn, fcAddOn.ID.UUID()},
				metric:      {integration.MappableTypeBillableMetric, f.charge.BillableMetricID.UUID()},
			}
			collections := map[kind]integration.CollectionMappingType{
				commitment: integration.CollectionMappingTypeMinimumCommitment, subscriptionFee: integration.CollectionMappingTypeSubscriptionFee,
			}
			h.Do(func(ctx context.Context, r port.Repos) error {
				for i, on := range tt.mappings {
					if !on {
						continue
					}
					var mbe core.BillingEntityID
					suffix := "_on_organization"
					if i == 0 {
						mbe, suffix = be, "_on_billing_entity"
					}
					for _, k := range kinds {
						ext := names[k] + suffix
						if o, ok := objects[k]; ok {
							if err := r.IntegrationMappings.Create(ctx, &integration.Mapping{
								ID: core.NewID[core.IntegrationMappingID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: f.integ.ID,
								MappableType: o.t, MappableID: o.id, BillingEntityID: mbe, ExternalID: ext, CreatedAt: h.Now(), UpdatedAt: h.Now(),
							}); err != nil {
								return err
							}
							continue
						}
						if err := r.IntegrationCollectionMappings.Create(ctx, &integration.CollectionMapping{
							ID: core.NewID[core.IntegrationCollectionMappingID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: f.integ.ID,
							Type: collections[k], BillingEntityID: mbe, ExternalID: &ext, CreatedAt: h.Now(), UpdatedAt: h.Now(),
						}); err != nil {
							return err
						}
					}
				}
				for i, on := range tt.fallbacks {
					if !on {
						continue
					}
					var mbe core.BillingEntityID
					ext := "fallback_item_on_organization"
					if i == 0 {
						mbe, ext = be, "fallback_item_on_billing_entity"
					}
					if err := r.IntegrationCollectionMappings.Create(ctx, &integration.CollectionMapping{
						ID: core.NewID[core.IntegrationCollectionMappingID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: f.integ.ID,
						Type: integration.CollectionMappingTypeFallbackItem, BillingEntityID: mbe, ExternalID: &ext, CreatedAt: h.Now(), UpdatedAt: h.Now(),
					}); err != nil {
						return err
					}
				}
				return nil
			})
			inv := h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
			fees := []*invoice.Fee{
				h.StoredFee(inv, 200, func(fe *invoice.Fee) { fe.Type, fe.AddOnID = core.FeeTypeAddOn, ao.ID }),
				h.StoredFee(inv, 150, func(fe *invoice.Fee) {
					fe.Type, fe.SubscriptionID, fe.FixedChargeID = core.FeeTypeFixedCharge, f.sub.ID, fc.ID
				}),
				h.StoredFee(inv, 300, billingtest.ChargeFee(f.sub, f.charge.ID)),
				h.StoredFee(inv, 400, func(fe *invoice.Fee) { fe.Type, fe.SubscriptionID = core.FeeTypeCommitment, f.sub.ID }),
				h.StoredFee(inv, 500, billingtest.SubscriptionFee(f.sub)),
			}
			check := func(what string, got []integration.AccountingFee) {
				t.Helper()
				if len(got) != len(kinds) {
					t.Fatalf("%s fees: got = %d, want = %d", what, len(got), len(kinds))
				}
				for i, af := range got {
					if af.ID != fees[i].ID {
						t.Fatalf("%s fee %d: got = %s, want = %s", what, i, af.ID, fees[i].ID)
					}
					if af.Item == nil || af.Item.ExternalID != tt.want(kinds[i]) {
						t.Errorf("%s %s item: got = %+v, want = %s", what, names[kinds[i]], af.Item, tt.want(kinds[i]))
					}
				}
			}
			if err := f.run(kernel.JobSyncInvoice, kernel.SyncInvoiceArgs{InvoiceID: inv.ID}, 1); err != nil {
				t.Fatal(err)
			}
			check("invoice", f.gw.invoices[0].Fees)
			cn := f.creditNote(inv)
			if err := f.run(kernel.JobSyncCreditNote, kernel.CreditNoteArgs{CreditNoteID: cn.ID}, 1); err != nil {
				t.Fatal(err)
			}
			var items []integration.AccountingFee
			for _, it := range f.gw.creditNotes[0].Items {
				items = append(items, it.Fee)
			}
			check("credit note", items)
		})
	}
}

func TestAccountingGroupedBy(t *testing.T) {
	// source: spec/services/integrations/aggregator/invoices/payloads/xero_spec.rb:229
	f := newAcctFixture(t, integration.ProviderXero, true)
	inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
	f.h.StoredFee(inv, 1000, billingtest.ChargeFee(f.sub, f.charge.ID), func(fe *invoice.Fee) {
		name := "Storage usage"
		fe.InvoiceDisplayName = &name
		fe.GroupedBy = map[string]string{"region": "eu", "tier": "gold"}
	})
	if err := f.run(kernel.JobSyncInvoice, kernel.SyncInvoiceArgs{InvoiceID: inv.ID}, 1); err != nil {
		t.Fatal(err)
	}
	af := f.gw.invoices[0].Fees[0]
	if got := af.InvoiceName + af.GroupedByDisplay; got != "Storage usage • eu • gold" && got != "Storage usage • gold • eu" {
		t.Errorf("description: got = %q, want = Storage usage • eu • gold", got)
	}
}

// The invoice document lists its fees oldest first; the adapters then keep
// the ones they send (providers/netsuite TestPositiveFees).
//
// source: spec/services/integrations/aggregator/invoices/payloads/base_payload_spec.rb:25
// source: spec/services/integrations/aggregator/invoices/payloads/base_payload_spec.rb:39
// source: spec/services/integrations/aggregator/invoices/payloads/base_payload_spec.rb:54
func TestAccountingInvoiceFeeOrder(t *testing.T) {
	f := newAcctFixture(t, integration.ProviderNetsuite, true)
	inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
	at := func(d time.Duration) func(*invoice.Fee) {
		return func(fe *invoice.Fee) { fe.CreatedAt = f.h.Now().Add(d) }
	}
	newer := f.h.StoredFee(inv, 1000, billingtest.ChargeFee(f.sub, f.charge.ID), at(-24*time.Hour))
	older := f.h.StoredFee(inv, 2000, billingtest.ChargeFee(f.sub, f.charge.ID), at(-48*time.Hour))
	if err := f.run(kernel.JobSyncInvoice, kernel.SyncInvoiceArgs{InvoiceID: inv.ID}, 1); err != nil {
		t.Fatal(err)
	}
	if len(f.gw.invoices) != 1 {
		t.Fatalf("invoices sent: got = %d, want = 1", len(f.gw.invoices))
	}
	var got []core.FeeID
	for _, fe := range f.gw.invoices[0].Fees {
		got = append(got, fe.ID)
	}
	if want := []core.FeeID{older.ID, newer.ID}; !slices.Equal(got, want) {
		t.Errorf("fees: got = %v, want = %v", got, want)
	}
}

func TestFetchItemsAndAccounts(t *testing.T) {
	ctx := context.Background()
	f := newAcctFixture(t, integration.ProviderNetsuite, false)
	stale := &integration.Item{
		ID: core.NewID[core.IntegrationItemID](f.h.IDs), OrganizationID: f.h.OrgID(), IntegrationID: f.integ.ID,
		ItemType: integration.ItemTypeStandard, ExternalID: "old", CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
	}
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.IntegrationItems.Upsert(ctx, stale) })
	ids := func(items []*integration.Item) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.ExternalID)
		}
		return out
	}
	stored := func(typ integration.ItemType) []string {
		p := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (core.Page[*integration.Item], error) {
			return r.IntegrationItems.List(ctx, f.h.OrgID(), f.integ.ID, integration.ItemFilter{ItemType: typ}, core.PageRequest{})
		})
		return ids(p.Items)
	}
	t.Run("items", func(t *testing.T) {
		// The fetched items replace the cached ones, in the provider's order.
		// source: spec/graphql/mutations/integration_items/fetch_items_spec.rb:40
		f.gw.items = []integration.ExternalRecord{{ID: "755", Name: "Item 1"}, {ID: "745"}, {ID: "753", AccountCode: "4000"}}
		got, err := f.svc.FetchItems(ctx, f.h.OrgID(), f.integ.ID)
		if err != nil {
			t.Fatalf("FetchItems: %v", err)
		}
		if want := []string{"755", "745", "753"}; !slices.Equal(ids(got), want) {
			t.Errorf("fetched: got = %v, want = %v", ids(got), want)
		}
		if got := stored(integration.ItemTypeStandard); len(got) != 3 || slices.Contains(got, "old") {
			t.Errorf("stored: got = %v, want = the 3 fetched items", got)
		}
		if got[0].ExternalName == nil || *got[0].ExternalName != "Item 1" || got[2].ExternalAccountCode == nil || *got[2].ExternalAccountCode != "4000" {
			t.Errorf("fields: got = %+v, want = the name and account code", got)
		}
	})
	t.Run("accounts", func(t *testing.T) {
		// source: spec/graphql/mutations/integration_items/fetch_accounts_spec.rb:44
		f.gw.accounts = []integration.ExternalRecord{{ID: "a1"}, {ID: "a2"}, {ID: "a3"}}
		got, err := f.svc.FetchAccounts(ctx, f.h.OrgID(), f.integ.ID)
		if err != nil {
			t.Fatalf("FetchAccounts: %v", err)
		}
		if want := []string{"a1", "a2", "a3"}; !slices.Equal(ids(got), want) || len(stored(integration.ItemTypeAccount)) != 3 {
			t.Errorf("got = %v (%d stored), want = %v", ids(got), len(stored(integration.ItemTypeAccount)), want)
		}
		if got := stored(integration.ItemTypeStandard); len(got) != 3 {
			t.Errorf("items: got = %v, want = kept", got)
		}
	})
	t.Run("subsidiaries", func(t *testing.T) {
		// Listed from the provider, not stored.
		// source: spec/graphql/resolvers/integrations/subsidiaries_resolver_spec.rb:49
		if _, err := f.svc.Subsidiaries(ctx, f.h.OrgID(), f.integ.ID); !errors.Is(err, integration.ErrUnsupported) {
			t.Errorf("unsupported: got = %v, want = ErrUnsupported", err)
		}
		f.gw.subsidiaries = []integration.ExternalRecord{{ID: "1", Name: "Holo, Inc."}, {ID: "3"}, {ID: "4"}, {ID: "2"}}
		got, err := f.svc.Subsidiaries(ctx, f.h.OrgID(), f.integ.ID)
		if err != nil {
			t.Fatalf("Subsidiaries: %v", err)
		}
		if len(got) != 4 || got[0].ID != "1" || got[0].Name != "Holo, Inc." {
			t.Errorf("got = %+v, want = 4 subsidiaries, the first 1 Holo, Inc.", got)
		}
		if _, err := f.svc.Subsidiaries(ctx, f.h.OrgID(), core.NewID[core.IntegrationID](f.h.IDs)); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("unknown integration: got = %v, want = not found", err)
		}
	})
	t.Run("job", func(t *testing.T) {
		// The job Xero's connection starts fetches the items again.
		// source: spec/jobs/integrations/aggregator/fetch_items_job_spec.rb:15
		// source: spec/jobs/integrations/aggregator/perform_sync_job_spec.rb:25
		f.gw.items = []integration.ExternalRecord{{ID: "828"}}
		args, _ := json.Marshal(kernel.IntegrationArgs{IntegrationID: f.integ.ID})
		if _, err := f.svc.HandleJob(ctx, port.Job{Kind: kernel.JobFetchIntegrationItems, OrganizationID: f.h.OrgID(), Args: args}, 1); err != nil {
			t.Fatalf("HandleJob: %v", err)
		}
		if got := stored(integration.ItemTypeStandard); !slices.Equal(got, []string{"828"}) {
			t.Errorf("items: got = %v, want = [828]", got)
		}
	})
}
