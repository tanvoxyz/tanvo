package catalog

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// MetricListFilter narrows [MetricRepository.List].
type MetricListFilter struct {
	// AggregationTypes, when not empty, keeps only metrics of these types.
	AggregationTypes []AggregationType
	// Recurring, when set, keeps only metrics whose Recurring flag equals
	// it.
	Recurring *bool
	// PlanID, when set, keeps only metrics charged on that plan.
	PlanID core.PlanID
	// Search keeps metrics whose name or code contains the trimmed term
	// (case-insensitive, with LIKE wildcards matched literally).
	Search string
	// Order is the list order; core.OrderRecent lists by creation instant,
	// newest first, then by ID ascending (lago BillableMetricsQuery).
	Order core.ListOrder
}

// MetricRepository stores billable metrics with their filters, as one
// aggregate. Every method is scoped to an organization. Metrics are
// soft-deleted: a discarded one keeps its row and filters with DeletedAt
// set and is invisible to every other method (lago billable_metric.rb
// default_scope kept).
type MetricRepository interface {
	// Create inserts m and its filters; a code already used by a kept
	// metric of the organization fails with core.ErrConflict.
	Create(ctx context.Context, m *BillableMetric) error
	// Update replaces the stored kept metric m.ID and its filters with m
	// (same uniqueness rule).
	Update(ctx context.Context, m *BillableMetric) error
	// Discard soft-deletes the kept metric id at at (lago metric.discard!);
	// a missing or discarded one fails with core.ErrNotFound. Its code is
	// free again for a new metric.
	Discard(ctx context.Context, org core.OrganizationID, id core.BillableMetricID, at time.Time) error
	// DiscardFilters soft-deletes at at the kept filters of the metric id,
	// kept or discarded, and returns how many it discarded (lago
	// BillableMetricFilters::DestroyAllService); the metric is then read
	// with no filters.
	DiscardFilters(ctx context.Context, org core.OrganizationID, id core.BillableMetricID, at time.Time) (int, error)
	// Get returns the kept metric id with its filters, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (*BillableMetric, error)
	// GetWithDiscarded returns the metric id, kept or discarded, with its
	// filters, or core.ErrNotFound, for the records that keep naming
	// a discarded metric (lago fee.rb:33, billable_metric with_discarded).
	GetWithDiscarded(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (*BillableMetric, error)
	// GetByCode returns the metric with that code, or core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*BillableMetric, error)
	// ListByIDs returns the metrics with the given IDs, in the order of
	// ids; a missing ID fails with core.ErrNotFound.
	ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.BillableMetricID) ([]*BillableMetric, error)
	// ListByCodes returns the metrics with the given codes, in the order of
	// codes; a missing code fails with core.ErrNotFound.
	ListByCodes(ctx context.Context, org core.OrganizationID, codes []string) ([]*BillableMetric, error)
	// InUse reports whether a charge prices the metric, which locks its
	// aggregation fields (domain §7.1).
	InUse(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (bool, error)
	// List returns the organization's metrics matching f in the order
	// f.Order; an undeclared order fails with core.ErrInvalidValue.
	List(ctx context.Context, org core.OrganizationID, f MetricListFilter, page core.PageRequest) (core.Page[*BillableMetric], error)
}

// PlanListFilter narrows [PlanRepository.List].
type PlanListFilter struct {
	// IncludeChildren also lists override plans; by default only original
	// plans are listed.
	IncludeChildren bool
	// Statuses, when not empty, keeps only plans in these statuses; when
	// empty, deleted plans are left out.
	Statuses []PlanStatus
	// Search keeps plans whose name or code contains the trimmed term
	// (case-insensitive, with LIKE wildcards matched literally).
	Search string
	// Order is the list order; core.OrderRecent lists by name (byte
	// order), then by creation instant, newest first, then by ID (lago
	// PlansQuery::DEFAULT_ORDER).
	Order core.ListOrder
}

// PlanRepository stores plans. Every method is scoped to an organization.
// Plans are never removed: deleting one is the [Plan.MarkDeleted] status
// change stored with Update, which keeps its charges, fixed charges,
// commitments and usage thresholds (domain §7.2).
type PlanRepository interface {
	// Create inserts p; the code of an original plan already used by
	// another original plan of the organization that is not deleted fails
	// with core.ErrConflict. Override plans share their parent's code.
	Create(ctx context.Context, p *Plan) error
	// Update replaces the stored plan p.ID with p (same uniqueness rule).
	Update(ctx context.Context, p *Plan) error
	// Get returns the plan id (original or override, deleted or not), or
	// core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.PlanID) (*Plan, error)
	// GetByCode returns the original plan with that code that is not
	// deleted, or core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Plan, error)
	// ListByIDs returns the plans with the given IDs (deleted or not), in
	// the order of ids; a missing ID fails with core.ErrNotFound.
	ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.PlanID) ([]*Plan, error)
	// ListChildren returns the override plans of parent that are not
	// deleted, in creation order.
	ListChildren(ctx context.Context, org core.OrganizationID, parent core.PlanID) ([]*Plan, error)
	// List returns the organization's plans in creation order.
	List(ctx context.Context, org core.OrganizationID, f PlanListFilter, page core.PageRequest) (core.Page[*Plan], error)
}

