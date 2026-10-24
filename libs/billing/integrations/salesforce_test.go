package integrations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// salesforceFixture is a Salesforce integration and an invoice.
func salesforceFixture(t *testing.T) (*billingtest.Harness, *integrations.Service, *integration.Integration) {
	t.Helper()
	h := billingtest.New(t, now)
	svc := integrations.New(h.Deps, sealer{})
	integ, err := svc.Create(context.Background(), h.OrgID(), integrations.Input{
		Code: "salesforce", Name: "Salesforce", Provider: integration.ProviderSalesforce,
		Values: map[string]string{integrations.KeyInstanceID: "https://example.my.salesforce.com"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return h, svc, integ
}

func TestResyncSalesforceInvoice(t *testing.T) {
	ctx := context.Background()
	t.Run("unknown invoice", func(t *testing.T) {
		// source: spec/services/integrations/salesforce/invoices/sync_service_spec.rb:12
		// source: spec/services/integrations/salesforce/invoices/sync_service_spec.rb:20
		h, svc, _ := salesforceFixture(t)
		_, err := svc.ResyncSalesforceInvoice(ctx, h.OrgID(), core.NewID[core.InvoiceID](h.IDs))
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("error: got = %v, want = not found", err)
		}
		if got := h.Webhooks(); len(got) != 0 {
			t.Errorf("webhooks: got = %v, want = none", got)
		}
	})
	t.Run("invoice", func(t *testing.T) {
		// source: spec/services/integrations/salesforce/invoices/sync_service_spec.rb:28
		// source: spec/services/integrations/salesforce/invoices/sync_service_spec.rb:34
		// source: spec/graphql/mutations/integrations/salesforce/sync_invoice_spec.rb:38
		h, svc, _ := salesforceFixture(t)
		inv := h.StoredInvoice(h.Customer(), func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
		got, err := svc.ResyncSalesforceInvoice(ctx, h.OrgID(), inv.ID)
		if err != nil {
			t.Fatalf("ResyncSalesforceInvoice: %v", err)
		}
		if got.ID != inv.ID {
			t.Errorf("invoice: got = %s, want = %s", got.ID, inv.ID)
		}
		if w := h.Webhooks(); len(w) != 1 || w[0] != webhook.EventTypeInvoiceResynced {
			t.Errorf("webhooks: got = %v, want = [invoice.resynced]", w)
		}
	})
	t.Run("invoice not visible", func(t *testing.T) {
		// source: spec/graphql/mutations/integrations/salesforce/sync_invoice_spec.rb:42
		h, svc, _ := salesforceFixture(t)
		for _, st := range []invoice.Status{invoice.StatusGenerating, invoice.StatusOpen, invoice.StatusClosed} {
			inv := h.StoredInvoice(h.Customer(), func(i *invoice.Invoice) { i.Status = st })
			if _, err := svc.ResyncSalesforceInvoice(ctx, h.OrgID(), inv.ID); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("%s: got = %v, want = not found", st, err)
			}
		}
		if w := h.Webhooks(); len(w) != 0 {
			t.Errorf("webhooks: got = %v, want = none", w)
		}
	})
}

func TestSetInvoiceSalesforceID(t *testing.T) {
	ctx := context.Background()
	t.Run("unknown invoice", func(t *testing.T) {
		// source: spec/services/invoices/sync_salesforce_id_service_spec.rb:17
		h, svc, integ := salesforceFixture(t)
		_, err := svc.SetInvoiceSalesforceID(ctx, h.OrgID(), core.NewID[core.InvoiceID](h.IDs), integ.Code, "1234")
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("error: got = %v, want = not found", err)
		}
	})
	t.Run("unknown integration", func(t *testing.T) {
		// source: spec/services/invoices/sync_salesforce_id_service_spec.rb:26
		h, svc, _ := salesforceFixture(t)
		inv := h.StoredInvoice(h.Customer())
		_, err := svc.SetInvoiceSalesforceID(ctx, h.OrgID(), inv.ID, "other", "1234")
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("error: got = %v, want = not found", err)
		}
	})
	t.Run("records the resource", func(t *testing.T) {
		// source: spec/services/invoices/sync_salesforce_id_service_spec.rb:43
		h, svc, integ := salesforceFixture(t)
		inv := h.StoredInvoice(h.Customer())
		got, err := svc.SetInvoiceSalesforceID(ctx, h.OrgID(), inv.ID, integ.Code, "1234")
		if err != nil {
			t.Fatalf("SetInvoiceSalesforceID: %v", err)
		}
		if got.ID != inv.ID {
			t.Errorf("invoice: got = %s, want = %s", got.ID, inv.ID)
		}
		res := billingtest.Read(h, func(ctx context.Context, r port.Repos) (*integration.Resource, error) {
			return r.IntegrationResources.Find(ctx, h.OrgID(), integ.ID, integration.ResourceTypeInvoice, inv.ID.UUID())
		})
		if res.ExternalID != "1234" {
			t.Errorf("external id: got = %q, want = 1234", res.ExternalID)
		}
		// One record per synced object: a second ID keeps the first.
		if _, err := svc.SetInvoiceSalesforceID(ctx, h.OrgID(), inv.ID, integ.Code, "5678"); err != nil {
			t.Fatalf("SetInvoiceSalesforceID again: %v", err)
		}
		res = billingtest.Read(h, func(ctx context.Context, r port.Repos) (*integration.Resource, error) {
			return r.IntegrationResources.Find(ctx, h.OrgID(), integ.ID, integration.ResourceTypeInvoice, inv.ID.UUID())
		})
		if res.ExternalID != "1234" {
			t.Errorf("external id: got = %q, want = 1234", res.ExternalID)
		}
	})
}
