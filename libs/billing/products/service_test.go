package products_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/metrics"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*billingtest.Harness, *products.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	s, err := products.New(h.Deps)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, s
}

func ptr[T any](v T) *T { return &v }

func wantFieldError(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if !ve.Has(field, code) {
		t.Fatalf("errors = %v, want %s %s", ve.Errors, field, code)
	}
}

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error = %v, want %s_not_found", err, resource)
	}
}

// fixture is a metered product on a metric filtered by region, with a
// category.
type fixture struct {
	h        *billingtest.Harness
	s        *products.Service
	metric   *catalog.BillableMetric
	region   catalog.MetricFilter
	category *catalog.ProductCategory
	product  *catalog.Product
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h, s := setup(t)
	f := &fixture{h: h, s: s}
	f.region = catalog.MetricFilter{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "region", Values: []string{"us", "eu"}}
	f.metric = h.Metric("api_calls", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{f.region}
	})
	var err error
	if f.category, err = s.CreateCategory(t.Context(), h.OrgID(), products.CategoryInput{Code: "compute", Name: "Compute"}); err != nil {
		t.Fatalf("CreateCategory() error = %v", err)
	}
	if f.product, err = s.CreateProduct(t.Context(), h.OrgID(), products.ProductInput{
		Code: "api", Name: "API", ProductType: catalog.ProductTypeMetered,
		Category: refs.ByID(f.category.ID), Metric: refs.ByID(f.metric.ID),
	}); err != nil {
		t.Fatalf("CreateProduct() error = %v", err)
	}
	return f
}

func (f *fixture) filter(t *testing.T, code string, values ...string) *catalog.ProductFilter {
	t.Helper()
	in := products.FilterInput{Code: code, Name: code}
	for _, v := range values {
		in.Values = append(in.Values, products.FilterValueInput{Key: "region", Value: ptr(v)})
	}
	pf, err := f.s.CreateFilter(t.Context(), f.h.OrgID(), f.product.ID, in)
	if err != nil {
		t.Fatalf("CreateFilter() error = %v", err)
	}
	return pf
}

func (f *fixture) card(t *testing.T, code string, opts ...func(*products.RateCardInput)) *catalog.RateCard {
	t.Helper()
	in := products.RateCardInput{Product: refs.ByID(f.product.ID), Code: code, Name: code, Currency: billingtest.USD}
	for _, o := range opts {
		o(&in)
	}
	c, err := f.s.CreateRateCard(t.Context(), f.h.OrgID(), in)
	if err != nil {
		t.Fatalf("CreateRateCard() error = %v", err)
	}
	return c
}

func standard(t *testing.T, code string, from time.Time) products.RateInput {
	return products.RateInput{
		Code: code, EffectiveFrom: from, RateModel: catalog.ChargeModelStandard,
		Properties: props(t, `{"amount":"1"}`), BillingIntervalUnit: catalog.IntervalUnitMonth,
	}
}

// attach applies the card to a new USD plan and returns the plan.
func (f *fixture) attach(t *testing.T, c *catalog.RateCard) *catalog.Plan {
	t.Helper()
	plan := f.h.Plan(period.Monthly, 0)
	if _, err := f.s.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), products.PlanRateCardInput{RateCard: refs.ByID(c.ID)}); err != nil {
		t.Fatalf("CreatePlanRateCard() error = %v", err)
	}
	return plan
}

