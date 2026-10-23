package invoicing

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// RetryAll enqueues a [kernel.JobRetryInvoices] retrying every failed
// invoice of the organization and returns how many (lago
// Invoices::RetryBatchService#call_async with Invoices::RetryAllJob).
//
// source: app/services/invoices/retry_batch_service.rb:13-19,40-42
func (s *Service) RetryAll(ctx context.Context, org core.OrganizationID) (int, error) {
	return s.enqueueBatch(ctx, org, kernel.JobRetryInvoices, invoice.Filter{Statuses: []invoice.Status{invoice.StatusFailed}}, nil)
}

// RetryAllPayments enqueues a [kernel.JobRetryInvoicePayments] retrying
// the payment of every finalized invoice of the organization whose
// payment is pending or failed and that no payment provider is handling,
// and returns how many (lago Invoices::Payments::RetryBatchService#call_async
// with Invoices::Payments::RetryAllJob).
//
// source: app/services/invoices/payments/retry_batch_service.rb:14-20,47-55
func (s *Service) RetryAllPayments(ctx context.Context, org core.OrganizationID) (int, error) {
	f := invoice.Filter{
		Statuses:        []invoice.Status{invoice.StatusFinalized},
		PaymentStatuses: []core.PaymentStatus{core.PaymentStatusPending, core.PaymentStatusFailed},
	}
	return s.enqueueBatch(ctx, org, kernel.JobRetryInvoicePayments, f, func(inv *invoice.Invoice) bool { return inv.ReadyForPaymentProcessing })
}

// enqueueBatch enqueues one job of kind with the invoices matching f and
// keep (when set), and returns how many; no job is enqueued without any.
func (s *Service) enqueueBatch(ctx context.Context, org core.OrganizationID, kind string, f invoice.Filter, keep func(*invoice.Invoice) bool) (int, error) {
	n := 0
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		invs, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
			p.Size = core.MaxPageSize
			return r.Invoices.List(ctx, org, f, p)
		})
		if err != nil {
			return fmt.Errorf("list invoices: %w", err)
		}
		var args kernel.InvoiceIDsArgs
		for _, inv := range invs {
			if keep == nil || keep(inv) {
				args.InvoiceIDs = append(args.InvoiceIDs, inv.ID)
			}
		}
		if n = len(args.InvoiceIDs); n == 0 {
			return nil
		}
		return kernel.Enqueue(ctx, r, org, kind, args, "", s.d.Now())
	})
	return n, err
}

// RetryInvoices runs a [kernel.JobRetryInvoices]: it retries the invoices
// ids one by one with [Service.Retry] and stops at the first failure,
// returning it (lago Invoices::RetryBatchService#call). It returns how
// many were retried.
//
// source: app/services/invoices/retry_batch_service.rb:21-34
func (s *Service) RetryInvoices(ctx context.Context, org core.OrganizationID, ids []core.InvoiceID) (int, error) {
	for i, id := range ids {
		if _, err := s.Retry(ctx, org, id); err != nil {
			return i, err
		}
	}
	return len(ids), nil
}

// RetryInvoicePayments runs a [kernel.JobRetryInvoicePayments]: it
// retries the payments of the invoices ids one by one with
// [Service.RetryPayment] and stops at the first failure, returning it
// (lago Invoices::Payments::RetryBatchService#call). It returns how many
// were retried.
//
// source: app/services/invoices/payments/retry_batch_service.rb:22-35
func (s *Service) RetryInvoicePayments(ctx context.Context, org core.OrganizationID, ids []core.InvoiceID) (int, error) {
	for i, id := range ids {
		if err := s.RetryPayment(ctx, org, id, PaymentMethodParams{}); err != nil {
			return i, err
		}
	}
	return len(ids), nil
}

// RetryTaxFailedInvoices runs a [kernel.JobRetryTaxFailedInvoices]: it
// retries every failed invoice of the organization with a tax error
// telling of the tax provider's API limit
// ([invoice.ErrorDetail.TaxRetried]), ignoring the rule errors of the
// retries, and returns how many were retried (lago
// Clock::RetryFailedInvoicesJob).
//
// source: app/jobs/clock/retry_failed_invoices_job.rb:7-16
func (s *Service) RetryTaxFailedInvoices(ctx context.Context, org core.OrganizationID) (int, error) {
	var ids []core.InvoiceID
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		invs, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
			p.Size = core.MaxPageSize
			return r.Invoices.List(ctx, org, invoice.Filter{Statuses: []invoice.Status{invoice.StatusFailed}}, p)
		})
		if err != nil {
			return fmt.Errorf("list failed invoices: %w", err)
		}
		for _, inv := range invs {
			details, err := r.ErrorDetails.ListByInvoice(ctx, org, inv.ID)
			if err != nil {
				return fmt.Errorf("load error details of invoice %s: %w", inv.ID, err)
			}
			if slices.ContainsFunc(details, (*invoice.ErrorDetail).TaxRetried) {
				ids = append(ids, inv.ID)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		// The retry's failure is ignored (lago Invoices::RetryService.call,
		// not call!): an invoice retried meanwhile is no longer failed.
		_, err := s.Retry(ctx, org, id)
		var rule *core.RuleError
		switch {
		case err == nil:
			n++
		case errors.As(err, &rule), errors.Is(err, core.ErrNotFound):
		default:
			return n, err
		}
	}
	return n, nil
}
