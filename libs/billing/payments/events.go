package payments

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ErrInvalidWebhook reports a provider webhook delivery whose signature
// does not verify or whose payload cannot be decoded; the sender is
// answered 400 and nothing is stored.
var ErrInvalidWebhook = errors.New("payments: invalid provider webhook")

// RefundStatuses records the refund statuses providers report;
// libs/billing/creditnotes implements it.
type RefundStatuses interface {
	// UpdateRefundStatus records the status of the refund a connection
	// identifies by providerRefundID.
	UpdateRefundStatus(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerRefundID string, status core.RefundStatus) (*payment.Refund, error)
}

// WithRefundStatuses sets the recorder of refund statuses; without one,
// refund events are ignored.
func (s *Service) WithRefundStatuses(u RefundStatuses) *Service {
	s.refunds = u
	return s
}

// webhookConnection opens the live payment connection id of org whose
// vendor is provider, or fails with a not-found error.
func (s *Service) webhookConnection(ctx context.Context, provider integration.Provider, org core.OrganizationID, id core.IntegrationID) (providerConn, error) {
	integ, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*integration.Integration, error) {
		return liveIntegration(ctx, r, org, id)
	})
	if err != nil {
		return providerConn{}, err
	}
	if integ == nil || integ.Provider != provider {
		return providerConn{}, notFound("payment_provider")
	}
	return s.open(ctx, integ)
}

// VerifyWebhook authenticates a webhook delivery received by the payment
// connection id of org, whose vendor must be provider, and returns the
// type of its first event. A delivery that verifies but carries no event
// Tanvo handles returns "" (it is acknowledged and not stored); one whose
// signature or payload is invalid fails with [ErrInvalidWebhook]; an
// unknown connection is not found (lago WebhooksController and
// InboundWebhooks::CreateService).
func (s *Service) VerifyWebhook(ctx context.Context, provider integration.Provider, org core.OrganizationID, id core.IntegrationID, payload []byte, header http.Header) (string, error) {
	pc, err := s.webhookConnection(ctx, provider, org, id)
	if err != nil {
		return "", err
	}
	if err := pc.gw.VerifyWebhook(pc.conn, payload, header); err != nil {
		if errors.Is(err, payment.ErrInvalidSignature) || errors.Is(err, payment.ErrInvalidPayload) {
			return "", fmt.Errorf("%w: %w", ErrInvalidWebhook, err)
		}
		return "", fmt.Errorf("verify %s webhook: %w", provider, err)
	}
	events, err := s.parseWebhook(ctx, pc, payload)
	switch {
	case ignoredEvent(err):
		return "", nil
	case errors.Is(err, payment.ErrInvalidPayload):
		return "", fmt.Errorf("%w: %w", ErrInvalidWebhook, err)
	case err != nil:
		return "", fmt.Errorf("parse %s webhook: %w", provider, err)
	case len(events) == 0:
		return "", nil
	}
	return events[0].Meta().Type, nil
}

// WebhookAcknowledgement returns the body a provider expects in answer to
// its webhooks, or false for an empty 200 (Adyen's "[accepted]").
func (s *Service) WebhookAcknowledgement(provider integration.Provider) (contentType string, body []byte, ok bool) {
	if s.providers == nil || s.providers.Gateways == nil {
		return "", nil, false
	}
	gw, err := s.providers.Gateways.PaymentProvider(provider)
	if err != nil {
		return "", nil, false
	}
	a, ok := gw.(payment.WebhookAcknowledger)
	if !ok {
		return "", nil, false
	}
	contentType, body = a.WebhookAcknowledgement()
	return contentType, body, true
}

// webhookParseTimeout bounds the decoding of a delivery, which may call
// the provider (Flutterwave verifies each charge with its API).
const webhookParseTimeout = 20 * time.Second

// parseWebhook decodes a delivery within [webhookParseTimeout]; a
// retryable failure of the provider's call is returned as an error, so
// the delivery is answered 500 and delivered again, or its processing
// retried.
func (s *Service) parseWebhook(ctx context.Context, pc providerConn, payload []byte) ([]payment.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, webhookParseTimeout)
	defer cancel()
	return pc.gw.ParseWebhook(ctx, pc.conn, payload)
}

// ignoredEvent reports a parse error the handler acknowledges without
// updating anything: a payable type or a refund status Tanvo does not know.
func ignoredEvent(err error) bool {
	return errors.Is(err, payment.ErrUnknownPayable) || errors.Is(err, payment.ErrUnknownStatus)
}