func TestCategories(t *testing.T) {
	t.Parallel()
	// source: spec/services/product_categories/create_service_spec.rb:18
	// source: spec/services/product_categories/create_service_spec.rb:46
	// source: spec/services/product_categories/create_service_spec.rb:55
	t.Run("creates a category with a unique code and a name", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if f.category.Code != "compute" || f.category.Name != "Compute" {
			t.Errorf("category = %+v, want compute", f.category)
		}
		_, err := f.s.CreateCategory(t.Context(), f.h.OrgID(), products.CategoryInput{Code: " compute ", Name: "Other"})
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
		_, err = f.s.CreateCategory(t.Context(), f.h.OrgID(), products.CategoryInput{Code: "storage"})
		wantFieldError(t, err, "name", core.CodeRequired)
	})
	// source: spec/services/product_categories/update_service_spec.rb:23
	// source: spec/services/product_categories/update_service_spec.rb:34
	// source: spec/services/product_categories/update_service_spec.rb:40
	t.Run("locks the code once a plan prices one of its products", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		got, err := f.s.UpdateCategory(t.Context(), f.h.OrgID(), f.category.ID, products.CategoryUpdate{Code: core.Some("compute-2")})
		if err != nil || got.Code != "compute-2" {
			t.Fatalf("UpdateCategory() = %+v, %v, want the new code", got, err)
		}
		f.attach(t, f.card(t, "api-usd"))
		_, err = f.s.UpdateCategory(t.Context(), f.h.OrgID(), f.category.ID, products.CategoryUpdate{Code: core.Some("compute-3")})
		wantFieldError(t, err, "code", catalog.CodeAttachedToPlanOrSubscription)
		got, err = f.s.UpdateCategory(t.Context(), f.h.OrgID(), f.category.ID, products.CategoryUpdate{Code: core.Some("compute-2"), Name: core.Some("Renamed")})
		if err != nil || got.Name != "Renamed" {
			t.Errorf("UpdateCategory() = %+v, %v, want the unchanged code accepted", got, err)
		}
	})
	// source: spec/services/product_categories/destroy_service_spec.rb:18
	// source: spec/services/product_categories/destroy_service_spec.rb:48
	t.Run("deletes the category with its products unless a plan prices one", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		plan := f.attach(t, c)
		_, err := f.s.DeleteCategory(t.Context(), f.h.OrgID(), f.category.ID)
		wantFieldError(t, err, "product_category", catalog.CodeAttachedToPlanOrSubscription)
		if _, err := f.s.DeletePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), refs.ByCode("api-usd")); err != nil {
			t.Fatalf("DeletePlanRateCard() error = %v", err)
		}
		if _, err := f.s.DeleteCategory(t.Context(), f.h.OrgID(), f.category.ID); err != nil {
			t.Fatalf("DeleteCategory() error = %v", err)
		}
		_, err = f.s.Product(t.Context(), f.h.OrgID(), refs.ByID(f.product.ID))
		wantNotFound(t, err, "product")
	})
}

