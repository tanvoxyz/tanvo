package porttest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Fixtures of the catalog group.

func catalogFixtureTax(ctx context.Context, t *testing.T, r port.Repos, e *env, org core.OrganizationID, code string) core.TaxID {
	t.Helper()
	tax := &catalog.Tax{
		ID: core.NewID[core.TaxID](e.ids), OrganizationID: org, Code: code, Name: "Tax " + code,
		Rate: money.MustParse("20"), CreatedAt: now, UpdatedAt: now,
	}
	noErr(t, "Taxes.Create()", r.Taxes.Create(ctx, tax))
	return tax.ID
}

func catalogFixtureMetric(e *env, org core.OrganizationID, code string) *catalog.BillableMetric {
	return &catalog.BillableMetric{
		ID: core.NewID[core.BillableMetricID](e.ids), OrganizationID: org, Code: code, Name: "Metric " + code,
		AggregationType: catalog.AggregationTypeCount, CreatedAt: now, UpdatedAt: now,
	}
}

func catalogFixturePlan(e *env, org core.OrganizationID, code string) *catalog.Plan {
	return &catalog.Plan{
		ID: core.NewID[core.PlanID](e.ids), OrganizationID: org, Code: code, Name: "Plan " + code,
		Interval: period.Monthly, Amount: 1000, Currency: money.MustCurrency("EUR"),
		Status: catalog.PlanStatusActive, CreatedAt: now, UpdatedAt: now,
	}
}

func catalogFixtureAddOn(e *env, org core.OrganizationID, code string) *catalog.AddOn {
	return &catalog.AddOn{
		ID: core.NewID[core.AddOnID](e.ids), OrganizationID: org, Code: code, Name: "Add-on " + code,
		Amount: 500, Currency: money.MustCurrency("EUR"), CreatedAt: now, UpdatedAt: now,
	}
}

func catalogFixtureCharge(e *env, org core.OrganizationID, plan core.PlanID, metric core.BillableMetricID, code string) *catalog.Charge {
	return &catalog.Charge{
		ID: core.NewID[core.ChargeID](e.ids), OrganizationID: org, PlanID: plan, Code: code, BillableMetricID: metric,
		ChargeModel: catalog.ChargeModelStandard, Properties: catalog.MustProperties(`{"amount":"1"}`),
		Invoiceable: true, CreatedAt: now, UpdatedAt: now,
	}
}

func catalogFixtureChargeFilter(e *env, charge core.ChargeID, values map[string][]string, created time.Time) catalog.ChargeFilter {
	return catalog.ChargeFilter{
		ID: core.NewID[core.ChargeFilterID](e.ids), ChargeID: charge, Values: values,
		Properties: catalog.MustProperties(`{"amount":"2"}`), Code: catalog.ChargeFilterCode(values), CreatedAt: created,
	}
}

func catalogFixtureFixedCharge(e *env, org core.OrganizationID, plan core.PlanID, addOn core.AddOnID, code string) *catalog.FixedCharge {
	return &catalog.FixedCharge{
		ID: core.NewID[core.FixedChargeID](e.ids), OrganizationID: org, PlanID: plan, Code: code, AddOnID: addOn,
		ChargeModel: catalog.FixedChargeModelStandard, Properties: catalog.MustProperties(`{"amount":"3"}`),
		Units: money.MustParse("1"), CreatedAt: now, UpdatedAt: now,
	}
}

func catalogMetricID(m *catalog.BillableMetric) core.BillableMetricID { return m.ID }
func catalogPlanID(p *catalog.Plan) core.PlanID                       { return p.ID }
func catalogChargeID(c *catalog.Charge) core.ChargeID                 { return c.ID }
func catalogFixedChargeID(f *catalog.FixedCharge) core.FixedChargeID  { return f.ID }
func catalogAddOnID(a *catalog.AddOn) core.AddOnID                    { return a.ID }
func catalogPricingUnitID(p *catalog.PricingUnit) core.PricingUnitID  { return p.ID }

