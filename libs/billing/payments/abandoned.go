package payments

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Batching of the abandoned payment cancellations
// (app/jobs/clock/cancel_abandoned_payments_job.rb:7-8): each batch of
// abandonedBatch payments runs abandonedSpacing after the previous one.
const (
	abandonedBatch   = 100
	abandonedSpacing = time.Minute
)

// CancelAbandonedPayments enqueues a [kernel.JobCancelAbandonedPayment]
// for each Stripe invoice payment of org processing while waiting for the
// customer's action, last changed within [payment.AbandonedRange] of
// args.At (lago Clock::CancelAbandonedPaymentsJob), batches of 100 spaced
// a minute apart.
func (s *Service) CancelAbandonedPayments(ctx context.Context, org core.OrganizationID, args kernel.InstantArgs) error {
	from, to := payment.AbandonedRange(args.At)
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ps, err := r.Payments.ListAbandoned(ctx, org, from, to)
		if err != nil {
			return wrap(err, "list abandoned payments")
		}
		providers := map[core.IntegrationID]integration.Provider{}
		n := 0
		for _, p := range ps {
			vendor, ok := providers[p.IntegrationID]
			if !ok {
				integ, err := r.Integrations.Get(ctx, org, p.IntegrationID)
				switch {
				case isNotFound(err):
				case err != nil:
					return wrap(err, "load integration %s", p.IntegrationID)
				default:
					vendor = integ.Provider
				}
				providers[p.IntegrationID] = vendor
			}
			if vendor != integration.ProviderStripe {
				continue
			}
			at := s.d.Now().Add(time.Duration(n/abandonedBatch) * abandonedSpacing)
			n++
			if err := kernel.Enqueue(ctx, r, org, kernel.JobCancelAbandonedPayment, kernel.PaymentArgs{PaymentID: p.ID}, "payment:"+p.ID.String()+":abandoned", at); err != nil {
				return err
			}
		}
		return nil
	})
}

// stripeFailedStatuses are the Stripe intent statuses of a failed payment
// (lago PaymentProviders::StripeProvider::FAILED_STATUSES).
var stripeFailedStatuses = []string{"canceled", "requires_payment_method"}

// CancelAbandonedPayment recovers an invoice payment abandoned at the
// provider's redirect (lago Invoices::Payments::CancelAbandonedService,
// app/services/invoices/payments/cancel_abandoned_service.rb):
//
//   - only a Stripe provider payment of an invoice neither paid, voided
//     nor activating a payment-gated subscription, processing while the
//     customer must follow a redirect, last changed within
//     [payment.AbandonedRange], is recovered;
//   - the provider's payment is read; one the provider failed meanwhile
//     takes the failed state, and a card payment still waiting for the
//     customer is cancelled at the provider;
//   - a payment failed so unlocks its invoice for a new automatic attempt
//     (ready for payment processing), unless the invoice was paid.
//
// A payment the provider cannot be asked about is left as it is.
func (s *Service) CancelAbandonedPayment(ctx context.Context, org core.OrganizationID, args kernel.PaymentArgs) error {
	if s.providers == nil {
		return ErrNoProviders
	}
	type target struct {
		p  *payment.Payment
		pc providerConn
	}
	t, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*target, error) {
		p, err := r.Payments.Get(ctx, org, args.PaymentID)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, wrap(err, "load payment %s", args.PaymentID)
		}
		ok, err := s.abandoned(ctx, r, p)
		if err != nil || !ok {
			return nil, err
		}
		integ, err := liveIntegration(ctx, r, org, p.IntegrationID)
		if err != nil || integ == nil || integ.Provider != integration.ProviderStripe {
			return nil, err
		}
		pc, err := s.open(ctx, integ)
		if err != nil {
			return nil, err
		}
		return &target{p: p, pc: pc}, nil
	})
	if err != nil || t == nil {
		return err
	}
	res, err := t.pc.gw.GetPayment(ctx, t.pc.conn, *t.p.ProviderPaymentID)
	if err != nil {
		if pe, ok := payment.AsProviderError(err); ok && (pe.Kind == payment.KindAuthentication || pe.Kind == payment.KindInvalidRequest || pe.Kind == payment.KindNotFound) {
			// cancel_abandoned_service.rb:58-65.
			slog.WarnContext(ctx, "payments: cannot read an abandoned payment", "organization_id", org.String(),
				"payment_id", t.p.ID.String(), "error", err)
			return nil
		}
		return wrap(err, "read abandoned payment %s", t.p.ID)
	}
	switch {
	case slices.Contains(stripeFailedStatuses, res.ProviderStatus):
	case res.ProviderStatus == payment.ProviderStatusRequiresAction && res.MethodDetails.Type == "card":
		if res, err = t.pc.gw.CancelPayment(ctx, t.pc.conn, *t.p.ProviderPaymentID); err != nil {
			return wrap(err, "cancel abandoned payment %s", t.p.ID)
		}
		if res.Status == "" {
			// A zero result: the intent was already final.
			return nil
		}
	default:
		return nil
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Payments.Get(ctx, org, t.p.ID)
		if err != nil {
			return wrap(err, "load payment %s", t.p.ID)
		}
		if res.ProviderStatus != "" {
			p.ProviderStatus = &res.ProviderStatus
		}
		if res.Status != "" {
			p.Status = res.Status
		}
		p.UpdatedAt = s.d.Now()
		if err := r.Payments.Update(ctx, p); err != nil {
			return wrap(err, "update payment %s", p.ID)
		}
		if p.Status != payment.StateFailed {
			return nil
		}
		return s.unlockInvoice(ctx, r, org, p.Payable.InvoiceID)
	})
}

