package products

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// ProductInput is a product to create.
type ProductInput struct {
	// Code is the unique code; surrounding spaces are trimmed.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// InvoiceDisplayName is the name printed on invoices.
	InvoiceDisplayName *string
	// ProductType is what the product sells.
	ProductType catalog.ProductType
	// Category references the category; zero for none.
	Category refs.Ref
	// Metric references the sold billable metric of a metered product;
	// zero for none.
	Metric refs.Ref
}

// CreateProduct creates a product. A missing category is
// core.NotFound("product_category"), a missing metric
// core.NotFound("billable_metric").
//
// source: app/services/products/create_service.rb:18-45
func (s *Service) CreateProduct(ctx context.Context, org core.OrganizationID, in ProductInput) (*catalog.Product, error) {
	now := s.d.Now()
	p := &catalog.Product{
		ID:                 core.NewID[core.ProductID](s.d.IDs),
		OrganizationID:     org,
		ProductType:        in.ProductType,
		Code:               strip(in.Code),
		Name:               in.Name,
		Description:        in.Description,
		InvoiceDisplayName: in.InvoiceDisplayName,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if !in.Category.IsZero() {
			c, err := categories(r, org).One(ctx, in.Category)
			if err != nil {
				return err
			}
			p.ProductCategoryID = c.ID
		}
		if !in.Metric.IsZero() {
			m, err := refs.Metrics(r, org).One(ctx, in.Metric)
			if err != nil {
				return err
			}
			p.BillableMetricID = m.ID
		}
		if err := validateProduct(ctx, r, p); err != nil {
			return err
		}
		return conflict(r.Products.Create(ctx, p), "code")
	})
	if err != nil {
		return nil, fmt.Errorf("create product: %w", err)
	}
	return p, nil
}

// ProductUpdate is a partial update of a product: only the set fields
// change. The type and the metric never change.
type ProductUpdate struct {
	// Code is the unique code; locked once a plan prices the product.
	Code core.Optional[string]
	// Name is the display name.
	Name core.Optional[string]
	// Description is free text.
	Description core.Optional[*string]
	// InvoiceDisplayName is the name printed on invoices.
	InvoiceDisplayName core.Optional[*string]
	// Category references the new category, a zero reference none; locked
	// once a plan prices the product.
	Category core.Optional[refs.Ref]
}

// UpdateProduct changes the product id. On an attached product a changed
// code is code attached_to_plan_or_subscription and a changed category
// product_category attached_to_plan_or_subscription.
//
// source: app/services/products/update_service.rb:18-45, app/controllers/api/v2/products_controller.rb:36-48
func (s *Service) UpdateProduct(ctx context.Context, org core.OrganizationID, id core.ProductID, in ProductUpdate) (*catalog.Product, error) {
	var out *catalog.Product
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := getProduct(ctx, r, org, id)
		if err != nil {
			return notFound(err, ResourceProduct)
		}
		var category core.ProductCategoryID
		if in.Category.Set && !in.Category.Value.IsZero() {
			c, err := categories(r, org).One(ctx, in.Category.Value)
			if err != nil {
				return err
			}
			category = c.ID
		}
		in.Name.Apply(&p.Name)
		in.Description.Apply(&p.Description)
		in.InvoiceDisplayName.Apply(&p.InvoiceDisplayName)
		isAttached, err := attached(ctx, r, org, catalog.PlanRateCardQuery{ProductID: id})
		if err != nil {
			return err
		}
		if isAttached {
			if in.Code.Set && strip(in.Code.Value) != p.Code {
				return fieldError("code", catalog.CodeAttachedToPlanOrSubscription)
			}
			if in.Category.Set && category != p.ProductCategoryID {
				return fieldError("product_category", catalog.CodeAttachedToPlanOrSubscription)
			}
		} else {
			if in.Code.Set {
				p.Code = strip(in.Code.Value)
			}
			if in.Category.Set {
				p.ProductCategoryID = category
			}
		}
		if err := validateProduct(ctx, r, p); err != nil {
			return err
		}
		p.UpdatedAt = s.d.Now()
		if err := r.Products.Update(ctx, p); err != nil {
			return conflict(err, "code")
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update product %s: %w", id, err)
	}
	return out, nil
}

// DeleteProduct deletes the product id with its filters and rate cards
// (and their rates). An attached product is product
// attached_to_plan_or_subscription.
//
// source: app/services/products/destroy_service.rb:15-34
func (s *Service) DeleteProduct(ctx context.Context, org core.OrganizationID, id core.ProductID) (*catalog.Product, error) {
	var out *catalog.Product
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := getProduct(ctx, r, org, id)
		if err != nil {
			return notFound(err, ResourceProduct)
		}
		ok, err := attached(ctx, r, org, catalog.PlanRateCardQuery{ProductID: id})
		if err != nil {
			return err
		}
		if ok {
			return fieldError("product", catalog.CodeAttachedToPlanOrSubscription)
		}
		now := s.d.Now()
		if err := r.Products.Discard(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard: %w", err)
		}
		p.DeletedAt = &now
		out = p
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("delete product %s: %w", id, err)
	}
	return out, nil
}

// Product returns the product ref references.
func (s *Service) Product(ctx context.Context, org core.OrganizationID, ref refs.Ref) (*catalog.Product, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Product, error) {
		return productsOf(r, org).One(ctx, ref)
	})
}

// ProductList narrows [Service.ListProducts].
type ProductList struct {
	// Search keeps products whose name or code contains the term.
	Search string
	// Categories keep products of these categories; a missing one is
	// core.NotFound("product_category").
	Categories []refs.Ref
	// WithoutCategory keeps uncategorized products; with Categories,
	// either matches.
	WithoutCategory bool
	// ProductType keeps products of that type; empty keeps every type.
	ProductType catalog.ProductType
}

// ListProducts returns a page of the products matching q, most recent
// first.
//
// source: app/queries/products_query.rb, app/controllers/api/v2/products_controller.rb:73-105
func (s *Service) ListProducts(ctx context.Context, org core.OrganizationID, q ProductList, page core.PageRequest) (core.Page[*catalog.Product], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.Product], error) {
		ids, err := categories(r, org).IDs(ctx, q.Categories)
		if err != nil {
			return core.Page[*catalog.Product]{}, err
		}
		return r.Products.List(ctx, org, catalog.ProductQuery{
			Search:                 q.Search,
			ProductCategoryIDs:     ids,
			WithoutProductCategory: q.WithoutCategory,
			ProductType:            q.ProductType,
		}, page)
	})
}

// FiltersCount returns the number of filters of the product p (lago
// V2::ProductSerializer filters_count).
//
// source: app/serializers/v2/product_serializer.rb:15
func (s *Service) FiltersCount(ctx context.Context, p *catalog.Product) (int, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (int, error) {
		return r.ProductFilters.Count(ctx, p.OrganizationID, p.ID)
	})
}

// validateProduct checks p as lago's model does, the code's uniqueness
// included.
//
// source: app/models/product.rb:28-33
func validateProduct(ctx context.Context, r port.Repos, p *catalog.Product) error {
	var v core.Validator
	v.Merge("", p.Validate())
	if p.Code != "" {
		other, err := r.Products.GetByCode(ctx, p.OrganizationID, p.Code)
		if err := uniqueCode(&v, err, other != nil && other.ID != p.ID); err != nil {
			return err
		}
	}
	return v.Err()
}
