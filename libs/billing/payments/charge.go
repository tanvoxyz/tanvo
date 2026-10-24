package payments

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CheckoutPaymentDelay delays the first automatic payment of an invoice in
// an organization that uses hosted checkout links, so that a payment the
// customer is making through a link settles first (lago
// CHECKOUT_AUTO_PAYMENT_DELAY, app/services/invoices/payments/create_service.rb:11).
const CheckoutPaymentDelay = 10 * time.Minute

// ErrRetry marks a payment attempt to be run again later (lago
// RetriableError, 20 attempts polynomially longer): an authentication the
// customer can complete on-session.
var ErrRetry = errors.New("payments: retry the payment")

// codeAuthenticationRequired is the stored error code of a charge that
// needed the customer's authentication; a later attempt runs on-session
// (lago StripeProvider::NEED_3DS_ERROR_CODE).
const codeAuthenticationRequired = "authentication_required"

// charge is a provider charge prepared in a transaction and run after it.
type charge struct {
	pc        providerConn
	params    payment.PaymentParams
	paymentID core.PaymentID
	invoiceID core.InvoiceID
	cust      *customer.Customer
	pcust     *payment.ProviderCustomer
}

// CreateInvoicePayment collects an invoice through the customer's payment
// provider (lago Invoices::Payments::CreateService and CreateJob):
//
//   - the first attempt of an invoice of an organization that uses
//     checkout links waits [CheckoutPaymentDelay] (args.At zero), except a
//     payment-gated subscription invoice and a recurring wallet top-up;
//   - an invoice self-billed, paid, voided or closed, without a provider
//     customer, or without a payment method to charge is skipped;
//   - a zero invoice is marked paid;
//   - an invoice with a payment processing at the provider waits for its
//     webhook;
//   - otherwise the attempt counter is incremented, the pending provider
//     payment reused or created for the amount due, and the provider
//     charged outside the transaction. Its outcome sets the payment and
//     the invoice's payment status (processing counts as pending); a
//     failure emits invoice.payment_failure unless the invoice stays
//     pending or the customer must authenticate.
//
// A rate-limited or unavailable provider returns an error wrapping
// [payment.ErrRetryable]; an authentication worth an on-session retry
// returns [ErrRetry]; another unexpected provider failure is returned
// after it was recorded.
func (s *Service) CreateInvoicePayment(ctx context.Context, org core.OrganizationID, args kernel.InvoiceArgs) error {
	if s.providers == nil {
		return ErrNoProviders
	}
	var settle *invoice.Invoice
	ch, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*charge, error) {
		ch, inv, done, err := s.prepareInvoiceCharge(ctx, r, org, args)
		if done {
			settle = inv
		}
		return ch, err
	})
	if err != nil {
		return err
	}
	s.settleCredit(ctx, settle)
	if ch == nil {
		return nil
	}
	paid, err := s.alreadyPaid(ctx, ch)
	if err != nil || paid {
		return err
	}
	res, cerr := ch.pc.gw.CreatePayment(ctx, ch.pc.conn, ch.params)
	if cerr != nil && errors.Is(cerr, payment.ErrRetryable) {
		// lago raises the rate limit and connection errors out of the
		// service, leaving the invoice untouched (stripe
		// payments/create_service.rb:56-61).
		return fmt.Errorf("charge invoice %s: %w", ch.invoiceID, cerr)
	}
	if errors.Is(cerr, payment.ErrUnsupported) {
		// A provider paid through its links only (Cashfree, Flutterwave)
		// leaves the pending payment for its webhook
		// (cashfree/payments/create_service.rb:19-25).
		return nil
	}
	var out error
	settle = nil
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, retErr, err := s.applyInvoiceCharge(ctx, r, ch, res, cerr)
		settle, out = inv, retErr
		return err
	})
	if err != nil {
		return err
	}
	s.settleCredit(ctx, settle)
	return out
}

