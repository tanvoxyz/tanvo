package products

import (
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// FilterValueInput selects events by one filter of the product's metric:
// the filter is named by BillableMetricFilterID or else by Key.
type FilterValueInput struct {
	// BillableMetricFilterID is the metric filter; zero to name it by Key.
	BillableMetricFilterID core.BillableMetricFilterID
	// Key is the metric filter's key, used when BillableMetricFilterID is
	// zero.
	Key string
	// Value is one of the metric filter's values; nil selects them all.
	Value *string
}

// FilterInput is a product filter to create.
type FilterInput struct {
	// Code is unique within the product; surrounding spaces are trimmed.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// InvoiceDisplayName is the name printed on invoices.
	InvoiceDisplayName *string
	// Values are the selected metric filter values; at least one.
	Values []FilterValueInput
}

// CreateFilter creates a filter of the metered product. A fixed product is
// product not_allowed_for_product_type; a key that is not one of the
// metric's filters values.key value_is_invalid.
//
// source: app/services/product_filters/create_service.rb:18-50
func (s *Service) CreateFilter(ctx context.Context, org core.OrganizationID, product core.ProductID, in FilterInput) (*catalog.ProductFilter, error) {
	var out *catalog.ProductFilter
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := getProduct(ctx, r, org, product)
		if err != nil {
			return notFound(err, ResourceProduct)
		}
		if p.ProductType != catalog.ProductTypeMetered {
			return fieldError("product", catalog.CodeNotAllowedForProductType)
		}
		m, err := productMetric(ctx, r, p)
		if err != nil {
			return err
		}
		values, err := resolveValues(m, in.Values)
		if err != nil {
			return err
		}
		if err := lockKey(ctx, r, org, productLock(p.ID)); err != nil {
			return err
		}
		if err := validateValues(ctx, r, p, m, values, core.ProductFilterID{}); err != nil {
			return err
		}
		now := s.d.Now()
		f := &catalog.ProductFilter{
			ID:                 core.NewID[core.ProductFilterID](s.d.IDs),
			OrganizationID:     org,
			ProductID:          p.ID,
			Code:               strip(in.Code),
			Name:               in.Name,
			Description:        in.Description,
			InvoiceDisplayName: in.InvoiceDisplayName,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if err := validateFilter(ctx, r, f); err != nil {
			return err
		}
		if err := validateValueRecords(m, values); err != nil {
			return err
		}
		f.Values = values
		if err := r.ProductFilters.Create(ctx, f); err != nil {
			return conflict(err, "code")
		}
		fillKeys(f, m)
		out = f
		// source: app/services/product_filters/create_service.rb:13-16
		return s.logChange(ctx, r, auditlog.ActivityProductFilterCreated, auditlog.ResourceProductFilter, f.ID.UUID(), org, nil, f)
	})
	if err != nil {
		return nil, fmt.Errorf("create product filter: %w", err)
	}
	return out, nil
}

// FilterUpdate is a partial update of a filter: only the set fields
// change.
type FilterUpdate struct {
	// Code is unique within the product; locked once a plan prices the
	// product.
	Code core.Optional[string]
	// Name is the display name.
	Name core.Optional[string]
	// Description is free text.
	Description core.Optional[*string]
	// InvoiceDisplayName is the name printed on invoices.
	InvoiceDisplayName core.Optional[*string]
	// Values replace the selected values; locked once a contract
	// prices the filter.
	Values core.Optional[[]FilterValueInput]
}

// UpdateFilter changes the filter id of the product. A changed code of an
// attached product's filter is code attached_to_plan_or_subscription,
// changed values of a filter a contract prices values
// attached_to_subscriptions.
//
// source: app/services/product_filters/update_service.rb:18-62
func (s *Service) UpdateFilter(ctx context.Context, org core.OrganizationID, product core.ProductID, id core.ProductFilterID, in FilterUpdate) (*catalog.ProductFilter, error) {
	var out *catalog.ProductFilter
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := filtersOf(r, org, product).One(ctx, refs.ByID(id))
		if err != nil {
			return err
		}
		p, err := getProduct(ctx, r, org, f.ProductID)
		if err != nil {
			return fmt.Errorf("load product: %w", err)
		}
		m, err := productMetric(ctx, r, p)
		if err != nil {
			return err
		}
		fillKeys(f, m)
		before, err := auditlog.ObjectOf(f)
		if err != nil {
			return err
		}
		var values []catalog.ProductFilterValue
		if in.Values.Set {
			if values, err = resolveValues(m, in.Values.Value); err != nil {
				return err
			}
		}
		if err := lockKey(ctx, r, org, productLock(p.ID)); err != nil {
			return err
		}
		if in.Code.Set && strip(in.Code.Value) != f.Code {
			ok, err := attached(ctx, r, org, catalog.PlanRateCardQuery{ProductID: p.ID})
			if err != nil {
				return err
			}
			if ok {
				return fieldError("code", catalog.CodeAttachedToPlanOrSubscription)
			}
		}
		changed := in.Values.Set && valuesChanged(f.Values, values)
		if changed {
			ok, err := attachedToContracts(ctx, r, org, catalog.PlanRateCardQuery{ProductFilterID: f.ID})
			if err != nil {
				return err
			}
			if ok {
				return fieldError("values", catalog.CodeAttachedToSubscriptions)
			}
		}
		if in.Values.Set {
			if err := validateValues(ctx, r, p, m, values, f.ID); err != nil {
				return err
			}
		}
		in.Name.Apply(&f.Name)
		in.Description.Apply(&f.Description)
		in.InvoiceDisplayName.Apply(&f.InvoiceDisplayName)
		if in.Code.Set {
			f.Code = strip(in.Code.Value)
		}
		if err := validateFilter(ctx, r, f); err != nil {
			return err
		}
		if changed {
			if err := validateValueRecords(m, values); err != nil {
				return err
			}
			f.Values = values
		}
		f.UpdatedAt = s.d.Now()
		if err := r.ProductFilters.Update(ctx, f); err != nil {
			return conflict(err, "code")
		}
		fillKeys(f, m)
		out = f
		// source: app/services/product_filters/update_service.rb:13-16
		return s.logChange(ctx, r, auditlog.ActivityProductFilterUpdated, auditlog.ResourceProductFilter, f.ID.UUID(), org, before, f)
	})
	if err != nil {
		return nil, fmt.Errorf("update product filter %s: %w", id, err)
	}
	return out, nil
}

