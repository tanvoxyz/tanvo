package creditnotes

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// refundFailure is the payload of credit_note.refund_failure.
type refundFailure struct {
	// CreditNote is the credit note whose refund failed.
	CreditNote *creditnote.CreditNote `json:"credit_note"`
	// Message is the provider's error.
	Message string `json:"message"`
}

// refundJob is what a refund needs, loaded before calling the provider.
type refundJob struct {
	cn  *creditnote.CreditNote
	inv *invoice.Invoice
	pay *payment.Payment
}

// Refund sends the refund of a finalized credit note to the payment
// provider that collected the invoice (the [kernel.JobRefundCreditNote]
// job). Nothing is sent when the note refunds nothing, its refund is no
// longer pending, it already has a provider refund, the invoice lost a
// dispute, no successful payment can be refunded, or no gateway is wired.
// The provider's refund is recorded as a payment.Refund and its status
// copied to the note. A gateway error returns an error wrapping
// [ErrRefund]; when it wraps [ErrRefundRejected] the refund fails first
// and credit_note.refund_failure is emitted, otherwise the refund stays
// pending so a retry sends it again under the same idempotency key, the
// credit note's ID ([kernel.RefundRequest.IdempotencyKey]).
func (s *Service) Refund(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) (*payment.Refund, error) {
	if s.d.Refunds == nil {
		return nil, nil
	}
	job, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*refundJob, error) {
		cn, err := get(ctx, r, org, id, false)
		if err != nil {
			return nil, err
		}
		if !cn.Refunded() || cn.RefundStatus != core.RefundStatusPending {
			return nil, nil
		}
		// A provider refund still pending is not sent twice.
		sent, err := r.Refunds.ListByCreditNote(ctx, org, cn.ID)
		if err != nil {
			return nil, fmt.Errorf("list refunds of credit note %s: %w", cn.ID, err)
		}
		if len(sent) > 0 {
			return nil, nil
		}
		inv, err := r.Invoices.Get(ctx, org, cn.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", cn.InvoiceID, err)
		}
		if inv.PaymentDisputeLostAt != nil {
			return nil, nil
		}
		p, err := refundablePayment(ctx, r, inv)
		if err != nil || p == nil {
			return nil, err
		}
		return &refundJob{cn: cn, inv: inv, pay: p}, nil
	})
	if err != nil || job == nil {
		return nil, err
	}
	res, gwErr := s.d.Refunds.Refund(ctx, kernel.RefundRequest{
		IdempotencyKey: job.cn.ID.String(),
		CreditNote:     job.cn, Invoice: job.inv, PaymentID: job.pay.ID, Amount: job.cn.RefundAmount, Currency: job.cn.Currency,
	})
	if gwErr != nil && !errors.Is(gwErr, ErrRefundRejected) {
		return nil, fmt.Errorf("%w: credit note %s: %w", ErrRefund, id, gwErr)
	}
	var refund *payment.Refund
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		// A concurrent run of the same refund sent the same idempotency
		// key: under the note's lock, the first records the outcome and
		// the others return it.
		if err := kernel.LockCreditNote(ctx, r, org, id); err != nil {
			return err
		}
		cn, err := get(ctx, r, org, id, false)
		if err != nil {
			return err
		}
		sent, err := r.Refunds.ListByCreditNote(ctx, org, cn.ID)
		if err != nil {
			return fmt.Errorf("list refunds of credit note %s: %w", cn.ID, err)
		}
		if len(sent) > 0 {
			refund, gwErr = sent[0], nil
			return nil
		}
		if cn.RefundStatus != core.RefundStatusPending {
			gwErr = nil
			return nil
		}
		if gwErr != nil {
			return s.failRefund(ctx, r, cn, gwErr.Error())
		}
		now := s.d.Now()
		refund = &payment.Refund{
			ID: core.NewID[core.RefundID](s.d.IDs), OrganizationID: org, PaymentID: job.pay.ID,
			Reason: payment.RefundReasonCreditNote, CreditNoteID: cn.ID, IntegrationID: job.pay.IntegrationID,
			Amount: cn.RefundAmount, Currency: cn.Currency, Status: res.Status, CreatedAt: now, UpdatedAt: now,
		}
		if refund.Status == "" {
			refund.Status = core.RefundStatusPending
		}
		if res.ProviderRefundID != "" {
			refund.ProviderRefundID = &res.ProviderRefundID
		}
		if job.pay.IntegrationID.IsZero() {
			refund.IntegrationID = core.IntegrationID{}
		}
		if err := r.Refunds.Create(ctx, refund); err != nil {
			return fmt.Errorf("create refund of credit note %s: %w", cn.ID, err)
		}
		if refund.Status == core.RefundStatusFailed {
			return s.failRefund(ctx, r, cn, "refund failed")
		}
		return s.setRefundStatus(ctx, r, cn, refund.Status)
	})
	if err != nil {
		return nil, err
	}
	if gwErr != nil {
		return nil, fmt.Errorf("%w: credit note %s: %w", ErrRefund, id, gwErr)
	}
	return refund, nil
}