// settleCredit settles or fails the wallet purchase of a credit invoice
// whose payment status became succeeded or failed (lago
// Invoices::PrepaidCreditJob after the commit); a failure is logged, the
// payment staying recorded.
func (s *Service) settleCredit(ctx context.Context, inv *invoice.Invoice) {
	if inv == nil || s.credits == nil {
		return
	}
	succeeded := inv.PaymentStatus == core.PaymentStatusSucceeded
	if err := s.credits.HandleCreditInvoicePayment(ctx, inv.OrganizationID, inv.ID, succeeded); err != nil {
		s.d.Logger.ErrorContext(ctx, "payments: settle credit invoice purchase",
			"organization_id", inv.OrganizationID.String(), "invoice", inv.ID.String(), "error", err)
	}
}

// prepareInvoiceCharge is the first transaction of
// [Service.CreateInvoicePayment]: it returns the charge to run, or nil.
// The invoice is returned with done when its credit purchase must settle.
func (s *Service) prepareInvoiceCharge(ctx context.Context, r port.Repos, org core.OrganizationID, args kernel.InvoiceArgs) (*charge, *invoice.Invoice, bool, error) {
	if err := kernel.LockInvoice(ctx, r, org, args.InvoiceID); err != nil {
		return nil, nil, false, err
	}
	inv, err := r.Invoices.Get(ctx, org, args.InvoiceID)
	if isNotFound(err) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, wrap(err, "load invoice %s", args.InvoiceID)
	}
	pcust, integ, err := customerConnection(ctx, r, org, inv.CustomerID)
	if err != nil || integ == nil {
		// create_service.rb:104-105: no provider, no job.
		return nil, nil, false, err
	}
	now := s.d.Now()
	if args.At.IsZero() {
		deferred, err := s.deferForCheckout(ctx, r, inv)
		if err != nil {
			return nil, nil, false, err
		}
		if deferred {
			next := args
			next.At = now
			key := "invoice:" + inv.ID.String() + ":checkout-delay"
			return nil, nil, false, kernel.Enqueue(ctx, r, org, kernel.JobCreateInvoicePayment, next, key, now.Add(CheckoutPaymentDelay))
		}
	}
	// create_service.rb:149-158.
	if inv.SelfBilled || inv.PaymentStatus == core.PaymentStatusSucceeded ||
		inv.Status == invoice.StatusVoided || inv.Status == invoice.StatusClosed {
		return nil, nil, false, nil
	}
	if providerCustomerID(pcust) == "" {
		return nil, nil, false, nil
	}
	method, err := determineMethod(ctx, r, inv, args, now)
	if err != nil {
		return nil, nil, false, err
	}
	if method == nil && !customerBalanceOnly(integ, pcust) {
		return nil, nil, false, nil
	}
	if inv.TotalAmount <= 0 {
		paid, err := sumSucceeded(ctx, r, org, payment.InvoicePayable(inv.ID))
		if err != nil {
			return nil, nil, false, err
		}
		settle, err := s.setInvoicePayment(ctx, r, inv, statusUpdate{status: core.PaymentStatusSucceeded, paid: &paid, notify: true})
		return nil, inv, settle, err
	}
	ps, err := r.Payments.ListByPayable(ctx, org, payment.InvoicePayable(inv.ID))
	if err != nil {
		return nil, nil, false, wrap(err, "list payments of invoice %s", inv.ID)
	}
	// create_service.rb:252-260: a payment processing at the provider is
	// settled by its webhook.
	for _, p := range ps {
		if p.Kind == payment.KindProvider && p.IntegrationID == integ.ID && p.Status == payment.StateProcessing &&
			p.Amount == inv.TotalAmount && p.Currency == inv.Currency {
			return nil, nil, false, nil
		}
	}
	inv.PaymentAttempts++
	inv.UpdatedAt = now
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return nil, nil, false, wrap(err, "update invoice %s", inv.ID)
	}
	p, err := s.pendingPayment(ctx, r, ps, payment.InvoicePayable(inv.ID), inv.CustomerID, integ, dueAmount(inv), inv.Currency, method, now)
	if err != nil {
		return nil, nil, false, err
	}
	pc, err := s.open(ctx, integ)
	if err != nil {
		return nil, nil, false, err
	}
	cust, err := r.Customers.Get(ctx, org, inv.CustomerID)
	if err != nil {
		return nil, nil, false, wrap(err, "load customer %s", inv.CustomerID)
	}
	be, err := r.BillingEntities.Get(ctx, org, inv.BillingEntityID)
	if err != nil {
		return nil, nil, false, wrap(err, "load billing entity %s", inv.BillingEntityID)
	}
	gated, paymentGated, err := invoiceGating(ctx, r, inv)
	if err != nil {
		return nil, nil, false, err
	}
	// create_service.rb:244-250.
	ref := be.Name + " - Invoice " + inv.Number
	if gated {
		ref = be.Name + " - Invoice " + inv.ID.String()
	}
	params := payment.PaymentParams{
		PaymentID:          p.ID,
		Payable:            p.Payable,
		InvoiceType:        string(inv.Type),
		IssuingDate:        inv.IssuingDate,
		CustomerID:         inv.CustomerID,
		BillingEntityID:    inv.BillingEntityID,
		ProviderCustomerID: providerCustomerID(pcust),
		MethodTypes:        pcust.ProviderPaymentMethods,
		Amount:             p.Amount,
		Currency:           p.Currency,
		Description:        ref,
		CustomerCountry:    cust.BillingAddress.Country,
		// stripe payments/create_service.rb:68-70: a payable whose charge
		// needed authentication is retried on-session.
		OnSession:             hadAuthenticationError(ps),
		ActivatesSubscription: paymentGated,
		Customer:              payment.CustomerParamsOf(cust),
	}
	if be.Address.Country != "" {
		params.BillingEntityCountry = be.Address.Country
	}
	if method != nil {
		params.ProviderMethodID = method.ProviderMethodID
	}
	return &charge{pc: pc, params: params, paymentID: p.ID, invoiceID: inv.ID, cust: cust, pcust: pcust}, nil, false, nil
}