func testCatalog(t *testing.T, h Harness) {
	t.Run("pricing units", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		unit := func(code string) *catalog.PricingUnit {
			return &catalog.PricingUnit{
				ID: core.NewID[core.PricingUnitID](e.ids), OrganizationID: org, Code: code, Name: "Unit " + code,
				ShortName: "CR", Description: ptr("Credits"), CreatedAt: now, UpdatedAt: now,
			}
		}
		u1, u2, u3 := unit("credits"), unit("tokens"), unit("seats")
		u2.Description = nil
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, u := range []*catalog.PricingUnit{u1, u2, u3} {
				noErr(t, "Create()", r.PricingUnits.Create(ctx, u))
			}
			wantErr(t, "Create() with the same ID", r.PricingUnits.Create(ctx, u1), core.ErrConflict)
			wantErr(t, "Create() with a taken code", r.PricingUnits.Create(ctx, unit("credits")), core.ErrConflict)
			got, err := r.PricingUnits.Get(ctx, org, u1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, u1)
			got, err = r.PricingUnits.GetByCode(ctx, org, "tokens")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, u2)
			_, err = r.PricingUnits.Get(ctx, org, core.NewID[core.PricingUnitID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.PricingUnits.GetByCode(ctx, org, "nope")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)

			u1.Name = "Credit"
			u1.Description = nil
			u1.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.PricingUnits.Update(ctx, u1))
			got, err = r.PricingUnits.Get(ctx, org, u1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, u1)
			u3.Code = "credits"
			wantErr(t, "Update() to a taken code", r.PricingUnits.Update(ctx, u3), core.ErrConflict)
			u3.Code = "seats"
			missing := unit("missing")
			wantErr(t, "Update() unknown", r.PricingUnits.Update(ctx, missing), core.ErrNotFound)

			// Isolation.
			_, err = r.PricingUnits.Get(ctx, b.OrganizationID, u1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.PricingUnits.GetByCode(ctx, b.OrganizationID, "credits")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			other := *u2
			other.OrganizationID = b.OrganizationID
			wantErr(t, "Update() through another organization", r.PricingUnits.Update(ctx, &other), core.ErrNotFound)
			// Another organization may use the same code.
			ob := unit("credits")
			ob.OrganizationID = b.OrganizationID
			noErr(t, "Create() in another organization", r.PricingUnits.Create(ctx, ob))

			all := listAll(t, 2, func(p core.PageRequest) (core.Page[*catalog.PricingUnit], error) {
				return r.PricingUnits.List(ctx, org, catalog.PricingUnitFilter{}, p)
			})
			equal(t, "List()", all, []*catalog.PricingUnit{u1, u2, u3})
			other2 := listAll(t, 2, func(p core.PageRequest) (core.Page[*catalog.PricingUnit], error) {
				return r.PricingUnits.List(ctx, b.OrganizationID, catalog.PricingUnitFilter{}, p)
			})
			equal(t, "List() of another organization", ids(other2, catalogPricingUnitID), []core.PricingUnitID{ob.ID})
			return nil
		})
	})

	t.Run("billable metrics", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		full := catalogFixtureMetric(e, org, "storage")
		full.Description = ptr("Storage used")
		full.AggregationType = catalog.AggregationTypeWeightedSum
		full.FieldName = ptr("gb")
		full.WeightedInterval = catalog.WeightedIntervalSeconds
		full.Recurring = true
		full.Expression = ptr("event.properties.gb * 2")
		full.RoundingFunction = catalog.RoundingFunctionCeil
		full.RoundingPrecision = ptr(-2)
		full.Filters = []catalog.MetricFilter{
			{ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "region", Values: []string{"eu", "us"}},
			{ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "cloud", Values: []string{"aws"}},
		}
		count := catalogFixtureMetric(e, org, "calls")
		sum := catalogFixtureMetric(e, org, "bytes")
		sum.AggregationType = catalog.AggregationTypeSum
		sum.FieldName = ptr("bytes")
		sum.RoundingPrecision = ptr(0)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, m := range []*catalog.BillableMetric{full, count, sum} {
				noErr(t, "Create()", r.Metrics.Create(ctx, m))
			}
			wantErr(t, "Create() with the same ID", r.Metrics.Create(ctx, full), core.ErrConflict)
			wantErr(t, "Create() with a taken code", r.Metrics.Create(ctx, catalogFixtureMetric(e, org, "calls")), core.ErrConflict)
			got, err := r.Metrics.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.Metrics.Get(ctx, org, count.ID)
			noErr(t, "Get() without optional fields", err)
			equal(t, "Get() without optional fields", got, count)
			got, err = r.Metrics.GetByCode(ctx, org, "bytes")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, sum)
			_, err = r.Metrics.Get(ctx, org, core.NewID[core.BillableMetricID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.Metrics.GetByCode(ctx, org, "nope")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)

			// Update replaces the filters.
			full.Name = "Storage"
			full.Description = nil
			full.Expression = nil
			full.Filters = []catalog.MetricFilter{
				{ID: full.Filters[1].ID, Key: "cloud", Values: []string{"aws", "gcp"}},
				{ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "tier", Values: []string{"hot"}},
			}
			full.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Metrics.Update(ctx, full))
			got, err = r.Metrics.Get(ctx, org, full.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, full)
			sum.Code = "calls"
			wantErr(t, "Update() to a taken code", r.Metrics.Update(ctx, sum), core.ErrConflict)
			sum.Code = "bytes"
			wantErr(t, "Update() unknown", r.Metrics.Update(ctx, catalogFixtureMetric(e, org, "missing")), core.ErrNotFound)
			count.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "k", Values: []string{"v"}}}
			noErr(t, "Update() adding filters", r.Metrics.Update(ctx, count))
			count.Filters = nil
			noErr(t, "Update() removing filters", r.Metrics.Update(ctx, count))
			got, err = r.Metrics.Get(ctx, org, count.ID)
			noErr(t, "Get() after removing filters", err)
			equal(t, "Get() after removing filters", got, count)
			// A removed key is discarded, so a new filter can take it again.
			count.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "k", Values: []string{"w"}}}
			noErr(t, "Update() re-adding a removed key", r.Metrics.Update(ctx, count))
			got, err = r.Metrics.Get(ctx, org, count.ID)
			noErr(t, "Get() after re-adding a removed key", err)
			equal(t, "Get() after re-adding a removed key", got, count)
			count.Filters = nil
			noErr(t, "Update() removing the filter again", r.Metrics.Update(ctx, count))

			byIDs, err := r.Metrics.ListByIDs(ctx, org, []core.BillableMetricID{sum.ID, full.ID})
			noErr(t, "ListByIDs()", err)
			equal(t, "ListByIDs()", byIDs, []*catalog.BillableMetric{sum, full})
			_, err = r.Metrics.ListByIDs(ctx, org, []core.BillableMetricID{sum.ID, core.NewID[core.BillableMetricID](e.ids)})
			wantErr(t, "ListByIDs() with a missing ID", err, core.ErrNotFound)
			empty, err := r.Metrics.ListByIDs(ctx, org, nil)
			noErr(t, "ListByIDs() of nothing", err)
			equal(t, "ListByIDs() of nothing", empty, []*catalog.BillableMetric{})
			byCodes, err := r.Metrics.ListByCodes(ctx, org, []string{"calls", "storage"})
			noErr(t, "ListByCodes()", err)
			equal(t, "ListByCodes()", byCodes, []*catalog.BillableMetric{count, full})
			_, err = r.Metrics.ListByCodes(ctx, org, []string{"calls", "nope"})
			wantErr(t, "ListByCodes() with a missing code", err, core.ErrNotFound)

			// Isolation.
			ob := b.OrganizationID
			_, err = r.Metrics.Get(ctx, ob, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Metrics.GetByCode(ctx, ob, "storage")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			_, err = r.Metrics.ListByIDs(ctx, ob, []core.BillableMetricID{full.ID})
			wantErr(t, "ListByIDs() from another organization", err, core.ErrNotFound)
			_, err = r.Metrics.ListByCodes(ctx, ob, []string{"storage"})
			wantErr(t, "ListByCodes() from another organization", err, core.ErrNotFound)
			other := *sum
			other.OrganizationID = ob
			wantErr(t, "Update() through another organization", r.Metrics.Update(ctx, &other), core.ErrNotFound)
			wantErr(t, "Discard() from another organization", r.Metrics.Discard(ctx, ob, sum.ID, at(time.Hour)), core.ErrNotFound)
			obm := catalogFixtureMetric(e, ob, "storage")
			noErr(t, "Create() in another organization", r.Metrics.Create(ctx, obm))
			p, err := r.Metrics.List(ctx, ob, catalog.MetricListFilter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", ids(p.Items, catalogMetricID), []core.BillableMetricID{obm.ID})

			// Lists and filters.
			all := listAll(t, 1, func(p core.PageRequest) (core.Page[*catalog.BillableMetric], error) {
				return r.Metrics.List(ctx, org, catalog.MetricListFilter{}, p)
			})
			equal(t, "List()", all, []*catalog.BillableMetric{full, count, sum})
			list := func(f catalog.MetricListFilter) []core.BillableMetricID {
				return ids(listAll(t, 2, func(p core.PageRequest) (core.Page[*catalog.BillableMetric], error) {
					return r.Metrics.List(ctx, org, f, p)
				}), catalogMetricID)
			}
			equal(t, "List() by aggregation types",
				list(catalog.MetricListFilter{AggregationTypes: []catalog.AggregationType{catalog.AggregationTypeSum, catalog.AggregationTypeCount}}),
				[]core.BillableMetricID{count.ID, sum.ID})
			equal(t, "List() recurring", list(catalog.MetricListFilter{Recurring: ptr(true)}), []core.BillableMetricID{full.ID})
			equal(t, "List() not recurring", list(catalog.MetricListFilter{Recurring: ptr(false)}), []core.BillableMetricID{count.ID, sum.ID})
			equal(t, "List() searching a name", list(catalog.MetricListFilter{Search: " STOR "}), []core.BillableMetricID{full.ID})
			equal(t, "List() searching a code", list(catalog.MetricListFilter{Search: "byt"}), []core.BillableMetricID{sum.ID})
			equal(t, "List() searching a wildcard", list(catalog.MetricListFilter{Search: "%"}), []core.BillableMetricID{})
			recent := slices.Clone(all)
			slices.SortStableFunc(recent, func(a, b *catalog.BillableMetric) int {
				if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
					return c
				}
				return strings.Compare(a.ID.String(), b.ID.String())
			})
			equal(t, "List() recent first", list(catalog.MetricListFilter{Order: core.OrderRecent}), ids(recent, catalogMetricID))
			_, err = r.Metrics.List(ctx, org, catalog.MetricListFilter{Order: core.OrderRecent}, core.PageRequest{Token: full.ID.String()})
			wantErr(t, "List() recent first with an ID token", err, core.ErrInvalidValue)
			_, err = r.Metrics.List(ctx, org, catalog.MetricListFilter{Order: core.ListOrder(9)}, core.PageRequest{})
			wantErr(t, "List() in an undeclared order", err, core.ErrInvalidValue)

			// InUse and the plan filter follow the charges.
			plan := catalogFixturePlan(e, org, "starter")
			noErr(t, "Plans.Create()", r.Plans.Create(ctx, plan))
			equal(t, "List() of a plan without charges", list(catalog.MetricListFilter{PlanID: plan.ID}), []core.BillableMetricID{})
			used, err := r.Metrics.InUse(ctx, org, sum.ID)
			noErr(t, "InUse()", err)
			equal(t, "InUse() without charges", used, false)
			charge := catalogFixtureCharge(e, org, plan.ID, sum.ID, "bytes")
			noErr(t, "Charges.Create()", r.Charges.Create(ctx, charge))
			used, err = r.Metrics.InUse(ctx, org, sum.ID)
			noErr(t, "InUse()", err)
			equal(t, "InUse() with a charge", used, true)
			used, err = r.Metrics.InUse(ctx, ob, sum.ID)
			noErr(t, "InUse() from another organization", err)
			equal(t, "InUse() from another organization", used, false)
			equal(t, "List() of a plan", list(catalog.MetricListFilter{PlanID: plan.ID}), []core.BillableMetricID{sum.ID})
			equal(t, "List() of a plan, recurring", list(catalog.MetricListFilter{PlanID: plan.ID, Recurring: ptr(true)}), []core.BillableMetricID{})

			// Discard. The services discard a metric's charges with it.
			n, err := r.Charges.DiscardByMetric(ctx, org, sum.ID, at(time.Hour))
			noErr(t, "Charges.DiscardByMetric()", err)
			equal(t, "Charges.DiscardByMetric()", n, 1)
			used, err = r.Metrics.InUse(ctx, org, sum.ID)
			noErr(t, "InUse() after Charges.DiscardByMetric()", err)
			equal(t, "InUse() after Charges.DiscardByMetric()", used, false)
			noErr(t, "Discard()", r.Metrics.Discard(ctx, org, sum.ID, at(time.Hour)))
			wantErr(t, "Discard() again", r.Metrics.Discard(ctx, org, sum.ID, at(2*time.Hour)), core.ErrNotFound)
			_, err = r.Metrics.Get(ctx, org, sum.ID)
			wantErr(t, "Get() discarded", err, core.ErrNotFound)
			_, err = r.Metrics.GetByCode(ctx, org, sum.Code)
			wantErr(t, "GetByCode() discarded", err, core.ErrNotFound)
			_, err = r.Metrics.ListByIDs(ctx, org, []core.BillableMetricID{sum.ID})
			wantErr(t, "ListByIDs() discarded", err, core.ErrNotFound)
			_, err = r.Metrics.ListByCodes(ctx, org, []string{sum.Code})
			wantErr(t, "ListByCodes() discarded", err, core.ErrNotFound)
			wantErr(t, "Update() discarded", r.Metrics.Update(ctx, sum), core.ErrNotFound)
			equal(t, "List() without the discarded metric", list(catalog.MetricListFilter{}), []core.BillableMetricID{full.ID, count.ID})
			noErr(t, "Discard() with filters", r.Metrics.Discard(ctx, org, full.ID, at(time.Hour)))
			got, err = r.Metrics.GetWithDiscarded(ctx, org, full.ID)
			noErr(t, "GetWithDiscarded() before DiscardFilters()", err)
			equal(t, "filters before DiscardFilters()", len(got.Filters), 2)
			nf, err := r.Metrics.DiscardFilters(ctx, ob, full.ID, at(2*time.Hour))
			noErr(t, "DiscardFilters() from another organization", err)
			equal(t, "DiscardFilters() from another organization", nf, 0)
			nf, err = r.Metrics.DiscardFilters(ctx, org, full.ID, at(2*time.Hour))
			noErr(t, "DiscardFilters()", err)
			equal(t, "DiscardFilters()", nf, 2)
			got, err = r.Metrics.GetWithDiscarded(ctx, org, full.ID)
			noErr(t, "GetWithDiscarded() after DiscardFilters()", err)
			equal(t, "filters after DiscardFilters()", len(got.Filters), 0)
			nf, err = r.Metrics.DiscardFilters(ctx, org, full.ID, at(3*time.Hour))
			noErr(t, "DiscardFilters() again", err)
			equal(t, "DiscardFilters() again", nf, 0)
			// A discarded metric frees its code.
			noErr(t, "Create() with a freed code", r.Metrics.Create(ctx, catalogFixtureMetric(e, org, "storage")))
			return nil
		})
	})

	t.Run("plans", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, ob := a.OrganizationID, b.OrganizationID
		e.tx(func(ctx context.Context, r port.Repos) error {
			vat := catalogFixtureTax(ctx, t, r, e, org, "vat")
			gst := catalogFixtureTax(ctx, t, r, e, org, "gst")

			full := catalogFixturePlan(e, org, "premium")
			full.InvoiceDisplayName = ptr("Premium plan")
			full.Description = ptr("All features")
			full.Interval = period.Yearly
			full.Amount = 120000
			full.Currency = money.MustCurrency("USD")
			full.PayInAdvance = true
			full.TrialPeriodDays = 14
			full.BillChargesMonthly = ptr(true)
			full.BillFixedChargesMonthly = ptr(false)
			full.TaxIDs = []core.TaxID{gst, vat}
			full.Metadata = core.Metadata{{Key: "tier", Value: "gold", DisplayInInvoice: true}, {Key: "crm", Value: "42"}}
			basic := catalogFixturePlan(e, org, "basic")
			noErr(t, "Create()", r.Plans.Create(ctx, full))
			noErr(t, "Create() without optional fields", r.Plans.Create(ctx, basic))
			wantErr(t, "Create() with the same ID", r.Plans.Create(ctx, full), core.ErrConflict)
			wantErr(t, "Create() with a taken code", r.Plans.Create(ctx, catalogFixturePlan(e, org, "basic")), core.ErrConflict)

			got, err := r.Plans.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.Plans.Get(ctx, org, basic.ID)
			noErr(t, "Get() without optional fields", err)
			equal(t, "Get() without optional fields", got, basic)
			_, err = r.Plans.Get(ctx, org, core.NewID[core.PlanID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Override plans share the parent's code.
			child := full.NewOverride(core.NewID[core.PlanID](e.ids), at(time.Hour))
			child.Amount = 99000
			child.TaxIDs = []core.TaxID{vat}
			noErr(t, "Create() override", r.Plans.Create(ctx, child))
			child2 := full.NewOverride(core.NewID[core.PlanID](e.ids), at(2*time.Hour))
			child2.TaxIDs = nil
			noErr(t, "Create() second override", r.Plans.Create(ctx, child2))
			got, err = r.Plans.Get(ctx, org, child.ID)
			noErr(t, "Get() override", err)
			equal(t, "Get() override", got, child)
			got, err = r.Plans.GetByCode(ctx, org, "premium")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode() returns the original plan", got, full)
			_, err = r.Plans.GetByCode(ctx, org, "nope")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)
			children, err := r.Plans.ListChildren(ctx, org, full.ID)
			noErr(t, "ListChildren()", err)
			equal(t, "ListChildren()", children, []*catalog.Plan{child, child2})
			children, err = r.Plans.ListChildren(ctx, org, basic.ID)
			noErr(t, "ListChildren() without children", err)
			equal(t, "ListChildren() without children", children, []*catalog.Plan{})

			// Update.
			full.Name = "Premium"
			full.Description = nil
			full.BillChargesMonthly = nil
			full.TaxIDs = []core.TaxID{vat}
			full.Metadata = nil
			full.Status = catalog.PlanStatusPendingDeletion
			full.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Plans.Update(ctx, full))
			got, err = r.Plans.Get(ctx, org, full.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, full)
			basic.Code = "premium"
			wantErr(t, "Update() to a taken code", r.Plans.Update(ctx, basic), core.ErrConflict)
			basic.Code = "basic"
			child2.Code = "basic"
			noErr(t, "Update() of an override to any code", r.Plans.Update(ctx, child2))
			wantErr(t, "Update() unknown", r.Plans.Update(ctx, catalogFixturePlan(e, org, "missing")), core.ErrNotFound)

			byIDs, err := r.Plans.ListByIDs(ctx, org, []core.PlanID{child.ID, basic.ID, full.ID})
			noErr(t, "ListByIDs()", err)
			equal(t, "ListByIDs()", byIDs, []*catalog.Plan{child, basic, full})
			_, err = r.Plans.ListByIDs(ctx, org, []core.PlanID{basic.ID, core.NewID[core.PlanID](e.ids)})
			wantErr(t, "ListByIDs() with a missing ID", err, core.ErrNotFound)

			// Lists.
			list := func(f catalog.PlanListFilter) []core.PlanID {
				return ids(listAll(t, 1, func(p core.PageRequest) (core.Page[*catalog.Plan], error) {
					return r.Plans.List(ctx, org, f, p)
				}), catalogPlanID)
			}
			equal(t, "List()", list(catalog.PlanListFilter{}), []core.PlanID{full.ID, basic.ID})
			equal(t, "List() with children", list(catalog.PlanListFilter{IncludeChildren: true}), []core.PlanID{full.ID, basic.ID, child.ID, child2.ID})
			equal(t, "List() active", list(catalog.PlanListFilter{Statuses: []catalog.PlanStatus{catalog.PlanStatusActive}}), []core.PlanID{basic.ID})
			equal(t, "List() pending deletion with children",
				list(catalog.PlanListFilter{IncludeChildren: true, Statuses: []catalog.PlanStatus{catalog.PlanStatusPendingDeletion}}),
				[]core.PlanID{full.ID})
			all := listAll(t, 3, func(p core.PageRequest) (core.Page[*catalog.Plan], error) {
				return r.Plans.List(ctx, org, catalog.PlanListFilter{IncludeChildren: true}, p)
			})
			equal(t, "List() items", all, []*catalog.Plan{full, basic, child, child2})
			byName := catalog.PlanListFilter{Statuses: []catalog.PlanStatus{catalog.PlanStatusActive, catalog.PlanStatusPendingDeletion}, Order: core.OrderRecent}
			equal(t, "List() by name", list(byName), []core.PlanID{basic.ID, full.ID})
			byName.Search = "PREM"
			equal(t, "List() searched", list(byName), []core.PlanID{full.ID})
			byName.Search = "basic"
			equal(t, "List() searched by code", list(byName), []core.PlanID{basic.ID})
			_, err = r.Plans.List(ctx, org, catalog.PlanListFilter{Order: core.ListOrder(9)}, core.PageRequest{})
			wantErr(t, "List() with an invalid order", err, core.ErrInvalidValue)

			// Isolation.
			_, err = r.Plans.Get(ctx, ob, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Plans.GetByCode(ctx, ob, "premium")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			_, err = r.Plans.ListByIDs(ctx, ob, []core.PlanID{full.ID})
			wantErr(t, "ListByIDs() from another organization", err, core.ErrNotFound)
			children, err = r.Plans.ListChildren(ctx, ob, full.ID)
			noErr(t, "ListChildren() from another organization", err)
			equal(t, "ListChildren() from another organization", children, []*catalog.Plan{})
			other := *basic
			other.OrganizationID = ob
			wantErr(t, "Update() through another organization", r.Plans.Update(ctx, &other), core.ErrNotFound)
			obp := catalogFixturePlan(e, ob, "premium")
			noErr(t, "Create() in another organization", r.Plans.Create(ctx, obp))
			p, err := r.Plans.List(ctx, ob, catalog.PlanListFilter{IncludeChildren: true}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", ids(p.Items, catalogPlanID), []core.PlanID{obp.ID})
			return nil
		})
	})

	t.Run("add-ons", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, ob := a.OrganizationID, b.OrganizationID
		e.tx(func(ctx context.Context, r port.Repos) error {
			vat := catalogFixtureTax(ctx, t, r, e, org, "vat")
			gst := catalogFixtureTax(ctx, t, r, e, org, "gst")
			full := catalogFixtureAddOn(e, org, "setup")
			full.InvoiceDisplayName = ptr("Setup fee")
			full.Description = ptr("One-time setup")
			full.Currency = money.MustCurrency("JPY")
			full.TaxIDs = []core.TaxID{vat, gst}
			bare := catalogFixtureAddOn(e, org, "support")
			third := catalogFixtureAddOn(e, org, "training")
			for _, ad := range []*catalog.AddOn{full, bare, third} {
				noErr(t, "Create()", r.AddOns.Create(ctx, ad))
			}
			wantErr(t, "Create() with the same ID", r.AddOns.Create(ctx, full), core.ErrConflict)
			wantErr(t, "Create() with a taken code", r.AddOns.Create(ctx, catalogFixtureAddOn(e, org, "setup")), core.ErrConflict)
			got, err := r.AddOns.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.AddOns.GetByCode(ctx, org, "support")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, bare)
			_, err = r.AddOns.Get(ctx, org, core.NewID[core.AddOnID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.AddOns.GetByCode(ctx, org, "nope")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)

			full.Name = "Setup"
			full.Description = nil
			full.TaxIDs = []core.TaxID{gst}
			full.Amount = 900
			full.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.AddOns.Update(ctx, full))
			got, err = r.AddOns.Get(ctx, org, full.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, full)
			bare.Code = "setup"
			wantErr(t, "Update() to a taken code", r.AddOns.Update(ctx, bare), core.ErrConflict)
			bare.Code = "support"
			wantErr(t, "Update() unknown", r.AddOns.Update(ctx, catalogFixtureAddOn(e, org, "missing")), core.ErrNotFound)

			byIDs, err := r.AddOns.ListByIDs(ctx, org, []core.AddOnID{third.ID, full.ID})
			noErr(t, "ListByIDs()", err)
			equal(t, "ListByIDs()", byIDs, []*catalog.AddOn{third, full})
			_, err = r.AddOns.ListByIDs(ctx, org, []core.AddOnID{core.NewID[core.AddOnID](e.ids)})
			wantErr(t, "ListByIDs() with a missing ID", err, core.ErrNotFound)
			all := listAll(t, 2, func(p core.PageRequest) (core.Page[*catalog.AddOn], error) {
				return r.AddOns.List(ctx, org, catalog.AddOnFilter{}, p)
			})
			equal(t, "List()", all, []*catalog.AddOn{full, bare, third})

			// Isolation.
			_, err = r.AddOns.Get(ctx, ob, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.AddOns.GetByCode(ctx, ob, "setup")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			_, err = r.AddOns.ListByIDs(ctx, ob, []core.AddOnID{full.ID})
			wantErr(t, "ListByIDs() from another organization", err, core.ErrNotFound)
			other := *bare
			other.OrganizationID = ob
			wantErr(t, "Update() through another organization", r.AddOns.Update(ctx, &other), core.ErrNotFound)
			wantErr(t, "Discard() from another organization", r.AddOns.Discard(ctx, ob, bare.ID, at(time.Hour)), core.ErrNotFound)
			oba := catalogFixtureAddOn(e, ob, "setup")
			noErr(t, "Create() in another organization", r.AddOns.Create(ctx, oba))
			p, err := r.AddOns.List(ctx, ob, catalog.AddOnFilter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", ids(p.Items, catalogAddOnID), []core.AddOnID{oba.ID})

			// Discards keep the row for Get only.
			noErr(t, "Discard()", r.AddOns.Discard(ctx, org, full.ID, at(time.Hour)))
			wantErr(t, "Discard() again", r.AddOns.Discard(ctx, org, full.ID, at(2*time.Hour)), core.ErrNotFound)
			got, err = r.AddOns.Get(ctx, org, full.ID)
			noErr(t, "Get() discarded", err)
			equal(t, "Get() discarded", got.DeletedAt, ptr(at(time.Hour)))
			equal(t, "Get() discarded, updated_at", got.UpdatedAt, at(time.Hour))
			_, err = r.AddOns.GetByCode(ctx, org, "setup")
			wantErr(t, "GetByCode() discarded", err, core.ErrNotFound)
			_, err = r.AddOns.ListByIDs(ctx, org, []core.AddOnID{full.ID})
			wantErr(t, "ListByIDs() discarded", err, core.ErrNotFound)
			wantErr(t, "Update() discarded", r.AddOns.Update(ctx, full), core.ErrNotFound)
			p, err = r.AddOns.List(ctx, org, catalog.AddOnFilter{}, core.PageRequest{})
			noErr(t, "List() after Discard()", err)
			equal(t, "List() after Discard()", ids(p.Items, catalogAddOnID), []core.AddOnID{bare.ID, third.ID})
			noErr(t, "Create() with a freed code", r.AddOns.Create(ctx, catalogFixtureAddOn(e, org, "setup")))
			return nil
		})
	})

	t.Run("charges", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, ob := a.OrganizationID, b.OrganizationID
		e.tx(func(ctx context.Context, r port.Repos) error {
			vat := catalogFixtureTax(ctx, t, r, e, org, "vat")
			gst := catalogFixtureTax(ctx, t, r, e, org, "gst")
			unit := &catalog.PricingUnit{
				ID: core.NewID[core.PricingUnitID](e.ids), OrganizationID: org, Code: "credits", Name: "Credits",
				ShortName: "CR", CreatedAt: now, UpdatedAt: now,
			}
			noErr(t, "PricingUnits.Create()", r.PricingUnits.Create(ctx, unit))
			metric := catalogFixtureMetric(e, org, "calls")
			metric.Filters = []catalog.MetricFilter{
				{ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "region", Values: []string{"eu", "us"}},
				{ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "tier", Values: []string{"hot", "cold"}},
			}
			metric2 := catalogFixtureMetric(e, org, "users")
			noErr(t, "Metrics.Create()", r.Metrics.Create(ctx, metric))
			noErr(t, "Metrics.Create()", r.Metrics.Create(ctx, metric2))
			plan := catalogFixturePlan(e, org, "starter")
			plan2 := catalogFixturePlan(e, org, "growth")
			noErr(t, "Plans.Create()", r.Plans.Create(ctx, plan))
			noErr(t, "Plans.Create()", r.Plans.Create(ctx, plan2))

			full := catalogFixtureCharge(e, org, plan.ID, metric.ID, "calls")
			full.ChargeModel = catalog.ChargeModelGraduated
			full.Properties = catalog.MustProperties(`{"graduated_ranges":[{"to":10,"per_unit_amount":"0.5","flat_amount":"0"},{"to":null,"per_unit_amount":"0.1","flat_amount":"1"}]}`)
			full.PayInAdvance = true
			full.Invoiceable = false
			full.RegroupPaidFees = catalog.RegroupPaidFeesInvoice
			full.Prorated = true
			full.MinAmount = 0
			full.InvoiceDisplayName = ptr("API calls")
			full.AppliedPricingUnit = &catalog.AppliedPricingUnit{PricingUnitID: unit.ID, ConversionRate: money.MustParse("0.0125")}
			full.TaxIDs = []core.TaxID{vat, gst}
			fEU := catalogFixtureChargeFilter(e, full.ID, map[string][]string{"region": {"eu"}}, at(2*time.Second))
			fEU.InvoiceDisplayName = ptr("Europe")
			fAll := catalogFixtureChargeFilter(e, full.ID, map[string][]string{"region": {catalog.AllFilterValues}, "tier": {"hot", "cold"}}, at(time.Second))
			full.Filters = []catalog.ChargeFilter{fEU, fAll}
			bare := catalogFixtureCharge(e, org, plan.ID, metric2.ID, "users")
			bare.MinAmount = 250
			onPlan2 := catalogFixtureCharge(e, org, plan2.ID, metric.ID, "calls")
			for _, c := range []*catalog.Charge{full, bare, onPlan2} {
				noErr(t, "Create()", r.Charges.Create(ctx, c))
			}
			wantErr(t, "Create() with the same ID", r.Charges.Create(ctx, full), core.ErrConflict)
			wantErr(t, "Create() with a code taken on the plan", r.Charges.Create(ctx, catalogFixtureCharge(e, org, plan.ID, metric.ID, "calls")), core.ErrConflict)
			dupFilters := catalogFixtureCharge(e, org, plan.ID, metric.ID, "calls_2")
			f1 := catalogFixtureChargeFilter(e, dupFilters.ID, map[string][]string{"region": {"us"}}, now)
			f2 := catalogFixtureChargeFilter(e, dupFilters.ID, map[string][]string{"region": {"us"}}, now)
			dupFilters.Filters = []catalog.ChargeFilter{f1, f2}
			wantErr(t, "Create() with a repeated filter code", r.Charges.Create(ctx, dupFilters), core.ErrConflict)
			_, err := r.Charges.Get(ctx, org, dupFilters.ID)
			wantErr(t, "Get() of a refused charge", err, core.ErrNotFound)

			got, err := r.Charges.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.Charges.Get(ctx, org, bare.ID)
			noErr(t, "Get() without optional fields", err)
			equal(t, "Get() without optional fields", got, bare)
			got, err = r.Charges.GetByCode(ctx, org, plan2.ID, "calls")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, onPlan2)
			_, err = r.Charges.GetByCode(ctx, org, plan2.ID, "users")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)
			_, err = r.Charges.Get(ctx, org, core.NewID[core.ChargeID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			gotFilter, err := r.Charges.GetFilter(ctx, org, fEU.ID)
			noErr(t, "GetFilter()", err)
			equal(t, "GetFilter()", gotFilter, &fEU)
			_, err = r.Charges.GetFilter(ctx, org, core.NewID[core.ChargeFilterID](e.ids))
			wantErr(t, "GetFilter() unknown", err, core.ErrNotFound)
			filters, err := r.Charges.ListFilters(ctx, org, full.ID)
			noErr(t, "ListFilters()", err)
			equal(t, "ListFilters() in creation order", filters, []*catalog.ChargeFilter{&fAll, &fEU})
			filters, err = r.Charges.ListFilters(ctx, org, bare.ID)
			noErr(t, "ListFilters() without filters", err)
			equal(t, "ListFilters() without filters", filters, []*catalog.ChargeFilter{})
			_, err = r.Charges.ListFilters(ctx, org, core.NewID[core.ChargeID](e.ids))
			wantErr(t, "ListFilters() of an unknown charge", err, core.ErrNotFound)

			// Override charges copy the parent's code on the override plan.
			child := full.NewOverride(core.NewID[core.ChargeID](e.ids), plan.ID, func() core.ChargeFilterID { return core.NewID[core.ChargeFilterID](e.ids) }, at(time.Hour))
			child.TaxIDs = nil
			child.AppliedPricingUnit = nil
			noErr(t, "Create() override on the same plan", r.Charges.Create(ctx, child))
			got, err = r.Charges.Get(ctx, org, child.ID)
			noErr(t, "Get() override", err)
			equal(t, "Get() override", got, child)
			children, err := r.Charges.ListChildren(ctx, org, full.ID)
			noErr(t, "ListChildren()", err)
			equal(t, "ListChildren()", children, []*catalog.Charge{child})

			// Update replaces filters and taxes.
			fUS := catalogFixtureChargeFilter(e, full.ID, map[string][]string{"region": {"us"}, "tier": {"cold"}}, at(3*time.Second))
			full.Filters = []catalog.ChargeFilter{fUS, fEU}
			full.Filters[1].Properties = catalog.MustProperties(`{"amount":"9"}`)
			full.Filters[1].InvoiceDisplayName = nil
			full.TaxIDs = []core.TaxID{gst}
			full.AppliedPricingUnit = nil
			full.InvoiceDisplayName = nil
			full.RegroupPaidFees = ""
			full.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Charges.Update(ctx, full))
			got, err = r.Charges.Get(ctx, org, full.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, full)
			removed, err := r.Charges.GetFilter(ctx, org, fAll.ID)
			noErr(t, "GetFilter() of a removed filter", err)
			equal(t, "GetFilter() of a removed filter", removed.DeletedAt, ptr(at(time.Minute)))
			filters, err = r.Charges.ListFilters(ctx, org, full.ID)
			noErr(t, "ListFilters() after Update()", err)
			equal(t, "ListFilters() after Update()", ids(filters, func(f *catalog.ChargeFilter) core.ChargeFilterID { return f.ID }),
				[]core.ChargeFilterID{fEU.ID, fUS.ID})
			bare.Code = "calls"
			wantErr(t, "Update() to a taken code", r.Charges.Update(ctx, bare), core.ErrConflict)
			bare.Code = "users"
			full.Filters = append(full.Filters, catalogFixtureChargeFilter(e, full.ID, map[string][]string{"region": {"us"}, "tier": {"cold"}}, now))
			wantErr(t, "Update() with a repeated filter code", r.Charges.Update(ctx, full), core.ErrConflict)
			full.Filters = full.Filters[:2]
			got, err = r.Charges.Get(ctx, org, full.ID)
			noErr(t, "Get() after a refused Update()", err)
			equal(t, "Get() after a refused Update()", got, full)
			wantErr(t, "Update() unknown", r.Charges.Update(ctx, catalogFixtureCharge(e, org, plan.ID, metric.ID, "x")), core.ErrNotFound)

			byPlan, err := r.Charges.ListByPlan(ctx, org, plan.ID)
			noErr(t, "ListByPlan()", err)
			equal(t, "ListByPlan()", byPlan, []*catalog.Charge{full, bare, child})
			byMetric, err := r.Charges.ListByMetric(ctx, org, metric.ID)
			noErr(t, "ListByMetric()", err)
			equal(t, "ListByMetric()", ids(byMetric, catalogChargeID), []core.ChargeID{full.ID, onPlan2.ID, child.ID})

			// Isolation.
			_, err = r.Charges.Get(ctx, ob, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Charges.GetByCode(ctx, ob, plan.ID, "calls")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			_, err = r.Charges.GetFilter(ctx, ob, fEU.ID)
			wantErr(t, "GetFilter() from another organization", err, core.ErrNotFound)
			_, err = r.Charges.ListFilters(ctx, ob, full.ID)
			wantErr(t, "ListFilters() from another organization", err, core.ErrNotFound)
			for what, list := range map[string]func() ([]*catalog.Charge, error){
				"ListByPlan()":   func() ([]*catalog.Charge, error) { return r.Charges.ListByPlan(ctx, ob, plan.ID) },
				"ListByMetric()": func() ([]*catalog.Charge, error) { return r.Charges.ListByMetric(ctx, ob, metric.ID) },
				"ListChildren()": func() ([]*catalog.Charge, error) { return r.Charges.ListChildren(ctx, ob, full.ID) },
			} {
				cs, err := list()
				noErr(t, what+" from another organization", err)
				equal(t, what+" from another organization", cs, []*catalog.Charge{})
			}
			other := *bare
			other.OrganizationID = ob
			wantErr(t, "Update() through another organization", r.Charges.Update(ctx, &other), core.ErrNotFound)
			wantErr(t, "Discard() from another organization", r.Charges.Discard(ctx, ob, bare.ID, at(time.Hour)), core.ErrNotFound)
			n, err := r.Charges.DiscardByPlan(ctx, ob, plan.ID, at(time.Hour))
			noErr(t, "DiscardByPlan() from another organization", err)
			equal(t, "DiscardByPlan() from another organization", n, 0)
			n, err = r.Charges.DiscardByMetric(ctx, ob, metric.ID, at(time.Hour))
			noErr(t, "DiscardByMetric() from another organization", err)
			equal(t, "DiscardByMetric() from another organization", n, 0)

			// Discards keep the rows for Get and GetFilter only; an override
			// stays kept when its parent goes.
			noErr(t, "Discard()", r.Charges.Discard(ctx, org, full.ID, at(time.Hour)))
			wantErr(t, "Discard() again", r.Charges.Discard(ctx, org, full.ID, at(2*time.Hour)), core.ErrNotFound)
			got, err = r.Charges.Get(ctx, org, full.ID)
			noErr(t, "Get() discarded", err)
			equal(t, "Get() discarded", got.DeletedAt, ptr(at(time.Hour)))
			equal(t, "Get() discarded, updated_at", got.UpdatedAt, at(time.Hour))
			equal(t, "Get() discarded, filters", got.Filters, []catalog.ChargeFilter{})
			removed, err = r.Charges.GetFilter(ctx, org, fEU.ID)
			noErr(t, "GetFilter() of a discarded charge", err)
			equal(t, "GetFilter() of a discarded charge", removed.DeletedAt, ptr(at(time.Hour)))
			got, err = r.Charges.GetByCode(ctx, org, plan.ID, "calls")
			noErr(t, "GetByCode() discarded", err)
			equal(t, "GetByCode() discarded finds the kept override", got.ID, child.ID)
			_, err = r.Charges.ListFilters(ctx, org, full.ID)
			wantErr(t, "ListFilters() of a discarded charge", err, core.ErrNotFound)
			wantErr(t, "Update() discarded", r.Charges.Update(ctx, full), core.ErrNotFound)
			children, err = r.Charges.ListChildren(ctx, org, full.ID)
			noErr(t, "ListChildren() of a discarded charge", err)
			equal(t, "ListChildren() of a discarded charge", ids(children, catalogChargeID), []core.ChargeID{child.ID})
			noErr(t, "Create() with a freed code", r.Charges.Create(ctx, catalogFixtureCharge(e, org, plan.ID, metric.ID, "calls")))
			n, err = r.Charges.DiscardByPlan(ctx, org, plan.ID, at(2*time.Hour))
			noErr(t, "DiscardByPlan()", err)
			equal(t, "DiscardByPlan()", n, 3)
			n, err = r.Charges.DiscardByMetric(ctx, org, metric.ID, at(3*time.Hour))
			noErr(t, "DiscardByMetric()", err)
			equal(t, "DiscardByMetric()", n, 1)
			byMetric, err = r.Charges.ListByMetric(ctx, org, metric.ID)
			noErr(t, "ListByMetric() after discards", err)
			equal(t, "ListByMetric() after discards", byMetric, []*catalog.Charge{})
			byPlan, err = r.Charges.ListByPlan(ctx, org, plan.ID)
			noErr(t, "ListByPlan() after discards", err)
			equal(t, "ListByPlan() after discards", byPlan, []*catalog.Charge{})
			return nil
		})
	})

	t.Run("fixed charges", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, ob := a.OrganizationID, b.OrganizationID
		e.tx(func(ctx context.Context, r port.Repos) error {
			vat := catalogFixtureTax(ctx, t, r, e, org, "vat")
			gst := catalogFixtureTax(ctx, t, r, e, org, "gst")
			seats := catalogFixtureAddOn(e, org, "seats")
			setup := catalogFixtureAddOn(e, org, "setup")
			noErr(t, "AddOns.Create()", r.AddOns.Create(ctx, seats))
			noErr(t, "AddOns.Create()", r.AddOns.Create(ctx, setup))
			plan := catalogFixturePlan(e, org, "starter")
			plan2 := catalogFixturePlan(e, org, "growth")
			noErr(t, "Plans.Create()", r.Plans.Create(ctx, plan))
			noErr(t, "Plans.Create()", r.Plans.Create(ctx, plan2))

			full := catalogFixtureFixedCharge(e, org, plan.ID, seats.ID, "seats")
			full.ChargeModel = catalog.FixedChargeModelGraduated
			full.Properties = catalog.MustProperties(`{"graduated_ranges":[{"to":null,"per_unit_amount":"5","flat_amount":"0"}]}`)
			full.Units = money.MustParse("12.5")
			full.PayInAdvance = true
			full.InvoiceDisplayName = ptr("Seats")
			full.TaxIDs = []core.TaxID{gst, vat}
			bare := catalogFixtureFixedCharge(e, org, plan.ID, setup.ID, "setup")
			bare.Units = money.MustParse("0")
			bare.Prorated = true
			onPlan2 := catalogFixtureFixedCharge(e, org, plan2.ID, seats.ID, "seats")
			for _, f := range []*catalog.FixedCharge{full, bare, onPlan2} {
				noErr(t, "Create()", r.FixedCharges.Create(ctx, f))
			}
			wantErr(t, "Create() with the same ID", r.FixedCharges.Create(ctx, full), core.ErrConflict)
			wantErr(t, "Create() with a code taken on the plan",
				r.FixedCharges.Create(ctx, catalogFixtureFixedCharge(e, org, plan.ID, seats.ID, "seats")), core.ErrConflict)
			got, err := r.FixedCharges.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.FixedCharges.Get(ctx, org, bare.ID)
			noErr(t, "Get() without optional fields", err)
			equal(t, "Get() without optional fields", got, bare)
			got, err = r.FixedCharges.GetByCode(ctx, org, plan2.ID, "seats")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, onPlan2)
			_, err = r.FixedCharges.GetByCode(ctx, org, plan2.ID, "setup")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)
			_, err = r.FixedCharges.Get(ctx, org, core.NewID[core.FixedChargeID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			child := full.NewOverride(core.NewID[core.FixedChargeID](e.ids), plan.ID, at(time.Hour))
			child.Units = money.MustParse("3")
			noErr(t, "Create() override on the same plan", r.FixedCharges.Create(ctx, child))
			children, err := r.FixedCharges.ListChildren(ctx, org, full.ID)
			noErr(t, "ListChildren()", err)
			equal(t, "ListChildren()", children, []*catalog.FixedCharge{child})

			full.Units = money.MustParse("7")
			full.InvoiceDisplayName = nil
			full.TaxIDs = []core.TaxID{vat}
			full.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.FixedCharges.Update(ctx, full))
			got, err = r.FixedCharges.Get(ctx, org, full.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, full)
			bare.Code = "seats"
			wantErr(t, "Update() to a taken code", r.FixedCharges.Update(ctx, bare), core.ErrConflict)
			bare.Code = "setup"
			wantErr(t, "Update() unknown", r.FixedCharges.Update(ctx, catalogFixtureFixedCharge(e, org, plan.ID, seats.ID, "x")), core.ErrNotFound)

			byPlan, err := r.FixedCharges.ListByPlan(ctx, org, plan.ID)
			noErr(t, "ListByPlan()", err)
			equal(t, "ListByPlan()", byPlan, []*catalog.FixedCharge{full, bare, child})
			byAddOn, err := r.FixedCharges.ListByAddOn(ctx, org, seats.ID)
			noErr(t, "ListByAddOn()", err)
			equal(t, "ListByAddOn()", ids(byAddOn, catalogFixedChargeID), []core.FixedChargeID{full.ID, onPlan2.ID, child.ID})

			// Isolation.
			_, err = r.FixedCharges.Get(ctx, ob, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.FixedCharges.GetByCode(ctx, ob, plan.ID, "seats")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			for what, list := range map[string]func() ([]*catalog.FixedCharge, error){
				"ListByPlan()":   func() ([]*catalog.FixedCharge, error) { return r.FixedCharges.ListByPlan(ctx, ob, plan.ID) },
				"ListByAddOn()":  func() ([]*catalog.FixedCharge, error) { return r.FixedCharges.ListByAddOn(ctx, ob, seats.ID) },
				"ListChildren()": func() ([]*catalog.FixedCharge, error) { return r.FixedCharges.ListChildren(ctx, ob, full.ID) },
			} {
				fs, err := list()
				noErr(t, what+" from another organization", err)
				equal(t, what+" from another organization", fs, []*catalog.FixedCharge{})
			}
			other := *bare
			other.OrganizationID = ob
			wantErr(t, "Update() through another organization", r.FixedCharges.Update(ctx, &other), core.ErrNotFound)
			wantErr(t, "Discard() from another organization", r.FixedCharges.Discard(ctx, ob, bare.ID, at(time.Hour)), core.ErrNotFound)
			n, err := r.FixedCharges.DiscardByPlan(ctx, ob, plan.ID, at(time.Hour))
			noErr(t, "DiscardByPlan() from another organization", err)
			equal(t, "DiscardByPlan() from another organization", n, 0)
			n, err = r.FixedCharges.DiscardByAddOn(ctx, ob, seats.ID, at(time.Hour))
			noErr(t, "DiscardByAddOn() from another organization", err)
			equal(t, "DiscardByAddOn() from another organization", n, 0)

			// Discards keep the row for Get only; an override stays kept
			// when its parent goes.
			noErr(t, "Discard()", r.FixedCharges.Discard(ctx, org, full.ID, at(time.Hour)))
			wantErr(t, "Discard() again", r.FixedCharges.Discard(ctx, org, full.ID, at(2*time.Hour)), core.ErrNotFound)
			got, err = r.FixedCharges.Get(ctx, org, full.ID)
			noErr(t, "Get() discarded", err)
			equal(t, "Get() discarded", got.DeletedAt, ptr(at(time.Hour)))
			got, err = r.FixedCharges.GetByCode(ctx, org, plan.ID, "seats")
			noErr(t, "GetByCode() discarded", err)
			equal(t, "GetByCode() discarded finds the kept override", got.ID, child.ID)
			wantErr(t, "Update() discarded", r.FixedCharges.Update(ctx, full), core.ErrNotFound)
			children, err = r.FixedCharges.ListChildren(ctx, org, full.ID)
			noErr(t, "ListChildren() of a discarded fixed charge", err)
			equal(t, "ListChildren() of a discarded fixed charge", ids(children, func(f *catalog.FixedCharge) core.FixedChargeID { return f.ID }),
				[]core.FixedChargeID{child.ID})
			noErr(t, "Create() with a freed code", r.FixedCharges.Create(ctx, catalogFixtureFixedCharge(e, org, plan.ID, seats.ID, "seats")))
			n, err = r.FixedCharges.DiscardByPlan(ctx, org, plan.ID, at(2*time.Hour))
			noErr(t, "DiscardByPlan()", err)
			equal(t, "DiscardByPlan()", n, 3)
			n, err = r.FixedCharges.DiscardByAddOn(ctx, org, seats.ID, at(3*time.Hour))
			noErr(t, "DiscardByAddOn()", err)
			equal(t, "DiscardByAddOn()", n, 1)
			byAddOn, err = r.FixedCharges.ListByAddOn(ctx, org, seats.ID)
			noErr(t, "ListByAddOn() after discards", err)
			equal(t, "ListByAddOn() after discards", byAddOn, []*catalog.FixedCharge{})
			noErr(t, "AddOns.Discard() of an add-on with discarded fixed charges", r.AddOns.Discard(ctx, org, seats.ID, at(3*time.Hour)))
			return nil
		})
	})
}
