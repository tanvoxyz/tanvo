package quote

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Filter narrows [Repository.List] (lago QuotesQuery,
// app/queries/quotes_query.rb:5-83). Each field left zero keeps all.
type Filter struct {
	// CustomerIDs keeps the quotes of these customers.
	CustomerIDs []core.CustomerID
	// ExternalCustomerIDs keeps the quotes of the customers with these
	// external ids.
	ExternalCustomerIDs []string
	// Numbers keeps the quotes with these numbers.
	Numbers []string
	// Statuses keeps the quotes whose current (latest) version has one of
	// these statuses.
	Statuses []VersionStatus
	// From and To keep the quotes created in [From, To].
	From, To *time.Time
	// OwnerIDs keeps the quotes owned by one of these members.
	OwnerIDs []core.MembershipID
	// OrderTypes keeps quotes of these order types.
	OrderTypes []OrderType
}

// Repository stores quotes.
type Repository interface {
	// Create inserts q; a number or sequential id already used in the
	// organization fails with core.ErrConflict.
	Create(ctx context.Context, q *Quote) error
	// Update replaces the stored quote q.ID with q.
	Update(ctx context.Context, q *Quote) error
	// Get returns the quote id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.QuoteID) (*Quote, error)
	// GetByNumber returns the quote with that number, or core.ErrNotFound.
	GetByNumber(ctx context.Context, org core.OrganizationID, number string) (*Quote, error)
	// NextSequence returns the next sequential id of the organization's
	// quotes.
	NextSequence(ctx context.Context, org core.OrganizationID) (int, error)
	// List returns the organization's quotes, newest first.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Quote], error)
}

// VersionRepository stores quote versions.
type VersionRepository interface {
	// Create inserts v; a version number already used in the quote, or a
	// second draft or approved version ([CheckSingleActive]), fails with
	// core.ErrConflict.
	Create(ctx context.Context, v *Version) error
	// Update replaces the stored version v.ID with v (same rules as
	// Create).
	Update(ctx context.Context, v *Version) error
	// Get returns the version id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.QuoteVersionID) (*Version, error)
	// ListByQuote returns the versions of a quote, latest version first.
	ListByQuote(ctx context.Context, org core.OrganizationID, q core.QuoteID) ([]*Version, error)
	// GetCurrent returns the latest version of a quote, or
	// core.ErrNotFound.
	GetCurrent(ctx context.Context, org core.OrganizationID, q core.QuoteID) (*Version, error)
}

// OrderFormFilter narrows [OrderFormRepository.List] (lago
// OrderFormsQuery, app/queries/order_forms_query.rb:3-112). Each field
// left zero keeps all.
type OrderFormFilter struct {
	// CustomerID keeps the order forms of one customer.
	CustomerID core.CustomerID
	// Statuses keeps order forms in these statuses.
	Statuses []OrderFormStatus
	// Number keeps the order form with this number.
	Number string
	// QuoteNumber keeps the order forms of the quote with this number.
	QuoteNumber string
	// OwnerID keeps the order forms of the quotes this member owns.
	OwnerID core.MembershipID
	// CreatedFrom and CreatedTo bound the creation instant (inclusive).
	CreatedFrom, CreatedTo *time.Time
	// ExpiresFrom and ExpiresTo bound the expiry instant (inclusive).
	ExpiresFrom, ExpiresTo *time.Time
	// Search keeps the order forms whose number contains it (ignoring
	// case).
	Search string
}

// OrderFormRepository stores order forms.
type OrderFormRepository interface {
	// Create inserts f; a number or sequential id already used, or a
	// second order form of the version, fails with core.ErrConflict.
	Create(ctx context.Context, f *OrderForm) error
	// Update replaces the stored order form f.ID with f.
	Update(ctx context.Context, f *OrderForm) error
	// Get returns the order form id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.OrderFormID) (*OrderForm, error)
	// GetByNumber returns the order form with that number, or
	// core.ErrNotFound.
	GetByNumber(ctx context.Context, org core.OrganizationID, number string) (*OrderForm, error)
	// GetByVersion returns the order form of a quote version, or
	// core.ErrNotFound.
	GetByVersion(ctx context.Context, org core.OrganizationID, v core.QuoteVersionID) (*OrderForm, error)
	// NextSequence returns the next sequential id of the organization's
	// order forms.
	NextSequence(ctx context.Context, org core.OrganizationID) (int, error)
	// List returns the organization's order forms, newest first.
	List(ctx context.Context, org core.OrganizationID, f OrderFormFilter, page core.PageRequest) (core.Page[*OrderForm], error)
	// ListExpiring returns up to limit generated order forms of every
	// organization whose expires_at is before before; the expiry job then
	// applies [OrderForm.Expirable] in each customer's time zone.
	ListExpiring(ctx context.Context, before time.Time, limit int) ([]*OrderForm, error)
}

// OrderFilter narrows [OrderRepository.List] (lago OrdersQuery,
// app/queries/orders_query.rb:3-108). Each field left zero keeps all.
type OrderFilter struct {
	// CustomerID keeps the orders of one customer.
	CustomerID core.CustomerID
	// Statuses keeps orders in these statuses.
	Statuses []OrderStatus
	// OrderTypes keeps the orders of quotes of these order types.
	OrderTypes []OrderType
	// ExecutionModes keeps orders in these execution modes.
	ExecutionModes []ExecutionMode
	// Number keeps the order with this number.
	Number string
	// OrderFormNumber keeps the order of the order form with this number.
	OrderFormNumber string
	// QuoteNumber keeps the orders of the quote with this number.
	QuoteNumber string
	// OwnerID keeps the orders of the quotes this member owns.
	OwnerID core.MembershipID
	// ExecutedFrom and ExecutedTo bound the execution instant (inclusive).
	ExecutedFrom, ExecutedTo *time.Time
	// Search keeps the orders whose number contains it (ignoring case).
	Search string
}

// OrderRepository stores orders.
type OrderRepository interface {
	// Create inserts o; a number or sequential id already used, or a
	// second order of the order form, fails with core.ErrConflict.
	Create(ctx context.Context, o *Order) error
	// Update replaces the stored order o.ID with o.
	Update(ctx context.Context, o *Order) error
	// Get returns the order id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.OrderID) (*Order, error)
	// GetByNumber returns the order with that number, or core.ErrNotFound.
	GetByNumber(ctx context.Context, org core.OrganizationID, number string) (*Order, error)
	// NextSequence returns the next sequential id of the organization's
	// orders.
	NextSequence(ctx context.Context, org core.OrganizationID) (int, error)
	// List returns the organization's orders, newest first.
	List(ctx context.Context, org core.OrganizationID, f OrderFilter, page core.PageRequest) (core.Page[*Order], error)
	// ListExecutable returns up to limit orders of every organization
	// whose scheduled execution is due at now ([Order.Executable]).
	ListExecutable(ctx context.Context, now time.Time, limit int) ([]*Order, error)
}
