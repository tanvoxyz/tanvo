package integrations_test

import (
	"encoding/json"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// TestErrorPayloads covers the JSON of the integration error webhooks.
// Tanvo's IDs drop lago's lago_ prefix.
func TestErrorPayloads(t *testing.T) {
	cust := core.MustParseID[core.CustomerID]("0199a000-0000-7000-8000-000000000001")
	integ := core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000002")
	perr := integrations.ProviderErrorDetails{Message: "message", ErrorCode: "code"}
	const errJSON = `"provider_error":{"message":"message","error_code":"code"}`
	for _, tt := range []struct {
		name    string
		payload any
		want    string
	}{
		{
			// source: spec/serializers/v1/integrations/customer_error_serializer_spec.rb:21
			name: "customer error",
			payload: integrations.CustomerErrorPayload{
				CustomerID: cust, ExternalCustomerID: "ext", AccountingProvider: integration.ProviderNetsuite,
				AccountingProviderCode: "netsuite", ProviderError: perr,
			},
			want: `{"customer_id":"` + cust.String() + `","external_customer_id":"ext","accounting_provider":"netsuite","accounting_provider_code":"netsuite",` + errJSON + `}`,
		},
		{
			// source: spec/serializers/v1/integrations/taxes/customer_error_serializer_spec.rb:21
			name: "tax customer error",
			payload: integrations.TaxProviderCustomerError{
				CustomerID: cust, ExternalCustomerID: "ext", TaxProvider: integration.ProviderAnrok,
				TaxProviderCode: "anrok", ProviderError: perr,
			},
			want: `{"customer_id":"` + cust.String() + `","external_customer_id":"ext","tax_provider":"anrok","tax_provider_code":"anrok",` + errJSON + `}`,
		},
		{
			// source: spec/serializers/v1/integrations/provider_error_serializer_spec.rb:21
			name: "provider error",
			payload: integrations.ProviderErrorPayload{
				IntegrationID: integ, Provider: integration.ProviderNetsuite, ProviderCode: "netsuite", ProviderError: perr,
			},
			want: `{"integration_id":"` + integ.String() + `","provider":"netsuite","provider_code":"netsuite",` + errJSON + `}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(b) != tt.want {
				t.Errorf("got = %s, want = %s", b, tt.want)
			}
		})
	}
}
