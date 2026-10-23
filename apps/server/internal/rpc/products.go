package rpc

import (
	"context"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// Products serves tanvo.catalog.v1.ProductService over the billing
// library's product catalog service (lago Api::V2::ProductsController,
// Api::V2::Products::ProductFiltersController and
// Api::V2::ProductCategoriesController).
type Products struct {
	catalogv1connect.UnimplementedProductServiceHandler

	svc *products.Service
}

var _ catalogv1connect.ProductServiceHandler = (*Products)(nil)

// NewProducts returns the service over svc.
func NewProducts(svc *products.Service) *Products { return &Products{svc: svc} }

var productTypes = NewEnum(map[catalog.ProductType]catalogv1.ProductType{
	catalog.ProductTypeMetered: catalogv1.ProductType_PRODUCT_TYPE_METERED,
	catalog.ProductTypeFixed:   catalogv1.ProductType_PRODUCT_TYPE_FIXED,
})

// The update_mask paths of the update RPCs.
var (
	productUpdateFields  = []string{"code", "name", "description", "invoice_display_name", "product_category_id"}
	filterUpdateFields   = []string{"code", "name", "description", "invoice_display_name", "values"}
	categoryUpdateFields = []string{"code", "name", "description", "invoice_display_name"}
)

// CreateProduct implements ProductService.CreateProduct.
func (s *Products) CreateProduct(ctx context.Context, req *catalogv1.CreateProductRequest) (*catalogv1.CreateProductResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetProduct()
	category, err := CatalogRef("product.product_category_id", b.GetProductCategoryId())
	if err != nil {
		return nil, err
	}
	metric, err := CatalogRef("product.billable_metric_id", b.GetBillableMetricId())
	if err != nil {
		return nil, err
	}
	pr, err := s.svc.CreateProduct(ctx, p.OrganizationID, products.ProductInput{
		Code:               b.GetCode(),
		Name:               b.GetName(),
		Description:        Opt(b.HasDescription(), b.GetDescription()),
		InvoiceDisplayName: Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()),
		ProductType:        productTypes.Core(b.GetProductType()),
		Category:           category,
		Metric:             metric,
	})
	if err != nil {
		return nil, catalogError(ctx, "product.", "invalid product", err)
	}
	out, err := s.productProto(ctx, pr)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.CreateProductResponse_builder{Product: out}.Build(), nil
}

// GetProduct implements ProductService.GetProduct.
func (s *Products) GetProduct(ctx context.Context, req *catalogv1.GetProductRequest) (*catalogv1.GetProductResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	pr, err := s.product(ctx, p.OrganizationID, "product_id", req.GetProductId())
	if err != nil {
		return nil, err
	}
	out, err := s.productProto(ctx, pr)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetProductResponse_builder{Product: out}.Build(), nil
}

// ListProducts implements ProductService.ListProducts: most recent first,
// searched by name or code. The filter takes product_type,
// product_category_id (OR of several) and without_product_category.
func (s *Products) ListProducts(ctx context.Context, req *catalogv1.ListProductsRequest) (*catalogv1.ListProductsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	q, err := productList(req.GetFilter())
	if err != nil {
		return nil, err
	}
	q.Search = req.GetSearch()
	page, err := s.svc.ListProducts(ctx, p.OrganizationID, q, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*catalogv1.Product, len(page.Items))
	for i, pr := range page.Items {
		if out[i], err = s.productProto(ctx, pr); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return catalogv1.ListProductsResponse_builder{Products: out, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdateProduct implements ProductService.UpdateProduct. product.id is an
// ID or code:<code>; an empty product_category_id in the mask removes the
// category. The type and the metric never change.
func (s *Products) UpdateProduct(ctx context.Context, req *catalogv1.UpdateProductRequest) (*catalogv1.UpdateProductResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetProduct()
	paths, err := UpdatePaths(req.GetUpdateMask(), productUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "invoice_display_name":
			return b.HasInvoiceDisplayName()
		case "product_category_id":
			return b.HasProductCategoryId()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.product(ctx, p.OrganizationID, "product.id", b.GetId())
	if err != nil {
		return nil, err
	}
	var in products.ProductUpdate
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "invoice_display_name":
			in.InvoiceDisplayName = core.Some(Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()))
		case "product_category_id":
			category, err := CatalogRef("product.product_category_id", b.GetProductCategoryId())
			if err != nil {
				return nil, err
			}
			in.Category = core.Some(category)
		}
	}
	pr, err := s.svc.UpdateProduct(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, catalogError(ctx, "product.", "invalid product", err)
	}
	out, err := s.productProto(ctx, pr)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.UpdateProductResponse_builder{Product: out}.Build(), nil
}

// DeleteProduct implements ProductService.DeleteProduct; the product's
// filters and rate cards are deleted with it.
func (s *Products) DeleteProduct(ctx context.Context, req *catalogv1.DeleteProductRequest) (*catalogv1.DeleteProductResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.product(ctx, p.OrganizationID, "product_id", req.GetProductId())
	if err != nil {
		return nil, err
	}
	pr, err := s.svc.DeleteProduct(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, catalogError(ctx, "product.", "product not deletable", err)
	}
	return catalogv1.DeleteProductResponse_builder{Product: productProtoWith(pr, 0)}.Build(), nil
}

// CreateProductFilter implements ProductService.CreateProductFilter.
func (s *Products) CreateProductFilter(ctx context.Context, req *catalogv1.CreateProductFilterRequest) (*catalogv1.CreateProductFilterResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	pr, err := s.product(ctx, p.OrganizationID, "product_id", req.GetProductId())
	if err != nil {
		return nil, err
	}
	b := req.GetFilter()
	f, err := s.svc.CreateFilter(ctx, p.OrganizationID, pr.ID, products.FilterInput{
		Code:               b.GetCode(),
		Name:               b.GetName(),
		Description:        Opt(b.HasDescription(), b.GetDescription()),
		InvoiceDisplayName: Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()),
		Values:             filterValuesInput(b.GetValues()),
	})
	if err != nil {
		return nil, catalogError(ctx, "filter.", "invalid product filter", err)
	}
	return catalogv1.CreateProductFilterResponse_builder{Filter: productFilterProto(f)}.Build(), nil
}