// pendingPayment returns the payable's pending provider payment, created
// for amount when it has none (lago Payment.create_with(…).find_or_create_by!(
// payable:, payable_payment_status: "pending")), its payment method set.
func (s *Service) pendingPayment(ctx context.Context, r port.Repos, ps []*payment.Payment, payable payment.Payable, cust core.CustomerID, integ *integration.Integration, amount int64, cur money.Currency, method *payment.Method, now time.Time) (*payment.Payment, error) {
	var methodID core.PaymentMethodID
	if method != nil {
		methodID = method.ID
	}
	for _, p := range ps {
		if p.Kind == payment.KindProvider && p.Status == payment.StatePending {
			p.PaymentMethodID = methodID
			p.UpdatedAt = now
			if err := r.Payments.Update(ctx, p); err != nil {
				return nil, wrap(err, "update payment %s", p.ID)
			}
			return p, nil
		}
	}
	p := &payment.Payment{
		ID:              core.NewID[core.PaymentID](s.d.IDs),
		OrganizationID:  integ.OrganizationID,
		Payable:         payable,
		CustomerID:      cust,
		Kind:            payment.KindProvider,
		Status:          payment.StatePending,
		Amount:          amount,
		Currency:        cur,
		IntegrationID:   integ.ID,
		PaymentMethodID: methodID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if err := r.Payments.Create(ctx, p); err != nil {
		return nil, wrap(err, "create payment")
	}
	return p, nil
}

// hadAuthenticationError reports whether a payment of the payable needed
// the customer's authentication (stripe
// payments/create_service.rb:68-70, payable_had_authentication_error?).
func hadAuthenticationError(ps []*payment.Payment) bool {
	return slices.ContainsFunc(ps, func(p *payment.Payment) bool {
		return p.ErrorCode != nil && *p.ErrorCode == codeAuthenticationRequired
	})
}

// alreadyPaid reports whether the invoice of ch was paid between the
// preparation and the charge, deleting then the unused pending payment
// when it was not charged (lago Invoices::Payments::AlreadyPaidError,
// create_service.rb:75-89; stripe payments/create_service.rb:123).
func (s *Service) alreadyPaid(ctx context.Context, ch *charge) (bool, error) {
	org := ch.pc.integ.OrganizationID
	var paid bool
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := r.Invoices.Get(ctx, org, ch.invoiceID)
		if err != nil {
			return wrap(err, "load invoice %s", ch.invoiceID)
		}
		if inv.PaymentStatus != core.PaymentStatusSucceeded {
			return nil
		}
		paid = true
		p, err := r.Payments.Get(ctx, org, ch.paymentID)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return wrap(err, "load payment %s", ch.paymentID)
		}
		if p.ProviderPaymentID == nil && p.Status != payment.StateSucceeded {
			return r.Payments.Delete(ctx, org, p.ID)
		}
		return nil
	})
	return paid, err
}