func TestProducts(t *testing.T) {
	t.Parallel()
	// source: spec/services/products/create_service_spec.rb:24
	// source: spec/services/products/create_service_spec.rb:43
	t.Run("creates metered and fixed products", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if f.product.BillableMetricID != f.metric.ID || f.product.ProductCategoryID != f.category.ID {
			t.Errorf("product = %+v, want the metric and category", f.product)
		}
		p, err := f.s.CreateProduct(t.Context(), f.h.OrgID(), products.ProductInput{Code: "seat", Name: "Seat", ProductType: catalog.ProductTypeFixed})
		if err != nil || !p.BillableMetricID.IsZero() || !p.ProductCategoryID.IsZero() {
			t.Errorf("CreateProduct() = %+v, %v, want a standalone fixed product", p, err)
		}
	})
	// source: spec/services/products/create_service_spec.rb:63
	// source: spec/services/products/create_service_spec.rb:72
	// source: spec/services/products/create_service_spec.rb:81
	// source: spec/services/products/create_service_spec.rb:90
	t.Run("rejects missing references, a metered product without metric and a used code", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		_, err := f.s.CreateProduct(t.Context(), f.h.OrgID(), products.ProductInput{Code: "x", Name: "X", ProductType: catalog.ProductTypeFixed, Category: refs.ByCode("nope")})
		wantNotFound(t, err, "product_category")
		_, err = f.s.CreateProduct(t.Context(), f.h.OrgID(), products.ProductInput{Code: "x", Name: "X", ProductType: catalog.ProductTypeMetered, Metric: refs.ByCode("nope")})
		wantNotFound(t, err, "billable_metric")
		_, err = f.s.CreateProduct(t.Context(), f.h.OrgID(), products.ProductInput{Code: "x", Name: "X", ProductType: catalog.ProductTypeMetered})
		wantFieldError(t, err, "billable_metric", core.CodeRequired)
		_, err = f.s.CreateProduct(t.Context(), f.h.OrgID(), products.ProductInput{Code: "api", Name: "X", ProductType: catalog.ProductTypeFixed})
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
	})
	// source: spec/services/products/update_service_spec.rb:24
	// source: spec/services/products/update_service_spec.rb:30
	// source: spec/services/products/update_service_spec.rb:42
	// source: spec/services/products/update_service_spec.rb:48
	t.Run("locks the code and category once a plan prices the product", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		_, err := f.s.UpdateProduct(t.Context(), f.h.OrgID(), f.product.ID, products.ProductUpdate{Category: core.Some(refs.ByCode("nope"))})
		wantNotFound(t, err, "product_category")
		got, err := f.s.UpdateProduct(t.Context(), f.h.OrgID(), f.product.ID, products.ProductUpdate{Code: core.Some("api-2"), Category: core.Some(refs.Ref{})})
		if err != nil || got.Code != "api-2" || !got.ProductCategoryID.IsZero() {
			t.Fatalf("UpdateProduct() = %+v, %v, want the new code without category", got, err)
		}
		f.attach(t, f.card(t, "api-usd"))
		_, err = f.s.UpdateProduct(t.Context(), f.h.OrgID(), f.product.ID, products.ProductUpdate{Code: core.Some("api-3")})
		wantFieldError(t, err, "code", catalog.CodeAttachedToPlanOrSubscription)
		_, err = f.s.UpdateProduct(t.Context(), f.h.OrgID(), f.product.ID, products.ProductUpdate{Category: core.Some(refs.ByID(f.category.ID))})
		wantFieldError(t, err, "product_category", catalog.CodeAttachedToPlanOrSubscription)
		got, err = f.s.UpdateProduct(t.Context(), f.h.OrgID(), f.product.ID, products.ProductUpdate{Code: core.Some("api-2"), Name: core.Some("Renamed")})
		if err != nil || got.Name != "Renamed" {
			t.Errorf("UpdateProduct() = %+v, %v, want unchanged structural fields accepted", got, err)
		}
	})
	// source: spec/services/products/destroy_service_spec.rb:24
	// source: spec/services/products/destroy_service_spec.rb:56
	t.Run("deletes the product with its filters and cards unless a plan prices it", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pf := f.filter(t, "eu", "eu")
		c := f.card(t, "api-usd", func(in *products.RateCardInput) { in.Rates = []products.RateInput{standard(t, "r1", now)} })
		plan := f.attach(t, c)
		_, err := f.s.DeleteProduct(t.Context(), f.h.OrgID(), f.product.ID)
		wantFieldError(t, err, "product", catalog.CodeAttachedToPlanOrSubscription)
		if _, err := f.s.DeletePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), refs.ByCode(c.Code)); err != nil {
			t.Fatalf("DeletePlanRateCard() error = %v", err)
		}
		if _, err := f.s.DeleteProduct(t.Context(), f.h.OrgID(), f.product.ID); err != nil {
			t.Fatalf("DeleteProduct() error = %v", err)
		}
		_, err = f.s.RateCard(t.Context(), f.h.OrgID(), refs.ByID(c.ID))
		wantNotFound(t, err, "rate_card")
		_, err = f.s.Filter(t.Context(), f.h.OrgID(), f.product.ID, refs.ByID(pf.ID))
		wantNotFound(t, err, "product_filter")
	})
}

