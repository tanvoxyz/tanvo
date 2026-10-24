package creditnotes

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// ItemInput is an amount to credit on one fee of the invoice.
type ItemInput struct {
	// FeeID is the credited fee.
	FeeID core.FeeID
	// Amount is the credited amount in minor units, before coupons and
	// taxes; automatic credit notes credit fractions of a minor unit.
	Amount money.Decimal
}

// CreateInput is a credit note to issue on an invoice (domain §13.2).
type CreateInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the credited invoice.
	InvoiceID core.InvoiceID
	// Items are the credited fee amounts.
	Items []ItemInput
	// Reason is why the note is issued; zero means other.
	Reason creditnote.Reason
	// Description is free text.
	Description *string
	// CreditAmount, RefundAmount and OffsetAmount split the note's total,
	// in minor units, between the customer's credit balance, a refund and
	// an offset of the invoice's due amount.
	CreditAmount int64
	RefundAmount int64
	OffsetAmount int64
	// Metadata is free key/value data.
	Metadata core.Metadata
	// Automatic is true for the credit notes billing issues itself
	// (terminations, upgrades, progressive billing), which skip the checks
	// of the invoice's type and payment status.
	Automatic bool
	// Preview computes the credit note without storing anything.
	Preview bool
}

// Create issues a credit note on an invoice in its own transaction.
func (s *Service) Create(ctx context.Context, in CreateInput) (*creditnote.CreditNote, error) {
	var out *creditnote.CreditNote
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cn, err := s.CreateIn(ctx, r, in)
		out = cn
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CreateIn issues a credit note on an invoice in the transaction of r:
// it checks the items and the split, computes the taxes, coupons
// adjustment and rounding, numbers and stores the note, settles its offset
// and, once finalized, emits credit_note.created and enqueues its document,
// refund and tax report jobs.
func (s *Service) CreateIn(ctx context.Context, r port.Repos, in CreateInput) (*creditnote.CreditNote, error) {
	st, err := lockAndLoad(ctx, r, in.OrganizationID, in.InvoiceID, in.Preview)
	if err != nil {
		return nil, err
	}
	return s.create(ctx, r, st, in)
}

func (s *Service) create(ctx context.Context, r port.Repos, st *invoiceState, in CreateInput) (*creditnote.CreditNote, error) {
	inv := st.inv
	if !in.Automatic && !st.validTypeOrStatus(in) {
		return nil, rule(CodeInvalidTypeOrStatus, "invoice cannot be credited in its type or status")
	}
	env, err := kernel.LoadCustomerEnv(ctx, r, inv.OrganizationID, inv.CustomerID, inv.BillingEntityID)
	if err != nil {
		return nil, err
	}
	cn, err := s.build(st, in, env.Today(s.d.Now()))
	if err != nil {
		return nil, err
	}
	if in.Preview {
		return cn, nil
	}
	if err := cn.Validate(); err != nil {
		return nil, err
	}
	seq, err := kernel.CreditNoteNumber(ctx, r, inv)
	if err != nil {
		return nil, err
	}
	cn.SequentialID = seq
	cn.Number = creditnote.Number(inv.Number, seq)
	if err := r.CreditNotes.Create(ctx, cn); err != nil {
		return nil, fmt.Errorf("create credit note of invoice %s: %w", inv.ID, err)
	}
	if cn.OffsetAmount > 0 {
		if err := s.settle(ctx, r, inv, cn); err != nil {
			return nil, err
		}
	}
	if inv.Type == invoice.TypeCredit && st.wallet != nil && s.wallets != nil {
		if err := s.wallets.VoidCreditNoteCredits(ctx, r, st.wallet, st.walletTx, cn); err != nil {
			return nil, fmt.Errorf("void wallet credits of credit note %s: %w", cn.ID, err)
		}
	}
	if cn.Status == creditnote.StatusFinalized {
		if err := s.issued(ctx, r, inv, cn); err != nil {
			return nil, err
		}
	}
	return cn, nil
}

// build computes a credit note from in without storing it.
func (s *Service) build(st *invoiceState, in CreateInput, issuingDate core.Date) (*creditnote.CreditNote, error) {
	inv := st.inv
	now := s.d.Now()
	cn := &creditnote.CreditNote{
		ID:             core.NewID[core.CreditNoteID](s.d.IDs),
		OrganizationID: inv.OrganizationID,
		InvoiceID:      inv.ID,
		CustomerID:     inv.CustomerID,
		Status:         creditnote.StatusFinalized,
		Reason:         cmp.Or(in.Reason, creditnote.ReasonOther),
		Description:    in.Description,
		CreditAmount:   in.CreditAmount,
		RefundAmount:   in.RefundAmount,
		OffsetAmount:   in.OffsetAmount,
		Currency:       inv.Currency,
		IssuingDate:    issuingDate,
		Metadata:       in.Metadata.Clone(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if inv.Status == invoice.StatusDraft && !in.Preview {
		cn.Status = creditnote.StatusDraft
	}
	var v core.Validator
	v.Enum("reason", cn.Reason)
	v.Check(len(in.Items) > 0, "items", core.CodeRequired)
	if err := v.Err(); err != nil {
		return nil, err
	}
	for i, it := range in.Items {
		f, _, ok := st.fee(it.FeeID)
		if !ok {
			return nil, notFound("fee", it.FeeID)
		}
		amount := kernel.RoundCents(it.Amount)
		if err := st.validItem(i, amount, f); err != nil {
			return nil, err
		}
		cn.Items = append(cn.Items, creditnote.Item{
			ID: core.NewID[core.CreditNoteItemID](s.d.IDs), FeeID: f.ID, Amount: amount, PreciseAmount: it.Amount,
		})
		st.credited[f.ID] += amount
	}
	// A note crediting all that is left absorbs the tax rounding of the
	// earlier notes. A preview counts its own items too, so it shows the
	// note a create would store.
	adjust := st.creditable() == 0
	if _, err := st.applyTaxes(s.d.IDs, cn, adjust); err != nil {
		return nil, err
	}
	if err := st.validAmounts(cn); err != nil {
		return nil, err
	}
	if cn.Credited() {
		cn.CreditStatus = creditnote.CreditStatusAvailable
	}
	if cn.Refunded() {
		cn.RefundStatus = core.RefundStatusPending
	}
	cn.TotalAmount = cn.CreditAmount + cn.RefundAmount + cn.OffsetAmount
	cn.BalanceAmount = cn.CreditAmount
	adjustRounding(cn)
	return cn, nil
}

// validTypeOrStatus reports whether a manual credit note may be issued: a
// credit invoice needs its active wallet and either a successful payment,
// or only an offset while its payment is pending or failed.
func (st *invoiceState) validTypeOrStatus(in CreateInput) bool {
	inv := st.inv
	if inv.Type != invoice.TypeCredit {
		return true
	}
	if st.wallet == nil {
		return false
	}
	switch inv.PaymentStatus {
	case core.PaymentStatusPending, core.PaymentStatusFailed:
		return in.CreditAmount <= 0 && in.RefundAmount <= 0
	default:
		return inv.PaymentStatus == core.PaymentStatusSucceeded
	}
}

// applyTaxes computes the coupons adjustment and the taxes of cn's items;
// adjust takes the tax rounding of the invoice's earlier notes off.
func (st *invoiceState) applyTaxes(ids core.IDGenerator, cn *creditnote.CreditNote, adjust bool) (tax.CreditNoteTaxes, error) {
	items := make([]tax.CreditNoteItem, len(cn.Items))
	for i, it := range cn.Items {
		_, idx, _ := st.fee(it.FeeID)
		items[i] = tax.CreditNoteItem{Fee: idx, PreciseAmountCents: it.PreciseAmount}
	}
	in := tax.CreditNoteTaxesInput{Invoice: st.taxInvoice(), Items: items}
	if adjust {
		in.TaxesRoundingAdjustmentCents = st.taxesRoundingAdjustment()
	}
	res, err := tax.ComputeCreditNoteTaxes(in)
	if err != nil {
		if errors.Is(err, tax.ErrInvoiceAppliedTaxNotFound) {
			return res, fmt.Errorf("%w: %w", rule(CodeInvoiceAppliedTaxNotFound, "fee tax not applied to the invoice"), err)
		}
		return res, fmt.Errorf("credit note taxes: %w", err)
	}
	cn.PreciseCouponsAdjustmentAmount = res.PreciseCouponsAdjustmentAmountCents
	cn.CouponsAdjustmentAmount = res.CouponsAdjustmentAmountCents
	cn.PreciseTaxesAmount = res.PreciseTaxesAmountCents
	cn.TaxesAmount = res.TaxesAmountCents
	cn.TaxesRate = res.TaxesRate
	cn.SubTotalExcludingTaxesAmount = tax.CreditNoteSubTotal(items, res.PreciseCouponsAdjustmentAmountCents)
	cn.AppliedTaxes = cn.AppliedTaxes[:0]
	for _, t := range res.AppliedTaxes {
		cn.AppliedTaxes = append(cn.AppliedTaxes, creditnote.AppliedTax{
			ID:    core.NewID[core.CreditNoteAppliedTaxID](ids),
			TaxID: st.appliedTaxID(t.TaxCode, t.TaxRate), TaxCode: t.TaxCode, TaxName: t.TaxName,
			TaxDescription: ptr(t.TaxDescription), TaxRate: t.TaxRate,
			BaseAmount: t.BaseAmountCents, Amount: t.AmountCents, PreciseAmount: t.PreciseAmountCents,
		})
	}
	return res, nil
}

// adjustRounding moves the total by a cent so that the sub total plus the
// taxes is the total (domain §13.2).
func adjustRounding(cn *creditnote.CreditNote) {
	a := tax.AdjustCreditNoteRounding(tax.CreditNoteAmounts{
		SubTotalExcludingTaxesAmountCents: cn.SubTotalExcludingTaxesAmount,
		TaxesAmountCents:                  cn.TaxesAmount,
		TotalAmountCents:                  cn.TotalAmount,
		CreditAmountCents:                 cn.CreditAmount,
		RefundAmountCents:                 cn.RefundAmount,
		OffsetAmountCents:                 cn.OffsetAmount,
		BalanceAmountCents:                cn.BalanceAmount,
	})
	cn.TotalAmount, cn.CreditAmount, cn.RefundAmount, cn.OffsetAmount, cn.BalanceAmount =
		a.TotalAmountCents, a.CreditAmountCents, a.RefundAmountCents, a.OffsetAmountCents, a.BalanceAmountCents
}

// settle records the offset of cn as a settlement of the invoice, marking
// it paid once nothing is due.
func (s *Service) settle(ctx context.Context, r port.Repos, inv *invoice.Invoice, cn *creditnote.CreditNote) error {
	now := s.d.Now()
	set := &invoice.Settlement{
		ID: core.NewID[core.InvoiceSettlementID](s.d.IDs), OrganizationID: inv.OrganizationID, InvoiceID: inv.ID,
		Type: invoice.SettlementTypeCreditNote, CreditNoteID: cn.ID, Amount: cn.OffsetAmount, Currency: inv.Currency, CreatedAt: now,
	}
	if err := r.InvoiceSettlements.Create(ctx, set); err != nil {
		return fmt.Errorf("settle invoice %s with credit note %s: %w", inv.ID, cn.ID, err)
	}
	inv.OffsettedAmount += cn.OffsetAmount
	inv.UpdatedAt = now
	paid := inv.TotalAmount-inv.TotalSettledAmount() <= 0 && inv.PaymentStatus != core.PaymentStatusSucceeded
	if paid {
		inv.PaymentStatus = core.PaymentStatusSucceeded
	}
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return fmt.Errorf("update invoice %s: %w", inv.ID, err)
	}
	if paid {
		// The settlement updates the invoice through lago
		// Invoices::UpdateService (invoice_settlements/create_service.rb,
		// invoices/update_service.rb:76-79,105-107).
		if err := s.d.LogInvoice(ctx, r, auditlog.ActivityInvoicePaymentStatusUpdated, inv); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, inv.OrganizationID, webhook.EventTypeInvoicePaymentStatusUpdated, "invoice", inv.ID.UUID(), inv)
	}
	return nil
}

// issued runs the effects of a finalized credit note: its webhook, its
// document, its refund and its report to the tax provider.
func (s *Service) issued(ctx context.Context, r port.Repos, inv *invoice.Invoice, cn *creditnote.CreditNote) error {
	org := cn.OrganizationID
	if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeCreditNoteCreated, "credit_note", cn.ID.UUID(), cn); err != nil {
		return err
	}
	// credit_notes/create_service.rb:100,
	// invoices/refresh_draft_and_finalize_service.rb:59.
	if err := s.logActivity(ctx, r, auditlog.ActivityCreditNoteCreated, cn); err != nil {
		return err
	}
	args := kernel.CreditNoteArgs{CreditNoteID: cn.ID}
	if err := kernel.Enqueue(ctx, r, org, kernel.JobGenerateCreditNoteDocument, args, "", s.d.Now()); err != nil {
		return err
	}
	if cn.Refunded() && inv.PaymentStatus == core.PaymentStatusSucceeded {
		p, err := refundablePayment(ctx, r, inv)
		if err != nil {
			return err
		}
		if p != nil {
			if err := kernel.Enqueue(ctx, r, org, kernel.JobRefundCreditNote, args, "refund:"+cn.ID.String(), s.d.Now()); err != nil {
				return err
			}
		}
	}
	if s.d.Taxes != nil {
		if err := kernel.Enqueue(ctx, r, org, kernel.JobReportCreditNoteTaxes, args, "", s.d.Now()); err != nil {
			return err
		}
	}
	return nil
}