// HandleWebhook applies a stored provider webhook (lago
// PaymentProviders::*::HandleEventService): its events are decoded by the
// connection's adapter and handled in order. A resource the event names
// but Tanvo does not have fails the webhook, for a retry, only in live
// mode; a test-mode event is acknowledged (handle_event_service.rb
// NotFoundFailure). A webhook of a connection since removed is ignored.
func (s *Service) HandleWebhook(ctx context.Context, w *payment.InboundWebhook) error {
	pc, err := s.webhookConnection(ctx, integration.Provider(w.Source), w.OrganizationID, w.IntegrationID)
	if isNotFound(err) {
		s.d.Logger.WarnContext(ctx, "payments: webhook of a removed connection",
			"organization_id", w.OrganizationID.String(), "integration_id", w.IntegrationID.String(), "webhook_id", w.ID.String())
		return nil
	}
	if err != nil {
		return err
	}
	events, err := s.parseWebhook(ctx, pc, w.Payload)
	if ignoredEvent(err) {
		s.d.Logger.WarnContext(ctx, "payments: unhandled webhook", "organization_id", w.OrganizationID.String(),
			"webhook_id", w.ID.String(), "error", err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse webhook %s: %w", w.ID, err)
	}
	for _, ev := range events {
		err := s.handleEvent(ctx, pc, ev)
		if isNotFound(err) && !ev.Meta().Livemode {
			s.d.Logger.WarnContext(ctx, "payments: resource of a test webhook not found", "organization_id",
				w.OrganizationID.String(), "webhook_id", w.ID.String(), "event_type", ev.Meta().Type, "error", err)
			continue
		}
		if err != nil {
			return fmt.Errorf("handle %s event of webhook %s: %w", ev.Meta().Type, w.ID, err)
		}
	}
	return nil
}

// handleEvent applies one provider event.
func (s *Service) handleEvent(ctx context.Context, pc providerConn, ev payment.Event) error {
	switch e := ev.(type) {
	case payment.PaymentEvent:
		return s.handlePaymentEvent(ctx, pc, e)
	case payment.RefundEvent:
		if s.refunds == nil {
			return nil
		}
		// charge.refund.updated (CreditNotes::Refunds::StripeService#update_status):
		// a failed refund is recorded, its error is the note's.
		_, err := s.refunds.UpdateRefundStatus(ctx, pc.integ.OrganizationID, pc.integ.ID, e.ProviderRefundID, e.Status)
		var rule *core.RuleError
		if errors.As(err, &rule) {
			return nil
		}
		return err
	case payment.PaymentMethodAttached:
		return s.handleMethodAttached(ctx, pc, e)
	case payment.PaymentMethodSetupFailed:
		return s.handleSetupFailed(ctx, pc, e)
	case payment.PaymentMethodDetached:
		return s.handleMethodDetached(ctx, pc, e)
	case payment.CustomerUpdated:
		return s.handleCustomerUpdated(ctx, pc, e)
	case payment.DisputeClosed:
		if !e.Lost {
			return nil
		}
		return s.loseDispute(ctx, pc, e)
	case payment.CustomerBalanceFunded:
		return s.handleBalanceFunded(ctx, pc, e)
	}
	return nil
}

// handlePaymentEvent applies a payment's new status (lago
// Invoices::Payments::*Service#update_payment_status and
// PaymentRequests::Payments::*Service#update_payment_status). The payment
// is found by its provider ID; a hosted checkout payment unknown until now
// is created, and a payment Tanvo started but lost is recovered from the
// event's metadata, unless its payable's payment failed. A payment of a
// payable already paid changes nothing. A succeeded payment's method is
// then recorded and its receipt issued ([kernel.JobSetPaymentMethod]).
func (s *Service) handlePaymentEvent(ctx context.Context, pc providerConn, ev payment.PaymentEvent) error {
	org := pc.integ.OrganizationID
	var settle []*invoice.Invoice
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		settle = nil
		p, err := r.Payments.GetByProviderPaymentID(ctx, org, pc.integ.ID, ev.ProviderPaymentID)
		switch {
		case isNotFound(err):
			p = nil
		case err != nil:
			return wrap(err, "load payment %s", ev.ProviderPaymentID)
		}
		payable := ev.Refs.Payable
		if p != nil {
			payable = p.Payable
		}
		if payable.Type == "" && !ev.Refs.InvoiceID.IsZero() {
			payable = payment.InvoicePayable(ev.Refs.InvoiceID)
		}
		switch payable.Type {
		case payment.PayableTypeInvoice:
			settle, err = s.invoicePaymentEvent(ctx, r, pc, ev, p, payable.InvoiceID)
		case payment.PayableTypePaymentRequest:
			settle, err = s.requestPaymentEvent(ctx, r, pc, ev, p, payable.PaymentRequestID)
		}
		return err
	})
	if err != nil {
		return err
	}
	for _, inv := range settle {
		s.settleCredit(ctx, inv)
	}
	return nil
}

// invoiceKey is the unique key of an invoice's payment job.
func invoiceKey(id core.InvoiceID) string { return "invoice:" + id.String() }

