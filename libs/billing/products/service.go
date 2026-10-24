package products

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// Resource names of the not-found errors.
const (
	// ResourceProductCategory is a product category.
	ResourceProductCategory = "product_category"
	// ResourceProduct is a product.
	ResourceProduct = "product"
	// ResourceProductFilter is a product filter.
	ResourceProductFilter = "product_filter"
	// ResourceRateCard is a rate card.
	ResourceRateCard = "rate_card"
	// ResourceRate is a rate of a rate card.
	ResourceRate = "rate_card_rate"
	// ResourcePlanRateCard is a rate card applied to a plan.
	ResourcePlanRateCard = "plan_rate_card"
	// ResourceRatePhase is a phase of a plan rate card.
	ResourceRatePhase = "rate_phase"
)

// Service manages the product catalog. It uses d.Tx, d.Clock and d.IDs
// only: the catalog emits no webhook.
type Service struct {
	d *kernel.Deps
}

// New returns the service over d. It fails when a dependency the service
// uses is nil.
func New(d *kernel.Deps) (*Service, error) {
	switch {
	case d == nil, d.Tx == nil:
		return nil, fmt.Errorf("%w: Tx", kernel.ErrMissingDependency)
	case d.Clock == nil:
		return nil, fmt.Errorf("%w: Clock", kernel.ErrMissingDependency)
	case d.IDs == nil:
		return nil, fmt.Errorf("%w: IDs", kernel.ErrMissingDependency)
	}
	return &Service{d: d}, nil
}

func read[T any](ctx context.Context, d *kernel.Deps, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	return out, err
}

// fieldError is the validation error of one field.
func fieldError(field, code string) error {
	var v core.Validator
	v.Add(field, code)
	return v.Err()
}

// notFound turns core.ErrNotFound into core.NotFound(resource).
func notFound(err error, resource string) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(resource)
	}
	return err
}

// conflict turns a uniqueness conflict of the store into field code
// value_already_exist.
func conflict(err error, field string) error {
	if errors.Is(err, core.ErrConflict) {
		return fieldError(field, core.CodeAlreadyExists)
	}
	return err
}

// strip trims a code as lago does (params[:code]&.strip).
func strip(code string) string { return strings.TrimSpace(code) }

// lockKey takes the advisory lock scope:id, held until the transaction
// ends; catalog locks are outside the billing lock order.
func lockKey(ctx context.Context, r port.Repos, org core.OrganizationID, k port.LockKey) error {
	if err := r.Locks.Lock(ctx, org, k); err != nil {
		return fmt.Errorf("lock %s %s: %w", k.Scope, k.ID, err)
	}
	return nil
}

// attachedPlans returns the plans whose rate cards match q.
func attachedPlans(ctx context.Context, r port.Repos, org core.OrganizationID, q catalog.PlanRateCardQuery) ([]core.PlanID, error) {
	ids, err := r.PlanRateCards.PlanIDs(ctx, org, q)
	if err != nil {
		return nil, fmt.Errorf("list attached plans: %w", err)
	}
	return ids, nil
}

// attached reports whether a plan or a contract applies a rate card
// matching q (lago CatalogAttachable#attached_to_plan_or_subscription?).
//
// source: app/models/concerns/catalog_attachable.rb:4-6
func attached(ctx context.Context, r port.Repos, org core.OrganizationID, q catalog.PlanRateCardQuery) (bool, error) {
	ids, err := attachedPlans(ctx, r, org, q)
	if err != nil || len(ids) > 0 {
		return len(ids) > 0, err
	}
	return contractCardExists(ctx, r, org, q)
}

// contractCardExists reports whether a contract applies a rate card
// matching q directly.
func contractCardExists(ctx context.Context, r port.Repos, org core.OrganizationID, q catalog.PlanRateCardQuery) (bool, error) {
	ok, err := r.ContractRateCards.Exists(ctx, org, subscription.RateCardQuery{
		RateCardID:        q.RateCardID,
		ProductID:         q.ProductID,
		ProductFilterID:   q.ProductFilterID,
		ProductCategoryID: q.ProductCategoryID,
	})
	if err != nil {
		return false, fmt.Errorf("find contract rate cards: %w", err)
	}
	return ok, nil
}

