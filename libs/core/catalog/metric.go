package catalog

import (
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Field error codes of billable metrics and charges, named after lago's.
const (
	// CodeNotCompatibleWithAggregationType: recurring is not allowed for the
	// metric's aggregation type.
	CodeNotCompatibleWithAggregationType = "not_compatible_with_aggregation_type"
	// CodeInvalidAggregationTypeOrChargeModel: the charge model, or paying in
	// advance, does not fit the metric's aggregation type.
	CodeInvalidAggregationTypeOrChargeModel = "invalid_aggregation_type_or_charge_model"
	// CodeMustBeTrueUnlessPayInAdvance: only in-advance charges may be
	// non-invoiceable.
	CodeMustBeTrueUnlessPayInAdvance = "must_be_true_unless_pay_in_advance"
	// CodeOnlyCompatibleWithPayInAdvanceAndNonInvoiceable: regrouping needs an
	// in-advance, non-invoiceable charge.
	CodeOnlyCompatibleWithPayInAdvanceAndNonInvoiceable = "only_compatible_with_pay_in_advance_and_non_invoiceable"
	// CodeNotCompatibleWithPayInAdvance: a minimum amount needs an in-arrears
	// charge.
	CodeNotCompatibleWithPayInAdvance = "not_compatible_with_pay_in_advance"
	// CodeInvalidBillableMetricOrChargeModel: proration does not fit the
	// metric or the charge model.
	CodeInvalidBillableMetricOrChargeModel = "invalid_billable_metric_or_charge_model"
	// CodeInvalidChargeModel: the fixed charge model does not allow the
	// option.
	CodeInvalidChargeModel = "invalid_charge_model"
)

// BillableMetric is what is measured (domain §7.1). It owns its filters.
type BillableMetric struct {
	// ID identifies the metric.
	ID core.BillableMetricID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique per organization, ≤ 255 characters; locked once used.
	Code string
	// Name is required, ≤ 255 characters.
	Name string
	// Description is free text.
	Description *string
	// AggregationType is the aggregation; locked once used.
	AggregationType AggregationType
	// FieldName is the event property aggregated; required unless the
	// aggregation is count; locked once used.
	FieldName *string
	// WeightedInterval is required (seconds) for weighted_sum and kept but
	// not read on the other types; locked once used.
	WeightedInterval WeightedInterval
	// Recurring carries the value over periods; forbidden for count, max and
	// latest; locked once used.
	Recurring bool
	// Expression is a lago-expression whose result is written to FieldName
	// before aggregation; locked once used. Its syntax is checked by
	// libs/expression.
	Expression *string
	// RoundingFunction rounds the aggregated value; locked once used.
	RoundingFunction RoundingFunction
	// RoundingPrecision is the number of decimal places of the rounding (may
	// be negative); 0 when a function is set without one; locked once used.
	RoundingPrecision *int
	// Filters are the metric's filters; keys are unique.
	Filters []MetricFilter
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
	// DeletedAt is the instant the metric was discarded; repositories
	// return only kept metrics, so it is set only on the metric a delete
	// returns.
	DeletedAt *time.Time
}

// MetricFilter is a filter of a billable metric: an event property and its
// allowed values (domain §7.1 BillableMetricFilter).
type MetricFilter struct {
	// ID identifies the filter.
	ID core.BillableMetricFilterID
	// Key is the event property, ≤ 255 characters, unique per metric.
	Key string
	// Values are the allowed values; non-empty and unique.
	Values []string
}

// Normalize applies the storage rules of domain §7.1: a count metric has
// no field name, and a rounding function without precision gets 0.
func (m *BillableMetric) Normalize() {
	if m.AggregationType == AggregationTypeCount {
		m.FieldName = nil
	}
	if m.RoundingFunction != "" && m.RoundingPrecision == nil {
		zero := 0
		m.RoundingPrecision = &zero
	}
	if m.Expression != nil && *m.Expression == "" {
		m.Expression = nil
	}
}

// PayableInAdvance reports whether the metric can back in-advance charges:
// count, sum and unique_count (domain §7.1).
func (m *BillableMetric) PayableInAdvance() bool {
	switch m.AggregationType {
	case AggregationTypeCount, AggregationTypeSum, AggregationTypeUniqueCount:
		return true
	default:
		return false
	}
}

// Filter returns the filter with key.
func (m *BillableMetric) Filter(key string) (MetricFilter, bool) {
	i := slices.IndexFunc(m.Filters, func(f MetricFilter) bool { return f.Key == key })
	if i < 0 {
		return MetricFilter{}, false
	}
	return m.Filters[i], true
}

// Validate checks the invariants of domain §7.1. Call [BillableMetric.Normalize]
// first.
func (m *BillableMetric) Validate() error {
	var v core.Validator
	v.Code("code", m.Code, 255)
	v.String("name", m.Name, 255)
	aggOK := v.Enum("aggregation_type", m.AggregationType)
	if aggOK {
		hasField := m.FieldName != nil && *m.FieldName != ""
		if m.AggregationType == AggregationTypeCount {
			v.Check(!hasField, "field_name", core.CodeForbidden)
		} else {
			v.Check(hasField, "field_name", core.CodeRequired)
		}
		// Lago checks the interval's inclusion only for weighted_sum and
		// keeps it, unchecked, on the other types (billable_metric.rb:59-61).
		if m.AggregationType == AggregationTypeWeightedSum {
			v.Check(m.WeightedInterval.Valid(), "weighted_interval", core.CodeInvalid)
		} else {
			v.OptionalEnum("weighted_interval", m.WeightedInterval)
		}
		if m.Recurring {
			switch m.AggregationType {
			case AggregationTypeCount, AggregationTypeMax, AggregationTypeLatest:
				v.Add("recurring", CodeNotCompatibleWithAggregationType)
			}
		}
	}
	if m.FieldName != nil {
		v.MaxLen("field_name", *m.FieldName, 255)
	}
	v.OptionalEnum("rounding_function", m.RoundingFunction)
	keys := map[string]bool{}
	for i, f := range m.Filters {
		p := fmt.Sprintf("filters[%d]", i)
		v.Merge(p, f.Validate())
		if f.Key != "" {
			v.Check(!keys[f.Key], p+".key", core.CodeDuplicated)
			keys[f.Key] = true
		}
	}
	return v.Err()
}

// Validate checks a filter: a key of ≤ 255 characters and non-empty,
// unique values.
func (f MetricFilter) Validate() error {
	var v core.Validator
	v.String("key", f.Key, 255)
	if v.Check(len(f.Values) > 0, "values", core.CodeRequired) {
		seen := map[string]bool{}
		for _, val := range f.Values {
			if !v.Check(val != "", "values", core.CodeInvalid) || !v.Check(!seen[val], "values", core.CodeDuplicated) {
				break
			}
			seen[val] = true
		}
	}
	return v.Err()
}

// MetricLockedFields are the fields of a metric that are locked once a
// charge uses it (domain §7.1), as the API names them.
func MetricLockedFields() []string {
	return []string{"code", "aggregation_type", "weighted_interval", "field_name", "recurring", "rounding_function", "rounding_precision", "expression"}
}

// MetricLockedChanges returns the locked fields whose value differs between
// old and updated, in [MetricLockedFields] order.
func MetricLockedChanges(old, updated *BillableMetric) []string {
	var out []string
	add := func(field string, changed bool) {
		if changed {
			out = append(out, field)
		}
	}
	add("code", old.Code != updated.Code)
	add("aggregation_type", old.AggregationType != updated.AggregationType)
	add("weighted_interval", old.WeightedInterval != updated.WeightedInterval)
	add("field_name", !catalogPtrEqual(old.FieldName, updated.FieldName))
	add("recurring", old.Recurring != updated.Recurring)
	add("rounding_function", old.RoundingFunction != updated.RoundingFunction)
	add("rounding_precision", !catalogPtrEqual(old.RoundingPrecision, updated.RoundingPrecision))
	add("expression", !catalogPtrEqual(old.Expression, updated.Expression))
	return out
}

// ValidateMetricUpdate refuses changes of locked fields (core.CodeImmutable)
// when the metric is used by a charge, then validates updated.
func ValidateMetricUpdate(old, updated *BillableMetric, usedByCharge bool) error {
	var v core.Validator
	if usedByCharge {
		for _, f := range MetricLockedChanges(old, updated) {
			v.Add(f, core.CodeImmutable)
		}
	}
	v.Merge("", updated.Validate())
	return v.Err()
}

// MetricFilterChange describes how replacing a metric's filters affects the
// charge filters that use them (domain §7.1): values removed from a kept
// key are trimmed from charge filter values, and removed keys delete the
// charge filters that reference them.
type MetricFilterChange struct {
	// RemovedKeys are keys no longer present.
	RemovedKeys []string
	// RemovedValues maps each kept key to the values it no longer allows.
	RemovedValues map[string][]string
}

// IsZero reports whether nothing was removed.
func (c MetricFilterChange) IsZero() bool {
	return len(c.RemovedKeys) == 0 && len(c.RemovedValues) == 0
}

// DiffMetricFilters compares the old and new filter lists of a metric.
func DiffMetricFilters(old, updated []MetricFilter) MetricFilterChange {
	c := MetricFilterChange{RemovedValues: map[string][]string{}}
	for _, o := range old {
		i := slices.IndexFunc(updated, func(f MetricFilter) bool { return f.Key == o.Key })
		if i < 0 {
			c.RemovedKeys = append(c.RemovedKeys, o.Key)
			continue
		}
		for _, val := range o.Values {
			if !slices.Contains(updated[i].Values, val) {
				c.RemovedValues[o.Key] = append(c.RemovedValues[o.Key], val)
			}
		}
	}
	if len(c.RemovedValues) == 0 {
		c.RemovedValues = nil
	}
	return c
}

func catalogPtrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
