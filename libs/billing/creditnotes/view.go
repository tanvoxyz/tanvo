package creditnotes

import (
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// ResourceBillingEntity is the resource of a billing entity not found.
const ResourceBillingEntity = "billing_entity"

// View is a credit note with what its API representation takes from its
// invoice (lago V1::CreditNoteSerializer and Types::CreditNotes::Object).
type View struct {
	// CreditNote is the credit note.
	CreditNote *creditnote.CreditNote
	// InvoiceNumber is the invoice's number.
	InvoiceNumber string
	// PurchaseOrderNumber is the invoice's purchase order number.
	PurchaseOrderNumber string
	// BillingEntityID is the invoice's billing entity.
	BillingEntityID core.BillingEntityID
	// SelfBilled reports a self-billed invoice.
	SelfBilled bool
	// TaxProviderSyncable reports a finalized credit note, not on a credit
	// invoice, whose report to the tax provider failed: the one
	// RetryTaxReporting retries.
	//
	// source: app/graphql/types/credit_notes/object.rb:75-80
	TaxProviderSyncable bool
}

// Views returns the views of cns, in order.
func (s *Service) Views(ctx context.Context, org core.OrganizationID, cns ...*creditnote.CreditNote) ([]View, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) ([]View, error) {
		if err := withErrors(ctx, r, org, cns...); err != nil {
			return nil, err
		}
		invoices := map[core.InvoiceID]*invoice.Invoice{}
		out := make([]View, len(cns))
		for i, cn := range cns {
			inv, ok := invoices[cn.InvoiceID]
			if !ok {
				var err error
				if inv, err = r.Invoices.Get(ctx, org, cn.InvoiceID); err != nil {
					return nil, fmt.Errorf("load invoice %s: %w", cn.InvoiceID, err)
				}
				invoices[cn.InvoiceID] = inv
			}
			out[i] = view(cn, inv)
		}
		return out, nil
	})
}

// view returns the view of cn on its invoice inv.
func view(cn *creditnote.CreditNote, inv *invoice.Invoice) View {
	return View{
		CreditNote:          cn,
		InvoiceNumber:       inv.Number,
		PurchaseOrderNumber: deref(inv.PurchaseOrderNumber),
		BillingEntityID:     inv.BillingEntityID,
		SelfBilled:          inv.SelfBilled,
		TaxProviderSyncable: cn.Status == creditnote.StatusFinalized && inv.Type != invoice.TypeCredit &&
			slices.ContainsFunc(cn.ErrorDetails, func(e creditnote.ErrorDetail) bool { return e.Code == creditnote.ErrorCodeTaxError }),
	}
}

// BillingEntityIDs resolves the billing entities of rs (IDs or codes,
// archived entities included) for a credit note list filter; one that
// does not exist is billing_entity not found.
//
// source: app/controllers/concerns/credit_note_index.rb:8-9
func (s *Service) BillingEntityIDs(ctx context.Context, org core.OrganizationID, rs []refs.Ref) ([]core.BillingEntityID, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) ([]core.BillingEntityID, error) {
		f := refs.Finder[core.BillingEntityID, *organization.BillingEntity]{
			Resource: ResourceBillingEntity,
			Get: func(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
				return r.BillingEntities.Get(ctx, org, id)
			},
			GetByCode: func(ctx context.Context, code string) (*organization.BillingEntity, error) {
				return r.BillingEntities.GetByCode(ctx, org, code)
			},
			ID: func(b *organization.BillingEntity) core.BillingEntityID { return b.ID },
		}
		return f.IDs(ctx, rs)
	})
}
