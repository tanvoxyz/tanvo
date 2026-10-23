package kernel

import (
	"encoding/json"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Jobs of the charge cascades from a parent plan to its override plans.
const (
	// JobCreateChargeChildren copies a new charge of a parent plan onto
	// its override plans that have active or pending subscriptions (lago
	// Charges::CreateChildrenJob); its args are [CreateChargeChildrenArgs].
	JobCreateChargeChildren = "billing.create_charge_children"
	// JobUpdateChargeChildren cascades the new pricing of a parent charge
	// to its overrides on plans with active or pending subscriptions (lago
	// Charges::UpdateChildrenJob); its args are
	// [UpdateChargeChildrenArgs].
	JobUpdateChargeChildren = "billing.update_charge_children"
	// JobDestroyChargeChildren discards the overrides of a discarded parent
	// charge on plans with active or pending subscriptions (lago
	// Charges::DestroyChildrenJob); its args are [ChargeArgs].
	JobDestroyChargeChildren = "billing.destroy_charge_children"
	// JobCascadeChargeFilter cascades one created, updated or destroyed
	// filter of a parent charge to its overrides (lago
	// ChargeFilters::CascadeJob); its args are [CascadeChargeFilterArgs].
	JobCascadeChargeFilter = "billing.cascade_charge_filter"
	// JobDestroyMetricFilters discards the filters of a deleted billable
	// metric (lago BillableMetricFilters::DestroyAllJob); its args are
	// [MetricArgs].
	JobDestroyMetricFilters = "billing.destroy_metric_filters"
)

// MetricArgs are the args of jobs about one billable metric.
type MetricArgs struct {
	// BillableMetricID is the metric.
	BillableMetricID core.BillableMetricID `json:"billable_metric_id"`
}

// ChargeChildrenBatch is the number of override plans or charges one
// charge cascade job changes in one transaction (lago each_slice(20)).
const ChargeChildrenBatch = 20

// ChargeArgs are the args of jobs about one charge.
type ChargeArgs struct {
	// ChargeID is the charge.
	ChargeID core.ChargeID `json:"charge_id"`
}

// CreateChargeChildrenArgs are the args of a [JobCreateChargeChildren].
type CreateChargeChildrenArgs struct {
	// ChargeID is the new parent charge.
	ChargeID core.ChargeID `json:"charge_id"`
	// Payload is the charge input the parent was created from, encoded by
	// the plans package.
	Payload json.RawMessage `json:"payload"`
	// PlanIDs are the override plans of this batch; nil lists them and
	// enqueues one job per batch (lago CreateChildrenBatchJob).
	PlanIDs []core.PlanID `json:"plan_ids,omitempty"`
}

// UpdateChargeChildrenArgs are the args of a [JobUpdateChargeChildren].
type UpdateChargeChildrenArgs struct {
	// ChargeID is the parent charge.
	ChargeID core.ChargeID `json:"charge_id"`
	// Code is the code to give the overrides; "" keeps theirs.
	Code string `json:"code,omitempty"`
	// ChargeModel is the parent's model; overrides of another model are
	// left alone.
	ChargeModel catalog.ChargeModel `json:"charge_model"`
	// Properties are the parent's new properties; nil when the update gave
	// none, so the overrides get the model's defaults.
	Properties *catalog.Properties `json:"properties,omitempty"`
	// ConversionRate is the parent's new pricing-unit conversion rate, nil
	// when the update gave none.
	ConversionRate *money.Decimal `json:"conversion_rate,omitempty"`
	// OldChargeModel and OldProperties are the parent's pricing before the
	// update: overrides that still price like it follow the new pricing.
	OldChargeModel catalog.ChargeModel `json:"old_charge_model"`
	// OldProperties are the parent's properties before the update.
	OldProperties catalog.Properties `json:"old_properties"`
	// OldConversionRate is the parent's conversion rate before the update,
	// nil when it had no pricing unit.
	OldConversionRate *money.Decimal `json:"old_conversion_rate,omitempty"`
	// ChildIDs are the override charges of this batch; nil lists them and
	// enqueues one job per batch (lago UpdateChildrenBatchJob).
	ChildIDs []core.ChargeID `json:"child_ids,omitempty"`
}

// Actions of a [JobCascadeChargeFilter].
const (
	// ChargeFilterCreated is a filter added to the parent charge.
	ChargeFilterCreated = "create"
	// ChargeFilterUpdated is a filter of the parent charge whose pricing
	// or display name changed.
	ChargeFilterUpdated = "update"
	// ChargeFilterDestroyed is a filter removed from the parent charge.
	ChargeFilterDestroyed = "destroy"
)

// CascadeChargeFilterArgs are the args of a [JobCascadeChargeFilter].
type CascadeChargeFilterArgs struct {
	// ChargeID is the parent charge.
	ChargeID core.ChargeID `json:"charge_id"`
	// Action is [ChargeFilterCreated], [ChargeFilterUpdated] or
	// [ChargeFilterDestroyed].
	Action string `json:"action"`
	// Values are the filter's values.
	Values map[string][]string `json:"values"`
	// OldProperties are the filter's properties before the change; nil for
	// a created filter.
	OldProperties *catalog.Properties `json:"old_properties,omitempty"`
	// NewProperties are the filter's properties after the change; nil for
	// a destroyed filter.
	NewProperties *catalog.Properties `json:"new_properties,omitempty"`
	// InvoiceDisplayName is the filter's display name after the change.
	InvoiceDisplayName *string `json:"invoice_display_name,omitempty"`
	// Code is the filter's code, which identifies its copies.
	Code string `json:"code"`
}

// chargeJobKinds returns the job kinds of the charge cascades.
func chargeJobKinds() []string {
	return []string{JobCreateChargeChildren, JobUpdateChargeChildren, JobDestroyChargeChildren, JobCascadeChargeFilter, JobDestroyMetricFilters}
}
