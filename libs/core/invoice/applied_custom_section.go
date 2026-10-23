package invoice

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// AppliedCustomSection is the copy of a custom section printed on an
// invoice, taken when the invoice is created (domain §11.9, lago
// applied_invoice_custom_sections).
type AppliedCustomSection struct {
	// ID identifies the copy.
	ID core.AppliedInvoiceCustomSectionID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the invoice.
	InvoiceID core.InvoiceID
	// Code is the section's code.
	Code string
	// Name is the section's name.
	Name string
	// DisplayName is the printed title.
	DisplayName *string
	// Details is the printed text.
	Details *string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// AppliedCustomSectionOf returns the copy of s printed on the invoice
// inv, identified by id.
func AppliedCustomSectionOf(id core.AppliedInvoiceCustomSectionID, inv core.InvoiceID, s *CustomSection, at time.Time) *AppliedCustomSection {
	return &AppliedCustomSection{
		ID: id, OrganizationID: s.OrganizationID, InvoiceID: inv,
		Code: s.Code, Name: s.Name, DisplayName: s.DisplayName, Details: s.Details, CreatedAt: at,
	}
}

// AppliedCustomSectionRepository stores the custom sections printed on
// invoices; they are deleted with their invoice.
type AppliedCustomSectionRepository interface {
	// Create inserts a.
	Create(ctx context.Context, a *AppliedCustomSection) error
	// ListByInvoice returns the sections of one invoice in creation order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*AppliedCustomSection, error)
	// DeleteByInvoice deletes the sections of one invoice, before a
	// refreshed draft or a retried generation applies them again.
	DeleteByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) error
}