// applyInvoiceCharge records the outcome of the charge ch in the second
// transaction of [Service.CreateInvoicePayment]. It returns the invoice
// when its credit purchase must settle, and the error the job ends with.
func (s *Service) applyInvoiceCharge(ctx context.Context, r port.Repos, ch *charge, res payment.PaymentResult, cerr error) (*invoice.Invoice, error, error) {
	org := ch.pc.integ.OrganizationID
	if err := kernel.LockInvoice(ctx, r, org, ch.invoiceID); err != nil {
		return nil, nil, err
	}
	inv, err := r.Invoices.Get(ctx, org, ch.invoiceID)
	if err != nil {
		return nil, nil, wrap(err, "load invoice %s", ch.invoiceID)
	}
	p, err := r.Payments.Get(ctx, org, ch.paymentID)
	if err != nil {
		return nil, nil, wrap(err, "load payment %s", ch.paymentID)
	}
	now := s.d.Now()
	if cerr != nil {
		return s.invoiceChargeFailed(ctx, r, inv, p, ch, cerr, now)
	}
	if err := s.recordResult(ctx, r, p, res, now); err != nil {
		return nil, nil, err
	}
	// create_service.rb:203-220.
	st := payableStatus(p.Status)
	u := statusUpdate{status: st, ready: p.Status == payment.StatePending || p.Status == payment.StateFailed, notify: p.Status == payment.StateSucceeded}
	if p.Status == payment.StateSucceeded {
		paid, err := sumSucceeded(ctx, r, org, p.Payable)
		if err != nil {
			return nil, nil, err
		}
		u.paid = &paid
	}
	settle, err := s.setInvoicePayment(ctx, r, inv, u)
	if err != nil {
		return nil, nil, err
	}
	if err := s.syncPayment(ctx, r, p, inv); err != nil {
		return nil, nil, err
	}
	if settle {
		return inv, nil, nil
	}
	return nil, nil, nil
}

// recordResult stores a charge's result on p (stripe
// payments/create_service.rb:21-29): its provider identifiers and status,
// the next action of a payment requiring one, announced by
// payment.requires_action; a method the provider no longer has is
// deleted.
func (s *Service) recordResult(ctx context.Context, r port.Repos, p *payment.Payment, res payment.PaymentResult, now time.Time) error {
	if res.ProviderPaymentID != "" {
		p.ProviderPaymentID = &res.ProviderPaymentID
	}
	if res.ProviderStatus != "" {
		p.ProviderStatus = &res.ProviderStatus
	}
	if res.Status != "" {
		p.Status = res.Status
	}
	if res.RequiresAction && len(res.NextAction) > 0 {
		p.ProviderPaymentData = res.NextAction
	}
	if !res.MethodDetails.IsZero() {
		p.ProviderPaymentMethod = res.MethodDetails
	}
	p.UpdatedAt = now
	if err := r.Payments.Update(ctx, p); err != nil {
		return wrap(err, "update payment %s", p.ID)
	}
	if res.StaleMethodID != "" {
		if err := s.deleteProviderMethod(ctx, r, p.OrganizationID, p.IntegrationID, res.StaleMethodID, now); err != nil {
			return err
		}
	}
	if res.RequiresAction {
		return s.emitPayment(ctx, r, webhook.EventTypePaymentRequiresAction, p)
	}
	return nil
}

// deleteProviderMethod deletes the live method of a connection with the
// provider's identifier, when there is one.
func (s *Service) deleteProviderMethod(ctx context.Context, r port.Repos, org core.OrganizationID, integ core.IntegrationID, providerMethodID string, now time.Time) error {
	m, err := r.PaymentMethods.GetByProviderMethodID(ctx, org, integ, providerMethodID)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return wrap(err, "load payment method %s", providerMethodID)
	}
	if err := m.Delete(now); err != nil {
		return err
	}
	if err := r.PaymentMethods.Update(ctx, m); err != nil {
		return wrap(err, "delete payment method %s", m.ID)
	}
	return nil
}

