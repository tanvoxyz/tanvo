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
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// requestCharge is a provider charge of a payment request prepared in a
// transaction and run after it.
type requestCharge struct {
	pc        providerConn
	params    payment.PaymentParams
	paymentID core.PaymentID
	requestID core.PaymentRequestID
	cust      *customer.Customer
	pcust     *payment.ProviderCustomer
}

// enqueueRequestCollection enqueues, after a payment request is created,
// its collection through the customer's payment provider, or its email
// when the customer has none (lago PaymentRequests::CreateService
// after_commit, app/services/payment_requests/create_service.rb:40-45).
func enqueueRequestCollection(ctx context.Context, r port.Repos, q *payment.Request) error {
	_, integ, err := customerConnection(ctx, r, q.OrganizationID, q.CustomerID)
	if err != nil {
		return err
	}
	if integ == nil {
		return enqueueRequestEmail(ctx, r, q)
	}
	args := kernel.PaymentRequestArgs{PaymentRequestID: q.ID, PaymentMethodType: q.PaymentMethodType, PaymentMethodID: q.PaymentMethodID}
	return kernel.Enqueue(ctx, r, q.OrganizationID, kernel.JobCreateRequestPayment, args, "payment_request:"+q.ID.String(), time.Time{})
}

// CreateRequestPayment collects a payment request through the customer's
// payment provider (lago PaymentRequests::Payments::CreateService and
// CreateJob, app/services/payment_requests/payments/create_service.rb):
//
//   - a customer without a payment connection fails with not found;
//   - a request already paid, of a customer without a provider customer
//     or with an invoice not ready for payment processing is skipped;
//   - a zero request is marked paid;
//   - a request with a payment processing at the provider waits for its
//     webhook;
//   - otherwise the attempt counter is incremented and the pending
//     provider payment reused or created for the request's amount, with
//     the request's payment method (none for manual) or else the
//     customer's default, and the provider charged outside the
//     transaction. Its outcome sets the payment status of the request and
//     of its invoices not already paid (processing counts as pending), a
//     succeeded payment pays the invoices in full and gets its receipt, and
//     a failed request is emailed again. A failure is recorded on the
//     request only, announced by payment_request.payment_failure, and the
//     request is emailed.
//
// A rate-limited or unavailable provider returns an error wrapping
// [payment.ErrRetryable]; an unexpected provider failure is returned
// after it was recorded.
func (s *Service) CreateRequestPayment(ctx context.Context, org core.OrganizationID, args kernel.PaymentRequestArgs) error {
	if s.providers == nil {
		return ErrNoProviders
	}
	ch, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*requestCharge, error) {
		return s.prepareRequestCharge(ctx, r, org, args)
	})
	if err != nil || ch == nil {
		return err
	}
	paid, err := s.requestAlreadyPaid(ctx, ch)
	if err != nil || paid {
		return err
	}
	res, cerr := ch.pc.gw.CreatePayment(ctx, ch.pc.conn, ch.params)
	if cerr != nil && errors.Is(cerr, payment.ErrRetryable) {
		return fmt.Errorf("charge payment request %s: %w", ch.requestID, cerr)
	}
	if errors.Is(cerr, payment.ErrUnsupported) {
		// A provider paid through its links only (Cashfree) leaves the
		// pending payment for its webhook (cashfree/payments/create_service.rb:19-25).
		return nil
	}
	var out error
	var settle []*invoice.Invoice
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		invs, retErr, err := s.applyRequestCharge(ctx, r, ch, res, cerr)
		settle, out = invs, retErr
		return err
	})
	if err != nil {
		return err
	}
	for _, inv := range settle {
		s.settleCredit(ctx, inv)
	}
	return out
}

