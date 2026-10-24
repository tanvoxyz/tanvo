package integration

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// AccountingGateway is the port of accounting providers (NetSuite, Xero).
// Sync methods return the ID of the record created at the provider.
type AccountingGateway interface {
	Gateway
	ContactGateway
	// SyncInvoice creates the invoice inv.
	SyncInvoice(ctx context.Context, conn Connection, inv *AccountingInvoice) (string, error)
	// SyncCreditNote creates the credit note cn.
	SyncCreditNote(ctx context.Context, conn Connection, cn *AccountingCreditNote) (string, error)
	// SyncPayment creates the payment p of a synced invoice.
	SyncPayment(ctx context.Context, conn Connection, p *AccountingPayment) (string, error)
	// FetchItems returns the provider's items.
	FetchItems(ctx context.Context, conn Connection) ([]ExternalRecord, error)
	// FetchAccounts returns the provider's ledger accounts, or
	// [ErrUnsupported].
	FetchAccounts(ctx context.Context, conn Connection) ([]ExternalRecord, error)
}

// InvoiceFinder is implemented by accounting adapters that can find an
// invoice already created by an earlier attempt (NetSuite, lago
// Invoices::ReconcileService).
type InvoiceFinder interface {
	// FindInvoice returns the ID of the provider's invoice numbered
	// number, and false when there is none.
	FindInvoice(ctx context.Context, conn Connection, number string) (string, bool, error)
}

// SubsidiaryLister is implemented by accounting adapters with
// subsidiaries (NetSuite).
type SubsidiaryLister interface {
	// Subsidiaries returns the provider's subsidiaries.
	Subsidiaries(ctx context.Context, conn Connection) ([]ExternalRecord, error)
}

// ExternalRecord is an item, account or subsidiary of the provider.
type ExternalRecord struct {
	// ID is the provider's ID of the record (the item code for Xero items).
	ID string
	// Name is the display name.
	Name string
	// AccountCode is the ledger account code, when the record has one.
	AccountCode string
}

// DocumentItems are the collection mappings an accounting document needs,
// resolved for its billing entity (lago base_payload.rb).
type DocumentItems struct {
	// Coupon maps coupon discounts.
	Coupon *ExternalItem
	// PrepaidCredit maps wallet credits and wallet top-up fees.
	PrepaidCredit *ExternalItem
	// CreditNote maps credit note credits.
	CreditNote *ExternalItem
	// Account is the payments ledger account.
	Account *ExternalItem
	// Tax is the NetSuite tax item; nil when unmapped.
	Tax *TaxItem
	// Currencies maps ISO 4217 codes to the provider's currency codes.
	Currencies map[string]string
}

// TaxItem is the NetSuite tax mapping.
type TaxItem struct {
	// Nexus is the tax nexus.
	Nexus string
	// Type is the tax type.
	Type string
	// Code is the tax code.
	Code string
}

// Complete reports whether the nexus, type and code are all set (lago
// tax_item_complete?).
func (t *TaxItem) Complete() bool {
	return t != nil && t.Nexus != "" && t.Type != "" && t.Code != ""
}

// AccountingFee is a fee of an accounting document, with its mapping
// resolved.
type AccountingFee struct {
	// ID is the fee.
	ID core.FeeID
	// Type is the fee type.
	Type core.FeeType
	// Item is the mapped external item; nil when nothing maps the fee,
	// in which case the adapter sends no item and the provider rejects
	// the document.
	Item *ExternalItem
	// InvoiceName is the fee's name on invoices.
	InvoiceName string
	// FilterDisplayName is the display name of the fee's charge filter;
	// empty without one.
	FilterDisplayName string
	// GroupedByDisplay is the pricing group suffix (" • a • b").
	GroupedByDisplay string
	// ItemName is the name of what the fee bills.
	ItemName string
	// ItemSource is the code of what the fee bills (lago Fee#item_source).
	ItemSource string
	// Units is the quantity.
	Units money.Decimal
	// PreciseUnitAmount is the unit price in major units.
	PreciseUnitAmount money.Decimal
	// AmountCents is the amount in minor units.
	AmountCents int64
	// TaxesAmountCents is the taxes amount in minor units.
	TaxesAmountCents int64
	// TaxesRate is the taxes rate in percent.
	TaxesRate money.Decimal
	// From and To are the service period; zero when unknown.
	From time.Time
	// To ends the service period.
	To time.Time
	// CreatedAt is the creation instant; documents order fees by it.
	CreatedAt time.Time
}