// FinalizeDrafts finalizes the draft credit notes of an invoice being
// finalized and runs their effects, in the transaction of r.
func (s *Service) FinalizeDrafts(ctx context.Context, r port.Repos, inv *invoice.Invoice) ([]*creditnote.CreditNote, error) {
	notes, err := r.CreditNotes.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load credit notes of invoice %s: %w", inv.ID, err)
	}
	var out []*creditnote.CreditNote
	for _, cn := range notes {
		if cn.Status != creditnote.StatusDraft {
			continue
		}
		if err := cn.Finalize(s.d.Now()); err != nil {
			return nil, fmt.Errorf("finalize credit note %s: %w", cn.ID, err)
		}
		if err := r.CreditNotes.Update(ctx, cn); err != nil {
			return nil, fmt.Errorf("update credit note %s: %w", cn.ID, err)
		}
		if err := s.issued(ctx, r, inv, cn); err != nil {
			return nil, err
		}
		out = append(out, cn)
	}
	return out, nil
}

// refundablePayment returns the latest successful payment of the invoice,
// else of a paid payment request including it; nil when there is none.
func refundablePayment(ctx context.Context, r port.Repos, inv *invoice.Invoice) (*payment.Payment, error) {
	org := inv.OrganizationID
	ps, err := r.Payments.ListByPayable(ctx, org, payment.InvoicePayable(inv.ID))
	if err != nil {
		return nil, fmt.Errorf("load payments of invoice %s: %w", inv.ID, err)
	}
	if p := latestSucceeded(ps); p != nil {
		return p, nil
	}
	reqs, err := r.PaymentRequests.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load payment requests of invoice %s: %w", inv.ID, err)
	}
	var all []*payment.Payment
	for _, req := range reqs {
		if req.PaymentStatus != core.PaymentStatusSucceeded {
			continue
		}
		ps, err := r.Payments.ListByPayable(ctx, org, payment.RequestPayable(req.ID))
		if err != nil {
			return nil, fmt.Errorf("load payments of payment request %s: %w", req.ID, err)
		}
		all = append(all, ps...)
	}
	return latestSucceeded(all), nil
}

func latestSucceeded(ps []*payment.Payment) *payment.Payment {
	var out *payment.Payment
	for _, p := range ps {
		if p.Status != payment.StateSucceeded {
			continue
		}
		if out == nil || p.CreatedAt.After(out.CreatedAt) {
			out = p
		}
	}
	return out
}

// sortedFees returns the fees by descending amount, keeping their order
// among equal amounts.
func sortedFees(fees []*invoice.Fee) []*invoice.Fee {
	out := slices.Clone(fees)
	slices.SortStableFunc(out, func(a, b *invoice.Fee) int { return cmp.Compare(b.Amount, a.Amount) })
	return out
}

// logActivity records the activity log t of cn.
func (s *Service) logActivity(ctx context.Context, r port.Repos, t auditlog.ActivityType, cn *creditnote.CreditNote) error {
	return s.d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: cn.OrganizationID, ResourceType: auditlog.ResourceCreditNote, ResourceID: cn.ID.UUID(),
		Object: cn, CustomerID: cn.CustomerID,
	})
}
