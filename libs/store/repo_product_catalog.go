package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindProductCatalog binds the repositories of the product catalog.
func (t *txState) bindProductCatalog(r *port.Repos) {
	r.ProductCategories = productCategoryRepo{t}
	r.Products = productRepo{t}
	r.ProductFilters = productFilterRepo{t}
	r.RateCards = rateCardRepo{t}
	r.Rates = rateRepo{t}
	r.PlanRateCards = planRateCardRepo{t}
}

var (
	_ catalog.ProductCategoryRepository = productCategoryRepo{}
	_ catalog.ProductRepository         = productRepo{}
	_ catalog.ProductFilterRepository   = productFilterRepo{}
	_ catalog.RateCardRepository        = rateCardRepo{}
	_ catalog.RateRepository            = rateRepo{}
	_ catalog.PlanRateCardRepository    = planRateCardRepo{}
)

// productCategoryRepo implements catalog.ProductCategoryRepository.
type productCategoryRepo struct{ t *txState }

func productCategoryOf(r db.ProductCategory) (*catalog.ProductCategory, error) {
	return &catalog.ProductCategory{
		ID:                 idOf[core.ProductCategoryID](r.ID),
		OrganizationID:     idOf[core.OrganizationID](r.OrganizationID),
		Code:               r.Code,
		Name:               r.Name,
		Description:        r.Description,
		InvoiceDisplayName: r.InvoiceDisplayName,
		CreatedAt:          utc(r.CreatedAt),
		UpdatedAt:          utc(r.UpdatedAt),
		DeletedAt:          optUTC(r.DeletedAt),
	}, nil
}

func (r productCategoryRepo) Create(ctx context.Context, c *catalog.ProductCategory) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateProductCategory(ctx, db.CreateProductCategoryParams{
		OrganizationID: c.OrganizationID.UUID(), ID: c.ID.UUID(), Code: c.Code, Name: c.Name,
		Description: c.Description, InvoiceDisplayName: c.InvoiceDisplayName,
		CreatedAt: utc(c.CreatedAt), UpdatedAt: utc(c.UpdatedAt),
	})
	return insertErr("product_category", n, err)
}

func (r productCategoryRepo) Update(ctx context.Context, c *catalog.ProductCategory) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdateProductCategory(ctx, db.UpdateProductCategoryParams{
		OrganizationID: c.OrganizationID.UUID(), ID: c.ID.UUID(), Code: c.Code, Name: c.Name,
		Description: c.Description, InvoiceDisplayName: c.InvoiceDisplayName, UpdatedAt: utc(c.UpdatedAt),
	})
	return updateErr("product_category", c.ID, n, err)
}

func (r productCategoryRepo) Discard(ctx context.Context, org core.OrganizationID, id core.ProductCategoryID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DiscardProductCategory(ctx, db.DiscardProductCategoryParams{OrganizationID: org.UUID(), ID: id.UUID(), At: optUTC(&at)})
	if err := updateErr("product_category", id, n, err); err != nil {
		return err
	}
	products, err := q.ListProductIDsOfCategory(ctx, db.ListProductIDsOfCategoryParams{OrganizationID: org.UUID(), ProductCategoryID: optUUID(id)})
	if err != nil {
		return fmt.Errorf("list products of category %s: %w", id, mapErr(err))
	}
	for _, p := range products {
		if err := productRepo(r).Discard(ctx, org, idOf[core.ProductID](p), at); err != nil {
			return err
		}
	}
	return nil
}

func (r productCategoryRepo) Get(ctx context.Context, org core.OrganizationID, id core.ProductCategoryID) (*catalog.ProductCategory, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetProductCategory(ctx, db.GetProductCategoryParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("product_category", id, err)
	}
	return productCategoryOf(row)
}

func (r productCategoryRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.ProductCategory, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetProductCategoryByCode(ctx, db.GetProductCategoryByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("product_category", code, err)
	}
	return productCategoryOf(row)
}

func (r productCategoryRepo) List(ctx context.Context, org core.OrganizationID, f catalog.ProductCategoryQuery, page core.PageRequest) (core.Page[*catalog.ProductCategory], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*catalog.ProductCategory]{}, err
	}
	after, afterAt, lim, err := timeCursorArgs(page)
	if err != nil {
		return core.Page[*catalog.ProductCategory]{}, err
	}
	rows, err := q.ListProductCategories(ctx, db.ListProductCategoriesParams{
		OrganizationID: org.UUID(), Search: searchArg(f.Search), After: after, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*catalog.ProductCategory]{}, fmt.Errorf("list product categories: %w", mapErr(err))
	}
	return pageOf(rows, page, productCategoryOf, func(c *catalog.ProductCategory) string { return recentCursor(c.CreatedAt, c.ID.UUID()) })
}

// productRepo implements catalog.ProductRepository.
type productRepo struct{ t *txState }

func productOf(r db.Product) (*catalog.Product, error) {
	pt, err := catalog.ParseProductType(r.ProductType)
	if err != nil {
		return nil, fmt.Errorf("product %s: %w", r.ID, err)
	}
	return &catalog.Product{
		ID:                 idOf[core.ProductID](r.ID),
		OrganizationID:     idOf[core.OrganizationID](r.OrganizationID),
		ProductCategoryID:  optIDOf[core.ProductCategoryID](r.ProductCategoryID),
		BillableMetricID:   optIDOf[core.BillableMetricID](r.BillableMetricID),
		ProductType:        pt,
		Code:               r.Code,
		Name:               r.Name,
		Description:        r.Description,
		InvoiceDisplayName: r.InvoiceDisplayName,
		CreatedAt:          utc(r.CreatedAt),
		UpdatedAt:          utc(r.UpdatedAt),
		DeletedAt:          optUTC(r.DeletedAt),
	}, nil
}