// eventPayment returns the payment a payment event reports on a payable
// without one of that provider ID (stripe_service.rb create_payment): the
// payable's pending payment not yet sent to the provider for amount,
// reused, or a new one; the provider's payment ID, raw status and state
// (pending reported as processing) are set. It returns nil when the
// payable already has another payment in progress (a payable has one
// pending or processing provider payment; lago RecordNotUnique).
func (s *Service) eventPayment(ctx context.Context, r port.Repos, pc providerConn, ev payment.PaymentEvent, payable payment.Payable, cust core.CustomerID, amount int64, cur money.Currency, now time.Time) (*payment.Payment, error) {
	org := pc.integ.OrganizationID
	ps, err := r.Payments.ListByPayable(ctx, org, payable)
	if err != nil {
		return nil, wrap(err, "list payments of %s", payable)
	}
	var p *payment.Payment
	for _, o := range ps {
		if o.Kind != payment.KindProvider || o.Final() {
			continue
		}
		if o.Status != payment.StatePending || o.ProviderPaymentID != nil || o.Amount != amount || o.IntegrationID != pc.integ.ID {
			return nil, nil
		}
		p = o
	}
	created := p == nil
	if created {
		p = &payment.Payment{
			ID:             core.NewID[core.PaymentID](s.d.IDs),
			OrganizationID: org,
			Payable:        payable,
			CustomerID:     cust,
			Kind:           payment.KindProvider,
			Status:         payment.StatePending,
			Amount:         amount,
			Currency:       cur,
			IntegrationID:  pc.integ.ID,
			CreatedAt:      now,
		}
	}
	p.ProviderPaymentID = &ev.ProviderPaymentID
	if ev.ProviderStatus != "" {
		p.ProviderStatus = &ev.ProviderStatus
	}
	if ev.Status != "" {
		p.Status = ev.Status
	}
	if p.Status == payment.StatePending {
		p.Status = payment.StateProcessing
	}
	p.UpdatedAt = now
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if created {
		err = r.Payments.Create(ctx, p)
	} else {
		err = r.Payments.Update(ctx, p)
	}
	if err != nil {
		return nil, wrap(err, "store payment %s", ev.ProviderPaymentID)
	}
	return p, nil
}

// applyEvent sets the reported state of p (update_payment_status: status,
// payable_payment_status and error_code).
func applyEvent(ctx context.Context, r port.Repos, p *payment.Payment, ev payment.PaymentEvent, now time.Time) error {
	if ev.Status != "" {
		p.Status = ev.Status
	}
	if ev.ProviderStatus != "" {
		p.ProviderStatus = &ev.ProviderStatus
	}
	if ev.ErrorCode != "" {
		p.ErrorCode = &ev.ErrorCode
	}
	p.UpdatedAt = now
	if err := r.Payments.Update(ctx, p); err != nil {
		return wrap(err, "update payment %s", p.ID)
	}
	return nil
}

// enqueueSetMethod enqueues, for a succeeded payment event, the recording
// of the payment's method and its receipt
// (payment_intent_succeeded_service.rb:9-15).
func enqueueSetMethod(ctx context.Context, r port.Repos, p *payment.Payment, ev payment.PaymentEvent) error {
	if p == nil || ev.Status != payment.StateSucceeded {
		return nil
	}
	args := kernel.PaymentMethodArgs{PaymentID: p.ID, ProviderMethodID: ev.ProviderMethodID}
	return kernel.Enqueue(ctx, r, p.OrganizationID, kernel.JobSetPaymentMethod, args, "payment:"+p.ID.String(), time.Time{})
}

// invoicePaymentEvent applies a payment event on the invoice id
// (Invoices::Payments::StripeService#update_payment_status,
// app/services/invoices/payments/stripe_service.rb:19-61). It returns the
// invoice when its credit purchase must settle.
func (s *Service) invoicePaymentEvent(ctx context.Context, r port.Repos, pc providerConn, ev payment.PaymentEvent, p *payment.Payment, id core.InvoiceID) ([]*invoice.Invoice, error) {
	org := pc.integ.OrganizationID
	if err := kernel.LockInvoice(ctx, r, org, id); err != nil {
		return nil, err
	}
	inv, err := r.Invoices.Get(ctx, org, id)
	if isNotFound(err) {
		if p == nil && ev.OneTime {
			return nil, notFound("invoice")
		}
		return nil, nil
	}
	if err != nil {
		return nil, wrap(err, "load invoice %s", id)
	}
	now := s.d.Now()
	if p == nil {
		// A payment Tanvo started is recovered unless the invoice's
		// payment failed (handle_missing_payment, stripe_service.rb:236-252).
		if !ev.OneTime && inv.PaymentStatus == core.PaymentStatusFailed {
			return nil, nil
		}
		inv.PaymentAttempts++
		inv.UpdatedAt = now
		if err := r.Invoices.Update(ctx, inv); err != nil {
			return nil, wrap(err, "update invoice %s", inv.ID)
		}
		amount := dueAmount(inv)
		if ev.OneTime && ev.Amount != 0 {
			amount = ev.Amount
		}
		if p, err = s.eventPayment(ctx, r, pc, ev, payment.InvoicePayable(inv.ID), inv.CustomerID, amount, inv.Currency, now); err != nil || p == nil {
			return nil, err
		}
	}
	if inv.PaymentStatus == core.PaymentStatusSucceeded {
		return nil, enqueueSetMethod(ctx, r, p, ev)
	}
	if err := applyEvent(ctx, r, p, ev, now); err != nil {
		return nil, err
	}
	if p.Status == payment.StateSucceeded {
		if err := s.emitPayment(ctx, r, webhook.EventTypePaymentSucceeded, p); err != nil {
			return nil, err
		}
	}
	if err := s.syncPayment(ctx, r, p, inv); err != nil {
		return nil, err
	}
	pending, err := authenticationRetryPending(ctx, r, pc.integ, p, inv)
	if err != nil {
		return nil, err
	}
	var settle []*invoice.Invoice
	if !pending {
		st := payableStatus(p.Status)
		u := statusUpdate{status: st, ready: ev.Status != payment.StateProcessing && st != core.PaymentStatusSucceeded, notify: true}
		if st == core.PaymentStatusSucceeded {
			paid, err := sumSucceeded(ctx, r, org, p.Payable)
			if err != nil {
				return nil, err
			}
			u.paid = &paid
		}
		ok, err := s.setInvoicePayment(ctx, r, inv, u)
		if err != nil {
			return nil, err
		}
		if ok {
			settle = append(settle, inv)
		}
	}
	return settle, enqueueSetMethod(ctx, r, p, ev)
}

