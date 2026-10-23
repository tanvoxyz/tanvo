package catalog

import (
	"regexp"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Rule codes of the product catalog (lago's names).
const (
	// CodeAttachedToPlanOrSubscription: a plan prices the object, which
	// locks its code (and a product's category) and refuses its deletion.
	CodeAttachedToPlanOrSubscription = "attached_to_plan_or_subscription"
	// CodeAttachedToSubscriptions: a subscription is billed by the object.
	CodeAttachedToSubscriptions = "attached_to_subscriptions"
	// CodeNotAllowedForProductType: the setting does not fit the product's
	// type.
	CodeNotAllowedForProductType = "not_allowed_for_product_type"
	// CodeKeyOnlyConflictsWithValues: a key-only filter value is combined
	// with specific values of the same key.
	CodeKeyOnlyConflictsWithValues = "key_only_conflicts_with_values"
	// CodeBillableMetricDeleted: the metric filter, or its metric, is
	// deleted.
	CodeBillableMetricDeleted = "billable_metric_deleted"
	// CodeMustBeBlank: the field must not be set.
	CodeMustBeBlank = "value_must_be_blank"
)

// catalogCodeFormat is the URL-safe code of product catalog objects; an
// all-dot code is refused.
//
// source: app/models/concerns/catalog_code_format.rb:14
var (
	catalogCodeChars = regexp.MustCompile(`^[a-zA-Z0-9_\-.]+$`)
	catalogCodeDots  = regexp.MustCompile(`^\.+$`)
)

// ValidCatalogCode reports whether code has the format of product catalog
// codes: letters, digits, `_`, `-` and `.`, not only dots.
func ValidCatalogCode(code string) bool {
	return catalogCodeChars.MatchString(code) && !catalogCodeDots.MatchString(code)
}

// catalogCode checks a required catalog code.
func catalogCode(v *core.Validator, field, code string) {
	if v.Code(field, code, 255) {
		v.Check(ValidCatalogCode(code), field, core.CodeInvalid)
	}
}

// ProductType is what a product sells.
type ProductType string

// ProductType values.
const (
	// ProductTypeMetered is "metered": usage of a billable metric.
	ProductTypeMetered ProductType = "metered"
	// ProductTypeFixed is "fixed": a fixed fee, in the units of the plan
	// rate card.
	ProductTypeFixed ProductType = "fixed"
)

// ProductTypeValues returns every ProductType, in declaration order.
func ProductTypeValues() []ProductType { return []ProductType{ProductTypeMetered, ProductTypeFixed} }

// Valid reports whether v is one of the declared ProductType values.
func (v ProductType) Valid() bool { return v == ProductTypeMetered || v == ProductTypeFixed }

// String returns the stored form of v.
func (v ProductType) String() string { return string(v) }

// ParseProductType parses the stored form s, failing with
// [core.ErrInvalidValue] for anything but a declared value.
func ParseProductType(s string) (ProductType, error) {
	if v := ProductType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.ProductType", s)
}

// ProductCategory groups products.
type ProductCategory struct {
	// ID identifies the category.
	ID core.ProductCategoryID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique among the organization's categories.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// InvoiceDisplayName is the name printed on invoices.
	InvoiceDisplayName *string
	// CreatedAt is when the category was created.
	CreatedAt time.Time
	// UpdatedAt is when the category last changed.
	UpdatedAt time.Time
	// DeletedAt is when the row was discarded; nil while kept.
	DeletedAt *time.Time
}

// Validate checks the category's own fields.
//
// source: app/models/product_category.rb:19-22
func (c *ProductCategory) Validate() error {
	var v core.Validator
	catalogCode(&v, "code", c.Code)
	v.String("name", c.Name, 255)
	v.OptionalString("invoice_display_name", c.InvoiceDisplayName, 255)
	return v.Err()
}

// Product is a sellable item of the product catalog: the usage of a
// billable metric (metered) or a fixed fee (fixed), priced by rate cards.
type Product struct {
	// ID identifies the product.
	ID core.ProductID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// ProductCategoryID is the category; zero for none.
	ProductCategoryID core.ProductCategoryID
	// BillableMetricID is the sold metric of a metered product; zero for a
	// fixed product.
	BillableMetricID core.BillableMetricID
	// ProductType is what the product sells; immutable.
	ProductType ProductType
	// Code is unique among the organization's products.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// InvoiceDisplayName is the name printed on invoices.
	InvoiceDisplayName *string
	// CreatedAt is when the product was created.
	CreatedAt time.Time
	// UpdatedAt is when the product last changed.
	UpdatedAt time.Time
	// DeletedAt is when the row was discarded; nil while kept.
	DeletedAt *time.Time
}

// Validate checks the product's own fields: a metered product sells a
// metric, a fixed one none.
//
// source: app/models/product.rb:28-33, :62-70
func (p *Product) Validate() error {
	var v core.Validator
	catalogCode(&v, "code", p.Code)
	v.String("name", p.Name, 255)
	v.OptionalString("invoice_display_name", p.InvoiceDisplayName, 255)
	if v.Enum("product_type", p.ProductType) {
		switch p.ProductType {
		case ProductTypeMetered:
			v.Check(!p.BillableMetricID.IsZero(), "billable_metric", core.CodeRequired)
		case ProductTypeFixed:
			v.Check(p.BillableMetricID.IsZero(), "billable_metric", CodeMustBeBlank)
		}
	}
	return v.Err()
}

// InvoiceName is the name printed on invoices: the display name, else the
// name.
func (p *Product) InvoiceName() string {
	if p.InvoiceDisplayName != nil && *p.InvoiceDisplayName != "" {
		return *p.InvoiceDisplayName
	}
	return p.Name
}

// ProductFilterValue selects events of a metered product by one metric
// filter: its Value, or every value of the filter when Value is nil.
type ProductFilterValue struct {
	// BillableMetricFilterID is the metric filter.
	BillableMetricFilterID core.BillableMetricFilterID
	// Key is the metric filter's key; filled by reads.
	Key string
	// Value is one of the metric filter's values; nil selects them all.
	Value *string
}

// ProductFilter is a priced subset of a metered product's events.
type ProductFilter struct {
	// ID identifies the filter.
	ID core.ProductFilterID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// ProductID is the filtered product.
	ProductID core.ProductID
	// Code is unique among the product's filters.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// InvoiceDisplayName is the name printed on invoices.
	InvoiceDisplayName *string
	// Values are the selected metric filter values, in creation order.
	Values []ProductFilterValue
	// CreatedAt is when the filter was created.
	CreatedAt time.Time
	// UpdatedAt is when the filter last changed.
	UpdatedAt time.Time
	// DeletedAt is when the row was discarded; nil while kept.
	DeletedAt *time.Time
}

// Validate checks the filter's own fields.
//
// source: app/models/product_filter.rb:22-25, app/models/product_filter_value.rb:14
func (f *ProductFilter) Validate() error {
	var v core.Validator
	catalogCode(&v, "code", f.Code)
	v.String("name", f.Name, 255)
	v.OptionalString("invoice_display_name", f.InvoiceDisplayName, 255)
	for _, fv := range f.Values {
		if fv.Value != nil && *fv.Value == "" {
			v.Add("values.value", core.CodeRequired)
			break
		}
	}
	return v.Err()
}