// DeleteFilter deletes the filter id of the product with its values and
// the rate cards pricing it. A filter of an attached product is
// product_filter attached_to_plan_or_subscription.
//
// source: app/services/product_filters/destroy_service.rb:15-38
func (s *Service) DeleteFilter(ctx context.Context, org core.OrganizationID, product core.ProductID, id core.ProductFilterID) (*catalog.ProductFilter, error) {
	var out *catalog.ProductFilter
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := filtersOf(r, org, product).One(ctx, refs.ByID(id))
		if err != nil {
			return err
		}
		ok, err := attached(ctx, r, org, catalog.PlanRateCardQuery{ProductID: f.ProductID})
		if err != nil {
			return err
		}
		if ok {
			return fieldError("product_filter", catalog.CodeAttachedToPlanOrSubscription)
		}
		now := s.d.Now()
		if err := r.ProductFilters.Discard(ctx, org, f.ID, now); err != nil {
			return fmt.Errorf("discard: %w", err)
		}
		f.DeletedAt = &now
		out = f
		// source: app/services/product_filters/destroy_service.rb:12-15
		return s.logChange(ctx, r, auditlog.ActivityProductFilterDeleted, auditlog.ResourceProductFilter, f.ID.UUID(), org, nil, f)
	})
	if err != nil {
		return nil, fmt.Errorf("delete product filter %s: %w", id, err)
	}
	return out, nil
}

// Filter returns the filter of the product ref references.
func (s *Service) Filter(ctx context.Context, org core.OrganizationID, product core.ProductID, ref refs.Ref) (*catalog.ProductFilter, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ProductFilter, error) {
		return filtersOf(r, org, product).One(ctx, ref)
	})
}

// ListFilters returns a page of the product's filters whose name or code
// contains search, oldest first.
//
// source: app/queries/product_filters_query.rb
func (s *Service) ListFilters(ctx context.Context, org core.OrganizationID, product core.ProductID, search string, page core.PageRequest) (core.Page[*catalog.ProductFilter], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.ProductFilter], error) {
		if _, err := getProduct(ctx, r, org, product); err != nil {
			return core.Page[*catalog.ProductFilter]{}, notFound(err, ResourceProduct)
		}
		return r.ProductFilters.List(ctx, org, product, search, page)
	})
}

// productMetric returns the metric of the metered product p, discarded
// metrics included (lago belongs_to with_discarded); nil for a fixed
// product.
func productMetric(ctx context.Context, r port.Repos, p *catalog.Product) (*catalog.BillableMetric, error) {
	if p.BillableMetricID.IsZero() {
		return nil, nil
	}
	m, err := r.Metrics.GetWithDiscarded(ctx, p.OrganizationID, p.BillableMetricID)
	if err != nil {
		return nil, fmt.Errorf("load billable metric: %w", err)
	}
	return m, nil
}

// resolveValues names each value's metric filter by ID, finding a key
// among the metric's filters (values.key value_is_invalid otherwise).
//
// source: app/services/product_filters/resolve_values_service.rb:12-27
func resolveValues(m *catalog.BillableMetric, in []FilterValueInput) ([]catalog.ProductFilterValue, error) {
	out := make([]catalog.ProductFilterValue, 0, len(in))
	for _, v := range in {
		id := v.BillableMetricFilterID
		if id.IsZero() && v.Key != "" {
			mf := metricFilterByKey(m, v.Key)
			if mf == nil {
				return nil, fieldError("values.key", core.CodeInvalid)
			}
			id = mf.ID
		}
		out = append(out, catalog.ProductFilterValue{BillableMetricFilterID: id, Value: v.Value})
	}
	return out, nil
}

