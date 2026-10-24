package payments

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// payableStatus maps a payment state to the payment status of what it
// pays: invoices and payment requests have no processing status (lago
// "A proper `processing` payment status should be introduced",
// app/services/invoices/payments/create_service.rb:205).
func payableStatus(st payment.State) core.PaymentStatus {
	switch st {
	case payment.StateSucceeded:
		return core.PaymentStatusSucceeded
	case payment.StateFailed:
		return core.PaymentStatusFailed
	default:
		return core.PaymentStatusPending
	}
}

// statusUpdate is a payment status change of an invoice or payment
// request by a payment flow (lago Invoices::UpdateService and
// PaymentRequests::UpdateService params).
type statusUpdate struct {
	// status is the new payment status.
	status core.PaymentStatus
	// ready is the new ready_for_payment_processing.
	ready bool
	// paid, when set, is the new total paid amount.
	paid *int64
	// notify emits the payment_status_updated webhook of a change
	// (webhook_notification).
	notify bool
}

// setInvoicePayment applies a payment flow's status change to inv (lago
// Invoices::UpdateService, app/services/invoices/update_service.rb:15-129):
// a draft's payment status cannot change; a voided invoice keeps its ready
// flag; a succeeded payment clears the overdue flag and resets the
// customer's dunning attempts in the currency when a dunning campaign
// requested the invoice. Then the gated subscription's payment rule is
// resolved, the fees take the status, a newly settled invoice expires its
// checkout link, and a changed status of a visible invoice is logged and,
// with notify, announced by invoice.payment_status_updated. It reports
// whether a credit invoice's wallet purchase must be settled after the
// commit (Invoices::PrepaidCreditJob).
func (s *Service) setInvoicePayment(ctx context.Context, r port.Repos, inv *invoice.Invoice, u statusUpdate) (bool, error) {
	org := inv.OrganizationID
	old := inv.PaymentStatus
	if inv.Status == invoice.StatusDraft && old != u.status {
		return false, core.NewRuleError(CodePaymentStatusUpdateOnDraftInvoice, "the payment status of a draft invoice cannot change")
	}
	now := s.d.Now()
	inv.PaymentStatus = u.status
	if inv.Status != invoice.StatusVoided {
		inv.ReadyForPaymentProcessing = u.ready
	}
	if u.paid != nil {
		inv.TotalPaidAmount = *u.paid
	}
	inv.UpdatedAt = now
	if inv.PaymentOverdue && inv.PaymentStatus == core.PaymentStatusSucceeded {
		inv.PaymentOverdue = false
		if err := s.resetDunning(ctx, r, inv, now); err != nil {
			return false, err
		}
	}
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return false, wrap(err, "update invoice %s", inv.ID)
	}
	if err := resolvePaymentGate(ctx, r, inv); err != nil {
		return false, err
	}
	if err := s.setFeesPaymentStatus(ctx, r, inv, now); err != nil {
		return false, err
	}
	if inv.PaymentStatus == core.PaymentStatusSucceeded && old != core.PaymentStatusSucceeded {
		if err := s.expireCheckout(ctx, r, inv, now); err != nil {
			return false, err
		}
	}
	if old != inv.PaymentStatus && visible(inv) {
		if u.notify {
			if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoicePaymentStatusUpdated, "invoice", inv.ID.UUID(), inv); err != nil {
				return false, err
			}
		}
		if err := s.d.LogInvoice(ctx, r, auditlog.ActivityInvoicePaymentStatusUpdated, inv); err != nil {
			return false, err
		}
	}
	settle := inv.Type == invoice.TypeCredit &&
		(inv.PaymentStatus == core.PaymentStatusSucceeded || inv.PaymentStatus == core.PaymentStatusFailed)
	return settle, nil
}

// setFeesPaymentStatus gives the invoice's fees its payment status (lago
// Invoices::UpdateFeesPaymentStatusJob).
func (s *Service) setFeesPaymentStatus(ctx context.Context, r port.Repos, inv *invoice.Invoice, now time.Time) error {
	fees, err := r.Fees.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return wrap(err, "list fees of invoice %s", inv.ID)
	}
	status := invoice.FeePaymentStatus(inv.PaymentStatus)
	for _, f := range fees {
		if f.PaymentStatus == status {
			continue
		}
		f.PaymentStatus = status
		f.UpdatedAt = now
		if err := r.Fees.Update(ctx, f); err != nil {
			return wrap(err, "update fee %s", f.ID)
		}
	}
	return nil
}

