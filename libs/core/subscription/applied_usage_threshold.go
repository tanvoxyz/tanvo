package subscription

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// AppliedUsageThreshold records a usage threshold reached by a subscription
// and the progressive-billing invoice it produced (domain §7.9, §22.16).
type AppliedUsageThreshold struct {
	// ID identifies the record.
	ID core.AppliedUsageThresholdID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// UsageThresholdID is the reached threshold (of the plan or of the
	// subscription).
	UsageThresholdID core.UsageThresholdID
	// SubscriptionID is the subscription that reached it.
	SubscriptionID core.SubscriptionID
	// InvoiceID is the progressive-billing invoice.
	InvoiceID core.InvoiceID
	// LifetimeUsageAmount is the lifetime usage amount, in minor units,
	// at which the threshold passed (≥ 0).
	LifetimeUsageAmount int64
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the record's invariants.
func (a *AppliedUsageThreshold) Validate() error {
	var v core.Validator
	v.Check(!a.UsageThresholdID.IsZero(), "usage_threshold_id", core.CodeRequired)
	v.Check(!a.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	v.Check(!a.InvoiceID.IsZero(), "invoice_id", core.CodeRequired)
	v.NonNegative("lifetime_usage_amount", a.LifetimeUsageAmount)
	return v.Err()
}