func TestFilters(t *testing.T) {
	t.Parallel()
	// source: spec/services/product_filters/create_service_spec.rb:27
	// source: spec/services/product_filters/create_service_spec.rb:51
	t.Run("creates a filter, resolving keys to the metric's filters", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pf := f.filter(t, "eu", "eu")
		if len(pf.Values) != 1 || pf.Values[0].BillableMetricFilterID != f.region.ID || *pf.Values[0].Value != "eu" {
			t.Errorf("values = %+v, want region eu", pf.Values)
		}
		n, err := f.s.FiltersCount(t.Context(), f.product)
		if err != nil || n != 1 {
			t.Errorf("FiltersCount() = %d, %v, want = 1", n, err)
		}
	})
	// source: spec/services/product_filters/create_service_spec.rb:59
	// source: spec/services/product_filters/create_service_spec.rb:141
	// source: spec/services/product_filters/create_service_spec.rb:216
	// source: spec/services/product_filters/create_service_spec.rb:225
	t.Run("rejects unknown keys, no values, unknown values and a used code", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.filter(t, "eu", "eu")
		create := func(code string, values ...products.FilterValueInput) error {
			_, err := f.s.CreateFilter(t.Context(), f.h.OrgID(), f.product.ID, products.FilterInput{Code: code, Name: code, Values: values})
			return err
		}
		wantFieldError(t, create("x", products.FilterValueInput{Key: "zone", Value: ptr("a")}), "values.key", core.CodeInvalid)
		wantFieldError(t, create("x"), "values", core.CodeRequired)
		wantFieldError(t, create("x", products.FilterValueInput{Key: "region", Value: ptr("asia")}), "values.value", core.CodeInvalid)
		wantFieldError(t, create("eu", products.FilterValueInput{Key: "region", Value: ptr("us")}), "code", core.CodeAlreadyExists)
	})
	// source: spec/services/product_filters/create_service_spec.rb:75
	// source: spec/services/product_filters/create_service_spec.rb:112
	// source: spec/services/product_filters/create_service_spec.rb:197
	t.Run("selects every value of a key-only entry, alone", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		all, err := f.s.CreateFilter(t.Context(), f.h.OrgID(), f.product.ID, products.FilterInput{Code: "all", Name: "All", Values: []products.FilterValueInput{{Key: "region"}}})
		if err != nil || len(all.Values) != 1 || all.Values[0].Value != nil {
			t.Fatalf("CreateFilter() = %+v, %v, want a key-only value", all, err)
		}
		_, err = f.s.CreateFilter(t.Context(), f.h.OrgID(), f.product.ID, products.FilterInput{Code: "mix", Name: "Mix", Values: []products.FilterValueInput{{Key: "region"}, {Key: "region", Value: ptr("eu")}}})
		wantFieldError(t, err, "values", "key_only_conflicts_with_values")
		_, err = f.s.CreateFilter(t.Context(), f.h.OrgID(), f.product.ID, products.FilterInput{Code: "all-2", Name: "All", Values: []products.FilterValueInput{{Key: "region"}}})
		wantFieldError(t, err, "values", core.CodeAlreadyExists)
		f.filter(t, "eu", "eu")
	})
	// source: spec/services/product_filters/create_service_spec.rb:166
	// source: spec/services/product_filters/create_service_spec.rb:172
	// source: spec/services/product_filters/create_service_spec.rb:182
	t.Run("rejects a value set another filter of the product uses, in any order", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.filter(t, "both", "us", "eu")
		_, err := f.s.CreateFilter(t.Context(), f.h.OrgID(), f.product.ID, products.FilterInput{Code: "again", Name: "Again", Values: []products.FilterValueInput{
			{Key: "region", Value: ptr("eu")}, {Key: "region", Value: ptr("us")},
		}})
		wantFieldError(t, err, "values", core.CodeAlreadyExists)
		f.filter(t, "eu", "eu")
	})
	// source: spec/services/product_filters/create_service_spec.rb:132
	t.Run("rejects a filter on a fixed product", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		p, err := f.s.CreateProduct(t.Context(), f.h.OrgID(), products.ProductInput{Code: "seat", Name: "Seat", ProductType: catalog.ProductTypeFixed})
		if err != nil {
			t.Fatalf("CreateProduct() error = %v", err)
		}
		_, err = f.s.CreateFilter(t.Context(), f.h.OrgID(), p.ID, products.FilterInput{Code: "x", Name: "X", Values: []products.FilterValueInput{{Key: "region"}}})
		wantFieldError(t, err, "product", catalog.CodeNotAllowedForProductType)
	})
	// source: spec/services/product_filters/update_service_spec.rb:48
	// source: spec/services/product_filters/update_service_spec.rb:54
	// source: spec/services/product_filters/update_service_spec.rb:81
	t.Run("locks the code once attached and replaces the values", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pf := f.filter(t, "eu", "eu")
		f.attach(t, f.card(t, "api-eu", func(in *products.RateCardInput) { in.Filter = refs.ByID(pf.ID) }))
		_, err := f.s.UpdateFilter(t.Context(), f.h.OrgID(), f.product.ID, pf.ID, products.FilterUpdate{Code: core.Some("eu-2")})
		wantFieldError(t, err, "code", catalog.CodeAttachedToPlanOrSubscription)
		got, err := f.s.UpdateFilter(t.Context(), f.h.OrgID(), f.product.ID, pf.ID, products.FilterUpdate{Name: core.Some("Europe")})
		if err != nil || got.Name != "Europe" {
			t.Fatalf("UpdateFilter() = %+v, %v, want the new name", got, err)
		}
		us := []products.FilterValueInput{{Key: "region", Value: ptr("us")}}
		if got, err = f.s.UpdateFilter(t.Context(), f.h.OrgID(), f.product.ID, pf.ID, products.FilterUpdate{Values: core.Some(us)}); err != nil || *got.Values[0].Value != "us" {
			t.Fatalf("UpdateFilter() = %+v, %v, want the values replaced", got, err)
		}
		if _, err := f.s.UpdateFilter(t.Context(), f.h.OrgID(), f.product.ID, pf.ID, products.FilterUpdate{Values: core.Some(us)}); err != nil {
			t.Errorf("UpdateFilter() error = %v, want the current values accepted", err)
		}
	})
	// source: spec/services/product_filters/destroy_service_spec.rb:27
	// source: spec/services/product_filters/destroy_service_spec.rb:39
	t.Run("deletes the filter with its cards unless a plan prices the product", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pf := f.filter(t, "eu", "eu")
		scoped := f.card(t, "api-eu", func(in *products.RateCardInput) { in.Filter = refs.ByID(pf.ID) })
		other := f.card(t, "api-usd")
		plan := f.attach(t, other)
		_, err := f.s.DeleteFilter(t.Context(), f.h.OrgID(), f.product.ID, pf.ID)
		wantFieldError(t, err, "product_filter", catalog.CodeAttachedToPlanOrSubscription)
		if _, err := f.s.DeletePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), refs.ByCode(other.Code)); err != nil {
			t.Fatalf("DeletePlanRateCard() error = %v", err)
		}
		if _, err := f.s.DeleteFilter(t.Context(), f.h.OrgID(), f.product.ID, pf.ID); err != nil {
			t.Fatalf("DeleteFilter() error = %v", err)
		}
		_, err = f.s.RateCard(t.Context(), f.h.OrgID(), refs.ByID(scoped.ID))
		wantNotFound(t, err, "rate_card")
	})
}

