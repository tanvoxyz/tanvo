package payments

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CreditInvoiceHandler settles the wallet purchase of a credit invoice
// whose payment status became succeeded or failed; libs/billing/wallets
// implements it.
type CreditInvoiceHandler interface {
	// HandleCreditInvoicePayment settles (succeeded) or fails the purchase
	// of the credit invoice id, in its own transaction.
	HandleCreditInvoicePayment(ctx context.Context, org core.OrganizationID, id core.InvoiceID, succeeded bool) error
}

// Service is the payments service.
type Service struct {
	d         *kernel.Deps
	credits   CreditInvoiceHandler
	providers *Providers
	sync      PaymentSync
	refunds   RefundStatuses
}

// New returns the service over the checked dependencies d.
func New(d *kernel.Deps) *Service { return &Service{d: d} }

// WithCreditInvoices sets the handler of paid credit invoices; without
// one, paying a credit invoice leaves its wallet purchase pending.
func (s *Service) WithCreditInvoices(h CreditInvoiceHandler) *Service {
	s.credits = h
	return s
}

// inTx runs fn in a transaction and returns its value.
func inTx[T any](ctx context.Context, tx port.Transactor, fn func(ctx context.Context, r port.Repos) (T, error)) (T, error) {
	var out T
	err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// notFound is the error of a missing resource.
func notFound(resource string) error { return core.NotFound(resource) }

// wrap annotates a repository error, keeping it as it is when it is a
// not-found error of resource.
func wrap(err error, format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, err)...)
}
