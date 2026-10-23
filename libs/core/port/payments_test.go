package port

import (
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// fakeProvider is a gateway that only names its vendor.
type fakeProvider struct {
	PaymentProvider
	p integration.Provider
}

func (f fakeProvider) Provider() integration.Provider { return f.p }

func TestPaymentProviderSet(t *testing.T) {
	stripe := fakeProvider{p: integration.ProviderStripe}
	tests := []struct {
		name     string
		gateways []PaymentProvider
		lookup   integration.Provider
		wantNew  error
		wantGet  error
	}{
		{name: "found", gateways: []PaymentProvider{stripe}, lookup: integration.ProviderStripe},
		{name: "unknown", gateways: []PaymentProvider{stripe}, lookup: integration.ProviderAdyen, wantGet: core.ErrNotFound},
		{name: "duplicate", gateways: []PaymentProvider{stripe, stripe}, wantNew: core.ErrConflict},
		{name: "not a payment vendor", gateways: []PaymentProvider{fakeProvider{p: integration.ProviderXero}}, wantNew: core.ErrInvalidValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewPaymentProviderSet(tt.gateways...)
			if !errors.Is(err, tt.wantNew) || (tt.wantNew == nil && err != nil) {
				t.Fatalf("new: got = %v, want = %v", err, tt.wantNew)
			}
			if err != nil {
				return
			}
			g, err := s.PaymentProvider(tt.lookup)
			if !errors.Is(err, tt.wantGet) || (tt.wantGet == nil && err != nil) {
				t.Fatalf("get: got = %v, want = %v", err, tt.wantGet)
			}
			if err == nil && g.Provider() != tt.lookup {
				t.Errorf("provider: got = %v, want = %v", g.Provider(), tt.lookup)
			}
		})
	}
}