// authenticationRetryPending reports whether a failed payment of inv
// leaves the invoice as it is because another attempt follows: one the
// customer can authenticate on-session (a Stripe failure needing
// authentication with 3D Secure supported or a payment-gated subscription
// invoice; stripe_provider.rb:47-51), or another payment of the invoice
// waiting for the customer's action (stripe_service.rb:63-68).
func authenticationRetryPending(ctx context.Context, r port.Repos, integ *integration.Integration, p *payment.Payment, inv *invoice.Invoice) (bool, error) {
	if p.Status != payment.StateFailed {
		return false, nil
	}
	if integ.Provider == integration.ProviderStripe && p.ErrorCode != nil && *p.ErrorCode == codeAuthenticationRequired {
		if settingOn(integ, integrations.KeySupports3DS) {
			return true, nil
		}
		_, gated, err := invoiceGating(ctx, r, inv)
		if err != nil || gated {
			return gated, err
		}
	}
	ps, err := r.Payments.ListByPayable(ctx, p.OrganizationID, p.Payable)
	if err != nil {
		return false, wrap(err, "list payments of %s", p.Payable)
	}
	return slices.ContainsFunc(ps, func(o *payment.Payment) bool {
		return o.ID != p.ID && o.ProviderStatus != nil && *o.ProviderStatus == payment.ProviderStatusRequiresAction
	}), nil
}

// lockRequest takes the lock of a payment request, then those of its
// invoices in ID order.
func lockRequest(ctx context.Context, r port.Repos, org core.OrganizationID, id core.PaymentRequestID) error {
	if err := r.Locks.Lock(ctx, org, port.LockKey{Scope: "payment_request", ID: id.UUID()}); err != nil {
		return wrap(err, "lock payment request %s", id)
	}
	return nil
}

// lockInvoices takes the locks of invoices in ID order.
func lockInvoices(ctx context.Context, r port.Repos, org core.OrganizationID, ids []core.InvoiceID) error {
	ids = slices.Clone(ids)
	slices.SortFunc(ids, func(a, b core.InvoiceID) int { return cmp.Compare(a.String(), b.String()) })
	for _, id := range ids {
		if err := kernel.LockInvoice(ctx, r, org, id); err != nil {
			return err
		}
	}
	return nil
}

// requestPaymentEvent applies a payment event on the payment request id
// (PaymentRequests::Payments::StripeService#update_payment_status,
// app/services/payment_requests/payments/stripe_service.rb:35-91): the
// request and its invoices not already paid take the status, a succeeded
// request pays its invoices in full and resets the customer's dunning
// attempts in the currency when a campaign made it, and a failed request
// is emailed again.
func (s *Service) requestPaymentEvent(ctx context.Context, r port.Repos, pc providerConn, ev payment.PaymentEvent, p *payment.Payment, id core.PaymentRequestID) ([]*invoice.Invoice, error) {
	org := pc.integ.OrganizationID
	if err := lockRequest(ctx, r, org, id); err != nil {
		return nil, err
	}
	q, err := r.PaymentRequests.Get(ctx, org, id)
	if isNotFound(err) {
		if p == nil && ev.OneTime {
			return nil, notFound("payment_request")
		}
		return nil, nil
	}
	if err != nil {
		return nil, wrap(err, "load payment request %s", id)
	}
	if err := lockInvoices(ctx, r, org, q.InvoiceIDs); err != nil {
		return nil, err
	}
	now := s.d.Now()
	if p == nil {
		if !ev.OneTime && q.PaymentStatus == core.PaymentStatusFailed {
			return nil, nil
		}
		q.PaymentAttempts++
		q.UpdatedAt = now
		if err := r.PaymentRequests.Update(ctx, q); err != nil {
			return nil, wrap(err, "update payment request %s", q.ID)
		}
		if p, err = s.eventPayment(ctx, r, pc, ev, payment.RequestPayable(q.ID), q.CustomerID, q.Amount, q.Currency, now); err != nil || p == nil {
			return nil, err
		}
	}
	if q.PaymentStatus == core.PaymentStatusSucceeded {
		return nil, enqueueSetMethod(ctx, r, p, ev)
	}
	if err := applyEvent(ctx, r, p, ev, now); err != nil {
		return nil, err
	}
	st := payableStatus(p.Status)
	ready := ev.Status != payment.StateProcessing && st != core.PaymentStatusSucceeded
	if err := s.setRequestPayment(ctx, r, q, statusUpdate{status: st, ready: ready, notify: true}); err != nil {
		return nil, err
	}
	settle, err := s.setRequestInvoices(ctx, r, q, st, ready, true)
	if err != nil {
		return nil, err
	}
	if st == core.PaymentStatusSucceeded && !q.DunningCampaignID.IsZero() {
		if err := s.resetCustomerDunning(ctx, r, org, q.CustomerID, q.Currency, now); err != nil {
			return nil, err
		}
	}
	if q.PaymentStatus == core.PaymentStatusFailed {
		if err := enqueueRequestEmail(ctx, r, q); err != nil {
			return nil, err
		}
	}
	return settle, enqueueSetMethod(ctx, r, p, ev)
}

