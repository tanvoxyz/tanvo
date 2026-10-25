package payments

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CreateReceipt exposes createReceipt to the tests.
func (s *Service) CreateReceipt(ctx context.Context, p *payment.Payment) (*payment.Receipt, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Receipt, error) {
		return s.createReceipt(ctx, r, p)
	})
}

// FindOrCreateMethod exposes findOrCreateMethod to the tests.
func (s *Service) FindOrCreateMethod(ctx context.Context, pcust *payment.ProviderCustomer, id string, details payment.MethodDetails, setDefault bool) (*payment.Method, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Method, error) {
		return s.findOrCreateMethod(ctx, r, pcust, id, details, setDefault)
	})
}

// UpdateMethodDetails exposes updateMethodDetails to the tests.
func (s *Service) UpdateMethodDetails(ctx context.Context, pcust *payment.ProviderCustomer, id string, details payment.MethodDetails) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return s.updateMethodDetails(ctx, r, pcust, id, details)
	})
}
