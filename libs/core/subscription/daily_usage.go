package subscription

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// DefaultDailyUsageHistoryDays is how many days back a daily usage history
// fill starts by default (lago DailyUsage::DEFAULT_HISTORY_DAYS,
// app/models/daily_usage.rb:4).
const DefaultDailyUsageHistoryDays = 120

// DailyUsage is the snapshot of a subscription's current usage at the end
// of one day in the customer's time zone, with its change from the
// previous snapshot of the same period (lago DailyUsage). The revenue
// analytics read them; summing UsageDiff over the days of a period gives
// its usage.
type DailyUsage struct {
	// ID identifies the snapshot.
	ID core.DailyUsageID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CustomerID is the subscription's customer.
	CustomerID core.CustomerID
	// SubscriptionID is the subscription.
	SubscriptionID core.SubscriptionID
	// ExternalSubscriptionID is the subscription's external ID.
	ExternalSubscriptionID string
	// Usage is the usage of the period up to the end of UsageDate.
	Usage UsageSnapshot
	// UsageDiff is Usage minus the previous snapshot of the same period
	// (see [DiffUsage]), the whole usage when there is none; nil (stored as
	// an empty object) for the first day of a history fill.
	UsageDiff *UsageSnapshot
	// From is the start of the charges period.
	From time.Time
	// To is the end of the charges period.
	To time.Time
	// RefreshedAt is the instant the usage was computed for.
	RefreshedAt time.Time
	// UsageDate is the day of the snapshot, in the customer's time zone.
	UsageDate core.Date
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the snapshot's invariants.
func (d *DailyUsage) Validate() error {
	var v core.Validator
	v.Check(!d.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	v.Check(!d.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	v.Required("external_subscription_id", d.ExternalSubscriptionID)
	v.Check(!d.From.IsZero(), "from_datetime", core.CodeRequired)
	v.Check(!d.To.IsZero(), "to_datetime", core.CodeRequired)
	v.Check(!d.RefreshedAt.IsZero(), "refreshed_at", core.CodeRequired)
	v.Check(!d.UsageDate.IsZero(), "usage_date", core.CodeRequired)
	return v.Err()
}

// DailyUsageDay is a subscription that has a snapshot for a day.
type DailyUsageDay struct {
	// SubscriptionID is the subscription.
	SubscriptionID core.SubscriptionID
	// UsageDate is the day.
	UsageDate core.Date
}

// UsageSnapshot is a subscription's usage of a period, per charge (lago
// V1::Customers::UsageSerializer with charges_usage). Its JSON form is the
// stored one.
type UsageSnapshot struct {
	// From is the start of the charges period.
	From time.Time `json:"from_datetime,omitzero"`
	// To is the end of the charges period.
	To time.Time `json:"to_datetime,omitzero"`
	// IssuingDate is the date the period is invoiced.
	IssuingDate core.Date `json:"issuing_date,omitzero"`
	// Currency is the ISO code of the amounts' currency.
	Currency string `json:"currency,omitempty"`
	// Amount is the usage amount before taxes, in minor units.
	Amount int64 `json:"amount_cents"`
	// TaxesAmount is the taxes, in minor units.
	TaxesAmount int64 `json:"taxes_amount_cents"`
	// TotalAmount is Amount + TaxesAmount, in minor units.
	TotalAmount int64 `json:"total_amount_cents"`
	// Charges is the usage per charge.
	Charges []ChargeUsage `json:"charges_usage"`
}

// UsageValues are the quantities of a charge usage, of one of its filters
// or of one of its groups.
type UsageValues struct {
	// Units are the billed units.
	Units money.Decimal `json:"units"`
	// TotalAggregatedUnits are the aggregated units before free units.
	TotalAggregatedUnits money.Decimal `json:"total_aggregated_units"`
	// EventsCount is the number of events.
	EventsCount int64 `json:"events_count"`
	// Amount is the amount, in minor units.
	Amount int64 `json:"amount_cents"`
	// PricingUnit is the amount in the charge's pricing unit, when it has
	// one.
	PricingUnit *PricingUnitUsage `json:"pricing_unit_details,omitempty"`
}

// PricingUnitUsage is a usage amount in a pricing unit.
type PricingUnitUsage struct {
	// Amount is the amount in pricing units.
	Amount money.Decimal `json:"amount"`
	// ShortName is the pricing unit's short name.
	ShortName string `json:"short_name"`
	// ConversionRate is fiat per pricing unit.
	ConversionRate money.Decimal `json:"conversion_rate"`
}

// ChargeUsage is the usage of one charge.
type ChargeUsage struct {
	UsageValues
	// Currency is the ISO code of Amount's currency.
	Currency string `json:"amount_currency"`
	// Charge is the charge.
	Charge ChargeRef `json:"charge"`
	// BillableMetric is the charge's billable metric.
	BillableMetric MetricRef `json:"billable_metric"`
	// Filters is the usage per charge filter, for a charge with filters.
	Filters []FilterUsage `json:"filters"`
	// Grouped is the usage per group, for a charge with pricing groups.
	Grouped []GroupedUsage `json:"grouped_usage"`
	// PresentationBreakdowns split the units of the fees without a group
	// or a filter by presentation group.
	PresentationBreakdowns []PresentationBreakdownUsage `json:"presentation_breakdowns"`
}

// ChargeRef identifies the charge of a [ChargeUsage].
type ChargeRef struct {
	// ID is the charge.
	ID core.ChargeID `json:"id"`
	// Code is the charge code.
	Code string `json:"code,omitempty"`
	// ChargeModel is the charge model.
	ChargeModel string `json:"charge_model"`
	// InvoiceDisplayName is the charge's display name.
	InvoiceDisplayName *string `json:"invoice_display_name"`
}

// MetricRef identifies the billable metric of a [ChargeUsage].
type MetricRef struct {
	// ID is the billable metric.
	ID core.BillableMetricID `json:"id"`
	// Name is the metric name.
	Name string `json:"name"`
	// Code is the metric code.
	Code string `json:"code"`
	// AggregationType is the metric's aggregation.
	AggregationType string `json:"aggregation_type"`
}

// FilterUsage is the usage of one filter of a charge (the default bucket
// has nil Values).
type FilterUsage struct {
	UsageValues
	// InvoiceDisplayName is the filter's display name.
	InvoiceDisplayName *string `json:"invoice_display_name"`
	// Values are the filter's values by key; nil for the default bucket.
	Values map[string][]string `json:"values"`
	// PresentationBreakdowns split the units by presentation group.
	PresentationBreakdowns []PresentationBreakdownUsage `json:"presentation_breakdowns"`
}

// GroupedUsage is the usage of one pricing group of a charge.
type GroupedUsage struct {
	UsageValues
	// GroupedBy are the group's values by key.
	GroupedBy map[string]string `json:"grouped_by"`
	// Filters is the group's usage per charge filter.
	Filters []FilterUsage `json:"filters"`
	// PresentationBreakdowns split the units of the group's fees without
	// a filter by presentation group.
	PresentationBreakdowns []PresentationBreakdownUsage `json:"presentation_breakdowns"`
}

// PresentationBreakdownUsage is the units of one presentation group.
type PresentationBreakdownUsage struct {
	// PresentationBy are the group's values by presentation key; a nil
	// value is a key missing on the events.
	PresentationBy map[string]*string `json:"presentation_by"`
	// Units are the group's units.
	Units money.Decimal `json:"units"`
}
