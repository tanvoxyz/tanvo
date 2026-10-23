package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindCreditNote binds the credit note repository.
func (t *txState) bindCreditNote(r *port.Repos) {
	r.CreditNotes = creditNoteRepo{t}
}

// creditNoteRepo implements creditnote.Repository; items and applied taxes
// are child rows replaced with their credit note.
type creditNoteRepo struct{ t *txState }

var _ creditnote.Repository = creditNoteRepo{}

func creditNoteParams(cn *creditnote.CreditNote) db.CreateCreditNoteParams {
	p := db.CreateCreditNoteParams{
		OrganizationID:               cn.OrganizationID.UUID(),
		ID:                           cn.ID.UUID(),
		InvoiceID:                    cn.InvoiceID.UUID(),
		CustomerID:                   cn.CustomerID.UUID(),
		Number:                       cn.Number,
		SequentialID:                 cn.SequentialID,
		Status:                       string(cn.Status),
		CreditStatus:                 optString(string(cn.CreditStatus)),
		RefundStatus:                 optString(string(cn.RefundStatus)),
		Reason:                       string(cn.Reason),
		Description:                  cn.Description,
		CreditAmount:                 cn.CreditAmount,
		RefundAmount:                 cn.RefundAmount,
		OffsetAmount:                 cn.OffsetAmount,
		BalanceAmount:                cn.BalanceAmount,
		SubTotalExcludingTaxesAmount: cn.SubTotalExcludingTaxesAmount,
		CouponsAdjustmentAmount:      cn.CouponsAdjustmentAmount,
		TaxesAmount:                  cn.TaxesAmount,
		TotalAmount:                  cn.TotalAmount,
		Currency:                     cn.Currency.Code(),
		IssuingDate:                  invoiceDateCol(cn.IssuingDate),
		Metadata:                     encodeMetadata(cn.Metadata),
		File:                         optString(string(cn.File)),
		XmlFile:                      optString(string(cn.XMLFile)),
		RefundedAt:                   optUTC(cn.RefundedAt),
		VoidedAt:                     optUTC(cn.VoidedAt),
		CreatedAt:                    utc(cn.CreatedAt),
		UpdatedAt:                    utc(cn.UpdatedAt),
	}
	p.PreciseCouponsAdjustmentAmount = cn.PreciseCouponsAdjustmentAmount
	p.PreciseTaxesAmount = cn.PreciseTaxesAmount
	p.TaxesRate = cn.TaxesRate
	return p
}

func creditNoteOf(r db.CreditNote) (*creditnote.CreditNote, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("credit note %s: %w", r.ID, err)
	}
	md, err := decodeMetadata(r.Metadata)
	if err != nil {
		return nil, fmt.Errorf("credit note %s: %w", r.ID, err)
	}
	cn := &creditnote.CreditNote{
		ID:                             idOf[core.CreditNoteID](r.ID),
		OrganizationID:                 idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:                      idOf[core.InvoiceID](r.InvoiceID),
		CustomerID:                     idOf[core.CustomerID](r.CustomerID),
		Number:                         r.Number,
		SequentialID:                   r.SequentialID,
		Status:                         creditnote.Status(r.Status),
		CreditStatus:                   creditnote.CreditStatus(deref(r.CreditStatus)),
		RefundStatus:                   core.RefundStatus(deref(r.RefundStatus)),
		Reason:                         creditnote.Reason(r.Reason),
		Description:                    r.Description,
		CreditAmount:                   r.CreditAmount,
		RefundAmount:                   r.RefundAmount,
		OffsetAmount:                   r.OffsetAmount,
		BalanceAmount:                  r.BalanceAmount,
		SubTotalExcludingTaxesAmount:   r.SubTotalExcludingTaxesAmount,
		CouponsAdjustmentAmount:        r.CouponsAdjustmentAmount,
		PreciseCouponsAdjustmentAmount: r.PreciseCouponsAdjustmentAmount,
		TaxesAmount:                    r.TaxesAmount,
		PreciseTaxesAmount:             r.PreciseTaxesAmount,
		TaxesRate:                      r.TaxesRate,
		TotalAmount:                    r.TotalAmount,
		Currency:                       cur,
		IssuingDate:                    invoiceDateOf(r.IssuingDate),
		Metadata:                       md,
		File:                           core.BlobRef(deref(r.File)),
		XMLFile:                        core.BlobRef(deref(r.XmlFile)),
		RefundedAt:                     optUTC(r.RefundedAt),
		VoidedAt:                       optUTC(r.VoidedAt),
		CreatedAt:                      utc(r.CreatedAt),
		UpdatedAt:                      utc(r.UpdatedAt),
	}
	return cn, nil
}

