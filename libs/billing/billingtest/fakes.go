package billingtest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// ErrFake is the error the fakes return when told to fail.
var ErrFake = errors.New("billingtest: fake failure")

// TaxProvider is a fake [kernel.TaxProvider]: it taxes the customers in
// Customers with Compute (ComputeTaxes fails when Compute is unset).
type TaxProvider struct {
	mu sync.Mutex
	// Customers are the customers the provider taxes; nil taxes nobody.
	Customers map[core.CustomerID]bool
	// Fail makes ComputeTaxes fail.
	Fail bool
	// Compute replaces the default computation.
	Compute func(req kernel.TaxRequest) ([]tax.ProviderTaxResult, error)
	// Requests records the requests received.
	Requests []kernel.TaxRequest
	// Voided records the voided invoices.
	Voided []core.InvoiceID
	// Reported records the reported credit notes.
	Reported []core.CreditNoteID
}

var _ kernel.TaxProvider = (*TaxProvider)(nil)

// Enabled implements [kernel.TaxProvider].
func (p *TaxProvider) Enabled(_ context.Context, c *customer.Customer) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Customers[c.ID], nil
}

// ComputeTaxes implements [kernel.TaxProvider].
func (p *TaxProvider) ComputeTaxes(_ context.Context, req kernel.TaxRequest) ([]tax.ProviderTaxResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Requests = append(p.Requests, req)
	if p.Fail {
		return nil, ErrFake
	}
	if p.Compute != nil {
		return p.Compute(req)
	}
	return nil, ErrFake
}

// VoidInvoice implements [kernel.TaxProvider].
func (p *TaxProvider) VoidInvoice(_ context.Context, _ *customer.Customer, inv *invoice.Invoice) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Fail {
		return ErrFake
	}
	p.Voided = append(p.Voided, inv.ID)
	return nil
}

// ReportCreditNote implements [kernel.TaxProvider].
func (p *TaxProvider) ReportCreditNote(_ context.Context, _ *customer.Customer, _ *invoice.Invoice, cn *creditnote.CreditNote) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Reported = append(p.Reported, cn.ID)
	return nil
}

// RefundGateway is a fake [kernel.RefundGateway] that succeeds (status
// pending) unless Fail is set.
type RefundGateway struct {
	mu sync.Mutex
	// Fail makes Refund fail.
	Fail bool
	// Status is the returned status; zero means pending.
	Status core.RefundStatus
	// Requests records the refunds asked.
	Requests []kernel.RefundRequest
}

var _ kernel.RefundGateway = (*RefundGateway)(nil)

// Refund implements [kernel.RefundGateway].
func (g *RefundGateway) Refund(_ context.Context, req kernel.RefundRequest) (kernel.RefundResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Requests = append(g.Requests, req)
	if req.IdempotencyKey == "" {
		return kernel.RefundResult{}, fmt.Errorf("refund without idempotency key: %w", ErrFake)
	}
	if g.Fail {
		return kernel.RefundResult{}, ErrFake
	}
	st := g.Status
	if st == "" {
		st = core.RefundStatusPending
	}
	return kernel.RefundResult{ProviderRefundID: "re_fake", Status: st}, nil
}
