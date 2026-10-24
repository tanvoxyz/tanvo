package payments

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CancelPayment cancels the payment args.PaymentID at its provider once its
// payable no longer needs it, as when a payment-gated subscription's
// activation is canceled or expires (lago PaymentProviders::CancelPaymentService,
// app/services/payment_providers/cancel_payment_service.rb):
//
//   - a payment without provider payment, connection or adapter, or one
//     already succeeded, is left as it is (cancel_payment_service.rb:15-17);
//   - a vendor without cancellation (Cashfree, Flutterwave, MoneyHash)
//     answers [payment.ErrUnsupported] and the payment is left to its
//     webhooks (cancel_payment_service.rb:26-33);
//   - a synchronous cancel (Stripe, GoCardless) records the new state; an
//     asynchronous one (Adyen) or a payment no longer cancelable answers a
//     zero result and the payment keeps its state until the webhook
//     (stripe/payments/cancel_service.rb:27-41, adyen/payments/cancel_service.rb:20-46).
//
// Only the payment is updated: its payable was closed by the caller.
func (s *Service) CancelPayment(ctx context.Context, org core.OrganizationID, args kernel.PaymentArgs) error {
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
		if p.Kind != payment.KindProvider || p.ProviderPaymentID == nil || *p.ProviderPaymentID == "" ||
			p.IntegrationID.IsZero() || p.Status == payment.StateSucceeded {
			return nil, nil
		}
		integ, err := liveIntegration(ctx, r, org, p.IntegrationID)
		if err != nil || integ == nil {
			return nil, err
		}
		pc, err := s.open(ctx, integ)
		if errors.Is(err, ErrNoAdapter) || errors.Is(err, ErrNoProviders) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &target{p: p, pc: pc}, nil
	})
	if err != nil || t == nil {
		return err
	}
	res, err := t.pc.gw.CancelPayment(ctx, t.pc.conn, *t.p.ProviderPaymentID)
	if errors.Is(err, payment.ErrUnsupported) {
		s.d.Logger.InfoContext(ctx, "payments: provider cancellation not supported", "organization_id", org.String(),
			"payment_id", t.p.ID.String(), "provider", string(t.pc.integ.Provider))
		return nil
	}
	if err != nil {
		return wrap(err, "cancel payment %s", t.p.ID)
	}
	if res.Status == "" && res.ProviderStatus == "" {
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
		return nil
	})
}