// setRequestPayment applies a payment flow's status change to q (lago
// PaymentRequests::UpdateService): a changed status is announced by
// payment_request.payment_status_updated with notify.
func (s *Service) setRequestPayment(ctx context.Context, r port.Repos, q *payment.Request, u statusUpdate) error {
	old := q.PaymentStatus
	q.PaymentStatus = u.status
	q.ReadyForPaymentProcessing = u.ready
	q.UpdatedAt = s.d.Now()
	if err := r.PaymentRequests.Update(ctx, q); err != nil {
		return wrap(err, "update payment request %s", q.ID)
	}
	if old != q.PaymentStatus && u.notify {
		return kernel.Emit(ctx, s.d, q.OrganizationID, webhook.EventTypePaymentRequestPaymentStatusUpdated, "payment_request", q.ID.UUID(), q)
	}
	return nil
}

// sumSucceeded returns the amount of the succeeded payments of payable.
func sumSucceeded(ctx context.Context, r port.Repos, org core.OrganizationID, payable payment.Payable) (int64, error) {
	ps, err := r.Payments.ListByPayable(ctx, org, payable)
	if err != nil {
		return 0, wrap(err, "list payments of %s", payable)
	}
	var paid int64
	for _, p := range ps {
		if p.Status == payment.StateSucceeded {
			paid += p.Amount
		}
	}
	return paid, nil
}

// dueAmount is lago Invoice#total_due_amount_cents.
func dueAmount(inv *invoice.Invoice) int64 {
	if inv.Status == invoice.StatusVoided {
		return 0
	}
	return payment.InvoiceFacts{
		TotalAmount: inv.TotalAmount, TotalPaidAmount: inv.TotalPaidAmount, FinalizedOffsetAmount: inv.OffsettedAmount,
	}.DueAmount()
}

// ProviderErrorDetails is the provider's error in the failure webhooks.
type ProviderErrorDetails struct {
	// Message is the provider's message.
	Message string `json:"message"`
	// ErrorCode is the provider's code.
	ErrorCode string `json:"error_code"`
}

// providerErrorOf returns the details of a provider error.
func providerErrorOf(err error) ProviderErrorDetails {
	if pe, ok := payment.AsProviderError(err); ok {
		return ProviderErrorDetails{Message: pe.Message, ErrorCode: pe.Code}
	}
	return ProviderErrorDetails{Message: err.Error()}
}

// errorDetailsOf returns the serialized provider error of the invoice
// failure webhook (lago V1::Errors::ErrorSerializerFactory).
func errorDetailsOf(err error) map[string]string {
	pe, ok := payment.AsProviderError(err)
	if !ok {
		return map[string]string{}
	}
	out := map[string]string{"kind": string(pe.Kind)}
	for k, v := range map[string]string{
		"code": pe.Code, "decline_code": pe.DeclineCode, "message": pe.Message, "request_id": pe.RequestID,
	} {
		if v != "" {
			out[k] = v
		}
	}
	if pe.HTTPStatus != 0 {
		out["http_status"] = strconv.Itoa(pe.HTTPStatus)
	}
	return out
}

// PaymentFailure is the payload of invoice.payment_failure for a
// provider's failure (lago V1::PaymentProviders::InvoicePaymentErrorSerializer).
type PaymentFailure struct {
	// InvoiceID is the invoice whose payment failed.
	InvoiceID core.InvoiceID `json:"invoice_id"`
	// CustomerID is the invoice's customer.
	CustomerID core.CustomerID `json:"customer_id"`
	// ExternalCustomerID is the customer's external ID.
	ExternalCustomerID string `json:"external_customer_id"`
	// ProviderCustomerID is the customer at the provider.
	ProviderCustomerID string `json:"provider_customer_id"`
	// PaymentProvider is the provider's vendor.
	PaymentProvider integration.Provider `json:"payment_provider"`
	// PaymentProviderCode is the connection's code.
	PaymentProviderCode string `json:"payment_provider_code"`
	// ProviderError is the provider's error.
	ProviderError ProviderErrorDetails `json:"provider_error"`
	// ErrorDetails details the provider's error.
	ErrorDetails map[string]string `json:"error_details"`
}

