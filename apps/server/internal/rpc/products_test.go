package rpc_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// restProduct is the JSON of a product in a REST response.
type restProduct struct {
	ID                string `json:"id"`
	Code              string `json:"code"`
	Name              string `json:"name"`
	ProductType       string `json:"product_type"`
	ProductCategoryID string `json:"product_category_id"`
	BillableMetricID  string `json:"billable_metric_id"`
	FiltersCount      int    `json:"filters_count"`
}

// restProductFilter is the JSON of a product filter in a REST response.
type restProductFilter struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Values []struct {
		Key   string  `json:"key"`
		Value *string `json:"value"`
	} `json:"values"`
}

// restProductCategory is the JSON of a product category in a REST
// response.
type restProductCategory struct {
	ID            string `json:"id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	ProductsCount int    `json:"products_count"`
}

// catalogFixture is an organization with a metric filtered by region, a
// category "compute" and its metered product "api".
type catalogFixture struct {
	s        *rpctest.Server
	metric   *catalog.BillableMetric
	category restProductCategory
	product  restProduct
}

func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()
	s := rpctest.New(t)
	f := &catalogFixture{s: s}
	f.metric = s.Fixtures.Metric("api_calls", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](s.Fixtures.IDs), Key: "region", Values: []string{"us", "eu"}}}
	})
	f.category = decode[struct {
		ProductCategory restProductCategory `json:"product_category"`
	}](t, f.do(t, http.MethodPost, "/v1/product_categories", `{"code":"compute","name":"Compute"}`, http.StatusOK)).ProductCategory
	f.product = f.createProduct(t, `{"code":"api","name":"API","product_type":"PRODUCT_TYPE_METERED",
		"product_category_id":"code:compute","billable_metric_id":"code:api_calls"}`)
	return f
}

func (f *catalogFixture) do(t *testing.T, method, path string, body any, want int) []byte {
	t.Helper()
	status, out := f.s.REST(t, f.s.APIKey, method, path, body)
	if status != want {
		t.Fatalf("%s %s: got status = %d (%s), want = %d", method, path, status, out, want)
	}
	return out
}

// fail runs a request that must fail with status and a body naming each
// of want (a field path, a reason).
func (f *catalogFixture) fail(t *testing.T, method, path string, body any, status int, want ...string) {
	t.Helper()
	out := string(f.do(t, method, path, body, status))
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("%s %s: got body = %s, want it to name %q", method, path, out, w)
		}
	}
}

func (f *catalogFixture) createProduct(t *testing.T, body string) restProduct {
	t.Helper()
	return decode[struct {
		Product restProduct `json:"product"`
	}](t, f.do(t, http.MethodPost, "/v1/products", body, http.StatusOK)).Product
}

func (f *catalogFixture) createFilter(t *testing.T, code string, values ...string) restProductFilter {
	t.Helper()
	vals := make([]string, len(values))
	for i, v := range values {
		vals[i] = fmt.Sprintf(`{"key":"region","value":%q}`, v)
	}
	body := fmt.Sprintf(`{"code":%q,"name":%q,"values":[%s]}`, code, code, strings.Join(vals, ","))
	return decode[struct {
		Filter restProductFilter `json:"filter"`
	}](t, f.do(t, http.MethodPost, "/v1/products/"+f.product.ID+"/filters", body, http.StatusOK)).Filter
}

func unknownID[I interface {
	core.Identifier[I]
	String() string
}]() string {
	return core.NewID[I](core.UUIDv7Generator{}).String()
}

func TestProductCategoriesREST(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:22")
	if f.category.ID == "" || f.category.Code != "compute" || f.category.Name != "Compute" {
		t.Errorf("got category = %+v, want compute", f.category)
	}
	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:36")
	f.fail(t, http.MethodPost, "/v1/product_categories", `{"code":"compute","name":"Other"}`, http.StatusBadRequest,
		"product_category.code", core.CodeAlreadyExists)

	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:104")
	got := decode[struct {
		ProductCategory restProductCategory `json:"product_category"`
	}](t, f.do(t, http.MethodGet, "/v1/product_categories/code%3Acompute", nil, http.StatusOK)).ProductCategory
	if got.ID != f.category.ID || got.ProductsCount != 1 {
		t.Errorf("got category = %+v, want compute with 1 product", got)
	}
	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:118")
	f.do(t, http.MethodGet, "/v1/product_categories/code%3Aunknown", nil, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:52")
	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:63")
	got = decode[struct {
		ProductCategory restProductCategory `json:"product_category"`
	}](t, f.do(t, http.MethodPatch, "/v1/product_categories/code%3Acompute", `{"code":"cpu","name":"CPU"}`, http.StatusOK)).ProductCategory
	if got.Code != "cpu" || got.Name != "CPU" {
		t.Errorf("got category = %+v, want cpu", got)
	}
	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:89")
	f.do(t, http.MethodPatch, "/v1/product_categories/code%3Aunknown", `{"name":"x"}`, http.StatusNotFound)

	f.s.Clock.Advance(time.Minute)
	f.do(t, http.MethodPost, "/v1/product_categories", `{"code":"storage","name":"Storage"}`, http.StatusOK)
	tests := []struct {
		name, source, query string
		want                []string
		counts              []int
	}{
		{"all", "spec/requests/api/v2/product_categories_controller_spec.rb:143", "", []string{"storage", "cpu"}, []int{0, 1}},
		{"search", "spec/requests/api/v2/product_categories_controller_spec.rb:189", "?search=stor", []string{"storage"}, []int{0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			list := decode[struct {
				ProductCategories []restProductCategory `json:"product_categories"`
			}](t, f.do(t, http.MethodGet, "/v1/product_categories"+tt.query, nil, http.StatusOK)).ProductCategories
			var codes []string
			var counts []int
			for _, c := range list {
				codes, counts = append(codes, c.Code), append(counts, c.ProductsCount)
			}
			if !slices.Equal(codes, tt.want) || !slices.Equal(counts, tt.counts) {
				t.Errorf("got categories = %v %v, want = %v %v", codes, counts, tt.want, tt.counts)
			}
		})
	}

	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:205")
	f.do(t, http.MethodDelete, "/v1/product_categories/code%3Astorage", nil, http.StatusOK)
	f.do(t, http.MethodGet, "/v1/product_categories/code%3Astorage", nil, http.StatusNotFound)
	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:216")
	f.do(t, http.MethodDelete, "/v1/product_categories/code%3Astorage", nil, http.StatusNotFound)
}

func TestProductsREST(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:26")
	if f.product.ID == "" || f.product.ProductType != "PRODUCT_TYPE_METERED" ||
		f.product.ProductCategoryID != f.category.ID || f.product.BillableMetricID != f.metric.ID.String() {
		t.Errorf("got product = %+v, want the metered api product", f.product)
	}
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:41")
	f.s.Clock.Advance(time.Minute)
	fixed := f.createProduct(t, `{"code":"seat","name":"Seat","product_type":"PRODUCT_TYPE_FIXED"}`)
	if fixed.ProductCategoryID != "" || fixed.BillableMetricID != "" {
		t.Errorf("got product = %+v, want no category nor metric", fixed)
	}
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:54")
	f.do(t, http.MethodPost, "/v1/products", fmt.Sprintf(`{"code":"x","name":"x","product_type":"PRODUCT_TYPE_METERED","billable_metric_id":%q}`,
		unknownID[core.BillableMetricID]()), http.StatusNotFound)
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:64")
	f.fail(t, http.MethodPost, "/v1/products", `{"code":"x","name":"x","product_type":"PRODUCT_TYPE_METERED"}`, http.StatusBadRequest,
		"product.billable_metric", core.CodeRequired)

	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:181")
	got := decode[struct {
		Product restProduct `json:"product"`
	}](t, f.do(t, http.MethodGet, "/v1/products/code%3Aapi", nil, http.StatusOK)).Product
	if got.ID != f.product.ID {
		t.Errorf("got product = %+v, want api", got)
	}
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:195")
	f.do(t, http.MethodGet, "/v1/products/"+unknownID[core.ProductID](), nil, http.StatusNotFound)

	f.createFilter(t, "us", "us")
	tests := []struct {
		name, source, query string
		status              int
		want                []string
	}{
		{"all", "spec/requests/api/v2/products_controller_spec.rb:213", "", http.StatusOK, []string{"seat", "api"}},
		{"by type", "spec/requests/api/v2/products_controller_spec.rb:261", "?filter=" + url.QueryEscape("product_type = PRODUCT_TYPE_FIXED"), http.StatusOK, []string{"seat"}},
		{"by unknown type", "spec/requests/api/v2/products_controller_spec.rb:270", "?filter=" + url.QueryEscape("product_type = other"), http.StatusBadRequest, nil},
		{"by category", "spec/requests/api/v2/products_controller_spec.rb:282", "?filter=" + url.QueryEscape(`product_category_id = "code:compute"`), http.StatusOK, []string{"api"}},
		{"without category", "spec/requests/api/v2/products_controller_spec.rb:304", "?filter=" + url.QueryEscape("without_product_category = true"), http.StatusOK, []string{"seat"}},
		{
			"category or none", "spec/requests/api/v2/products_controller_spec.rb:314",
			"?filter=" + url.QueryEscape(`product_category_id = "code:compute" AND without_product_category = true`), http.StatusOK,
			[]string{"seat", "api"},
		},
		{"unknown category", "spec/requests/api/v2/products_controller_spec.rb:324", "?filter=" + url.QueryEscape(`product_category_id = "code:unknown"`), http.StatusNotFound, nil},
		{"search", "spec/requests/api/v2/products_controller_spec.rb:335", "?search=sea", http.StatusOK, []string{"seat"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			body := f.do(t, http.MethodGet, "/v1/products"+tt.query, nil, tt.status)
			if tt.status != http.StatusOK {
				return
			}
			var codes []string
			for _, p := range decode[struct {
				Products []restProduct `json:"products"`
			}](t, body).Products {
				codes = append(codes, p.Code)
				// source: spec/requests/api/v2/products_controller_spec.rb:248
				if p.Code == "api" && p.FiltersCount != 1 {
					t.Errorf("got filters_count = %d, want = 1", p.FiltersCount)
				}
			}
			if !slices.Equal(codes, tt.want) {
				t.Errorf("got products = %v, want = %v", codes, tt.want)
			}
		})
	}

	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:81")
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:92")
	got = decode[struct {
		Product restProduct `json:"product"`
	}](t, f.do(t, http.MethodPatch, "/v1/products/code%3Aseat", `{"code":"seats","name":"Seats"}`, http.StatusOK)).Product
	if got.Code != "seats" || got.Name != "Seats" {
		t.Errorf("got product = %+v, want seats", got)
	}
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:118")
	got = decode[struct {
		Product restProduct `json:"product"`
	}](t, f.do(t, http.MethodPatch, "/v1/products/code%3Aseats", `{"product_category_id":"code:compute"}`, http.StatusOK)).Product
	if got.ProductCategoryID != f.category.ID {
		t.Errorf("got product_category_id = %q, want = %q", got.ProductCategoryID, f.category.ID)
	}
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:128")
	f.do(t, http.MethodPatch, "/v1/products/code%3Aseats", `{"product_category_id":"code:unknown"}`, http.StatusNotFound)
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:153")
	got = decode[struct {
		Product restProduct `json:"product"`
	}](t, f.do(t, http.MethodPatch, "/v1/products/code%3Aseats?update_mask=productCategoryId", `{}`, http.StatusOK)).Product
	if got.ProductCategoryID != "" {
		t.Errorf("got product_category_id = %q, want none", got.ProductCategoryID)
	}
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:166")
	f.do(t, http.MethodPatch, "/v1/products/code%3Aunknown", `{"name":"x"}`, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:350")
	f.do(t, http.MethodDelete, "/v1/products/code%3Aseats", nil, http.StatusOK)
	f.do(t, http.MethodGet, "/v1/products/code%3Aseats", nil, http.StatusNotFound)
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:361")
	f.do(t, http.MethodDelete, "/v1/products/code%3Aseats", nil, http.StatusNotFound)
}

func TestProductAttachedREST(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	us := f.createFilter(t, "us", "us")
	f.do(t, http.MethodPost, "/v1/rate_cards", `{"code":"api_usd","name":"API","product_id":"code:api","currency":"USD"}`, http.StatusOK)
	plan := f.s.Fixtures.Plan(period.Monthly, 0)
	f.do(t, http.MethodPost, "/v1/plans/"+plan.ID.String()+"/rate_cards", `{"rate_card_id":"code:api_usd"}`, http.StatusOK)

	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:105")
	f.fail(t, http.MethodPatch, "/v1/products/code%3Aapi", `{"code":"other"}`, http.StatusBadRequest, "product.code", "attached_to_plan_or_subscription")
	t.Log("source: spec/requests/api/v2/products_controller_spec.rb:141")
	f.do(t, http.MethodPost, "/v1/product_categories", `{"code":"storage","name":"Storage"}`, http.StatusOK)
	f.fail(t, http.MethodPatch, "/v1/products/code%3Aapi", `{"product_category_id":"code:storage"}`, http.StatusBadRequest,
		"product.product_category", "attached_to_plan_or_subscription")
	t.Log("source: spec/requests/api/v2/product_categories_controller_spec.rb:77")
	f.fail(t, http.MethodPatch, "/v1/product_categories/code%3Acompute", `{"code":"other"}`, http.StatusBadRequest,
		"product_category.code", "attached_to_plan_or_subscription")
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:137")
	f.fail(t, http.MethodPatch, "/v1/products/code%3Aapi/filters/code%3Aus", `{"code":"other"}`, http.StatusBadRequest,
		"filter.code", "attached_to_plan_or_subscription")
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:243")
	f.fail(t, http.MethodDelete, "/v1/products/code%3Aapi/filters/"+us.ID, nil, http.StatusBadRequest, "attached_to_plan_or_subscription")
	f.do(t, http.MethodGet, "/v1/products/code%3Aapi/filters/"+us.ID, nil, http.StatusOK)
}

func TestProductFiltersREST(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	path := "/v1/products/" + f.product.ID + "/filters"
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:24")
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:49")
	us := f.createFilter(t, "us", "us")
	if us.ID == "" || len(us.Values) != 1 || us.Values[0].Key != "region" || us.Values[0].Value == nil || *us.Values[0].Value != "us" {
		t.Errorf("got filter = %+v, want region us", us)
	}
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:37")
	f.fail(t, http.MethodPost, path, `{"code":"x","name":"x"}`, http.StatusBadRequest, "filter.required")
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:62")
	all := decode[struct {
		Filter restProductFilter `json:"filter"`
	}](t, f.do(t, http.MethodPost, path, `{"code":"any","name":"any","values":[{"key":"region"}]}`, http.StatusOK)).Filter
	if len(all.Values) != 1 || all.Values[0].Value != nil {
		t.Errorf("got values = %+v, want region with every value", all.Values)
	}
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:75")
	f.do(t, http.MethodPost, path, `{"code":"mixed","name":"mixed","values":[{"key":"region"},{"key":"region","value":"eu"}]}`, http.StatusBadRequest)
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:86")
	f.do(t, http.MethodPost, "/v1/products/code%3Aunknown/filters", `{"code":"x","name":"x","values":[{"key":"region"}]}`, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:164")
	got := decode[struct {
		Filter restProductFilter `json:"filter"`
	}](t, f.do(t, http.MethodGet, path+"/code%3Aus", nil, http.StatusOK)).Filter
	if got.ID != us.ID {
		t.Errorf("got filter = %+v, want us", got)
	}
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:113")
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:124")
	got = decode[struct {
		Filter restProductFilter `json:"filter"`
	}](t, f.do(t, http.MethodPatch, path+"/code%3Aus", `{"code":"eu","name":"EU","values":[{"key":"region","value":"eu"}]}`, http.StatusOK)).Filter
	if got.Code != "eu" || len(got.Values) != 1 || *got.Values[0].Value != "eu" {
		t.Errorf("got filter = %+v, want eu", got)
	}
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:149")
	f.do(t, http.MethodPatch, path+"/code%3Aunknown", `{"name":"x"}`, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:183")
	other := f.createProduct(t, `{"code":"other","name":"Other","product_type":"PRODUCT_TYPE_METERED","billable_metric_id":"code:api_calls"}`)
	f.do(t, http.MethodPost, "/v1/products/"+other.ID+"/filters", `{"code":"x","name":"x","values":[{"key":"region"}]}`, http.StatusOK)
	list := decode[struct {
		Filters []restProductFilter `json:"filters"`
	}](t, f.do(t, http.MethodGet, path, nil, http.StatusOK)).Filters
	if len(list) != 2 || list[0].Code != "eu" || list[1].Code != "any" {
		t.Errorf("got filters = %+v, want eu and any", list)
	}
	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:212")
	list = decode[struct {
		Filters []restProductFilter `json:"filters"`
	}](t, f.do(t, http.MethodGet, path+"?search=an", nil, http.StatusOK)).Filters
	if len(list) != 1 || list[0].Code != "any" {
		t.Errorf("got filters = %+v, want any", list)
	}

	t.Log("source: spec/requests/api/v2/products/filters_controller_spec.rb:228")
	deleted := decode[struct {
		Filter restProductFilter `json:"filter"`
	}](t, f.do(t, http.MethodDelete, path+"/code%3Aeu", nil, http.StatusOK)).Filter
	if deleted.ID != us.ID || len(deleted.Values) != 1 {
		t.Errorf("got deleted filter = %+v, want eu with its values", deleted)
	}
	f.do(t, http.MethodGet, path+"/code%3Aeu", nil, http.StatusNotFound)
}