// prepareRequestCharge is the first transaction of
// [Service.CreateRequestPayment]: it returns the charge to run, or nil.
func (s *Service) prepareRequestCharge(ctx context.Context, r port.Repos, org core.OrganizationID, args kernel.PaymentRequestArgs) (*requestCharge, error) {
	if err := lockRequest(ctx, r, org, args.PaymentRequestID); err != nil {
		return nil, err
	}
	q, err := r.PaymentRequests.Get(ctx, org, args.PaymentRequestID)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap(err, "load payment request %s", args.PaymentRequestID)
	}
	pcust, integ, err := customerConnection(ctx, r, org, q.CustomerID)
	if err != nil {
		return nil, err
	}
	if integ == nil {
		// The connection was removed after the job was enqueued: nothing
		// to collect (create_service.rb:124, should_process_payment?). The
		// customer without one at creation is emailed instead
		// (enqueueRequestCollection, create_service.rb:104).
		return nil, nil
	}
	if err := lockInvoices(ctx, r, org, q.InvoiceIDs); err != nil {
		return nil, err
	}
	// create_service.rb should_process_payment?.
	if q.PaymentStatus == core.PaymentStatusSucceeded || providerCustomerID(pcust) == "" {
		return nil, nil
	}
	invs, err := r.Invoices.ListByIDs(ctx, org, q.InvoiceIDs)
	if err != nil {
		return nil, wrap(err, "load invoices of payment request %s", q.ID)
	}
	if slices.ContainsFunc(invs, func(inv *invoice.Invoice) bool { return !inv.ReadyForPaymentProcessing }) {
		return nil, nil
	}
	if q.Amount <= 0 {
		return nil, s.setRequestPayment(ctx, r, q, statusUpdate{status: core.PaymentStatusSucceeded, notify: true})
	}
	ps, err := r.Payments.ListByPayable(ctx, org, payment.RequestPayable(q.ID))
	if err != nil {
		return nil, wrap(err, "list payments of payment request %s", q.ID)
	}
	for _, p := range ps {
		if p.Kind == payment.KindProvider && p.IntegrationID == integ.ID && p.Status == payment.StateProcessing &&
			p.Amount == q.Amount && p.Currency == q.Currency {
			return nil, nil
		}
	}
	now := s.d.Now()
	q.PaymentAttempts++
	q.UpdatedAt = now
	if err := r.PaymentRequests.Update(ctx, q); err != nil {
		return nil, wrap(err, "update payment request %s", q.ID)
	}
	method, err := requestMethod(ctx, r, q, args)
	if err != nil {
		return nil, err
	}
	p, err := s.pendingPayment(ctx, r, ps, payment.RequestPayable(q.ID), q.CustomerID, integ, q.Amount, q.Currency, method, now)
	if err != nil {
		return nil, err
	}
	pc, err := s.open(ctx, integ)
	if err != nil {
		return nil, err
	}
	cust, err := r.Customers.Get(ctx, org, q.CustomerID)
	if err != nil {
		return nil, wrap(err, "load customer %s", q.CustomerID)
	}
	beID, err := payableBillingEntity(ctx, r, p)
	if err != nil {
		return nil, err
	}
	be, err := r.BillingEntities.Get(ctx, org, beID)
	if err != nil {
		return nil, wrap(err, "load billing entity %s", beID)
	}
	// create_service.rb payment_reference.
	ref := be.Name + " - Overdue invoices"
	if len(invs) == 1 {
		ref += ": " + invs[0].Number
	}
	params := payment.PaymentParams{
		PaymentID:            p.ID,
		Payable:              p.Payable,
		CustomerID:           q.CustomerID,
		BillingEntityID:      beID,
		ProviderCustomerID:   providerCustomerID(pcust),
		MethodTypes:          pcust.ProviderPaymentMethods,
		Amount:               p.Amount,
		Currency:             p.Currency,
		Description:          ref,
		CustomerCountry:      cust.BillingAddress.Country,
		BillingEntityCountry: be.Address.Country,
		OnSession:            hadAuthenticationError(ps),
		Customer:             payment.CustomerParamsOf(cust),
	}
	if method != nil {
		params.ProviderMethodID = method.ProviderMethodID
	}
	return &requestCharge{pc: pc, params: params, paymentID: p.ID, requestID: q.ID, cust: cust, pcust: pcust}, nil
}

