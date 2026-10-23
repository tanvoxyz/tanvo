package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Rule codes of [Engine.MarkInvoiceOverdue], carried by *core.RuleError
// (lago Invoices::Payments::MarkOverdueService not_allowed_failure codes).
const (
	// CodeInvoiceNotFinalized: only a finalized invoice becomes overdue.
	CodeInvoiceNotFinalized = "invoice_not_finalized"
	// CodeInvoicePaymentAlreadySucceeded: the invoice is paid.
	CodeInvoicePaymentAlreadySucceeded = "invoice_payment_already_succeeded"
	// CodeInvoiceDueDateInFuture: the payment due date is not reached.
	CodeInvoiceDueDateInFuture = "invoice_due_date_in_future"
	// CodeInvoiceDisputeLost: the invoice lost a payment dispute.
	CodeInvoiceDisputeLost = "invoice_dispute_lost"
)

// MarkOverdueInvoices enqueues the [kernel.JobMarkInvoiceOverdue] of every
// invoice of org whose payment is overdue at the clock instant at and
// returns their number (lago Clock::MarkInvoicesAsPaymentOverdueJob,
// app/jobs/clock/mark_invoices_as_payment_overdue_job.rb:7-18): finalized,
// not paid, not yet overdue, without a lost dispute, with a payment due
// date before at (the date compared as its UTC midnight).
func (e *Engine) MarkOverdueInvoices(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	n := 0
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		invs, err := r.Invoices.ListOverdueCandidates(ctx, org, overdueDate(at))
		if err != nil {
			return fmt.Errorf("list overdue invoices: %w", err)
		}
		for _, inv := range invs {
			args := kernel.InvoiceArgs{InvoiceID: inv.ID}
			if err := kernel.Enqueue(ctx, r, org, kernel.JobMarkInvoiceOverdue, args, kernel.OverdueKey(inv.ID), time.Time{}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// overdueDate returns the date d such that a due date is before d exactly
// when its UTC midnight is before at (payment_due_date < Time.current).
func overdueDate(at time.Time) core.Date {
	today := core.DateOf(at, time.UTC)
	if at.Equal(today.Midnight(time.UTC)) {
		return today
	}
	return today.AddDays(1)
}

// MarkInvoiceOverdue marks the payment of invoice id overdue and emits
// invoice.payment_overdue (lago Invoices::Payments::MarkOverdueService,
// app/services/invoices/payments/mark_overdue_service.rb:19-33). An invoice
// not finalized, paid, not yet due at now or with a lost dispute is
// refused with a *core.RuleError.
func (e *Engine) MarkInvoiceOverdue(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	var out *invoice.Invoice
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := kernel.LockInvoice(ctx, r, org, id); err != nil {
			return err
		}
		inv, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", id, err)
		}
		now := e.d.Now()
		switch {
		case inv.Status != invoice.StatusFinalized:
			return core.NewRuleError(CodeInvoiceNotFinalized, "the invoice is not finalized")
		case inv.PaymentStatus == core.PaymentStatusSucceeded:
			return core.NewRuleError(CodeInvoicePaymentAlreadySucceeded, "the invoice is paid")
		case inv.PaymentDueDate.Midnight(time.UTC).After(now):
			return core.NewRuleError(CodeInvoiceDueDateInFuture, "the invoice payment is not due yet")
		case inv.PaymentDisputeLostAt != nil:
			return core.NewRuleError(CodeInvoiceDisputeLost, "the invoice lost a payment dispute")
		}
		inv.PaymentOverdue, inv.UpdatedAt = true, now
		if err := r.Invoices.Update(ctx, inv); err != nil {
			return fmt.Errorf("update invoice %s: %w", id, err)
		}
		out = inv
		return kernel.Emit(ctx, e.d, org, webhook.EventTypeInvoicePaymentOverdue, "invoice", kernel.ObjectID(inv.ID), inv)
	})
	if err != nil {
		return nil, fmt.Errorf("mark invoice %s overdue: %w", id, err)
	}
	return out, nil
}