func (r productRepo) Create(ctx context.Context, p *catalog.Product) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateProduct(ctx, db.CreateProductParams{
		OrganizationID: p.OrganizationID.UUID(), ID: p.ID.UUID(), ProductCategoryID: optUUID(p.ProductCategoryID),
		BillableMetricID: optUUID(p.BillableMetricID), ProductType: p.ProductType.String(), Code: p.Code, Name: p.Name,
		Description: p.Description, InvoiceDisplayName: p.InvoiceDisplayName,
		CreatedAt: utc(p.CreatedAt), UpdatedAt: utc(p.UpdatedAt),
	})
	return insertErr("product", n, err)
}

func (r productRepo) Update(ctx context.Context, p *catalog.Product) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdateProduct(ctx, db.UpdateProductParams{
		OrganizationID: p.OrganizationID.UUID(), ID: p.ID.UUID(), ProductCategoryID: optUUID(p.ProductCategoryID),
		Code: p.Code, Name: p.Name, Description: p.Description, InvoiceDisplayName: p.InvoiceDisplayName,
		UpdatedAt: utc(p.UpdatedAt),
	})
	return updateErr("product", p.ID, n, err)
}

func (r productRepo) Discard(ctx context.Context, org core.OrganizationID, id core.ProductID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	o, p, t := org.UUID(), id.UUID(), optUTC(&at)
	n, err := q.DiscardProduct(ctx, db.DiscardProductParams{OrganizationID: o, ID: p, At: t})
	if err := updateErr("product", id, n, err); err != nil {
		return err
	}
	if err := q.DiscardProductFilterValuesOfProduct(ctx, db.DiscardProductFilterValuesOfProductParams{OrganizationID: o, ProductID: p, At: t}); err != nil {
		return fmt.Errorf("discard filter values of product %s: %w", id, mapErr(err))
	}
	if err := q.DiscardProductFiltersOfProduct(ctx, db.DiscardProductFiltersOfProductParams{OrganizationID: o, ProductID: p, At: t}); err != nil {
		return fmt.Errorf("discard filters of product %s: %w", id, mapErr(err))
	}
	if err := q.DiscardRatesOfProduct(ctx, db.DiscardRatesOfProductParams{OrganizationID: o, ProductID: p, At: t}); err != nil {
		return fmt.Errorf("discard rates of product %s: %w", id, mapErr(err))
	}
	if err := q.DiscardRateCardsOfProduct(ctx, db.DiscardRateCardsOfProductParams{OrganizationID: o, ProductID: p, At: t}); err != nil {
		return fmt.Errorf("discard rate cards of product %s: %w", id, mapErr(err))
	}
	return nil
}

func (r productRepo) Get(ctx context.Context, org core.OrganizationID, id core.ProductID) (*catalog.Product, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetProduct(ctx, db.GetProductParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("product", id, err)
	}
	return productOf(row)
}

func (r productRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.Product, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetProductByCode(ctx, db.GetProductByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("product", code, err)
	}
	return productOf(row)
}

func (r productRepo) List(ctx context.Context, org core.OrganizationID, f catalog.ProductQuery, page core.PageRequest) (core.Page[*catalog.Product], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*catalog.Product]{}, err
	}
	after, afterAt, lim, err := timeCursorArgs(page)
	if err != nil {
		return core.Page[*catalog.Product]{}, err
	}
	rows, err := q.ListProducts(ctx, db.ListProductsParams{
		OrganizationID: org.UUID(), Search: searchArg(f.Search), ProductType: optString(f.ProductType.String()),
		CategoryIds: nonNil(uuids(f.ProductCategoryIDs)), WithoutCategory: f.WithoutProductCategory,
		After: after, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*catalog.Product]{}, fmt.Errorf("list products: %w", mapErr(err))
	}
	return pageOf(rows, page, productOf, func(p *catalog.Product) string { return recentCursor(p.CreatedAt, p.ID.UUID()) })
}

func (r productRepo) Count(ctx context.Context, org core.OrganizationID, f catalog.ProductQuery) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.CountProducts(ctx, db.CountProductsParams{
		OrganizationID: org.UUID(), Search: searchArg(f.Search), ProductType: optString(f.ProductType.String()),
		CategoryIds: nonNil(uuids(f.ProductCategoryIDs)), WithoutCategory: f.WithoutProductCategory,
	})
	if err != nil {
		return 0, fmt.Errorf("count products: %w", mapErr(err))
	}
	return int(n), nil
}

// productFilterRepo implements catalog.ProductFilterRepository.
type productFilterRepo struct{ t *txState }

func productFilterOf(r db.ProductFilter) (*catalog.ProductFilter, error) {
	return &catalog.ProductFilter{
		ID:                 idOf[core.ProductFilterID](r.ID),
		OrganizationID:     idOf[core.OrganizationID](r.OrganizationID),
		ProductID:          idOf[core.ProductID](r.ProductID),
		Code:               r.Code,
		Name:               r.Name,
		Description:        r.Description,
		InvoiceDisplayName: r.InvoiceDisplayName,
		Values:             []catalog.ProductFilterValue{},
		CreatedAt:          utc(r.CreatedAt),
		UpdatedAt:          utc(r.UpdatedAt),
		DeletedAt:          optUTC(r.DeletedAt),
	}, nil
}

