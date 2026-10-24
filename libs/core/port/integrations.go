package port

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// IntegrationProviders resolves the tax, accounting and CRM adapters (and
// the OAuth grants of payment connections) by vendor (architecture §12).
type IntegrationProviders interface {
	// IntegrationGateway returns the adapter of p; an unknown or
	// unconfigured vendor is [core.ErrNotFound].
	IntegrationGateway(p integration.Provider) (integration.Gateway, error)
	// TaxGateway returns the tax adapter of p.
	TaxGateway(p integration.Provider) (integration.TaxGateway, error)
	// AccountingGateway returns the accounting adapter of p.
	AccountingGateway(p integration.Provider) (integration.AccountingGateway, error)
	// CRMGateway returns the CRM adapter of p; a CRM that receives no
	// records (Salesforce) wraps [integration.ErrUnsupported].
	CRMGateway(p integration.Provider) (integration.CRMGateway, error)
}

// IntegrationProviderSet is an [IntegrationProviders] built once by
// apps/server from the configured adapters.
type IntegrationProviderSet struct {
	byProvider map[integration.Provider]integration.Gateway
}

// NewIntegrationProviderSet returns the set of gateways keyed by
// [integration.Gateway.Provider]. A vendor given twice, a vendor outside
// the tax, accounting, CRM and payment categories, or an adapter lacking
// the port of its category (CRM adapters excepted) is an error. A payment
// vendor is registered only for its OAuth grant
// ([integration.OAuthGateway]); its payments go through the payment
// providers.
func NewIntegrationProviderSet(gateways ...integration.Gateway) (*IntegrationProviderSet, error) {
	s := &IntegrationProviderSet{byProvider: make(map[integration.Provider]integration.Gateway, len(gateways))}
	for _, g := range gateways {
		p := g.Provider()
		c, _ := integration.CategoryOf(p)
		var ok bool
		switch c {
		case integration.CategoryTax:
			_, ok = g.(integration.TaxGateway)
		case integration.CategoryAccounting:
			_, ok = g.(integration.AccountingGateway)
		case integration.CategoryCRM:
			ok = true
		case integration.CategoryPayment:
			_, ok = g.(integration.OAuthGateway)
		}
		if !ok {
			return nil, fmt.Errorf("integration provider %q: %w", p, core.ErrInvalidValue)
		}
		if _, dup := s.byProvider[p]; dup {
			return nil, fmt.Errorf("integration provider %q registered twice: %w", p, core.ErrConflict)
		}
		s.byProvider[p] = g
	}
	return s, nil
}

// IntegrationGateway implements [IntegrationProviders].
func (s *IntegrationProviderSet) IntegrationGateway(p integration.Provider) (integration.Gateway, error) {
	if g, ok := s.byProvider[p]; ok {
		return g, nil
	}
	return nil, fmt.Errorf("integration provider %q: %w", p, core.ErrNotFound)
}

// TaxGateway implements [IntegrationProviders].
func (s *IntegrationProviderSet) TaxGateway(p integration.Provider) (integration.TaxGateway, error) {
	return gatewayAs[integration.TaxGateway](s, p)
}

// AccountingGateway implements [IntegrationProviders].
func (s *IntegrationProviderSet) AccountingGateway(p integration.Provider) (integration.AccountingGateway, error) {
	return gatewayAs[integration.AccountingGateway](s, p)
}

// CRMGateway implements [IntegrationProviders].
func (s *IntegrationProviderSet) CRMGateway(p integration.Provider) (integration.CRMGateway, error) {
	return gatewayAs[integration.CRMGateway](s, p)
}

func gatewayAs[T integration.Gateway](s *IntegrationProviderSet, p integration.Provider) (T, error) {
	var zero T
	g, err := s.IntegrationGateway(p)
	if err != nil {
		return zero, err
	}
	t, ok := g.(T)
	if !ok {
		return zero, fmt.Errorf("integration provider %q: %w", p, integration.ErrUnsupported)
	}
	return t, nil
}
