package rpc_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// restInvoice is the JSON of an invoice in a REST response.
type restInvoice struct {
	ID                   string  `json:"id"`
	CustomerID           string  `json:"customer_id"`
	InvoiceType          string  `json:"invoice_type"`
	Status               string  `json:"status"`
	PaymentStatus        string  `json:"payment_status"`
	Currency             string  `json:"currency"`
	FeesAmount           cnMoney `json:"fees_amount"`
	TaxesAmount          cnMoney `json:"taxes_amount"`
	TotalAmount          cnMoney `json:"total_amount"`
	PaymentDisputeLostAt string  `json:"payment_dispute_lost_at"`
}

func invoiceDecode(t *testing.T, body []byte) restInvoice {
	t.Helper()
	return decode[struct {
		Invoice restInvoice `json:"invoice"`
	}](t, body).Invoice
}

// invoiceFixture is a customer of the API's organization.
type invoiceFixture struct {
	s    *rpctest.Server
	cust *customer.Customer
}

func newInvoiceFixture(t *testing.T) *invoiceFixture {
	t.Helper()
	s := rpctest.New(t)
	return &invoiceFixture{s: s, cust: s.Fixtures.Customer()}
}

// stored stores an invoice of the fixture's customer in the status.
func (f *invoiceFixture) stored(status invoice.Status, opts ...func(*invoice.Invoice)) *invoice.Invoice {
	return f.s.Fixtures.StoredInvoice(f.cust, append([]func(*invoice.Invoice){func(inv *invoice.Invoice) { inv.Status = status }}, opts...)...)
}

// call runs the REST request and checks its status and that the body
// contains want.
func (f *invoiceFixture) call(t *testing.T, method, path, body string, status int, want string) []byte {
	t.Helper()
	got, out := f.s.REST(t, f.s.APIKey, method, path, body)
	if got != status || !strings.Contains(string(out), want) {
		t.Fatalf("got = %d %s, want = %d with %q", got, out, status, want)
	}
	return out
}

func missingInvoice() string { return core.NewID[core.InvoiceID](core.UUIDv7Generator{}).String() }

func TestCreateInvoice(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	h := f.s.Fixtures
	first, second := h.AddOn(1000), h.AddOn(400)
	tax := h.Tax("vat_20", "20")
	// source: spec/requests/api/v1/invoices_controller_spec.rb:43
	t.Run("creates an invoice", func(t *testing.T) {
		body := `{"customer_id":"external_id:` + f.cust.ExternalID + `","fees":[` +
			`{"add_on_id":"code:` + first.Code + `","invoice_display_name":"Invoice item #1","unit_amount":"12","units":"2","description":"desc-123","tax_ids":["code:` + tax.Code + `"]},` +
			`{"add_on_id":"code:` + second.Code + `"}]}`
		inv := invoiceDecode(t, f.call(t, http.MethodPost, "/v1/invoices", body, http.StatusOK, ""))
		if inv.InvoiceType != "INVOICE_TYPE_ONE_OFF" || inv.FeesAmount.Amount != "28.00" || inv.Currency != "USD" {
			t.Errorf("invoice got = %+v, want = one-off of 28.00 USD", inv)
		}
		if inv.TaxesAmount.Amount != "4.80" || inv.TotalAmount.Amount != "32.80" {
			t.Errorf("taxes, total = %s, %s, want = 4.80, 32.80", inv.TaxesAmount.Amount, inv.TotalAmount.Amount)
		}
	})
	tests := []struct {
		name, source, body string
		status             int
		want               string
	}{
		{
			"customer does not exist", "spec/requests/api/v1/invoices_controller_spec.rb:65",
			`{"customer_id":"external_id:unknown","fees":[{"add_on_id":"code:` + first.Code + `"}]}`, http.StatusNotFound, "customer_not_found",
		},
		{
			"add-on does not exist", "spec/requests/api/v1/invoices_controller_spec.rb:74",
			`{"customer_id":"` + f.cust.ID.String() + `","fees":[{"add_on_id":"code:` + first.Code + `"},{"add_on_id":"code:invalid"}]}`, http.StatusNotFound, "add_on_not_found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f.call(t, http.MethodPost, "/v1/invoices", tt.body, tt.status, tt.want)
		})
	}
}

func TestGetInvoice(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	inv := f.stored(invoice.StatusFinalized)
	other := rpctest.New(t)
	tests := []struct {
		name, source string
		s            *rpctest.Server
		id           string
		status       int
	}{
		{"returns an invoice", "spec/requests/api/v1/invoices_controller_spec.rb:284", f.s, inv.ID.String(), http.StatusOK},
		{"not found", "spec/requests/api/v1/invoices_controller_spec.rb:354", f.s, missingInvoice(), http.StatusNotFound},
		{"other organization", "spec/requests/api/v1/invoices_controller_spec.rb:363", other, inv.ID.String(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := tt.s.REST(t, tt.s.APIKey, http.MethodGet, "/v1/invoices/"+tt.id, nil)
			if status != tt.status {
				t.Fatalf("status got = %d %s, want = %d", status, body, tt.status)
			}
			if status == http.StatusOK {
				if got := invoiceDecode(t, body); got.ID != tt.id || got.CustomerID != f.cust.ID.String() || got.Status != "INVOICE_STATUS_FINALIZED" {
					t.Errorf("invoice got = %+v, want = finalized %s", got, tt.id)
				}
			}
		})
	}
}

