package payments

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// UpdatePaymentReference sets the invoice number on the provider payment
// that paid the invoice args.InvoiceID before it was numbered: a
// payment-gated subscription invoice is charged with its ID as reference
// and finalized once paid (lago PaymentProviders::UpdatePaymentReferenceService,
// app/services/payment_providers/update_payment_reference_service.rb).
//
// The newest succeeded payment of the invoice is updated; an invoice
// without number, a payment without provider payment, connection or
// adapter is skipped. The update is best effort: a provider failure is
// logged and the job succeeds (stripe/payments/update_reference_service.rb:30-40);
// adapters without reference updates answer [payment.ErrUnsupported]
// (update_payment_reference_service.rb:20-33).
func (s *Service) UpdatePaymentReference(ctx context.Context, org core.OrganizationID, args kernel.InvoiceArgs) error {
	type target struct {
		pc     providerConn
		p      *payment.Payment
		number string
	}
	t, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*target, error) {
		inv, err := r.Invoices.Get(ctx, org, args.InvoiceID)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, wrap(err, "load invoice %s", args.InvoiceID)
		}
		if inv.Number == "" {
			return nil, nil
		}
		ps, err := r.Payments.ListByPayable(ctx, org, payment.InvoicePayable(inv.ID))
		if err != nil {
			return nil, wrap(err, "list payments of invoice %s", inv.ID)
		}
		// resolve_service.rb:88-93: the newest succeeded payment.
		var p *payment.Payment
		for _, q := range ps {
			if q.Status == payment.StateSucceeded && (p == nil || q.CreatedAt.After(p.CreatedAt)) {
				p = q
			}
		}
		if p == nil || p.Kind != payment.KindProvider || p.ProviderPaymentID == nil || *p.ProviderPaymentID == "" || p.IntegrationID.IsZero() {
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
		return &target{pc: pc, p: p, number: inv.Number}, nil
	})
	if err != nil || t == nil {
		return err
	}
	err = t.pc.gw.UpdatePaymentReference(ctx, t.pc.conn, *t.p.ProviderPaymentID, t.number)
	switch {
	case err == nil, errors.Is(err, payment.ErrUnsupported):
		return nil
	case isProviderError(err):
		s.d.Logger.WarnContext(ctx, "payments: update provider payment reference", "organization_id", org.String(),
			"payment_id", t.p.ID.String(), "error", err)
		return nil
	default:
		return wrap(err, "update reference of payment %s", t.p.ID)
	}
}

// isProviderError reports whether err comes from the provider.
func isProviderError(err error) bool {
	_, ok := payment.AsProviderError(err)
	return ok
}
