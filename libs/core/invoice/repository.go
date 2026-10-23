package invoice

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Filter narrows [Repository.List]; zero fields do not filter.
type Filter struct {
	// CustomerID keeps the invoices of one customer.
	CustomerID core.CustomerID
	// BillingEntityID keeps the invoices of one billing entity.
	BillingEntityID core.BillingEntityID
	// Statuses keeps the invoices in one of these statuses.
	Statuses []Status
	// PaymentStatuses keeps the invoices in one of these payment statuses.
	PaymentStatuses []core.PaymentStatus
	// Types keeps the invoices of one of these types.
	Types []Type
	// IDs keeps the invoices with one of these IDs.
	IDs []core.InvoiceID
	// IncludeGenerating also lists generating invoices, which the API hides.
	IncludeGenerating bool
}

// Repository stores invoices. Every method is scoped to an organization.
type Repository interface {
	// Create inserts inv; a number (other than a draft number) already used
	// in the organization, or an x402 payment token already used in any
	// organization, fails with core.ErrConflict.
	Create(ctx context.Context, inv *Invoice) error
	// Update replaces the stored invoice inv.ID with inv (same uniqueness
	// rule as Create).
	Update(ctx context.Context, inv *Invoice) error
	// Get returns the invoice id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*Invoice, error)
	// GetByNumber returns the invoice with that number, or core.ErrNotFound.
	GetByNumber(ctx context.Context, org core.OrganizationID, number string) (*Invoice, error)
	// List returns the invoices matching f in creation order.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Invoice], error)
	// ListByIDs returns the invoices with these IDs in creation order;
	// unknown IDs are ignored.
	ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.InvoiceID) ([]*Invoice, error)
	// ListDraftsToFinalize returns the drafts whose expected finalization
	// date is on or before date (or unset), across all customers.
	ListDraftsToFinalize(ctx context.Context, org core.OrganizationID, date core.Date) ([]*Invoice, error)
	// ListRefreshRequested returns the drafts flagged for refresh.
	ListRefreshRequested(ctx context.Context, org core.OrganizationID) ([]*Invoice, error)
	// ListOverdueCandidates returns the finalized, not-overdue invoices not
	// fully paid whose payment due date is before date.
	ListOverdueCandidates(ctx context.Context, org core.OrganizationID, date core.Date) ([]*Invoice, error)
	// NextSequentialID returns the next per-(customer, billing entity)
	// sequence: 1 + the largest assigned one.
	NextSequentialID(ctx context.Context, org core.OrganizationID, customer core.CustomerID, billingEntity core.BillingEntityID) (int64, error)
	// NextBillingEntitySequentialID returns the next per-billing-entity
	// sequence over non-self-billed invoices.
	NextBillingEntitySequentialID(ctx context.Context, org core.OrganizationID, billingEntity core.BillingEntityID) (int64, error)
	// DeleteDraft deletes the draft id; a non-draft fails with
	// core.ErrConflict and a missing one with core.ErrNotFound.
	DeleteDraft(ctx context.Context, org core.OrganizationID, id core.InvoiceID) error
}

// SubscriptionRepository stores invoice subscriptions, the idempotency
// records of subscription billing (§11.1). Every method is scoped to an
// organization.
type SubscriptionRepository interface {
	// Create inserts s; a row one of whose [Subscription.Keys] is already
	// used fails with core.ErrConflict.
	Create(ctx context.Context, s *Subscription) error
	// ListByInvoice returns the rows of one invoice in creation order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*Subscription, error)
	// ListBySubscription returns the rows of one subscription in creation
	// order.
	ListBySubscription(ctx context.Context, org core.OrganizationID, subscription core.SubscriptionID) ([]*Subscription, error)
	// Find returns a row with key k (see [Subscription.Keys]), or
	// core.ErrNotFound.
	Find(ctx context.Context, org core.OrganizationID, k IdempotencyKey) (*Subscription, error)
	// Last returns the row of the subscription with the latest period
	// start (then timestamp), or core.ErrNotFound.
	Last(ctx context.Context, org core.OrganizationID, subscription core.SubscriptionID) (*Subscription, error)
	// DeleteByInvoice deletes the rows of one invoice.
	DeleteByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) error
}

// FeeFilter narrows [FeeRepository.List]; zero fields do not filter.
type FeeFilter struct {
	// InvoiceID keeps the fees of one invoice.
	InvoiceID core.InvoiceID
	// SubscriptionID keeps the fees of one subscription.
	SubscriptionID core.SubscriptionID
	// ChargeID keeps the fees of one charge.
	ChargeID core.ChargeID
	// Types keeps the fees of one of these types.
	Types []core.FeeType
}