// source: spec/requests/api/v1/invoices_controller_spec.rb:440
func TestListInvoices(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	f.stored(invoice.StatusFinalized)
	f.stored(invoice.StatusDraft)
	f.stored(invoice.StatusGenerating)
	f.s.Fixtures.StoredInvoice(f.s.Fixtures.Customer(), func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized })
	out := decode[struct {
		Invoices []restInvoice `json:"invoices"`
	}](t, f.call(t, http.MethodGet, "/v1/customers/"+f.cust.ID.String()+"/invoices", "", http.StatusOK, ""))
	if len(out.Invoices) != 2 {
		t.Errorf("invoices = %d, want = 2", len(out.Invoices))
	}
	for _, inv := range out.Invoices {
		if inv.CustomerID != f.cust.ID.String() {
			t.Errorf("customer got = %s, want = %s", inv.CustomerID, f.cust.ID)
		}
	}
	all := decode[struct {
		Invoices []restInvoice `json:"invoices"`
	}](t, f.call(t, http.MethodGet, "/v1/invoices?filter=status%3DINVOICE_STATUS_DRAFT", "", http.StatusOK, ""))
	if len(all.Invoices) != 1 || all.Invoices[0].Status != "INVOICE_STATUS_DRAFT" {
		t.Errorf("drafts got = %+v, want = one draft", all.Invoices)
	}
}

