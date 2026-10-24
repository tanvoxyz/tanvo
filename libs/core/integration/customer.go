package integration

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// CodeTaxIntegrationExists: a customer has at most one tax integration
// customer (domain §17.2).
const CodeTaxIntegrationExists = "tax_integration_exists"

// IntegrationCustomer links a customer to its record in an accounting, tax
// or CRM integration (domain §17.2); payment connections use the payment
// package's provider customers (domain §15.5).
type IntegrationCustomer struct {
	// ID identifies the link.
	ID core.IntegrationCustomerID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// IntegrationID is the connection.
	IntegrationID core.IntegrationID
	// CustomerID is the local customer.
	CustomerID core.CustomerID
	// Category is the integration's category, copied for lookups.
	Category Category
	// ExternalCustomerID is the customer's id in the external system;
	// several customers may share one.
	ExternalCustomerID string
	// SubsidiaryID is the NetSuite subsidiary.
	SubsidiaryID *string
	// SyncWithProvider creates or updates the external record from Tanvo.
	SyncWithProvider bool
	// IsDefault marks the customer's default connection of the category.
	IsDefault bool
	// Settings holds provider-specific values (HubSpot targeted object,
	// email) as a JSON object.
	Settings json.RawMessage
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the entity-local invariants of domain §17.2.
func (c *IntegrationCustomer) Validate() error {
	var v core.Validator
	v.Check(!c.IntegrationID.IsZero(), "integration_id", core.CodeRequired)
	v.Check(!c.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	if v.Enum("category", c.Category) {
		v.Check(c.Category == CategoryAccounting || c.Category == CategoryTax || c.Category == CategoryCRM, "category", core.CodeForbidden)
	}
	v.MaxLen("external_customer_id", c.ExternalCustomerID, 255)
	if !c.SyncWithProvider {
		v.Required("external_customer_id", c.ExternalCustomerID)
	}
	v.OptionalString("subsidiary_id", c.SubsidiaryID, 255)
	if len(c.Settings) > 0 {
		v.Check(isJSONObject(c.Settings), "settings", core.CodeInvalidFormat)
	}
	return v.Err()
}

// Setting returns the string value of key in Settings.
func (c *IntegrationCustomer) Setting(key string) (string, bool) { return getSetting(c.Settings, key) }

// SetSetting stores the string value of key in Settings.
func (c *IntegrationCustomer) SetSetting(key, value string) error {
	s, err := setSetting(c.Settings, key, value)
	if err != nil {
		return err
	}
	c.Settings = s
	return nil
}

// CheckCustomerLinks enforces the per-customer rules of domain §17.2 for
// candidate among the customer's existing links: one tax integration
// customer, a *core.ValidationError on integration_id, and one default per
// category, a *core.RuleError.
func CheckCustomerLinks(candidate *IntegrationCustomer, existing []*IntegrationCustomer) error {
	others := slices.DeleteFunc(slices.Clone(existing), func(x *IntegrationCustomer) bool {
		return x.ID == candidate.ID || x.CustomerID != candidate.CustomerID
	})
	if candidate.Category == CategoryTax && slices.ContainsFunc(others, func(x *IntegrationCustomer) bool { return x.Category == CategoryTax }) {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "integration_id", Code: CodeTaxIntegrationExists}}}
	}
	if candidate.IsDefault && slices.ContainsFunc(others, func(x *IntegrationCustomer) bool {
		return x.Category == candidate.Category && x.IsDefault
	}) {
		return core.NewRuleError("default_already_exists", "a customer has one default connection per category")
	}
	return nil
}