// failure is a classified provider failure of a charge (lago
// prepare_failed_result of each provider's Payments::CreateService).
type failure struct {
	// status is the payable status of the failed payment.
	status payment.State
	// retry runs the attempt again later, the payable pending.
	retry bool
	// silent skips the failure webhook (create_service.rb:222-229).
	silent bool
	// reraise ends the job with the error after it was recorded.
	reraise bool
}

// classify returns how a provider's failure of a charge is recorded:
//
//   - an amount below the provider's minimum and a reused idempotency key
//     leave the payable pending (stripe payments/create_service.rb:36-41,52-53);
//   - an authentication the customer must complete fails the payment
//     without webhook, and retries on-session when the provider supports
//     it or the charge activates a subscription
//     (stripe payments/create_service.rb:42-44, create_service.rb:131-139);
//   - any other refusal fails it with the webhook; a failure the provider
//     did not classify is raised as well (stripe payments/create_service.rb:62-63).
func classify(err error) failure {
	pe, ok := payment.AsProviderError(err)
	if !ok {
		if errors.Is(err, payment.ErrNoPaymentMethod) || errors.Is(err, payment.ErrMissingCountry) {
			return failure{status: payment.StateFailed}
		}
		return failure{status: payment.StateFailed, reraise: true}
	}
	switch pe.Kind {
	case payment.KindAmountTooSmall:
		return failure{status: payment.StatePending, silent: true}
	case payment.KindIdempotency:
		return failure{status: payment.StatePending}
	case payment.KindAuthenticationRequired:
		return failure{status: payment.StateFailed, retry: pe.RetryOnSession, silent: true}
	case payment.KindPermanent:
		return failure{status: payment.StateFailed, reraise: true}
	default:
		return failure{status: payment.StateFailed}
	}
}

// errorCodeOf returns the stored error code of a failure.
func errorCodeOf(err error) string {
	if pe, ok := payment.AsProviderError(err); ok {
		if pe.Kind == payment.KindAuthenticationRequired {
			return codeAuthenticationRequired
		}
		if pe.Code != "" {
			return pe.Code
		}
		return string(pe.Kind)
	}
	return ""
}

// recordFailure stores a classified failure on p (prepare_failed_result):
// the provider's status is failed, the payable status that of f, and the
// provider's payment and error code are kept.
func recordFailure(ctx context.Context, r port.Repos, p *payment.Payment, cause error, f failure, now time.Time) error {
	failed := string(payment.StateFailed)
	p.ProviderStatus = &failed
	p.Status = f.status
	if pe, ok := payment.AsProviderError(cause); ok && pe.ProviderPaymentID != "" {
		id := pe.ProviderPaymentID
		p.ProviderPaymentID = &id
	}
	if code := errorCodeOf(cause); code != "" {
		p.ErrorCode = &code
	}
	p.UpdatedAt = now
	if err := r.Payments.Update(ctx, p); err != nil {
		return wrap(err, "update payment %s", p.ID)
	}
	return nil
}

// invoiceChargeFailed records a failed charge of inv (create_service.rb:90-102).
func (s *Service) invoiceChargeFailed(ctx context.Context, r port.Repos, inv *invoice.Invoice, p *payment.Payment, ch *charge, cause error, now time.Time) (*invoice.Invoice, error, error) {
	f := classify(cause)
	if err := recordFailure(ctx, r, p, cause, f, now); err != nil {
		return nil, nil, err
	}
	// create_service.rb:222-229: a payable left pending is not reported.
	if !f.silent && p.Status != payment.StatePending {
		src := failureSource{cust: ch.cust, pc: ch.pcust, integ: ch.pc.integ}
		if err := s.emitInvoiceFailure(ctx, r, inv, src, cause, errorDetailsOf(cause)); err != nil {
			return nil, nil, err
		}
	}
	st := payableStatus(p.Status)
	if f.retry {
		st = core.PaymentStatusPending
	}
	settle, err := s.setInvoicePayment(ctx, r, inv, statusUpdate{
		status: st, ready: st == core.PaymentStatusPending || st == core.PaymentStatusFailed,
	})
	if err != nil {
		return nil, nil, err
	}
	var out error
	switch {
	case f.retry:
		out = fmt.Errorf("charge invoice %s: %w: %w", inv.ID, ErrRetry, cause)
	case f.reraise:
		out = fmt.Errorf("charge invoice %s: %w", inv.ID, cause)
	}
	if settle {
		return inv, out, nil
	}
	return nil, out, nil
}

