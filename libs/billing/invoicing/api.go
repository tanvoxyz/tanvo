package invoicing

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Codes of the rule and validation errors of the invoice API (lago
// Api::V1::InvoicesController and the services it calls).
const (
	// CodeNotDeletable rejects the deletion of an invoice that is not a
	// draft (lago Invoices::DeleteService).
	CodeNotDeletable = "not_deletable"
	// CodeInvoiceSyncedToExternalSystem rejects the deletion of a draft
	// synced to an integration (lago Invoices::DeleteService).
	CodeInvoiceSyncedToExternalSystem = "invoice_synced_to_external_system"
	// CodeCreditNoteNotDeletable rejects the deletion of a draft with a
	// credit note that is not a draft (lago Invoices::DeleteService).
	CodeCreditNoteNotDeletable = "credit_note_not_deletable"
	// CodeInvalidStatus rejects a payment retry of a draft, voided or paid
	// invoice, and a retry of an invoice that did not fail (lago
	// Invoices::Payments::RetryService, Invoices::RetryService).
	CodeInvalidStatus = "invalid_status"
	// CodePaymentProcessing rejects a payment retry while the provider
	// handles a payment.
	CodePaymentProcessing = "payment_processor_is_currently_handling_payment"
	// CodeIsDraft rejects the download of a draft from the customer
	// portal (lago Invoices::GeneratePdfService).
	CodeIsDraft = "is_draft"
	// CodeNotDisputable rejects a lost dispute on an invoice neither
	// finalized nor voided.
	CodeNotDisputable = "not_disputable"
	// CodeNoLinkedPaymentProvider rejects a payment URL for a customer
	// without a payment provider.
	CodeNoLinkedPaymentProvider = "no_linked_payment_provider"
	// CodeInvalidPaymentProvider rejects a payment URL through GoCardless.
	CodeInvalidPaymentProvider = "invalid_payment_provider"
	// CodeInvalidInvoiceStatusOrPaymentStatus rejects a payment URL for a
	// draft, voided or paid invoice.
	CodeInvalidInvoiceStatusOrPaymentStatus = "invalid_invoice_status_or_payment_status"
	// CodeCustomerMustHavePaymentProvider is the error code of the
	// invoice.payment_failure webhook of a payment retry without a
	// provider.
	CodeCustomerMustHavePaymentProvider = "customer_must_have_payment_provider"
)

// ErrPaymentURLUnavailable reports that the invoice may be paid through
// its customer's provider but no payment intent generator is wired.
var ErrPaymentURLUnavailable = errors.New("invoicing: payment URL generation is not available")

// visibleInvoice loads the invoice id the API shows (lago
// organization.invoices.visible: draft, finalized, voided and failed), or
// fails with core.NotFound("invoice").
func visibleInvoice(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	inv, err := r.Invoices.Get(ctx, org, id)
	if err == nil && !(invoice.Filter{}).Lists(inv.Status) {
		err = core.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load invoice %s: %w", id, asNotFound(err, "invoice"))
	}
	return inv, nil
}

// onVisible is [Service.onInvoice] on a visible invoice.
func (s *Service) onVisible(ctx context.Context, org core.OrganizationID, id core.InvoiceID, fn func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error) (*invoice.Invoice, error) {
	inv, err := s.onInvoice(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		if !(invoice.Filter{}).Lists(inv.Status) {
			return fmt.Errorf("invoice %s is %s: %w", id, inv.Status, core.NotFound("invoice"))
		}
		return fn(ctx, r, inv)
	})
	return inv, asNotFound(err, "invoice")
}

// View returns the visible invoice id with its fees, credits, taxes,
// settlements and error details (lago Api::V1::InvoicesController#show).
func (s *Service) View(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*InvoiceView, error) {
	var out *InvoiceView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := visibleInvoice(ctx, r, org, id)
		if err != nil {
			return err
		}
		out, err = newViewer(r, org).invoice(ctx, inv, true)
		return err
	})
	return out, err
}