// fill reads the values of the filters fs.
func (r productFilterRepo) fill(ctx context.Context, q *db.Queries, org core.OrganizationID, fs []*catalog.ProductFilter) error {
	if len(fs) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(fs))
	byID := make(map[uuid.UUID]*catalog.ProductFilter, len(fs))
	for i, f := range fs {
		ids[i] = f.ID.UUID()
		byID[ids[i]] = f
	}
	rows, err := q.ListProductFilterValues(ctx, db.ListProductFilterValuesParams{OrganizationID: org.UUID(), FilterIds: ids})
	if err != nil {
		return fmt.Errorf("list product filter values: %w", mapErr(err))
	}
	for _, v := range rows {
		f := byID[v.ProductFilterID]
		f.Values = append(f.Values, catalog.ProductFilterValue{
			BillableMetricFilterID: idOf[core.BillableMetricFilterID](v.BillableMetricFilterID),
			Key:                    v.Key,
			Value:                  v.Value,
		})
	}
	return nil
}

// writeValues inserts the values of f.
func (r productFilterRepo) writeValues(ctx context.Context, q *db.Queries, f *catalog.ProductFilter) error {
	for i, v := range f.Values {
		if err := q.InsertProductFilterValue(ctx, db.InsertProductFilterValueParams{
			OrganizationID: f.OrganizationID.UUID(), ProductFilterID: f.ID.UUID(), Position: int32(i), //nolint:gosec // at most 50 values
			BillableMetricFilterID: v.BillableMetricFilterID.UUID(), Value: v.Value,
		}); err != nil {
			return fmt.Errorf("product filter %s values: %w", f.ID, mapErr(err))
		}
	}
	return nil
}

func (r productFilterRepo) Create(ctx context.Context, f *catalog.ProductFilter) error {
	q, err := r.t.queries(ctx, f.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateProductFilter(ctx, db.CreateProductFilterParams{
		OrganizationID: f.OrganizationID.UUID(), ID: f.ID.UUID(), ProductID: f.ProductID.UUID(), Code: f.Code,
		Name: f.Name, Description: f.Description, InvoiceDisplayName: f.InvoiceDisplayName,
		CreatedAt: utc(f.CreatedAt), UpdatedAt: utc(f.UpdatedAt),
	})
	if err := insertErr("product_filter", n, err); err != nil {
		return err
	}
	return r.writeValues(ctx, q, f)
}

func (r productFilterRepo) Update(ctx context.Context, f *catalog.ProductFilter) error {
	q, err := r.t.queries(ctx, f.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdateProductFilter(ctx, db.UpdateProductFilterParams{
		OrganizationID: f.OrganizationID.UUID(), ID: f.ID.UUID(), Code: f.Code, Name: f.Name,
		Description: f.Description, InvoiceDisplayName: f.InvoiceDisplayName, UpdatedAt: utc(f.UpdatedAt),
	})
	if err := updateErr("product_filter", f.ID, n, err); err != nil {
		return err
	}
	current := &catalog.ProductFilter{ID: f.ID, Values: []catalog.ProductFilterValue{}}
	if err := r.fill(ctx, q, f.OrganizationID, []*catalog.ProductFilter{current}); err != nil {
		return err
	}
	if sameValues(current.Values, f.Values) {
		return nil
	}
	if err := q.DiscardProductFilterValues(ctx, db.DiscardProductFilterValuesParams{
		OrganizationID: f.OrganizationID.UUID(), ProductFilterID: f.ID.UUID(), At: optUTC(&f.UpdatedAt),
	}); err != nil {
		return fmt.Errorf("product filter %s values: %w", f.ID, mapErr(err))
	}
	return r.writeValues(ctx, q, f)
}

// sameValues reports whether a and b select the same metric filter
// values, in any order (lago values_changed?).
func sameValues(a, b []catalog.ProductFilterValue) bool {
	key := func(v catalog.ProductFilterValue) string {
		return v.BillableMetricFilterID.String() + "\x00" + deref(v.Value)
	}
	ka, kb := make([]string, len(a)), make([]string, len(b))
	for i, v := range a {
		ka[i] = key(v)
	}
	for i, v := range b {
		kb[i] = key(v)
	}
	slices.Sort(ka)
	slices.Sort(kb)
	return slices.Equal(ka, kb)
}

func (r productFilterRepo) Discard(ctx context.Context, org core.OrganizationID, id core.ProductFilterID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	o, f, t := org.UUID(), id.UUID(), optUTC(&at)
	n, err := q.DiscardProductFilter(ctx, db.DiscardProductFilterParams{OrganizationID: o, ID: f, At: t})
	if err := updateErr("product_filter", id, n, err); err != nil {
		return err
	}
	if err := q.DiscardRatesOfProductFilter(ctx, db.DiscardRatesOfProductFilterParams{OrganizationID: o, ProductFilterID: &f, At: t}); err != nil {
		return fmt.Errorf("discard rates of product filter %s: %w", id, mapErr(err))
	}
	if err := q.DiscardRateCardsOfProductFilter(ctx, db.DiscardRateCardsOfProductFilterParams{OrganizationID: o, ProductFilterID: &f, At: t}); err != nil {
		return fmt.Errorf("discard rate cards of product filter %s: %w", id, mapErr(err))
	}
	if err := q.DiscardProductFilterValues(ctx, db.DiscardProductFilterValuesParams{OrganizationID: o, ProductFilterID: f, At: t}); err != nil {
		return fmt.Errorf("discard values of product filter %s: %w", id, mapErr(err))
	}
	return nil
}

func (r productFilterRepo) one(ctx context.Context, q *db.Queries, org core.OrganizationID, row db.ProductFilter) (*catalog.ProductFilter, error) {
	f, err := productFilterOf(row)
	if err != nil {
		return nil, err
	}
	if err := r.fill(ctx, q, org, []*catalog.ProductFilter{f}); err != nil {
		return nil, err
	}
	return f, nil
}

func (r productFilterRepo) Get(ctx context.Context, org core.OrganizationID, id core.ProductFilterID) (*catalog.ProductFilter, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetProductFilter(ctx, db.GetProductFilterParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("product_filter", id, err)
	}
	return r.one(ctx, q, org, row)
}

func (r productFilterRepo) GetByCode(ctx context.Context, org core.OrganizationID, product core.ProductID, code string) (*catalog.ProductFilter, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetProductFilterByCode(ctx, db.GetProductFilterByCodeParams{OrganizationID: org.UUID(), ProductID: product.UUID(), Code: code})
	if err != nil {
		return nil, getErr("product_filter", code, err)
	}
	return r.one(ctx, q, org, row)
}

func (r productFilterRepo) List(ctx context.Context, org core.OrganizationID, product core.ProductID, search string, page core.PageRequest) (core.Page[*catalog.ProductFilter], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*catalog.ProductFilter]{}, err
	}
	after, afterAt, lim, err := timeCursorArgs(page)
	if err != nil {
		return core.Page[*catalog.ProductFilter]{}, err
	}
	rows, err := q.ListProductFilters(ctx, db.ListProductFiltersParams{
		OrganizationID: org.UUID(), ProductID: product.UUID(), Search: searchArg(search), After: after, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*catalog.ProductFilter]{}, fmt.Errorf("list product filters: %w", mapErr(err))
	}
	out, err := pageOf(rows, page, productFilterOf, func(f *catalog.ProductFilter) string { return recentCursor(f.CreatedAt, f.ID.UUID()) })
	if err != nil {
		return out, err
	}
	return out, r.fill(ctx, q, org, out.Items)
}

