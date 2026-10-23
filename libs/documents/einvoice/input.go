package einvoice

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Invoice is an invoice to render, with what the caller resolves from its
// associations.
type Invoice struct {
	// Invoice is the finalized invoice.
	Invoice *invoice.Invoice
	// BillingEntity is the issuing billing entity.
	BillingEntity *organization.BillingEntity
	// Customer is the invoiced customer.
	Customer *customer.Customer
	// Lines are the invoice's fees, in any order; each fee becomes a line
	// and they make up the tax breakdown.
	Lines []Line
	// DeliveryDate is the date of supply. For subscription and progressive
	// billing invoices it is the earliest start of the current charges
	// period of the invoice's subscriptions; when zero, the invoice's
	// creation date (UTC) is used, as for one-off and credit invoices.
	DeliveryDate core.Date
	// PurchaseOrderNumber is the buyer's purchase order number, if any.
	PurchaseOrderNumber string
	// ProviderTaxes is set when a tax provider computed the invoice's taxes
	// (an applied tax without a tax and with a positive taxable base).
	ProviderTaxes bool
	// Labels are the localized strings; zero means English.
	Labels Labels
}

// Line is one fee of an invoice.
type Line struct {
	// Fee is the fee.
	Fee invoice.Fee
	// Name is the item name: the billable metric, add-on, fixed charge
	// add-on, product or plan name, or the wallet name for credits.
	Name string
	// Description is the printed description: the fee's invoice name, or
	// the localized "<interval> subscription - <plan invoice name>".
	Description string
}

// CreditNote is a credit note to render, with what the caller resolves from
// its associations.
type CreditNote struct {
	// CreditNote is the finalized credit note.
	CreditNote *creditnote.CreditNote
	// Invoice is the credited invoice.
	Invoice *invoice.Invoice
	// BillingEntity is the issuing billing entity.
	BillingEntity *organization.BillingEntity
	// Customer is the credited customer.
	Customer *customer.Customer
	// Lines are the credit note's items with their fees, in any order.
	Lines []CreditNoteLine
	// PurchaseOrderNumber is the credited invoice's purchase order number.
	PurchaseOrderNumber string
	// ProviderTaxes is set when a tax provider computed the credited
	// invoice's taxes.
	ProviderTaxes bool
	// Labels are the localized strings; zero means English.
	Labels Labels
}

// CreditNoteLine is one item of a credit note.
type CreditNoteLine struct {
	// Item is the credit note item.
	Item creditnote.Item
	// Fee is the credited fee.
	Fee invoice.Fee
	// Name is the fee's item name, as [Line.Name].
	Name string
	// Description is the fee's printed description, as [Line.Description].
	Description string
}

// Payment is a payment receipt to render, with what the caller resolves
// from its associations.
type Payment struct {
	// Payment is the payment.
	Payment *payment.Payment
	// Receipt is the payment's receipt.
	Receipt *payment.Receipt
	// BillingEntity is the billing entity of the paying customer.
	BillingEntity *organization.BillingEntity
	// Customer is the paying customer.
	Customer *customer.Customer
	// Invoices are the invoices the payment settles: the payable invoice,
	// or the invoices of the payable payment request.
	Invoices []*invoice.Invoice
	// ProviderType is the payment provider of a provider payment, such as
	// "stripe".
	ProviderType string
	// Labels are the localized strings; zero means English.
	Labels Labels
}

// parties are the seller and the buyer of a document.
type parties struct {
	seller *organization.BillingEntity
	buyer  *customer.Customer
}

// check fails when a party is missing.
func (p parties) check() error {
	if p.seller == nil {
		return fmt.Errorf("einvoice: billing entity: %w", core.ErrNotFound)
	}
	if p.buyer == nil {
		return fmt.Errorf("einvoice: customer: %w", core.ErrNotFound)
	}
	return nil
}

func (in *Invoice) check() error {
	if in == nil || in.Invoice == nil {
		return fmt.Errorf("einvoice: invoice: %w", core.ErrNotFound)
	}
	return parties{in.BillingEntity, in.Customer}.check()
}

func (in *CreditNote) check() error {
	if in == nil || in.CreditNote == nil {
		return fmt.Errorf("einvoice: credit note: %w", core.ErrNotFound)
	}
	if in.Invoice == nil {
		return fmt.Errorf("einvoice: credited invoice: %w", core.ErrNotFound)
	}
	return parties{in.BillingEntity, in.Customer}.check()
}

func (in *Payment) check() error {
	if in == nil || in.Payment == nil {
		return fmt.Errorf("einvoice: payment: %w", core.ErrNotFound)
	}
	if in.Receipt == nil {
		return fmt.Errorf("einvoice: payment receipt: %w", core.ErrNotFound)
	}
	for i, inv := range in.Invoices {
		if inv == nil {
			return fmt.Errorf("einvoice: invoice %d: %w", i, core.ErrNotFound)
		}
	}
	return parties{in.BillingEntity, in.Customer}.check()
}