// setRequestInvoices gives the invoices of q the status st, skipping
// those already paid unless st is succeeded, and pays them in full when it
// is (update_invoices_payment_status and update_invoices_paid_amount_cents,
// app/services/payment_requests/payments/updatable.rb). It returns the
// credit invoices whose purchase must settle.
func (s *Service) setRequestInvoices(ctx context.Context, r port.Repos, q *payment.Request, st core.PaymentStatus, ready, notify bool) ([]*invoice.Invoice, error) {
	invs, err := r.Invoices.ListByIDs(ctx, q.OrganizationID, q.InvoiceIDs)
	if err != nil {
		return nil, wrap(err, "load invoices of payment request %s", q.ID)
	}
	var settle []*invoice.Invoice
	for _, inv := range invs {
		if inv.PaymentStatus == core.PaymentStatusSucceeded && st != core.PaymentStatusSucceeded {
			continue
		}
		u := statusUpdate{status: st, ready: ready, notify: notify}
		if st == core.PaymentStatusSucceeded {
			paid := inv.TotalPaidAmount + dueAmount(inv)
			u.paid = &paid
		}
		ok, err := s.setInvoicePayment(ctx, r, inv, u)
		if err != nil {
			return nil, err
		}
		if ok {
			settle = append(settle, inv)
		}
	}
	return settle, nil
}

// enqueueRequestEmail enqueues the email of payment request q (lago
// PaymentRequestMailer#requested deliver_later).
func enqueueRequestEmail(ctx context.Context, r port.Repos, q *payment.Request) error {
	args := kernel.PaymentRequestArgs{PaymentRequestID: q.ID}
	return kernel.Enqueue(ctx, r, q.OrganizationID, kernel.JobSendPaymentRequestEmail, args, "", time.Time{})
}

// providerCustomer returns the live provider customer of a connection
// with the provider's ID, or nil.
func providerCustomer(ctx context.Context, r port.Repos, integ *integration.Integration, providerCustomerID string) (*payment.ProviderCustomer, error) {
	if providerCustomerID == "" {
		return nil, nil
	}
	pc, err := r.PaymentProviderCustomers.GetByProviderCustomerID(ctx, integ.OrganizationID, integ.ID, providerCustomerID)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap(err, "load provider customer %s", providerCustomerID)
	}
	return pc, nil
}

// missingCustomer handles an event about a provider customer Tanvo does
// not know (stripe webhooks/base_service.rb:27-49): one created outside
// Tanvo, of a customer of another organization or since deleted, or, with
// linked, of a customer linked to another provider customer is ignored;
// otherwise the provider customer is not found.
func missingCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, refs payment.Refs, linked bool) error {
	if refs.CustomerID.IsZero() {
		return nil
	}
	c, err := r.Customers.Get(ctx, org, refs.CustomerID)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return wrap(err, "load customer %s", refs.CustomerID)
	}
	if c.DeletedAt != nil {
		return nil
	}
	if linked {
		pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, c.ID)
		if err != nil {
			return wrap(err, "list payment connections of customer %s", c.ID)
		}
		if len(pcs) > 0 {
			return nil
		}
	}
	return notFound("payment_provider_customer")
}

// handleMethodAttached records a payment method saved for a provider
// customer. One saved through a setup page (lago
// SetupIntentSucceededService) must still be attached at the provider; it
// becomes the customer's default there and in Tanvo, with its details,
// and the customer's invoices waiting for a method are collected again.
// Another is recorded, its details fetched when the event has none.
//
// The provider customer is found by the provider's ID, else by the Tanvo
// customer the event names ([eventCustomer]); one still without a
// provider ID takes the event's (an Adyen shopper reference, lago
// adyen_service.rb:70-82, announced by customer.payment_provider_created).
// A provider that neither fetches methods nor keeps a default
// ([payment.ErrUnsupported]) has its method recorded from the event.
func (s *Service) handleMethodAttached(ctx context.Context, pc providerConn, e payment.PaymentMethodAttached) error {
	org := pc.integ.OrganizationID
	pcust, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.ProviderCustomer, error) {
		pcust, err := s.eventCustomer(ctx, r, pc.integ, e.ProviderCustomerID, e.Refs)
		if err != nil || pcust != nil {
			return pcust, err
		}
		return nil, missingCustomer(ctx, r, org, e.Refs, true)
	})
	if err != nil || pcust == nil || e.ProviderMethodID == "" {
		return err
	}
	providerID := providerCustomerID(pcust)
	details := e.Details
	if e.SetAsDefault {
		pm, err := pc.gw.PaymentMethod(ctx, pc.conn, e.ProviderMethodID)
		switch {
		case errors.Is(err, payment.ErrUnsupported):
		case err != nil:
			return fmt.Errorf("fetch payment method %s: %w", e.ProviderMethodID, err)
		case pm.ProviderCustomerID == "":
			return nil
		default:
			if !pm.Details.IsZero() {
				details = pm.Details
			}
		}
		err = pc.gw.SetDefaultPaymentMethod(ctx, pc.conn, providerID, e.ProviderMethodID)
		if err != nil && !errors.Is(err, payment.ErrUnsupported) {
			return fmt.Errorf("set default payment method %s: %w", e.ProviderMethodID, err)
		}
	} else if details.IsZero() {
		pm, err := pc.gw.PaymentMethod(ctx, pc.conn, e.ProviderMethodID)
		switch {
		case err == nil:
			details = pm.Details
		case !errors.Is(err, payment.ErrUnsupported):
			return fmt.Errorf("fetch payment method %s: %w", e.ProviderMethodID, err)
		}
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if e.SetAsDefault {
			return s.updateDefaultMethod(ctx, r, pcust, e.ProviderMethodID, details)
		}
		_, err := s.findOrCreateMethod(ctx, r, pcust, e.ProviderMethodID, details, false)
		return err
	})
}

