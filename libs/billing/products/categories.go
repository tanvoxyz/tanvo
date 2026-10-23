package products

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CategoryInput is a product category to create.
type CategoryInput struct {
	// Code is the unique code; surrounding spaces are trimmed.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// InvoiceDisplayName is the name printed on invoices.
	InvoiceDisplayName *string
}

// CreateCategory creates a product category.
//
// source: app/services/product_categories/create_service.rb:16-29
func (s *Service) CreateCategory(ctx context.Context, org core.OrganizationID, in CategoryInput) (*catalog.ProductCategory, error) {
	now := s.d.Now()
	c := &catalog.ProductCategory{
		ID:                 core.NewID[core.ProductCategoryID](s.d.IDs),
		OrganizationID:     org,
		Code:               strip(in.Code),
		Name:               in.Name,
		Description:        in.Description,
		InvoiceDisplayName: in.InvoiceDisplayName,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := validateCategory(ctx, r, c); err != nil {
			return err
		}
		return conflict(r.ProductCategories.Create(ctx, c), "code")
	})
	if err != nil {
		return nil, fmt.Errorf("create product category: %w", err)
	}
	return c, nil
}

// CategoryUpdate is a partial update of a category: only the set fields
// change.
type CategoryUpdate struct {
	// Code is the unique code; locked once a plan prices the category.
	Code core.Optional[string]
	// Name is the display name.
	Name core.Optional[string]
	// Description is free text.
	Description core.Optional[*string]
	// InvoiceDisplayName is the name printed on invoices.
	InvoiceDisplayName core.Optional[*string]
}

// UpdateCategory changes the category id. A changed code of an attached
// category is code attached_to_plan_or_subscription.
//
// source: app/services/product_categories/update_service.rb:16-37
func (s *Service) UpdateCategory(ctx context.Context, org core.OrganizationID, id core.ProductCategoryID, in CategoryUpdate) (*catalog.ProductCategory, error) {
	var out *catalog.ProductCategory
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := getCategory(ctx, r, org, id)
		if err != nil {
			return notFound(err, ResourceProductCategory)
		}
		in.Name.Apply(&c.Name)
		in.Description.Apply(&c.Description)
		in.InvoiceDisplayName.Apply(&c.InvoiceDisplayName)
		if in.Code.Set && strip(in.Code.Value) != c.Code {
			ok, err := attached(ctx, r, org, catalog.PlanRateCardQuery{ProductCategoryID: id})
			if err != nil {
				return err
			}
			if ok {
				return fieldError("code", catalog.CodeAttachedToPlanOrSubscription)
			}
			c.Code = strip(in.Code.Value)
		}
		if err := validateCategory(ctx, r, c); err != nil {
			return err
		}
		c.UpdatedAt = s.d.Now()
		if err := r.ProductCategories.Update(ctx, c); err != nil {
			return conflict(err, "code")
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update product category %s: %w", id, err)
	}
	return out, nil
}

// DeleteCategory deletes the category id with its products, their filters
// and rate cards. An attached category is product_category
// attached_to_plan_or_subscription.
//
// source: app/services/product_categories/destroy_service.rb:15-34
func (s *Service) DeleteCategory(ctx context.Context, org core.OrganizationID, id core.ProductCategoryID) (*catalog.ProductCategory, error) {
	var out *catalog.ProductCategory
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := getCategory(ctx, r, org, id)
		if err != nil {
			return notFound(err, ResourceProductCategory)
		}
		ok, err := attached(ctx, r, org, catalog.PlanRateCardQuery{ProductCategoryID: id})
		if err != nil {
			return err
		}
		if ok {
			return fieldError("product_category", catalog.CodeAttachedToPlanOrSubscription)
		}
		now := s.d.Now()
		if err := r.ProductCategories.Discard(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard: %w", err)
		}
		c.DeletedAt = &now
		out = c
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("delete product category %s: %w", id, err)
	}
	return out, nil
}

// Category returns the category ref references.
func (s *Service) Category(ctx context.Context, org core.OrganizationID, ref refs.Ref) (*catalog.ProductCategory, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ProductCategory, error) {
		return categories(r, org).One(ctx, ref)
	})
}

// ListCategories returns a page of the categories whose name or code
// contains search, most recent first.
//
// source: app/queries/product_categories_query.rb:6-27
func (s *Service) ListCategories(ctx context.Context, org core.OrganizationID, search string, page core.PageRequest) (core.Page[*catalog.ProductCategory], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.ProductCategory], error) {
		return r.ProductCategories.List(ctx, org, catalog.ProductCategoryQuery{Search: search}, page)
	})
}

// ProductsCount returns the number of products of the category c (lago
// V2::ProductCategorySerializer products_count).
//
// source: app/serializers/v2/product_category_serializer.rb:12
func (s *Service) ProductsCount(ctx context.Context, c *catalog.ProductCategory) (int, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (int, error) {
		return r.Products.Count(ctx, c.OrganizationID, catalog.ProductQuery{ProductCategoryIDs: []core.ProductCategoryID{c.ID}})
	})
}

// validateCategory checks c as lago's model does, the code's uniqueness
// included.
//
// source: app/models/product_category.rb:19-22
func validateCategory(ctx context.Context, r port.Repos, c *catalog.ProductCategory) error {
	var v core.Validator
	v.Merge("", c.Validate())
	if c.Code != "" {
		other, err := r.ProductCategories.GetByCode(ctx, c.OrganizationID, c.Code)
		if err := uniqueCode(&v, err, other != nil && other.ID != c.ID); err != nil {
			return err
		}
	}
	return v.Err()
}

// uniqueCode adds code value_already_exist when a lookup by code found
// another object (taken); a lookup error other than not found is returned.
func uniqueCode(v *core.Validator, err error, taken bool) error {
	switch {
	case err == nil:
		v.Check(!taken, "code", core.CodeAlreadyExists)
	case !isNotFound(err):
		return fmt.Errorf("check code: %w", err)
	}
	return nil
}
