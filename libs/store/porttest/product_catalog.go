package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// productCatalogFixture is a category, a metered product with a filter, a
// rate card per product slice, a plan and a tax of one organization.
type productCatalogFixture struct {
	org      core.OrganizationID
	metric   *catalog.BillableMetric
	category *catalog.ProductCategory
	product  *catalog.Product
	fixed    *catalog.Product
	filter   *catalog.ProductFilter
	card     *catalog.RateCard
	filtered *catalog.RateCard
	plan     *catalog.Plan
	tax      core.TaxID
}

func newProductCatalogFixture(t *testing.T, e *env, org core.OrganizationID) *productCatalogFixture {
	t.Helper()
	f := &productCatalogFixture{org: org}
	f.metric = catalogFixtureMetric(e, org, "api_calls")
	f.metric.Filters = []catalog.MetricFilter{
		{ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "region", Values: []string{"eu", "us"}},
	}
	f.category = &catalog.ProductCategory{
		ID: core.NewID[core.ProductCategoryID](e.ids), OrganizationID: org, Code: "compute", Name: "Compute",
		Description: ptr("All compute"), CreatedAt: now, UpdatedAt: now,
	}
	f.product = &catalog.Product{
		ID: core.NewID[core.ProductID](e.ids), OrganizationID: org, ProductCategoryID: f.category.ID,
		BillableMetricID: f.metric.ID, ProductType: catalog.ProductTypeMetered, Code: "calls", Name: "API calls",
		InvoiceDisplayName: ptr("Calls"), CreatedAt: at(time.Second), UpdatedAt: at(time.Second),
	}
	f.fixed = &catalog.Product{
		ID: core.NewID[core.ProductID](e.ids), OrganizationID: org, ProductType: catalog.ProductTypeFixed,
		Code: "seat", Name: "Seat", CreatedAt: at(2 * time.Second), UpdatedAt: at(2 * time.Second),
	}
	f.filter = &catalog.ProductFilter{
		ID: core.NewID[core.ProductFilterID](e.ids), OrganizationID: org, ProductID: f.product.ID, Code: "eu",
		Name: "EU", Values: []catalog.ProductFilterValue{
			{BillableMetricFilterID: f.metric.Filters[0].ID, Key: "region", Value: ptr("eu")},
		},
		CreatedAt: now, UpdatedAt: now,
	}
	f.card = &catalog.RateCard{
		ID: core.NewID[core.RateCardID](e.ids), OrganizationID: org, ProductID: f.product.ID, Code: "calls_eur",
		Name: "Calls EUR", Currency: money.MustCurrency("EUR"), BillingTiming: catalog.BillingTimingArrears,
		DisplayOnInvoice: true, CreatedAt: now, UpdatedAt: now,
	}
	f.filtered = &catalog.RateCard{
		ID: core.NewID[core.RateCardID](e.ids), OrganizationID: org, ProductID: f.product.ID, ProductFilterID: f.filter.ID,
		Code: "calls_eu_eur", Name: "Calls EU", Currency: money.MustCurrency("EUR"), BillingTiming: catalog.BillingTimingAdvance,
		RegroupPaidFees: catalog.RegroupPaidFeesInvoice, CreatedAt: at(time.Second), UpdatedAt: at(time.Second),
	}
	f.plan = catalogFixturePlan(e, org, "starter")
	e.tx(func(ctx context.Context, r port.Repos) error {
		f.tax = catalogFixtureTax(ctx, t, r, e, org, "vat")
		f.card.TaxIDs = []core.TaxID{f.tax}
		noErr(t, "Metrics.Create", r.Metrics.Create(ctx, f.metric))
		noErr(t, "Plans.Create", r.Plans.Create(ctx, f.plan))
		noErr(t, "ProductCategories.Create", r.ProductCategories.Create(ctx, f.category))
		noErr(t, "Products.Create", r.Products.Create(ctx, f.product))
		noErr(t, "Products.Create(fixed)", r.Products.Create(ctx, f.fixed))
		noErr(t, "ProductFilters.Create", r.ProductFilters.Create(ctx, f.filter))
		noErr(t, "RateCards.Create", r.RateCards.Create(ctx, f.card))
		return r.RateCards.Create(ctx, f.filtered)
	})
	return f
}