func TestUpdateInvoice(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	inv := f.stored(invoice.StatusFinalized)
	voided := f.stored(invoice.StatusVoided)
	body := `{"payment_status":"PAYMENT_STATUS_SUCCEEDED"}`
	tests := []struct {
		name, source, id, body string
		status                 int
		want                   string
	}{
		{"updates an invoice", "spec/requests/api/v1/invoices_controller_spec.rb:220", inv.ID.String(), body, http.StatusOK, `"payment_status":"PAYMENT_STATUS_SUCCEEDED"`},
		{"not found", "spec/requests/api/v1/invoices_controller_spec.rb:228", missingInvoice(), body, http.StatusNotFound, "invoice_not_found"},
		{"voided", "spec/requests/api/v1/invoices_controller_spec.rb:238", voided.ID.String(), body, http.StatusBadRequest, payments.CodeUpdateOnVoidedInvoice},
		{"metadata", "spec/requests/api/v1/invoices_controller_spec.rb:249", inv.ID.String(), `{"metadata":[{"key":"Hello","value":"Hi"}]}`, http.StatusOK, `"Hello"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f.call(t, http.MethodPatch, "/v1/invoices/"+tt.id, tt.body, tt.status, tt.want)
		})
	}
	if got := f.s.Fixtures.Invoice(voided.ID).PaymentStatus; got != voided.PaymentStatus {
		t.Errorf("voided payment status got = %s, want = %s", got, voided.PaymentStatus)
	}
}

func TestInvoiceActions(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	draft := func() string { return f.stored(invoice.StatusDraft).ID.String() }
	finalized := func() string { return f.stored(invoice.StatusFinalized).ID.String() }
	voided := func() string { return f.stored(invoice.StatusVoided).ID.String() }
	generating := func() string { return f.stored(invoice.StatusGenerating).ID.String() }
	deleted := f.stored(invoice.StatusDraft).ID.String()
	f.call(t, http.MethodDelete, "/v1/invoices/"+deleted, "", http.StatusOK, "")
	tests := []struct {
		name, source, method, action string
		id                           func() string
		status                       int
		want                         string
	}{
		{"refresh: missing", "spec/requests/api/v1/invoices_controller_spec.rb:545", http.MethodPost, ":refresh", missingInvoice, http.StatusNotFound, "invoice_not_found"},
		{"refresh: finalized", "spec/requests/api/v1/invoices_controller_spec.rb:571", http.MethodPost, ":refresh", finalized, http.StatusOK, "INVOICE_STATUS_FINALIZED"},
		{"finalize: missing", "spec/requests/api/v1/invoices_controller_spec.rb:593", http.MethodPost, ":finalize", missingInvoice, http.StatusNotFound, "invoice_not_found"},
		{"finalize: not draft", "spec/requests/api/v1/invoices_controller_spec.rb:602", http.MethodPost, ":finalize", finalized, http.StatusNotFound, "invoice_not_found"},
		{"void: missing", "spec/requests/api/v1/invoices_controller_spec.rb:636", http.MethodPost, ":void", missingInvoice, http.StatusNotFound, "invoice_not_found"},
		{"void: draft", "spec/requests/api/v1/invoices_controller_spec.rb:645", http.MethodPost, ":void", draft, http.StatusBadRequest, "not_voidable"},
		{"void: voided", "spec/requests/api/v1/invoices_controller_spec.rb:654", http.MethodPost, ":void", voided, http.StatusBadRequest, "not_voidable"},
		{"void: finalized", "spec/requests/api/v1/invoices_controller_spec.rb:674", http.MethodPost, ":void", finalized, http.StatusOK, "INVOICE_STATUS_VOIDED"},
		{"delete: draft", "spec/requests/api/v1/invoices_controller_spec.rb:736", http.MethodDelete, "", draft, http.StatusOK, ""},
		{"delete: missing", "spec/requests/api/v1/invoices_controller_spec.rb:750", http.MethodDelete, "", missingInvoice, http.StatusNotFound, "invoice_not_found"},
		{"delete: not draft", "spec/requests/api/v1/invoices_controller_spec.rb:759", http.MethodDelete, "", finalized, http.StatusBadRequest, invoicing.CodeNotDeletable},
		{"delete: deleted", "spec/requests/api/v1/invoices_controller_spec.rb:770", http.MethodDelete, "", func() string { return deleted }, http.StatusNotFound, "invoice_not_found"},
		{"lose dispute: missing", "spec/requests/api/v1/invoices_controller_spec.rb:796", http.MethodPost, ":lose_dispute", missingInvoice, http.StatusNotFound, "invoice_not_found"},
		{"lose dispute: finalized", "spec/requests/api/v1/invoices_controller_spec.rb:808", http.MethodPost, ":lose_dispute", finalized, http.StatusOK, "payment_dispute_lost_at"},
		{"lose dispute: voided", "spec/requests/api/v1/invoices_controller_spec.rb:825", http.MethodPost, ":lose_dispute", voided, http.StatusOK, "payment_dispute_lost_at"},
		{"lose dispute: draft", "spec/requests/api/v1/invoices_controller_spec.rb:840", http.MethodPost, ":lose_dispute", draft, http.StatusBadRequest, invoicing.CodeNotDisputable},
		{"lose dispute: generating", "spec/requests/api/v1/invoices_controller_spec.rb:849", http.MethodPost, ":lose_dispute", generating, http.StatusNotFound, "invoice_not_found"},
		{"download: draft", "spec/requests/api/v1/invoices_controller_spec.rb:895", http.MethodPost, ":download", draft, http.StatusNotFound, "invoice_not_found"},
		{"download: without pdf", "spec/requests/api/v1/invoices_controller_spec.rb:871", http.MethodPost, ":download", finalized, http.StatusOK, ""},
		{"retry payment: missing", "spec/requests/api/v1/invoices_controller_spec.rb:989", http.MethodPost, ":retry_payment", missingInvoice, http.StatusNotFound, "invoice_not_found"},
		{"retry: missing", "spec/requests/api/v1/invoices_controller_spec.rb:1033", http.MethodPost, ":retry", missingInvoice, http.StatusNotFound, "invoice_not_found"},
		{"payment url: missing", "spec/requests/api/v1/invoices_controller_spec.rb:1120", http.MethodPost, ":generate_payment_url", missingInvoice, http.StatusNotFound, "invoice_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			body := "{}"
			if tt.method == http.MethodDelete {
				body = ""
			}
			f.call(t, tt.method, "/v1/invoices/"+tt.id()+tt.action, body, tt.status, tt.want)
		})
	}
}

// source: spec/services/invoices/finalize_batch_service_spec.rb:12
func TestFinalizeAllInvoices(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	f.stored(invoice.StatusDraft)
	f.stored(invoice.StatusDraft)
	f.stored(invoice.StatusFinalized)
	f.call(t, http.MethodPost, "/v1/invoices:finalize_all", "{}", http.StatusOK, `"queued_count":2`)
}

// source: spec/services/invoices/retry_batch_service_spec.rb:11
func TestRetryAllInvoices(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	f.stored(invoice.StatusFailed)
	f.stored(invoice.StatusFailed)
	f.stored(invoice.StatusDraft)
	f.call(t, http.MethodPost, "/v1/invoices:retry_all", "{}", http.StatusOK, `"queued_count":2`)
}

// source: spec/services/invoices/payments/retry_batch_service_spec.rb:11
func TestRetryAllInvoicePayments(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	payable := func(ps core.PaymentStatus, ready bool) func(*invoice.Invoice) {
		return func(inv *invoice.Invoice) { inv.PaymentStatus, inv.ReadyForPaymentProcessing = ps, ready }
	}
	f.stored(invoice.StatusFinalized, payable(core.PaymentStatusFailed, true))
	f.stored(invoice.StatusFinalized, payable(core.PaymentStatusPending, true))
	f.stored(invoice.StatusFinalized, payable(core.PaymentStatusFailed, false))
	f.stored(invoice.StatusFinalized, payable(core.PaymentStatusSucceeded, true))
	f.stored(invoice.StatusDraft, payable(core.PaymentStatusPending, true))
	f.call(t, http.MethodPost, "/v1/invoices:retry_all_payments", "{}", http.StatusOK, `"queued_count":2`)
}
