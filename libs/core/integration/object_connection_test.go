package integration

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestObjectConnectionValidate(t *testing.T) {
	sub := ObjectOwner{SubscriptionID: core.MustParseID[core.SubscriptionID](uuid.NewString())}
	pc := core.MustParseID[core.PaymentProviderCustomerID](uuid.NewString())
	ic := core.MustParseID[core.IntegrationCustomerID](uuid.NewString())
	for _, tt := range []struct {
		name   string
		source string
		c      ObjectConnection
		want   []string
	}{
		{"specific payment", "spec/models/billing_object_connection_spec.rb:9", ObjectConnection{Owner: sub, Category: CategoryPayment, Behavior: ConnectionBehaviorSpecific, PaymentProviderCustomerID: pc}, []string{}},
		{"specific tax", "spec/models/billing_object_connection_spec.rb:9", ObjectConnection{Owner: sub, Category: CategoryTax, Behavior: ConnectionBehaviorSpecific, IntegrationCustomerID: ic}, []string{}},
		{"skip accounting", "spec/models/billing_object_connection_spec.rb:9", ObjectConnection{Owner: sub, Category: CategoryAccounting, Behavior: ConnectionBehaviorSkip}, []string{}},
		{"skip crm", "spec/models/billing_object_connection_spec.rb:9", ObjectConnection{Owner: sub, Category: CategoryCRM, Behavior: ConnectionBehaviorSkip}, []string{}},
		{"unknown category", "spec/models/billing_object_connection_spec.rb:9", ObjectConnection{Owner: sub, Category: "shipping", Behavior: ConnectionBehaviorSkip}, []string{"category: invalid_value"}},
		{"auth category", "", ObjectConnection{Owner: sub, Category: CategoryAuth, Behavior: ConnectionBehaviorSkip}, []string{"category: invalid_value"}},
		{"inherit is stored as no row", "spec/models/billing_object_connection_spec.rb:9", ObjectConnection{Owner: sub, Category: CategoryTax, Behavior: ConnectionBehaviorInherit}, []string{"behavior: invalid_value"}},
		{"no behavior", "", ObjectConnection{Owner: sub, Category: CategoryTax}, []string{"behavior: value_is_mandatory"}},
		{"no owner", "", ObjectConnection{Category: CategoryTax, Behavior: ConnectionBehaviorSkip}, []string{"owner: invalid_value"}},
		{"two owners", "", ObjectConnection{Owner: ObjectOwner{SubscriptionID: sub.SubscriptionID, WalletID: core.MustParseID[core.WalletID](uuid.NewString())}, Category: CategoryTax, Behavior: ConnectionBehaviorSkip}, []string{"owner: invalid_value"}},
		{"specific without a connection", "", ObjectConnection{Owner: sub, Category: CategoryPayment, Behavior: ConnectionBehaviorSpecific}, []string{"payment_provider_customer_id: invalid_value"}},
		{"skip with a connection", "", ObjectConnection{Owner: sub, Category: CategoryTax, Behavior: ConnectionBehaviorSkip, IntegrationCustomerID: ic}, []string{"integration_customer_id: invalid_value"}},
		{"payment with an integration customer", "", ObjectConnection{Owner: sub, Category: CategoryPayment, Behavior: ConnectionBehaviorSpecific, PaymentProviderCustomerID: pc, IntegrationCustomerID: ic}, []string{"integration_customer_id: invalid_value"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Log("source: " + tt.source)
			}
			if got := fieldErrors(tt.c.Validate()); !slices.Equal(got, tt.want) {
				t.Errorf("Validate() got = %v, want = %v", got, tt.want)
			}
		})
	}
}
