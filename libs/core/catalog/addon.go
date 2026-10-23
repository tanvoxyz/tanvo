package catalog

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// AddOn is a reusable priced item, used by one-off invoices and fixed
// charges (domain §7.5).
type AddOn struct {
	// ID identifies the add-on.
	ID core.AddOnID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique among live add-ons.
	Code string
	// Name is required, ≤ 255 characters.
	Name string
	// InvoiceDisplayName replaces Name on invoices.
	InvoiceDisplayName *string
	// Description is free text.
	Description *string
	// Amount is the default price, > 0, in minor units of Currency.
	Amount int64
	// Currency is the add-on currency.
	Currency money.Currency
	// TaxIDs are the add-on's taxes (domain §22.10).
	TaxIDs []core.TaxID
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// InvoiceName returns the name printed on invoices.
func (a *AddOn) InvoiceName() string {
	if a.InvoiceDisplayName != nil && *a.InvoiceDisplayName != "" {
		return *a.InvoiceDisplayName
	}
	return a.Name
}

// Validate checks the invariants of domain §7.5.
func (a *AddOn) Validate() error {
	var v core.Validator
	v.Code("code", a.Code, 255)
	v.String("name", a.Name, 255)
	v.OptionalString("invoice_display_name", a.InvoiceDisplayName, 255)
	v.Positive("amount", a.Amount)
	v.Currency("currency", a.Currency)
	return v.Err()
}