func (r productFilterRepo) ListAll(ctx context.Context, org core.OrganizationID, product core.ProductID) ([]*catalog.ProductFilter, error) {
	var out []*catalog.ProductFilter
	page := core.PageRequest{Size: core.MaxPageSize}
	for {
		p, err := r.List(ctx, org, product, "", page)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		if p.NextToken == "" {
			return out, nil
		}
		page.Token = p.NextToken
	}
}

func (r productFilterRepo) Count(ctx context.Context, org core.OrganizationID, product core.ProductID) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.CountProductFilters(ctx, db.CountProductFiltersParams{OrganizationID: org.UUID(), ProductID: product.UUID()})
	if err != nil {
		return 0, fmt.Errorf("count product filters: %w", mapErr(err))
	}
	return int(n), nil
}

func (r productFilterRepo) ReferencesMetric(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	ok, err := q.ProductFilterValuesReferenceMetric(ctx, db.ProductFilterValuesReferenceMetricParams{
		OrganizationID: org.UUID(), BillableMetricID: id.UUID(),
	})
	if err != nil {
		return false, fmt.Errorf("check product filter values of metric: %w", mapErr(err))
	}
	return ok, nil
}

func (r productFilterRepo) MetricReferences(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) ([]catalog.ProductFilterValue, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListProductFilterValuesOfMetric(ctx, db.ListProductFilterValuesOfMetricParams{
		OrganizationID: org.UUID(), BillableMetricID: id.UUID(),
	})
	if err != nil {
		return nil, fmt.Errorf("list product filter values of metric: %w", mapErr(err))
	}
	out := make([]catalog.ProductFilterValue, len(rows))
	for i, row := range rows {
		out[i] = catalog.ProductFilterValue{BillableMetricFilterID: idOf[core.BillableMetricFilterID](row.BillableMetricFilterID), Value: row.Value}
	}
	return out, nil
}

// rateCardRepo implements catalog.RateCardRepository.
type rateCardRepo struct{ t *txState }

func rateCardOf(r db.RateCard, taxIDs []uuid.UUID) (*catalog.RateCard, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("rate card %s: %w", r.ID, err)
	}
	timing, err := catalog.ParseBillingTiming(r.BillingTiming)
	if err != nil {
		return nil, fmt.Errorf("rate card %s: %w", r.ID, err)
	}
	return &catalog.RateCard{
		ID:               idOf[core.RateCardID](r.ID),
		OrganizationID:   idOf[core.OrganizationID](r.OrganizationID),
		ProductID:        idOf[core.ProductID](r.ProductID),
		ProductFilterID:  optIDOf[core.ProductFilterID](r.ProductFilterID),
		Code:             r.Code,
		Name:             r.Name,
		Description:      r.Description,
		Currency:         cur,
		BillingTiming:    timing,
		Proration:        r.Proration,
		DisplayOnInvoice: r.DisplayOnInvoice,
		RegroupPaidFees:  catalog.RegroupPaidFees(deref(r.RegroupPaidFees)),
		PricingUnitID:    optIDOf[core.PricingUnitID](r.PricingUnitID),
		TaxIDs:           idsOf[core.TaxID](taxIDs),
		CreatedAt:        utc(r.CreatedAt),
		UpdatedAt:        utc(r.UpdatedAt),
		DeletedAt:        optUTC(r.DeletedAt),
	}, nil
}

