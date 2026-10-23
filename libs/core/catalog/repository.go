package catalog

import (
	"context"

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
}

// MetricRepository stores billable metrics with their filters, as one
// aggregate. Every method is scoped to an organization.
type MetricRepository interface {
	// Create inserts m and its filters; a code already used in the
	// organization fails with core.ErrConflict.
	Create(ctx context.Context, m *BillableMetric) error
	// Update replaces the stored metric m.ID and its filters with m (same
	// uniqueness rule).
	Update(ctx context.Context, m *BillableMetric) error
	// Delete removes the metric id; callers delete its charges (and their
	// filters) in the same transaction (domain §7.1).
	Delete(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) error
	// Get returns the metric id with its filters, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (*BillableMetric, error)
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
	// List returns the organization's metrics in creation order.
	List(ctx context.Context, org core.OrganizationID, f MetricListFilter, page core.PageRequest) (core.Page[*BillableMetric], error)
}

// PlanListFilter narrows [PlanRepository.List].
type PlanListFilter struct {
	// IncludeChildren also lists override plans; by default only original
	// plans are listed.
	IncludeChildren bool
	// Statuses, when not empty, keeps only plans in these statuses.
	Statuses []PlanStatus
}

// PlanRepository stores plans. Every method is scoped to an organization.
type PlanRepository interface {
	// Create inserts p; the code of an original plan already used by
	// another original plan of the organization fails with
	// core.ErrConflict. Override plans share their parent's code.
	Create(ctx context.Context, p *Plan) error
	// Update replaces the stored plan p.ID with p (same uniqueness rule).
	Update(ctx context.Context, p *Plan) error
	// Delete removes the plan id; callers delete its charges, fixed
	// charges, commitments, thresholds and entitlements in the same
	// transaction, and its override plans first (domain §7.2).
	Delete(ctx context.Context, org core.OrganizationID, id core.PlanID) error
	// Get returns the plan id (original or override), or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.PlanID) (*Plan, error)
	// GetByCode returns the original plan with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Plan, error)
	// ListByIDs returns the plans with the given IDs, in the order of ids;
	// a missing ID fails with core.ErrNotFound.
	ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.PlanID) ([]*Plan, error)
	// ListChildren returns the override plans of parent in creation order.
	ListChildren(ctx context.Context, org core.OrganizationID, parent core.PlanID) ([]*Plan, error)
	// List returns the organization's plans in creation order.
	List(ctx context.Context, org core.OrganizationID, f PlanListFilter, page core.PageRequest) (core.Page[*Plan], error)
}

// ChargeRepository stores charges with their filters, as one aggregate.
// Every method is scoped to an organization.
type ChargeRepository interface {
	// Create inserts c and its filters; a code already used by another
	// charge of the plan, or a filter code repeated in the charge, fails
	// with core.ErrConflict.
	Create(ctx context.Context, c *Charge) error
	// Update replaces the stored charge c.ID and its filters with c (same
	// uniqueness rules).
	Update(ctx context.Context, c *Charge) error
	// Delete removes the charge id and its filters.
	Delete(ctx context.Context, org core.OrganizationID, id core.ChargeID) error
	// DeleteByPlan removes every charge of the plan and returns how many.
	DeleteByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) (int, error)
	// DeleteByMetric removes every charge of the metric (on every plan)
	// and returns how many.
	DeleteByMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID) (int, error)
	// Get returns the charge id with its filters, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.ChargeID) (*Charge, error)
	// GetByCode returns the plan's charge with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, plan core.PlanID, code string) (*Charge, error)
	// GetFilter returns the charge filter id, or core.ErrNotFound.
	GetFilter(ctx context.Context, org core.OrganizationID, id core.ChargeFilterID) (*ChargeFilter, error)
	// ListByPlan returns the plan's charges in creation order.
	ListByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) ([]*Charge, error)
	// ListByMetric returns the charges of the metric on every plan, in
	// creation order.
	ListByMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID) ([]*Charge, error)
	// ListChildren returns the override charges copied from parent, in
	// creation order.
	ListChildren(ctx context.Context, org core.OrganizationID, parent core.ChargeID) ([]*Charge, error)
	// ListFilters returns the filters of the charge in creation order.
	ListFilters(ctx context.Context, org core.OrganizationID, charge core.ChargeID) ([]*ChargeFilter, error)
}

// FixedChargeRepository stores fixed charges. Every method is scoped to an
// organization.
type FixedChargeRepository interface {
	// Create inserts f; a code already used by another fixed charge of the
	// plan fails with core.ErrConflict.
	Create(ctx context.Context, f *FixedCharge) error
	// Update replaces the stored fixed charge f.ID with f (same rule).
	Update(ctx context.Context, f *FixedCharge) error
	// Delete removes the fixed charge id.
	Delete(ctx context.Context, org core.OrganizationID, id core.FixedChargeID) error
	// DeleteByPlan removes every fixed charge of the plan and returns how
	// many.
	DeleteByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) (int, error)
	// DeleteByAddOn removes every fixed charge of the add-on (on every
	// plan) and returns how many.
	DeleteByAddOn(ctx context.Context, org core.OrganizationID, addOn core.AddOnID) (int, error)
	// Get returns the fixed charge id, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.FixedChargeID) (*FixedCharge, error)
	// GetByCode returns the plan's fixed charge with that code, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, plan core.PlanID, code string) (*FixedCharge, error)
	// ListByPlan returns the plan's fixed charges in creation order.
	ListByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) ([]*FixedCharge, error)
	// ListByAddOn returns the fixed charges of the add-on on every plan,
	// in creation order.
	ListByAddOn(ctx context.Context, org core.OrganizationID, addOn core.AddOnID) ([]*FixedCharge, error)
	// ListChildren returns the override fixed charges copied from parent,
	// in creation order.
	ListChildren(ctx context.Context, org core.OrganizationID, parent core.FixedChargeID) ([]*FixedCharge, error)
}

// AddOnRepository stores add-ons. Every method is scoped to an
// organization.
type AddOnRepository interface {
	// Create inserts a; a code already used in the organization fails with
	// core.ErrConflict.
	Create(ctx context.Context, a *AddOn) error
	// Update replaces the stored add-on a.ID with a (same rule).
	Update(ctx context.Context, a *AddOn) error
	// Delete removes the add-on id; callers delete its fixed charges in the
	// same transaction.
	Delete(ctx context.Context, org core.OrganizationID, id core.AddOnID) error
	// Get returns the add-on id, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.AddOnID) (*AddOn, error)
	// GetByCode returns the add-on with that code, or core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*AddOn, error)
	// ListByIDs returns the add-ons with the given IDs, in the order of
	// ids; a missing ID fails with core.ErrNotFound.
	ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.AddOnID) ([]*AddOn, error)
	// List returns the organization's add-ons in creation order.
	List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*AddOn], error)
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
	// List returns the organization's pricing units in creation order.
	List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*PricingUnit], error)
}
