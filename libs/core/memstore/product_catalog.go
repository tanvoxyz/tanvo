package memstore

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
)

// productCatalogData holds the product catalog: categories, products,
// product filters (with their values), rate cards (with their taxes),
// rates, plan rate cards and rate phases (with their overrides). Rows are
// discarded, never deleted: Get returns a discarded row, every other read
// skips it. A filter's values are replaced in place; the discarded ones are
// not kept, as nothing reads them, and a discarded filter has none.
type productCatalogData struct {
	categories    *table[core.ProductCategoryID, catalog.ProductCategory]
	products      *table[core.ProductID, catalog.Product]
	filters       *table[core.ProductFilterID, catalog.ProductFilter]
	rateCards     *table[core.RateCardID, catalog.RateCard]
	rates         *table[core.RateCardRateID, catalog.Rate]
	planRateCards *table[core.PlanRateCardID, catalog.PlanRateCard]
	phases        *table[core.RatePhaseID, catalog.RatePhase]
}

func newProductCatalogData() *productCatalogData {
	return &productCatalogData{
		categories: newTable[core.ProductCategoryID](func(c catalog.ProductCategory) catalog.ProductCategory {
			c.Description = clonePtr(c.Description)
			c.InvoiceDisplayName = clonePtr(c.InvoiceDisplayName)
			c.DeletedAt = clonePtr(c.DeletedAt)
			return c
		}),
		products: newTable[core.ProductID](func(p catalog.Product) catalog.Product {
			p.Description = clonePtr(p.Description)
			p.InvoiceDisplayName = clonePtr(p.InvoiceDisplayName)
			p.DeletedAt = clonePtr(p.DeletedAt)
			return p
		}),
		filters: newTable[core.ProductFilterID](func(f catalog.ProductFilter) catalog.ProductFilter {
			f.Description = clonePtr(f.Description)
			f.InvoiceDisplayName = clonePtr(f.InvoiceDisplayName)
			f.DeletedAt = clonePtr(f.DeletedAt)
			f.Values = slices.Clone(f.Values)
			for i := range f.Values {
				f.Values[i].Value = clonePtr(f.Values[i].Value)
				f.Values[i].Key = ""
			}
			return f
		}),
		rateCards: newTable[core.RateCardID](func(c catalog.RateCard) catalog.RateCard {
			c.Description = clonePtr(c.Description)
			c.TaxIDs = slices.Clone(c.TaxIDs)
			c.DeletedAt = clonePtr(c.DeletedAt)
			return c
		}),
		rates: newTable[core.RateCardRateID](func(r catalog.Rate) catalog.Rate {
			r.PricingUnitConversionRate = clonePtr(r.PricingUnitConversionRate)
			r.DeletedAt = clonePtr(r.DeletedAt)
			return r
		}),
		planRateCards: newTable[core.PlanRateCardID](func(p catalog.PlanRateCard) catalog.PlanRateCard {
			p.Units = clonePtr(p.Units)
			p.DeletedAt = clonePtr(p.DeletedAt)
			return p
		}),
		phases: newTable[core.RatePhaseID](func(p catalog.RatePhase) catalog.RatePhase {
			p.Name = clonePtr(p.Name)
			p.BillingIntervalCycleCount = clonePtr(p.BillingIntervalCycleCount)
			p.DeletedAt = clonePtr(p.DeletedAt)
			if p.Override != nil {
				o := *p.Override
				o.BillingIntervalCount = clonePtr(o.BillingIntervalCount)
				o.PricingUnitConversionRate = clonePtr(o.PricingUnitConversionRate)
				p.Override = &o
			}
			return p
		}),
	}
}

func (d *productCatalogData) snapshot() *productCatalogData {
	return &productCatalogData{
		categories:    d.categories.snapshot(),
		products:      d.products.snapshot(),
		filters:       d.filters.snapshot(),
		rateCards:     d.rateCards.snapshot(),
		rates:         d.rates.snapshot(),
		planRateCards: d.planRateCards.snapshot(),
		phases:        d.phases.snapshot(),
	}
}