// UpdateRefundStatus records the status a payment provider reports for
// the refund it identifies by providerRefundID, and copies it to the
// credit note. A refund of a note already refunded is left unchanged; a
// failure emits credit_note.refund_failure and returns a *core.RuleError
// with code [CodeRefundFailed] once the status is stored.
func (s *Service) UpdateRefundStatus(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerRefundID string, status core.RefundStatus) (*payment.Refund, error) {
	var failed bool
	refund, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Refund, error) {
		var v core.Validator
		if !v.Enum("status", status) {
			return nil, v.Err()
		}
		refund, err := r.Refunds.GetByProviderRefundID(ctx, org, integration, providerRefundID)
		if errors.Is(err, core.ErrNotFound) {
			return nil, fmt.Errorf("refund %s: %w", providerRefundID, core.ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("load refund %s: %w", providerRefundID, err)
		}
		if refund.CreditNoteID.IsZero() {
			return refund, nil
		}
		cn, err := get(ctx, r, org, refund.CreditNoteID, true)
		if err != nil {
			return nil, err
		}
		if cn.RefundStatus == core.RefundStatusSucceeded || refund.Status == status {
			return refund, nil
		}
		// The provider's status is taken as is, a failed refund may still
		// succeed (lago StripeService#update_status).
		refund.Status, refund.UpdatedAt = status, s.d.Now()
		if err := r.Refunds.Update(ctx, refund); err != nil {
			return nil, fmt.Errorf("update refund %s: %w", refund.ID, err)
		}
		if status == core.RefundStatusFailed {
			failed = true
			return refund, s.failRefund(ctx, r, cn, "payment refund failed")
		}
		return refund, s.setRefundStatus(ctx, r, cn, status)
	})
	if err != nil {
		return nil, err
	}
	if failed {
		return refund, rule(CodeRefundFailed, "refund failed to perform")
	}
	return refund, nil
}

// setRefundStatus sets the refund status of cn to the provider's status
// unless it is already there, recording RefundedAt on success (lago
// update_credit_note_status, which checks no transition).
func (s *Service) setRefundStatus(ctx context.Context, r port.Repos, cn *creditnote.CreditNote, status core.RefundStatus) error {
	if cn.RefundStatus == status {
		return nil
	}
	now := s.d.Now()
	cn.RefundStatus, cn.UpdatedAt = status, now
	if status == core.RefundStatusSucceeded {
		cn.RefundedAt = &now
	}
	if err := r.CreditNotes.Update(ctx, cn); err != nil {
		return fmt.Errorf("update credit note %s: %w", cn.ID, err)
	}
	return nil
}

// failRefund fails the refund of cn and emits credit_note.refund_failure.
func (s *Service) failRefund(ctx context.Context, r port.Repos, cn *creditnote.CreditNote, message string) error {
	if err := s.setRefundStatus(ctx, r, cn, core.RefundStatusFailed); err != nil {
		return err
	}
	return kernel.Emit(ctx, s.d, cn.OrganizationID, webhook.EventTypeCreditNoteRefundFailure, "credit_note", cn.ID.UUID(),
		refundFailure{CreditNote: cn, Message: message})
}

// ReportTaxes reports a finalized credit note to the tax provider of its
// customer (the [kernel.JobReportCreditNoteTaxes] job); customers without
// a provider and notes of credit invoices are skipped (lago
// CreditNotes::ProviderTaxes::ReportJob).
func (s *Service) ReportTaxes(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) error {
	if s.d.Taxes == nil {
		return nil
	}
	type report struct {
		cn  *creditnote.CreditNote
		inv *invoice.Invoice
		env *kernel.CustomerEnv
	}
	rep, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*report, error) {
		cn, err := get(ctx, r, org, id, false)
		if err != nil {
			return nil, err
		}
		inv, err := r.Invoices.Get(ctx, org, cn.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", cn.InvoiceID, err)
		}
		if inv.Type == invoice.TypeCredit {
			return nil, nil
		}
		env, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
		if err != nil {
			return nil, err
		}
		return &report{cn: cn, inv: inv, env: env}, nil
	})
	if err != nil || rep == nil {
		return err
	}
	ok, err := s.d.Taxes.Enabled(ctx, rep.env.Customer)
	if err != nil {
		return fmt.Errorf("%w: %w", kernel.ErrTaxProvider, err)
	}
	if !ok {
		return nil
	}
	if err := s.d.Taxes.ReportCreditNote(ctx, rep.env.Customer, rep.inv, rep.cn); err != nil {
		return fmt.Errorf("%w: report credit note %s: %w", kernel.ErrTaxProvider, id, err)
	}
	return nil
}