// eventCustomer returns the live provider customer of integ an event
// names: by the provider's customer ID, else the connection of the Tanvo
// customer of refs, else, for Adyen, of the customer whose external ID is
// the shopper reference (lago adyen_service.rb:70-73). A provider customer
// found so without a provider ID is given providerCustomerID, and
// customer.payment_provider_created is emitted (adyen_service.rb:78-82).
// It returns nil when the customer is unknown.
func (s *Service) eventCustomer(ctx context.Context, r port.Repos, integ *integration.Integration, providerCustomerID string, refs payment.Refs) (*payment.ProviderCustomer, error) {
	pcust, err := providerCustomer(ctx, r, integ, providerCustomerID)
	if err != nil || pcust != nil {
		return pcust, err
	}
	org := integ.OrganizationID
	id := refs.CustomerID
	if id.IsZero() && integ.Provider == integration.ProviderAdyen && providerCustomerID != "" {
		c, err := r.Customers.GetByExternalID(ctx, org, providerCustomerID)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, wrap(err, "load customer %s", providerCustomerID)
		}
		id = c.ID
	}
	if id.IsZero() {
		return nil, nil
	}
	pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, id)
	if err != nil {
		return nil, wrap(err, "list payment connections of customer %s", id)
	}
	for _, pc := range pcs {
		if pc.Deleted() || pc.IntegrationID != integ.ID {
			continue
		}
		if pc.ProviderCustomerID != nil && *pc.ProviderCustomerID != "" || providerCustomerID == "" {
			return pc, nil
		}
		c, err := r.Customers.Get(ctx, org, id)
		if err != nil {
			return nil, wrap(err, "load customer %s", id)
		}
		if c.DeletedAt != nil {
			return nil, nil
		}
		pid := providerCustomerID
		pc.ProviderCustomerID = &pid
		pc.UpdatedAt = s.d.Now()
		if err := r.PaymentProviderCustomers.Update(ctx, pc); err != nil {
			return nil, wrap(err, "update provider customer %s", pc.ID)
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeCustomerPaymentProviderCreated, "customer", c.ID.UUID(), c); err != nil {
			return nil, err
		}
		return pc, nil
	}
	return nil, nil
}

// handleCustomerUpdated applies a provider customer's default method (lago
// CustomerUpdatedService); a provider customer Tanvo does not know is
// ignored.
func (s *Service) handleCustomerUpdated(ctx context.Context, pc providerConn, e payment.CustomerUpdated) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		pcust, err := providerCustomer(ctx, r, pc.integ, e.ProviderCustomerID)
		if err != nil || pcust == nil {
			return err
		}
		return s.updateDefaultMethod(ctx, r, pcust, e.DefaultMethodID, payment.MethodDetails{})
	})
}

// CodeDeletedCustomer reports a provider event about a deleted customer.
const CodeDeletedCustomer = "deleted_customer"

// updateDefaultMethod makes the provider method id the default of the
// customer of pcust, recording it when Tanvo does not have it, and
// collects again the customer's finalized invoices waiting for a payment
// (PaymentProviderCustomers::Stripe::UpdatePaymentMethodService).
func (s *Service) updateDefaultMethod(ctx context.Context, r port.Repos, pcust *payment.ProviderCustomer, id string, details payment.MethodDetails) error {
	org := pcust.OrganizationID
	c, err := r.Customers.Get(ctx, org, pcust.CustomerID)
	if err != nil {
		return wrap(err, "load customer %s", pcust.CustomerID)
	}
	if c.DeletedAt != nil {
		return core.NewRuleError(CodeDeletedCustomer, "the customer of this provider customer was deleted")
	}
	if _, err := s.findOrCreateMethod(ctx, r, pcust, id, details, true); err != nil {
		return err
	}
	invs, err := kernel.All(func(page core.PageRequest) (core.Page[*invoice.Invoice], error) {
		return r.Invoices.List(ctx, org, invoice.Filter{
			CustomerID: c.ID, Statuses: []invoice.Status{invoice.StatusFinalized},
			PaymentStatuses: []core.PaymentStatus{core.PaymentStatusPending},
		}, page)
	})
	if err != nil {
		return wrap(err, "list pending invoices of customer %s", c.ID)
	}
	for _, inv := range invs {
		if !inv.ReadyForPaymentProcessing {
			continue
		}
		if err := kernel.Enqueue(ctx, r, org, kernel.JobCreateInvoicePayment, kernel.InvoiceArgs{InvoiceID: inv.ID}, invoiceKey(inv.ID), time.Time{}); err != nil {
			return err
		}
	}
	return nil
}

