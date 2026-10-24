package payments

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// SetPaymentMethod records the method a succeeded provider payment was
// made with, then issues its receipt (lago
// Payments::SetPaymentMethodAndCreateReceiptJob,
// app/jobs/payments/set_payment_method_and_create_receipt_job.rb, and
// Payments::SetPaymentMethodDataService,
// app/services/payments/set_payment_method_data_service.rb):
//
//   - the customer's method with the provider's identifier becomes the
//     payment's method;
//   - when the payment does not record that provider method yet, its
//     details are fetched from the provider and stored on the payment and
//     on its method; a provider unable to describe methods
//     ([payment.ErrUnsupported]) leaves them as they are;
//   - the receipt of the payment is issued.
//
// A removed payment is ignored.
func (s *Service) SetPaymentMethod(ctx context.Context, org core.OrganizationID, args kernel.PaymentMethodArgs) error {
	if s.providers == nil {
		return ErrNoProviders
	}
	type step struct {
		pc    providerConn
		fetch bool
	}
	st, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*step, error) {
		p, err := r.Payments.Get(ctx, org, args.PaymentID)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, wrap(err, "load payment %s", args.PaymentID)
		}
		if err := s.linkMethod(ctx, r, p, args.ProviderMethodID); err != nil {
			return nil, err
		}
		fetch := args.ProviderMethodID != "" && (p.ProviderPaymentMethodID == nil || *p.ProviderPaymentMethodID != args.ProviderMethodID)
		if !fetch || p.IntegrationID.IsZero() {
			_, err := s.createReceipt(ctx, r, p)
			return nil, err
		}
		integ, err := r.Integrations.Get(ctx, org, p.IntegrationID)
		if isNotFound(err) {
			_, err := s.createReceipt(ctx, r, p)
			return nil, err
		}
		if err != nil {
			return nil, wrap(err, "load connection %s", p.IntegrationID)
		}
		pc, err := s.open(ctx, integ)
		if err != nil {
			return nil, err
		}
		return &step{pc: pc, fetch: true}, nil
	})
	if err != nil || st == nil {
		return err
	}
	pm, ferr := st.pc.gw.PaymentMethod(ctx, st.pc.conn, args.ProviderMethodID)
	switch {
	case errors.Is(ferr, payment.ErrUnsupported):
	case ferr != nil:
		return wrap(ferr, "fetch payment method %s", args.ProviderMethodID)
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Payments.Get(ctx, org, args.PaymentID)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return wrap(err, "load payment %s", args.PaymentID)
		}
		if ferr == nil {
			id := args.ProviderMethodID
			p.ProviderPaymentMethodID = &id
			p.ProviderPaymentMethod = pm.Details
			p.UpdatedAt = s.d.Now()
			if err := r.Payments.Update(ctx, p); err != nil {
				return wrap(err, "update payment %s", p.ID)
			}
			if !p.PaymentMethodID.IsZero() {
				m, err := r.PaymentMethods.Get(ctx, org, p.PaymentMethodID)
				switch {
				case isNotFound(err):
				case err != nil:
					return wrap(err, "load payment method %s", p.PaymentMethodID)
				default:
					m.Details = pm.Details
					m.UpdatedAt = s.d.Now()
					if err := r.PaymentMethods.Update(ctx, m); err != nil {
						return wrap(err, "update payment method %s", m.ID)
					}
				}
			}
		}
		_, err = s.createReceipt(ctx, r, p)
		return err
	})
}

// linkMethod sets on p the customer's live method with the provider's
// identifier id, when there is one
// (set_payment_method_and_create_receipt_job.rb:27-33).
func (s *Service) linkMethod(ctx context.Context, r port.Repos, p *payment.Payment, id string) error {
	if id == "" || p.IntegrationID.IsZero() {
		return nil
	}
	m, err := r.PaymentMethods.GetByProviderMethodID(ctx, p.OrganizationID, p.IntegrationID, id)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return wrap(err, "load payment method %s", id)
	}
	if m.CustomerID != p.CustomerID || m.ID == p.PaymentMethodID {
		return nil
	}
	p.PaymentMethodID = m.ID
	p.UpdatedAt = s.d.Now()
	if err := r.Payments.Update(ctx, p); err != nil {
		return wrap(err, "update payment %s", p.ID)
	}
	return nil
}