// GetProductFilter implements ProductService.GetProductFilter.
func (s *Products) GetProductFilter(ctx context.Context, req *catalogv1.GetProductFilterRequest) (*catalogv1.GetProductFilterResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.filter(ctx, p.OrganizationID, req.GetProductId(), "filter_id", req.GetFilterId())
	if err != nil {
		return nil, err
	}
	return catalogv1.GetProductFilterResponse_builder{Filter: productFilterProto(f)}.Build(), nil
}

// ListProductFilters implements ProductService.ListProductFilters: the
// product's filters, oldest first.
func (s *Products) ListProductFilters(ctx context.Context, req *catalogv1.ListProductFiltersRequest) (*catalogv1.ListProductFiltersResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	pr, err := s.product(ctx, p.OrganizationID, "product_id", req.GetProductId())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListFilters(ctx, p.OrganizationID, pr.ID, req.GetSearch(), Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListProductFiltersResponse_builder{
		Filters:       Items(page, productFilterProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateProductFilter implements ProductService.UpdateProductFilter.
// filter.id is an ID or code:<code> within the product; values replace
// the selected values.
func (s *Products) UpdateProductFilter(ctx context.Context, req *catalogv1.UpdateProductFilterRequest) (*catalogv1.UpdateProductFilterResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetFilter()
	paths, err := UpdatePaths(req.GetUpdateMask(), filterUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "invoice_display_name":
			return b.HasInvoiceDisplayName()
		case "values":
			return len(b.GetValues()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.filter(ctx, p.OrganizationID, req.GetProductId(), "filter.id", b.GetId())
	if err != nil {
		return nil, err
	}
	var in products.FilterUpdate
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "invoice_display_name":
			in.InvoiceDisplayName = core.Some(Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()))
		case "values":
			in.Values = core.Some(filterValuesInput(b.GetValues()))
		}
	}
	f, err := s.svc.UpdateFilter(ctx, p.OrganizationID, cur.ProductID, cur.ID, in)
	if err != nil {
		return nil, catalogError(ctx, "filter.", "invalid product filter", err)
	}
	return catalogv1.UpdateProductFilterResponse_builder{Filter: productFilterProto(f)}.Build(), nil
}

// DeleteProductFilter implements ProductService.DeleteProductFilter; the
// rate cards pricing the filter are deleted with it.
func (s *Products) DeleteProductFilter(ctx context.Context, req *catalogv1.DeleteProductFilterRequest) (*catalogv1.DeleteProductFilterResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.filter(ctx, p.OrganizationID, req.GetProductId(), "filter_id", req.GetFilterId())
	if err != nil {
		return nil, err
	}
	f, err := s.svc.DeleteFilter(ctx, p.OrganizationID, cur.ProductID, cur.ID)
	if err != nil {
		return nil, catalogError(ctx, "filter.", "product filter not deletable", err)
	}
	return catalogv1.DeleteProductFilterResponse_builder{Filter: productFilterProto(f)}.Build(), nil
}

// CreateProductCategory implements ProductService.CreateProductCategory.
func (s *Products) CreateProductCategory(ctx context.Context, req *catalogv1.CreateProductCategoryRequest) (*catalogv1.CreateProductCategoryResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetProductCategory()
	c, err := s.svc.CreateCategory(ctx, p.OrganizationID, products.CategoryInput{
		Code:               b.GetCode(),
		Name:               b.GetName(),
		Description:        Opt(b.HasDescription(), b.GetDescription()),
		InvoiceDisplayName: Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()),
	})
	if err != nil {
		return nil, catalogError(ctx, "product_category.", "invalid product category", err)
	}
	return catalogv1.CreateProductCategoryResponse_builder{ProductCategory: productCategoryProto(c, 0)}.Build(), nil
}

// GetProductCategory implements ProductService.GetProductCategory.
func (s *Products) GetProductCategory(ctx context.Context, req *catalogv1.GetProductCategoryRequest) (*catalogv1.GetProductCategoryResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.category(ctx, p.OrganizationID, "product_category_id", req.GetProductCategoryId())
	if err != nil {
		return nil, err
	}
	out, err := s.categoryProto(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetProductCategoryResponse_builder{ProductCategory: out}.Build(), nil
}

// ListProductCategories implements ProductService.ListProductCategories:
// most recent first, searched by name or code.
func (s *Products) ListProductCategories(ctx context.Context, req *catalogv1.ListProductCategoriesRequest) (*catalogv1.ListProductCategoriesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListCategories(ctx, p.OrganizationID, req.GetSearch(), Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*catalogv1.ProductCategory, len(page.Items))
	for i, c := range page.Items {
		if out[i], err = s.categoryProto(ctx, c); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return catalogv1.ListProductCategoriesResponse_builder{ProductCategories: out, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdateProductCategory implements ProductService.UpdateProductCategory;
// product_category.id is an ID or code:<code>.
func (s *Products) UpdateProductCategory(ctx context.Context, req *catalogv1.UpdateProductCategoryRequest) (*catalogv1.UpdateProductCategoryResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetProductCategory()
	paths, err := UpdatePaths(req.GetUpdateMask(), categoryUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "invoice_display_name":
			return b.HasInvoiceDisplayName()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.category(ctx, p.OrganizationID, "product_category.id", b.GetId())
	if err != nil {
		return nil, err
	}
	var in products.CategoryUpdate
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "invoice_display_name":
			in.InvoiceDisplayName = core.Some(Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()))
		}
	}
	c, err := s.svc.UpdateCategory(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, catalogError(ctx, "product_category.", "invalid product category", err)
	}
	out, err := s.categoryProto(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.UpdateProductCategoryResponse_builder{ProductCategory: out}.Build(), nil
}

// DeleteProductCategory implements ProductService.DeleteProductCategory;
// the category's products are deleted with it.
func (s *Products) DeleteProductCategory(ctx context.Context, req *catalogv1.DeleteProductCategoryRequest) (*catalogv1.DeleteProductCategoryResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.category(ctx, p.OrganizationID, "product_category_id", req.GetProductCategoryId())
	if err != nil {
		return nil, err
	}
	c, err := s.svc.DeleteCategory(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, catalogError(ctx, "product_category.", "product category not deletable", err)
	}
	return catalogv1.DeleteProductCategoryResponse_builder{ProductCategory: productCategoryProto(c, 0)}.Build(), nil
}

// product returns the product the reference s of field names.
func (s *Products) product(ctx context.Context, org core.OrganizationID, field, ref string) (*catalog.Product, error) {
	r, err := CatalogRef(field, ref)
	if err != nil {
		return nil, err
	}
	pr, err := s.svc.Product(ctx, org, r)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return pr, nil
}

// filter returns the filter ref of field within the product productRef.
func (s *Products) filter(ctx context.Context, org core.OrganizationID, productRef, field, ref string) (*catalog.ProductFilter, error) {
	pr, err := s.product(ctx, org, "product_id", productRef)
	if err != nil {
		return nil, err
	}
	r, err := CatalogRef(field, ref)
	if err != nil {
		return nil, err
	}
	f, err := s.svc.Filter(ctx, org, pr.ID, r)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return f, nil
}

// category returns the category the reference s of field names.
func (s *Products) category(ctx context.Context, org core.OrganizationID, field, ref string) (*catalog.ProductCategory, error) {
	r, err := CatalogRef(field, ref)
	if err != nil {
		return nil, err
	}
	c, err := s.svc.Category(ctx, org, r)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return c, nil
}

func (s *Products) productProto(ctx context.Context, pr *catalog.Product) (*catalogv1.Product, error) {
	n, err := s.svc.FiltersCount(ctx, pr)
	if err != nil {
		return nil, err
	}
	return productProtoWith(pr, n), nil
}

func (s *Products) categoryProto(ctx context.Context, c *catalog.ProductCategory) (*catalogv1.ProductCategory, error) {
	n, err := s.svc.ProductsCount(ctx, c)
	if err != nil {
		return nil, err
	}
	return productCategoryProto(c, n), nil
}

// productList parses the filter of ListProducts.
func productList(s string) (products.ProductList, error) {
	var q products.ProductList
	e, err := ParseFilter(s)
	if err != nil {
		return q, err
	}
	eq, err := Equalities(e, "product_type", "product_category_id", "without_product_category")
	if err != nil {
		return q, err
	}
	if v := eq["product_type"]; v != nil {
		types, err := FilterEnum[catalogv1.ProductType]("product_type", v, catalogv1.ProductType(0).Descriptor())
		if err != nil {
			return q, err
		}
		if len(types) != 1 {
			return q, filterError(`field "product_type" takes one value`)
		}
		q.ProductType = productTypes.Core(types[0])
	}
	if q.Categories, err = filterRefs(eq["product_category_id"]); err != nil {
		return q, err
	}
	without, err := FilterBool("without_product_category", eq["without_product_category"])
	if err != nil {
		return q, err
	}
	q.WithoutCategory = without != nil && *without
	return q, nil
}

// filterRefs parses catalog references given as filter values.
func filterRefs(values []string) ([]refs.Ref, error) {
	if values == nil {
		return nil, nil
	}
	out := make([]refs.Ref, len(values))
	for i, v := range values {
		r, err := CatalogRef(FilterField, v)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}

// catalogError maps the validation errors of the product catalog service,
// whose field paths are the message's, onto the request message prefix;
// an error on the object itself (rate_card attached_to_plan_or_subscription)
// keeps the object's name.
func catalogError(ctx context.Context, prefix, msg string, err error) error {
	if v, ok := validationViolations(prefix, err); ok {
		self := strings.TrimSuffix(prefix, ".")
		for i := range v {
			if v[i].Field == prefix+self {
				v[i].Field = self
			}
		}
		return withReason(InvalidArgument(msg, v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func filterValuesInput(in []*catalogv1.ProductFilterValue) []products.FilterValueInput {
	out := make([]products.FilterValueInput, len(in))
	for i, v := range in {
		out[i] = products.FilterValueInput{Key: v.GetKey(), Value: Opt(v.HasValue(), v.GetValue())}
	}
	return out
}

func productProtoWith(pr *catalog.Product, filters int) *catalogv1.Product {
	return catalogv1.Product_builder{
		Id:                 new(pr.ID.String()),
		Code:               new(pr.Code),
		Name:               new(pr.Name),
		InvoiceDisplayName: pr.InvoiceDisplayName,
		Description:        pr.Description,
		ProductType:        new(productTypes.Proto(pr.ProductType)),
		ProductCategoryId:  StringPtr(pr.ProductCategoryID.String()),
		BillableMetricId:   StringPtr(pr.BillableMetricID.String()),
		CreatedAt:          Timestamp(pr.CreatedAt),
		UpdatedAt:          Timestamp(pr.UpdatedAt),
		FiltersCount:       new(int32(filters)), //nolint:gosec // bounded by the product's filters
	}.Build()
}

func productFilterProto(f *catalog.ProductFilter) *catalogv1.ProductFilter {
	values := make([]*catalogv1.ProductFilterValue, len(f.Values))
	for i, v := range f.Values {
		values[i] = catalogv1.ProductFilterValue_builder{Key: new(v.Key), Value: v.Value}.Build()
	}
	return catalogv1.ProductFilter_builder{
		Id:                 new(f.ID.String()),
		Code:               new(f.Code),
		Name:               new(f.Name),
		Description:        f.Description,
		InvoiceDisplayName: f.InvoiceDisplayName,
		Values:             values,
		CreatedAt:          Timestamp(f.CreatedAt),
		UpdatedAt:          Timestamp(f.UpdatedAt),
	}.Build()
}

func productCategoryProto(c *catalog.ProductCategory, productsCount int) *catalogv1.ProductCategory {
	return catalogv1.ProductCategory_builder{
		Id:                 new(c.ID.String()),
		Code:               new(c.Code),
		Name:               new(c.Name),
		Description:        c.Description,
		InvoiceDisplayName: c.InvoiceDisplayName,
		CreatedAt:          Timestamp(c.CreatedAt),
		UpdatedAt:          Timestamp(c.UpdatedAt),
		ProductsCount:      new(int32(productsCount)), //nolint:gosec // bounded by the category's products
	}.Build()
}