func recentKey[V any](created func(*V) time.Time, id func(*V) uuid.UUID) func(*V) (time.Time, uuid.UUID) {
	return func(v *V) (time.Time, uuid.UUID) { return created(v), id(v) }
}

// kept reports whether a row discarded at deletedAt is kept.
func kept(deletedAt *time.Time) bool { return deletedAt == nil }

// discardAt returns a copy of at for a DeletedAt field.
func discardAt(at time.Time) *time.Time { return &at }

// discardProduct discards the product id with its filters (and their
// values) and its rate cards (and their rates).
func (d *productCatalogData) discardProduct(id core.ProductID, at time.Time) {
	for _, f := range d.filters.find(func(f *catalog.ProductFilter) bool { return f.ProductID == id && kept(f.DeletedAt) }) {
		f.DeletedAt, f.Values = discardAt(at), nil
		d.filters.put(f.ID, *f)
	}
	for _, c := range d.rateCards.find(func(c *catalog.RateCard) bool { return c.ProductID == id && kept(c.DeletedAt) }) {
		d.discardRateCard(c, at)
	}
	p, _ := d.products.get(id)
	p.DeletedAt = discardAt(at)
	d.products.put(id, *p)
}

// discardRateCard discards the card c with its rates.
func (d *productCatalogData) discardRateCard(c *catalog.RateCard, at time.Time) {
	for _, x := range d.rates.find(func(x *catalog.Rate) bool { return x.RateCardID == c.ID && kept(x.DeletedAt) }) {
		x.DeletedAt = discardAt(at)
		d.rates.put(x.ID, *x)
	}
	c.DeletedAt = discardAt(at)
	d.rateCards.put(c.ID, *c)
}

// discardPhases discards the kept phases of the plan rate card id that
// keep does not hold.
func (d *productCatalogData) discardPhases(id core.PlanRateCardID, keep func(core.RatePhaseID) bool, at time.Time) {
	for _, ph := range d.phases.find(func(ph *catalog.RatePhase) bool { return ph.PlanRateCardID == id && kept(ph.DeletedAt) }) {
		if keep(ph.ID) {
			continue
		}
		ph.DeletedAt = discardAt(at)
		d.phases.put(ph.ID, *ph)
	}
}

func keepNone(core.RatePhaseID) bool { return false }

// productCategoryRepo implements catalog.ProductCategoryRepository.
type productCategoryRepo struct{ d *productCatalogData }

func (r productCategoryRepo) check(c *catalog.ProductCategory) error {
	if r.d.categories.any(func(x *catalog.ProductCategory) bool {
		return x.ID != c.ID && x.OrganizationID == c.OrganizationID && x.Code == c.Code && kept(x.DeletedAt)
	}) {
		return conflict("product_category", "code")
	}
	return nil
}