func creditNoteItemOf(r db.CreditNoteItem) creditnote.Item {
	return creditnote.Item{
		ID:            idOf[core.CreditNoteItemID](r.ID),
		FeeID:         optIDOf[core.FeeID](r.FeeID),
		Amount:        r.Amount,
		PreciseAmount: r.PreciseAmount,
	}
}

func creditNoteAppliedTaxOf(r db.CreditNoteAppliedTax) creditnote.AppliedTax {
	return creditnote.AppliedTax{
		ID:             idOf[core.CreditNoteAppliedTaxID](r.ID),
		TaxID:          optIDOf[core.TaxID](r.TaxID),
		TaxCode:        r.TaxCode,
		TaxName:        r.TaxName,
		TaxDescription: r.TaxDescription,
		TaxRate:        r.TaxRate,
		BaseAmount:     r.BaseAmount,
		Amount:         r.Amount,
		PreciseAmount:  r.PreciseAmount,
	}
}

// creditNoteWriteChildren replaces the items and applied taxes of cn.
func creditNoteWriteChildren(ctx context.Context, q *db.Queries, cn *creditnote.CreditNote, replace bool) error {
	org, id := cn.OrganizationID.UUID(), cn.ID.UUID()
	if replace {
		if err := q.DeleteCreditNoteItems(ctx, db.DeleteCreditNoteItemsParams{OrganizationID: org, CreditNoteID: id}); err != nil {
			return err
		}
		if err := q.DeleteCreditNoteAppliedTaxes(ctx, db.DeleteCreditNoteAppliedTaxesParams{OrganizationID: org, CreditNoteID: id}); err != nil {
			return err
		}
	}
	for i, it := range cn.Items {
		p := db.InsertCreditNoteItemParams{
			OrganizationID: org, CreditNoteID: id, Position: int32(i), //nolint:gosec // a list position
			ID: it.ID.UUID(), FeeID: optUUID(it.FeeID), Amount: it.Amount,
		}
		p.PreciseAmount = it.PreciseAmount
		if err := q.InsertCreditNoteItem(ctx, p); err != nil {
			return err
		}
	}
	for i, t := range cn.AppliedTaxes {
		p := db.InsertCreditNoteAppliedTaxParams{
			OrganizationID: org, CreditNoteID: id, Position: int32(i), //nolint:gosec // a list position
			ID: t.ID.UUID(), TaxID: optUUID(t.TaxID), TaxCode: t.TaxCode, TaxName: t.TaxName,
			TaxDescription: t.TaxDescription, BaseAmount: t.BaseAmount, Amount: t.Amount,
		}
		p.TaxRate = t.TaxRate
		p.PreciseAmount = t.PreciseAmount
		if err := q.InsertCreditNoteAppliedTax(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// creditNoteLoad converts credit note rows and attaches their items and
// applied taxes, read in two batch queries.
func creditNoteLoad(ctx context.Context, q *db.Queries, org core.OrganizationID, rows []db.CreditNote) ([]*creditnote.CreditNote, error) {
	out, err := convAll(rows, creditNoteOf)
	if err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]uuid.UUID, len(rows))
	byID := make(map[uuid.UUID]*creditnote.CreditNote, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		byID[r.ID] = out[i]
	}
	items, err := q.ListCreditNoteItems(ctx, db.ListCreditNoteItemsParams{OrganizationID: org.UUID(), CreditNoteIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list credit note items: %w", mapErr(err))
	}
	for _, r := range items {
		cn := byID[r.CreditNoteID]
		cn.Items = append(cn.Items, creditNoteItemOf(r))
	}
	taxes, err := q.ListCreditNoteAppliedTaxes(ctx, db.ListCreditNoteAppliedTaxesParams{OrganizationID: org.UUID(), CreditNoteIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list credit note applied taxes: %w", mapErr(err))
	}
	for _, r := range taxes {
		cn := byID[r.CreditNoteID]
		cn.AppliedTaxes = append(cn.AppliedTaxes, creditNoteAppliedTaxOf(r))
	}
	return out, nil
}

// creditNoteList runs a list query and loads the children of its rows.
func creditNoteList(ctx context.Context, q *db.Queries, org core.OrganizationID, rows []db.CreditNote, err error) ([]*creditnote.CreditNote, error) {
	if err != nil {
		return nil, fmt.Errorf("list credit notes: %w", mapErr(err))
	}
	return creditNoteLoad(ctx, q, org, rows)
}

func (r creditNoteRepo) Create(ctx context.Context, cn *creditnote.CreditNote) error {
	q, err := r.t.queries(ctx, cn.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		if n, err = q.CreateCreditNote(ctx, creditNoteParams(cn)); err != nil || n == 0 {
			return err
		}
		return creditNoteWriteChildren(ctx, q, cn, false)
	})
	return insertErr("credit_note", n, err)
}

func (r creditNoteRepo) Update(ctx context.Context, cn *creditnote.CreditNote) error {
	q, err := r.t.queries(ctx, cn.OrganizationID)
	if err != nil {
		return err
	}
	if err := r.t.trackCreditNote(ctx, q, cn.OrganizationID, cn.ID); err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		if n, err = q.UpdateCreditNote(ctx, db.UpdateCreditNoteParams(creditNoteParams(cn))); err != nil || n == 0 {
			return err
		}
		return creditNoteWriteChildren(ctx, q, cn, true)
	})
	return updateErr("credit_note", cn.ID, n, err)
}