// WalletTransactionPaymentFailure is the payload of
// wallet_transaction.payment_failure (lago
// V1::PaymentProviders::WalletTransactionPaymentErrorSerializer).
type WalletTransactionPaymentFailure struct {
	// WalletTransactionID is the purchase whose payment failed.
	WalletTransactionID core.WalletTransactionID `json:"wallet_transaction_id"`
	// CustomerID is the wallet's customer.
	CustomerID core.CustomerID `json:"customer_id"`
	// ExternalCustomerID is the customer's external ID.
	ExternalCustomerID string `json:"external_customer_id"`
	// ProviderCustomerID is the customer at the provider.
	ProviderCustomerID string `json:"provider_customer_id"`
	// PaymentProvider is the provider's vendor.
	PaymentProvider integration.Provider `json:"payment_provider"`
	// PaymentProviderCode is the connection's code.
	PaymentProviderCode string `json:"payment_provider_code"`
	// ProviderError is the provider's error.
	ProviderError ProviderErrorDetails `json:"provider_error"`
}

// RequestPaymentFailure is the payload of payment_request.payment_failure
// (lago V1::PaymentProviders::PaymentRequestPaymentErrorSerializer).
type RequestPaymentFailure struct {
	// PaymentRequestID is the request whose payment failed.
	PaymentRequestID core.PaymentRequestID `json:"payment_request_id"`
	// InvoiceIDs are the requested invoices.
	InvoiceIDs []core.InvoiceID `json:"invoice_ids"`
	// CustomerID is the request's customer.
	CustomerID core.CustomerID `json:"customer_id"`
	// ExternalCustomerID is the customer's external ID.
	ExternalCustomerID string `json:"external_customer_id"`
	// ProviderCustomerID is the customer at the provider.
	ProviderCustomerID string `json:"provider_customer_id"`
	// PaymentProvider is the provider's vendor.
	PaymentProvider integration.Provider `json:"payment_provider"`
	// PaymentProviderCode is the connection's code.
	PaymentProviderCode string `json:"payment_provider_code"`
	// ProviderError is the provider's error.
	ProviderError ProviderErrorDetails `json:"provider_error"`
}

// failureSource is the customer and connection a failure webhook names.
type failureSource struct {
	cust  *customer.Customer
	pc    *payment.ProviderCustomer
	integ *integration.Integration
}

// failureSourceOf loads the customer of a failure webhook.
func failureSourceOf(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID, pc *payment.ProviderCustomer, integ *integration.Integration) (failureSource, error) {
	c, err := r.Customers.Get(ctx, org, id)
	if err != nil {
		return failureSource{}, wrap(err, "load customer %s", id)
	}
	return failureSource{cust: c, pc: pc, integ: integ}, nil
}

func (f failureSource) provider() (integration.Provider, string) {
	if f.integ == nil {
		return "", ""
	}
	return f.integ.Provider, f.integ.Code
}

// emitInvoiceFailure announces a provider's failure to collect inv (lago
// Invoices::Payments::DeliverErrorWebhookService): a credit invoice open
// or visible emits wallet_transaction.payment_failure on its purchase,
// and a visible invoice invoice.payment_failure, each with its activity
// log.
func (s *Service) emitInvoiceFailure(ctx context.Context, r port.Repos, inv *invoice.Invoice, src failureSource, cause error, details map[string]string) error {
	org := inv.OrganizationID
	vendor, code := src.provider()
	perr := providerErrorOf(cause)
	if inv.Type == invoice.TypeCredit && (inv.Status == invoice.StatusOpen || visible(inv)) {
		t, err := creditFeeTransaction(ctx, r, inv)
		if err != nil {
			return err
		}
		if t != nil {
			payload := WalletTransactionPaymentFailure{
				WalletTransactionID: t.ID, CustomerID: src.cust.ID, ExternalCustomerID: src.cust.ExternalID,
				ProviderCustomerID: providerCustomerID(src.pc), PaymentProvider: vendor, PaymentProviderCode: code,
				ProviderError: perr,
			}
			if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeWalletTransactionPaymentFailure, "wallet_transaction", t.ID.UUID(), payload); err != nil {
				return err
			}
			if err := s.d.LogChange(ctx, r, kernel.Change{
				Type: auditlog.ActivityWalletTransactionPaymentFailed, OrganizationID: org, ResourceType: auditlog.ResourceWallet,
				ResourceID: t.WalletID.UUID(), Object: t, ExternalCustomerID: src.cust.ExternalID,
			}); err != nil {
				return err
			}
		}
	}
	if !visible(inv) {
		return nil
	}
	if err := s.d.LogInvoice(ctx, r, auditlog.ActivityInvoicePaymentFailure, inv); err != nil {
		return err
	}
	if details == nil {
		details = map[string]string{}
	}
	payload := PaymentFailure{
		InvoiceID: inv.ID, CustomerID: src.cust.ID, ExternalCustomerID: src.cust.ExternalID,
		ProviderCustomerID: providerCustomerID(src.pc), PaymentProvider: vendor, PaymentProviderCode: code,
		ProviderError: perr, ErrorDetails: details,
	}
	return kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoicePaymentFailure, "invoice", inv.ID.UUID(), payload)
}

