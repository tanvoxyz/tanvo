package rpc_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// cnMoney is the JSON of a Money.
type cnMoney struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// restCreditNote is the JSON of a credit note in a REST response.
type restCreditNote struct {
	ID           string  `json:"id"`
	InvoiceID    string  `json:"invoice_id"`
	CustomerID   string  `json:"customer_id"`
	Status       string  `json:"status"`
	CreditStatus string  `json:"credit_status"`
	RefundStatus string  `json:"refund_status"`
	Reason       string  `json:"reason"`
	Description  string  `json:"description"`
	CreditAmount cnMoney `json:"credit_amount"`
	RefundAmount cnMoney `json:"refund_amount"`
	OffsetAmount cnMoney `json:"offset_amount"`
	Balance      cnMoney `json:"balance_amount"`
	TotalAmount  cnMoney `json:"total_amount"`
	Items        []struct {
		FeeID  string `json:"fee_id"`
		Amount string `json:"amount"`
	} `json:"items"`
	Metadata []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"metadata"`
}

// cnFixture is a customer with a paid USD invoice of two fees of 0.10,
// 0.05 of it paid.
type cnFixture struct {
	s    *rpctest.Server
	cust *customer.Customer
	inv  *invoice.Invoice
	fees []*invoice.Fee
}

func newCNFixture(t *testing.T) *cnFixture {
	t.Helper()
	s := rpctest.New(t)
	f := &cnFixture{s: s, cust: s.Fixtures.Customer()}
	f.inv, f.fees = s.Fixtures.CreditableInvoice(f.cust, []int64{10, 10}, nil,
		[]func(*invoice.Invoice){func(inv *invoice.Invoice) { inv.TotalPaidAmount = 5 }})
	return f
}

func cnDecode(t *testing.T, body []byte) restCreditNote {
	t.Helper()
	return decode[struct {
		CreditNote restCreditNote `json:"credit_note"`
	}](t, body).CreditNote
}

func TestCreateCreditNote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   func(f *cnFixture) string
		status int
		check  func(t *testing.T, f *cnFixture, cn restCreditNote)
	}{
		{
			name:   "creates a credit note",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:423",
			body: func(f *cnFixture) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_DUPLICATED_CHARGE","description":"Duplicated charge",
					"credit_amount":{"currency":"USD","amount":"0.10"},"refund_amount":{"currency":"USD","amount":"0.05"},
					"items":[{"fee_id":%q,"amount":"0.10"},{"fee_id":%q,"amount":"0.05"}]}`, f.inv.ID, f.fees[0].ID, f.fees[1].ID)
			},
			status: http.StatusOK,
			check: func(t *testing.T, f *cnFixture, cn restCreditNote) {
				if cn.CreditStatus != "CREDIT_STATUS_AVAILABLE" || cn.RefundStatus != "REFUND_STATUS_PENDING" ||
					cn.Reason != "CREDIT_NOTE_REASON_DUPLICATED_CHARGE" || cn.Description != "Duplicated charge" {
					t.Errorf("got %+v, want an available note pending refund for a duplicated charge", cn)
				}
				if cn.TotalAmount != (cnMoney{"USD", "0.15"}) || cn.CreditAmount != (cnMoney{"USD", "0.10"}) ||
					cn.Balance != (cnMoney{"USD", "0.10"}) || cn.RefundAmount != (cnMoney{"USD", "0.05"}) {
					t.Errorf("got total = %v, credit = %v, balance = %v, refund = %v, want = 0.15, 0.10, 0.10, 0.05",
						cn.TotalAmount, cn.CreditAmount, cn.Balance, cn.RefundAmount)
				}
				if cn.CustomerID != f.cust.ID.String() || len(cn.Items) != 2 || cn.Items[0].FeeID != f.fees[0].ID.String() ||
					cn.Items[0].Amount != "0.10" || cn.Items[1].Amount != "0.05" {
					t.Errorf("got customer = %s, items = %+v, want = %s, [0.10 0.05]", cn.CustomerID, cn.Items, f.cust.ID)
				}
			},
		},
		{
			name:   "invoice not found",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:455",
			body: func(f *cnFixture) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","items":[{"fee_id":%q,"amount":"0.10"}]}`,
					core.NewID[core.InvoiceID](core.UUIDv7Generator{}), f.fees[0].ID)
			},
			status: http.StatusNotFound,
		},
		{
			name:   "with metadata",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:502",
			body: func(f *cnFixture) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","credit_amount":{"currency":"USD","amount":"0.10"},
					"items":[{"fee_id":%q,"amount":"0.10"}],"metadata":[{"key":"source","value":"api"}]}`, f.inv.ID, f.fees[0].ID)
			},
			status: http.StatusOK,
			check: func(t *testing.T, _ *cnFixture, cn restCreditNote) {
				if len(cn.Metadata) != 1 || cn.Metadata[0].Key != "source" || cn.Metadata[0].Value != "api" {
					t.Errorf("got metadata = %+v, want = [source: api]", cn.Metadata)
				}
			},
		},
		{
			name:   "with offset amount",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:549",
			body: func(f *cnFixture) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","offset_amount":{"currency":"USD","amount":"0.10"},
					"items":[{"fee_id":%q,"amount":"0.10"}]}`, f.inv.ID, f.fees[0].ID)
			},
			status: http.StatusOK,
			check: func(t *testing.T, _ *cnFixture, cn restCreditNote) {
				if cn.OffsetAmount != (cnMoney{"USD", "0.10"}) || cn.TotalAmount != (cnMoney{"USD", "0.10"}) {
					t.Errorf("got offset = %v, total = %v, want both 0.10", cn.OffsetAmount, cn.TotalAmount)
				}
			},
		},
		{
			name:   "amount in another currency",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:423",
			body: func(f *cnFixture) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","credit_amount":{"currency":"EUR","amount":"0.10"},
					"items":[{"fee_id":%q,"amount":"0.10"}]}`, f.inv.ID, f.fees[0].ID)
			},
			status: http.StatusBadRequest,
		},
		{
			name:   "item amount below the minor unit",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:423",
			body: func(f *cnFixture) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","items":[{"fee_id":%q,"amount":"0.105"}]}`,
					f.inv.ID, f.fees[0].ID)
			},
			status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newCNFixture(t)
			status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/credit_notes", tt.body(f))
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, f, cnDecode(t, body))
			}
		})
	}
}