func (r creditNoteRepo) Get(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) (*creditnote.CreditNote, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetCreditNote(ctx, db.GetCreditNoteParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("credit_note", id, err)
	}
	out, err := creditNoteLoad(ctx, q, org, []db.CreditNote{row})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

func (r creditNoteRepo) List(ctx context.Context, org core.OrganizationID, f creditnote.Filter, page core.PageRequest) (core.Page[*creditnote.CreditNote], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*creditnote.CreditNote]{}, fmt.Errorf("list credit notes: %w", err)
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*creditnote.CreditNote]{}, err
	}
	after, lim, recent, err := listArgs(f.Order, page)
	if err != nil {
		return core.Page[*creditnote.CreditNote]{}, err
	}
	var currency *string
	if !f.Currency.IsZero() {
		currency = new(f.Currency.Code())
	}
	rows, err := q.ListCreditNotes(ctx, db.ListCreditNotesParams{
		OrganizationID:      org.UUID(),
		CustomerID:          optUUID(f.CustomerID),
		CustomerExternalID:  optString(f.CustomerExternalID),
		InvoiceID:           optUUID(f.InvoiceID),
		InvoiceNumber:       optString(f.InvoiceNumber),
		BillingEntityIds:    uuids(f.BillingEntityIDs),
		SelfBilled:          f.SelfBilled,
		PurchaseOrderNumber: optString(f.PurchaseOrderNumber),
		Currency:            currency,
		Statuses:            enumStrings(f.Statuses),
		CreditStatuses:      enumStrings(f.CreditStatuses),
		RefundStatuses:      enumStrings(f.RefundStatuses),
		Reasons:             enumStrings(f.Reasons),
		Types:               enumStrings(f.Types),
		IssuingDateFrom:     invoiceDateCol(f.IssuingDateFrom),
		IssuingDateTo:       invoiceDateCol(f.IssuingDateTo),
		AmountFrom:          f.AmountFrom,
		AmountTo:            f.AmountTo,
		Search:              searchArg(f.Search),
		After:               after,
		Recent:              recent,
		Lim:                 lim,
	})
	if err != nil {
		return core.Page[*creditnote.CreditNote]{}, fmt.Errorf("list credit notes: %w", mapErr(err))
	}
	// Load the children of every fetched row (the extra one included)
	// before paging; pageOf only converts.
	loaded, err := creditNoteLoad(ctx, q, org, rows)
	if err != nil {
		return core.Page[*creditnote.CreditNote]{}, err
	}
	return pageOf(loaded, page,
		func(cn *creditnote.CreditNote) (*creditnote.CreditNote, error) { return cn, nil },
		func(cn *creditnote.CreditNote) string { return cn.ID.String() })
}