func (r rateCardRepo) writeTaxes(ctx context.Context, q *db.Queries, c *catalog.RateCard) error {
	if err := q.DeleteRateCardTaxes(ctx, db.DeleteRateCardTaxesParams{OrganizationID: c.OrganizationID.UUID(), RateCardID: c.ID.UUID()}); err != nil {
		return fmt.Errorf("rate card %s taxes: %w", c.ID, mapErr(err))
	}
	for i, id := range c.TaxIDs {
		if err := q.InsertRateCardTax(ctx, db.InsertRateCardTaxParams{
			OrganizationID: c.OrganizationID.UUID(), RateCardID: c.ID.UUID(), TaxID: id.UUID(), Position: int32(i), //nolint:gosec // at most 50 taxes
		}); err != nil {
			return fmt.Errorf("rate card %s taxes: %w", c.ID, mapErr(err))
		}
	}
	return nil
}

func (r rateCardRepo) Create(ctx context.Context, c *catalog.RateCard) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateRateCard(ctx, db.CreateRateCardParams{
		OrganizationID: c.OrganizationID.UUID(), ID: c.ID.UUID(), ProductID: c.ProductID.UUID(),
		ProductFilterID: optUUID(c.ProductFilterID), Code: c.Code, Name: c.Name, Description: c.Description,
		Currency: c.Currency.Code(), BillingTiming: c.BillingTiming.String(), Proration: c.Proration,
		DisplayOnInvoice: c.DisplayOnInvoice, RegroupPaidFees: optString(string(c.RegroupPaidFees)),
		PricingUnitID: optUUID(c.PricingUnitID), CreatedAt: utc(c.CreatedAt), UpdatedAt: utc(c.UpdatedAt),
	})
	if err := insertErr("rate_card", n, err); err != nil {
		return err
	}
	return r.writeTaxes(ctx, q, c)
}

func (r rateCardRepo) Update(ctx context.Context, c *catalog.RateCard) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdateRateCard(ctx, db.UpdateRateCardParams{
		OrganizationID: c.OrganizationID.UUID(), ID: c.ID.UUID(), Code: c.Code, Name: c.Name, Description: c.Description,
		Currency: c.Currency.Code(), BillingTiming: c.BillingTiming.String(), Proration: c.Proration,
		DisplayOnInvoice: c.DisplayOnInvoice, RegroupPaidFees: optString(string(c.RegroupPaidFees)),
		PricingUnitID: optUUID(c.PricingUnitID), UpdatedAt: utc(c.UpdatedAt),
	})
	if err := updateErr("rate_card", c.ID, n, err); err != nil {
		return err
	}
	return r.writeTaxes(ctx, q, c)
}

func (r rateCardRepo) Discard(ctx context.Context, org core.OrganizationID, id core.RateCardID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DiscardRateCard(ctx, db.DiscardRateCardParams{OrganizationID: org.UUID(), ID: id.UUID(), At: optUTC(&at)})
	if err := updateErr("rate_card", id, n, err); err != nil {
		return err
	}
	if err := q.DiscardRatesOfRateCard(ctx, db.DiscardRatesOfRateCardParams{OrganizationID: org.UUID(), RateCardID: id.UUID(), At: optUTC(&at)}); err != nil {
		return fmt.Errorf("discard rates of rate card %s: %w", id, mapErr(err))
	}
	return nil
}

func (r rateCardRepo) Get(ctx context.Context, org core.OrganizationID, id core.RateCardID) (*catalog.RateCard, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetRateCard(ctx, db.GetRateCardParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("rate_card", id, err)
	}
	return rateCardOf(db.RateCard{
		OrganizationID: row.OrganizationID, ID: row.ID, ProductID: row.ProductID, ProductFilterID: row.ProductFilterID,
		Code: row.Code, Name: row.Name, Description: row.Description, Currency: row.Currency, BillingTiming: row.BillingTiming,
		Proration: row.Proration, DisplayOnInvoice: row.DisplayOnInvoice, RegroupPaidFees: row.RegroupPaidFees,
		PricingUnitID: row.PricingUnitID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
	}, row.TaxIds)
}

func (r rateCardRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.RateCard, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetRateCardByCode(ctx, db.GetRateCardByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("rate_card", code, err)
	}
	return rateCardOf(db.RateCard{
		OrganizationID: row.OrganizationID, ID: row.ID, ProductID: row.ProductID, ProductFilterID: row.ProductFilterID,
		Code: row.Code, Name: row.Name, Description: row.Description, Currency: row.Currency, BillingTiming: row.BillingTiming,
		Proration: row.Proration, DisplayOnInvoice: row.DisplayOnInvoice, RegroupPaidFees: row.RegroupPaidFees,
		PricingUnitID: row.PricingUnitID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
	}, row.TaxIds)
}

func (r rateCardRepo) List(ctx context.Context, org core.OrganizationID, f catalog.RateCardQuery, page core.PageRequest) (core.Page[*catalog.RateCard], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*catalog.RateCard]{}, err
	}
	after, afterAt, lim, err := timeCursorArgs(page)
	if err != nil {
		return core.Page[*catalog.RateCard]{}, err
	}
	rows, err := q.ListRateCards(ctx, db.ListRateCardsParams{
		OrganizationID: org.UUID(), Search: searchArg(f.Search), ProductIds: nonNil(uuids(f.ProductIDs)),
		ProductFilterIds: nonNil(uuids(f.ProductFilterIDs)), CategoryIds: nonNil(uuids(f.ProductCategoryIDs)),
		WithoutCategory: f.WithoutProductCategory, After: after, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*catalog.RateCard]{}, fmt.Errorf("list rate cards: %w", mapErr(err))
	}
	return pageOf(rows, page, func(row db.ListRateCardsRow) (*catalog.RateCard, error) {
		return rateCardOf(db.RateCard{
			OrganizationID: row.OrganizationID, ID: row.ID, ProductID: row.ProductID, ProductFilterID: row.ProductFilterID,
			Code: row.Code, Name: row.Name, Description: row.Description, Currency: row.Currency, BillingTiming: row.BillingTiming,
			Proration: row.Proration, DisplayOnInvoice: row.DisplayOnInvoice, RegroupPaidFees: row.RegroupPaidFees,
			PricingUnitID: row.PricingUnitID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
		}, row.TaxIds)
	}, func(c *catalog.RateCard) string { return recentCursor(c.CreatedAt, c.ID.UUID()) })
}

