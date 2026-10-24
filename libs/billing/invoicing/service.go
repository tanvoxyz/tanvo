package invoicing

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// CreditInvoiceHandler reacts to the voiding of a credit invoice (a wallet
// purchase); libs/billing/wallets implements it.
type CreditInvoiceHandler interface {
	// HandleCreditInvoiceVoided fails the pending purchase of the voided
	// credit invoice id.
	HandleCreditInvoiceVoided(ctx context.Context, org core.OrganizationID, id core.InvoiceID) error
}

// Service is the invoicing service.
type Service struct {
	d              *kernel.Deps
	fees           *fees.Service
	deduct         *deduct.Service
	creditNotes    *creditnotes.Service
	creditInvoices CreditInvoiceHandler
	statuses       StatusResolver
	// contractSegments resolves the in-advance segments of contracts;
	// nil bills no contract event in advance.
	contractSegments ContractSegments
}

// New returns the service over the checked dependencies d, with its own
// fees, deduct and credit notes services.
func New(d *kernel.Deps) *Service {
	return &Service{d: d, fees: fees.New(d), deduct: deduct.New(d), creditNotes: creditnotes.New(d)}
}

// WithCreditNotes makes the service use cn (the engine's, wired with its
// wallet voider) for credit notes, and returns it.
func (s *Service) WithCreditNotes(cn *creditnotes.Service) *Service {
	s.creditNotes = cn
	return s
}

// WithCreditInvoices sets the handler of voided credit invoices, and
// returns the service.
func (s *Service) WithCreditInvoices(h CreditInvoiceHandler) *Service {
	s.creditInvoices = h
	return s
}
