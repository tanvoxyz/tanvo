package memstore

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
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
type creditNoteRepo struct {
	d  *creditNoteData
	st *state // the whole transaction state, for the list filters
}

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
	if err := f.Validate(); err != nil {
		return core.Page[*creditnote.CreditNote]{}, fmt.Errorf("list credit notes: %w", err)
	}
	search := strings.ToLower(core.SearchTerm(f.Search))
	byInvoice := f.InvoiceNumber != "" || len(f.BillingEntityIDs) > 0 || f.SelfBilled != nil || f.PurchaseOrderNumber != ""
	rows := r.d.creditNotes.find(func(x *creditnote.CreditNote) bool {
		switch {
		case x.OrganizationID != org,
			!f.CustomerID.IsZero() && x.CustomerID != f.CustomerID,
			!f.InvoiceID.IsZero() && x.InvoiceID != f.InvoiceID,
			!f.Currency.IsZero() && x.Currency.Code() != f.Currency.Code(),
			len(f.Statuses) > 0 && !slices.Contains(f.Statuses, x.Status),
			len(f.CreditStatuses) > 0 && !slices.Contains(f.CreditStatuses, x.CreditStatus),
			len(f.RefundStatuses) > 0 && !slices.Contains(f.RefundStatuses, x.RefundStatus),
			len(f.Reasons) > 0 && !slices.Contains(f.Reasons, x.Reason),
			len(f.Types) > 0 && !slices.ContainsFunc(f.Types, func(t creditnote.Type) bool { return t.Is(x) }),
			!f.IssuingDateFrom.IsZero() && (x.IssuingDate.IsZero() || x.IssuingDate.Before(f.IssuingDateFrom)),
			!f.IssuingDateTo.IsZero() && (x.IssuingDate.IsZero() || x.IssuingDate.After(f.IssuingDateTo)),
			f.AmountFrom != nil && x.TotalAmount < *f.AmountFrom,
			f.AmountTo != nil && x.TotalAmount > *f.AmountTo:
			return false
		}
		if byInvoice {
			inv, ok := r.st.invoice.invoices.get(x.InvoiceID)
			if !ok ||
				f.InvoiceNumber != "" && inv.Number != f.InvoiceNumber ||
				len(f.BillingEntityIDs) > 0 && !slices.Contains(f.BillingEntityIDs, inv.BillingEntityID) ||
				f.SelfBilled != nil && inv.SelfBilled != *f.SelfBilled ||
				f.PurchaseOrderNumber != "" && !invoicePurchaseOrderIs(inv, f.PurchaseOrderNumber) {
				return false
			}
		}
		var c *customer.Customer
		if f.CustomerExternalID != "" || search != "" && f.CustomerID.IsZero() {
			c, _ = r.st.customer.customers.get(x.CustomerID)
		}
		if f.CustomerExternalID != "" && (c == nil || c.ExternalID != f.CustomerExternalID) {
			return false
		}
		if search == "" || strings.Contains(x.ID.String(), search) || strings.Contains(strings.ToLower(x.Number), search) {
			return true
		}
		return c != nil && f.CustomerID.IsZero() && creditNoteCustomerMatches(c, search)
	})
	if f.Order == core.OrderRecent {
		slices.Reverse(rows)
		return paginateDesc(rows, func(x *creditnote.CreditNote) core.CreditNoteID { return x.ID }, page)
	}
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
		return x.OrganizationID == org && x.CreditStatus != creditnote.CreditStatusVoided && x.Status != creditnote.StatusDeleted
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

func (r creditNoteRepo) DetachFees(_ context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	for _, cn := range r.d.creditNotes.find(func(x *creditnote.CreditNote) bool { return x.OrganizationID == org && x.InvoiceID == inv }) {
		c := *cn
		c.Items = slices.Clone(cn.Items)
		for i := range c.Items {
			c.Items[i].FeeID = core.FeeID{}
		}
		r.d.creditNotes.put(c.ID, c)
	}
	return nil
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
	if err := r.st.checkCreditNotesDeletable(set[core.CreditNoteID]{id: true}, nil); err != nil {
		return err
	}
	r.d.creditNotes.del(id)
	return nil
}

// creditNoteCustomerMatches reports whether the lower-cased text occurs in
// the name, first name, last name, external ID or email of c (the customer
// fields the credit note search looks at; not the legal name).
func creditNoteCustomerMatches(c *customer.Customer, text string) bool {
	fields := []string{c.ExternalID}
	for _, p := range []*string{c.Name, c.Firstname, c.Lastname, c.Email} {
		if p != nil {
			fields = append(fields, *p)
		}
	}
	return slices.ContainsFunc(fields, func(s string) bool { return strings.Contains(strings.ToLower(s), text) })
}