// rateRepo implements catalog.RateRepository.
type rateRepo struct{ t *txState }

func rateOf(r db.RateCardRate) (*catalog.Rate, error) {
	model, err := catalog.ParseChargeModel(r.RateModel)
	if err != nil {
		return nil, fmt.Errorf("rate %s: %w", r.ID, err)
	}
	unit, err := catalog.ParseIntervalUnit(r.BillingIntervalUnit)
	if err != nil {
		return nil, fmt.Errorf("rate %s: %w", r.ID, err)
	}
	props, err := catalog.NewProperties(r.Properties)
	if err != nil {
		return nil, fmt.Errorf("rate %s: %w", r.ID, err)
	}
	return &catalog.Rate{
		ID:                        idOf[core.RateCardRateID](r.ID),
		OrganizationID:            idOf[core.OrganizationID](r.OrganizationID),
		RateCardID:                idOf[core.RateCardID](r.RateCardID),
		Code:                      r.Code,
		EffectiveFrom:             utc(r.EffectiveFrom),
		RateModel:                 model,
		Properties:                props,
		MinAmount:                 r.MinAmount,
		BillingIntervalCount:      int(r.BillingIntervalCount),
		BillingIntervalUnit:       unit,
		PricingUnitConversionRate: r.PricingUnitConversionRate,
		CreatedAt:                 utc(r.CreatedAt),
		UpdatedAt:                 utc(r.UpdatedAt),
		DeletedAt:                 optUTC(r.DeletedAt),
	}, nil
}

func (r rateRepo) Create(ctx context.Context, x *catalog.Rate) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateRateCardRate(ctx, db.CreateRateCardRateParams{
		OrganizationID: x.OrganizationID.UUID(), ID: x.ID.UUID(), RateCardID: x.RateCardID.UUID(), Code: x.Code,
		EffectiveFrom: utc(x.EffectiveFrom), RateModel: string(x.RateModel), Properties: x.Properties.Raw(),
		MinAmount: x.MinAmount, BillingIntervalCount: int32(x.BillingIntervalCount), //nolint:gosec // validated ≤ 1000
		BillingIntervalUnit: x.BillingIntervalUnit.String(), PricingUnitConversionRate: x.PricingUnitConversionRate,
		CreatedAt: utc(x.CreatedAt), UpdatedAt: utc(x.UpdatedAt),
	})
	return insertErr("rate", n, err)
}

func (r rateRepo) Update(ctx context.Context, x *catalog.Rate) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdateRateCardRate(ctx, db.UpdateRateCardRateParams{
		OrganizationID: x.OrganizationID.UUID(), ID: x.ID.UUID(), Code: x.Code,
		EffectiveFrom: utc(x.EffectiveFrom), RateModel: string(x.RateModel), Properties: x.Properties.Raw(),
		MinAmount: x.MinAmount, BillingIntervalCount: int32(x.BillingIntervalCount), //nolint:gosec // validated ≤ 1000
		BillingIntervalUnit: x.BillingIntervalUnit.String(), PricingUnitConversionRate: x.PricingUnitConversionRate,
		UpdatedAt: utc(x.UpdatedAt),
	})
	return updateErr("rate", x.ID, n, err)
}

func (r rateRepo) Discard(ctx context.Context, org core.OrganizationID, id core.RateCardRateID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DiscardRateCardRate(ctx, db.DiscardRateCardRateParams{OrganizationID: org.UUID(), ID: id.UUID(), At: optUTC(&at)})
	return updateErr("rate", id, n, err)
}

func (r rateRepo) Get(ctx context.Context, org core.OrganizationID, id core.RateCardRateID) (*catalog.Rate, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetRateCardRate(ctx, db.GetRateCardRateParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("rate", id, err)
	}
	return rateOf(row)
}

