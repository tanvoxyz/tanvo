package invoicing

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Codes of the rule errors of the draft operations.
const (
	// CodeNotDraft rejects a draft operation on another status.
	CodeNotDraft = "not_draft"
	// CodeTaxesPending rejects the finalization of a draft waiting for
	// its provider taxes.
	CodeTaxesPending = "cannot_finalize_with_pending_taxes"
	// CodeNotRefreshable rejects the refresh of an invoice that is not a
	// subscription invoice.
	CodeNotRefreshable = "not_refreshable"
)

// genMode is how [Service.generate] treats the invoice.
type genMode int

const (
	// modeNew generates a new invoice: a draft announces invoice.drafted.
	modeNew genMode = iota
	// modeRefresh recomputes a draft that stays a draft.
	modeRefresh
	// modeFinalize recomputes a draft and finalizes it.
	modeFinalize
)

// FinalizeDraft refreshes the draft id and finalizes it (lago
// Invoices::RefreshDraftAndFinalizeService): the fees, deductions and
// taxes are computed again, balances are consumed, the invoice is
// numbered and redated, its draft credit notes are finalized and
// invoice.created is emitted. A finalized invoice is returned as is.
func (s *Service) FinalizeDraft(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	return s.onInvoice(ctx, org, id, s.finalizeDraft)
}

// RefreshDraft recomputes the draft id (lago Invoices::RefreshDraftService):
// its fees, deductions and taxes; it stays a draft. Any other status is
// returned as is; an invoice other than a subscription one fails with
// [CodeNotRefreshable].
func (s *Service) RefreshDraft(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	return s.onInvoice(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		if inv.Type != invoice.TypeSubscription {
			return core.NewRuleError(CodeNotRefreshable, "only subscription invoices are refreshed")
		}
		if inv.Status != invoice.StatusDraft {
			return nil
		}
		return s.regenerate(ctx, r, inv, modeRefresh)
	})
}

// onInvoice takes the lock of the invoice id in a transaction, loads it
// under the lock and runs fn on it (lago invoice.with_lock, as in
// Invoices::RefreshDraftAndFinalizeService and VoidService): concurrent
// finalizations, refreshes and voids of one invoice serialize, so a draft
// is numbered once and no whole-row write is lost.
func (s *Service) onInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID, fn func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error) (*invoice.Invoice, error) {
	var out *invoice.Invoice
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := kernel.LockInvoice(ctx, r, org, id); err != nil {
			return err
		}
		inv, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", id, err)
		}
		out = inv
		return fn(ctx, r, inv)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// finalizeDraft is [Service.FinalizeDraft] in the transaction of r.
func (s *Service) finalizeDraft(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	switch {
	case inv.Status == invoice.StatusFinalized:
		return nil
	case inv.Status != invoice.StatusDraft:
		return core.NewRuleError(CodeNotDraft, "only drafts are finalized")
	case inv.TaxStatus == invoice.TaxStatusPending:
		return core.NewRuleError(CodeTaxesPending, "the draft waits for its provider taxes")
	case inv.Type == invoice.TypeSubscription:
		return s.regenerate(ctx, r, inv, modeFinalize)
	}
	// Progressive billing drafts keep their fees: usage moved on since.
	org := inv.OrganizationID
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
	if err != nil {
		return err
	}
	iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load subscriptions of invoice %s: %w", inv.ID, err)
	}
	fs, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
	}
	if err := s.deduct.ResetDraft(ctx, r, inv); err != nil {
		return err
	}
	resetFeeTaxes(fs)
	resolver, err := s.planResolver(ctx, r, cenv, iss)
	if err != nil {
		return err
	}
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{resolver: resolver, vies: true})
	if err != nil {
		return err
	}
	return s.finish(ctx, r, cenv, inv, kindOf(inv, iss), st, modeFinalize)
}

