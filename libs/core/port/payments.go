package port

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// PaymentProvider is a payment provider adapter (architecture §12); see
// [payment.Gateway].
type PaymentProvider = payment.Gateway

// PaymentProviders resolves the adapter of a connection's vendor.
type PaymentProviders interface {
	// PaymentProvider returns the adapter of p; an unknown or
	// unconfigured vendor is [core.ErrNotFound].
	PaymentProvider(p integration.Provider) (PaymentProvider, error)
}

// PaymentProviderSet is a [PaymentProviders] built once by apps/server
// from the configured adapters.
type PaymentProviderSet struct {
	byProvider map[integration.Provider]PaymentProvider
}

// NewPaymentProviderSet returns the set of gateways, keyed by
// [payment.ConnectionGateway.Provider]; a vendor given twice is an error.
func NewPaymentProviderSet(gateways ...PaymentProvider) (*PaymentProviderSet, error) {
	s := &PaymentProviderSet{byProvider: make(map[integration.Provider]PaymentProvider, len(gateways))}
	for _, g := range gateways {
		p := g.Provider()
		if c, ok := integration.CategoryOf(p); !ok || c != integration.CategoryPayment {
			return nil, fmt.Errorf("payment provider %q: %w", p, core.ErrInvalidValue)
		}
		if _, dup := s.byProvider[p]; dup {
			return nil, fmt.Errorf("payment provider %q registered twice: %w", p, core.ErrConflict)
		}
		s.byProvider[p] = g
	}
	return s, nil
}

// PaymentProvider implements [PaymentProviders].
func (s *PaymentProviderSet) PaymentProvider(p integration.Provider) (PaymentProvider, error) {
	if g, ok := s.byProvider[p]; ok {
		return g, nil
	}
	return nil, fmt.Errorf("payment provider %q: %w", p, core.ErrNotFound)
}
