package deduct

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// ApplyBeforeTaxes applies steps 1 and 2 of domain §22.7 to inv, whose
// fees are fees: progressive billing credits, then coupons (not on drafts,
// one-off or credit invoices). It runs in the caller's transaction r.
//
// It sets FeesAmount, ProgressiveBillingCreditAmount, CouponsAmount and
// SubTotalExcludingTaxesAmount on inv and the coupon shares
// (PreciseCouponsAmount) of fees in memory; the caller saves them after
// computing taxes with [kernel.Compute]. Credits and applied coupons are
// written through r and the credit repository unless opts.Preview.
func (s *Service) ApplyBeforeTaxes(ctx context.Context, r port.Repos, env *kernel.CustomerEnv, inv *invoice.Invoice, fees []*invoice.Fee, opts Options) (BeforeTaxes, error) {
	var out BeforeTaxes
	if err := checkEnv(env, inv); err != nil {
		return out, err
	}
	var total int64
	for _, f := range fees {
		total += max(0, f.Amount)
	}
	inv.FeesAmount = total
	inv.SubTotalExcludingTaxesAmount = total - inv.ProgressiveBillingCreditAmount - inv.CouponsAmount
	if err := s.applyProgressiveBilling(ctx, r, inv, fees, opts, &out); err != nil {
		return out, err
	}
	if opts.Draft || !receivesCredits(inv.Type) {
		return out, nil
	}
	if err := s.applyCoupons(ctx, r, inv, fees, opts, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ApplyAfterTaxes applies steps 4 and 5 of domain §22.7 to inv, whose fees
// are fees: credit notes, then prepaid wallet credits (not on drafts,
// one-off or credit invoices). The caller has applied the totals of
// [kernel.Compute] to inv first ([invoice.Invoice.ApplyTotals]), so that
// SubTotalIncludingTaxesAmount and the fees' taxes are known; appliedTaxes
// are the invoice taxes of that computation (nil loads the stored ones).
// It runs in the caller's transaction r.
//
// It sets CreditNotesAmount, PrepaidCreditAmount (and its granted and
// purchased parts) and TotalAmount on inv and the credit-note shares
// (PreciseCreditNotesAmount) of fees in memory; the caller saves them.
// Credits, credit notes, wallets and wallet transactions are written
// through r unless opts.Preview.
func (s *Service) ApplyAfterTaxes(ctx context.Context, r port.Repos, env *kernel.CustomerEnv, inv *invoice.Invoice, fees []*invoice.Fee, appliedTaxes []*invoice.AppliedTax, opts Options) (AfterTaxes, error) {
	var out AfterTaxes
	if err := checkEnv(env, inv); err != nil {
		return out, err
	}
	remaining := func() int64 {
		return max(0, inv.SubTotalIncludingTaxesAmount-inv.CreditNotesAmount-inv.PrepaidCreditAmount)
	}
	defer func() { inv.TotalAmount = remaining() }()
	if opts.Draft || !receivesCredits(inv.Type) {
		return out, nil
	}
	n, err := s.applyCreditNotes(ctx, r, inv, fees, remaining(), opts, &out)
	if err != nil {
		return out, err
	}
	out.CreditNotes = n
	if remaining() > 0 {
		n, err := s.applyPrepaidCredits(ctx, r, inv, fees, appliedTaxes, remaining(), opts, &out)
		if err != nil {
			return out, err
		}
		out.PrepaidCredit = n
	}
	return out, nil
}

// checkEnv verifies that env is the environment of the invoice's customer.
func checkEnv(env *kernel.CustomerEnv, inv *invoice.Invoice) error {
	if env == nil || env.Customer.ID != inv.CustomerID || env.OrganizationID() != inv.OrganizationID {
		return fmt.Errorf("deduct invoice %s: %w: customer environment of another customer", inv.ID, core.ErrInvalidValue)
	}
	return nil
}