func (r creditNoteRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*creditnote.CreditNote, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListCreditNotesByInvoice(ctx, db.ListCreditNotesByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()})
	return creditNoteList(ctx, q, org, rows, err)
}

func (r creditNoteRepo) ListAvailable(ctx context.Context, org core.OrganizationID, customer core.CustomerID, currency money.Currency) ([]*creditnote.CreditNote, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAvailableCreditNotes(ctx, db.ListAvailableCreditNotesParams{
		OrganizationID: org.UUID(), CustomerID: customer.UUID(), Currency: currency.Code(),
	})
	return creditNoteList(ctx, q, org, rows, err)
}

func (r creditNoteRepo) ListItemsByCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) ([]creditnote.Item, error) {
	cn, err := r.Get(ctx, org, id)
	if err != nil {
		return nil, err
	}
	return cn.Items, nil
}

func (r creditNoteRepo) CreditedByFee(ctx context.Context, org core.OrganizationID, fees []core.FeeID) ([]creditnote.FeeCredit, error) {
	if len(fees) == 0 {
		return nil, nil
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.CreditedByFee(ctx, db.CreditedByFeeParams{OrganizationID: org.UUID(), FeeIds: uuids(fees)})
	if err != nil {
		return nil, fmt.Errorf("credited by fee: %w", mapErr(err))
	}
	sums := make(map[uuid.UUID]int64, len(rows))
	for _, r := range rows {
		if r.FeeID != nil {
			sums[*r.FeeID] = r.Amount
		}
	}
	// In the order of fees, each fee once.
	var out []creditnote.FeeCredit
	for _, id := range fees {
		if n, ok := sums[id.UUID()]; ok {
			out = append(out, creditnote.FeeCredit{FeeID: id, Amount: n})
			delete(sums, id.UUID())
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

func (r creditNoteRepo) NextSequentialID(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) (int64, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.MaxCreditNoteSequentialID(ctx, db.MaxCreditNoteSequentialIDParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()})
	if err != nil {
		return 0, fmt.Errorf("next credit note sequential id: %w", mapErr(err))
	}
	return n + 1, nil
}

func (r creditNoteRepo) Delete(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	status, err := q.GetCreditNoteStatus(ctx, db.GetCreditNoteStatusParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err := invoiceStatusErr("credit_note", id, status, err); err != nil {
		return err
	}
	if err := r.t.trackCreditNote(ctx, q, org, id); err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.DeleteDraftCreditNote(ctx, db.DeleteDraftCreditNoteParams{OrganizationID: org.UUID(), ID: id.UUID()})
		return err
	})
	return updateErr("credit_note", id, n, err)
}

func (r creditNoteRepo) DetachFees(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.DetachCreditNoteItemFees(ctx, db.DetachCreditNoteItemFeesParams{OrganizationID: org.UUID(), InvoiceID: invoice.UUID()}); err != nil {
		return fmt.Errorf("detach credit note item fees of invoice %s: %w", invoice, mapErr(err))
	}
	return nil
}
