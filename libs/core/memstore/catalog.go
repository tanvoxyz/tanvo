package memstore

import (
	"context"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
)

// catalogData holds the tables of billable metrics, plans, charges, fixed
// charges, add-ons and pricing units of package catalog. Metric filters
// and charge filters are stored with their aggregate.
type catalogData struct {
	metrics      *table[core.BillableMetricID, catalog.BillableMetric]
	plans        *table[core.PlanID, catalog.Plan]
	charges      *table[core.ChargeID, catalog.Charge]
	fixedCharges *table[core.FixedChargeID, catalog.FixedCharge]
	addOns       *table[core.AddOnID, catalog.AddOn]
	pricingUnits *table[core.PricingUnitID, catalog.PricingUnit]
}

func newCatalogData() *catalogData {
	return &catalogData{
		metrics: newTable[core.BillableMetricID](func(m catalog.BillableMetric) catalog.BillableMetric {
			m.Description = catalogClonePtr(m.Description)
			m.FieldName = catalogClonePtr(m.FieldName)
			m.Expression = catalogClonePtr(m.Expression)
			m.RoundingPrecision = catalogClonePtr(m.RoundingPrecision)
			m.Filters = slices.Clone(m.Filters)
			for i := range m.Filters {
				m.Filters[i].Values = slices.Clone(m.Filters[i].Values)
			}
			return m
		}),
		plans: newTable[core.PlanID](func(p catalog.Plan) catalog.Plan {
			p.InvoiceDisplayName = catalogClonePtr(p.InvoiceDisplayName)
			p.Description = catalogClonePtr(p.Description)
			p.BillChargesMonthly = catalogClonePtr(p.BillChargesMonthly)
			p.BillFixedChargesMonthly = catalogClonePtr(p.BillFixedChargesMonthly)
			p.TaxIDs = slices.Clone(p.TaxIDs)
			p.Metadata = p.Metadata.Clone()
			return p
		}),
		charges: newTable[core.ChargeID](func(c catalog.Charge) catalog.Charge {
			o := c.Clone()
			o.InvoiceDisplayName = catalogClonePtr(c.InvoiceDisplayName)
			for i := range o.Filters {
				o.Filters[i].InvoiceDisplayName = catalogClonePtr(o.Filters[i].InvoiceDisplayName)
			}
			return *o
		}),
		fixedCharges: newTable[core.FixedChargeID](func(f catalog.FixedCharge) catalog.FixedCharge {
			o := f.Clone()
			o.InvoiceDisplayName = catalogClonePtr(f.InvoiceDisplayName)
			return *o
		}),
		addOns: newTable[core.AddOnID](func(a catalog.AddOn) catalog.AddOn {
			a.InvoiceDisplayName = catalogClonePtr(a.InvoiceDisplayName)
			a.Description = catalogClonePtr(a.Description)
			a.TaxIDs = slices.Clone(a.TaxIDs)
			return a
		}),
		pricingUnits: newTable[core.PricingUnitID](func(p catalog.PricingUnit) catalog.PricingUnit {
			p.Description = catalogClonePtr(p.Description)
			return p
		}),
	}
}

func (d *catalogData) snapshot() *catalogData {
	return &catalogData{
		metrics:      d.metrics.snapshot(),
		plans:        d.plans.snapshot(),
		charges:      d.charges.snapshot(),
		fixedCharges: d.fixedCharges.snapshot(),
		addOns:       d.addOns.snapshot(),
		pricingUnits: d.pricingUnits.snapshot(),
	}
}

func catalogClonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// catalogByIDs returns the rows with ids in the order of ids, failing with
// core.ErrNotFound on the first missing or foreign one.
func catalogByIDs[K interface {
	comparable
	Compare(K) int
	String() string
}, V any](t *table[K, V], entity string, ids []K, inOrg func(*V) bool,
) ([]*V, error) {
	out := make([]*V, 0, len(ids))
	for _, id := range ids {
		v, ok := t.get(id)
		if !ok || !inOrg(v) {
			return nil, notFound(entity, id)
		}
		out = append(out, v)
	}
	return out, nil
}

// metricRepo implements catalog.MetricRepository.
type metricRepo struct{ d *catalogData }

