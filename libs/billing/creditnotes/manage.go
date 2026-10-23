package creditnotes

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// inTx runs fn in a transaction and returns its result.
func inTx[T any](ctx context.Context, tx port.Transactor, fn func(ctx context.Context, r port.Repos) (T, error)) (T, error) {
	var out T
	err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// get loads the credit note id; draft says whether a draft is found. A
// deleted credit note is never found (lago not_deleted).
func get(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CreditNoteID, draft bool) (*creditnote.CreditNote, error) {
	cn, err := r.CreditNotes.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) || (err == nil && (cn.Status == creditnote.StatusDeleted || !draft && cn.Status == creditnote.StatusDraft)) {
		return nil, notFound("credit_note", id)
	}
	if err != nil {
		return nil, fmt.Errorf("load credit note %s: %w", id, err)
	}
	return cn, nil
}

// Void voids the balance left on a finalized credit note.
func (s *Service) Void(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) (*creditnote.CreditNote, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*creditnote.CreditNote, error) {
		cn, err := get(ctx, r, org, id, false)
		if err != nil {
			return nil, err
		}
		if err := kernel.LockCustomer(ctx, r, org, cn.CustomerID, customer.LockScopeCreditNotes); err != nil {
			return nil, err
		}
		// Reload under the lock: an invoice may have consumed the balance.
		if cn, err = get(ctx, r, org, id, false); err != nil {
			return nil, err
		}
		if !cn.Voidable() {
			return nil, rule(CodeNoVoidableAmount, "credit note has no balance to void")
		}
		if err := cn.Void(s.d.Now()); err != nil {
			return nil, fmt.Errorf("void credit note %s: %w", id, err)
		}
		if err := r.CreditNotes.Update(ctx, cn); err != nil {
			return nil, fmt.Errorf("update credit note %s: %w", id, err)
		}
		return cn, nil
	})
}

// Delete soft-deletes a draft credit note: it moves to the hidden,
// terminal deleted status (lago CreditNotes::DeleteService).
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cn, err := get(ctx, r, org, id, true)
		if err != nil {
			return err
		}
		if cn.Status != creditnote.StatusDraft {
			return rule(CodeNotDeletable, "only draft credit notes can be deleted")
		}
		if err := cn.MarkDeleted(s.d.Now()); err != nil {
			return fmt.Errorf("delete credit note %s: %w", id, err)
		}
		if err := r.CreditNotes.Update(ctx, cn); err != nil {
			return fmt.Errorf("delete credit note %s: %w", id, err)
		}
		return nil
	})
}

// UpdateInput changes a finalized credit note; nil fields are kept.
type UpdateInput struct {
	// RefundStatus sets the refund status.
	RefundStatus *core.RefundStatus
	// Metadata replaces the metadata, or with MergeMetadata merges into it
	// key by key; an empty list clears them.
	Metadata *core.Metadata
	// MergeMetadata merges Metadata instead of replacing.
	MergeMetadata bool
}

// Update changes the refund status or the metadata of a finalized credit
// note.
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.CreditNoteID, in UpdateInput) (*creditnote.CreditNote, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*creditnote.CreditNote, error) {
		cn, err := get(ctx, r, org, id, false)
		if err != nil {
			return nil, err
		}
		now := s.d.Now()
		changed := false
		// Assigning succeeded stamps refunded_at again even when the status
		// is unchanged (app/services/credit_notes/update_service.rb:19-22).
		if in.RefundStatus != nil {
			var v core.Validator
			if !v.Enum("refund_status", *in.RefundStatus) {
				return nil, v.Err()
			}
			if *in.RefundStatus != cn.RefundStatus || *in.RefundStatus == core.RefundStatusSucceeded {
				changed = true
			}
			cn.RefundStatus = *in.RefundStatus
			if cn.RefundStatus == core.RefundStatusSucceeded {
				cn.RefundedAt = &now
			}
		}
		if in.Metadata != nil {
			md := mergeMetadata(cn.Metadata, *in.Metadata, in.MergeMetadata)
			if !slices.Equal(md, cn.Metadata) {
				cn.Metadata = md
				changed = true
			}
		}
		if !changed {
			return cn, nil
		}
		cn.UpdatedAt = now
		if err := cn.Validate(); err != nil {
			return nil, err
		}
		if err := r.CreditNotes.Update(ctx, cn); err != nil {
			return nil, fmt.Errorf("update credit note %s: %w", id, err)
		}
		return cn, nil
	})
}

