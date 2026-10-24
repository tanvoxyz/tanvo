package rpc

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// TestConnectionProtoProvider covers the provider of a customer's
// connection, lago IntegrationCustomerSerializer#type.
func TestConnectionProtoProvider(t *testing.T) {
	for _, tt := range []struct {
		source   string
		provider integration.Provider
	}{
		{"source: spec/serializers/v1/integration_customer_serializer_spec.rb:39", integration.ProviderAnrok},
		{"source: spec/serializers/v1/integration_customer_serializer_spec.rb:47", integration.ProviderAvalara},
		{"source: spec/serializers/v1/integration_customer_serializer_spec.rb:55", integration.ProviderXero},
		{"source: spec/serializers/v1/integration_customer_serializer_spec.rb:63", integration.ProviderHubspot},
		{"source: spec/serializers/v1/integration_customer_serializer_spec.rb:71", integration.ProviderSalesforce},
	} {
		t.Run(string(tt.provider), func(t *testing.T) {
			got := connectionProto(customers.Connection{Integration: &integration.Integration{Provider: tt.provider}})
			if got.GetProvider() != string(tt.provider) {
				t.Errorf("provider: got = %q, want = %q", got.GetProvider(), tt.provider)
			}
		})
	}
}