// syncPayment enqueues the accounting sync of a succeeded payment of a
// finalized invoice (lago Payment#should_sync_payment?).
func (s *Service) syncPayment(ctx context.Context, r port.Repos, p *payment.Payment, inv *invoice.Invoice) error {
	if s.sync == nil || p.Status != payment.StateSucceeded || inv == nil || inv.Status != invoice.StatusFinalized {
		return nil
	}
	return s.sync.EnqueuePaymentSync(ctx, r, p, inv)
}

// deferForCheckout reports whether the first payment attempt of inv waits
// for a checkout link payment (create_service.rb:132-139): only in an
// organization that created checkout links, not for a payment-gated
// subscription invoice nor for a recurring wallet top-up.
func (s *Service) deferForCheckout(ctx context.Context, r port.Repos, inv *invoice.Invoice) (bool, error) {
	if inv.PaymentAttempts != 0 {
		return false, nil
	}
	org := inv.OrganizationID
	switch inv.Type {
	case invoice.TypeSubscription:
		_, paymentGated, err := invoiceGating(ctx, r, inv)
		if err != nil || paymentGated {
			return false, err
		}
	case invoice.TypeCredit:
		// create_service.rb:134,141-143: a billing entity setting here.
		be, err := r.BillingEntities.Get(ctx, org, inv.BillingEntityID)
		if err != nil && !isNotFound(err) {
			return false, wrap(err, "load billing entity %s", inv.BillingEntityID)
		}
		if be != nil && be.SkipCreditInvoicePaymentDelay {
			return false, nil
		}
		txs, err := r.WalletTransactions.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return false, wrap(err, "list wallet transactions of invoice %s", inv.ID)
		}
		if slices.ContainsFunc(txs, func(t *wallet.Transaction) bool {
			return t.Inbound() && t.Source != wallet.TransactionSourceManual
		}) {
			return false, nil
		}
	}
	ok, err := r.PaymentIntents.Exists(ctx, org)
	if err != nil {
		return false, wrap(err, "check checkout links")
	}
	return ok, nil
}

// invoiceGating reports whether inv is open and bills a subscription
// waiting for an activation rule (lago Invoice#subscription_gated?) and
// for a payment rule (subscription_payment_gated?).
func invoiceGating(ctx context.Context, r port.Repos, inv *invoice.Invoice) (gated, paymentGated bool, err error) {
	if inv.Status != invoice.StatusOpen {
		return false, false, nil
	}
	org := inv.OrganizationID
	links, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return false, false, wrap(err, "list subscriptions of invoice %s", inv.ID)
	}
	for _, l := range links {
		sub, err := r.Subscriptions.Get(ctx, org, l.SubscriptionID)
		if err != nil {
			return false, false, wrap(err, "load subscription %s", l.SubscriptionID)
		}
		if sub.Status != subscription.StatusIncomplete {
			continue
		}
		rules, err := r.ActivationRules.ListBySubscription(ctx, org, sub.ID)
		if err != nil {
			return false, false, wrap(err, "load activation rules of %s", sub.ID)
		}
		gated = gated || subscription.Gated(sub, rules)
		paymentGated = paymentGated || subscription.PaymentGated(sub, rules)
	}
	return gated, paymentGated, nil
}

// customerBalanceOnly reports a Stripe customer paying only by bank
// transfer to its balance, chargeable without a payment method
// (create_service.rb:160-171).
func customerBalanceOnly(integ *integration.Integration, pc *payment.ProviderCustomer) bool {
	return integ.Provider == integration.ProviderStripe && slices.Equal(pc.ProviderPaymentMethods, []string{"customer_balance"})
}

