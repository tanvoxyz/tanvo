package rpc_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

func TestInvoiceIntegrationSyncs(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	inv := f.stored(invoice.StatusFinalized)
	voided := f.stored(invoice.StatusVoided)
	createIntegration(t, f.s, `{"code":"sf1","name":"Salesforce","salesforce":{"instance_id":"inst-1"}}`)
	id := inv.ID.String()
	tests := []struct {
		name, source, path, body string
		session                  bool
		status                   int
		want                     string
	}{
		{
			// source: spec/graphql/mutations/integrations/sync_invoice_spec.rb:53
			"retries the accounting sync", "app/graphql/mutations/integrations/sync_invoice.rb:18",
			"/v1/invoices/" + id + ":retry_sync", `{}`, false, http.StatusOK, "",
		},
		{
			"retry sync of a missing invoice", "app/services/integrations/aggregator/invoices/create_service.rb (invoice not found)",
			"/v1/invoices/" + missingInvoice() + ":retry_sync", `{}`, false, http.StatusNotFound, "",
		},
		{
			"sets the salesforce id", "spec/requests/api/v1/invoices_controller_spec.rb:1068",
			"/v1/invoices/" + id + ":set_salesforce_id", `{"integration_id":"code:sf1","external_id":"a0B1"}`, false, http.StatusOK, id,
		},
		{
			"salesforce id of a missing invoice", "spec/requests/api/v1/invoices_controller_spec.rb:1079",
			"/v1/invoices/" + missingInvoice() + ":set_salesforce_id", `{"integration_id":"code:sf1","external_id":"a0B1"}`, false, http.StatusNotFound, "",
		},
		{
			"salesforce id with an unknown integration", "spec/services/invoices/sync_salesforce_id_service_spec.rb (integration not found)",
			"/v1/invoices/" + id + ":set_salesforce_id", `{"integration_id":"code:unknown","external_id":"a0B1"}`, false, http.StatusNotFound, "",
		},
		{
			"tax voiding of an invoice not voided", "app/services/invoices/provider_taxes/void_service.rb:16",
			"/v1/invoices/" + id + ":retry_tax_voiding", `{}`, false, http.StatusBadRequest, "status_not_voided",
		},
		{
			"tax voiding of a voided invoice", "app/services/invoices/provider_taxes/void_service.rb:42-44",
			"/v1/invoices/" + voided.ID.String() + ":retry_tax_voiding", `{}`, false, http.StatusOK, voided.ID.String(),
		},
		{
			"hubspot invoice sync", "app/graphql/mutations/integrations/hubspot/sync_invoice.rb:20-22",
			"/v1/invoices/" + id + ":sync_hubspot", `{}`, true, http.StatusOK, "",
		},
		{
			"salesforce invoice sync", "app/graphql/mutations/integrations/salesforce/sync_invoice.rb:20-22",
			"/v1/invoices/" + id + ":sync_salesforce", `{}`, true, http.StatusOK, "",
		},
		{
			"salesforce sync of a missing invoice", "app/services/integrations/salesforce/invoices/sync_service.rb (invoice not found)",
			"/v1/invoices/" + missingInvoice() + ":sync_salesforce", `{}`, true, http.StatusNotFound, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			cred := f.s.APIKey
			if tt.session {
				cred = f.s.Session
			}
			status, body := f.s.REST(t, cred, http.MethodPost, tt.path, tt.body)
			if status != tt.status || (tt.want != "" && !strings.Contains(string(body), tt.want)) {
				t.Errorf("got = %d %s, want = %d with %q", status, body, tt.status, tt.want)
			}
		})
	}
}

func TestRetryCreditNoteSync(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	cn := f.s.Fixtures.StoredCreditNote(f.stored(invoice.StatusFinalized), 100)
	t.Log("source: app/graphql/mutations/integrations/sync_credit_note.rb")
	t.Log("source: spec/graphql/mutations/integrations/sync_credit_note_spec.rb:53")
	for _, tt := range []struct {
		id     string
		status int
	}{
		{cn.ID.String(), http.StatusOK},
		{core.NewID[core.CreditNoteID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
	} {
		if status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/credit_notes/"+tt.id+":retry_sync", `{}`); status != tt.status {
			t.Errorf("%s: got = %d %s, want = %d", tt.id, status, body, tt.status)
		}
	}
}

func TestRetryCreditNoteTaxReporting(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	cn := f.s.Fixtures.StoredCreditNote(f.stored(invoice.StatusFinalized), 100)
	for _, tt := range []struct {
		source, id string
		status     int
	}{
		{"spec/services/credit_notes/provider_taxes/report_service_spec.rb:111", cn.ID.String(), http.StatusOK},
		{"spec/services/credit_notes/provider_taxes/report_service_spec.rb:102", core.NewID[core.CreditNoteID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
	} {
		t.Logf("source: %s", tt.source)
		status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/credit_notes/"+tt.id+":retry_tax_reporting", `{}`)
		if status != tt.status || (tt.status == http.StatusOK && !strings.Contains(string(body), tt.id)) {
			t.Errorf("%s: got = %d %s, want = %d", tt.id, status, body, tt.status)
		}
	}
}
