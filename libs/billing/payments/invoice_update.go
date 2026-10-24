package payments

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Codes of the rule errors of invoice updates (lago
// Invoices::UpdateService and Api::V1::InvoicesController#update).
const (
	// CodeUpdateOnVoidedInvoice rejects the update of a voided invoice.
	CodeUpdateOnVoidedInvoice = "update_on_voided_invoice"
	// CodeMetadataOnDraftInvoice rejects metadata on a draft.
	CodeMetadataOnDraftInvoice = "metadata_on_draft_invoice"
	// CodePaymentStatusUpdateOnDraftInvoice rejects a payment status
	// change of a draft.
	CodePaymentStatusUpdateOnDraftInvoice = "payment_status_update_on_draft_invoice"
)

// InvoiceUpdate is the change of an invoice through the API (lago
// Invoices::UpdateService params): nil fields are left unchanged.
type InvoiceUpdate struct {
	// PaymentStatus is the new payment status.
	PaymentStatus *core.PaymentStatus
	// Metadata replaces the invoice's metadata (lago
	// Invoices::Metadata::UpdateService keeps the listed items and
	// deletes the others).
	Metadata *core.Metadata
}

// UpdateInvoice updates the payment status and metadata of a visible
// invoice (lago Invoices::UpdateService with webhook_notification). A
// voided invoice is not updatable; a draft takes neither metadata nor a
// payment status change. A succeeded payment clears the overdue flag and
// resets the customer's dunning attempts in the currency when a dunning
// campaign requested the invoice. After the change the fees take the
// invoice's payment status (lago Invoices::UpdateFeesPaymentStatusJob), a
// succeeded payment expires an active checkout link, a changed status is
// announced by invoice.payment_status_updated, and a credit invoice whose
// payment succeeded or failed settles its wallet purchase (lago
// Invoices::PrepaidCreditJob).
func (s *Service) UpdateInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID, in InvoiceUpdate) (*invoice.Invoice, error) {
	if in.PaymentStatus != nil && !in.PaymentStatus.Valid() {
		return nil, fieldError("payment_status", core.CodeInvalid)
	}
	if in.Metadata != nil && len(*in.Metadata) > core.InvoiceMetadataLimits().MaxItems {
		return nil, fieldError("metadata", core.CodeInvalid)
	}
	inv, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*invoice.Invoice, error) {
		return s.updateInvoice(ctx, r, org, id, in)
	})
	if err != nil {
		return nil, err
	}
	if in.PaymentStatus != nil && inv.Type == invoice.TypeCredit && s.credits != nil &&
		(*in.PaymentStatus == core.PaymentStatusSucceeded || *in.PaymentStatus == core.PaymentStatusFailed) {
		// lago runs Invoices::PrepaidCreditJob after the commit.
		if err := s.credits.HandleCreditInvoicePayment(ctx, org, inv.ID, *in.PaymentStatus == core.PaymentStatusSucceeded); err != nil {
			s.d.Logger.ErrorContext(ctx, "payments: settle credit invoice purchase",
				"organization_id", org.String(), "invoice", inv.ID.String(), "error", err)
		}
	}
	return inv, nil
}

// updateInvoice is [Service.UpdateInvoice] in the transaction of r.
func (s *Service) updateInvoice(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID, in InvoiceUpdate) (*invoice.Invoice, error) {
	if err := kernel.LockInvoice(ctx, r, org, id); err != nil {
		return nil, err
	}
	inv, err := r.Invoices.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) || (err == nil && !visible(inv)) {
		return nil, notFound("invoice")
	}
	if err != nil {
		return nil, wrap(err, "load invoice %s", id)
	}
	if inv.Status == invoice.StatusVoided {
		return nil, core.NewRuleError(CodeUpdateOnVoidedInvoice, "a voided invoice cannot be updated")
	}
	if inv.Status == invoice.StatusDraft && in.Metadata != nil {
		return nil, core.NewRuleError(CodeMetadataOnDraftInvoice, "a draft invoice takes no metadata")
	}
	old := inv.PaymentStatus
	if in.PaymentStatus != nil {
		inv.PaymentStatus = *in.PaymentStatus
	}
	if inv.Status == invoice.StatusDraft && old != inv.PaymentStatus {
		return nil, core.NewRuleError(CodePaymentStatusUpdateOnDraftInvoice, "the payment status of a draft invoice cannot change")
	}
	now := s.d.Now()
	inv.UpdatedAt = now
	if inv.PaymentOverdue && inv.PaymentStatus == core.PaymentStatusSucceeded {
		inv.PaymentOverdue = false
		if err := s.resetDunning(ctx, r, inv, now); err != nil {
			return nil, err
		}
	}
	if in.Metadata != nil {
		inv.Metadata = in.Metadata.Clone()
	}
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return nil, wrap(err, "update invoice %s", inv.ID)
	}
	if in.PaymentStatus == nil {
		return inv, nil
	}
	if err := resolvePaymentGate(ctx, r, inv); err != nil {
		return nil, err
	}
	fees, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, wrap(err, "list fees of invoice %s", inv.ID)
	}
	status := invoice.FeePaymentStatus(inv.PaymentStatus)
	for _, f := range fees {
		if f.PaymentStatus == status {
			continue
		}
		f.PaymentStatus = status
		f.UpdatedAt = now
		if err := r.Fees.Update(ctx, f); err != nil {
			return nil, wrap(err, "update fee %s", f.ID)
		}
	}
	if inv.PaymentStatus == core.PaymentStatusSucceeded && old != core.PaymentStatusSucceeded {
		if err := s.expireCheckout(ctx, r, inv, now); err != nil {
			return nil, err
		}
	}
	if old != inv.PaymentStatus {
		// update_service.rb:76-79,105-107.
		if err := s.d.LogInvoice(ctx, r, auditlog.ActivityInvoicePaymentStatusUpdated, inv); err != nil {
			return nil, err
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoicePaymentStatusUpdated, "invoice", inv.ID.UUID(), inv); err != nil {
			return nil, err
		}
	}
	return inv, nil
}

// visible reports whether the API shows inv (lago Invoice.visible: draft,
// finalized, voided and failed).
func visible(inv *invoice.Invoice) bool {
	return invoice.Filter{}.Lists(inv.Status)
}