func TestGetCreditNote(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	cn := f.s.Fixtures.StoredCreditNote(f.inv, 10)
	draft := f.s.Fixtures.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
	tests := []struct {
		name   string
		source string
		id     string
		status int
	}{
		{"returns a credit note", "spec/requests/api/v1/credit_notes_controller_spec.rb:32", cn.ID.String(), http.StatusOK},
		{"not found", "spec/requests/api/v1/credit_notes_controller_spec.rb:78", core.NewID[core.CreditNoteID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
		{"draft", "spec/requests/api/v1/credit_notes_controller_spec.rb:87", draft.ID.String(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/credit_notes/"+tt.id, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := cnDecode(t, body)
			if got.ID != cn.ID.String() || got.InvoiceID != f.inv.ID.String() || got.Status != "CREDIT_NOTE_STATUS_FINALIZED" ||
				got.CreditAmount != (cnMoney{"USD", "0.10"}) {
				t.Errorf("got %+v, want the finalized note %s of 0.10", got, cn.ID)
			}
		})
	}
}

func TestUpdateCreditNote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		draft  bool
		status int
		check  func(t *testing.T, cn restCreditNote)
	}{
		{
			name:   "updates the refund status",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:153",
			body:   `{"refund_status":"REFUND_STATUS_SUCCEEDED"}`,
			status: http.StatusOK,
			check: func(t *testing.T, cn restCreditNote) {
				if cn.RefundStatus != "REFUND_STATUS_SUCCEEDED" {
					t.Errorf("got refund_status = %s, want = REFUND_STATUS_SUCCEEDED", cn.RefundStatus)
				}
			},
		},
		{
			name:   "invalid refund status",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:175",
			body:   `{"refund_status":"REFUND_STATUS_UNSPECIFIED"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "draft",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:184",
			body:   `{"refund_status":"REFUND_STATUS_SUCCEEDED"}`,
			draft:  true,
			status: http.StatusNotFound,
		},
		{
			name:   "merges metadata",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:198",
			body:   `{"metadata":[{"key":"bar","value":"baz"}]}`,
			status: http.StatusOK,
			check: func(t *testing.T, cn restCreditNote) {
				got := map[string]string{}
				for _, m := range cn.Metadata {
					got[m.Key] = m.Value
				}
				if len(got) != 2 || got["foo"] != "qux" || got["bar"] != "baz" {
					t.Errorf("got metadata = %v, want = map[bar:baz foo:qux]", got)
				}
			},
		},
		{
			name:   "updates an existing key",
			source: "spec/requests/api/v1/credit_notes_controller_spec.rb:209",
			body:   `{"metadata":[{"key":"foo","value":"new"}]}`,
			status: http.StatusOK,
			check: func(t *testing.T, cn restCreditNote) {
				if len(cn.Metadata) != 1 || cn.Metadata[0].Key != "foo" || cn.Metadata[0].Value != "new" {
					t.Errorf("got metadata = %+v, want = [foo: new]", cn.Metadata)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newCNFixture(t)
			cn := f.s.Fixtures.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) {
				cn.RefundAmount, cn.RefundStatus = 5, core.RefundStatusPending
				cn.Metadata = core.Metadata{{Key: "foo", Value: "qux"}}
				if tt.draft {
					cn.Status = creditnote.StatusDraft
				}
			})
			status, body := f.s.REST(t, f.s.APIKey, http.MethodPatch, "/v1/credit_notes/"+cn.ID.String(), tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, cnDecode(t, body))
			}
		})
	}
}

func TestVoidCreditNote(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	cn := f.s.Fixtures.StoredCreditNote(f.inv, 10)
	consumed := f.s.Fixtures.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) {
		cn.BalanceAmount, cn.CreditStatus = 0, creditnote.CreditStatusConsumed
	})
	tests := []struct {
		name   string
		source string
		id     string
		status int
	}{
		{"voids the credit note", "spec/requests/api/v1/credit_notes_controller_spec.rb:696", cn.ID.String(), http.StatusOK},
		{"not found", "spec/requests/api/v1/credit_notes_controller_spec.rb:709", core.NewID[core.CreditNoteID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
		{"not voidable", "spec/requests/api/v1/credit_notes_controller_spec.rb:718", consumed.ID.String(), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/credit_notes/"+tt.id+":void", "{}")
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			if got := cnDecode(t, body); got.CreditStatus != "CREDIT_STATUS_VOIDED" || got.Balance != (cnMoney{"USD", "0.00"}) {
				t.Errorf("got credit_status = %s, balance = %v, want = CREDIT_STATUS_VOIDED, 0.00", got.CreditStatus, got.Balance)
			}
		})
	}
}

func TestEstimateCreditNote(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	tests := []struct {
		name    string
		source  string
		invoice string
		status  int
	}{
		{"computes the amounts", "spec/requests/api/v1/credit_notes_controller_spec.rb:756", f.inv.ID.String(), http.StatusOK},
		{"invalid invoice", "spec/requests/api/v1/credit_notes_controller_spec.rb:777", core.NewID[core.InvoiceID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			body := fmt.Sprintf(`{"invoice_id":%q,"items":[{"fee_id":%q,"amount":"0.05"}]}`, tt.invoice, f.fees[0].ID)
			status, out := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/credit_notes:estimate", body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, out, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[struct {
				CreditNote     restCreditNote `json:"credit_note"`
				MaxCreditable  cnMoney        `json:"max_creditable_amount"`
				MaxRefundable  cnMoney        `json:"max_refundable_amount"`
				MaxOffsettable cnMoney        `json:"max_offsettable_amount"`
			}](t, out)
			// 0.05 is creditable; 0.05 was paid, so refundable; 0.15 is due.
			if got.MaxCreditable != (cnMoney{"USD", "0.05"}) || got.MaxRefundable != (cnMoney{"USD", "0.05"}) ||
				got.MaxOffsettable != (cnMoney{"USD", "0.05"}) {
				t.Errorf("got max credit = %v, refund = %v, offset = %v, want 0.05 each", got.MaxCreditable, got.MaxRefundable, got.MaxOffsettable)
			}
			if len(got.CreditNote.Items) != 1 || got.CreditNote.Items[0].Amount != "0.05" {
				t.Errorf("got items = %+v, want one of 0.05", got.CreditNote.Items)
			}
		})
	}
}

func TestDownloadCreditNote(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	cn := f.s.Fixtures.StoredCreditNote(f.inv, 10)
	withFile := f.s.Fixtures.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.File = "credit_notes/cn.pdf" })
	draft := f.s.Fixtures.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
	tests := []struct {
		name   string
		source string
		id     string
		status int
		pdf    bool
	}{
		{"enqueues the document job", "spec/requests/api/v1/credit_notes_controller_spec.rb:250", cn.ID.String(), http.StatusOK, false},
		{"file attached", "spec/requests/api/v1/credit_notes_controller_spec.rb:260", withFile.ID.String(), http.StatusOK, true},
		{"not found", "spec/requests/api/v1/credit_notes_controller_spec.rb:271", core.NewID[core.CreditNoteID](core.UUIDv7Generator{}).String(), http.StatusNotFound, false},
		{"draft", "spec/requests/api/v1/credit_notes_controller_spec.rb:280", draft.ID.String(), http.StatusNotFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/credit_notes/"+tt.id+":download", "{}")
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[struct {
				PDF *struct {
					ContentType string `json:"content_type"`
				} `json:"pdf"`
			}](t, body)
			if (got.PDF != nil) != tt.pdf || (tt.pdf && got.PDF.ContentType != "application/pdf") {
				t.Errorf("got pdf = %+v (%s), want present = %v", got.PDF, body, tt.pdf)
			}
		})
	}
	var jobs int
	if err := f.s.Admin.QueryRow(t.Context(), `SELECT count(*) FROM job_outbox WHERE kind = 'documents.generate_credit_note'`).Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobs != 1 {
		t.Errorf("got document jobs = %d, want = 1", jobs)
	}
}

func TestListCreditNotes(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	voided := f.s.Fixtures.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) {
		cn.CreditStatus, cn.BalanceAmount = creditnote.CreditStatusVoided, 0
	})
	refund := f.s.Fixtures.StoredCreditNote(f.inv, 30, func(cn *creditnote.CreditNote) {
		cn.CreditAmount, cn.BalanceAmount, cn.RefundAmount, cn.RefundStatus = 0, 0, 30, core.RefundStatusSucceeded
		cn.CreditStatus, cn.Reason = "", creditnote.ReasonOrderChange
	})
	f.s.Fixtures.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
	other := f.s.Fixtures.Customer()
	otherInv, _ := f.s.Fixtures.CreditableInvoice(other, []int64{10}, nil, nil)
	elsewhere := f.s.Fixtures.StoredCreditNote(otherInv, 10)
	tests := []struct {
		name   string
		source string
		path   string
		status int
		want   []string
	}{
		{
			"finalized notes, newest first", "spec/requests/api/v1/credit_notes_controller_spec.rb:353", "/v1/credit_notes", http.StatusOK,
			[]string{elsewhere.ID.String(), refund.ID.String(), voided.ID.String()},
		},
		{
			"customer", "spec/requests/api/v1/credit_notes_controller_spec.rb:366", "/v1/customers/" + other.ID.String() + "/credit_notes", http.StatusOK,
			[]string{elsewhere.ID.String()},
		},
		{
			"external customer id", "spec/requests/api/v1/credit_notes_controller_spec.rb:366", "/v1/customers/external_id%3A" + other.ExternalID + "/credit_notes", http.StatusOK,
			[]string{elsewhere.ID.String()},
		},
		{
			"credit status", "spec/queries/credit_notes_query_spec.rb", "/v1/credit_notes?filter=credit_status%3DVOIDED", http.StatusOK,
			[]string{voided.ID.String()},
		},
		{
			"refund status and reason", "spec/queries/credit_notes_query_spec.rb", "/v1/credit_notes?filter=refund_status%3DSUCCEEDED%20AND%20reason%3DORDER_CHANGE", http.StatusOK,
			[]string{refund.ID.String()},
		},
		{
			"type", "spec/queries/credit_notes_query_spec.rb", "/v1/credit_notes?filter=type%3Drefund", http.StatusOK,
			[]string{refund.ID.String()},
		},
		{
			"amount range", "spec/queries/credit_notes_query_spec.rb", "/v1/credit_notes?filter=amount%3E%3D20%20AND%20amount%3C%3D30", http.StatusOK,
			[]string{refund.ID.String()},
		},
		{"unknown field", "spec/queries/credit_notes_query_spec.rb", "/v1/credit_notes?filter=status%3Ddraft", http.StatusBadRequest, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, tt.path, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			var got []string
			for _, cn := range decode[struct {
				CreditNotes []restCreditNote `json:"credit_notes"`
			}](t, body).CreditNotes {
				got = append(got, cn.ID)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got = %v, want = %v", got, tt.want)
			}
		})
	}
}