// ChargeRepository stores charges with their filters, as one aggregate.
// Every method is scoped to an organization. Charges and filters are
// soft-deleted (lago charge.rb, charge_filter.rb and charge_filter_value.rb
// discard on deleted_at): a discarded one keeps its row with DeletedAt set
// for the fees that reference it, Get and GetFilter still return it, and
// every other read sees kept rows only.
type ChargeRepository interface {
	// Create inserts c and its filters; a code already used by another
	// kept charge of the plan, or a filter code repeated in the charge,
	// fails with core.ErrConflict.
	Create(ctx context.Context, c *Charge) error
	// Update replaces the stored kept charge c.ID and its filters with c
	// (same uniqueness rules): the stored filters missing from c.Filters
	// are discarded at c.UpdatedAt (lago
	// ChargeFilters::CreateOrUpdateBatchService), the others replaced.
	Update(ctx context.Context, c *Charge) error
	// Discard soft-deletes the kept charge id and its filters at at (lago
	// Charges::DestroyService); a missing or discarded one fails with
	// core.ErrNotFound. Its code is free again. Override charges of id
	// stay kept: callers discard them.
	Discard(ctx context.Context, org core.OrganizationID, id core.ChargeID, at time.Time) error
	// DiscardByPlan discards every kept charge of the plan, with its
	// filters, and returns how many.
	DiscardByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID, at time.Time) (int, error)
	// DiscardByMetric discards every kept charge of the metric (on every
	// plan), with its filters, and returns how many (lago
	// BillableMetrics::DestroyService and DestroyAllService).
	DiscardByMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID, at time.Time) (int, error)
	// Get returns the charge id, kept or discarded, with its kept filters,
	// or core.ErrNotFound; callers serving kept charges check DeletedAt.
	Get(ctx context.Context, org core.OrganizationID, id core.ChargeID) (*Charge, error)
	// GetByCode returns the plan's kept charge with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, plan core.PlanID, code string) (*Charge, error)
	// GetFilter returns the charge filter id, kept or discarded, or
	// core.ErrNotFound.
	GetFilter(ctx context.Context, org core.OrganizationID, id core.ChargeFilterID) (*ChargeFilter, error)
	// ListByPlan returns the plan's kept charges in creation order.
	ListByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) ([]*Charge, error)
	// ListByMetric returns the kept charges of the metric on every plan,
	// in creation order.
	ListByMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID) ([]*Charge, error)
	// ListChildren returns the kept override charges copied from parent,
	// in creation order.
	ListChildren(ctx context.Context, org core.OrganizationID, parent core.ChargeID) ([]*Charge, error)
	// ListFilters returns the kept filters of the kept charge in creation
	// order; a missing or discarded charge fails with core.ErrNotFound.
	ListFilters(ctx context.Context, org core.OrganizationID, charge core.ChargeID) ([]*ChargeFilter, error)
}

