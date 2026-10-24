package integration

import (
	"context"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Targeted objects of a HubSpot integration customer.
const (
	// TargetedObjectCompanies syncs the customer as a HubSpot company.
	TargetedObjectCompanies = "companies"
	// TargetedObjectContacts syncs the customer as a HubSpot contact.
	TargetedObjectContacts = "contacts"
)

// ContactGateway is implemented by the adapters that create the
// customer's record at the provider: accounting contacts (NetSuite, Xero),
// Avalara customers and HubSpot contacts or companies.
type ContactGateway interface {
	// CreateContact creates the record of c and returns its ID.
	CreateContact(ctx context.Context, conn Connection, c *Contact) (ContactResult, error)
	// UpdateContact updates the record c.LinkedID.
	UpdateContact(ctx context.Context, conn Connection, c *Contact) (ContactResult, error)
}

// ContactResult is the provider's record of a contact.
type ContactResult struct {
	// ExternalID is the record's ID at the provider.
	ExternalID string
	// Email is the email the provider stored, when it returns one
	// (HubSpot).
	Email string
}

// Contact is the neutral document of a customer synced to a provider.
type Contact struct {
	// CustomerID is the Tanvo customer.
	CustomerID core.CustomerID
	// ExternalID is the customer's external ID.
	ExternalID string
	// LinkedID is the record's ID at the provider, for updates.
	LinkedID string
	// Individual reports a customer of type individual; a customer of
	// type company or without type is a company.
	Individual bool
	// Name is the customer's name.
	Name string
	// DisplayName is the display name without the legal name preferred.
	DisplayName string
	// Firstname is the first name.
	Firstname string
	// Lastname is the last name.
	Lastname string
	// LegalName is the legal name.
	LegalName string
	// Email is the customer's email field (a comma-separated list).
	Email string
	// Phone is the customer's phone field (a comma-separated list).
	Phone string
	// URL is the customer's website.
	URL string
	// TaxNumber is the tax identification number.
	TaxNumber string
	// BillingAddress is the billing address.
	BillingAddress core.Address
	// ShippingAddress is the shipping address as stored.
	ShippingAddress core.Address
	// SalesforceID is the customer's Salesforce ID, when known.
	SalesforceID string
	// SubsidiaryID is the NetSuite subsidiary.
	SubsidiaryID string
	// TargetedObject is the HubSpot object of the customer (companies or
	// contacts).
	TargetedObject string
	// Link is the customer's page in the Tanvo front end.
	Link string
}

// FirstEmail is the first address of the email list, trimmed (lago
// contacts/payloads/base_payload.rb#email).
func (c *Contact) FirstEmail() string { return firstOfList(c.Email) }

// FirstPhone is the first number of the phone list, trimmed.
func (c *Contact) FirstPhone() string { return firstOfList(c.Phone) }

// EffectiveShippingAddress is the shipping address with each empty field
// taken from the billing address, as for customers (domain §6).
func (c *Contact) EffectiveShippingAddress() core.Address {
	return c.ShippingAddress.WithFallback(c.BillingAddress)
}

// SameBillingAndShippingAddress reports whether the shipping address is
// empty or equal to the billing address.
func (c *Contact) SameBillingAndShippingAddress() bool {
	return c.ShippingAddress.IsZero() || c.ShippingAddress == c.BillingAddress
}

// EmptyBillingAndShippingAddress reports whether both addresses are empty.
func (c *Contact) EmptyBillingAndShippingAddress() bool {
	return c.ShippingAddress.IsZero() && c.BillingAddress.IsZero()
}

func firstOfList(s string) string {
	first, _, _ := strings.Cut(s, ",")
	return strings.TrimSpace(first)
}