// regenerate recomputes the subscription draft inv from its invoice
// subscriptions (lago RefreshDraftService): fees, invoice subscriptions,
// applied taxes, error details and progressive billing credits are
// deleted, then the subscriptions are billed again at the same instants.
func (s *Service) regenerate(ctx context.Context, r port.Repos, inv *invoice.Invoice, mode genMode) error {
	org := inv.OrganizationID
	oldTotal := inv.TotalAmount
	old, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load subscriptions of invoice %s: %w", inv.ID, err)
	}
	if len(old) == 0 {
		return fmt.Errorf("refresh invoice %s: %w: no subscription", inv.ID, core.ErrInvalidValue)
	}
	k := kindOf(inv, old)
	refreshNotes, err := s.draftCreditNotes(ctx, r, inv)
	if err != nil {
		return err
	}
	if err := s.deduct.ResetDraft(ctx, r, inv); err != nil {
		return err
	}
	// The credit note items let go of the fees about to be deleted, and
	// follow the regenerated ones in refreshNotes
	// (refresh_draft_service.rb:124).
	if refreshNotes != nil {
		if err := r.CreditNotes.DetachFees(ctx, org, inv.ID); err != nil {
			return fmt.Errorf("detach credit notes of invoice %s: %w", inv.ID, err)
		}
	}
	if err := s.clearInvoice(ctx, r, inv); err != nil {
		return err
	}
	inv.RefreshRequested = false
	var rows []*billed
	for _, is := range old {
		env, err := kernel.LoadSubscriptionEnv(ctx, r, org, is.SubscriptionID)
		if err != nil {
			return err
		}
		b := &billed{item: BillItem{SubscriptionID: is.SubscriptionID, Reason: is.Reason}, env: env}
		ok, err := s.resolveBoundaries(ctx, r, b, is.Timestamp)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		b.is.InvoiceID = inv.ID
		b.is.Recurring = is.Recurring
		if err := r.InvoiceSubscriptions.Create(ctx, b.is); err != nil {
			return fmt.Errorf("create invoice subscription of %s: %w", is.SubscriptionID, err)
		}
		rows = append(rows, b)
	}
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
	if err != nil {
		return err
	}
	// The invoice keeps no skip_charges flag: starting invoices are the
	// ones billed without charges (activation, end of trial).
	skipCharges := !slices.ContainsFunc(old, func(is *invoice.Subscription) bool {
		return is.Reason != invoice.InvoicingReasonSubscriptionStarting
	})
	if _, err := s.generate(ctx, r, inv, cenv, k, rows, skipCharges, mode, refreshNotes); err != nil {
		return err
	}
	// lago app/services/invoices/refresh_draft_service.rb:73-76.
	if inv.TotalAmount != oldTotal {
		if err := flagWalletRefresh(ctx, r, inv); err != nil {
			return err
		}
	}
	// The regenerated fees keep the invoice's creation instant, so a
	// refresh does not look like a fee billed today (lago
	// RefreshDraftService).
	fs, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
	}
	for _, f := range fs {
		if f.CreatedAt.Equal(inv.CreatedAt) {
			continue
		}
		f.CreatedAt = inv.CreatedAt
		if err := r.Fees.Update(ctx, f); err != nil {
			return fmt.Errorf("update fee %s: %w", f.ID, err)
		}
	}
	return nil
}

