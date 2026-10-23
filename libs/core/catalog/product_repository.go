package catalog

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// ProductCategoryQuery narrows [ProductCategoryRepository.List].
type ProductCategoryQuery struct {
	// Search keeps categories whose name or code contains the trimmed term
	// (case-insensitive).
	Search string
}

// ProductCategoryRepository stores product categories. Every method is
// scoped to an organization.
//
// The product catalog is soft-deleted (lago discard on deleted_at): a
// discarded row keeps its row with DeletedAt set, Get still returns it for
// history, and every other read sees kept rows only; codes are unique
// among kept rows. A discard cascades as lago's destroy services do.
type ProductCategoryRepository interface {
	// Create inserts c; a code already used fails with core.ErrConflict.
	Create(ctx context.Context, c *ProductCategory) error
	// Update replaces the stored category c.ID with c (same uniqueness
	// rule).
	Update(ctx context.Context, c *ProductCategory) error
	// Discard discards the kept category id at at with its kept products,
	// as [ProductRepository.Discard] does; a missing or discarded one fails
	// with core.ErrNotFound.
	//
	// source: app/services/product_categories/destroy_service.rb:22-28
	Discard(ctx context.Context, org core.OrganizationID, id core.ProductCategoryID, at time.Time) error
	// Get returns the category id, kept or discarded, or core.ErrNotFound;
	// callers serving kept categories check DeletedAt.
	Get(ctx context.Context, org core.OrganizationID, id core.ProductCategoryID) (*ProductCategory, error)
	// GetByCode returns the kept category with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*ProductCategory, error)
	// List returns the kept categories matching q, newest first, then by
	// ID.
	List(ctx context.Context, org core.OrganizationID, q ProductCategoryQuery, page core.PageRequest) (core.Page[*ProductCategory], error)
}

// ProductQuery narrows [ProductRepository.List] and
// [ProductRepository.Count].
type ProductQuery struct {
	// Search keeps products whose name or code contains the trimmed term
	// (case-insensitive).
	Search string
	// ProductCategoryIDs keeps products of these categories.
	ProductCategoryIDs []core.ProductCategoryID
	// WithoutProductCategory keeps uncategorized products; combined with
	// ProductCategoryIDs, either matches.
	WithoutProductCategory bool
	// ProductType keeps products of that type; empty keeps every type.
	ProductType ProductType
}

// ProductRepository stores products. Every method is scoped to an
// organization; products are soft-deleted as categories are.
type ProductRepository interface {
	// Create inserts p; a code already used fails with core.ErrConflict.
	Create(ctx context.Context, p *Product) error
	// Update replaces the stored product p.ID with p (same uniqueness
	// rule).
	Update(ctx context.Context, p *Product) error
	// Discard discards the kept product id at at with the values of its
	// filters, its filters, the rates of its rate cards and its rate cards;
	// a missing or discarded one fails with core.ErrNotFound.
	//
	// source: app/services/products/destroy_service.rb:20-26
	Discard(ctx context.Context, org core.OrganizationID, id core.ProductID, at time.Time) error
	// Get returns the product id, kept or discarded, or core.ErrNotFound;
	// callers serving kept products check DeletedAt.
	Get(ctx context.Context, org core.OrganizationID, id core.ProductID) (*Product, error)
	// GetByCode returns the kept product with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Product, error)
	// List returns the kept products matching q, newest first, then by ID.
	List(ctx context.Context, org core.OrganizationID, q ProductQuery, page core.PageRequest) (core.Page[*Product], error)
	// Count returns the number of kept products matching q.
	Count(ctx context.Context, org core.OrganizationID, q ProductQuery) (int, error)
}