// ListViews returns a page of the invoices matching f, with their
// subscriptions (lago InvoicesQuery and the index payload).
func (s *Service) ListViews(ctx context.Context, org core.OrganizationID, f invoice.Filter, page core.PageRequest) (core.Page[*InvoiceView], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*InvoiceView]{}, err
	}
	var out core.Page[*InvoiceView]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Invoices.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list invoices: %w", err)
		}
		v := newViewer(r, org)
		out = core.Page[*InvoiceView]{NextToken: p.NextToken, Items: make([]*InvoiceView, 0, len(p.Items))}
		for _, inv := range p.Items {
			iv, err := v.invoice(ctx, inv, false)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, iv)
		}
		return nil
	})
	return out, err
}

// view returns the full view of the invoice id, after an operation.
func (s *Service) view(ctx context.Context, org core.OrganizationID, inv *invoice.Invoice, err error) (*InvoiceView, error) {
	if err != nil {
		return nil, err
	}
	var out *InvoiceView
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cur, err := r.Invoices.Get(ctx, org, inv.ID)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", inv.ID, err)
		}
		out, err = newViewer(r, org).invoice(ctx, cur, true)
		return err
	})
	return out, err
}

// CreateOneOffView is [Service.CreateOneOff] returning the invoice's view.
func (s *Service) CreateOneOffView(ctx context.Context, in OneOffInput) (*InvoiceView, error) {
	inv, err := s.CreateOneOff(ctx, in)
	return s.view(ctx, in.OrganizationID, inv, err)
}

// Delete marks the draft id deleted (lago Invoices::DeleteService and
// Invoice#mark_as_deleted): another status fails with [CodeNotDeletable],
// a draft synced to an integration with
// [CodeInvoiceSyncedToExternalSystem]. Its credit notes are deleted
// first, a non-draft one failing with [CodeCreditNoteNotDeletable]. The
// invoice is kept, invisible, with its fees and subscription periods, so
// the periods are not billed again; invoice.deleted is emitted with it.
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.InvoiceID) error {
	_, err := s.onVisible(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		if inv.Status != invoice.StatusDraft {
			return core.NewRuleError(CodeNotDeletable, "only draft invoices are deleted")
		}
		synced, err := r.IntegrationResources.ExistsForSyncable(ctx, org, inv.ID.UUID())
		if err != nil {
			return fmt.Errorf("find integration resources of invoice %s: %w", inv.ID, err)
		}
		if synced {
			return core.NewRuleError(CodeInvoiceSyncedToExternalSystem, "the draft invoice is synced to an integration")
		}
		cns, err := r.CreditNotes.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return fmt.Errorf("list credit notes of invoice %s: %w", inv.ID, err)
		}
		// The credit notes not deleted are soft-deleted
		// (CreditNotes::DeleteService, delete_service.rb:41-49).
		for _, cn := range cns {
			if cn.Status == creditnote.StatusDeleted {
				continue
			}
			if cn.Status != creditnote.StatusDraft {
				return core.NewRuleError(CodeCreditNoteNotDeletable, "a credit note of the invoice is not a draft")
			}
			if err := cn.MarkDeleted(s.d.Now()); err != nil {
				return fmt.Errorf("delete credit note %s: %w", cn.ID, err)
			}
			if err := r.CreditNotes.Update(ctx, cn); err != nil {
				return fmt.Errorf("delete credit note %s: %w", cn.ID, err)
			}
		}
		inv.Status = invoice.StatusDeleted
		inv.UpdatedAt = s.d.Now()
		if err := r.Invoices.Update(ctx, inv); err != nil {
			return fmt.Errorf("delete invoice %s: %w", inv.ID, err)
		}
		// delete_service.rb:13.
		if err := s.d.LogInvoice(ctx, r, auditlog.ActivityInvoiceDeleted, inv); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoiceDeleted, "invoice", kernel.ObjectID(inv.ID), inv)
	})
	return err
}