// attachedToContracts reports whether a contract bills a rate card
// matching q: a rate card is attached when a contract applies it or when a
// catalog plan applying it has a contract; a product filter only when a
// contract applies one of its cards (lago RateCard#attached_to_subscriptions?,
// ProductFilter#attached_to_subscriptions?).
//
// source: app/models/rate_card.rb:107-110, app/models/product_filter.rb:19-21
func attachedToContracts(ctx context.Context, r port.Repos, org core.OrganizationID, q catalog.PlanRateCardQuery) (bool, error) {
	ok, err := contractCardExists(ctx, r, org, q)
	if err != nil || ok || q.RateCardID.IsZero() {
		return ok, err
	}
	plans, err := attachedPlans(ctx, r, org, catalog.PlanRateCardQuery{RateCardID: q.RateCardID})
	if err != nil {
		return false, err
	}
	for _, plan := range plans {
		if ok, err := planContracted(ctx, r, org, plan); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// planContracted reports whether a contract, of any status, uses the
// catalog plan (lago CatalogPlan#attached_to_contracts?).
//
// source: app/models/catalog_plan.rb:34-36
func planContracted(ctx context.Context, r port.Repos, org core.OrganizationID, plan core.PlanID) (bool, error) {
	ok, err := r.Contracts.ExistsForPlan(ctx, org, plan)
	if err != nil {
		return false, fmt.Errorf("find contracts of plan %s: %w", plan, err)
	}
	return ok, nil
}

// getCategory returns the kept category id.
func getCategory(ctx context.Context, r port.Repos, org core.OrganizationID, id core.ProductCategoryID) (*catalog.ProductCategory, error) {
	c, err := r.ProductCategories.Get(ctx, org, id)
	if err == nil && c.DeletedAt != nil {
		return nil, core.ErrNotFound
	}
	return c, err
}

// getProduct returns the kept product id.
func getProduct(ctx context.Context, r port.Repos, org core.OrganizationID, id core.ProductID) (*catalog.Product, error) {
	p, err := r.Products.Get(ctx, org, id)
	if err == nil && p.DeletedAt != nil {
		return nil, core.ErrNotFound
	}
	return p, err
}

// getFilter returns the kept product filter id.
func getFilter(ctx context.Context, r port.Repos, org core.OrganizationID, id core.ProductFilterID) (*catalog.ProductFilter, error) {
	f, err := r.ProductFilters.Get(ctx, org, id)
	if err == nil && f.DeletedAt != nil {
		return nil, core.ErrNotFound
	}
	return f, err
}

// getRateCard returns the kept rate card id.
func getRateCard(ctx context.Context, r port.Repos, org core.OrganizationID, id core.RateCardID) (*catalog.RateCard, error) {
	c, err := r.RateCards.Get(ctx, org, id)
	if err == nil && c.DeletedAt != nil {
		return nil, core.ErrNotFound
	}
	return c, err
}

// getPlanRateCard returns the kept plan rate card id.
func getPlanRateCard(ctx context.Context, r port.Repos, org core.OrganizationID, id core.PlanRateCardID) (*catalog.PlanRateCard, error) {
	p, err := r.PlanRateCards.Get(ctx, org, id)
	if err == nil && p.DeletedAt != nil {
		return nil, core.ErrNotFound
	}
	return p, err
}

// orgLocation returns the time zone of the organization: its default
// billing entity's (lago Organization#timezone), UTC without one.
//
// source: app/models/organization.rb:292-295
func orgLocation(ctx context.Context, r port.Repos, org core.OrganizationID) (*time.Location, error) {
	b, err := r.BillingEntities.GetDefault(ctx, org)
	if errors.Is(err, core.ErrNotFound) {
		return time.UTC, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load default billing entity: %w", err)
	}
	if b.Timezone == nil {
		return time.UTC, nil
	}
	return b.Timezone, nil
}

// beforeToday reports whether t is before the start of today in loc (lago
// Utils::Datetime.before_today?).
//
// source: app/services/utils/datetime.rb:55-62
func beforeToday(t, now time.Time, loc *time.Location) bool {
	n := now.In(loc)
	return t.Before(time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc))
}

// categories finds the organization's categories (resource
// "product_category").
func categories(r port.Repos, org core.OrganizationID) refs.Finder[core.ProductCategoryID, *catalog.ProductCategory] {
	return refs.Finder[core.ProductCategoryID, *catalog.ProductCategory]{
		Resource: ResourceProductCategory,
		Get: func(ctx context.Context, id core.ProductCategoryID) (*catalog.ProductCategory, error) {
			return getCategory(ctx, r, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*catalog.ProductCategory, error) {
			return r.ProductCategories.GetByCode(ctx, org, code)
		},
		ID: func(c *catalog.ProductCategory) core.ProductCategoryID { return c.ID },
	}
}

// productsOf finds the organization's products (resource "product").
func productsOf(r port.Repos, org core.OrganizationID) refs.Finder[core.ProductID, *catalog.Product] {
	return refs.Finder[core.ProductID, *catalog.Product]{
		Resource: ResourceProduct,
		Get: func(ctx context.Context, id core.ProductID) (*catalog.Product, error) {
			return getProduct(ctx, r, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*catalog.Product, error) {
			return r.Products.GetByCode(ctx, org, code)
		},
		ID: func(p *catalog.Product) core.ProductID { return p.ID },
	}
}

// filtersOf finds the filters of the product (resource "product_filter");
// a filter of another product is not found.
func filtersOf(r port.Repos, org core.OrganizationID, product core.ProductID) refs.Finder[core.ProductFilterID, *catalog.ProductFilter] {
	return refs.Finder[core.ProductFilterID, *catalog.ProductFilter]{
		Resource: ResourceProductFilter,
		Get: func(ctx context.Context, id core.ProductFilterID) (*catalog.ProductFilter, error) {
			f, err := getFilter(ctx, r, org, id)
			if err == nil && f.ProductID != product {
				return nil, core.ErrNotFound
			}
			return f, err
		},
		GetByCode: func(ctx context.Context, code string) (*catalog.ProductFilter, error) {
			return r.ProductFilters.GetByCode(ctx, org, product, code)
		},
		ID: func(f *catalog.ProductFilter) core.ProductFilterID { return f.ID },
	}
}

// rateCardsOf finds the organization's rate cards (resource "rate_card").
func rateCardsOf(r port.Repos, org core.OrganizationID) refs.Finder[core.RateCardID, *catalog.RateCard] {
	return refs.Finder[core.RateCardID, *catalog.RateCard]{
		Resource: ResourceRateCard,
		Get: func(ctx context.Context, id core.RateCardID) (*catalog.RateCard, error) {
			return getRateCard(ctx, r, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*catalog.RateCard, error) {
			return r.RateCards.GetByCode(ctx, org, code)
		},
		ID: func(c *catalog.RateCard) core.RateCardID { return c.ID },
	}
}

// productLock is the lock of the product (lago product.with_lock).
func productLock(id core.ProductID) port.LockKey {
	return port.LockKey{Scope: "product", ID: id.UUID()}
}

// planLock is the lock of the plan's rate cards (lago
// catalog_plan.with_lock).
func planLock(id core.PlanID) port.LockKey {
	return port.LockKey{Scope: "catalog_plan", ID: id.UUID()}
}

// planRateCardLock is the lock of the plan rate card's phases (lago
// applied_rate_card.with_lock).
func planRateCardLock(id core.PlanRateCardID) port.LockKey {
	return port.LockKey{Scope: "plan_rate_card", ID: id.UUID()}
}

// isNotFound reports whether err is a missing object of the store.
func isNotFound(err error) bool { return errors.Is(err, core.ErrNotFound) }

// logChange records the activity log t of the catalog object obj, the
// resource res with ID id, whose attributes before an update are before.
// A catalog object's log names no customer (lago
// IGNORED_EXTERNAL_CUSTOMER_ID_CLASSES, app/services/utils/activity_log.rb:6).
func (s *Service) logChange(ctx context.Context, r port.Repos, t auditlog.ActivityType, res auditlog.ResourceType, id uuid.UUID, org core.OrganizationID, before map[string]any, obj any) error {
	return s.d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: org, ResourceType: res, ResourceID: id, Object: obj, Before: before,
	})
}

// rateCardObject is the logged object of a rate card: the card with its
// rates, as lago serializes it (SERIALIZED_INCLUDED_OBJECTS rate_card,
// app/services/utils/activity_log.rb:13).
type rateCardObject struct {
	*catalog.RateCard
	// Rates are the card's kept rates.
	Rates []*catalog.Rate
}

// rateCardSnapshot returns the logged attributes of the card c with its
// rates, for the prior state of an update.
func rateCardSnapshot(ctx context.Context, r port.Repos, c *catalog.RateCard) (map[string]any, error) {
	rates, err := r.Rates.ListByRateCard(ctx, c.OrganizationID, c.ID)
	if err != nil {
		return nil, fmt.Errorf("list rates: %w", err)
	}
	return auditlog.ObjectOf(rateCardObject{RateCard: c, Rates: rates})
}

// logRateCard records the activity log t of the rate card c with its
// current rates, whose attributes before an update are before.
func (s *Service) logRateCard(ctx context.Context, r port.Repos, t auditlog.ActivityType, before map[string]any, c *catalog.RateCard) error {
	if s.d.ActivityLogs == nil {
		return nil
	}
	rates, err := r.Rates.ListByRateCard(ctx, c.OrganizationID, c.ID)
	if err != nil {
		return fmt.Errorf("list rates: %w", err)
	}
	return s.logChange(ctx, r, t, auditlog.ResourceRateCard, c.ID.UUID(), c.OrganizationID, before, rateCardObject{RateCard: c, Rates: rates})
}