// AccountingInvoice is the neutral document of an invoice synced to an
// accounting provider.
type AccountingInvoice struct {
	// ID is the invoice.
	ID core.InvoiceID
	// Number is the invoice number.
	Number string
	// ExternalCustomerID is the provider's contact.
	ExternalCustomerID string
	// IssuingDate is the issuing date.
	IssuingDate core.Date
	// PaymentDueDate is the payment due date.
	PaymentDueDate core.Date
	// Currency is the invoice currency.
	Currency money.Currency
	// PurchaseOrderNumber is the customer's purchase order number.
	PurchaseOrderNumber string
	// URL is the invoice's page in the Tanvo front end.
	URL string
	// PlanCodes are the codes of the invoice's subscription plans.
	PlanCodes []string
	// TaxesAmountCents is the invoice's taxes.
	TaxesAmountCents int64
	// TaxesRate is the invoice's taxes rate in percent.
	TaxesRate money.Decimal
	// CouponsAmountCents is the coupons discount.
	CouponsAmountCents int64
	// PrepaidCreditAmountCents is the wallet credit applied.
	PrepaidCreditAmountCents int64
	// ProgressiveBillingCreditAmountCents is the usage already billed.
	ProgressiveBillingCreditAmountCents int64
	// CreditNotesAmountCents is the credit notes applied.
	CreditNotesAmountCents int64
	// CouponNames, ProgressiveBillingNames and CreditNoteNames are the
	// item names of the invoice's credits of each kind.
	CouponNames []string
	// ProgressiveBillingNames are the progressive billing credits.
	ProgressiveBillingNames []string
	// CreditNoteNames are the credit note credits.
	CreditNoteNames []string
	// Fees are every fee of the invoice in creation order.
	Fees []AccountingFee
	// Items are the collection mappings.
	Items DocumentItems
	// Document is the invoice's API representation (NetSuite's
	// fullInvoicePayload).
	Document json.RawMessage
}

// AccountingCreditNoteItem is an item of a credit note.
type AccountingCreditNoteItem struct {
	// ID identifies the item.
	ID string
	// Fee is the credited fee.
	Fee AccountingFee
	// AmountCents is the credited amount.
	AmountCents int64
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// AccountingCreditNote is the neutral document of a credit note synced to
// an accounting provider.
type AccountingCreditNote struct {
	// ID is the credit note.
	ID core.CreditNoteID
	// Number is the credit note number.
	Number string
	// ExternalCustomerID is the provider's contact.
	ExternalCustomerID string
	// IssuingDate is the issuing date.
	IssuingDate core.Date
	// Currency is the currency.
	Currency money.Currency
	// TaxesAmountCents is the credit note's taxes.
	TaxesAmountCents int64
	// TaxesRate is the credit note's taxes rate in percent.
	TaxesRate money.Decimal
	// CouponsAdjustmentAmountCents is the coupons share credited back.
	CouponsAdjustmentAmountCents int64
	// CouponNames are the item names of the invoice's coupon credits.
	CouponNames []string
	// Items are the credit note's items in creation order.
	Items []AccountingCreditNoteItem
	// Collections are the collection mappings.
	Collections DocumentItems
	// Document is the credit note's API representation (NetSuite's
	// fullCreditNotePayload).
	Document json.RawMessage
}

// AccountingPayment is the neutral document of a payment synced to an
// accounting provider.
type AccountingPayment struct {
	// ID is the payment.
	ID core.PaymentID
	// InvoiceExternalID is the provider's record of the paid invoice.
	InvoiceExternalID string
	// ExternalCustomerID is the provider's contact.
	ExternalCustomerID string
	// Account is the payments ledger account; nil when unmapped.
	Account *ExternalItem
	// AmountCents is the paid amount.
	AmountCents int64
	// Currency is the payment currency.
	Currency money.Currency
	// CreatedAt is the payment's creation instant.
	CreatedAt time.Time
}

// Major converts minor units of c into major units, rounded half away
// from zero to the minor unit first (lago BasePayload#amount).
func Major(c money.Currency, cents money.Decimal) money.Decimal {
	return c.MinorToUnits(cents.Round(0, money.HalfUp))
}
