package rpc_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// TestErrorDetails checks that invoices and credit notes carry their
// error details (V1::InvoiceSerializer and V1::CreditNoteSerializer
// error_details).
func TestErrorDetails(t *testing.T) {
	t.Parallel()
	t.Log("source: app/serializers/v1/invoice_serializer.rb:137-142")
	t.Log("source: app/serializers/v1/credit_note_serializer.rb:74-79")
	f := newCNFixture(t)
	h := f.s.Fixtures
	cn := h.StoredCreditNote(f.inv, 10)
	details := json.RawMessage(`{"tax_error":"validationError"}`)
	h.Do(func(ctx context.Context, r port.Repos) error {
		if err := r.ErrorDetails.Create(ctx, &invoice.ErrorDetail{
			ID: core.NewID[core.ErrorDetailID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: f.inv.ID,
			Code: invoice.ErrorCodeTaxError, Details: details, CreatedAt: h.Now(),
		}); err != nil {
			return err
		}
		return r.CreditNoteErrorDetails.Create(ctx, &creditnote.ErrorDetail{
			ID: core.NewID[core.ErrorDetailID](h.IDs), OrganizationID: h.OrgID(), CreditNoteID: cn.ID,
			Code: creditnote.ErrorCodeTaxError, Details: details, CreatedAt: h.Now(),
		})
	})
	tests := []struct {
		name, path, want string
	}{
		{"invoice", "/v1/invoices/" + f.inv.ID.String(), "INVOICE_ERROR_CODE_TAX_ERROR"},
		{"credit note", "/v1/credit_notes/" + cn.ID.String(), "CREDIT_NOTE_ERROR_CODE_TAX_ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, tt.path, nil)
			if status != http.StatusOK {
				t.Fatalf("got status = %d (%s), want = 200", status, body)
			}
			var out map[string]struct {
				Errors []struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"errors"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			var errs []string
			for _, o := range out {
				for _, e := range o.Errors {
					if strings.Contains(e.Message, "validationError") {
						errs = append(errs, e.Code)
					}
				}
			}
			if len(errs) != 1 || errs[0] != tt.want {
				t.Errorf("errors: got = %v (%s), want = [%s]", errs, body, tt.want)
			}
		})
	}
}