func mergeMetadata(old, md core.Metadata, merge bool) core.Metadata {
	if !merge {
		if len(md) == 0 {
			return nil
		}
		return md.Clone()
	}
	out := old.Clone()
	for _, it := range md {
		if i := slices.IndexFunc(out, func(o core.MetadataItem) bool { return o.Key == it.Key }); i >= 0 {
			out[i] = it
			continue
		}
		out = append(out, it)
	}
	return out
}

// Recredit gives back amount of a consumed credit to the credit note (the
// invoice it paid was voided), in the transaction of r.
func (s *Service) Recredit(ctx context.Context, r port.Repos, cn *creditnote.CreditNote, amount int64) error {
	if cn.CreditStatus == creditnote.CreditStatusVoided {
		return rule(CodeCreditNoteVoided, "credit note is voided")
	}
	if err := cn.Recredit(amount, s.d.Now()); err != nil {
		return fmt.Errorf("recredit credit note %s: %w", cn.ID, err)
	}
	if err := r.CreditNotes.Update(ctx, cn); err != nil {
		return fmt.Errorf("update credit note %s: %w", cn.ID, err)
	}
	return nil
}

// RefreshDraft recomputes a draft credit note of a draft invoice being
// refreshed, in the transaction of r: its items move to fee, rescaled by
// the ratio of the new fee amount to oldFeeAmounts[item] (the amount of
// the fee the item credited before the refresh; a missing entry keeps the
// item), then the taxes and amounts are computed again. Finalized notes
// are left unchanged.
func (s *Service) RefreshDraft(ctx context.Context, r port.Repos, cn *creditnote.CreditNote, fee *invoice.Fee, oldFeeAmounts map[core.CreditNoteItemID]int64) error {
	if cn.Status != creditnote.StatusDraft {
		return nil
	}
	st, err := loadInvoice(ctx, r, cn.OrganizationID, cn.InvoiceID)
	if err != nil {
		return err
	}
	st.notes = slices.DeleteFunc(st.notes, func(o *creditnote.CreditNote) bool { return o.ID == cn.ID })
	if _, _, ok := st.fee(fee.ID); !ok {
		st.fees = append(st.fees, fee)
	}
	precise := money.Zero
	for i := range cn.Items {
		it := &cn.Items[i]
		it.FeeID = fee.ID
		if old, ok := oldFeeAmounts[it.ID]; ok {
			if old == 0 {
				it.PreciseAmount = money.Zero
			} else {
				it.PreciseAmount = it.PreciseAmount.Div(money.NewFromInt(old)).Mul(money.NewFromInt(fee.Amount))
			}
			it.Amount = kernel.RoundCents(it.PreciseAmount)
		}
		precise = precise.Add(it.PreciseAmount)
	}
	res, err := st.applyTaxes(s.d.IDs, cn, false)
	if err != nil {
		return err
	}
	cn.CreditAmount = kernel.RoundCents(precise.Truncate(dbScale).Sub(res.PreciseCouponsAdjustmentAmountCents).Add(money.NewFromInt(cn.TaxesAmount)))
	cn.BalanceAmount = cn.CreditAmount
	cn.TotalAmount = cn.CreditAmount + cn.RefundAmount
	adjustRounding(cn)
	cn.UpdatedAt = s.d.Now()
	if err := r.CreditNotes.Update(ctx, cn); err != nil {
		return fmt.Errorf("update credit note %s: %w", cn.ID, err)
	}
	return nil
}

// dbScale is the scale precise amounts are stored with.
const dbScale = 5