func (r metricRepo) check(m *catalog.BillableMetric) error {
	if r.d.metrics.any(func(x *catalog.BillableMetric) bool {
		return x.ID != m.ID && x.OrganizationID == m.OrganizationID && x.Code == m.Code
	}) {
		return conflict("billable_metric", "code")
	}
	return nil
}

func (r metricRepo) Create(_ context.Context, m *catalog.BillableMetric) error {
	if r.d.metrics.has(m.ID) {
		return conflict("billable_metric", "id")
	}
	if err := r.check(m); err != nil {
		return err
	}
	r.d.metrics.put(m.ID, *m)
	return nil
}

func (r metricRepo) Update(_ context.Context, m *catalog.BillableMetric) error {
	cur, ok := r.d.metrics.get(m.ID)
	if !ok || cur.OrganizationID != m.OrganizationID {
		return notFound("billable_metric", m.ID)
	}
	if err := r.check(m); err != nil {
		return err
	}
	r.d.metrics.put(m.ID, *m)
	return nil
}

func (r metricRepo) Delete(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) error {
	if _, err := r.Get(ctx, org, id); err != nil {
		return err
	}
	if r.d.charges.any(func(c *catalog.Charge) bool { return c.OrganizationID == org && c.BillableMetricID == id }) {
		return conflict("billable_metric", "still referenced by charges")
	}
	r.d.metrics.del(id)
	return nil
}

func (r metricRepo) Get(_ context.Context, org core.OrganizationID, id core.BillableMetricID) (*catalog.BillableMetric, error) {
	m, ok := r.d.metrics.get(id)
	if !ok || m.OrganizationID != org {
		return nil, notFound("billable_metric", id)
	}
	return m, nil
}

func (r metricRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*catalog.BillableMetric, error) {
	m, ok := r.d.metrics.first(func(x *catalog.BillableMetric) bool { return x.OrganizationID == org && x.Code == code })
	if !ok {
		return nil, notFound("billable_metric", stringer(code))
	}
	return m, nil
}

func (r metricRepo) ListByIDs(_ context.Context, org core.OrganizationID, ids []core.BillableMetricID) ([]*catalog.BillableMetric, error) {
	return catalogByIDs(r.d.metrics, "billable_metric", ids, func(m *catalog.BillableMetric) bool { return m.OrganizationID == org })
}