// requestMethod returns the method charged for q (create_service.rb
// determine_payment_method): with a method chosen, none for manual, that
// method or else the customer's default; otherwise the customer's
// default.
func requestMethod(ctx context.Context, r port.Repos, q *payment.Request, args kernel.PaymentRequestArgs) (*payment.Method, error) {
	org := q.OrganizationID
	if args.PaymentMethodType == "" && args.PaymentMethodID.IsZero() {
		return defaultMethod(ctx, r, org, q.CustomerID)
	}
	if args.PaymentMethodType == core.PaymentMethodTypeManual {
		return nil, nil
	}
	if !args.PaymentMethodID.IsZero() {
		return liveCustomerMethod(ctx, r, org, q.CustomerID, args.PaymentMethodID)
	}
	return defaultMethod(ctx, r, org, q.CustomerID)
}

// requestAlreadyPaid reports whether the request of ch was paid between
// the preparation and the charge, deleting then the unused pending payment
// (create_service.rb AlreadyPaidError).
func (s *Service) requestAlreadyPaid(ctx context.Context, ch *requestCharge) (bool, error) {
	org := ch.pc.integ.OrganizationID
	var paid bool
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		q, err := r.PaymentRequests.Get(ctx, org, ch.requestID)
		if err != nil {
			return wrap(err, "load payment request %s", ch.requestID)
		}
		if q.PaymentStatus != core.PaymentStatusSucceeded {
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

// applyRequestCharge records the outcome of the charge ch in the second
// transaction of [Service.CreateRequestPayment]. It returns the credit
// invoices whose purchase must settle and the error the job ends with.
func (s *Service) applyRequestCharge(ctx context.Context, r port.Repos, ch *requestCharge, res payment.PaymentResult, cerr error) ([]*invoice.Invoice, error, error) {
	org := ch.pc.integ.OrganizationID
	if err := lockRequest(ctx, r, org, ch.requestID); err != nil {
		return nil, nil, err
	}
	q, err := r.PaymentRequests.Get(ctx, org, ch.requestID)
	if err != nil {
		return nil, nil, wrap(err, "load payment request %s", ch.requestID)
	}
	if err := lockInvoices(ctx, r, org, q.InvoiceIDs); err != nil {
		return nil, nil, err
	}
	p, err := r.Payments.Get(ctx, org, ch.paymentID)
	if err != nil {
		return nil, nil, wrap(err, "load payment %s", ch.paymentID)
	}
	now := s.d.Now()
	if cerr != nil {
		// create_service.rb rescue ServiceFailure: the request is emailed,
		// the failure announced and the request alone updated.
		f := classify(cerr)
		if err := recordFailure(ctx, r, p, cerr, f, now); err != nil {
			return nil, nil, err
		}
		if err := enqueueRequestEmail(ctx, r, q); err != nil {
			return nil, nil, err
		}
		src := failureSource{cust: ch.cust, pc: ch.pcust, integ: ch.pc.integ}
		if err := s.emitRequestFailure(ctx, q, src, cerr); err != nil {
			return nil, nil, err
		}
		st := payableStatus(p.Status)
		if err := s.setRequestPayment(ctx, r, q, statusUpdate{status: st, ready: p.Status == payment.StateFailed}); err != nil {
			return nil, nil, err
		}
		if f.reraise {
			return nil, fmt.Errorf("charge payment request %s: %w", q.ID, cerr), nil
		}
		return nil, nil, nil
	}
	if err := s.recordResult(ctx, r, p, res, now); err != nil {
		return nil, nil, err
	}
	st := payableStatus(p.Status)
	ready := p.Status == payment.StateFailed
	notify := st == core.PaymentStatusSucceeded
	if err := s.setRequestPayment(ctx, r, q, statusUpdate{status: st, ready: ready, notify: notify}); err != nil {
		return nil, nil, err
	}
	settle, err := s.setRequestInvoices(ctx, r, q, st, ready, notify)
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.createReceipt(ctx, r, p); err != nil {
		return nil, nil, err
	}
	if q.PaymentStatus == core.PaymentStatusFailed {
		if err := enqueueRequestEmail(ctx, r, q); err != nil {
			return nil, nil, err
		}
	}
	return settle, nil, nil
}