// abandoned reports whether p is an invoice payment abandoned at the
// provider's redirect (cancel_abandoned_service.rb:68-83).
func (s *Service) abandoned(ctx context.Context, r port.Repos, p *payment.Payment) (bool, error) {
	if p.Kind != payment.KindProvider || p.Payable.Type != payment.PayableTypeInvoice || p.ProviderPaymentID == nil {
		return false, nil
	}
	if p.Status != payment.StateProcessing || p.ProviderStatus == nil || *p.ProviderStatus != payment.ProviderStatusRequiresAction {
		return false, nil
	}
	if redirectType(p.ProviderPaymentData) != "redirect_to_url" {
		return false, nil
	}
	from, to := payment.AbandonedRange(s.d.Now())
	if p.UpdatedAt.Before(from) || p.UpdatedAt.After(to) {
		return false, nil
	}
	inv, err := r.Invoices.Get(ctx, p.OrganizationID, p.Payable.InvoiceID)
	if isNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, wrap(err, "load invoice %s", p.Payable.InvoiceID)
	}
	if inv.PaymentStatus == core.PaymentStatusSucceeded || inv.Status == invoice.StatusVoided || inv.Status == invoice.StatusClosed {
		return false, nil
	}
	_, gated, err := invoiceGating(ctx, r, inv)
	if err != nil {
		return false, err
	}
	return !gated, nil
}

// redirectType returns the "type" of a payment's provider data.
func redirectType(data json.RawMessage) string {
	var v struct {
		Type string `json:"type"`
	}
	if len(data) == 0 || json.Unmarshal(data, &v) != nil {
		return ""
	}
	return v.Type
}

// unlockInvoice makes the invoice id ready for payment processing again
// unless it was paid (cancel_abandoned_service.rb:85-89).
func (s *Service) unlockInvoice(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) error {
	if err := kernel.LockInvoice(ctx, r, org, id); err != nil {
		return err
	}
	inv, err := r.Invoices.Get(ctx, org, id)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return wrap(err, "load invoice %s", id)
	}
	if inv.PaymentStatus == core.PaymentStatusSucceeded || inv.ReadyForPaymentProcessing {
		return nil
	}
	if inv.Status == invoice.StatusVoided {
		return nil
	}
	inv.ReadyForPaymentProcessing = true
	inv.UpdatedAt = s.d.Now()
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return wrap(err, "unlock invoice %s", id)
	}
	return nil
}