func (r metricRepo) ListByCodes(ctx context.Context, org core.OrganizationID, codes []string) ([]*catalog.BillableMetric, error) {
	out := make([]*catalog.BillableMetric, 0, len(codes))
	for _, code := range codes {
		m, err := r.GetByCode(ctx, org, code)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (r metricRepo) InUse(_ context.Context, org core.OrganizationID, id core.BillableMetricID) (bool, error) {
	return r.d.charges.any(func(c *catalog.Charge) bool { return c.OrganizationID == org && c.BillableMetricID == id }), nil
}

func (r metricRepo) List(_ context.Context, org core.OrganizationID, f catalog.MetricListFilter, page core.PageRequest) (core.Page[*catalog.BillableMetric], error) {
	rows := r.d.metrics.find(func(m *catalog.BillableMetric) bool {
		if m.OrganizationID != org {
			return false
		}
		if len(f.AggregationTypes) > 0 && !slices.Contains(f.AggregationTypes, m.AggregationType) {
			return false
		}
		if f.Recurring != nil && m.Recurring != *f.Recurring {
			return false
		}
		if !f.PlanID.IsZero() && !r.d.charges.any(func(c *catalog.Charge) bool {
			return c.OrganizationID == org && c.PlanID == f.PlanID && c.BillableMetricID == m.ID
		}) {
			return false
		}
		return true
	})
	return paginate(rows, func(m *catalog.BillableMetric) core.BillableMetricID { return m.ID }, page)
}

// planRepo implements catalog.PlanRepository.
type planRepo struct{ d *catalogData }

func (r planRepo) check(p *catalog.Plan) error {
	if p.IsChild() || p.Deleted() {
		return nil
	}
	if r.d.plans.any(func(x *catalog.Plan) bool {
		return x.ID != p.ID && x.OrganizationID == p.OrganizationID && x.IsParent() && !x.Deleted() && x.Code == p.Code
	}) {
		return conflict("plan", "code")
	}
	return nil
}

func (r planRepo) Create(_ context.Context, p *catalog.Plan) error {
	if r.d.plans.has(p.ID) {
		return conflict("plan", "id")
	}
	if err := r.check(p); err != nil {
		return err
	}
	r.d.plans.put(p.ID, *p)
	return nil
}

func (r planRepo) Update(_ context.Context, p *catalog.Plan) error {
	cur, ok := r.d.plans.get(p.ID)
	if !ok || cur.OrganizationID != p.OrganizationID {
		return notFound("plan", p.ID)
	}
	if err := r.check(p); err != nil {
		return err
	}
	r.d.plans.put(p.ID, *p)
	return nil
}

func (r planRepo) Get(_ context.Context, org core.OrganizationID, id core.PlanID) (*catalog.Plan, error) {
	p, ok := r.d.plans.get(id)
	if !ok || p.OrganizationID != org {
		return nil, notFound("plan", id)
	}
	return p, nil
}

func (r planRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*catalog.Plan, error) {
	p, ok := r.d.plans.first(func(x *catalog.Plan) bool {
		return x.OrganizationID == org && x.IsParent() && !x.Deleted() && x.Code == code
	})
	if !ok {
		return nil, notFound("plan", stringer(code))
	}
	return p, nil
}

func (r planRepo) ListByIDs(_ context.Context, org core.OrganizationID, ids []core.PlanID) ([]*catalog.Plan, error) {
	return catalogByIDs(r.d.plans, "plan", ids, func(p *catalog.Plan) bool { return p.OrganizationID == org })
}

func (r planRepo) ListChildren(_ context.Context, org core.OrganizationID, parent core.PlanID) ([]*catalog.Plan, error) {
	return r.d.plans.find(func(p *catalog.Plan) bool {
		return p.OrganizationID == org && p.ParentID == parent && !p.Deleted()
	}), nil
}

func (r planRepo) List(_ context.Context, org core.OrganizationID, f catalog.PlanListFilter, page core.PageRequest) (core.Page[*catalog.Plan], error) {
	rows := r.d.plans.find(func(p *catalog.Plan) bool {
		return p.OrganizationID == org && (f.IncludeChildren || p.IsParent()) &&
			(len(f.Statuses) == 0 && !p.Deleted() || slices.Contains(f.Statuses, p.Status))
	})
	return paginate(rows, func(p *catalog.Plan) core.PlanID { return p.ID }, page)
}

// chargeRepo implements catalog.ChargeRepository.
type chargeRepo struct{ d *catalogData }

func (r chargeRepo) check(c *catalog.Charge) error {
	if c.ParentID.IsZero() && r.d.charges.any(func(x *catalog.Charge) bool {
		return x.ID != c.ID && x.OrganizationID == c.OrganizationID && x.PlanID == c.PlanID && x.ParentID.IsZero() && x.Code == c.Code
	}) {
		return conflict("charge", "code")
	}
	codes := map[string]bool{}
	for _, f := range c.Filters {
		if f.Code == "" {
			continue
		}
		if codes[f.Code] {
			return conflict("charge_filter", "code")
		}
		codes[f.Code] = true
	}
	return nil
}

func (r chargeRepo) Create(_ context.Context, c *catalog.Charge) error {
	if r.d.charges.has(c.ID) {
		return conflict("charge", "id")
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.charges.put(c.ID, *c)
	return nil
}

func (r chargeRepo) Update(_ context.Context, c *catalog.Charge) error {
	cur, ok := r.d.charges.get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID {
		return notFound("charge", c.ID)
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.charges.put(c.ID, *c)
	return nil
}

func (r chargeRepo) Delete(ctx context.Context, org core.OrganizationID, id core.ChargeID) error {
	if _, err := r.Get(ctx, org, id); err != nil {
		return err
	}
	_, err := r.delete(func(c *catalog.Charge) bool { return c.ID == id })
	return err
}

// delete removes the charges matching pred; an override charge left
// behind with its parent among them fails with core.ErrConflict.
func (r chargeRepo) delete(pred func(*catalog.Charge) bool) (int, error) {
	return r.d.charges.deleteRestricted("charge", pred, func(c *catalog.Charge) core.ChargeID { return c.ParentID })
}

func (r chargeRepo) DeleteByPlan(_ context.Context, org core.OrganizationID, plan core.PlanID) (int, error) {
	return r.delete(func(c *catalog.Charge) bool { return c.OrganizationID == org && c.PlanID == plan })
}

func (r chargeRepo) DeleteByMetric(_ context.Context, org core.OrganizationID, metric core.BillableMetricID) (int, error) {
	return r.delete(func(c *catalog.Charge) bool { return c.OrganizationID == org && c.BillableMetricID == metric })
}

func (r chargeRepo) Get(_ context.Context, org core.OrganizationID, id core.ChargeID) (*catalog.Charge, error) {
	c, ok := r.d.charges.get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("charge", id)
	}
	return c, nil
}

func (r chargeRepo) GetByCode(_ context.Context, org core.OrganizationID, plan core.PlanID, code string) (*catalog.Charge, error) {
	c, ok := r.d.charges.first(func(x *catalog.Charge) bool {
		return x.OrganizationID == org && x.PlanID == plan && x.Code == code
	})
	if !ok {
		return nil, notFound("charge", stringer(code))
	}
	return c, nil
}

func (r chargeRepo) GetFilter(_ context.Context, org core.OrganizationID, id core.ChargeFilterID) (*catalog.ChargeFilter, error) {
	c, ok := r.d.charges.first(func(x *catalog.Charge) bool {
		return x.OrganizationID == org && slices.ContainsFunc(x.Filters, func(f catalog.ChargeFilter) bool { return f.ID == id })
	})
	if !ok {
		return nil, notFound("charge_filter", id)
	}
	f, _ := c.Filter(id)
	return f, nil
}

func (r chargeRepo) ListByPlan(_ context.Context, org core.OrganizationID, plan core.PlanID) ([]*catalog.Charge, error) {
	return r.d.charges.find(func(c *catalog.Charge) bool { return c.OrganizationID == org && c.PlanID == plan }), nil
}

func (r chargeRepo) ListByMetric(_ context.Context, org core.OrganizationID, metric core.BillableMetricID) ([]*catalog.Charge, error) {
	return r.d.charges.find(func(c *catalog.Charge) bool { return c.OrganizationID == org && c.BillableMetricID == metric }), nil
}

func (r chargeRepo) ListChildren(_ context.Context, org core.OrganizationID, parent core.ChargeID) ([]*catalog.Charge, error) {
	return r.d.charges.find(func(c *catalog.Charge) bool { return c.OrganizationID == org && c.ParentID == parent }), nil
}

func (r chargeRepo) ListFilters(ctx context.Context, org core.OrganizationID, charge core.ChargeID) ([]*catalog.ChargeFilter, error) {
	c, err := r.Get(ctx, org, charge)
	if err != nil {
		return nil, err
	}
	out := make([]*catalog.ChargeFilter, len(c.Filters))
	for i := range c.Filters {
		out[i] = &c.Filters[i]
	}
	slices.SortStableFunc(out, func(a, b *catalog.ChargeFilter) int {
		if n := a.CreatedAt.Compare(b.CreatedAt); n != 0 {
			return n
		}
		return a.ID.Compare(b.ID)
	})
	return out, nil
}

// fixedChargeRepo implements catalog.FixedChargeRepository.
type fixedChargeRepo struct{ d *catalogData }

func (r fixedChargeRepo) check(f *catalog.FixedCharge) error {
	if f.ParentID.IsZero() && r.d.fixedCharges.any(func(x *catalog.FixedCharge) bool {
		return x.ID != f.ID && x.OrganizationID == f.OrganizationID && x.PlanID == f.PlanID && x.ParentID.IsZero() && x.Code == f.Code
	}) {
		return conflict("fixed_charge", "code")
	}
	return nil
}

func (r fixedChargeRepo) Create(_ context.Context, f *catalog.FixedCharge) error {
	if r.d.fixedCharges.has(f.ID) {
		return conflict("fixed_charge", "id")
	}
	if err := r.check(f); err != nil {
		return err
	}
	r.d.fixedCharges.put(f.ID, *f)
	return nil
}

func (r fixedChargeRepo) Update(_ context.Context, f *catalog.FixedCharge) error {
	cur, ok := r.d.fixedCharges.get(f.ID)
	if !ok || cur.OrganizationID != f.OrganizationID {
		return notFound("fixed_charge", f.ID)
	}
	if err := r.check(f); err != nil {
		return err
	}
	r.d.fixedCharges.put(f.ID, *f)
	return nil
}

func (r fixedChargeRepo) Delete(ctx context.Context, org core.OrganizationID, id core.FixedChargeID) error {
	if _, err := r.Get(ctx, org, id); err != nil {
		return err
	}
	_, err := r.delete(func(f *catalog.FixedCharge) bool { return f.ID == id })
	return err
}

// delete removes the fixed charges matching pred; an override fixed charge
// left behind with its parent among them fails with core.ErrConflict.
func (r fixedChargeRepo) delete(pred func(*catalog.FixedCharge) bool) (int, error) {
	return r.d.fixedCharges.deleteRestricted("fixed_charge", pred, func(f *catalog.FixedCharge) core.FixedChargeID { return f.ParentID })
}

func (r fixedChargeRepo) DeleteByPlan(_ context.Context, org core.OrganizationID, plan core.PlanID) (int, error) {
	return r.delete(func(f *catalog.FixedCharge) bool { return f.OrganizationID == org && f.PlanID == plan })
}

func (r fixedChargeRepo) DeleteByAddOn(_ context.Context, org core.OrganizationID, addOn core.AddOnID) (int, error) {
	return r.delete(func(f *catalog.FixedCharge) bool { return f.OrganizationID == org && f.AddOnID == addOn })
}

func (r fixedChargeRepo) Get(_ context.Context, org core.OrganizationID, id core.FixedChargeID) (*catalog.FixedCharge, error) {
	f, ok := r.d.fixedCharges.get(id)
	if !ok || f.OrganizationID != org {
		return nil, notFound("fixed_charge", id)
	}
	return f, nil
}

func (r fixedChargeRepo) GetByCode(_ context.Context, org core.OrganizationID, plan core.PlanID, code string) (*catalog.FixedCharge, error) {
	f, ok := r.d.fixedCharges.first(func(x *catalog.FixedCharge) bool {
		return x.OrganizationID == org && x.PlanID == plan && x.Code == code
	})
	if !ok {
		return nil, notFound("fixed_charge", stringer(code))
	}
	return f, nil
}

func (r fixedChargeRepo) ListByPlan(_ context.Context, org core.OrganizationID, plan core.PlanID) ([]*catalog.FixedCharge, error) {
	return r.d.fixedCharges.find(func(f *catalog.FixedCharge) bool { return f.OrganizationID == org && f.PlanID == plan }), nil
}

func (r fixedChargeRepo) ListByAddOn(_ context.Context, org core.OrganizationID, addOn core.AddOnID) ([]*catalog.FixedCharge, error) {
	return r.d.fixedCharges.find(func(f *catalog.FixedCharge) bool { return f.OrganizationID == org && f.AddOnID == addOn }), nil
}

func (r fixedChargeRepo) ListChildren(_ context.Context, org core.OrganizationID, parent core.FixedChargeID) ([]*catalog.FixedCharge, error) {
	return r.d.fixedCharges.find(func(f *catalog.FixedCharge) bool { return f.OrganizationID == org && f.ParentID == parent }), nil
}

// addOnRepo implements catalog.AddOnRepository.
type addOnRepo struct{ d *catalogData }

func (r addOnRepo) check(a *catalog.AddOn) error {
	if r.d.addOns.any(func(x *catalog.AddOn) bool {
		return x.ID != a.ID && x.OrganizationID == a.OrganizationID && x.Code == a.Code
	}) {
		return conflict("add_on", "code")
	}
	return nil
}

func (r addOnRepo) Create(_ context.Context, a *catalog.AddOn) error {
	if r.d.addOns.has(a.ID) {
		return conflict("add_on", "id")
	}
	if err := r.check(a); err != nil {
		return err
	}
	r.d.addOns.put(a.ID, *a)
	return nil
}

func (r addOnRepo) Update(_ context.Context, a *catalog.AddOn) error {
	cur, ok := r.d.addOns.get(a.ID)
	if !ok || cur.OrganizationID != a.OrganizationID {
		return notFound("add_on", a.ID)
	}
	if err := r.check(a); err != nil {
		return err
	}
	r.d.addOns.put(a.ID, *a)
	return nil
}

func (r addOnRepo) Delete(ctx context.Context, org core.OrganizationID, id core.AddOnID) error {
	if _, err := r.Get(ctx, org, id); err != nil {
		return err
	}
	if r.d.fixedCharges.any(func(f *catalog.FixedCharge) bool { return f.OrganizationID == org && f.AddOnID == id }) {
		return conflict("add_on", "still referenced by fixed charges")
	}
	r.d.addOns.del(id)
	return nil
}

func (r addOnRepo) Get(_ context.Context, org core.OrganizationID, id core.AddOnID) (*catalog.AddOn, error) {
	a, ok := r.d.addOns.get(id)
	if !ok || a.OrganizationID != org {
		return nil, notFound("add_on", id)
	}
	return a, nil
}

func (r addOnRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*catalog.AddOn, error) {
	a, ok := r.d.addOns.first(func(x *catalog.AddOn) bool { return x.OrganizationID == org && x.Code == code })
	if !ok {
		return nil, notFound("add_on", stringer(code))
	}
	return a, nil
}

func (r addOnRepo) ListByIDs(_ context.Context, org core.OrganizationID, ids []core.AddOnID) ([]*catalog.AddOn, error) {
	return catalogByIDs(r.d.addOns, "add_on", ids, func(a *catalog.AddOn) bool { return a.OrganizationID == org })
}

func (r addOnRepo) List(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*catalog.AddOn], error) {
	rows := r.d.addOns.find(func(a *catalog.AddOn) bool { return a.OrganizationID == org })
	return paginate(rows, func(a *catalog.AddOn) core.AddOnID { return a.ID }, page)
}

// pricingUnitRepo implements catalog.PricingUnitRepository.
type pricingUnitRepo struct{ d *catalogData }

func (r pricingUnitRepo) check(p *catalog.PricingUnit) error {
	if r.d.pricingUnits.any(func(x *catalog.PricingUnit) bool {
		return x.ID != p.ID && x.OrganizationID == p.OrganizationID && x.Code == p.Code
	}) {
		return conflict("pricing_unit", "code")
	}
	return nil
}

func (r pricingUnitRepo) Create(_ context.Context, p *catalog.PricingUnit) error {
	if r.d.pricingUnits.has(p.ID) {
		return conflict("pricing_unit", "id")
	}
	if err := r.check(p); err != nil {
		return err
	}
	r.d.pricingUnits.put(p.ID, *p)
	return nil
}

func (r pricingUnitRepo) Update(_ context.Context, p *catalog.PricingUnit) error {
	cur, ok := r.d.pricingUnits.get(p.ID)
	if !ok || cur.OrganizationID != p.OrganizationID {
		return notFound("pricing_unit", p.ID)
	}
	if err := r.check(p); err != nil {
		return err
	}
	r.d.pricingUnits.put(p.ID, *p)
	return nil
}

func (r pricingUnitRepo) Get(_ context.Context, org core.OrganizationID, id core.PricingUnitID) (*catalog.PricingUnit, error) {
	p, ok := r.d.pricingUnits.get(id)
	if !ok || p.OrganizationID != org {
		return nil, notFound("pricing_unit", id)
	}
	return p, nil
}

func (r pricingUnitRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*catalog.PricingUnit, error) {
	p, ok := r.d.pricingUnits.first(func(x *catalog.PricingUnit) bool { return x.OrganizationID == org && x.Code == code })
	if !ok {
		return nil, notFound("pricing_unit", stringer(code))
	}
	return p, nil
}

func (r pricingUnitRepo) List(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*catalog.PricingUnit], error) {
	rows := r.d.pricingUnits.find(func(p *catalog.PricingUnit) bool { return p.OrganizationID == org })
	return paginate(rows, func(p *catalog.PricingUnit) core.PricingUnitID { return p.ID }, page)
}

var (
	_ catalog.MetricRepository      = metricRepo{}
	_ catalog.PlanRepository        = planRepo{}
	_ catalog.ChargeRepository      = chargeRepo{}
	_ catalog.FixedChargeRepository = fixedChargeRepo{}
	_ catalog.AddOnRepository       = addOnRepo{}
	_ catalog.PricingUnitRepository = pricingUnitRepo{}
)
