package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// EnqueueInvoiceDocuments enqueues the [JobGenerateInvoiceDocument] of the
// finalized invoice inv, unique per invoice, notifying the customer when
// its billing entity sends invoice.finalized emails. Advance charges
// invoices are never emailed (lago
// app/services/invoices/advance_charges_service.rb:31); every billing
// entity may send them, there is no premium gate (lago should_deliver_email?,
// e.g. app/services/invoices/subscription_service.rb:242-245).
func EnqueueInvoiceDocuments(ctx context.Context, r port.Repos, inv *invoice.Invoice, runAt time.Time) error {
	notify := false
	if inv.Type != invoice.TypeAdvanceCharges {
		be, err := r.BillingEntities.Get(ctx, inv.OrganizationID, inv.BillingEntityID)
		if err != nil {
			return fmt.Errorf("load billing entity %s: %w", inv.BillingEntityID, err)
		}
		notify = be.SendsEmail(organization.EmailSettingInvoiceFinalized)
	}
	args := InvoiceArgs{InvoiceID: inv.ID, Notify: notify}
	return Enqueue(ctx, r, inv.OrganizationID, JobGenerateInvoiceDocument, args, InvoiceDocumentsKey(inv), runAt)
}

// InvoiceDocumentsKey is the unique key of the [JobGenerateInvoiceDocument]
// of inv.
func InvoiceDocumentsKey(inv *invoice.Invoice) string { return "invoice:" + inv.ID.String() }
