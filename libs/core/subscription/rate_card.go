package subscription

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Rule codes of contracts and their rate cards (lago's names).
const (
	// CodeContractLocked: the contract is no longer pending, so its
	// pricing is signed (or it is finished and read-only).
	CodeContractLocked = "contract_locked"
	// CodeCannotTerminate: the contract is already terminated or
	// canceled.
	CodeCannotTerminate = "cannot_terminate"
	// CodeAlreadyEnded: the requested end is not in the future.
	CodeAlreadyEnded = "already_ended"
	// CodeActiveContractExists: a pending contract cannot start while an
	// active contract shares its external ID.
	CodeActiveContractExists = "active_contract_exists"
	// CodeInvalidBillingSchedule: the rate card's schedule cannot be
	// built from its rates and phases.
	CodeInvalidBillingSchedule = "invalid_billing_schedule"
	// CodeOverlappingPeriods: a billing segment overlaps one already
	// stored for its rate card.
	CodeOverlappingPeriods = "overlapping_periods"
)

// RateCard is a rate card applied to a contract (lago ContractRateCard):
// the card's prices on the card's own billing schedule, from EffectiveDate
// to the contract's end (the contract's EndingAt is the only end of its
// cards). Its phases are [catalog.RatePhase] rows owned by the card
// (SubscriptionRateCardID). At most one kept card per contract and rate
// card.
type RateCard struct {
	// ID identifies the contract rate card.
	ID core.SubscriptionRateCardID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the contract.
	SubscriptionID core.SubscriptionID
	// RateCardID is the applied rate card.
	RateCardID core.RateCardID
	// Units is the quantity billed, for fixed products; nil bills none.
	Units *money.Decimal
	// EffectiveDate is the day the card starts, in the customer's time
	// zone: the contract's start day.
	EffectiveDate core.Date
	// BillingAnchorDate anchors the card's billing intervals: its own, or
	// the contract's effective anchor.
	BillingAnchorDate core.Date
	// NextBillingAt is when the card next has a segment to bill; nil once
	// its schedule is exhausted.
	NextBillingAt *time.Time
	// CreatedAt is when the card was applied.
	CreatedAt time.Time
	// UpdatedAt is when the card last changed.
	UpdatedAt time.Time
	// DeletedAt is when the card was discarded; nil while kept.
	DeletedAt *time.Time
}

// Validate checks the card's own fields.
//
// source: app/models/contract_rate_card.rb:20-26
func (c *RateCard) Validate() error {
	var v core.Validator
	v.Check(!c.EffectiveDate.IsZero(), "effective_date", core.CodeRequired)
	v.Check(!c.BillingAnchorDate.IsZero(), "billing_anchor_date", core.CodeRequired)
	if c.Units != nil {
		v.Check(!c.Units.IsNegative(), "units", core.CodeOutOfRange)
	}
	return v.Err()
}

// InheritedBillingAnchor reports whether the card's anchor is the one it
// inherits from its contract: the contract's explicit anchor
// (contractAnchor), else the card's start day. Any other anchor was set on
// the card and survives a reseed.
//
// source: app/models/contract_rate_card.rb:46-49
func (c *RateCard) InheritedBillingAnchor(contractAnchor *core.Date) bool {
	if contractAnchor != nil {
		return c.BillingAnchorDate == *contractAnchor
	}
	return c.BillingAnchorDate == c.EffectiveDate
}

// RateCardQuery narrows [RateCardRepository.List] and
// [RateCardRepository.Exists]; zero fields do not filter.
type RateCardQuery struct {
	// SubscriptionID keeps the cards of the contract.
	SubscriptionID core.SubscriptionID
	// RateCardID keeps the cards applying the rate card.
	RateCardID core.RateCardID
	// ProductID keeps the cards whose rate card prices the product.
	ProductID core.ProductID
	// ProductFilterID keeps the cards whose rate card is scoped to the
	// product filter.
	ProductFilterID core.ProductFilterID
	// ProductCategoryID keeps the cards whose rate card's product is in
	// the category.
	ProductCategoryID core.ProductCategoryID
}

// RateCardRepository stores contract rate cards and their phases. Every
// method is scoped to an organization; the reads return kept cards only,
// except Get.
type RateCardRepository interface {
	// Create inserts c with its phases; a second kept card of the same
	// rate card on the contract fails with core.ErrConflict.
	Create(ctx context.Context, c *RateCard, phases []*catalog.RatePhase) error
	// Update replaces the stored card c.ID with c.
	Update(ctx context.Context, c *RateCard) error
	// Discard discards the card id and its phases at at.
	Discard(ctx context.Context, org core.OrganizationID, id core.SubscriptionRateCardID, at time.Time) error
	// Get returns the card id, kept or discarded, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.SubscriptionRateCardID) (*RateCard, error)
	// GetByRateCard returns the contract's kept card of the rate card, or
	// core.ErrNotFound.
	GetByRateCard(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, rateCard core.RateCardID) (*RateCard, error)
	// ListBySubscription returns the contract's kept cards by effective
	// date, then ID.
	ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*RateCard, error)
	// List returns the kept cards matching q by effective date, then ID
	// (lago ContractRateCardsQuery).
	List(ctx context.Context, org core.OrganizationID, q RateCardQuery, page core.PageRequest) (core.Page[*RateCard], error)
	// Exists reports whether a kept card matches q.
	Exists(ctx context.Context, org core.OrganizationID, q RateCardQuery) (bool, error)
	// ListDueForBilling returns the customer's kept cards due at t (lago
	// ContractRateCard.due_for_billing): next_billing_at ≤ t, the contract
	// active and started by t, the customer not deleted, the rate card
	// priced (it has a kept rate), and the contract not ended by the
	// card's start (its effective date's midnight in the customer's time
	// zone, else its billing entity's, else UTC). Ordered by ID.
	ListDueForBilling(ctx context.Context, org core.OrganizationID, customer core.CustomerID, t time.Time) ([]*RateCard, error)
	// CustomersDueForBilling returns the customers having a card due at t
	// (as ListDueForBilling), ordered by ID.
	CustomersDueForBilling(ctx context.Context, org core.OrganizationID, t time.Time) ([]core.CustomerID, error)
	// ListPhases returns the kept phases of the card id by position.
	ListPhases(ctx context.Context, org core.OrganizationID, id core.SubscriptionRateCardID) ([]*catalog.RatePhase, error)
	// SavePhases makes phases the kept phases of the card id: a phase
	// left out is discarded at at, the others inserted or updated.
	SavePhases(ctx context.Context, org core.OrganizationID, id core.SubscriptionRateCardID, phases []*catalog.RatePhase, at time.Time) error
}