// source: spec/services/billable_metrics/destroy_service_spec.rb:78
func TestMetricDeleteReferencedByProductFilter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.filter(t, "eu", "eu")
	ms, err := metrics.New(f.h.Deps, nil)
	if err != nil {
		t.Fatalf("metrics.New() error = %v", err)
	}
	_, err = ms.Delete(t.Context(), f.h.OrgID(), f.metric.ID)
	wantFieldError(t, err, "billable_metric", metrics.CodeReferencedByProductFilter)
	m := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*catalog.BillableMetric, error) {
		return r.Metrics.Get(ctx, f.h.OrgID(), f.metric.ID)
	})
	if m.DeletedAt != nil {
		t.Errorf("deleted_at = %v, want the metric kept", m.DeletedAt)
	}
}

// TestMetricFiltersReferencedByProductFilter ports the product filter
// references of the metric filter batch update.
func TestMetricFiltersReferencedByProductFilter(t *testing.T) {
	t.Parallel()
	region := func(values ...string) catalog.MetricFilter {
		return catalog.MetricFilter{Key: "region", Values: values}
	}
	cloud := catalog.MetricFilter{Key: "cloud", Values: []string{"aws", "gcp"}}
	tests := []struct {
		name       string
		source     string
		referenced *string
		filters    []catalog.MetricFilter
		wantErr    bool
		wantValues []string
	}{
		{"removing a referenced value", "spec/services/billable_metric_filters/create_or_update_batch_service_spec.rb:467", ptr("eu"), []catalog.MetricFilter{region("us", "apac")}, true, []string{"us", "eu", "apac"}},
		{"discarding a referenced filter", "spec/services/billable_metric_filters/create_or_update_batch_service_spec.rb:479", ptr("eu"), []catalog.MetricFilter{cloud}, true, []string{"us", "eu", "apac"}},
		{"discarding all filters", "spec/services/billable_metric_filters/create_or_update_batch_service_spec.rb:491", ptr("eu"), []catalog.MetricFilter{}, true, []string{"us", "eu", "apac"}},
		{"removing an unreferenced value", "spec/services/billable_metric_filters/create_or_update_batch_service_spec.rb:503", ptr("eu"), []catalog.MetricFilter{region("us", "eu")}, false, []string{"us", "eu"}},
		{"trimming a value of a whole-set reference", "spec/services/billable_metric_filters/create_or_update_batch_service_spec.rb:517", nil, []catalog.MetricFilter{region("us", "eu")}, false, []string{"us", "eu"}},
		{"discarding a whole-set referenced filter", "spec/services/billable_metric_filters/create_or_update_batch_service_spec.rb:528", nil, []catalog.MetricFilter{cloud}, true, []string{"us", "eu", "apac"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, s := setup(t)
			m := h.Metric("api_calls", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
				m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "region", Values: []string{"us", "eu", "apac"}}}
			})
			p, err := s.CreateProduct(t.Context(), h.OrgID(), products.ProductInput{
				Code: "api", Name: "API", ProductType: catalog.ProductTypeMetered, Metric: refs.ByID(m.ID),
			})
			if err != nil {
				t.Fatalf("CreateProduct() error = %v", err)
			}
			if _, err := s.CreateFilter(t.Context(), h.OrgID(), p.ID, products.FilterInput{
				Code: "f", Name: "f", Values: []products.FilterValueInput{{Key: "region", Value: tt.referenced}},
			}); err != nil {
				t.Fatalf("CreateFilter() error = %v", err)
			}
			ms, err := metrics.New(h.Deps, nil)
			if err != nil {
				t.Fatalf("metrics.New() error = %v", err)
			}
			_, err = ms.Update(t.Context(), h.OrgID(), m.ID, metrics.UpdateInput{Filters: core.Some(tt.filters)})
			if tt.wantErr {
				wantFieldError(t, err, "filters", metrics.CodeReferencedByProductFilter)
			} else if err != nil {
				t.Fatalf("Update() error = %v (%s)", err, tt.source)
			}
			got := billingtest.Read(h, func(ctx context.Context, r port.Repos) (*catalog.BillableMetric, error) {
				return r.Metrics.Get(ctx, h.OrgID(), m.ID)
			})
			i := slices.IndexFunc(got.Filters, func(f catalog.MetricFilter) bool { return f.Key == "region" })
			if i < 0 || !slices.Equal(got.Filters[i].Values, tt.wantValues) {
				t.Errorf("region filter = %v, want = %v (%s)", got.Filters, tt.wantValues, tt.source)
			}
		})
	}
}
