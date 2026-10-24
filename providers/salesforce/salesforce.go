package salesforce

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// Provider is the vendor this adapter serves.
const Provider = integration.ProviderSalesforce

// KeyInstanceID is the setting of the Salesforce org's instance ID.
const KeyInstanceID = "instance_id"

// Gateway is the Salesforce [integration.Gateway].
type Gateway struct{}

var _ integration.Gateway = Gateway{}

// New returns a Salesforce gateway.
func New() Gateway { return Gateway{} }

// Provider implements [integration.Gateway].
func (Gateway) Provider() integration.Provider { return Provider }

// ValidateConnection implements [integration.Gateway]: a connection names
// its org's instance (lago salesforce_integration.rb:5).
func (Gateway) ValidateConnection(conn integration.Connection) error {
	if conn.Setting(KeyInstanceID) == "" {
		return fmt.Errorf("salesforce: %s: %w", KeyInstanceID, core.ErrInvalidValue)
	}
	return nil
}