// Refresh refreshes the visible invoice id ([Service.RefreshDraft], lago
// Api::V1::InvoicesController#refresh) and returns its view.
func (s *Service) Refresh(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*InvoiceView, error) {
	inv, err := s.onVisible(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		if inv.Type != invoice.TypeSubscription {
			return core.NewRuleError(CodeNotRefreshable, "only subscription invoices are refreshed")
		}
		if inv.Status != invoice.StatusDraft {
			return nil
		}
		return s.regenerate(ctx, r, inv, modeRefresh)
	})
	return s.view(ctx, org, inv, err)
}

// Finalize refreshes and finalizes the draft id ([Service.FinalizeDraft]);
// an invoice that is not a draft is not found (lago
// Api::V1::InvoicesController#finalize, invoices.draft.find_by).
func (s *Service) Finalize(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*InvoiceView, error) {
	inv, err := s.onInvoice(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		if inv.Status != invoice.StatusDraft {
			return fmt.Errorf("invoice %s is %s: %w", id, inv.Status, core.NotFound("invoice"))
		}
		return s.finalizeDraft(ctx, r, inv)
	})
	return s.view(ctx, org, inv, asNotFound(err, "invoice"))
}

// FinalizeAll enqueues the finalization of every draft of the
// organization and returns how many (lago Invoices::FinalizeBatchService
// call_async with Invoices::FinalizeAllJob).
func (s *Service) FinalizeAll(ctx context.Context, org core.OrganizationID) (int, error) {
	n := 0
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		now := s.d.Now()
		f := invoice.Filter{Statuses: []invoice.Status{invoice.StatusDraft}}
		page := core.PageRequest{Size: core.MaxPageSize}
		for {
			p, err := r.Invoices.List(ctx, org, f, page)
			if err != nil {
				return fmt.Errorf("list draft invoices: %w", err)
			}
			for _, inv := range p.Items {
				args := kernel.InvoiceArgs{InvoiceID: inv.ID}
				if err := kernel.Enqueue(ctx, r, org, kernel.JobFinalizeDraft, args, "invoice:"+inv.ID.String(), now); err != nil {
					return err
				}
				n++
			}
			if p.NextToken == "" {
				return nil
			}
			page.Token = p.NextToken
		}
	})
	return n, err
}

// VoidView voids the visible invoice id ([Service.Void]) and returns its
// view.
func (s *Service) VoidView(ctx context.Context, org core.OrganizationID, id core.InvoiceID, in VoidInput) (*InvoiceView, error) {
	inv, err := s.onVisible(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		return s.void(ctx, r, inv, in)
	})
	return s.view(ctx, org, inv, err)
}