func (r rateRepo) ListByRateCard(ctx context.Context, org core.OrganizationID, card core.RateCardID) ([]*catalog.Rate, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRateCardRates(ctx, db.ListRateCardRatesParams{OrganizationID: org.UUID(), RateCardID: card.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list rates: %w", mapErr(err))
	}
	return convAll(rows, rateOf)
}

// planRateCardRepo implements catalog.PlanRateCardRepository.
type planRateCardRepo struct{ t *txState }

func planRateCardOf(r db.PlanRateCard) (*catalog.PlanRateCard, error) {
	return &catalog.PlanRateCard{
		ID:             idOf[core.PlanRateCardID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		PlanID:         idOf[core.PlanID](r.PlanID),
		RateCardID:     idOf[core.RateCardID](r.RateCardID),
		Units:          r.Units,
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
		DeletedAt:      optUTC(r.DeletedAt),
	}, nil
}

func ratePhaseOf(r db.RatePhase) (*catalog.RatePhase, error) {
	p := &catalog.RatePhase{
		ID:                        idOf[core.RatePhaseID](r.ID),
		OrganizationID:            idOf[core.OrganizationID](r.OrganizationID),
		PlanRateCardID:            idOf[core.PlanRateCardID](r.PlanRateCardID),
		Code:                      r.Code,
		Position:                  int(r.Position),
		Name:                      r.Name,
		BillingIntervalCycleCount: optInt(r.BillingIntervalCycleCount),
		CreatedAt:                 utc(r.CreatedAt),
		UpdatedAt:                 utc(r.UpdatedAt),
		DeletedAt:                 optUTC(r.DeletedAt),
	}
	if r.OverrideRateModel == nil {
		return p, nil
	}
	model, err := catalog.ParseChargeModel(*r.OverrideRateModel)
	if err != nil {
		return nil, fmt.Errorf("rate phase %s: %w", r.ID, err)
	}
	props, err := catalog.NewProperties(r.OverrideProperties)
	if err != nil {
		return nil, fmt.Errorf("rate phase %s: %w", r.ID, err)
	}
	o := &catalog.RateOverride{
		RateModel:                 model,
		Properties:                props,
		MinAmount:                 deref(r.OverrideMinAmount),
		BillingIntervalCount:      optInt(r.OverrideBillingIntervalCount),
		PricingUnitConversionRate: r.OverridePricingUnitConversionRate,
	}
	if r.OverrideBillingIntervalUnit != nil {
		if o.BillingIntervalUnit, err = catalog.ParseIntervalUnit(*r.OverrideBillingIntervalUnit); err != nil {
			return nil, fmt.Errorf("rate phase %s: %w", r.ID, err)
		}
	}
	p.Override = o
	return p, nil
}

// writePhases makes phases the kept phases of the plan rate card id: the
// kept ones missing from phases are discarded at at, the others are
// parked past every position and rewritten, and the new ones inserted.
func (r planRateCardRepo) writePhases(ctx context.Context, q *db.Queries, org core.OrganizationID, id core.PlanRateCardID, phases []*catalog.RatePhase, at time.Time) error {
	rows, err := q.ListRatePhases(ctx, db.ListRatePhasesParams{OrganizationID: org.UUID(), PlanRateCardID: id.UUID()})
	if err != nil {
		return fmt.Errorf("plan rate card %s phases: %w", id, mapErr(err))
	}
	existing := make(map[uuid.UUID]bool, len(rows))
	for _, row := range rows {
		existing[row.ID] = true
	}
	keep := []uuid.UUID{}
	for _, p := range phases {
		if existing[p.ID.UUID()] {
			keep = append(keep, p.ID.UUID())
		}
	}
	if err := q.DiscardRatePhases(ctx, db.DiscardRatePhasesParams{
		OrganizationID: org.UUID(), PlanRateCardID: id.UUID(), Keep: keep, At: optUTC(&at),
	}); err != nil {
		return fmt.Errorf("plan rate card %s phases: %w", id, mapErr(err))
	}
	if err := q.ParkRatePhases(ctx, db.ParkRatePhasesParams{OrganizationID: org.UUID(), PlanRateCardID: id.UUID(), Ids: keep}); err != nil {
		return fmt.Errorf("plan rate card %s phases: %w", id, mapErr(err))
	}
	for _, p := range phases {
		arg := db.InsertRatePhaseParams{
			OrganizationID: org.UUID(), ID: p.ID.UUID(), PlanRateCardID: id.UUID(), Code: p.Code,
			Position: int32(p.Position), Name: p.Name, //nolint:gosec // a handful of phases
			BillingIntervalCycleCount: optInt32(p.BillingIntervalCycleCount),
			CreatedAt:                 utc(p.CreatedAt), UpdatedAt: utc(p.UpdatedAt),
		}
		if o := p.Override; o != nil {
			model := string(o.RateModel)
			arg.OverrideRateModel = &model
			arg.OverrideProperties = o.Properties.Raw()
			arg.OverrideMinAmount = &o.MinAmount
			arg.OverrideBillingIntervalCount = optInt32(o.BillingIntervalCount)
			arg.OverrideBillingIntervalUnit = optString(o.BillingIntervalUnit.String())
			arg.OverridePricingUnitConversionRate = o.PricingUnitConversionRate
		}
		if !existing[p.ID.UUID()] {
			n, err := q.InsertRatePhase(ctx, arg)
			if err := insertErr("rate_phase", n, err); err != nil {
				return err
			}
			continue
		}
		n, err := q.UpdateRatePhase(ctx, db.UpdateRatePhaseParams{
			OrganizationID: arg.OrganizationID, ID: arg.ID, PlanRateCardID: arg.PlanRateCardID, Code: arg.Code,
			Position: arg.Position, Name: arg.Name, BillingIntervalCycleCount: arg.BillingIntervalCycleCount,
			OverrideRateModel: arg.OverrideRateModel, OverrideProperties: arg.OverrideProperties,
			OverrideMinAmount: arg.OverrideMinAmount, OverrideBillingIntervalCount: arg.OverrideBillingIntervalCount,
			OverrideBillingIntervalUnit:       arg.OverrideBillingIntervalUnit,
			OverridePricingUnitConversionRate: arg.OverridePricingUnitConversionRate, UpdatedAt: arg.UpdatedAt,
		})
		if err := updateErr("rate_phase", p.ID, n, err); err != nil {
			return err
		}
	}
	return nil
}

func (r planRateCardRepo) Create(ctx context.Context, p *catalog.PlanRateCard, phases []*catalog.RatePhase) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreatePlanRateCard(ctx, db.CreatePlanRateCardParams{
		OrganizationID: p.OrganizationID.UUID(), ID: p.ID.UUID(), PlanID: p.PlanID.UUID(), RateCardID: p.RateCardID.UUID(),
		Units: p.Units, CreatedAt: utc(p.CreatedAt), UpdatedAt: utc(p.UpdatedAt),
	})
	if err := insertErr("plan_rate_card", n, err); err != nil {
		return err
	}
	return r.writePhases(ctx, q, p.OrganizationID, p.ID, phases, p.CreatedAt)
}

func (r planRateCardRepo) Update(ctx context.Context, p *catalog.PlanRateCard) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdatePlanRateCard(ctx, db.UpdatePlanRateCardParams{
		OrganizationID: p.OrganizationID.UUID(), ID: p.ID.UUID(), Units: p.Units, UpdatedAt: utc(p.UpdatedAt),
	})
	return updateErr("plan_rate_card", p.ID, n, err)
}