// creditFeeTransaction returns the purchase the credit fee of inv bills
// (lago invoice.fees.credit.first.invoiceable), or nil.
func creditFeeTransaction(ctx context.Context, r port.Repos, inv *invoice.Invoice) (*wallet.Transaction, error) {
	fees, err := r.Fees.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return nil, wrap(err, "list fees of invoice %s", inv.ID)
	}
	for _, f := range fees {
		if f.Type != core.FeeTypeCredit || f.WalletTransactionID.IsZero() {
			continue
		}
		t, err := r.WalletTransactions.Get(ctx, inv.OrganizationID, f.WalletTransactionID)
		if err != nil {
			return nil, wrap(err, "load wallet transaction %s", f.WalletTransactionID)
		}
		return t, nil
	}
	return nil, nil
}

// emitRequestFailure announces a provider's failure to collect q (lago
// PaymentRequests::Payments::DeliverErrorWebhookService).
func (s *Service) emitRequestFailure(ctx context.Context, q *payment.Request, src failureSource, cause error) error {
	vendor, code := src.provider()
	payload := RequestPaymentFailure{
		PaymentRequestID: q.ID, InvoiceIDs: q.InvoiceIDs, CustomerID: src.cust.ID, ExternalCustomerID: src.cust.ExternalID,
		ProviderCustomerID: providerCustomerID(src.pc), PaymentProvider: vendor, PaymentProviderCode: code,
		ProviderError: providerErrorOf(cause),
	}
	return kernel.Emit(ctx, s.d, q.OrganizationID, webhook.EventTypePaymentRequestPaymentFailure, "payment_request", q.ID.UUID(), payload)
}

// emitPayment announces a payment (payment.succeeded,
// payment.requires_action; lago V1::PaymentSerializer) with its details.
func (s *Service) emitPayment(ctx context.Context, r port.Repos, t webhook.EventType, p *payment.Payment) error {
	d, err := detail(ctx, r, p)
	if err != nil {
		return err
	}
	return kernel.Emit(ctx, s.d, p.OrganizationID, t, "payment", p.ID.UUID(), d)
}

// ProviderFailure is the payload of payment_provider.error (lago
// V1::PaymentProviders::ErrorSerializer).
type ProviderFailure struct {
	// IntegrationID is the failing connection.
	IntegrationID core.IntegrationID `json:"integration_id"`
	// PaymentProviderCode is the connection's code.
	PaymentProviderCode string `json:"payment_provider_code"`
	// PaymentProviderName is the connection's name.
	PaymentProviderName string `json:"payment_provider_name"`
	// Source is the vendor.
	Source integration.Provider `json:"source"`
	// Action is the failing operation (payment_provider.register_webhook).
	Action string `json:"action"`
	// ProviderError is the provider's error.
	ProviderError ProviderErrorDetails `json:"provider_error"`
}

// emitProviderFailure emits payment_provider.error for a failing operation
// on a connection.
func (s *Service) emitProviderFailure(ctx context.Context, integ *integration.Integration, action string, cause error) error {
	payload := ProviderFailure{
		IntegrationID: integ.ID, PaymentProviderCode: integ.Code, PaymentProviderName: integ.Name,
		Source: integ.Provider, Action: action, ProviderError: providerErrorOf(cause),
	}
	return kernel.Emit(ctx, s.d, integ.OrganizationID, webhook.EventTypePaymentProviderError, "integration", integ.ID.UUID(), payload)
}

// isNotFound reports a repository's not-found error.
func isNotFound(err error) bool { return errors.Is(err, core.ErrNotFound) }
