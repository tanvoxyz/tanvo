package invoicing

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CodeStatusNotVoided refuses the retry of the tax voiding of an invoice
// that is not voided.
const CodeStatusNotVoided = "status_not_voided"

// RetryTaxVoidingView tells the tax provider again that the voided
// invoice id was voided and returns the invoice; a provider refusal is
// stored as a tax_voiding_error detail and returned as a tax_error
// validation error (lago RetryTaxProviderVoiding mutation,
// Invoices::ProviderTaxes::VoidService).
func (s *Service) RetryTaxVoidingView(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*InvoiceView, error) {
	var inv *invoice.Invoice
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if inv, err = visibleInvoice(ctx, r, org, id); err != nil {
			return err
		}
		if inv.Status != invoice.StatusVoided {
			return core.NewRuleError(CodeStatusNotVoided, "only voided invoices have their tax voiding retried")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.VoidProviderTaxes(ctx, org, id); err != nil {
		return nil, err
	}
	var code string
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		details, err := r.ErrorDetails.ListByInvoice(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load error details of invoice %s: %w", id, err)
		}
		for _, d := range details {
			if d.Code != invoice.ErrorCodeTaxVoidingError {
				continue
			}
			var p map[string]string
			if json.Unmarshal(d.Details, &p) == nil {
				code = p[string(invoice.ErrorCodeTaxVoidingError)]
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if code != "" {
		return nil, &core.ValidationError{Errors: []core.FieldError{{Field: "tax_error", Code: code}}}
	}
	return s.view(ctx, org, inv, nil)
}
