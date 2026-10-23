package deduct

import (
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// applyCreditNotes runs step 4 (lago Credits::CreditNoteService) under the
// customer's credit_notes lock: the customer's available credit notes in
// the invoice currency, not issued on the invoice, oldest first, each
// taking min(balance, remaining) and spreading it over the fees after
// taxes. remaining is the invoice total left; the amount applied is
// returned.
func (s *Service) applyCreditNotes(ctx context.Context, r port.Repos, inv *invoice.Invoice, fees []*invoice.Fee, remaining int64, opts Options, out *AfterTaxes) (int64, error) {
	org := inv.OrganizationID
	existing, err := s.d.Credits.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return 0, fmt.Errorf("list credits of invoice %s: %w", inv.ID, err)
	}
	if slices.ContainsFunc(existing, func(c *kernel.Credit) bool { return c.Kind() == kernel.CreditKindCreditNote }) {
		return 0, nil
	}
	if !opts.Preview {
		if err := kernel.LockCustomer(ctx, r, org, inv.CustomerID, customer.LockScopeCreditNotes); err != nil {
			return 0, err
		}
	}
	notes, err := r.CreditNotes.ListAvailable(ctx, org, inv.CustomerID, inv.Currency)
	if err != nil {
		return 0, fmt.Errorf("list available credit notes: %w", err)
	}
	now := s.d.Now()
	var total int64
	for _, cn := range notes {
		if remaining <= 0 {
			break
		}
		if cn.InvoiceID == inv.ID {
			continue
		}
		amount := min(cn.BalanceAmount, remaining)
		if amount <= 0 {
			continue
		}
		credit := &kernel.Credit{
			ID: s.d.IDs.NewUUID(), OrganizationID: org, InvoiceID: inv.ID,
			CreditNoteID: cn.ID, Amount: amount, Currency: inv.Currency, CreatedAt: now,
		}
		if !opts.Preview {
			if err := s.d.Credits.Create(ctx, credit); err != nil {
				return 0, fmt.Errorf("create credit note credit: %w", err)
			}
			spreadCreditNote(fees, amount, remaining)
			if _, err := cn.ConsumeBalance(amount, now); err != nil {
				return 0, fmt.Errorf("consume credit note %s: %w", cn.ID, err)
			}
			if err := r.CreditNotes.Update(ctx, cn); err != nil {
				return 0, fmt.Errorf("update credit note %s: %w", cn.ID, err)
			}
		}
		remaining -= amount
		total += amount
		out.Credits = append(out.Credits, credit)
	}
	inv.CreditNotesAmount += total
	return total, nil
}

// spreadCreditNote adds to each fee's credit-note share its part of a
// credit of amount on an invoice with remaining left: amount × (fee after
// coupons and taxes − credited so far) / remaining, capped at the fee after
// taxes.
func spreadCreditNote(fees []*invoice.Fee, amount, remaining int64) {
	for _, f := range fees {
		after := f.SubTotalExcludingTaxesPreciseAmount().Add(kernel.Cents(f.TaxesAmount))
		share := money.Prorate(kernel.Cents(amount), after.Sub(f.PreciseCreditNotesAmount), kernel.Cents(remaining))
		f.PreciseCreditNotesAmount = money.Min(f.PreciseCreditNotesAmount.Add(share), after)
	}
}
