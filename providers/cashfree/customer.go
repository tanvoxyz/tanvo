package cashfree

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// DefaultMethodTypes implements [payment.CustomerGateway]: the payment
// link page decides the methods offered.
func (g *Gateway) DefaultMethodTypes() []string { return nil }

// ValidateMethodTypes implements [payment.CustomerGateway]; any list is
// accepted and ignored.
func (g *Gateway) ValidateMethodTypes([]string) error { return nil }

// CreateCustomer implements [payment.CustomerGateway]. Cashfree payment
// links keep no customer: lago only saves the local provider customer
// (payment_providers/cashfree/customers/create_service.rb), so it is
// [payment.ErrUnsupported].
func (g *Gateway) CreateCustomer(context.Context, payment.Connection, payment.CustomerParams) (string, error) {
	return "", fmt.Errorf("cashfree create customer: %w", payment.ErrUnsupported)
}

// UpdateCustomer implements [payment.CustomerGateway]; see CreateCustomer
// (lago payment_provider_customers/cashfree_service.rb update).
func (g *Gateway) UpdateCustomer(context.Context, payment.Connection, string, payment.CustomerParams) error {
	return fmt.Errorf("cashfree update customer: %w", payment.ErrUnsupported)
}

// SetupURL implements [payment.MethodGateway]. Cashfree saves no method
// for later charges: lago answers feature_not_supported
// (payment_provider_customers/cashfree_service.rb generate_checkout_url);
// [payment.ErrUnsupported].
func (g *Gateway) SetupURL(context.Context, payment.Connection, payment.SetupParams) (string, error) {
	return "", fmt.Errorf("cashfree setup url: %w", payment.ErrUnsupported)
}

// ListPaymentMethods implements [payment.MethodGateway];
// [payment.ErrUnsupported] (see SetupURL).
func (g *Gateway) ListPaymentMethods(context.Context, payment.Connection, string) ([]payment.ProviderMethod, error) {
	return nil, fmt.Errorf("cashfree list payment methods: %w", payment.ErrUnsupported)
}

// DefaultPaymentMethod implements [payment.MethodGateway];
// [payment.ErrUnsupported] (see SetupURL).
func (g *Gateway) DefaultPaymentMethod(context.Context, payment.Connection, string) (string, error) {
	return "", fmt.Errorf("cashfree default payment method: %w", payment.ErrUnsupported)
}

// PaymentMethod implements [payment.MethodGateway]; [payment.ErrUnsupported]
// (see SetupURL).
func (g *Gateway) PaymentMethod(context.Context, payment.Connection, string) (payment.ProviderMethod, error) {
	return payment.ProviderMethod{}, fmt.Errorf("cashfree payment method: %w", payment.ErrUnsupported)
}

// SetDefaultPaymentMethod implements [payment.MethodGateway];
// [payment.ErrUnsupported] (see SetupURL).
func (g *Gateway) SetDefaultPaymentMethod(context.Context, payment.Connection, string, string) error {
	return fmt.Errorf("cashfree set default payment method: %w", payment.ErrUnsupported)
}

// DetachPaymentMethod implements [payment.MethodGateway];
// [payment.ErrUnsupported] (see SetupURL).
func (g *Gateway) DetachPaymentMethod(context.Context, payment.Connection, string) error {
	return fmt.Errorf("cashfree detach payment method: %w", payment.ErrUnsupported)
}
