package memstore

import (
	"context"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// creditNoteData holds the tables of package creditnote; items and applied
// taxes are stored inside their credit note.
type creditNoteData struct {
	creditNotes *table[core.CreditNoteID, creditnote.CreditNote]
}

func newCreditNoteData() *creditNoteData {
	return &creditNoteData{
		creditNotes: newTable[core.CreditNoteID](func(cn creditnote.CreditNote) creditnote.CreditNote {
			cn = creditnote.Clone(cn)
			cn.Description = invoiceClonePtr(cn.Description)
			cn.RefundedAt = invoiceClonePtr(cn.RefundedAt)
			cn.VoidedAt = invoiceClonePtr(cn.VoidedAt)
			return cn
		}),
	}
}

func (d *creditNoteData) snapshot() *creditNoteData {
	return &creditNoteData{creditNotes: d.creditNotes.snapshot()}
}

var _ creditnote.Repository = creditNoteRepo{}

// creditNoteRepo implements creditnote.Repository.
type creditNoteRepo struct{ d *creditNoteData }

func (r creditNoteRepo) checkNumber(cn *creditnote.CreditNote) error {
	if cn.Number != "" && r.d.creditNotes.any(func(x *creditnote.CreditNote) bool {
		return x.ID != cn.ID && x.OrganizationID == cn.OrganizationID && x.Number == cn.Number
	}) {
		return conflict("credit_note", "number")
	}
	return nil
}

func (r creditNoteRepo) Create(_ context.Context, cn *creditnote.CreditNote) error {
	if r.d.creditNotes.has(cn.ID) {
		return conflict("credit_note", "id")
	}
	if err := r.checkNumber(cn); err != nil {
		return err
	}
	r.d.creditNotes.put(cn.ID, *cn)
	return nil
}

func (r creditNoteRepo) Update(_ context.Context, cn *creditnote.CreditNote) error {
	cur, ok := r.d.creditNotes.get(cn.ID)
	if !ok || cur.OrganizationID != cn.OrganizationID {
		return notFound("credit_note", cn.ID)
	}
	if err := r.checkNumber(cn); err != nil {
		return err
	}
	r.d.creditNotes.put(cn.ID, *cn)
	return nil
}

func (r creditNoteRepo) Get(_ context.Context, org core.OrganizationID, id core.CreditNoteID) (*creditnote.CreditNote, error) {
	cn, ok := r.d.creditNotes.get(id)
	if !ok || cn.OrganizationID != org {
		return nil, notFound("credit_note", id)
	}
	return cn, nil
}

func (r creditNoteRepo) List(_ context.Context, org core.OrganizationID, f creditnote.Filter, page core.PageRequest) (core.Page[*creditnote.CreditNote], error) {
	rows := r.d.creditNotes.find(func(x *creditnote.CreditNote) bool {
		return x.OrganizationID == org &&
			(f.CustomerID.IsZero() || x.CustomerID == f.CustomerID) &&
			(f.InvoiceID.IsZero() || x.InvoiceID == f.InvoiceID) &&
			(len(f.Statuses) == 0 || slices.Contains(f.Statuses, x.Status)) &&
			(len(f.CreditStatuses) == 0 || slices.Contains(f.CreditStatuses, x.CreditStatus)) &&
			(len(f.Reasons) == 0 || slices.Contains(f.Reasons, x.Reason))
	})
	return paginate(rows, func(x *creditnote.CreditNote) core.CreditNoteID { return x.ID }, page)
}

func (r creditNoteRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*creditnote.CreditNote, error) {
	return r.d.creditNotes.find(func(x *creditnote.CreditNote) bool { return x.OrganizationID == org && x.InvoiceID == inv }), nil
}

func (r creditNoteRepo) ListAvailable(_ context.Context, org core.OrganizationID, customer core.CustomerID, currency money.Currency) ([]*creditnote.CreditNote, error) {
	rows := r.d.creditNotes.find(func(x *creditnote.CreditNote) bool {
		return x.OrganizationID == org && x.CustomerID == customer && x.Currency == currency &&
			x.Status == creditnote.StatusFinalized && x.CreditStatus == creditnote.CreditStatusAvailable && x.BalanceAmount > 0
	})
	slices.SortStableFunc(rows, func(a, b *creditnote.CreditNote) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return rows, nil
}

func (r creditNoteRepo) ListItemsByCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) ([]creditnote.Item, error) {
	cn, err := r.Get(ctx, org, id)
	if err != nil {
		return nil, err
	}
	return cn.Items, nil
}

func (r creditNoteRepo) CreditedByFee(_ context.Context, org core.OrganizationID, fees []core.FeeID) ([]creditnote.FeeCredit, error) {
	sums := map[core.FeeID]int64{}
	for _, cn := range r.d.creditNotes.find(func(x *creditnote.CreditNote) bool {
		return x.OrganizationID == org && x.CreditStatus != creditnote.CreditStatusVoided
	}) {
		for _, it := range cn.Items {
			if slices.Contains(fees, it.FeeID) {
				sums[it.FeeID] += it.Amount
			}
		}
	}
	var out []creditnote.FeeCredit
	for _, id := range fees {
		if n, ok := sums[id]; ok {
			out = append(out, creditnote.FeeCredit{FeeID: id, Amount: n})
			delete(sums, id)
		}
	}
	return out, nil
}

func (r creditNoteRepo) ListAppliedTaxes(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) ([]creditnote.AppliedTax, error) {
	cn, err := r.Get(ctx, org, id)
	if err != nil {
		return nil, err
	}
	return cn.AppliedTaxes, nil
}

func (r creditNoteRepo) NextSequentialID(_ context.Context, org core.OrganizationID, inv core.InvoiceID) (int64, error) {
	var n int64
	for _, x := range r.d.creditNotes.find(func(x *creditnote.CreditNote) bool { return x.OrganizationID == org && x.InvoiceID == inv }) {
		n = max(n, x.SequentialID)
	}
	return n + 1, nil
}

func (r creditNoteRepo) Delete(_ context.Context, org core.OrganizationID, id core.CreditNoteID) error {
	cn, ok := r.d.creditNotes.get(id)
	if !ok || cn.OrganizationID != org {
		return notFound("credit_note", id)
	}
	if cn.Status != creditnote.StatusDraft {
		return conflict("credit_note", "not_draft")
	}
	r.d.creditNotes.del(id)
	return nil
}
