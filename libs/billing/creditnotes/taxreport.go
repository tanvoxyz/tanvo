package creditnotes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// ReportTaxes reports a finalized credit note to the tax provider of its
// customer (the [kernel.JobReportCreditNoteTaxes] job); customers without
// a provider and notes of credit invoices are skipped. A provider's
// refusal is kept as a tax_error detail and the job succeeds, to be
// retried with [Service.RetryTaxReporting] (lago
// CreditNotes::ProviderTaxes::ReportJob).
//
// source: app/jobs/credit_notes/provider_taxes/report_job.rb:8-15
func (s *Service) ReportTaxes(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) error {
	_, err := s.reportTaxes(ctx, org, id, true)
	return err
}

// RetryTaxReporting reports the finalized credit note id to its customer's
// tax provider again and returns it; a refusal is kept as a tax_error
// detail and fails as a tax_error validation error (lago
// RetryTaxReporting mutation).
//
// source: app/graphql/mutations/credit_notes/retry_tax_reporting.rb:17-22
func (s *Service) RetryTaxReporting(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) (*creditnote.CreditNote, error) {
	code, err := s.reportTaxes(ctx, org, id, false)
	if err != nil {
		return nil, err
	}
	if code != "" {
		return nil, &core.ValidationError{Errors: []core.FieldError{{Field: "tax_error", Code: code}}}
	}
	return s.Get(ctx, org, id)
}

// reportTaxes is lago CreditNotes::ProviderTaxes::ReportService: the
// credit note's tax errors are discarded, it is reported, and a refusal
// is stored as a tax_error detail whose code is returned. job skips the
// notes the job skips: those of credit invoices and of customers without
// a tax provider.
//
// source: app/services/credit_notes/provider_taxes/report_service.rb:14-50
func (s *Service) reportTaxes(ctx context.Context, org core.OrganizationID, id core.CreditNoteID, job bool) (string, error) {
	type report struct {
		cn  *creditnote.CreditNote
		inv *invoice.Invoice
		env *kernel.CustomerEnv
	}
	rep, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*report, error) {
		cn, err := get(ctx, r, org, id, false)
		if err != nil {
			return nil, err
		}
		inv, err := r.Invoices.Get(ctx, org, cn.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", cn.InvoiceID, err)
		}
		if job && inv.Type == invoice.TypeCredit {
			return nil, nil
		}
		env, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
		if err != nil {
			return nil, err
		}
		return &report{cn: cn, inv: inv, env: env}, nil
	})
	if err != nil || rep == nil || s.d.Taxes == nil {
		return "", err
	}
	ok, err := s.d.Taxes.Enabled(ctx, rep.env.Customer)
	if err != nil {
		return "", fmt.Errorf("%w: %w", kernel.ErrTaxProvider, err)
	}
	if job && !ok {
		return "", nil
	}
	// report_service.rb:17.
	if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return r.CreditNoteErrorDetails.DeleteByCreditNote(ctx, org, id, creditnote.ErrorCodeTaxError)
	}); err != nil {
		return "", fmt.Errorf("discard tax errors of credit note %s: %w", id, err)
	}
	if !ok {
		return "", nil
	}
	err = s.d.Taxes.ReportCreditNote(ctx, rep.env.Customer, rep.inv, rep.cn)
	var pe *kernel.TaxProviderError
	switch {
	case err == nil:
		return "", nil
	case !errors.As(err, &pe):
		return "", fmt.Errorf("%w: report credit note %s: %w", kernel.ErrTaxProvider, id, err)
	}
	// report_service.rb:37-49.
	details, err := json.Marshal(map[string]string{string(creditnote.ErrorCodeTaxError): pe.Code})
	if err != nil {
		return "", fmt.Errorf("encode tax error: %w", err)
	}
	if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return r.CreditNoteErrorDetails.Create(ctx, &creditnote.ErrorDetail{
			ID: core.NewID[core.ErrorDetailID](s.d.IDs), OrganizationID: org, CreditNoteID: id,
			Code: creditnote.ErrorCodeTaxError, Details: details, CreatedAt: s.d.Now(),
		})
	}); err != nil {
		return "", fmt.Errorf("store tax error of credit note %s: %w", id, err)
	}
	return pe.Code, nil
}

// withErrors loads the error details of the credit notes cns.
func withErrors(ctx context.Context, r port.Repos, org core.OrganizationID, cns ...*creditnote.CreditNote) error {
	if len(cns) == 0 {
		return nil
	}
	ids := make([]core.CreditNoteID, len(cns))
	for i, cn := range cns {
		ids[i], cn.ErrorDetails = cn.ID, nil
	}
	ds, err := r.CreditNoteErrorDetails.ListByCreditNotes(ctx, org, ids)
	if err != nil {
		return fmt.Errorf("load credit note error details: %w", err)
	}
	for _, d := range ds {
		for _, cn := range cns {
			if cn.ID == d.CreditNoteID {
				cn.ErrorDetails = append(cn.ErrorDetails, *d)
			}
		}
	}
	return nil
}
