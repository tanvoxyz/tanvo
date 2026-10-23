package invoicing

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// RegenerationPreview previews the invoice regenerated from the visible
// invoice id as it stands (lago Invoices::BuildRegenerationPreviewService,
// app/services/invoices/build_regeneration_preview_service.rb): its fees,
// keeping their IDs and coupon shares, are taxed again by the current tax
// rules, never by a tax provider, and the totals recomputed from them
// without the invoice's coupon credits (Invoices::ComputeAmountsFromFees
// with no provider taxes). Nothing is stored.
func (s *Service) RegenerationPreview(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*InvoiceView, error) {
	var out *InvoiceView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := visibleInvoice(ctx, r, org, id)
		if err != nil {
			return err
		}
		cenv, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
		if err != nil {
			return err
		}
		fs, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
		}
		// Fee#dup drops the applied taxes and keeps the coupon share the
		// fee taxes are computed on.
		for _, f := range fs {
			f.AppliedTaxes = nil
		}
		deps := *s.d
		deps.Taxes = nil
		prepaid := inv.PrepaidCreditAmount
		res, err := kernel.NewTaxResolver(r, cenv).Compute(ctx, &deps, kernel.TaxInput{
			Invoice: inv, Fees: fs,
			Deductions: invoice.Deductions{
				ProgressiveBillingCredit: inv.ProgressiveBillingCreditAmount,
				CreditNotes:              inv.CreditNotesAmount,
			},
			Inline: true,
		})
		if err != nil {
			return err
		}
		inv.ApplyTotals(res.Totals)
		inv.PrepaidCreditAmount = prepaid
		inv.TaxesRate = res.TaxesRate
		for _, f := range fs {
			if err := r.Fees.Update(ctx, f); err != nil {
				return fmt.Errorf("save fee %s: %w", f.ID, err)
			}
		}
		if err := r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, org, inv.ID, res.AppliedTaxes); err != nil {
			return fmt.Errorf("save taxes of invoice %s: %w", inv.ID, err)
		}
		if out, err = newViewer(r, org).invoice(ctx, inv, true); err != nil {
			return err
		}
		// The preview is a copy, without the invoice's credits.
		out.Credits = nil
		return errDryRun
	})
	if errors.Is(err, errDryRun) {
		return out, nil
	}
	return nil, err
}
