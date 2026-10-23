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