// FeeRepository stores fees with their applied taxes. Every method is
// scoped to an organization.
type FeeRepository interface {
	// Create inserts f.
	Create(ctx context.Context, f *Fee) error
	// Update replaces the stored fee f.ID with f.
	Update(ctx context.Context, f *Fee) error
	// Get returns the fee id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.FeeID) (*Fee, error)
	// List returns the fees matching f in creation order.
	List(ctx context.Context, org core.OrganizationID, f FeeFilter, page core.PageRequest) (core.Page[*Fee], error)
	// ListByInvoice returns all fees of one invoice in creation order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*Fee, error)
	// ListBySubscriptionPeriod returns the fees of a subscription whose
	// properties start at from, in creation order.
	ListBySubscriptionPeriod(ctx context.Context, org core.OrganizationID, subscription core.SubscriptionID, from time.Time) ([]*Fee, error)
	// ListUnbilledInAdvance returns the pay-in-advance fees not attached to
	// an invoice, of an invoiceable charge, with a succeeded payment and
	// created before before, in creation order; callers regroup them per
	// subscription and period with [GroupUnbilled].
	ListUnbilledInAdvance(ctx context.Context, org core.OrganizationID, before time.Time) ([]*Fee, error)
	// ListByCharge returns the fees of one charge in creation order.
	ListByCharge(ctx context.Context, org core.OrganizationID, charge core.ChargeID) ([]*Fee, error)
	// ListByEventTransaction returns the pay-in-advance fees created for an
	// event transaction ID of a subscription.
	ListByEventTransaction(ctx context.Context, org core.OrganizationID, subscription core.SubscriptionID, transactionID string) ([]*Fee, error)
	// DeleteByInvoice deletes the fees of one invoice.
	DeleteByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) error
}

// AppliedTaxRepository stores invoice applied taxes. Fee applied taxes are
// stored with their fee ([Fee.AppliedTaxes]).
type AppliedTaxRepository interface {
	// ReplaceForInvoice replaces the applied taxes of one invoice by taxes.
	ReplaceForInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID, taxes []*AppliedTax) error
	// ListByInvoice returns the applied taxes of one invoice in creation
	// order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*AppliedTax, error)
}

// AdjustedFeeRepository stores the adjustments of draft fees (§12.3).
type AdjustedFeeRepository interface {
	// Create inserts a; a second adjustment of the same fee fails with
	// core.ErrConflict.
	Create(ctx context.Context, a *AdjustedFee) error
	// ListByInvoice returns the adjustments of one invoice in creation order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*AdjustedFee, error)
	// Delete deletes the adjustment id, or fails with core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.AdjustedFeeID) error
	// DeleteByInvoice deletes the adjustments of one invoice.
	DeleteByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) error
}

// SettlementRepository stores invoice settlements.
type SettlementRepository interface {
	// Create inserts s.
	Create(ctx context.Context, s *Settlement) error
	// ListByInvoice returns the settlements of one invoice in creation order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*Settlement, error)
}

// CustomSectionRepository stores invoice custom sections.
type CustomSectionRepository interface {
	// Create inserts s; a code already in use fails with core.ErrConflict.
	Create(ctx context.Context, s *CustomSection) error
	// Update replaces the stored section s.ID with s.
	Update(ctx context.Context, s *CustomSection) error
	// Get returns the section id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID) (*CustomSection, error)
	// GetByCode returns the section with that code, or core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*CustomSection, error)
	// ListByIDs returns the sections with these IDs in creation order, as
	// selected by [ResolveCustomSections]; unknown IDs are ignored.
	ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.InvoiceCustomSectionID) ([]*CustomSection, error)
	// List returns the organization's sections in creation order.
	List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*CustomSection], error)
	// Delete deletes the section id, or fails with core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID) error
}

// ErrorDetailRepository stores invoice error details.
type ErrorDetailRepository interface {
	// Create inserts e.
	Create(ctx context.Context, e *ErrorDetail) error
	// ListByInvoice returns the error details of one invoice in creation
	// order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*ErrorDetail, error)
	// DeleteByInvoice deletes the error details of one invoice with code
	// (all codes when code is "").
	DeleteByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID, code ErrorCode) error
}

// UnbilledGroup is the unbilled in-advance fees of one subscription and
// period, billed together by an advance-charges invoice.
type UnbilledGroup struct {
	// SubscriptionID is the subscription of the fees.
	SubscriptionID core.SubscriptionID
	// From is the start of the fees' charges period.
	From time.Time
	// Currency is the fees' currency.
	Currency money.Currency
	// Fees are the fees in input order.
	Fees []*Fee
}

// GroupUnbilled regroups fees per subscription, charges period start and
// currency, in order of first appearance.
func GroupUnbilled(fees []*Fee) []UnbilledGroup {
	var groups []UnbilledGroup
	for _, f := range fees {
		from := f.Properties.ChargesFrom
		if from.IsZero() {
			from = f.Properties.From
		}
		i := -1
		for j := range groups {
			g := &groups[j]
			if g.SubscriptionID == f.SubscriptionID && g.From.Equal(from) && g.Currency == f.Currency {
				i = j
				break
			}
		}
		if i < 0 {
			groups = append(groups, UnbilledGroup{SubscriptionID: f.SubscriptionID, From: from, Currency: f.Currency})
			i = len(groups) - 1
		}
		groups[i].Fees = append(groups[i].Fees, f)
	}
	return groups
}