// findOrCreateMethod returns the customer's method of the provider
// identifier id, created from the provider with details and made the
// default when Tanvo does not have it (lago
// PaymentMethods::FindOrCreateFromProviderService and
// CreateFromProviderService); a found one is made the default with
// setDefault. An empty id finds nothing.
func (s *Service) findOrCreateMethod(ctx context.Context, r port.Repos, pcust *payment.ProviderCustomer, id string, details payment.MethodDetails, setDefault bool) (*payment.Method, error) {
	if id == "" {
		return nil, nil
	}
	org := pcust.OrganizationID
	m, err := r.PaymentMethods.GetByProviderMethodID(ctx, org, pcust.IntegrationID, id)
	switch {
	case err == nil:
		if m.CustomerID != pcust.CustomerID {
			return nil, nil
		}
		if !setDefault {
			return m, nil
		}
		return m, s.setDefault(ctx, r, m)
	case !isNotFound(err):
		return nil, wrap(err, "load payment method %s", id)
	}
	if details.Type == "" {
		details.Type = "card"
		if len(pcust.ProviderPaymentMethods) > 0 {
			details.Type = pcust.ProviderPaymentMethods[0]
		}
	}
	now := s.d.Now()
	m = &payment.Method{
		ID:                        core.NewID[core.PaymentMethodID](s.d.IDs),
		OrganizationID:            org,
		CustomerID:                pcust.CustomerID,
		IntegrationID:             pcust.IntegrationID,
		PaymentProviderCustomerID: pcust.ID,
		ProviderMethodID:          id,
		Details:                   details,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := r.PaymentMethods.Create(ctx, m); err != nil {
		return nil, wrap(err, "create payment method %s", id)
	}
	return m, s.setDefault(ctx, r, m)
}

// handleMethodDetached deletes a method removed at the provider (lago
// PaymentProviderCustomers::StripeService#delete_payment_method).
func (s *Service) handleMethodDetached(ctx context.Context, pc providerConn, e payment.PaymentMethodDetached) error {
	org := pc.integ.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		pcust, err := s.eventCustomer(ctx, r, pc.integ, e.ProviderCustomerID, e.Refs)
		if err != nil {
			return err
		}
		if pcust == nil {
			return missingCustomer(ctx, r, org, e.Refs, false)
		}
		m, err := r.PaymentMethods.GetByProviderMethodID(ctx, org, pc.integ.ID, e.ProviderMethodID)
		if isNotFound(err) || err == nil && m.CustomerID != pcust.CustomerID {
			return nil
		}
		if err != nil {
			return wrap(err, "load payment method %s", e.ProviderMethodID)
		}
		if err := m.Delete(s.d.Now()); err != nil {
			return err
		}
		if err := r.PaymentMethods.Update(ctx, m); err != nil {
			return wrap(err, "delete payment method %s", m.ID)
		}
		return nil
	})
}

// CustomerProviderError is the payload of customer.payment_provider_error
// (lago V1::PaymentProviders::CustomerErrorSerializer).
type CustomerProviderError struct {
	// CustomerID is the customer.
	CustomerID core.CustomerID `json:"customer_id"`
	// ExternalCustomerID is its external ID.
	ExternalCustomerID string `json:"external_customer_id"`
	// PaymentProvider is the connection's vendor.
	PaymentProvider integration.Provider `json:"payment_provider"`
	// PaymentProviderCode is the connection's code.
	PaymentProviderCode string `json:"payment_provider_code"`
	// ProviderError is the provider's error.
	ProviderError ProviderErrorDetails `json:"provider_error"`
}

// emitCustomerProviderError emits customer.payment_provider_error for c at
// the connection integ.
func (s *Service) emitCustomerProviderError(ctx context.Context, integ *integration.Integration, c *customerRef, perr ProviderErrorDetails) error {
	payload := CustomerProviderError{
		CustomerID: c.id, ExternalCustomerID: c.externalID, PaymentProvider: integ.Provider,
		PaymentProviderCode: integ.Code, ProviderError: perr,
	}
	return kernel.Emit(ctx, s.d, integ.OrganizationID, webhook.EventTypeCustomerPaymentProviderError, "customer", c.id.UUID(), payload)
}

// customerRef is the customer a provider webhook names.
type customerRef struct {
	id         core.CustomerID
	externalID string
}

// handleSetupFailed reports a failed setup of a payment method with
// customer.payment_provider_error (lago adyen_service.rb preauthorise,
// moneyhash webhooks); a customer Tanvo does not know is ignored.
func (s *Service) handleSetupFailed(ctx context.Context, pc providerConn, e payment.PaymentMethodSetupFailed) error {
	org := pc.integ.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		id := e.Refs.CustomerID
		pcust, err := providerCustomer(ctx, r, pc.integ, e.ProviderCustomerID)
		if err != nil {
			return err
		}
		if pcust != nil {
			id = pcust.CustomerID
		}
		if id.IsZero() {
			return nil
		}
		c, err := r.Customers.Get(ctx, org, id)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return wrap(err, "load customer %s", id)
		}
		perr := ProviderErrorDetails{Message: e.ErrorMessage, ErrorCode: e.ErrorCode}
		return s.emitCustomerProviderError(ctx, pc.integ, &customerRef{id: c.ID, externalID: c.ExternalID}, perr)
	})
}