// RegenerateView regenerates the visible voided invoice id
// ([Service.Regenerate]) and returns the new invoice's view.
func (s *Service) RegenerateView(ctx context.Context, org core.OrganizationID, id core.InvoiceID, in RegenerateInput) (*InvoiceView, error) {
	var out *InvoiceView
	err := s.inRegeneration(ctx, org, id, in, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		var err error
		out, err = newViewer(r, org).invoice(ctx, inv, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RetryView retries the failed invoice id ([Service.Retry], lago
// Invoices::RetryService): another status fails with [CodeInvalidStatus].
func (s *Service) RetryView(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*InvoiceView, error) {
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := visibleInvoice(ctx, r, org, id)
		if err != nil {
			return err
		}
		if inv.Status != invoice.StatusFailed {
			return core.NewRuleError(CodeInvalidStatus, "only failed invoices are retried")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	inv, err := s.Retry(ctx, org, id)
	return s.view(ctx, org, inv, err)
}

// RetryPayment retries the payment of the visible invoice id (lago
// Invoices::Payments::RetryService): a draft, voided or paid invoice
// fails with [CodeInvalidStatus], one whose provider handles a payment
// with [CodePaymentProcessing]. With a payment provider linked to the
// customer the payment is enqueued, else invoice.payment_failure is
// emitted with [CodeCustomerMustHavePaymentProvider]. pm, when set,
// overrides the payment method the payment is collected with.
func (s *Service) RetryPayment(ctx context.Context, org core.OrganizationID, id core.InvoiceID, pm PaymentMethodParams) error {
	_, err := s.onVisible(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		if inv.Status == invoice.StatusDraft || inv.Status == invoice.StatusVoided || inv.PaymentStatus == core.PaymentStatusSucceeded {
			return core.NewRuleError(CodeInvalidStatus, "the invoice is not payable")
		}
		if !inv.ReadyForPaymentProcessing {
			return core.NewRuleError(CodePaymentProcessing, "the payment provider is handling a payment")
		}
		pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, inv.CustomerID)
		if err != nil {
			return fmt.Errorf("list payment connections of customer %s: %w", inv.CustomerID, err)
		}
		if len(pcs) == 0 {
			return kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoicePaymentFailure, "invoice", kernel.ObjectID(inv.ID),
				PaymentFailure{Invoice: inv, ErrorDetails: map[string]string{"code": CodeCustomerMustHavePaymentProvider}})
		}
		args := kernel.InvoiceArgs{InvoiceID: inv.ID, PaymentMethodType: pm.Type, PaymentMethodID: pm.ID}
		return kernel.Enqueue(ctx, r, org, kernel.JobCreateInvoicePayment, args, "invoice:"+inv.ID.String(), s.d.Now())
	})
	return err
}

// PaymentMethodParams override the payment method of a retried payment
// (lago retry_payment payment_method params, resolved by
// PaymentMethods::DetermineService): MANUAL collects nothing, an ID
// selects that payment method of the customer, PROVIDER alone the
// customer's default. The zero value derives the payment method from the
// invoice.
type PaymentMethodParams struct {
	// Type is the payment method kind.
	Type core.PaymentMethodType
	// ID is the payment method.
	ID core.PaymentMethodID
}

// PaymentFailure is the payload of invoice.payment_failure: the invoice
// and the provider error details (lago SendWebhookJob error_details).
type PaymentFailure struct {
	// Invoice is the invoice.
	Invoice *invoice.Invoice `json:"invoice"`
	// ErrorDetails are the error details.
	ErrorDetails map[string]string `json:"error_details"`
}

// LoseDispute records a lost payment dispute on the visible invoice id
// (lago Invoices::LoseDisputeService and Invoice#mark_as_dispute_lost!):
// an earlier loss keeps its instant, the overdue flag is cleared,
// invoice.payment_dispute_lost is emitted and the provider taxes are
// voided. An invoice neither finalized nor voided fails with
// [CodeNotDisputable].
func (s *Service) LoseDispute(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*InvoiceView, error) {
	inv, err := s.onVisible(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		now := s.d.Now()
		if inv.Status != invoice.StatusFinalized && inv.Status != invoice.StatusVoided {
			return core.NewRuleError(CodeNotDisputable, "only finalized and voided invoices lose a dispute")
		}
		at := inv.PaymentDisputeLostAt
		if err := inv.MarkDisputeLost(now); err != nil {
			return err
		}
		if at != nil {
			inv.PaymentDisputeLostAt = at
		}
		inv.PaymentOverdue = false
		if err := r.Invoices.Update(ctx, inv); err != nil {
			return fmt.Errorf("update invoice %s: %w", inv.ID, err)
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoicePaymentDisputeLost, "invoice", kernel.ObjectID(inv.ID), inv); err != nil {
			return err
		}
		return kernel.Enqueue(ctx, r, org, kernel.JobVoidProviderTaxes, kernel.InvoiceArgs{InvoiceID: inv.ID}, "", now)
	})
	return s.view(ctx, org, inv, err)
}

// Download returns the finalized invoice id and whether its PDF is
// rendered; when it is not, its rendering is enqueued (lago
// Api::V1::InvoicesController#download_pdf). Another status is not
// found.
func (s *Service) Download(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, bool, error) {
	var out *invoice.Invoice
	ready := false
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := r.Invoices.Get(ctx, org, id)
		if err == nil && inv.Status != invoice.StatusFinalized {
			err = core.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", id, asNotFound(err, "invoice"))
		}
		out = inv
		if !inv.File.IsZero() {
			ready = true
			return nil
		}
		return kernel.Enqueue(ctx, r, org, kernel.JobGenerateInvoiceDocument, kernel.InvoiceArgs{InvoiceID: inv.ID}, "invoice:"+inv.ID.String(), s.d.Now())
	})
	if err != nil {
		return nil, false, err
	}
	return out, ready, nil
}

