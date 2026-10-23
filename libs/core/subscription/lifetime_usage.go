package subscription

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// LifetimeUsage is the usage amount of a subscription chain (an external
// subscription ID across upgrades and downgrades) since its start (domain
// §10.3). It drives usage thresholds and lifetime alerts.
type LifetimeUsage struct {
	// ID identifies the record.
	ID core.LifetimeUsageID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the current subscription of the chain; it moves to
	// the new subscription on an upgrade or downgrade.
	SubscriptionID core.SubscriptionID
	// ExternalSubscriptionID is the chain's external ID.
	ExternalSubscriptionID string
	// Currency is the currency of the amounts.
	Currency money.Currency
	// HistoricalUsageAmount is the imported usage before Tanvo, in minor
	// units (≥ 0).
	HistoricalUsageAmount int64
	// InvoicedUsageAmount is the usage already invoiced, in minor units
	// (≥ 0).
	InvoicedUsageAmount int64
	// CurrentUsageAmount is the not yet invoiced usage of the current
	// period, in minor units (≥ 0).
	CurrentUsageAmount int64
	// RecalculateInvoicedUsage flags InvoicedUsageAmount for refresh.
	RecalculateInvoicedUsage bool
	// RecalculateCurrentUsage flags CurrentUsageAmount for refresh.
	RecalculateCurrentUsage bool
	// InvoicedUsageAmountRefreshedAt is the last refresh of
	// InvoicedUsageAmount.
	InvoicedUsageAmountRefreshedAt *time.Time
	// CurrentUsageAmountRefreshedAt is the last refresh of
	// CurrentUsageAmount.
	CurrentUsageAmountRefreshedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the record's invariants.
func (l *LifetimeUsage) Validate() error {
	var v core.Validator
	v.Check(!l.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	v.Required("external_subscription_id", l.ExternalSubscriptionID)
	v.Currency("currency", l.Currency)
	v.NonNegative("historical_usage_amount", l.HistoricalUsageAmount)
	v.NonNegative("invoiced_usage_amount", l.InvoicedUsageAmount)
	v.NonNegative("current_usage_amount", l.CurrentUsageAmount)
	return v.Err()
}

// Total returns historical + invoiced + current usage, in minor units.
func (l *LifetimeUsage) Total() int64 {
	return l.HistoricalUsageAmount + l.InvoicedUsageAmount + l.CurrentUsageAmount
}

// FlagForRecalculation flags both amounts for refresh at now (events
// arrived or an invoice was finalized).
func (l *LifetimeUsage) FlagForRecalculation(now time.Time) {
	l.RecalculateInvoicedUsage = true
	l.RecalculateCurrentUsage = true
	l.UpdatedAt = now
}

// SetInvoicedUsage records a refreshed invoiced amount at now.
func (l *LifetimeUsage) SetInvoicedUsage(amount int64, now time.Time) {
	l.InvoicedUsageAmount = amount
	l.RecalculateInvoicedUsage = false
	l.InvoicedUsageAmountRefreshedAt = &now
	l.UpdatedAt = now
}

// SetCurrentUsage records a refreshed current amount at now.
func (l *LifetimeUsage) SetCurrentUsage(amount int64, now time.Time) {
	l.CurrentUsageAmount = amount
	l.RecalculateCurrentUsage = false
	l.CurrentUsageAmountRefreshedAt = &now
	l.UpdatedAt = now
}
