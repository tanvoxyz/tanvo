package creditnotes

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ProgressiveInput is the automatic credit note that gives back, on a
// progressive billing invoice, the usage the period's invoice bills again
// (domain §22.16).
type ProgressiveInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the progressive billing invoice.
	InvoiceID core.InvoiceID
	// Amount is the amount to credit before coupons and taxes, in minor
	// units.
	Amount money.Decimal
	// Reason is why the note is issued; zero means other.
	Reason creditnote.Reason
	// Items are the credited fee amounts; nil spreads Amount over the
	// invoice's fees, the largest first.
	Items []ItemInput
}

// CreateFromProgressiveBillingInvoice issues, in the transaction of r, the
// credit note crediting in.Amount of a progressive billing invoice. It
// returns nil when the amount is not positive or the note credits nothing.
// An invoice of another type returns a *core.RuleError with code
// [CodeNotProgressiveBilling]; an amount larger than the fees can still
// credit, one with code [CodeCreditableAmountIsLessThanRequested].
func (s *Service) CreateFromProgressiveBillingInvoice(ctx context.Context, r port.Repos, in ProgressiveInput) (*creditnote.CreditNote, error) {
	if in.Amount.Sign() <= 0 {
		return nil, nil
	}
	st, err := lockAndLoad(ctx, r, in.OrganizationID, in.InvoiceID, false)
	if err != nil {
		return nil, err
	}
	if st.inv.Type != invoice.TypeProgressiveBilling {
		return nil, rule(CodeNotProgressiveBilling, "invoice is not a progressive billing invoice")
	}
	items := in.Items
	if items == nil {
		if items, err = st.progressiveItems(in.Amount); err != nil {
			return nil, err
		}
	}
	cn := &creditnote.CreditNote{}
	for _, it := range items {
		cn.Items = append(cn.Items, creditnote.Item{FeeID: it.FeeID, PreciseAmount: it.Amount})
	}
	res, err := st.applyTaxes(s.d.IDs, cn, false)
	if err != nil {
		return nil, err
	}
	credit := kernel.RoundCents(in.Amount.Truncate(dbScale).
		Sub(res.PreciseCouponsAdjustmentAmountCents.Truncate(dbScale)).
		Add(money.NewFromInt(res.TaxesAmountCents)))
	if credit == 0 {
		return nil, nil
	}
	return s.create(ctx, r, st, CreateInput{
		OrganizationID: in.OrganizationID, InvoiceID: in.InvoiceID,
		Items: items, Reason: in.Reason, CreditAmount: credit, Automatic: true,
	})
}

// progressiveItems spreads amount over the fees, the largest first.
func (st *invoiceState) progressiveItems(amount money.Decimal) ([]ItemInput, error) {
	var items []ItemInput
	remaining := amount
	for _, f := range sortedFees(st.fees) {
		if remaining.Sign() == 0 {
			break
		}
		credit := money.NewFromInt(st.feeCreditable(f))
		if remaining.Cmp(credit) < 0 {
			credit = remaining
		}
		items = append(items, ItemInput{FeeID: f.ID, Amount: credit.Truncate(dbScale)})
		remaining = remaining.Sub(credit)
	}
	if remaining.Sign() > 0 {
		return nil, rule(CodeCreditableAmountIsLessThanRequested, "creditable amount is less than requested")
	}
	return items, nil
}
