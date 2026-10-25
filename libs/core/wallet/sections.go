package wallet

import "github.com/tanvoxyz/tanvo/libs/core"

// CustomSections is the invoice custom section selection of a wallet, a
// recurring rule or a transaction (lago invoice_custom_section, domain
// §14): Skip prints none, else IDs are the sections printed. The IDs may
// name sections deleted since, which print nothing.
type CustomSections struct {
	// Skip prints no custom sections.
	Skip bool
	// IDs are the selected sections, in their order.
	IDs []core.InvoiceCustomSectionID
}

// Set reports whether the selection skips or selects sections, so that it
// takes precedence (lago `skip_invoice_custom_sections ||
// selected_invoice_custom_sections.any?`).
func (s CustomSections) Set() bool { return s.Skip || len(s.IDs) > 0 }