// DisputeLost is the payload of invoice.payment_dispute_lost raised by a
// provider (lago SendWebhookJob with provider_error: reason).
type DisputeLost struct {
	*invoice.Invoice
	// ProviderError is the dispute's reason.
	ProviderError string `json:"provider_error"`
}

// loseDispute records a payment dispute lost at the provider on the
// invoices the payment paid (lago Payments::LoseDisputeService): each
// keeps the instant of an earlier loss, is no longer overdue, emits
// invoice.payment_dispute_lost with the reason and has its provider taxes
// voided. A payment Tanvo does not know is ignored; an invoice neither
// finalized nor voided fails the event (not_disputable).
func (s *Service) loseDispute(ctx context.Context, pc providerConn, e payment.DisputeClosed) error {
	org := pc.integ.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Payments.GetByProviderPaymentID(ctx, org, pc.integ.ID, e.ProviderPaymentID)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return wrap(err, "load payment %s", e.ProviderPaymentID)
		}
		ids := []core.InvoiceID{p.Payable.InvoiceID}
		if p.Payable.Type == payment.PayableTypePaymentRequest {
			if err := lockRequest(ctx, r, org, p.Payable.PaymentRequestID); err != nil {
				return err
			}
			q, err := r.PaymentRequests.Get(ctx, org, p.Payable.PaymentRequestID)
			if err != nil {
				return wrap(err, "load payment request %s", p.Payable.PaymentRequestID)
			}
			ids = q.InvoiceIDs
		}
		if err := lockInvoices(ctx, r, org, ids); err != nil {
			return err
		}
		at := e.ClosedAt
		if at.IsZero() {
			at = s.d.Now()
		}
		invs, err := r.Invoices.ListByIDs(ctx, org, ids)
		if err != nil {
			return wrap(err, "load invoices of payment %s", p.ID)
		}
		for _, inv := range invs {
			prev := inv.PaymentDisputeLostAt
			if err := inv.MarkDisputeLost(at.UTC()); err != nil {
				return err
			}
			if prev != nil {
				inv.PaymentDisputeLostAt = prev
			}
			inv.PaymentOverdue = false
			if err := r.Invoices.Update(ctx, inv); err != nil {
				return wrap(err, "update invoice %s", inv.ID)
			}
			if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoicePaymentDisputeLost, "invoice", inv.ID.UUID(), DisputeLost{Invoice: inv, ProviderError: e.Reason}); err != nil {
				return err
			}
			if err := kernel.Enqueue(ctx, r, org, kernel.JobVoidProviderTaxes, kernel.InvoiceArgs{InvoiceID: inv.ID}, "", s.d.Now()); err != nil {
				return err
			}
		}
		return nil
	})
}

// handleBalanceFunded collects again, after a bank transfer funded the
// balance of a customer paying only by balance, its oldest finalized
// invoice in the currency, pending or failed, whose last provider payment
// failed (lago CustomerCashBalanceTransactionCreatedService).
func (s *Service) handleBalanceFunded(ctx context.Context, pc providerConn, e payment.CustomerBalanceFunded) error {
	if e.EndingBalance <= 0 {
		return nil
	}
	org := pc.integ.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		pcust, err := providerCustomer(ctx, r, pc.integ, e.ProviderCustomerID)
		if err != nil || pcust == nil || !customerBalanceOnly(pc.integ, pcust) {
			return err
		}
		selfBilled := false
		invs, err := kernel.All(func(page core.PageRequest) (core.Page[*invoice.Invoice], error) {
			return r.Invoices.List(ctx, org, invoice.Filter{
				CustomerID: pcust.CustomerID, Statuses: []invoice.Status{invoice.StatusFinalized},
				PaymentStatuses: []core.PaymentStatus{core.PaymentStatusPending, core.PaymentStatusFailed},
				Currency:        e.Currency, SelfBilled: &selfBilled,
			}, page)
		})
		if err != nil {
			return wrap(err, "list collectible invoices of customer %s", pcust.CustomerID)
		}
		if len(invs) == 0 {
			return nil
		}
		oldest := slices.MinFunc(invs, func(a, b *invoice.Invoice) int {
			if c := a.IssuingDate.Compare(b.IssuingDate); c != 0 {
				return c
			}
			return a.CreatedAt.Compare(b.CreatedAt)
		})
		ps, err := r.Payments.ListByPayable(ctx, org, payment.InvoicePayable(oldest.ID))
		if err != nil {
			return wrap(err, "list payments of invoice %s", oldest.ID)
		}
		var last *payment.Payment
		for _, p := range ps {
			if p.Kind == payment.KindProvider {
				last = p
			}
		}
		if last == nil || last.Status != payment.StateFailed {
			return nil
		}
		return kernel.Enqueue(ctx, r, org, kernel.JobCreateInvoicePayment, kernel.InvoiceArgs{InvoiceID: oldest.ID}, invoiceKey(oldest.ID), time.Time{})
	})
}