// DownloadForCustomer is [Service.Download] from the customer portal of
// the customer cust: the invoice must be one of its visible invoices (any
// other is not found), and not a draft ([CodeIsDraft]).
//
// source: app/graphql/mutations/customer_portal/download_invoice.rb:16-20
// source: app/services/invoices/generate_pdf_service.rb:14-26
func (s *Service) DownloadForCustomer(ctx context.Context, org core.OrganizationID, cust core.CustomerID, id core.InvoiceID) (*invoice.Invoice, bool, error) {
	var out *invoice.Invoice
	ready := false
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := visibleInvoice(ctx, r, org, id)
		if err != nil {
			return err
		}
		if inv.CustomerID != cust {
			return fmt.Errorf("invoice %s of another customer: %w", id, core.NotFound("invoice"))
		}
		if inv.Status == invoice.StatusDraft {
			return core.NewRuleError(CodeIsDraft, "a draft invoice has no document")
		}
		out = inv
		if !inv.File.IsZero() {
			ready = true
			return nil
		}
		return kernel.Enqueue(ctx, r, org, kernel.JobGenerateInvoiceDocument, kernel.InvoiceArgs{InvoiceID: inv.ID}, "invoice:"+inv.ID.String(), s.d.Now())
	})
	if err != nil {
		return nil, false, err
	}
	return out, ready, nil
}

// PaymentURL checks that the visible invoice id may be paid through a
// checkout of its customer's provider (lago
// Invoices::Payments::GeneratePaymentUrlService): without a provider it
// fails with [CodeNoLinkedPaymentProvider], through GoCardless with
// [CodeInvalidPaymentProvider], for a draft, voided or paid invoice with
// [CodeInvalidInvoiceStatusOrPaymentStatus] (all on base). The payment
// intent itself needs a provider adapter: it fails with
// [ErrPaymentURLUnavailable].
func (s *Service) PaymentURL(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (string, error) {
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := visibleInvoice(ctx, r, org, id)
		if err != nil {
			return err
		}
		pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, inv.CustomerID)
		if err != nil {
			return fmt.Errorf("list payment connections of customer %s: %w", inv.CustomerID, err)
		}
		if len(pcs) == 0 {
			return fieldError("base", CodeNoLinkedPaymentProvider)
		}
		integ, err := r.Integrations.Get(ctx, org, pcs[0].IntegrationID)
		if err != nil {
			return fmt.Errorf("load integration %s: %w", pcs[0].IntegrationID, err)
		}
		if integ.Provider == integration.ProviderGocardless {
			return fieldError("base", CodeInvalidPaymentProvider)
		}
		if inv.PaymentStatus == core.PaymentStatusSucceeded || inv.Status == invoice.StatusVoided || inv.Status == invoice.StatusDraft {
			return fieldError("base", CodeInvalidInvoiceStatusOrPaymentStatus)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "", ErrPaymentURLUnavailable
}
