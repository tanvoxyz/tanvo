package creditnote

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Filter narrows [Repository.List] (the credit note list of the API, which
// passes Statuses = [StatusFinalized]); zero fields do not filter.
type Filter struct {
	// CustomerID keeps the credit notes of one customer; when set, Search
	// does not look at the customer's fields.
	CustomerID core.CustomerID
	// CustomerExternalID keeps the credit notes of the customers with that
	// external ID.
	CustomerExternalID string
	// InvoiceID keeps the credit notes of one invoice.
	InvoiceID core.InvoiceID
	// InvoiceNumber keeps the credit notes of the invoice with that number.
	InvoiceNumber string
	// PurchaseOrderNumber keeps the credit notes whose invoice has that
	// purchase order number, compared case-insensitively.
	PurchaseOrderNumber string
	// BillingEntityIDs keeps the credit notes whose invoice was issued by
	// one of these billing entities.
	BillingEntityIDs []core.BillingEntityID
	// SelfBilled, when set, keeps the credit notes whose invoice is (true)
	// or is not (false) self-billed.
	SelfBilled *bool
	// Currency keeps the credit notes in that currency.
	Currency money.Currency
	// Statuses keeps the credit notes in one of these statuses.
	Statuses []Status
	// CreditStatuses keeps the credit notes in one of these credit
	// statuses; the empty status matches the credit notes without one.
	CreditStatuses []CreditStatus
	// RefundStatuses keeps the credit notes in one of these refund
	// statuses; the empty status matches the credit notes without one.
	RefundStatuses []core.RefundStatus
	// Reasons keeps the credit notes with one of these reasons.
	Reasons []Reason
	// Types keeps the credit notes with a positive amount of one of these
	// kinds.
	Types []Type
	// IssuingDateFrom keeps the credit notes issued on or after the date.
	IssuingDateFrom core.Date
	// IssuingDateTo keeps the credit notes issued on or before the date.
	IssuingDateTo core.Date
	// AmountFrom keeps the credit notes whose total amount is at least it.
	AmountFrom *int64
	// AmountTo keeps the credit notes whose total amount is at most it.
	AmountTo *int64
	// Search keeps the credit notes whose ID or number contains the trimmed
	// term, or, unless CustomerID is set, whose customer's name, first
	// name, last name, external ID or email contains it (case-insensitive,
	// with LIKE wildcards matched literally).
	Search string
	// Order is the list order; core.OrderRecent lists the newest first.
	Order core.ListOrder
}

// Type is a kind of amount a credit note carries, for [Filter.Types].
type Type string

// Type values.
const (
	// TypeCredit keeps credit notes with a positive credit amount.
	TypeCredit Type = "credit"
	// TypeRefund keeps credit notes with a positive refund amount.
	TypeRefund Type = "refund"
	// TypeOffset keeps credit notes with a positive offset amount.
	TypeOffset Type = "offset"
)

// Valid reports whether t is a declared Type.
func (t Type) Valid() bool { return t == TypeCredit || t == TypeRefund || t == TypeOffset }

// String returns the stored form of t.
func (t Type) String() string { return string(t) }

// Is reports whether cn has a positive amount of kind t.
func (t Type) Is(cn *CreditNote) bool {
	switch t {
	case TypeCredit:
		return cn.CreditAmount > 0
	case TypeRefund:
		return cn.RefundAmount > 0
	case TypeOffset:
		return cn.OffsetAmount > 0
	default:
		return false
	}
}

// Validate checks the filter: declared enumeration values and a valid
// order.
func (f Filter) Validate() error {
	var v core.Validator
	for i, s := range f.Statuses {
		v.Enum(fmt.Sprintf("statuses[%d]", i), s)
	}
	for i, s := range f.CreditStatuses {
		v.OptionalEnum(fmt.Sprintf("credit_statuses[%d]", i), s)
	}
	for i, s := range f.RefundStatuses {
		v.OptionalEnum(fmt.Sprintf("refund_statuses[%d]", i), s)
	}
	for i, r := range f.Reasons {
		v.Enum(fmt.Sprintf("reasons[%d]", i), r)
	}
	for i, t := range f.Types {
		v.Enum(fmt.Sprintf("types[%d]", i), t)
	}
	v.Check(f.Order.Valid(), "order", core.CodeInvalid)
	return v.Err()
}

// FeeCredit is the amount credited on one fee by non-voided credit notes.
type FeeCredit struct {
	// FeeID is the credited fee.
	FeeID core.FeeID
	// Amount is Σ item amounts.
	Amount int64
}

// Repository stores credit notes with their items and applied taxes. Every
// method is scoped to an organization.
type Repository interface {
	// Create inserts cn with its items and applied taxes; a number already
	// used in the organization fails with core.ErrConflict.
	Create(ctx context.Context, cn *CreditNote) error
	// Update replaces the stored credit note cn.ID, its items and applied
	// taxes with cn.
	Update(ctx context.Context, cn *CreditNote) error
	// Get returns the credit note id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) (*CreditNote, error)
	// List returns the credit notes matching f, in the order f.Order; an
	// invalid filter fails with core.ErrInvalidValue.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*CreditNote], error)
	// ListByInvoice returns every credit note of one invoice in creation
	// order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*CreditNote, error)
	// ListAvailable returns the finalized credit notes of a customer with
	// an available, positive balance in currency, oldest first: the credits
	// applied to a new invoice (§22.7).
	ListAvailable(ctx context.Context, org core.OrganizationID, customer core.CustomerID, currency money.Currency) ([]*CreditNote, error)
	// ListItemsByCreditNote returns the items of one credit note.
	ListItemsByCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) ([]Item, error)
	// CreditedByFee returns, for each of fees, the amount credited by the
	// invoice's credit notes whose credit status is not voided; fees with
	// nothing credited are omitted.
	CreditedByFee(ctx context.Context, org core.OrganizationID, fees []core.FeeID) ([]FeeCredit, error)
	// DetachFees clears the fee of every item of the credit notes of the
	// invoice, so that the fees of a draft invoice being refreshed can be
	// deleted; the items then move to the regenerated fees with Update
	// (lago Invoices::RefreshDraftService, refresh_draft_service.rb:124).
	DetachFees(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) error
	// ListAppliedTaxes returns the applied taxes of one credit note.
	ListAppliedTaxes(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) ([]AppliedTax, error)
	// NextSequentialID returns the next per-invoice sequence: 1 + the
	// largest assigned one.
	NextSequentialID(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) (int64, error)
	// Delete deletes the draft credit note id; a finalized one, or one
	// still referenced by a refund, a wallet transaction, an invoice
	// settlement or an invoice credit, fails with core.ErrConflict and a
	// missing one with core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) error
}
