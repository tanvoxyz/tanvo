package integration

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// ConnectionBehavior is how a billing object picks its connection of a
// category (domain §15.5).
type ConnectionBehavior string

// ConnectionBehavior values. Only specific and skip are stored: inherit is
// the absence of an [ObjectConnection], named in requests and routings.
const (
	// ConnectionBehaviorSpecific is "specific": a chosen connection of the
	// customer.
	ConnectionBehaviorSpecific ConnectionBehavior = "specific"
	// ConnectionBehaviorSkip is "skip": no connection of the category.
	ConnectionBehaviorSkip ConnectionBehavior = "skip"
	// ConnectionBehaviorInherit is "inherit": the customer's default
	// connection of the category.
	ConnectionBehaviorInherit ConnectionBehavior = "inherit"
)

// ConnectionBehaviorValues returns every ConnectionBehavior, in declaration
// order.
func ConnectionBehaviorValues() []ConnectionBehavior {
	return []ConnectionBehavior{ConnectionBehaviorSpecific, ConnectionBehaviorSkip, ConnectionBehaviorInherit}
}

// Valid reports whether v is one of the declared ConnectionBehavior values.
func (v ConnectionBehavior) Valid() bool {
	switch v {
	case ConnectionBehaviorSpecific, ConnectionBehaviorSkip, ConnectionBehaviorInherit:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ConnectionBehavior) String() string { return string(v) }

// ParseConnectionBehavior parses the stored form s, failing with
// [core.ErrInvalidValue] for anything but a declared value.
func ParseConnectionBehavior(s string) (ConnectionBehavior, error) {
	if v := ConnectionBehavior(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("integration.ConnectionBehavior", s)
}

// RoutedCategories are the categories a billing object chooses a
// connection of, in routing order.
//
// source: app/models/billing_object_connection.rb:4-9
func RoutedCategories() []Category {
	return []Category{CategoryPayment, CategoryTax, CategoryAccounting, CategoryCRM}
}

// ObjectOwner is the billing object of an [ObjectConnection]: exactly one
// of its IDs is set.
type ObjectOwner struct {
	// SubscriptionID is a subscription.
	SubscriptionID core.SubscriptionID
	// WalletID is a wallet.
	WalletID core.WalletID
	// RecurringTransactionRuleID is a recurring transaction rule.
	RecurringTransactionRuleID core.RecurringTransactionRuleID
}

// Valid reports whether exactly one ID of o is set.
func (o ObjectOwner) Valid() bool {
	n := 0
	for _, zero := range []bool{o.SubscriptionID.IsZero(), o.WalletID.IsZero(), o.RecurringTransactionRuleID.IsZero()} {
		if !zero {
			n++
		}
	}
	return n == 1
}

// ObjectConnection is a billing object's choice of connection for one
// category (domain §15.5): a specific connection of its customer, or none
// (skip). Without one, the object inherits the customer's default
// connection of the category. At most one per (owner, category).
type ObjectConnection struct {
	// ID identifies the choice.
	ID core.ObjectConnectionID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Owner is the billing object.
	Owner ObjectOwner
	// Category is payment, tax, accounting or crm.
	Category Category
	// Behavior is specific or skip.
	Behavior ConnectionBehavior
	// PaymentProviderCustomerID is the chosen payment connection
	// (specific, payment category).
	PaymentProviderCustomerID core.PaymentProviderCustomerID
	// IntegrationCustomerID is the chosen connection of another category
	// (specific).
	IntegrationCustomerID core.IntegrationCustomerID
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the entity-local invariants: one owner, a routed
// category, a stored behavior, and the connection column of the category
// set exactly when the behavior is specific.
func (c *ObjectConnection) Validate() error {
	var v core.Validator
	v.Check(c.Owner.Valid(), "owner", core.CodeInvalid)
	payment := c.Category == CategoryPayment
	if v.Enum("category", c.Category) {
		v.Check(c.Category != CategoryAuth, "category", core.CodeInvalid)
	}
	if v.Enum("behavior", c.Behavior) {
		v.Check(c.Behavior != ConnectionBehaviorInherit, "behavior", core.CodeInvalid)
	}
	specific := c.Behavior == ConnectionBehaviorSpecific
	v.Check(c.PaymentProviderCustomerID.IsZero() != (specific && payment), "payment_provider_customer_id", core.CodeInvalid)
	v.Check(c.IntegrationCustomerID.IsZero() != (specific && !payment), "integration_customer_id", core.CodeInvalid)
	return v.Err()
}

// ObjectConnectionRepository stores billing objects' connection choices.
type ObjectConnectionRepository interface {
	// Create inserts c; a second choice of the owner and category fails
	// with core.ErrConflict.
	Create(ctx context.Context, c *ObjectConnection) error
	// Update replaces the stored choice c.ID with c.
	Update(ctx context.Context, c *ObjectConnection) error
	// Delete removes the choice id, or fails with core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.ObjectConnectionID) error
	// ListByOwner returns the choices of a billing object, by ID.
	ListByOwner(ctx context.Context, org core.OrganizationID, owner ObjectOwner) ([]*ObjectConnection, error)
	// DeleteByPaymentProviderCustomer removes the choices of a payment
	// connection.
	DeleteByPaymentProviderCustomer(ctx context.Context, org core.OrganizationID, id core.PaymentProviderCustomerID) error
}
