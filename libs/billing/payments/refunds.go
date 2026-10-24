package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Refunds returns the [kernel.RefundGateway] that sends credit-note
// refunds to the payment provider that collected the refunded payment
// (lago CreditNotes::Refunds::StripeService, GocardlessService and
// AdyenService, app/services/credit_notes/refunds/). It is the
// [kernel.Deps.Refunds] of the credit notes service.
func (s *Service) Refunds() kernel.RefundGateway { return refundGateway{s} }

type refundGateway struct{ s *Service }

// Refund refunds req.Amount of payment req.PaymentID through its
// provider:
//
//   - a manual payment, a payment of a removed or inactive connection, of
//     a vendor without an adapter ([ErrNoAdapter]) or without refunds
//     ([payment.ErrUnsupported]) are skipped
//     with [creditnotes.ErrRefundSkipped] (lago enqueues a refund only for
//     the payment's provider, credit_notes/create_service.rb:255-264);
//   - a refusal of the provider ([payment.ErrRejected]) wraps
//     [creditnotes.ErrRefundRejected], failing the refund
//     (stripe_service.rb:47-52, adyen_service.rb:100-105,
//     gocardless_service.rb:48-53);
//   - a refund whose provider status is unknown ([payment.ErrUnknownStatus])
//     is recorded pending with its provider identifier;
//   - any other error is returned for the job to retry with the same
//     idempotency key, the credit note's ID.
func (g refundGateway) Refund(ctx context.Context, req kernel.RefundRequest) (kernel.RefundResult, error) {
	s := g.s
	cn := req.CreditNote
	if cn == nil {
		return kernel.RefundResult{}, errors.New("payments: refund without a credit note")
	}
	pc, p, err := inTx2(ctx, s, func(ctx context.Context, r port.Repos) (*providerConn, *payment.Payment, error) {
		p, err := r.Payments.Get(ctx, cn.OrganizationID, req.PaymentID)
		if err != nil {
			return nil, nil, wrap(err, "load payment %s", req.PaymentID)
		}
		if p.Kind != payment.KindProvider || p.ProviderPaymentID == nil || *p.ProviderPaymentID == "" || p.IntegrationID.IsZero() {
			return nil, p, nil
		}
		integ, err := liveIntegration(ctx, r, cn.OrganizationID, p.IntegrationID)
		if err != nil || integ == nil {
			return nil, p, err
		}
		pc, err := s.open(ctx, integ)
		if errors.Is(err, ErrNoAdapter) {
			return nil, p, nil
		}
		if err != nil {
			return nil, p, err
		}
		return &pc, p, nil
	})
	if err != nil {
		return kernel.RefundResult{}, err
	}
	if pc == nil {
		return kernel.RefundResult{}, fmt.Errorf("payment %s: %w", req.PaymentID, creditnotes.ErrRefundSkipped)
	}
	params := payment.RefundParams{
		CreditNoteID: cn.ID, InvoiceID: cn.InvoiceID, CustomerID: cn.CustomerID,
		ProviderPaymentID: *p.ProviderPaymentID, Amount: req.Amount, Currency: req.Currency, Reason: cn.Reason,
	}
	res, err := pc.gw.Refund(ctx, pc.conn, params)
	switch {
	case err == nil:
		return kernel.RefundResult{ProviderRefundID: res.ProviderRefundID, Status: res.Status}, nil
	case errors.Is(err, payment.ErrUnsupported):
		return kernel.RefundResult{}, fmt.Errorf("payment %s: %w: %w", p.ID, creditnotes.ErrRefundSkipped, err)
	case errors.Is(err, payment.ErrUnknownStatus):
		return kernel.RefundResult{ProviderRefundID: res.ProviderRefundID}, nil
	case errors.Is(err, payment.ErrRejected):
		return kernel.RefundResult{}, fmt.Errorf("%w: %w", creditnotes.ErrRefundRejected, err)
	default:
		return kernel.RefundResult{}, fmt.Errorf("refund payment %s: %w", p.ID, err)
	}
}

// inTx2 runs f in a transaction of s and returns its two results.
func inTx2[A, B any](ctx context.Context, s *Service, f func(context.Context, port.Repos) (A, B, error)) (A, B, error) {
	var a A
	var b B
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		a, b, err = f(ctx, r)
		return err
	})
	return a, b, err
}
