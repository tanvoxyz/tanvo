package memstore

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

var _ invoice.AppliedCustomSectionRepository = appliedCustomSectionRepo{}

// appliedCustomSectionRepo implements invoice.AppliedCustomSectionRepository.
type appliedCustomSectionRepo struct{ d *invoiceData }

func (r appliedCustomSectionRepo) Create(_ context.Context, a *invoice.AppliedCustomSection) error {
	if r.d.sectionCopies.has(a.ID) {
		return conflict("applied_invoice_custom_section", "id")
	}
	if _, ok := r.d.invoices.get(a.InvoiceID); !ok {
		return notFound("invoice", a.InvoiceID)
	}
	r.d.sectionCopies.put(a.ID, *a)
	return nil
}

func (r appliedCustomSectionRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.AppliedCustomSection, error) {
	return r.d.sectionCopies.find(func(x *invoice.AppliedCustomSection) bool {
		return x.OrganizationID == org && x.InvoiceID == inv
	}), nil
}

func (r appliedCustomSectionRepo) DeleteByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	r.d.sectionCopies.deleteWhere(func(x *invoice.AppliedCustomSection) bool {
		return x.OrganizationID == org && x.InvoiceID == inv
	})
	return nil
}
