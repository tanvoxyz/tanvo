package payment

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Method is a customer's payment method at a provider connection (domain
// §15.5). At most one live method per customer is the default.
type Method struct {
	// ID identifies the payment method.
	ID core.PaymentMethodID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CustomerID is the owning customer.
	CustomerID core.CustomerID
	// IntegrationID is the provider connection.
	IntegrationID core.IntegrationID
	// PaymentProviderCustomerID is the customer's connection to it.
	PaymentProviderCustomerID core.PaymentProviderCustomerID
	// ProviderMethodID is the provider's identifier of the method; required.
	ProviderMethodID string
	// Details describe the method (type, brand, last 4, expiry).
	Details MethodDetails
	// IsDefault marks the customer's default method.
	IsDefault bool
	// DeletedAt is set once deleted (soft delete).
	DeletedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the payment method.
func (m *Method) Validate() error {
	var v core.Validator
	v.Check(!m.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	v.String("provider_method_id", m.ProviderMethodID, 255)
	if m.Details.ExpMonth != 0 {
		v.IntRange("details.exp_month", m.Details.ExpMonth, 1, 12)
	}
	return v.Err()
}

// Deleted reports whether the method was deleted.
func (m *Method) Deleted() bool { return m.DeletedAt != nil }

// Delete soft-deletes the method at now; it stops being the default.
func (m *Method) Delete(now time.Time) error {
	if m.Deleted() {
		return &core.TransitionError{Entity: "payment_method", From: "deleted", To: "deleted"}
	}
	m.DeletedAt = &now
	m.IsDefault = false
	m.UpdatedAt = now
	return nil
}

// ProviderCustomer is a customer's identity at one provider connection
// (domain §15.5); one live row per (connection, customer).
type ProviderCustomer struct {
	// ID identifies the provider customer.
	ID core.PaymentProviderCustomerID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CustomerID is the customer.
	CustomerID core.CustomerID
	// IntegrationID is the provider connection.
	IntegrationID core.IntegrationID
	// ProviderCustomerID is the provider's customer identifier, once known.
	ProviderCustomerID *string
	// SyncWithProvider creates the customer at the provider when missing.
	SyncWithProvider bool
	// ProviderPaymentMethods are the provider method types allowed for the
	// customer (validated by the provider adapter).
	ProviderPaymentMethods []string
	// DeletedAt is set once deleted (soft delete).
	DeletedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the provider customer.
func (c *ProviderCustomer) Validate() error {
	var v core.Validator
	v.Check(!c.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	v.Check(!c.IntegrationID.IsZero(), "integration_id", core.CodeRequired)
	if c.ProviderCustomerID != nil {
		v.String("provider_customer_id", *c.ProviderCustomerID, 255)
	}
	return v.Err()
}

// Deleted reports whether the provider customer was deleted.
func (c *ProviderCustomer) Deleted() bool { return c.DeletedAt != nil }

// MethodLevel is one level of the payment method resolution order: the
// payment method type and method chosen on an object (request,
// subscription, wallet transaction, recurring rule, wallet).
type MethodLevel struct {
	// Type is the level's payment method type; "" when the level does not
	// choose one.
	Type core.PaymentMethodType
	// MethodID is the level's payment method; zero when none.
	MethodID core.PaymentMethodID
}

// ResolvePaymentMethod returns the payment method that collects a payable
// (domain §15.5): levels are given in order (explicit request →
// subscription → wallet transaction → recurring rule → wallet), and the
// first level that is manual stops automatic charging (automatic = false)
// while the first that names a method wins. Otherwise the customer's
// default (possibly zero) is used.
func ResolvePaymentMethod(customerDefault core.PaymentMethodID, levels ...MethodLevel) (method core.PaymentMethodID, automatic bool) {
	for _, l := range levels {
		if l.Type == core.PaymentMethodTypeManual {
			return core.PaymentMethodID{}, false
		}
		if !l.MethodID.IsZero() {
			return l.MethodID, true
		}
	}
	return customerDefault, true
}

// ValidateMethodChoice checks a payment method chosen in a request: a
// provider type with or without a method found for the customer, a manual
// type without a method, or nothing. found reports whether the method ID
// names one of the customer's methods.
func ValidateMethodChoice(t core.PaymentMethodType, id core.PaymentMethodID, found bool) error {
	var v core.Validator
	switch {
	case t == "" && id.IsZero():
	case t == core.PaymentMethodTypeProvider && (id.IsZero() || found):
	case t == core.PaymentMethodTypeManual && id.IsZero():
	default:
		v.Add("payment_method", CodeInvalidPaymentMethod)
	}
	return v.Err()
}

// ConnectionLevel is one level of the connection resolution order: an
// object's connection mode and, for specific, its connection.
type ConnectionLevel struct {
	// Mode is how the level selects its connection; "" means inherit.
	Mode ConnectionMode
	// IntegrationID is the connection of a specific level.
	IntegrationID core.IntegrationID
}

// ResolveConnection returns the connection in effect for an object (domain
// §15.5): the first level that is specific gives its connection, the first
// that is skip gives none (zero), and inherit levels defer to the next;
// with no decision the customer's default connection applies.
func ResolveConnection(customerDefault core.IntegrationID, levels ...ConnectionLevel) core.IntegrationID {
	for _, l := range levels {
		switch l.Mode {
		case ConnectionModeSpecific:
			return l.IntegrationID
		case ConnectionModeSkip:
			return core.IntegrationID{}
		}
	}
	return customerDefault
}
