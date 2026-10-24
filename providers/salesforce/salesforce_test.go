package salesforce

import (
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

func TestValidateConnection(t *testing.T) {
	t.Log("source: spec/models/integrations/salesforce_integration_spec.rb:10")
	tests := []struct {
		name     string
		settings string
		want     error
	}{
		{"instance", `{"instance_id":"https://acme.my.salesforce.com"}`, nil},
		{"no instance", `{}`, core.ErrInvalidValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New().ValidateConnection(integration.Connection{Settings: []byte(tt.settings)})
			if !errors.Is(err, tt.want) && (err != nil || tt.want != nil) {
				t.Errorf("ValidateConnection() = %v, want = %v", err, tt.want)
			}
		})
	}
}
