package customer

import (
	"context"
	"fmt"
	"math"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Filter narrows [Repository.List] (the customer list of the API); zero
// fields do not filter.
type Filter struct {
	// ExternalID keeps the customer with that external ID; when set,
	// Search is ignored.
	ExternalID string
	// BillingEntityIDs keeps customers of these billing entities.
	BillingEntityIDs []core.BillingEntityID
	// AccountTypes keeps customers of one of these account types.
	AccountTypes []AccountType
	// CustomerType keeps customers of that type.
	CustomerType Type
	// HasCustomerType, when set, keeps customers with (true) or without
	// (false) a customer type.
	HasCustomerType *bool
	// Countries keeps customers whose billing address is in one of these
	// countries (ISO 3166-1 alpha-2 codes).
	Countries []string
	// States keeps customers whose billing address state is one of these.
	States []string
	// Zipcodes keeps customers whose billing address zipcode is one of
	// these.
	Zipcodes []string
	// Currencies keeps customers with one of these currencies.
	Currencies []money.Currency
	// HasTaxIdentificationNumber, when set, keeps customers with (true) or
	// without (false) a tax identification number.
	HasTaxIdentificationNumber *bool
	// Metadata keeps customers whose metadata matches the filter.
	Metadata core.MetadataFilter
	// ActiveSubscriptionsCountFrom and ActiveSubscriptionsCountTo keep
	// customers by their number of active subscriptions; see
	// [Filter.ActiveSubscriptionsRange].
	ActiveSubscriptionsCountFrom *int
	// ActiveSubscriptionsCountTo: see ActiveSubscriptionsCountFrom.
	ActiveSubscriptionsCountTo *int
	// Search keeps customers whose name, first name, last name, legal name,
	// external ID or email contains the trimmed term (case-insensitive,
	// with LIKE wildcards matched literally).
	Search string
	// IncludeDeleted also lists archived customers (and searches them).
	IncludeDeleted bool
	// Order is the list order; core.OrderRecent lists by creation instant,
	// newest first, then by ID ascending.
	Order core.ListOrder
}

// Validate checks the filter: declared enumeration values, ISO country
// codes, set currencies, non-negative counts, and no customer type when
// HasCustomerType is false.
func (f Filter) Validate() error {
	var v core.Validator
	for i, t := range f.AccountTypes {
		v.Enum(fmt.Sprintf("account_types[%d]", i), t)
	}
	v.OptionalEnum("customer_type", f.CustomerType)
	if f.HasCustomerType != nil && !*f.HasCustomerType && f.CustomerType != "" {
		v.Add("customer_type", core.CodeForbidden)
	}
	for i, c := range f.Countries {
		v.Check(core.ValidCountry(c), fmt.Sprintf("countries[%d]", i), core.CodeInvalid)
	}
	for i, c := range f.Currencies {
		v.Currency(fmt.Sprintf("currencies[%d]", i), c)
	}
	if f.ActiveSubscriptionsCountFrom != nil {
		v.Check(*f.ActiveSubscriptionsCountFrom >= 0, "active_subscriptions_count_from", core.CodeOutOfRange)
	}
	if f.ActiveSubscriptionsCountTo != nil {
		v.Check(*f.ActiveSubscriptionsCountTo >= 0, "active_subscriptions_count_to", core.CodeOutOfRange)
	}
	v.Check(f.Order.Valid(), "order", core.CodeInvalid)
	return v.Err()
}

// ActiveSubscriptionsRange returns the inclusive range of active
// subscription counts kept by the filter, and false when it does not
// filter by count. Equal bounds keep that exact count, two bounds keep the
// counts between them (inclusive), a lone From keeps the counts above it
// and a lone To the counts below it (both exclusive).
func (f Filter) ActiveSubscriptionsRange() (lo, hi int, ok bool) {
	from, to := f.ActiveSubscriptionsCountFrom, f.ActiveSubscriptionsCountTo
	switch {
	case from == nil && to == nil:
		return 0, 0, false
	case from != nil && to != nil:
		return *from, *to, true
	case from != nil:
		return *from + 1, math.MaxInt32, true
	default:
		return 0, *to - 1, true
	}
}

// Repository stores customers. Every method is scoped to an organization.
type Repository interface {
	// Create inserts c; an external ID used by another non-deleted
	// customer of the organization fails with core.ErrConflict.
	Create(ctx context.Context, c *Customer) error
	// Update replaces the stored customer c.ID with c (same uniqueness
	// rule as Create).
	Update(ctx context.Context, c *Customer) error
	// Get returns the customer id of org, archived or not, or
	// core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.CustomerID) (*Customer, error)
	// GetByExternalID returns the non-deleted customer with that external
	// ID, or core.ErrNotFound.
	GetByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (*Customer, error)
	// List returns the organization's customers matching f, in the order
	// f.Order; an invalid filter fails with core.ErrInvalidValue.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Customer], error)
	// ListForDunning returns the non-deleted customers that dunning may
	// consider (not excluded), in creation order: those with
	// AppliedDunningCampaignID = campaign, or, when campaign is zero, those
	// without a campaign of their own (falling back to their billing
	// entity's).
	ListForDunning(ctx context.Context, org core.OrganizationID, campaign core.DunningCampaignID, page core.PageRequest) (core.Page[*Customer], error)
	// ResetDunningCampaign clears the applied campaign and the attempt
	// counters of every customer that uses campaign (domain §16: deleting a
	// campaign resets its customers), returning how many changed.
	ResetDunningCampaign(ctx context.Context, org core.OrganizationID, campaign core.DunningCampaignID) (int, error)
}