// validateValues checks the set of values of a filter of p: present, of
// the metric's filters, a key-only value never with specific values of
// the same filter, and a set no other filter of p selects (other than
// exclude).
//
// source: app/services/product_filters/validate_values_service.rb:12-50
func validateValues(ctx context.Context, r port.Repos, p *catalog.Product, m *catalog.BillableMetric, values []catalog.ProductFilterValue, exclude core.ProductFilterID) error {
	if len(values) == 0 {
		return fieldError("values", core.CodeRequired)
	}
	for _, v := range values {
		if metricFilter(m, v.BillableMetricFilterID) == nil {
			return fieldError("values.billable_metric_filter", core.CodeInvalid)
		}
	}
	keyOnly := map[core.BillableMetricFilterID]bool{}
	for _, v := range values {
		if v.Value == nil {
			keyOnly[v.BillableMetricFilterID] = true
		}
	}
	for _, v := range values {
		if v.Value != nil && keyOnly[v.BillableMetricFilterID] {
			return fieldError("values", catalog.CodeKeyOnlyConflictsWithValues)
		}
	}
	others, err := r.ProductFilters.ListAll(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return fmt.Errorf("list product filters: %w", err)
	}
	submitted := normalizedValues(values)
	for _, o := range others {
		if o.ID != exclude && slices.Equal(normalizedValues(o.Values), submitted) {
			return fieldError("values", core.CodeAlreadyExists)
		}
	}
	return nil
}

// validateValueRecords checks each value as lago's ProductFilterValue
// model does, stopping at the first invalid one with its errors under
// values.: a non-empty value among its metric filter's values, once per
// filter, and a kept metric (values are created anew on every change).
//
// source: app/models/product_filter_value.rb:14-42
func validateValueRecords(m *catalog.BillableMetric, values []catalog.ProductFilterValue) error {
	type pair struct {
		id    core.BillableMetricFilterID
		value string
		null  bool
	}
	var seen []pair
	for _, v := range values {
		var e core.Validator
		mf := metricFilter(m, v.BillableMetricFilterID)
		if v.Value != nil {
			if *v.Value == "" {
				e.Add("values.value", core.CodeRequired)
			} else if mf == nil || !slices.Contains(mf.Values, *v.Value) {
				e.Add("values.value", core.CodeInvalid)
			}
		}
		k := pair{id: v.BillableMetricFilterID, null: v.Value == nil}
		if v.Value != nil {
			k.value = *v.Value
		}
		if slices.Contains(seen, k) {
			e.Add("values.value", core.CodeAlreadyExists)
		}
		seen = append(seen, k)
		if m != nil && m.DeletedAt != nil {
			e.Add("values.billable_metric_filter", catalog.CodeBillableMetricDeleted)
		}
		if err := e.Err(); err != nil {
			return err
		}
	}
	return nil
}

// validateFilter checks f's own fields, without its values, and the
// code's uniqueness within the product.
//
// source: app/models/product_filter.rb:23-26
func validateFilter(ctx context.Context, r port.Repos, f *catalog.ProductFilter) error {
	own := *f
	own.Values = nil
	var v core.Validator
	v.Merge("", own.Validate())
	if f.Code != "" {
		other, err := r.ProductFilters.GetByCode(ctx, f.OrganizationID, f.ProductID, f.Code)
		if err := uniqueCode(&v, err, other != nil && other.ID != f.ID); err != nil {
			return err
		}
	}
	return v.Err()
}

// valuesChanged reports whether the submitted values differ from the
// current ones as sets.
//
// source: app/services/product_filters/update_service.rb:69-74
func valuesChanged(cur, next []catalog.ProductFilterValue) bool {
	return !slices.Equal(normalizedValues(cur), normalizedValues(next))
}

// normalizedValues returns the sorted [filter ID, value] pairs of values,
// a nil value as "" (lago's to_s).
func normalizedValues(values []catalog.ProductFilterValue) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		s := v.BillableMetricFilterID.String() + "\x00"
		if v.Value != nil {
			s += *v.Value
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// metricFilter returns the filter id of m, or nil.
func metricFilter(m *catalog.BillableMetric, id core.BillableMetricFilterID) *catalog.MetricFilter {
	if m == nil || id.IsZero() {
		return nil
	}
	for i := range m.Filters {
		if m.Filters[i].ID == id {
			return &m.Filters[i]
		}
	}
	return nil
}

// metricFilterByKey returns the filter of m with key, or nil.
func metricFilterByKey(m *catalog.BillableMetric, key string) *catalog.MetricFilter {
	if m == nil {
		return nil
	}
	for i := range m.Filters {
		if m.Filters[i].Key == key {
			return &m.Filters[i]
		}
	}
	return nil
}

// fillKeys sets the keys of f's values from the metric m.
func fillKeys(f *catalog.ProductFilter, m *catalog.BillableMetric) {
	for i, v := range f.Values {
		if mf := metricFilter(m, v.BillableMetricFilterID); mf != nil {
			f.Values[i].Key = mf.Key
		}
	}
}