// FixedChargeRepository stores fixed charges. Every method is scoped to an
// organization. Fixed charges are soft-deleted (lago fixed_charge.rb
// discard on deleted_at): a discarded one keeps its row with DeletedAt set
// for the fees that reference it, Get still returns it, and every other
// read sees kept rows only.
type FixedChargeRepository interface {
	// Create inserts f; a code already used by another kept fixed charge
	// of the plan fails with core.ErrConflict.
	Create(ctx context.Context, f *FixedCharge) error
	// Update replaces the stored kept fixed charge f.ID with f (same rule).
	Update(ctx context.Context, f *FixedCharge) error
	// Discard soft-deletes the kept fixed charge id at at (lago
	// FixedCharges::DestroyService); a missing or discarded one fails with
	// core.ErrNotFound. Override fixed charges of id stay kept: callers
	// discard them.
	Discard(ctx context.Context, org core.OrganizationID, id core.FixedChargeID, at time.Time) error
	// DiscardByPlan discards every kept fixed charge of the plan and
	// returns how many.
	DiscardByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID, at time.Time) (int, error)
	// DiscardByAddOn discards every kept fixed charge of the add-on (on
	// every plan) and returns how many (lago AddOns::DestroyService).
	DiscardByAddOn(ctx context.Context, org core.OrganizationID, addOn core.AddOnID, at time.Time) (int, error)
	// Get returns the fixed charge id, kept or discarded, or
	// core.ErrNotFound; callers serving kept fixed charges check DeletedAt.
	Get(ctx context.Context, org core.OrganizationID, id core.FixedChargeID) (*FixedCharge, error)
	// GetByCode returns the plan's kept fixed charge with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, plan core.PlanID, code string) (*FixedCharge, error)
	// ListByPlan returns the plan's kept fixed charges in creation order.
	ListByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) ([]*FixedCharge, error)
	// ListByAddOn returns the kept fixed charges of the add-on on every
	// plan, in creation order.
	ListByAddOn(ctx context.Context, org core.OrganizationID, addOn core.AddOnID) ([]*FixedCharge, error)
	// ListChildren returns the kept override fixed charges copied from
	// parent, in creation order.
	ListChildren(ctx context.Context, org core.OrganizationID, parent core.FixedChargeID) ([]*FixedCharge, error)
}

// AddOnRepository stores add-ons. Every method is scoped to an
// organization. Add-ons are soft-deleted (lago add_on.rb discard on
// deleted_at): a discarded one keeps its row with DeletedAt set for the
// fees that reference it, Get still returns it, and every other read sees
// kept rows only.
type AddOnRepository interface {
	// Create inserts a; a code already used by a kept add-on of the
	// organization fails with core.ErrConflict.
	Create(ctx context.Context, a *AddOn) error
	// Update replaces the stored kept add-on a.ID with a (same rule).
	Update(ctx context.Context, a *AddOn) error
	// Discard soft-deletes the kept add-on id at at; a missing or
	// discarded one fails with core.ErrNotFound. Its code is free again;
	// callers discard its fixed charges in the same transaction (lago
	// AddOns::DestroyService).
	Discard(ctx context.Context, org core.OrganizationID, id core.AddOnID, at time.Time) error
	// Get returns the add-on id, kept or discarded, or core.ErrNotFound;
	// callers serving kept add-ons check DeletedAt.
	Get(ctx context.Context, org core.OrganizationID, id core.AddOnID) (*AddOn, error)
	// GetByCode returns the kept add-on with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*AddOn, error)
	// ListByIDs returns the kept add-ons with the given IDs, in the order
	// of ids; a missing ID fails with core.ErrNotFound.
	ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.AddOnID) ([]*AddOn, error)
	// List returns the organization's kept add-ons matching f in the order
	// f.Order; an undeclared order fails with core.ErrInvalidValue.
	List(ctx context.Context, org core.OrganizationID, f AddOnFilter, page core.PageRequest) (core.Page[*AddOn], error)
}

// AddOnFilter narrows [AddOnRepository.List].
type AddOnFilter struct {
	// Search keeps add-ons whose name or code contains the trimmed term
	// (case-insensitive, with LIKE wildcards matched literally).
	Search string
	// Order is the list order; core.OrderRecent lists by creation instant,
	// newest first, then by ID ascending (lago AddOnsQuery).
	Order core.ListOrder
}

// PricingUnitRepository stores pricing units. Every method is scoped to an
// organization.
type PricingUnitRepository interface {
	// Create inserts p; a code already used in the organization fails with
	// core.ErrConflict.
	Create(ctx context.Context, p *PricingUnit) error
	// Update replaces the stored pricing unit p.ID with p.
	Update(ctx context.Context, p *PricingUnit) error
	// Get returns the pricing unit id, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.PricingUnitID) (*PricingUnit, error)
	// GetByCode returns the pricing unit with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*PricingUnit, error)
	// List returns the organization's pricing units matching f in the
	// order f.Order; an undeclared order fails with core.ErrInvalidValue.
	List(ctx context.Context, org core.OrganizationID, f PricingUnitFilter, page core.PageRequest) (core.Page[*PricingUnit], error)
}

// PricingUnitFilter narrows [PricingUnitRepository.List].
type PricingUnitFilter struct {
	// Search keeps pricing units whose name or code contains the trimmed
	// term (case-insensitive, with LIKE wildcards matched literally).
	Search string
	// Order is the list order; core.OrderRecent lists by name (byte
	// order), then by creation instant, newest first, then by ID ascending
	// (lago PricingUnitsQuery).
	Order core.ListOrder
}