func productCategoryID(c *catalog.ProductCategory) core.ProductCategoryID { return c.ID }
func productID(p *catalog.Product) core.ProductID                         { return p.ID }
func rateCardID(c *catalog.RateCard) core.RateCardID                      { return c.ID }
func rateID(r *catalog.Rate) core.RateCardRateID                          { return r.ID }
func planRateCardID(p *catalog.PlanRateCard) core.PlanRateCardID          { return p.ID }

func testProductCatalog(t *testing.T, h Harness) {
	t.Run("products", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		f := newProductCatalogFixture(t, e, a.OrganizationID)
		org := f.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.ProductCategories.GetByCode(ctx, org, "compute")
			noErr(t, "ProductCategories.GetByCode", err)
			equal(t, "ProductCategories.GetByCode", got, f.category)
			p, err := r.Products.Get(ctx, org, f.product.ID)
			noErr(t, "Products.Get", err)
			equal(t, "Products.Get", p, f.product)
			p, err = r.Products.GetByCode(ctx, org, "seat")
			noErr(t, "Products.GetByCode", err)
			equal(t, "Products.GetByCode", p, f.fixed)
			_, err = r.Products.Get(ctx, b.OrganizationID, f.product.ID)
			wantErr(t, "Products.Get(other org)", err, core.ErrNotFound)

			dup := *f.fixed
			dup.ID = core.NewID[core.ProductID](e.ids)
			wantErr(t, "Products.Create(same code)", r.Products.Create(ctx, &dup), core.ErrConflict)
			cat := *f.category
			cat.ID = core.NewID[core.ProductCategoryID](e.ids)
			wantErr(t, "ProductCategories.Create(same code)", r.ProductCategories.Create(ctx, &cat), core.ErrConflict)

			all := listAll(t, 1, func(p core.PageRequest) (core.Page[*catalog.Product], error) {
				return r.Products.List(ctx, org, catalog.ProductQuery{}, p)
			})
			equal(t, "Products.List", ids(all, productID), []core.ProductID{f.fixed.ID, f.product.ID})
			got2, err := r.Products.List(ctx, org, catalog.ProductQuery{WithoutProductCategory: true}, core.PageRequest{})
			noErr(t, "Products.List(uncategorized)", err)
			equal(t, "Products.List(uncategorized)", ids(got2.Items, productID), []core.ProductID{f.fixed.ID})
			got2, err = r.Products.List(ctx, org, catalog.ProductQuery{ProductCategoryIDs: []core.ProductCategoryID{f.category.ID}, WithoutProductCategory: true}, core.PageRequest{})
			noErr(t, "Products.List(category or none)", err)
			equal(t, "Products.List(category or none)", len(got2.Items), 2)
			got2, err = r.Products.List(ctx, org, catalog.ProductQuery{ProductType: catalog.ProductTypeMetered, Search: "API"}, core.PageRequest{})
			noErr(t, "Products.List(type, search)", err)
			equal(t, "Products.List(type, search)", ids(got2.Items, productID), []core.ProductID{f.product.ID})
			n, err := r.Products.Count(ctx, org, catalog.ProductQuery{ProductCategoryIDs: []core.ProductCategoryID{f.category.ID}})
			noErr(t, "Products.Count", err)
			equal(t, "Products.Count", n, 1)
			cats, err := r.ProductCategories.List(ctx, org, catalog.ProductCategoryQuery{Search: "comp"}, core.PageRequest{})
			noErr(t, "ProductCategories.List", err)
			equal(t, "ProductCategories.List", ids(cats.Items, productCategoryID), []core.ProductCategoryID{f.category.ID})

			upd := *f.product
			upd.ProductCategoryID = core.ProductCategoryID{}
			upd.Name, upd.UpdatedAt = "Calls", at(time.Minute)
			noErr(t, "Products.Update", r.Products.Update(ctx, &upd))
			p, err = r.Products.Get(ctx, org, f.product.ID)
			noErr(t, "Products.Get(updated)", err)
			equal(t, "Products.Get(updated)", p, &upd)
			return nil
		})
	})

	t.Run("filters", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, _ := e.twoOrgs()
		f := newProductCatalogFixture(t, e, a.OrganizationID)
		org := f.org
		all := &catalog.ProductFilter{
			ID: core.NewID[core.ProductFilterID](e.ids), OrganizationID: org, ProductID: f.product.ID, Code: "all",
			Name: "All regions", Values: []catalog.ProductFilterValue{{BillableMetricFilterID: f.metric.Filters[0].ID, Key: "region"}},
			CreatedAt: at(time.Second), UpdatedAt: at(time.Second),
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "ProductFilters.Create", r.ProductFilters.Create(ctx, all))
			dup := *all
			dup.ID = core.NewID[core.ProductFilterID](e.ids)
			wantErr(t, "ProductFilters.Create(same code)", r.ProductFilters.Create(ctx, &dup), core.ErrConflict)
			got, err := r.ProductFilters.GetByCode(ctx, org, f.product.ID, "eu")
			noErr(t, "ProductFilters.GetByCode", err)
			equal(t, "ProductFilters.GetByCode", got, f.filter)
			list := listAll(t, 1, func(p core.PageRequest) (core.Page[*catalog.ProductFilter], error) {
				return r.ProductFilters.List(ctx, org, f.product.ID, "", p)
			})
			equal(t, "ProductFilters.List", list, []*catalog.ProductFilter{f.filter, all})
			found, err := r.ProductFilters.List(ctx, org, f.product.ID, "regions", core.PageRequest{})
			noErr(t, "ProductFilters.List(search)", err)
			equal(t, "ProductFilters.List(search)", found.Items, []*catalog.ProductFilter{all})
			every, err := r.ProductFilters.ListAll(ctx, org, f.product.ID)
			noErr(t, "ProductFilters.ListAll", err)
			equal(t, "ProductFilters.ListAll", every, []*catalog.ProductFilter{f.filter, all})
			n, err := r.ProductFilters.Count(ctx, org, f.product.ID)
			noErr(t, "ProductFilters.Count", err)
			equal(t, "ProductFilters.Count", n, 2)
			referenced, err := r.ProductFilters.ReferencesMetric(ctx, org, f.metric.ID)
			noErr(t, "ProductFilters.ReferencesMetric", err)
			equal(t, "ProductFilters.ReferencesMetric", referenced, true)
			values, err := r.ProductFilters.MetricReferences(ctx, org, f.metric.ID)
			noErr(t, "ProductFilters.MetricReferences", err)
			equal(t, "ProductFilters.MetricReferences", values, []catalog.ProductFilterValue{
				{BillableMetricFilterID: f.metric.Filters[0].ID, Value: ptr("eu")},
				{BillableMetricFilterID: f.metric.Filters[0].ID},
			})

			all.Values = []catalog.ProductFilterValue{
				{BillableMetricFilterID: f.metric.Filters[0].ID, Key: "region", Value: ptr("us")},
				{BillableMetricFilterID: f.metric.Filters[0].ID, Key: "region", Value: ptr("eu")},
			}
			all.UpdatedAt = at(time.Minute)
			noErr(t, "ProductFilters.Update", r.ProductFilters.Update(ctx, all))
			got, err = r.ProductFilters.Get(ctx, org, all.ID)
			noErr(t, "ProductFilters.Get(updated)", err)
			equal(t, "ProductFilters.Get(updated)", got, all)

			gone := at(2 * time.Minute)
			noErr(t, "ProductFilters.Discard", r.ProductFilters.Discard(ctx, org, f.filter.ID, gone))
			wantErr(t, "ProductFilters.Discard(again)", r.ProductFilters.Discard(ctx, org, f.filter.ID, gone), core.ErrNotFound)
			got, err = r.ProductFilters.Get(ctx, org, f.filter.ID)
			noErr(t, "ProductFilters.Get(discarded)", err)
			equal(t, "ProductFilters.Get(discarded).DeletedAt", got.DeletedAt, &gone)
			equal(t, "ProductFilters.Get(discarded).Values", len(got.Values), 0)
			_, err = r.ProductFilters.GetByCode(ctx, org, f.product.ID, "eu")
			wantErr(t, "ProductFilters.GetByCode(discarded)", err, core.ErrNotFound)
			every, err = r.ProductFilters.ListAll(ctx, org, f.product.ID)
			noErr(t, "ProductFilters.ListAll(discarded)", err)
			equal(t, "ProductFilters.ListAll(discarded)", every, []*catalog.ProductFilter{all})
			card, err := r.RateCards.Get(ctx, org, f.filtered.ID)
			noErr(t, "RateCards.Get(filter discarded)", err)
			equal(t, "RateCards.Get(filter discarded).DeletedAt", card.DeletedAt, &gone)
			card, err = r.RateCards.Get(ctx, org, f.card.ID)
			noErr(t, "RateCards.Get(whole product)", err)
			equal(t, "RateCards.Get(whole product).DeletedAt", card.DeletedAt, (*time.Time)(nil))
			_, err = r.RateCards.GetByCode(ctx, org, "calls_eu_eur")
			wantErr(t, "RateCards.GetByCode(filter discarded)", err, core.ErrNotFound)

			again := *f.filter
			again.ID, again.DeletedAt = core.NewID[core.ProductFilterID](e.ids), nil
			again.Values = []catalog.ProductFilterValue{{BillableMetricFilterID: f.metric.Filters[0].ID, Key: "region", Value: ptr("eu")}}
			noErr(t, "ProductFilters.Create(code of discarded)", r.ProductFilters.Create(ctx, &again))
			return nil
		})
	})

	t.Run("rate_cards", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, _ := e.twoOrgs()
		f := newProductCatalogFixture(t, e, a.OrganizationID)
		org := f.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.RateCards.GetByCode(ctx, org, "calls_eur")
			noErr(t, "RateCards.GetByCode", err)
			equal(t, "RateCards.GetByCode", got, f.card)
			got, err = r.RateCards.Get(ctx, org, f.filtered.ID)
			noErr(t, "RateCards.Get", err)
			equal(t, "RateCards.Get", got, f.filtered)
			dup := *f.card
			dup.ID = core.NewID[core.RateCardID](e.ids)
			wantErr(t, "RateCards.Create(same code)", r.RateCards.Create(ctx, &dup), core.ErrConflict)

			list := listAll(t, 1, func(p core.PageRequest) (core.Page[*catalog.RateCard], error) {
				return r.RateCards.List(ctx, org, catalog.RateCardQuery{}, p)
			})
			equal(t, "RateCards.List", ids(list, rateCardID), []core.RateCardID{f.filtered.ID, f.card.ID})
			page, err := r.RateCards.List(ctx, org, catalog.RateCardQuery{ProductFilterIDs: []core.ProductFilterID{f.filter.ID}}, core.PageRequest{})
			noErr(t, "RateCards.List(filter)", err)
			equal(t, "RateCards.List(filter)", ids(page.Items, rateCardID), []core.RateCardID{f.filtered.ID})
			page, err = r.RateCards.List(ctx, org, catalog.RateCardQuery{
				ProductIDs: []core.ProductID{f.product.ID}, ProductCategoryIDs: []core.ProductCategoryID{f.category.ID}, Search: "eu",
			}, core.PageRequest{})
			noErr(t, "RateCards.List(product, category, search)", err)
			equal(t, "RateCards.List(product, category, search)", len(page.Items), 2)
			page, err = r.RateCards.List(ctx, org, catalog.RateCardQuery{ProductIDs: []core.ProductID{f.fixed.ID}}, core.PageRequest{})
			noErr(t, "RateCards.List(fixed)", err)
			equal(t, "RateCards.List(fixed)", len(page.Items), 0)

			f.card.TaxIDs, f.card.Proration, f.card.UpdatedAt = nil, true, at(time.Minute)
			noErr(t, "RateCards.Update", r.RateCards.Update(ctx, f.card))
			got, err = r.RateCards.Get(ctx, org, f.card.ID)
			noErr(t, "RateCards.Get(updated)", err)
			equal(t, "RateCards.Get(updated).TaxIDs", len(got.TaxIDs), 0)
			equal(t, "RateCards.Get(updated).Proration", got.Proration, true)

			f.filtered.TaxIDs = []core.TaxID{f.tax}
			noErr(t, "RateCards.Update(taxes)", r.RateCards.Update(ctx, f.filtered))
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Taxes.Discard", r.Taxes.Discard(ctx, org, f.tax, at(time.Hour)))
			got, err := r.RateCards.Get(ctx, org, f.filtered.ID)
			noErr(t, "RateCards.Get(tax discarded)", err)
			equal(t, "RateCards.Get(tax discarded).TaxIDs", len(got.TaxIDs), 0)
			return nil
		})
	})

	t.Run("rates", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, _ := e.twoOrgs()
		f := newProductCatalogFixture(t, e, a.OrganizationID)
		org := f.org
		rate := func(code string, from time.Time) *catalog.Rate {
			return &catalog.Rate{
				ID: core.NewID[core.RateCardRateID](e.ids), OrganizationID: org, RateCardID: f.card.ID, Code: code,
				EffectiveFrom: from, RateModel: catalog.ChargeModelStandard, Properties: catalog.MustProperties(`{"amount":"1"}`),
				BillingIntervalCount: 1, BillingIntervalUnit: catalog.IntervalUnitMonth, CreatedAt: now, UpdatedAt: now,
			}
		}
		v1, v2 := rate("v1", at(0)), rate("v2", at(24*time.Hour))
		v2.MinAmount, v2.PricingUnitConversionRate = 100, ptr(money.MustParse("0.5"))
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Rates.Create(v1)", r.Rates.Create(ctx, v1))
			noErr(t, "Rates.Create(v2)", r.Rates.Create(ctx, v2))
			wantErr(t, "Rates.Create(same code)", r.Rates.Create(ctx, rate("v1", at(48*time.Hour))), core.ErrConflict)
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			wantErr(t, "Rates.Create(same instant)", r.Rates.Create(ctx, rate("v3", at(0))), core.ErrConflict)
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Rates.Get(ctx, org, v2.ID)
			noErr(t, "Rates.Get", err)
			equal(t, "Rates.Get", got, v2)
			list, err := r.Rates.ListByRateCard(ctx, org, f.card.ID)
			noErr(t, "Rates.ListByRateCard", err)
			equal(t, "Rates.ListByRateCard", ids(list, rateID), []core.RateCardRateID{v2.ID, v1.ID})
			v2.Properties, v2.UpdatedAt = catalog.MustProperties(`{"amount":"2"}`), at(time.Minute)
			noErr(t, "Rates.Update", r.Rates.Update(ctx, v2))
			got, err = r.Rates.Get(ctx, org, v2.ID)
			noErr(t, "Rates.Get(updated)", err)
			equal(t, "Rates.Get(updated)", got, v2)
			gone := at(2 * time.Minute)
			noErr(t, "Rates.Discard", r.Rates.Discard(ctx, org, v2.ID, gone))
			wantErr(t, "Rates.Discard(again)", r.Rates.Discard(ctx, org, v2.ID, gone), core.ErrNotFound)
			got, err = r.Rates.Get(ctx, org, v2.ID)
			noErr(t, "Rates.Get(discarded)", err)
			equal(t, "Rates.Get(discarded).DeletedAt", got.DeletedAt, &gone)
			list, err = r.Rates.ListByRateCard(ctx, org, f.card.ID)
			noErr(t, "Rates.ListByRateCard(discarded)", err)
			equal(t, "Rates.ListByRateCard(discarded)", ids(list, rateID), []core.RateCardRateID{v1.ID})
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Rates.Create(code and instant of discarded)", r.Rates.Create(ctx, rate("v2", at(24*time.Hour))))
			return nil
		})
	})

	t.Run("plan_rate_cards", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		f := newProductCatalogFixture(t, e, a.OrganizationID)
		org := f.org
		other := catalogFixturePlan(e, org, "pro")
		phase := func(code string, pos int, cycles *int) *catalog.RatePhase {
			return &catalog.RatePhase{
				ID: core.NewID[core.RatePhaseID](e.ids), OrganizationID: org, Code: code, Position: pos,
				BillingIntervalCycleCount: cycles, CreatedAt: now, UpdatedAt: now,
			}
		}
		intro := phase("intro", 1, ptr(3))
		intro.Name = ptr("Intro")
		intro.Override = &catalog.RateOverride{
			RateModel: catalog.ChargeModelStandard, Properties: catalog.MustProperties(`{"amount":"0.5"}`),
			MinAmount: 10, BillingIntervalCount: ptr(2), BillingIntervalUnit: catalog.IntervalUnitWeek,
		}
		steady := phase("default", 2, nil)
		p1 := &catalog.PlanRateCard{
			ID: core.NewID[core.PlanRateCardID](e.ids), OrganizationID: org, PlanID: f.plan.ID, RateCardID: f.card.ID,
			CreatedAt: now, UpdatedAt: now,
		}
		p2 := &catalog.PlanRateCard{
			ID: core.NewID[core.PlanRateCardID](e.ids), OrganizationID: org, PlanID: other.ID, RateCardID: f.filtered.ID,
			Units: ptr(money.MustParse("2")), CreatedAt: at(time.Second), UpdatedAt: at(time.Second),
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Plans.Create", r.Plans.Create(ctx, other))
			noErr(t, "PlanRateCards.Create(p1)", r.PlanRateCards.Create(ctx, p1, []*catalog.RatePhase{intro, steady}))
			return r.PlanRateCards.Create(ctx, p2, []*catalog.RatePhase{phase("default", 1, nil)})
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			dup := *p1
			dup.ID = core.NewID[core.PlanRateCardID](e.ids)
			wantErr(t, "PlanRateCards.Create(same card)", r.PlanRateCards.Create(ctx, &dup, nil), core.ErrConflict)
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.PlanRateCards.GetByRateCard(ctx, org, f.plan.ID, f.card.ID)
			noErr(t, "PlanRateCards.GetByRateCard", err)
			equal(t, "PlanRateCards.GetByRateCard", got, p1)
			_, err = r.PlanRateCards.Get(ctx, b.OrganizationID, p1.ID)
			wantErr(t, "PlanRateCards.Get(other org)", err, core.ErrNotFound)
			phases, err := r.PlanRateCards.ListPhases(ctx, org, p1.ID)
			noErr(t, "PlanRateCards.ListPhases", err)
			intro.PlanRateCardID, steady.PlanRateCardID = p1.ID, p1.ID
			equal(t, "PlanRateCards.ListPhases", phases, []*catalog.RatePhase{intro, steady})

			list := listAll(t, 1, func(p core.PageRequest) (core.Page[*catalog.PlanRateCard], error) {
				return r.PlanRateCards.List(ctx, org, catalog.PlanRateCardQuery{}, p)
			})
			equal(t, "PlanRateCards.List", ids(list, planRateCardID), []core.PlanRateCardID{p1.ID, p2.ID})
			page, err := r.PlanRateCards.List(ctx, org, catalog.PlanRateCardQuery{PlanID: other.ID}, core.PageRequest{})
			noErr(t, "PlanRateCards.List(plan)", err)
			equal(t, "PlanRateCards.List(plan)", page.Items, []*catalog.PlanRateCard{p2})
			plans, err := r.PlanRateCards.PlanIDs(ctx, org, catalog.PlanRateCardQuery{ProductFilterID: f.filter.ID})
			noErr(t, "PlanRateCards.PlanIDs(filter)", err)
			equal(t, "PlanRateCards.PlanIDs(filter)", plans, []core.PlanID{other.ID})
			plans, err = r.PlanRateCards.PlanIDs(ctx, org, catalog.PlanRateCardQuery{ProductCategoryID: f.category.ID})
			noErr(t, "PlanRateCards.PlanIDs(category)", err)
			equal(t, "PlanRateCards.PlanIDs(category)", len(plans), 2)
			plans, err = r.PlanRateCards.PlanIDs(ctx, org, catalog.PlanRateCardQuery{ProductID: f.fixed.ID})
			noErr(t, "PlanRateCards.PlanIDs(fixed)", err)
			equal(t, "PlanRateCards.PlanIDs(fixed)", len(plans), 0)

			p1.Units, p1.UpdatedAt = ptr(money.MustParse("1.5")), at(time.Minute)
			noErr(t, "PlanRateCards.Update", r.PlanRateCards.Update(ctx, p1))
			got, err = r.PlanRateCards.Get(ctx, org, p1.ID)
			noErr(t, "PlanRateCards.Get(updated)", err)
			equal(t, "PlanRateCards.Get(updated)", got, p1)

			steady.Position, intro.Position = 1, 2
			intro.BillingIntervalCycleCount, steady.BillingIntervalCycleCount = nil, ptr(1)
			intro.UpdatedAt, steady.UpdatedAt = at(time.Minute), at(time.Minute)
			noErr(t, "PlanRateCards.SavePhases(swap)", r.PlanRateCards.SavePhases(ctx, org, p1.ID, []*catalog.RatePhase{steady, intro}, at(time.Minute)))
			phases, err = r.PlanRateCards.ListPhases(ctx, org, p1.ID)
			noErr(t, "PlanRateCards.ListPhases(swapped)", err)
			equal(t, "PlanRateCards.ListPhases(swapped)", phases, []*catalog.RatePhase{steady, intro})

			tail := phase("intro", 1, nil)
			tail.PlanRateCardID = p1.ID
			noErr(t, "PlanRateCards.SavePhases(replace)", r.PlanRateCards.SavePhases(ctx, org, p1.ID, []*catalog.RatePhase{tail}, at(2*time.Minute)))
			phases, err = r.PlanRateCards.ListPhases(ctx, org, p1.ID)
			noErr(t, "PlanRateCards.ListPhases(replaced)", err)
			equal(t, "PlanRateCards.ListPhases(replaced)", phases, []*catalog.RatePhase{tail})
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			wantErr(t, "PlanRateCards.SavePhases(same code)", r.PlanRateCards.SavePhases(ctx, org, p1.ID, []*catalog.RatePhase{
				phase("x", 1, ptr(1)), phase("x", 2, nil),
			}, at(3*time.Minute)), core.ErrConflict)
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			gone := at(3 * time.Minute)
			noErr(t, "PlanRateCards.Discard", r.PlanRateCards.Discard(ctx, org, p2.ID, gone))
			wantErr(t, "PlanRateCards.Discard(again)", r.PlanRateCards.Discard(ctx, org, p2.ID, gone), core.ErrNotFound)
			got, err := r.PlanRateCards.Get(ctx, org, p2.ID)
			noErr(t, "PlanRateCards.Get(discarded)", err)
			equal(t, "PlanRateCards.Get(discarded).DeletedAt", got.DeletedAt, &gone)
			phases, err := r.PlanRateCards.ListPhases(ctx, org, p2.ID)
			noErr(t, "PlanRateCards.ListPhases(discarded)", err)
			equal(t, "PlanRateCards.ListPhases(discarded)", len(phases), 0)
			wantErr(t, "PlanRateCards.SavePhases(discarded)", r.PlanRateCards.SavePhases(ctx, org, p2.ID, nil, gone), core.ErrNotFound)
			again := *p2
			again.ID, again.DeletedAt = core.NewID[core.PlanRateCardID](e.ids), nil
			noErr(t, "PlanRateCards.Create(card of discarded)", r.PlanRateCards.Create(ctx, &again, []*catalog.RatePhase{phase("default", 1, nil)}))

			n, err := r.PlanRateCards.DiscardByPlan(ctx, org, f.plan.ID, gone)
			noErr(t, "PlanRateCards.DiscardByPlan", err)
			equal(t, "PlanRateCards.DiscardByPlan", n, 1)
			phases, err = r.PlanRateCards.ListPhases(ctx, org, p1.ID)
			noErr(t, "PlanRateCards.ListPhases(plan discarded)", err)
			equal(t, "PlanRateCards.ListPhases(plan discarded)", len(phases), 0)
			list := listAll(t, 1, func(p core.PageRequest) (core.Page[*catalog.PlanRateCard], error) {
				return r.PlanRateCards.List(ctx, org, catalog.PlanRateCardQuery{}, p)
			})
			equal(t, "PlanRateCards.List(discarded)", ids(list, planRateCardID), []core.PlanRateCardID{again.ID})
			return nil
		})
	})

	t.Run("cascade", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, _ := e.twoOrgs()
		f := newProductCatalogFixture(t, e, a.OrganizationID)
		org := f.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Rates.Create", r.Rates.Create(ctx, &catalog.Rate{
				ID: core.NewID[core.RateCardRateID](e.ids), OrganizationID: org, RateCardID: f.card.ID, Code: "v1",
				EffectiveFrom: now, RateModel: catalog.ChargeModelStandard, Properties: catalog.MustProperties(`{"amount":"1"}`),
				BillingIntervalCount: 1, BillingIntervalUnit: catalog.IntervalUnitMonth, CreatedAt: now, UpdatedAt: now,
			}))
			return r.PlanRateCards.Create(ctx, &catalog.PlanRateCard{
				ID: core.NewID[core.PlanRateCardID](e.ids), OrganizationID: org, PlanID: f.plan.ID, RateCardID: f.card.ID,
				CreatedAt: now, UpdatedAt: now,
			}, nil)
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			gone := at(time.Hour)
			noErr(t, "ProductCategories.Discard", r.ProductCategories.Discard(ctx, org, f.category.ID, gone))
			c, err := r.ProductCategories.Get(ctx, org, f.category.ID)
			noErr(t, "ProductCategories.Get(discarded)", err)
			equal(t, "ProductCategories.Get(discarded).DeletedAt", c.DeletedAt, &gone)
			p, err := r.Products.Get(ctx, org, f.product.ID)
			noErr(t, "Products.Get(category discarded)", err)
			equal(t, "Products.Get(category discarded).DeletedAt", p.DeletedAt, &gone)
			fl, err := r.ProductFilters.Get(ctx, org, f.filter.ID)
			noErr(t, "ProductFilters.Get(category discarded)", err)
			equal(t, "ProductFilters.Get(category discarded).DeletedAt", fl.DeletedAt, &gone)
			card, err := r.RateCards.Get(ctx, org, f.card.ID)
			noErr(t, "RateCards.Get(category discarded)", err)
			equal(t, "RateCards.Get(category discarded).DeletedAt", card.DeletedAt, &gone)
			rates, err := r.Rates.ListByRateCard(ctx, org, f.card.ID)
			noErr(t, "Rates.ListByRateCard(category discarded)", err)
			equal(t, "Rates.ListByRateCard(category discarded)", len(rates), 0)
			plans, err := r.PlanRateCards.PlanIDs(ctx, org, catalog.PlanRateCardQuery{PlanID: f.plan.ID})
			noErr(t, "PlanRateCards.PlanIDs(category discarded)", err)
			equal(t, "PlanRateCards.PlanIDs(category discarded)", len(plans), 0)
			all := listAll(t, 1, func(p core.PageRequest) (core.Page[*catalog.Product], error) {
				return r.Products.List(ctx, org, catalog.ProductQuery{}, p)
			})
			equal(t, "Products.List(category discarded)", ids(all, productID), []core.ProductID{f.fixed.ID})
			referenced, err := r.ProductFilters.ReferencesMetric(ctx, org, f.metric.ID)
			noErr(t, "ProductFilters.ReferencesMetric(discarded)", err)
			equal(t, "ProductFilters.ReferencesMetric(discarded)", referenced, false)

			noErr(t, "Products.Discard", r.Products.Discard(ctx, org, f.fixed.ID, gone))
			wantErr(t, "Products.Discard(again)", r.Products.Discard(ctx, org, f.fixed.ID, gone), core.ErrNotFound)
			n, err := r.Products.Count(ctx, org, catalog.ProductQuery{})
			noErr(t, "Products.Count(discarded)", err)
			equal(t, "Products.Count(discarded)", n, 0)

			cat := *f.category
			cat.ID, cat.DeletedAt = core.NewID[core.ProductCategoryID](e.ids), nil
			noErr(t, "ProductCategories.Create(code of discarded)", r.ProductCategories.Create(ctx, &cat))
			prod := *f.fixed
			prod.ID, prod.DeletedAt = core.NewID[core.ProductID](e.ids), nil
			noErr(t, "Products.Create(code of discarded)", r.Products.Create(ctx, &prod))
			return nil
		})
	})
}
