package deduct

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ProgressiveBilled is what progressive billing invoices billed for a
// subscription in the charges period containing an instant (lago
// Subscriptions::ProgressiveBilledAmount).
type ProgressiveBilled struct {
	// Invoice is the latest progressive billing invoice of the period (by
	// issuing date, then creation); nil when there is none.
	Invoice *invoice.Invoice
	// BilledAmount is the fees amount of Invoice.
	BilledAmount int64
	// TotalBilledAmount is Σ over the period's progressive billing invoices
	// of their fees' amounts after coupons plus taxes.
	TotalBilledAmount int64
	// ToInvoice is what an invoice of the period may still deduct: the fees
	// of Invoice less the active progressive billing credits already taken
	// from it and its credit note items, never negative.
	ToInvoice int64
	// ToCredit is what may still be credited back on Invoice: its fees less
	// its coupons, the active credits already taken from it and the credit
	// of its non-voided credit notes, never negative.
	ToCredit int64
}

// ProgressiveBilledAmount returns what progressive billing invoices billed
// for subscription sub in the charges period containing ts: invoices whose
// charges period has from ≤ ts < to, finalized or failed (or generating
// when includeGenerating).
func (s *Service) ProgressiveBilledAmount(ctx context.Context, r port.Repos, org core.OrganizationID, sub core.SubscriptionID, ts time.Time, includeGenerating bool) (ProgressiveBilled, error) {
	var out ProgressiveBilled
	iss, err := r.InvoiceSubscriptions.ListBySubscription(ctx, org, sub)
	if err != nil {
		return out, fmt.Errorf("list invoice subscriptions of %s: %w", sub, err)
	}
	var invoices []*invoice.Invoice
	for _, is := range iss {
		if is.ChargesPeriod.From.After(ts) || !is.ChargesPeriod.To.After(ts) {
			continue
		}
		inv, err := r.Invoices.Get(ctx, org, is.InvoiceID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("get invoice %s: %w", is.InvoiceID, err)
		}
		if inv.Type != invoice.TypeProgressiveBilling || slices.ContainsFunc(invoices, func(x *invoice.Invoice) bool { return x.ID == inv.ID }) {
			continue
		}
		switch inv.Status {
		case invoice.StatusFinalized, invoice.StatusFailed:
		case invoice.StatusGenerating:
			if !includeGenerating {
				continue
			}
		default:
			continue
		}
		invoices = append(invoices, inv)
	}
	if len(invoices) == 0 {
		return out, nil
	}
	slices.SortStableFunc(invoices, func(a, b *invoice.Invoice) int {
		if c := b.IssuingDate.Compare(a.IssuingDate); c != 0 {
			return c
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})

	total := money.Zero
	for _, inv := range invoices {
		fees, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return out, fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
		}
		for _, f := range fees {
			total = total.Add(kernel.Cents(f.TaxesAmount)).Add(f.SubTotalExcludingTaxesPreciseAmount())
		}
	}
	out.TotalBilledAmount = kernel.RoundCents(total)

	pb := invoices[0]
	out.Invoice = pb
	out.BilledAmount = pb.FeesAmount
	applied, err := s.activeProgressiveCredits(ctx, r, org, pb.ID)
	if err != nil {
		return out, err
	}
	cns, err := r.CreditNotes.ListByInvoice(ctx, org, pb.ID)
	if err != nil {
		return out, fmt.Errorf("list credit notes of invoice %s: %w", pb.ID, err)
	}
	var items, credited int64
	for _, cn := range cns {
		items += cn.ItemsAmount()
		if cn.CreditStatus == creditnote.CreditStatusAvailable || cn.CreditStatus == creditnote.CreditStatusConsumed {
			credited += cn.CreditAmount
		}
	}
	out.ToInvoice = max(0, pb.FeesAmount-applied-items)
	out.ToCredit = max(0, pb.FeesAmount-pb.CouponsAmount-applied-credited)
	return out, nil
}

// activeProgressiveCredits sums the credits taken from a progressive
// billing invoice by invoices that are not voided or closed.
func (s *Service) activeProgressiveCredits(ctx context.Context, r port.Repos, org core.OrganizationID, pb core.InvoiceID) (int64, error) {
	credits, err := r.Credits.ListByProgressiveBillingInvoice(ctx, org, pb)
	if err != nil {
		return 0, fmt.Errorf("list credits of progressive invoice %s: %w", pb, err)
	}
	var sum int64
	for _, c := range credits {
		inv, err := r.Invoices.Get(ctx, org, c.InvoiceID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("get invoice %s: %w", c.InvoiceID, err)
		}
		if inv.Status == invoice.StatusVoided || inv.Status == invoice.StatusClosed {
			continue
		}
		sum += c.Amount
	}
	return sum, nil
}

