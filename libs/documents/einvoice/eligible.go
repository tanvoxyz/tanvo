package einvoice

import (
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

// Eligible reports whether be's documents carry an e-invoice: e-invoicing
// is enabled and its country is one whose format is produced (France or
// Germany).
func Eligible(be *organization.BillingEntity) bool {
	if be == nil || !be.EInvoicing {
		return false
	}
	return organization.EInvoicingCountry(strings.ToUpper(be.Address.Country)).Valid()
}

// germanSeller reports whether be is in Germany, which selects the
// XRechnung and Peppol profiles of UBL documents.
func germanSeller(be *organization.BillingEntity) bool {
	return strings.ToUpper(be.Address.Country) == string(organization.EInvoicingCountryDE)
}
