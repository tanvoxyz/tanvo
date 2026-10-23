package refs

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Ref references an object by ID or by code: exactly one is set.
type Ref struct {
	// ID is the object's ID.
	ID uuid.UUID
	// Code is the object's code.
	Code string
}

// ByID returns the reference of id.
func ByID[I core.Identifier[I]](id I) Ref { return Ref{ID: id.UUID()} }

// ByCode returns the reference of code.
func ByCode(code string) Ref { return Ref{Code: code} }

// IsZero reports whether r references nothing.
func (r Ref) IsZero() bool { return r.ID == uuid.Nil && r.Code == "" }

// String returns the ID, or "code:<code>".
func (r Ref) String() string {
	if r.Code != "" {
		return "code:" + r.Code
	}
	return r.ID.String()
}

// Finder finds objects of type T with ID type I by ID and by code.
type Finder[I core.Identifier[I], T any] struct {
	// Resource is the resource name of the not-found error ("tax").
	Resource string
	// Get finds an object by ID.
	Get func(ctx context.Context, id I) (T, error)
	// GetByCode finds an object by code.
	GetByCode func(ctx context.Context, code string) (T, error)
	// ID returns the ID of an object.
	ID func(T) I
	// Discarded reports whether an object found by ID is discarded, so not
	// found; nil when Get finds kept objects only.
	Discarded func(T) bool
}

// One resolves r; a missing object, or a zero r, fails with
// core.NotFound(f.Resource).
func (f Finder[I, T]) One(ctx context.Context, r Ref) (T, error) {
	var (
		v   T
		err error
	)
	switch {
	case r.Code != "":
		v, err = f.GetByCode(ctx, r.Code)
	case r.ID != uuid.Nil:
		var id I
		if id, err = core.ParseID[I](r.ID.String()); err == nil {
			v, err = f.Get(ctx, id)
			if err == nil && f.Discarded != nil && f.Discarded(v) {
				err = core.ErrNotFound
			}
		}
	default:
		err = core.ErrNotFound
	}
	if errors.Is(err, core.ErrNotFound) {
		return v, core.NotFound(f.Resource)
	}
	if err != nil {
		return v, fmt.Errorf("find %s %s: %w", f.Resource, r, err)
	}
	return v, nil
}

// IDs resolves refs in order, dropping repeated objects; a missing object
// fails with core.NotFound(f.Resource). A nil refs is nil.
func (f Finder[I, T]) IDs(ctx context.Context, refs []Ref) ([]I, error) {
	if refs == nil {
		return nil, nil
	}
	out := make([]I, 0, len(refs))
	seen := map[I]bool{}
	for _, r := range refs {
		v, err := f.One(ctx, r)
		if err != nil {
			return nil, err
		}
		if id := f.ID(v); !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// Taxes finds the organization's kept taxes (resource "tax").
func Taxes(r port.Repos, org core.OrganizationID) Finder[core.TaxID, *catalog.Tax] {
	return Finder[core.TaxID, *catalog.Tax]{
		Resource:  "tax",
		Get:       func(ctx context.Context, id core.TaxID) (*catalog.Tax, error) { return r.Taxes.Get(ctx, org, id) },
		GetByCode: func(ctx context.Context, code string) (*catalog.Tax, error) { return r.Taxes.GetByCode(ctx, org, code) },
		ID:        func(t *catalog.Tax) core.TaxID { return t.ID },
		Discarded: func(t *catalog.Tax) bool { return t.DeletedAt != nil },
	}
}

// Metrics finds the organization's billable metrics (resource
// "billable_metric").
func Metrics(r port.Repos, org core.OrganizationID) Finder[core.BillableMetricID, *catalog.BillableMetric] {
	return Finder[core.BillableMetricID, *catalog.BillableMetric]{
		Resource: "billable_metric",
		Get: func(ctx context.Context, id core.BillableMetricID) (*catalog.BillableMetric, error) {
			return r.Metrics.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*catalog.BillableMetric, error) {
			return r.Metrics.GetByCode(ctx, org, code)
		},
		ID: func(m *catalog.BillableMetric) core.BillableMetricID { return m.ID },
	}
}

// Plans finds the organization's plans (resource "plan"): by ID any plan,
// by code the original plan that is not deleted.
func Plans(r port.Repos, org core.OrganizationID) Finder[core.PlanID, *catalog.Plan] {
	return Finder[core.PlanID, *catalog.Plan]{
		Resource: "plan",
		Get:      func(ctx context.Context, id core.PlanID) (*catalog.Plan, error) { return r.Plans.Get(ctx, org, id) },
		GetByCode: func(ctx context.Context, code string) (*catalog.Plan, error) {
			return r.Plans.GetByCode(ctx, org, code)
		},
		ID: func(p *catalog.Plan) core.PlanID { return p.ID },
	}
}

// AddOns finds the organization's kept add-ons (resource "add_on").
func AddOns(r port.Repos, org core.OrganizationID) Finder[core.AddOnID, *catalog.AddOn] {
	return Finder[core.AddOnID, *catalog.AddOn]{
		Resource: "add_on",
		Get:      func(ctx context.Context, id core.AddOnID) (*catalog.AddOn, error) { return r.AddOns.Get(ctx, org, id) },
		GetByCode: func(ctx context.Context, code string) (*catalog.AddOn, error) {
			return r.AddOns.GetByCode(ctx, org, code)
		},
		ID:        func(a *catalog.AddOn) core.AddOnID { return a.ID },
		Discarded: func(a *catalog.AddOn) bool { return a.DeletedAt != nil },
	}
}

// PricingUnits finds the organization's pricing units (resource
// "pricing_unit").
func PricingUnits(r port.Repos, org core.OrganizationID) Finder[core.PricingUnitID, *catalog.PricingUnit] {
	return Finder[core.PricingUnitID, *catalog.PricingUnit]{
		Resource: "pricing_unit",
		Get: func(ctx context.Context, id core.PricingUnitID) (*catalog.PricingUnit, error) {
			return r.PricingUnits.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*catalog.PricingUnit, error) {
			return r.PricingUnits.GetByCode(ctx, org, code)
		},
		ID: func(p *catalog.PricingUnit) core.PricingUnitID { return p.ID },
	}
}

// Features finds the organization's kept features (resource "feature").
func Features(r port.Repos, org core.OrganizationID) Finder[core.FeatureID, *catalog.Feature] {
	return Finder[core.FeatureID, *catalog.Feature]{
		Resource: "feature",
		Get: func(ctx context.Context, id core.FeatureID) (*catalog.Feature, error) {
			return r.Features.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*catalog.Feature, error) {
			return r.Features.GetByCode(ctx, org, code)
		},
		ID: func(f *catalog.Feature) core.FeatureID { return f.ID },
	}
}