// ProductFilterRepository stores product filters with their values. Every
// method is scoped to an organization; reads fill each value's Key from
// its metric filter. Filters and their values are soft-deleted as
// categories are; reads return the kept values only.
type ProductFilterRepository interface {
	// Create inserts f with its values; a code already used within the
	// product fails with core.ErrConflict.
	Create(ctx context.Context, f *ProductFilter) error
	// Update replaces the stored kept filter f.ID with f (same uniqueness
	// rule). Changed values discard the kept ones at f.UpdatedAt and insert
	// f.Values (lago replace_values).
	//
	// source: app/services/product_filters/update_service.rb:82-92
	Update(ctx context.Context, f *ProductFilter) error
	// Discard discards the kept filter id at at with the rates of the rate
	// cards scoped to it, those cards and its values; a missing or
	// discarded one fails with core.ErrNotFound.
	//
	// source: app/services/product_filters/destroy_service.rb:17-26
	Discard(ctx context.Context, org core.OrganizationID, id core.ProductFilterID, at time.Time) error
	// Get returns the filter id, kept or discarded, or core.ErrNotFound;
	// callers serving kept filters check DeletedAt.
	Get(ctx context.Context, org core.OrganizationID, id core.ProductFilterID) (*ProductFilter, error)
	// GetByCode returns the product's kept filter with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, product core.ProductID, code string) (*ProductFilter, error)
	// List returns the product's kept filters whose name or code contains
	// search (every filter when blank) in creation order.
	List(ctx context.Context, org core.OrganizationID, product core.ProductID, search string, page core.PageRequest) (core.Page[*ProductFilter], error)
	// ListAll returns every kept filter of the product in creation order.
	ListAll(ctx context.Context, org core.OrganizationID, product core.ProductID) ([]*ProductFilter, error)
	// Count returns the number of the product's kept filters.
	Count(ctx context.Context, org core.OrganizationID, product core.ProductID) (int, error)
	// ReferencesMetric reports whether a kept filter value selects a kept
	// filter of the metric id.
	ReferencesMetric(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (bool, error)
	// MetricReferences returns the distinct kept filter values that select
	// a kept filter of the metric id (Key is not filled).
	MetricReferences(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) ([]ProductFilterValue, error)
}

// RateCardQuery narrows [RateCardRepository.List].
type RateCardQuery struct {
	// Search keeps cards whose name or code contains the trimmed term
	// (case-insensitive).
	Search string
	// ProductIDs keeps cards of these products.
	ProductIDs []core.ProductID
	// ProductFilterIDs keeps cards of these product filters.
	ProductFilterIDs []core.ProductFilterID
	// ProductCategoryIDs keeps cards of the products of these categories.
	ProductCategoryIDs []core.ProductCategoryID
	// WithoutProductCategory keeps cards of uncategorized products;
	// combined with ProductCategoryIDs, either matches.
	WithoutProductCategory bool
}

// RateCardRepository stores rate cards with their taxes. Every method is
// scoped to an organization; cards are soft-deleted as categories are.
type RateCardRepository interface {
	// Create inserts c with its taxes; a code already used fails with
	// core.ErrConflict.
	Create(ctx context.Context, c *RateCard) error
	// Update replaces the stored card c.ID, taxes included, with c (same
	// uniqueness rule).
	Update(ctx context.Context, c *RateCard) error
	// Discard discards the kept card id at at with its rates; a missing or
	// discarded one fails with core.ErrNotFound.
	//
	// source: app/services/rate_cards/destroy_service.rb:17-20
	Discard(ctx context.Context, org core.OrganizationID, id core.RateCardID, at time.Time) error
	// Get returns the card id, kept or discarded, or core.ErrNotFound;
	// callers serving kept cards check DeletedAt.
	Get(ctx context.Context, org core.OrganizationID, id core.RateCardID) (*RateCard, error)
	// GetByCode returns the kept card with that code, or core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*RateCard, error)
	// List returns the kept cards matching q, newest first, then by ID.
	List(ctx context.Context, org core.OrganizationID, q RateCardQuery, page core.PageRequest) (core.Page[*RateCard], error)
}

// RateRepository stores the rates of rate cards. Every method is scoped
// to an organization; rates are soft-deleted as categories are, and codes
// and effective instants are unique among a card's kept rates.
type RateRepository interface {
	// Create inserts r; a code or effective instant already used on the
	// card fails with core.ErrConflict.
	Create(ctx context.Context, r *Rate) error
	// Update replaces the stored rate r.ID with r (same uniqueness rules).
	Update(ctx context.Context, r *Rate) error
	// Discard discards the kept rate id at at; a missing or discarded one
	// fails with core.ErrNotFound.
	Discard(ctx context.Context, org core.OrganizationID, id core.RateCardRateID, at time.Time) error
	// Get returns the rate id, kept or discarded, or core.ErrNotFound;
	// callers serving kept rates check DeletedAt.
	Get(ctx context.Context, org core.OrganizationID, id core.RateCardRateID) (*Rate, error)
	// ListByRateCard returns the card's kept rates, latest effective first.
	ListByRateCard(ctx context.Context, org core.OrganizationID, card core.RateCardID) ([]*Rate, error)
}

// PlanRateCardQuery narrows [PlanRateCardRepository.List] and
// [PlanRateCardRepository.PlanIDs]; every set field must hold.
type PlanRateCardQuery struct {
	// PlanID keeps the plan's rate cards.
	PlanID core.PlanID
	// RateCardID keeps the applications of that card.
	RateCardID core.RateCardID
	// ProductID keeps the applications of the product's cards.
	ProductID core.ProductID
	// ProductFilterID keeps the applications of the filter's cards.
	ProductFilterID core.ProductFilterID
	// ProductCategoryID keeps the applications of the cards of the
	// category's products.
	ProductCategoryID core.ProductCategoryID
}

// PlanRateCardRepository stores the rate cards applied to plans, with
// their phases. Every method is scoped to an organization; plan rate cards
// and phases (with their embedded overrides) are soft-deleted as
// categories are, and reads join kept rate cards and products only.
type PlanRateCardRepository interface {
	// Create inserts p with its phases; a card applied to the plan
	// already (kept) fails with core.ErrConflict.
	Create(ctx context.Context, p *PlanRateCard, phases []*RatePhase) error
	// Update replaces the stored kept plan rate card p.ID with p.
	Update(ctx context.Context, p *PlanRateCard) error
	// Discard discards the kept plan rate card id at at with its phases
	// and their overrides; a missing or discarded one fails with
	// core.ErrNotFound.
	//
	// source: app/services/plan_rate_cards/destroy_service.rb:19-24
	Discard(ctx context.Context, org core.OrganizationID, id core.PlanRateCardID, at time.Time) error
	// DiscardByPlan discards every kept plan rate card of the plan at at,
	// as Discard does, and returns how many it discarded.
	//
	// source: app/services/catalog_plans/destroy_service.rb:20-24
	DiscardByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID, at time.Time) (int, error)
	// Get returns the plan rate card id, kept or discarded, or
	// core.ErrNotFound; callers serving kept ones check DeletedAt.
	Get(ctx context.Context, org core.OrganizationID, id core.PlanRateCardID) (*PlanRateCard, error)
	// GetByRateCard returns the plan's kept application of the card, or
	// core.ErrNotFound.
	GetByRateCard(ctx context.Context, org core.OrganizationID, plan core.PlanID, card core.RateCardID) (*PlanRateCard, error)
	// List returns the kept plan rate cards matching q in creation order.
	List(ctx context.Context, org core.OrganizationID, q PlanRateCardQuery, page core.PageRequest) (core.Page[*PlanRateCard], error)
	// PlanIDs returns the distinct plans of the kept plan rate cards
	// matching q.
	PlanIDs(ctx context.Context, org core.OrganizationID, q PlanRateCardQuery) ([]core.PlanID, error)
	// ListPhases returns the kept phases of the plan rate card id by
	// position.
	ListPhases(ctx context.Context, org core.OrganizationID, id core.PlanRateCardID) ([]*RatePhase, error)
	// SavePhases makes phases the kept phases of the plan rate card id: a
	// kept phase missing from phases is discarded at at, one present is
	// updated in place and a new one inserted. A code or position used
	// twice fails with core.ErrConflict.
	SavePhases(ctx context.Context, org core.OrganizationID, id core.PlanRateCardID, phases []*RatePhase, at time.Time) error
}