// applyProgressiveBilling runs step 1 (lago Credits::ProgressiveBillingService):
// for each subscription of inv, the latest progressive billing invoice of
// its charges period is deducted fee by fee from the matching charge fees,
// and what cannot be deducted is returned as credit note items.
func (s *Service) applyProgressiveBilling(ctx context.Context, r port.Repos, inv *invoice.Invoice, fees []*invoice.Fee, opts Options, out *BeforeTaxes) error {
	org := inv.OrganizationID
	iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("list invoice subscriptions of %s: %w", inv.ID, err)
	}
	for _, is := range iss {
		billed, err := s.ProgressiveBilledAmount(ctx, r, org, is.SubscriptionID, is.ChargesPeriod.From, false)
		if err != nil {
			return err
		}
		if billed.Invoice == nil {
			continue
		}
		applied, items, err := applyProgressiveToFees(ctx, r, billed.Invoice, is.SubscriptionID, billed.ToInvoice, fees)
		if err != nil {
			return err
		}
		if len(items) > 0 && billed.ToCredit > 0 {
			note := ProgressiveCreditNote{InvoiceID: billed.Invoice.ID, Items: items}
			for _, it := range items {
				note.Amount += it.Amount
			}
			out.ProgressiveCreditNotes = append(out.ProgressiveCreditNotes, note)
		}
		if applied <= 0 {
			continue
		}
		c := &invoice.Credit{
			ID: core.NewID[core.InvoiceCreditID](s.d.IDs), OrganizationID: org, InvoiceID: inv.ID,
			ProgressiveBillingInvoiceID: billed.Invoice.ID, Amount: applied,
			Currency: inv.Currency, BeforeTaxes: true, CreatedAt: s.d.Now(),
		}
		if !opts.Preview {
			if err := r.Credits.Create(ctx, c); err != nil {
				return fmt.Errorf("create progressive billing credit: %w", err)
			}
		}
		inv.SubTotalExcludingTaxesAmount -= applied
		inv.ProgressiveBillingCreditAmount += applied
		out.ProgressiveBillingCredit += applied
		out.Credits = append(out.Credits, c)
	}
	return nil
}

// applyProgressiveToFees deducts up to amount from the charge fees of sub
// matching the fees of the progressive billing invoice pb (same charge,
// filter and grouping), largest progressive fee first, into their coupon
// shares. It returns the amount deducted and, for what is left, the
// credit note items on pb's fees.
func applyProgressiveToFees(ctx context.Context, r port.Repos, pb *invoice.Invoice, sub core.SubscriptionID, amount int64, fees []*invoice.Fee) (int64, []FeeAmount, error) {
	org := pb.OrganizationID
	pbFees, err := r.Fees.ListByInvoice(ctx, org, pb.ID)
	if err != nil {
		return 0, nil, fmt.Errorf("list fees of invoice %s: %w", pb.ID, err)
	}
	cns, err := r.CreditNotes.ListByInvoice(ctx, org, pb.ID)
	if err != nil {
		return 0, nil, fmt.Errorf("list credit notes of invoice %s: %w", pb.ID, err)
	}
	credited := map[core.FeeID]int64{}
	for _, cn := range cns {
		for _, it := range cn.Items {
			credited[it.FeeID] += it.Amount
		}
	}
	slices.SortStableFunc(pbFees, func(a, b *invoice.Fee) int { return cmp.Compare(b.Amount, a.Amount) })

	remaining := amount
	available := make([]int64, len(pbFees))
	for i, pf := range pbFees {
		available[i] = max(0, pf.Amount-credited[pf.ID])
		idx := slices.IndexFunc(fees, func(f *invoice.Fee) bool {
			return f.Type == core.FeeTypeCharge && f.SubscriptionID == sub && f.ChargeID == pf.ChargeID &&
				f.ChargeFilterID == pf.ChargeFilterID && maps.Equal(f.GroupedBy, pf.GroupedBy)
		})
		if idx < 0 {
			continue
		}
		f := fees[idx]
		net := f.SubTotalExcludingTaxesPreciseAmount().Round(0, money.Floor)
		netCents, _ := net.Int64()
		n := min(max(min(remaining, available[i], netCents), 0), remaining)
		f.PreciseCouponsAmount = f.PreciseCouponsAmount.Add(kernel.Cents(n))
		remaining -= n
		available[i] -= n
	}
	applied := amount - remaining
	var items []FeeAmount
	for i, pf := range pbFees {
		n := min(remaining, available[i])
		if n <= 0 {
			continue
		}
		remaining -= n
		items = append(items, FeeAmount{FeeID: pf.ID, Amount: n})
	}
	return applied, items, nil
}