func (r productCategoryRepo) Create(_ context.Context, c *catalog.ProductCategory) error {
	if r.d.categories.has(c.ID) {
		return conflict("product_category", "id")
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.categories.put(c.ID, *c)
	return nil
}

func (r productCategoryRepo) Update(_ context.Context, c *catalog.ProductCategory) error {
	cur, ok := r.d.categories.get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID || !kept(cur.DeletedAt) {
		return notFound("product_category", c.ID)
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.categories.put(c.ID, *c)
	return nil
}

func (r productCategoryRepo) Discard(_ context.Context, org core.OrganizationID, id core.ProductCategoryID, at time.Time) error {
	cur, ok := r.d.categories.get(id)
	if !ok || cur.OrganizationID != org || !kept(cur.DeletedAt) {
		return notFound("product_category", id)
	}
	for _, p := range r.d.products.find(func(p *catalog.Product) bool { return p.ProductCategoryID == id && kept(p.DeletedAt) }) {
		r.d.discardProduct(p.ID, at)
	}
	cur.DeletedAt = discardAt(at)
	r.d.categories.put(id, *cur)
	return nil
}

func (r productCategoryRepo) Get(_ context.Context, org core.OrganizationID, id core.ProductCategoryID) (*catalog.ProductCategory, error) {
	c, ok := r.d.categories.get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("product_category", id)
	}
	return c, nil
}

func (r productCategoryRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*catalog.ProductCategory, error) {
	c, ok := r.d.categories.first(func(x *catalog.ProductCategory) bool {
		return x.OrganizationID == org && x.Code == code && kept(x.DeletedAt)
	})
	if !ok {
		return nil, notFound("product_category", stringer(code))
	}
	return c, nil
}

func (r productCategoryRepo) List(_ context.Context, org core.OrganizationID, q catalog.ProductCategoryQuery, page core.PageRequest) (core.Page[*catalog.ProductCategory], error) {
	rows := r.d.categories.find(func(c *catalog.ProductCategory) bool {
		return c.OrganizationID == org && kept(c.DeletedAt) && matchesNameOrCode(q.Search, c.Name, c.Code)
	})
	return paginateRecent(rows, recentKey(
		func(c *catalog.ProductCategory) time.Time { return c.CreatedAt },
		func(c *catalog.ProductCategory) uuid.UUID { return c.ID.UUID() }), page)
}

// productRepo implements catalog.ProductRepository.
type productRepo struct{ d *productCatalogData }

func (r productRepo) check(p *catalog.Product) error {
	if r.d.products.any(func(x *catalog.Product) bool {
		return x.ID != p.ID && x.OrganizationID == p.OrganizationID && x.Code == p.Code && kept(x.DeletedAt)
	}) {
		return conflict("product", "code")
	}
	return nil
}

func (r productRepo) Create(_ context.Context, p *catalog.Product) error {
	if r.d.products.has(p.ID) {
		return conflict("product", "id")
	}
	if err := r.check(p); err != nil {
		return err
	}
	r.d.products.put(p.ID, *p)
	return nil
}

func (r productRepo) Update(_ context.Context, p *catalog.Product) error {
	cur, ok := r.d.products.get(p.ID)
	if !ok || cur.OrganizationID != p.OrganizationID || !kept(cur.DeletedAt) {
		return notFound("product", p.ID)
	}
	if err := r.check(p); err != nil {
		return err
	}
	r.d.products.put(p.ID, *p)
	return nil
}

func (r productRepo) Discard(_ context.Context, org core.OrganizationID, id core.ProductID, at time.Time) error {
	cur, ok := r.d.products.get(id)
	if !ok || cur.OrganizationID != org || !kept(cur.DeletedAt) {
		return notFound("product", id)
	}
	r.d.discardProduct(id, at)
	return nil
}

func (r productRepo) Get(_ context.Context, org core.OrganizationID, id core.ProductID) (*catalog.Product, error) {
	p, ok := r.d.products.get(id)
	if !ok || p.OrganizationID != org {
		return nil, notFound("product", id)
	}
	return p, nil
}

func (r productRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*catalog.Product, error) {
	p, ok := r.d.products.first(func(x *catalog.Product) bool { return x.OrganizationID == org && x.Code == code && kept(x.DeletedAt) })
	if !ok {
		return nil, notFound("product", stringer(code))
	}
	return p, nil
}

func (r productRepo) match(org core.OrganizationID, q catalog.ProductQuery) func(*catalog.Product) bool {
	return func(p *catalog.Product) bool {
		if p.OrganizationID != org || !kept(p.DeletedAt) || !matchesNameOrCode(q.Search, p.Name, p.Code) {
			return false
		}
		if q.ProductType != "" && p.ProductType != q.ProductType {
			return false
		}
		if len(q.ProductCategoryIDs) > 0 || q.WithoutProductCategory {
			in := slices.Contains(q.ProductCategoryIDs, p.ProductCategoryID) && !p.ProductCategoryID.IsZero()
			if uncategorized := q.WithoutProductCategory && p.ProductCategoryID.IsZero(); !in && !uncategorized {
				return false
			}
		}
		return true
	}
}

func (r productRepo) List(_ context.Context, org core.OrganizationID, q catalog.ProductQuery, page core.PageRequest) (core.Page[*catalog.Product], error) {
	rows := r.d.products.find(r.match(org, q))
	return paginateRecent(rows, recentKey(
		func(p *catalog.Product) time.Time { return p.CreatedAt },
		func(p *catalog.Product) uuid.UUID { return p.ID.UUID() }), page)
}

func (r productRepo) Count(_ context.Context, org core.OrganizationID, q catalog.ProductQuery) (int, error) {
	return r.d.products.count(r.match(org, q)), nil
}

// productFilterRepo implements catalog.ProductFilterRepository.
type productFilterRepo struct {
	d  *productCatalogData
	st *state
}

func (r productFilterRepo) check(f *catalog.ProductFilter) error {
	if r.d.filters.any(func(x *catalog.ProductFilter) bool {
		return x.ID != f.ID && x.OrganizationID == f.OrganizationID && x.ProductID == f.ProductID && x.Code == f.Code && kept(x.DeletedAt)
	}) {
		return conflict("product_filter", "code")
	}
	return nil
}

// fill sets the keys of f's values from their metric filters.
func (r productFilterRepo) fill(f *catalog.ProductFilter) *catalog.ProductFilter {
	for i, v := range f.Values {
		r.st.catalog.metrics.first(func(m *catalog.BillableMetric) bool {
			for _, mf := range m.Filters {
				if mf.ID == v.BillableMetricFilterID {
					f.Values[i].Key = mf.Key
					return true
				}
			}
			return false
		})
	}
	return f
}

func (r productFilterRepo) Create(_ context.Context, f *catalog.ProductFilter) error {
	if r.d.filters.has(f.ID) {
		return conflict("product_filter", "id")
	}
	if err := r.check(f); err != nil {
		return err
	}
	r.d.filters.put(f.ID, *f)
	return nil
}

func (r productFilterRepo) Update(_ context.Context, f *catalog.ProductFilter) error {
	cur, ok := r.d.filters.get(f.ID)
	if !ok || cur.OrganizationID != f.OrganizationID || !kept(cur.DeletedAt) {
		return notFound("product_filter", f.ID)
	}
	if err := r.check(f); err != nil {
		return err
	}
	r.d.filters.put(f.ID, *f)
	return nil
}

func (r productFilterRepo) Discard(_ context.Context, org core.OrganizationID, id core.ProductFilterID, at time.Time) error {
	cur, ok := r.d.filters.get(id)
	if !ok || cur.OrganizationID != org || !kept(cur.DeletedAt) {
		return notFound("product_filter", id)
	}
	for _, c := range r.d.rateCards.find(func(c *catalog.RateCard) bool { return c.ProductFilterID == id && kept(c.DeletedAt) }) {
		r.d.discardRateCard(c, at)
	}
	cur.DeletedAt, cur.Values = discardAt(at), nil
	r.d.filters.put(id, *cur)
	return nil
}

func (r productFilterRepo) Get(_ context.Context, org core.OrganizationID, id core.ProductFilterID) (*catalog.ProductFilter, error) {
	f, ok := r.d.filters.get(id)
	if !ok || f.OrganizationID != org {
		return nil, notFound("product_filter", id)
	}
	return r.fill(f), nil
}

func (r productFilterRepo) GetByCode(_ context.Context, org core.OrganizationID, product core.ProductID, code string) (*catalog.ProductFilter, error) {
	f, ok := r.d.filters.first(func(x *catalog.ProductFilter) bool {
		return x.OrganizationID == org && x.ProductID == product && x.Code == code && kept(x.DeletedAt)
	})
	if !ok {
		return nil, notFound("product_filter", stringer(code))
	}
	return r.fill(f), nil
}

func (r productFilterRepo) rows(org core.OrganizationID, product core.ProductID) []*catalog.ProductFilter {
	rows := r.d.filters.find(func(f *catalog.ProductFilter) bool {
		return f.OrganizationID == org && f.ProductID == product && kept(f.DeletedAt)
	})
	for _, f := range rows {
		r.fill(f)
	}
	return rows
}

var filterKey = recentKey(
	func(f *catalog.ProductFilter) time.Time { return f.CreatedAt },
	func(f *catalog.ProductFilter) uuid.UUID { return f.ID.UUID() })

func (r productFilterRepo) List(_ context.Context, org core.OrganizationID, product core.ProductID, search string, page core.PageRequest) (core.Page[*catalog.ProductFilter], error) {
	rows := slices.DeleteFunc(r.rows(org, product), func(f *catalog.ProductFilter) bool { return !matchesNameOrCode(search, f.Name, f.Code) })
	return paginateOldest(rows, filterKey, page)
}

func (r productFilterRepo) ListAll(_ context.Context, org core.OrganizationID, product core.ProductID) ([]*catalog.ProductFilter, error) {
	rows := r.rows(org, product)
	slices.SortStableFunc(rows, func(a, b *catalog.ProductFilter) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	return rows, nil
}

func (r productFilterRepo) Count(_ context.Context, org core.OrganizationID, product core.ProductID) (int, error) {
	return r.d.filters.count(func(f *catalog.ProductFilter) bool {
		return f.OrganizationID == org && f.ProductID == product && kept(f.DeletedAt)
	}), nil
}

func (r productFilterRepo) ReferencesMetric(_ context.Context, org core.OrganizationID, id core.BillableMetricID) (bool, error) {
	m, ok := r.st.catalog.metrics.get(id)
	if !ok || m.OrganizationID != org {
		return false, nil
	}
	return r.d.filters.any(func(f *catalog.ProductFilter) bool {
		if f.OrganizationID != org || !kept(f.DeletedAt) {
			return false
		}
		for _, v := range f.Values {
			for _, mf := range m.Filters {
				if mf.ID == v.BillableMetricFilterID {
					return true
				}
			}
		}
		return false
	}), nil
}

func (r productFilterRepo) MetricReferences(_ context.Context, org core.OrganizationID, id core.BillableMetricID) ([]catalog.ProductFilterValue, error) {
	m, ok := r.st.catalog.metrics.get(id)
	if !ok || m.OrganizationID != org {
		return nil, nil
	}
	var out []catalog.ProductFilterValue
	for _, f := range r.d.filters.find(func(f *catalog.ProductFilter) bool { return f.OrganizationID == org && kept(f.DeletedAt) }) {
		for _, v := range f.Values {
			if !slices.ContainsFunc(m.Filters, func(mf catalog.MetricFilter) bool { return mf.ID == v.BillableMetricFilterID }) {
				continue
			}
			if slices.ContainsFunc(out, func(o catalog.ProductFilterValue) bool {
				return o.BillableMetricFilterID == v.BillableMetricFilterID && sameFilterValue(o.Value, v.Value)
			}) {
				continue
			}
			out = append(out, catalog.ProductFilterValue{BillableMetricFilterID: v.BillableMetricFilterID, Value: v.Value})
		}
	}
	return out, nil
}

func sameFilterValue(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// rateCardRepo implements catalog.RateCardRepository.
type rateCardRepo struct{ d *productCatalogData }

func (r rateCardRepo) check(c *catalog.RateCard) error {
	if r.d.rateCards.any(func(x *catalog.RateCard) bool {
		return x.ID != c.ID && x.OrganizationID == c.OrganizationID && x.Code == c.Code && kept(x.DeletedAt)
	}) {
		return conflict("rate_card", "code")
	}
	return nil
}

func (r rateCardRepo) Create(_ context.Context, c *catalog.RateCard) error {
	if r.d.rateCards.has(c.ID) {
		return conflict("rate_card", "id")
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.rateCards.put(c.ID, *c)
	return nil
}

func (r rateCardRepo) Update(_ context.Context, c *catalog.RateCard) error {
	cur, ok := r.d.rateCards.get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID || !kept(cur.DeletedAt) {
		return notFound("rate_card", c.ID)
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.rateCards.put(c.ID, *c)
	return nil
}

func (r rateCardRepo) Discard(_ context.Context, org core.OrganizationID, id core.RateCardID, at time.Time) error {
	cur, ok := r.d.rateCards.get(id)
	if !ok || cur.OrganizationID != org || !kept(cur.DeletedAt) {
		return notFound("rate_card", id)
	}
	r.d.discardRateCard(cur, at)
	return nil
}

func (r rateCardRepo) Get(_ context.Context, org core.OrganizationID, id core.RateCardID) (*catalog.RateCard, error) {
	c, ok := r.d.rateCards.get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("rate_card", id)
	}
	return c, nil
}

func (r rateCardRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*catalog.RateCard, error) {
	c, ok := r.d.rateCards.first(func(x *catalog.RateCard) bool { return x.OrganizationID == org && x.Code == code && kept(x.DeletedAt) })
	if !ok {
		return nil, notFound("rate_card", stringer(code))
	}
	return c, nil
}

func (r rateCardRepo) List(_ context.Context, org core.OrganizationID, q catalog.RateCardQuery, page core.PageRequest) (core.Page[*catalog.RateCard], error) {
	rows := r.d.rateCards.find(func(c *catalog.RateCard) bool {
		if c.OrganizationID != org || !kept(c.DeletedAt) || !matchesNameOrCode(q.Search, c.Name, c.Code) {
			return false
		}
		if len(q.ProductIDs) > 0 && !slices.Contains(q.ProductIDs, c.ProductID) {
			return false
		}
		if len(q.ProductFilterIDs) > 0 && (c.ProductFilterID.IsZero() || !slices.Contains(q.ProductFilterIDs, c.ProductFilterID)) {
			return false
		}
		if len(q.ProductCategoryIDs) > 0 || q.WithoutProductCategory {
			p, ok := r.d.products.get(c.ProductID)
			if !ok {
				return false
			}
			in := !p.ProductCategoryID.IsZero() && slices.Contains(q.ProductCategoryIDs, p.ProductCategoryID)
			if uncategorized := q.WithoutProductCategory && p.ProductCategoryID.IsZero(); !in && !uncategorized {
				return false
			}
		}
		return true
	})
	return paginateRecent(rows, recentKey(
		func(c *catalog.RateCard) time.Time { return c.CreatedAt },
		func(c *catalog.RateCard) uuid.UUID { return c.ID.UUID() }), page)
}

// rateRepo implements catalog.RateRepository.
type rateRepo struct{ d *productCatalogData }

func (r rateRepo) check(x *catalog.Rate) error {
	for _, o := range r.d.rates.find(func(o *catalog.Rate) bool {
		return o.ID != x.ID && o.OrganizationID == x.OrganizationID && o.RateCardID == x.RateCardID && kept(o.DeletedAt)
	}) {
		if o.Code == x.Code {
			return conflict("rate", "code")
		}
		if o.EffectiveFrom.Equal(x.EffectiveFrom) {
			return conflict("rate", "effective_from")
		}
	}
	return nil
}

func (r rateRepo) Create(_ context.Context, x *catalog.Rate) error {
	if r.d.rates.has(x.ID) {
		return conflict("rate", "id")
	}
	if err := r.check(x); err != nil {
		return err
	}
	r.d.rates.put(x.ID, *x)
	return nil
}

func (r rateRepo) Update(_ context.Context, x *catalog.Rate) error {
	cur, ok := r.d.rates.get(x.ID)
	if !ok || cur.OrganizationID != x.OrganizationID || !kept(cur.DeletedAt) {
		return notFound("rate", x.ID)
	}
	if err := r.check(x); err != nil {
		return err
	}
	r.d.rates.put(x.ID, *x)
	return nil
}

func (r rateRepo) Discard(_ context.Context, org core.OrganizationID, id core.RateCardRateID, at time.Time) error {
	cur, ok := r.d.rates.get(id)
	if !ok || cur.OrganizationID != org || !kept(cur.DeletedAt) {
		return notFound("rate", id)
	}
	cur.DeletedAt = discardAt(at)
	r.d.rates.put(id, *cur)
	return nil
}

func (r rateRepo) Get(_ context.Context, org core.OrganizationID, id core.RateCardRateID) (*catalog.Rate, error) {
	x, ok := r.d.rates.get(id)
	if !ok || x.OrganizationID != org {
		return nil, notFound("rate", id)
	}
	return x, nil
}

func (r rateRepo) ListByRateCard(_ context.Context, org core.OrganizationID, card core.RateCardID) ([]*catalog.Rate, error) {
	rows := r.d.rates.find(func(x *catalog.Rate) bool {
		return x.OrganizationID == org && x.RateCardID == card && kept(x.DeletedAt)
	})
	slices.SortStableFunc(rows, func(a, b *catalog.Rate) int {
		return cmp.Or(b.EffectiveFrom.Compare(a.EffectiveFrom), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	return rows, nil
}

// planRateCardRepo implements catalog.PlanRateCardRepository.
type planRateCardRepo struct{ d *productCatalogData }

func (r planRateCardRepo) Create(_ context.Context, p *catalog.PlanRateCard, phases []*catalog.RatePhase) error {
	if r.d.planRateCards.has(p.ID) {
		return conflict("plan_rate_card", "id")
	}
	if r.d.planRateCards.any(func(x *catalog.PlanRateCard) bool {
		return x.OrganizationID == p.OrganizationID && x.PlanID == p.PlanID && x.RateCardID == p.RateCardID && kept(x.DeletedAt)
	}) {
		return conflict("plan_rate_card", "rate_card")
	}
	if err := r.putPhases(p.ID, phases, p.CreatedAt); err != nil {
		return err
	}
	r.d.planRateCards.put(p.ID, *p)
	return nil
}

// putPhases makes phases the kept phases of the plan rate card id,
// discarding the others at at.
func (r planRateCardRepo) putPhases(id core.PlanRateCardID, phases []*catalog.RatePhase, at time.Time) error {
	codes, positions := map[string]bool{}, map[int]bool{}
	for _, ph := range phases {
		if codes[ph.Code] || positions[ph.Position] {
			return conflict("rate_phase", "code")
		}
		codes[ph.Code], positions[ph.Position] = true, true
	}
	r.d.discardPhases(id, func(p core.RatePhaseID) bool {
		return slices.ContainsFunc(phases, func(ph *catalog.RatePhase) bool { return ph.ID == p })
	}, at)
	for _, ph := range phases {
		x := *ph
		x.PlanRateCardID = id
		r.d.phases.put(x.ID, x)
	}
	return nil
}

func (r planRateCardRepo) Update(_ context.Context, p *catalog.PlanRateCard) error {
	cur, ok := r.d.planRateCards.get(p.ID)
	if !ok || cur.OrganizationID != p.OrganizationID || !kept(cur.DeletedAt) {
		return notFound("plan_rate_card", p.ID)
	}
	r.d.planRateCards.put(p.ID, *p)
	return nil
}

func (r planRateCardRepo) Discard(_ context.Context, org core.OrganizationID, id core.PlanRateCardID, at time.Time) error {
	cur, ok := r.d.planRateCards.get(id)
	if !ok || cur.OrganizationID != org || !kept(cur.DeletedAt) {
		return notFound("plan_rate_card", id)
	}
	r.d.discardPhases(id, keepNone, at)
	cur.DeletedAt = discardAt(at)
	r.d.planRateCards.put(id, *cur)
	return nil
}

func (r planRateCardRepo) DiscardByPlan(_ context.Context, org core.OrganizationID, plan core.PlanID, at time.Time) (int, error) {
	rows := r.d.planRateCards.find(func(p *catalog.PlanRateCard) bool {
		return p.OrganizationID == org && p.PlanID == plan && kept(p.DeletedAt)
	})
	for _, p := range rows {
		r.d.discardPhases(p.ID, keepNone, at)
		p.DeletedAt = discardAt(at)
		r.d.planRateCards.put(p.ID, *p)
	}
	return len(rows), nil
}

func (r planRateCardRepo) Get(_ context.Context, org core.OrganizationID, id core.PlanRateCardID) (*catalog.PlanRateCard, error) {
	p, ok := r.d.planRateCards.get(id)
	if !ok || p.OrganizationID != org {
		return nil, notFound("plan_rate_card", id)
	}
	return p, nil
}

func (r planRateCardRepo) GetByRateCard(_ context.Context, org core.OrganizationID, plan core.PlanID, card core.RateCardID) (*catalog.PlanRateCard, error) {
	p, ok := r.d.planRateCards.first(func(x *catalog.PlanRateCard) bool {
		return x.OrganizationID == org && x.PlanID == plan && x.RateCardID == card && kept(x.DeletedAt)
	})
	if !ok {
		return nil, notFound("plan_rate_card", card)
	}
	return p, nil
}

func (r planRateCardRepo) match(org core.OrganizationID, q catalog.PlanRateCardQuery) func(*catalog.PlanRateCard) bool {
	return func(p *catalog.PlanRateCard) bool {
		if p.OrganizationID != org || !kept(p.DeletedAt) || (!q.PlanID.IsZero() && p.PlanID != q.PlanID) || (!q.RateCardID.IsZero() && p.RateCardID != q.RateCardID) {
			return false
		}
		c, ok := r.d.rateCards.get(p.RateCardID)
		if !ok || !kept(c.DeletedAt) || (!q.ProductID.IsZero() && c.ProductID != q.ProductID) || (!q.ProductFilterID.IsZero() && c.ProductFilterID != q.ProductFilterID) {
			return false
		}
		if !q.ProductCategoryID.IsZero() {
			prod, ok := r.d.products.get(c.ProductID)
			return ok && prod.ProductCategoryID == q.ProductCategoryID
		}
		return true
	}
}

func (r planRateCardRepo) List(_ context.Context, org core.OrganizationID, q catalog.PlanRateCardQuery, page core.PageRequest) (core.Page[*catalog.PlanRateCard], error) {
	rows := r.d.planRateCards.find(r.match(org, q))
	return paginateOldest(rows, recentKey(
		func(p *catalog.PlanRateCard) time.Time { return p.CreatedAt },
		func(p *catalog.PlanRateCard) uuid.UUID { return p.ID.UUID() }), page)
}

func (r planRateCardRepo) PlanIDs(_ context.Context, org core.OrganizationID, q catalog.PlanRateCardQuery) ([]core.PlanID, error) {
	var out []core.PlanID
	for _, p := range r.d.planRateCards.find(r.match(org, q)) {
		if !slices.Contains(out, p.PlanID) {
			out = append(out, p.PlanID)
		}
	}
	slices.SortFunc(out, func(a, b core.PlanID) int { return cmp.Compare(a.String(), b.String()) })
	return out, nil
}

func (r planRateCardRepo) ListPhases(_ context.Context, org core.OrganizationID, id core.PlanRateCardID) ([]*catalog.RatePhase, error) {
	rows := r.d.phases.find(func(p *catalog.RatePhase) bool {
		return p.OrganizationID == org && p.PlanRateCardID == id && kept(p.DeletedAt)
	})
	slices.SortStableFunc(rows, func(a, b *catalog.RatePhase) int { return cmp.Compare(a.Position, b.Position) })
	return rows, nil
}

func (r planRateCardRepo) SavePhases(_ context.Context, org core.OrganizationID, id core.PlanRateCardID, phases []*catalog.RatePhase, at time.Time) error {
	cur, ok := r.d.planRateCards.get(id)
	if !ok || cur.OrganizationID != org || !kept(cur.DeletedAt) {
		return notFound("plan_rate_card", id)
	}
	return r.putPhases(id, phases, at)
}

var (
	_ catalog.ProductCategoryRepository = productCategoryRepo{}
	_ catalog.ProductRepository         = productRepo{}
	_ catalog.ProductFilterRepository   = productFilterRepo{}
	_ catalog.RateCardRepository        = rateCardRepo{}
	_ catalog.RateRepository            = rateRepo{}
	_ catalog.PlanRateCardRepository    = planRateCardRepo{}
)