func (r planRateCardRepo) Discard(ctx context.Context, org core.OrganizationID, id core.PlanRateCardID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DiscardPlanRateCard(ctx, db.DiscardPlanRateCardParams{OrganizationID: org.UUID(), ID: id.UUID(), At: optUTC(&at)})
	if err := updateErr("plan_rate_card", id, n, err); err != nil {
		return err
	}
	return discardPhases(ctx, q, org, id.UUID(), at)
}

func (r planRateCardRepo) DiscardByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID, at time.Time) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	ids, err := q.DiscardPlanRateCardsOfPlan(ctx, db.DiscardPlanRateCardsOfPlanParams{OrganizationID: org.UUID(), PlanID: plan.UUID(), At: optUTC(&at)})
	if err != nil {
		return 0, fmt.Errorf("discard plan rate cards of plan %s: %w", plan, mapErr(err))
	}
	for _, id := range ids {
		if err := discardPhases(ctx, q, org, id, at); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// discardPhases discards every kept phase of the plan rate card id, with
// its override.
func discardPhases(ctx context.Context, q *db.Queries, org core.OrganizationID, id uuid.UUID, at time.Time) error {
	if err := q.DiscardRatePhases(ctx, db.DiscardRatePhasesParams{
		OrganizationID: org.UUID(), PlanRateCardID: id, Keep: []uuid.UUID{}, At: optUTC(&at),
	}); err != nil {
		return fmt.Errorf("discard phases of plan rate card %s: %w", id, mapErr(err))
	}
	return nil
}

func (r planRateCardRepo) Get(ctx context.Context, org core.OrganizationID, id core.PlanRateCardID) (*catalog.PlanRateCard, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPlanRateCard(ctx, db.GetPlanRateCardParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("plan_rate_card", id, err)
	}
	return planRateCardOf(row)
}

func (r planRateCardRepo) GetByRateCard(ctx context.Context, org core.OrganizationID, plan core.PlanID, card core.RateCardID) (*catalog.PlanRateCard, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPlanRateCardByRateCard(ctx, db.GetPlanRateCardByRateCardParams{
		OrganizationID: org.UUID(), PlanID: plan.UUID(), RateCardID: card.UUID(),
	})
	if err != nil {
		return nil, getErr("plan_rate_card", card, err)
	}
	return planRateCardOf(row)
}

func (r planRateCardRepo) List(ctx context.Context, org core.OrganizationID, f catalog.PlanRateCardQuery, page core.PageRequest) (core.Page[*catalog.PlanRateCard], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*catalog.PlanRateCard]{}, err
	}
	after, afterAt, lim, err := timeCursorArgs(page)
	if err != nil {
		return core.Page[*catalog.PlanRateCard]{}, err
	}
	rows, err := q.ListPlanRateCards(ctx, db.ListPlanRateCardsParams{
		OrganizationID: org.UUID(), PlanID: optUUID(f.PlanID), RateCardID: optUUID(f.RateCardID),
		ProductID: optUUID(f.ProductID), ProductFilterID: optUUID(f.ProductFilterID), CategoryID: optUUID(f.ProductCategoryID),
		After: after, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*catalog.PlanRateCard]{}, fmt.Errorf("list plan rate cards: %w", mapErr(err))
	}
	return pageOf(rows, page, planRateCardOf, func(p *catalog.PlanRateCard) string { return recentCursor(p.CreatedAt, p.ID.UUID()) })
}

func (r planRateCardRepo) PlanIDs(ctx context.Context, org core.OrganizationID, f catalog.PlanRateCardQuery) ([]core.PlanID, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	ids, err := q.ListPlanRateCardPlanIDs(ctx, db.ListPlanRateCardPlanIDsParams{
		OrganizationID: org.UUID(), PlanID: optUUID(f.PlanID), RateCardID: optUUID(f.RateCardID),
		ProductID: optUUID(f.ProductID), ProductFilterID: optUUID(f.ProductFilterID), CategoryID: optUUID(f.ProductCategoryID),
	})
	if err != nil {
		return nil, fmt.Errorf("list plan rate card plans: %w", mapErr(err))
	}
	return idsOf[core.PlanID](ids), nil
}

func (r planRateCardRepo) ListPhases(ctx context.Context, org core.OrganizationID, id core.PlanRateCardID) ([]*catalog.RatePhase, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRatePhases(ctx, db.ListRatePhasesParams{OrganizationID: org.UUID(), PlanRateCardID: id.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list rate phases: %w", mapErr(err))
	}
	return convAll(rows, ratePhaseOf)
}

func (r planRateCardRepo) SavePhases(ctx context.Context, org core.OrganizationID, id core.PlanRateCardID, phases []*catalog.RatePhase, at time.Time) error {
	p, err := r.Get(ctx, org, id)
	if err != nil {
		return err
	}
	if p.DeletedAt != nil {
		return notFound("plan_rate_card", id)
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	return r.writePhases(ctx, q, org, id, phases, at)
}
