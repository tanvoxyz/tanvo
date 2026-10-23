package creditnote

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Filter narrows [Repository.List]; zero fields do not filter.
type Filter struct {
	// CustomerID keeps the credit notes of one customer.
	CustomerID core.CustomerID
	// InvoiceID keeps the credit notes of one invoice.
	InvoiceID core.InvoiceID
	// Statuses keeps the credit notes in one of these statuses.
	Statuses []Status
	// CreditStatuses keeps the credit notes in one of these credit statuses.
	CreditStatuses []CreditStatus
	// Reasons keeps the credit notes with one of these reasons.
	Reasons []Reason
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
	// List returns the credit notes matching f in creation order.
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
	// ListAppliedTaxes returns the applied taxes of one credit note.
	ListAppliedTaxes(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) ([]AppliedTax, error)
	// NextSequentialID returns the next per-invoice sequence: 1 + the
	// largest assigned one.
	NextSequentialID(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) (int64, error)
	// Delete deletes the draft credit note id; a finalized one fails with
	// core.ErrConflict and a missing one with core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) error
}