// draftCreditNotes returns what moves the credit notes of the draft inv
// to its regenerated subscription fees (lago RefreshDraftService with
// CreditNotes::RefreshDraftService): each note follows the new
// subscription fee of the subscription it credited, rescaled by the new
// fee amount. It is nil when inv has no credit note.
func (s *Service) draftCreditNotes(ctx context.Context, r port.Repos, inv *invoice.Invoice) (func(context.Context, port.Repos) error, error) {
	org := inv.OrganizationID
	notes, err := r.CreditNotes.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load credit notes of invoice %s: %w", inv.ID, err)
	}
	if len(notes) == 0 || s.creditNotes == nil {
		return nil, nil
	}
	fs, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
	}
	byID := make(map[core.FeeID]*invoice.Fee, len(fs))
	for _, f := range fs {
		byID[f.ID] = f
	}
	oldAmounts := map[core.CreditNoteItemID]int64{}
	subs := make([]core.SubscriptionID, len(notes))
	for i, cn := range notes {
		for _, it := range cn.Items {
			f, ok := byID[it.FeeID]
			if !ok {
				continue
			}
			oldAmounts[it.ID] = f.Amount
			if subs[i].IsZero() {
				subs[i] = f.SubscriptionID
			}
		}
	}
	return func(ctx context.Context, r port.Repos) error {
		fresh, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
		}
		for i, cn := range notes {
			var fee *invoice.Fee
			for _, f := range fresh {
				if f.Type == core.FeeTypeSubscription && f.SubscriptionID == subs[i] {
					fee = f
				}
			}
			// lago fails the refresh when the subscription has no new
			// fee (fee.id on nil, credit_notes/refresh_draft_service.rb:21).
			if fee == nil {
				return fmt.Errorf("refresh credit note %s: %w: no subscription fee regenerated", cn.ID, core.ErrInvalidValue)
			}
			if err := s.creditNotes.RefreshDraft(ctx, r, cn, fee, oldAmounts); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

// finish concludes a settled invoice and runs its follow-ups: the draft
// credit notes once finalized, then the webhooks
// and jobs of [Service.announce].
func (s *Service) finish(ctx context.Context, r port.Repos, cenv *kernel.CustomerEnv, inv *invoice.Invoice, k kernel.InvoiceKind, st settled, mode genMode) error {
	if err := s.conclude(ctx, r, cenv, inv, k, st, mode == modeFinalize); err != nil {
		return err
	}
	// An invoice held for a VIES check waits for the check, which
	// finalizes it (vies_check_job.rb:44-55): nothing pulls its taxes, and
	// a new draft is announced (subscription_service.rb:93-97).
	if st.taxFailure != nil || st.vies {
		if inv.Status == invoice.StatusDraft && mode == modeNew {
			return s.announceDraft(ctx, r, inv)
		}
		return nil
	}
	if inv.Status == invoice.StatusFinalized {
		if mode == modeFinalize {
			if _, err := s.creditNotes.FinalizeDrafts(ctx, r, inv); err != nil {
				return err
			}
		}
	}
	return s.announce(ctx, r, inv, createdEvent(inv), mode == modeNew)
}

// FinalizeDueDrafts enqueues the finalization of every draft whose grace
// period ended at at, in its customer's time zone (lago
// Invoices::FinalizeBatchService over the drafts due), and returns how
// many were enqueued. Each job redates its draft from at
// ([kernel.WithFinalizationTime]).
func (s *Service) FinalizeDueDrafts(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	n := 0
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		// The latest time zone (UTC+14) sees the latest date; each draft is
		// then checked in its customer's zone.
		drafts, err := r.Invoices.ListDraftsToFinalize(ctx, org, core.DateOf(at, latestZone))
		if err != nil {
			return fmt.Errorf("list drafts to finalize: %w", err)
		}
		envs := map[core.CustomerID]*kernel.CustomerEnv{}
		for _, inv := range drafts {
			cenv, ok := envs[inv.CustomerID]
			if !ok {
				if cenv, err = kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID); err != nil {
					return err
				}
				envs[inv.CustomerID] = cenv
			}
			// A draft without an expected finalization date is due on its
			// issuing date (lago Invoice.ready_to_be_finalized,
			// COALESCE(expected_finalization_date, issuing_date),
			// app/models/invoice.rb:148).
			due := inv.ExpectedFinalizationDate
			if due.IsZero() {
				due = inv.IssuingDate
			}
			if cenv.Today(at).Before(due) {
				continue
			}
			if err := kernel.Enqueue(ctx, r, org, kernel.JobFinalizeDraft, kernel.InvoiceArgs{InvoiceID: inv.ID, At: at}, "invoice:"+inv.ID.String(), s.d.Now()); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// latestZone is the time zone with the latest local date.
var latestZone = time.FixedZone("UTC+14", 14*60*60)

// RefreshFlaggedDrafts enqueues the refresh of every draft flagged for
// refresh (lago Invoices::RefreshDraftJob over ready_to_be_refreshed) and
// returns how many were enqueued.
func (s *Service) RefreshFlaggedDrafts(ctx context.Context, org core.OrganizationID) (int, error) {
	n := 0
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		drafts, err := r.Invoices.ListRefreshRequested(ctx, org)
		if err != nil {
			return fmt.Errorf("list drafts to refresh: %w", err)
		}
		for _, inv := range drafts {
			if err := kernel.Enqueue(ctx, r, org, kernel.JobRefreshDraft, kernel.InvoiceArgs{InvoiceID: inv.ID}, "invoice:"+inv.ID.String(), s.d.Now()); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
