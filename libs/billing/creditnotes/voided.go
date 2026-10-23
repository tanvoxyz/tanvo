package creditnotes

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CheckVoidAmounts checks, in the transaction of r, the credit and refund
// asked for the credit note of the voiding of invoice id (lago
// Invoices::VoidService#valid_credit_note_amounts?): each fits what the
// invoice may credit or refund, and so does their sum. A failure is a
// *core.ValidationError on credit_refund_amount with code
// [CodeTotalAmountExceedsInvoiceAmount].
func (s *Service) CheckVoidAmounts(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID, credit, refund int64) error {
	st, err := loadInvoice(ctx, r, org, id)
	if err != nil {
		return err
	}
	creditable := st.creditable()
	var v core.Validator
	v.Check(credit <= creditable && refund <= st.refundable() && credit+refund <= creditable,
		"credit_refund_amount", CodeTotalAmountExceedsInvoiceAmount)
	return v.Err()
}

// IssueVoidNotes issues, in the transaction of r, the credit notes of the
// voiding of invoice id with generate_credit_note (lago
// Invoices::VoidService#create_credit_notes!): a note of credit and
// refund, when positive, whose items take the same share of every fee's
// creditable amount; then, when something is still creditable, a note
// over all of it that is voided at once, so that the invoice is fully
// credited. It returns the notes, the requested one first.
func (s *Service) IssueVoidNotes(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID, credit, refund int64) ([]*creditnote.CreditNote, error) {
	desc := "Credit note created due to voided invoice " + id.String()
	var out []*creditnote.CreditNote
	if requested := credit + refund; requested > 0 {
		st, err := lockAndLoad(ctx, r, org, id, false)
		if err != nil {
			return nil, err
		}
		items, err := s.scaledItems(st, requested)
		if err != nil {
			return nil, err
		}
		cn, err := s.create(ctx, r, st, CreateInput{
			OrganizationID: org, InvoiceID: id, Items: items, Reason: creditnote.ReasonOther,
			Description: &desc, CreditAmount: credit, RefundAmount: refund,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, cn)
	}
	st, err := lockAndLoad(ctx, r, org, id, false)
	if err != nil {
		return nil, err
	}
	if st.creditable() <= 0 {
		return out, nil
	}
	est, err := s.estimate(st, remainingItems(st))
	if err != nil {
		return nil, err
	}
	items := make([]ItemInput, len(est.Items))
	for i, it := range est.Items {
		items[i] = ItemInput{FeeID: it.FeeID, Amount: money.NewFromInt(it.Amount)}
	}
	cn, err := s.create(ctx, r, st, CreateInput{
		OrganizationID: org, InvoiceID: id, Items: items, Reason: creditnote.ReasonOther,
		Description: &desc, CreditAmount: est.CreditAmount,
	})
	if err != nil {
		return nil, err
	}
	if err := cn.Void(s.d.Now()); err != nil {
		return nil, fmt.Errorf("void credit note %s: %w", cn.ID, err)
	}
	if err := r.CreditNotes.Update(ctx, cn); err != nil {
		return nil, fmt.Errorf("update credit note %s: %w", cn.ID, err)
	}
	return append(out, cn), nil
}

// remainingItems returns an item of every fee of st with a creditable
// amount, crediting all of it.
func remainingItems(st *invoiceState) []ItemInput {
	var items []ItemInput
	for _, f := range st.fees {
		if n := st.feeCreditable(f); n > 0 {
			items = append(items, ItemInput{FeeID: f.ID, Amount: money.NewFromInt(n)})
		}
	}
	return items
}

// scaledItems returns the remaining items of st scaled so that their note
// totals requested: every amount is multiplied by requested ÷ the total
// of their estimate, unrounded (lago
// credit_note_items_matching_requested_amount).
func (s *Service) scaledItems(st *invoiceState, requested int64) ([]ItemInput, error) {
	base := remainingItems(st)
	if len(base) == 0 {
		return nil, nil
	}
	est, err := s.estimate(st, base)
	if err != nil {
		return nil, err
	}
	if est.TotalAmount == 0 {
		return nil, nil
	}
	ratio := money.NewFromInt(requested).Div(money.NewFromInt(est.TotalAmount))
	for i := range base {
		base[i].Amount = base[i].Amount.Mul(ratio)
	}
	return base, nil
}
