package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
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
	blobs     port.BlobStore
	funding   FundingFormatter
}

// New returns the service over the checked dependencies d.
func New(d *kernel.Deps) *Service { return &Service{d: d} }

// WithCreditInvoices sets the handler of paid credit invoices; without
// one, paying a credit invoice leaves its wallet purchase pending.
func (s *Service) WithCreditInvoices(h CreditInvoiceHandler) *Service {
	s.credits = h
	return s
}

// WithBlobs sets the blob store holding the organization logos, which
// the hosted checkouts show; without one they show none.
func (s *Service) WithBlobs(b port.BlobStore) *Service {
	s.blobs = b
	return s
}

// logoURL returns the permanent link of the logo of o, empty when it has
// none (lago Organization#logo_url, app/models/organization.rb:234-238).
func (s *Service) logoURL(ctx context.Context, o *organization.Organization) (string, error) {
	if s.blobs == nil || o.Logo.IsZero() {
		return "", nil
	}
	u, err := s.blobs.URL(ctx, o.ID, o.Logo)
	if errors.Is(err, core.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("link logo of organization %s: %w", o.ID, err)
	}
	return u, nil
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
