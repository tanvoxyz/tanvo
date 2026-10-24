package port

import (
	"context"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

type fakeGateway struct{ p integration.Provider }

func (g fakeGateway) Provider() integration.Provider                { return g.p }
func (fakeGateway) ValidateConnection(integration.Connection) error { return nil }

type fakeTax struct{ fakeGateway }

func (fakeTax) QuoteTaxes(context.Context, integration.Connection, *integration.TaxTransaction) (*integration.TaxResult, error) {
	return nil, nil
}

func (fakeTax) ReportTaxes(context.Context, integration.Connection, *integration.TaxTransaction) (*integration.TaxResult, error) {
	return nil, nil
}
func (fakeTax) VoidTaxes(context.Context, integration.Connection, string) error { return nil }

type fakeOAuth struct{ fakeGateway }

func (fakeOAuth) AuthorizationURL(integration.Connection, string, string) (string, error) {
	return "", nil
}

func (fakeOAuth) ExchangeCode(context.Context, integration.Connection, string, string) (integration.ConnectionUpdate, error) {
	return integration.ConnectionUpdate{}, nil
}

func TestIntegrationProviderSet(t *testing.T) {
	anrok := fakeTax{fakeGateway{integration.ProviderAnrok}}
	salesforce := fakeGateway{integration.ProviderSalesforce}

	tests := []struct {
		name     string
		gateways []integration.Gateway
		want     error
	}{
		{"tax and crm", []integration.Gateway{anrok, salesforce}, nil},
		{"tax adapter without the tax port", []integration.Gateway{fakeGateway{integration.ProviderAvalara}}, core.ErrInvalidValue},
		{"payment vendor without oauth", []integration.Gateway{fakeGateway{integration.ProviderStripe}}, core.ErrInvalidValue},
		{"payment vendor with oauth", []integration.Gateway{fakeOAuth{fakeGateway{integration.ProviderGocardless}}}, nil},
		{"duplicate", []integration.Gateway{anrok, anrok}, core.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewIntegrationProviderSet(tt.gateways...)
			if !errors.Is(err, tt.want) && (err != nil || tt.want != nil) {
				t.Errorf("NewIntegrationProviderSet() error = %v, want = %v", err, tt.want)
			}
		})
	}

	s, err := NewIntegrationProviderSet(anrok, salesforce)
	if err != nil {
		t.Fatal(err)
	}
	if g, err := s.TaxGateway(integration.ProviderAnrok); err != nil || g.Provider() != integration.ProviderAnrok {
		t.Errorf("TaxGateway(anrok) = %v, %v, want = anrok", g, err)
	}
	if _, err := s.CRMGateway(integration.ProviderSalesforce); !errors.Is(err, integration.ErrUnsupported) {
		t.Errorf("CRMGateway(salesforce) error = %v, want = ErrUnsupported", err)
	}
	if _, err := s.AccountingGateway(integration.ProviderXero); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("AccountingGateway(xero) error = %v, want = ErrNotFound", err)
	}
}