// determineMethod returns the payment method that collects inv, or nil
// when it is collected manually or has none (lago
// PaymentMethods::DetermineService): an override of the job first, then
// the method chosen on the first subscription of a subscription,
// advance-charges or progressive-billing invoice, or on the purchase of a
// credit invoice (then its active recurring rule for a recurring top-up,
// then its wallet); otherwise the customer's default.
func determineMethod(ctx context.Context, r port.Repos, inv *invoice.Invoice, args kernel.InvoiceArgs, now time.Time) (*payment.Method, error) {
	org, cust := inv.OrganizationID, inv.CustomerID
	if args.PaymentMethodType != "" || !args.PaymentMethodID.IsZero() {
		return chosenMethod(ctx, r, org, cust, args.PaymentMethodType, args.PaymentMethodID)
	}
	switch inv.Type {
	case invoice.TypeSubscription, invoice.TypeAdvanceCharges, invoice.TypeProgressiveBilling:
		links, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return nil, wrap(err, "list subscriptions of invoice %s", inv.ID)
		}
		if len(links) == 0 {
			return nil, nil
		}
		sub, err := r.Subscriptions.Get(ctx, org, links[0].SubscriptionID)
		if err != nil {
			return nil, wrap(err, "load subscription %s", links[0].SubscriptionID)
		}
		return chosenMethod(ctx, r, org, cust, sub.PaymentMethodType, sub.PaymentMethodID)
	case invoice.TypeCredit:
		return creditMethod(ctx, r, inv, now)
	default:
		return defaultMethod(ctx, r, org, cust)
	}
}

// creditMethod is determine_credit_payment_method.
func creditMethod(ctx context.Context, r port.Repos, inv *invoice.Invoice, now time.Time) (*payment.Method, error) {
	org, cust := inv.OrganizationID, inv.CustomerID
	txs, err := r.WalletTransactions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, wrap(err, "list wallet transactions of invoice %s", inv.ID)
	}
	if len(txs) == 0 {
		return nil, nil
	}
	t := txs[0]
	if t.PaymentMethodType == core.PaymentMethodTypeManual {
		return nil, nil
	}
	if !t.PaymentMethodID.IsZero() {
		return liveCustomerMethod(ctx, r, org, cust, t.PaymentMethodID)
	}
	if t.Source == wallet.TransactionSourceInterval || t.Source == wallet.TransactionSourceThreshold {
		rules, err := r.RecurringTransactionRules.ListByWallet(ctx, org, t.WalletID)
		if err != nil {
			return nil, wrap(err, "list recurring rules of wallet %s", t.WalletID)
		}
		for _, rule := range rules {
			if !rule.CurrentlyActive(now) {
				continue
			}
			if rule.PaymentMethodType == core.PaymentMethodTypeManual {
				return nil, nil
			}
			if !rule.PaymentMethodID.IsZero() {
				return liveCustomerMethod(ctx, r, org, cust, rule.PaymentMethodID)
			}
			break
		}
	}
	w, err := r.Wallets.Get(ctx, org, t.WalletID)
	if err != nil {
		return nil, wrap(err, "load wallet %s", t.WalletID)
	}
	return chosenMethod(ctx, r, org, cust, w.PaymentMethodType, w.PaymentMethodID)
}

// chosenMethod resolves one level's choice: manual collects by hand, a
// method must be one of the customer's live methods, and nothing falls
// back to the customer's default.
func chosenMethod(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, t core.PaymentMethodType, id core.PaymentMethodID) (*payment.Method, error) {
	if t == core.PaymentMethodTypeManual {
		return nil, nil
	}
	if !id.IsZero() {
		return liveCustomerMethod(ctx, r, org, cust, id)
	}
	return defaultMethod(ctx, r, org, cust)
}

// liveCustomerMethod returns the customer's live method id, or nil (lago
// customer.payment_methods.find_by(id:)).
func liveCustomerMethod(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, id core.PaymentMethodID) (*payment.Method, error) {
	m, err := r.PaymentMethods.Get(ctx, org, id)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap(err, "load payment method %s", id)
	}
	if m.Deleted() || m.CustomerID != cust {
		return nil, nil
	}
	return m, nil
}

// defaultMethod returns the customer's live default method, or nil.
func defaultMethod(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID) (*payment.Method, error) {
	m, err := r.PaymentMethods.GetDefault(ctx, org, cust)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap(err, "load default payment method of customer %s", cust)
	}
	return m, nil
}
