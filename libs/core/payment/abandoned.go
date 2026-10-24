package payment

import "time"

// Abandoned payment recovery (lago Invoices::Payments::CancelAbandonedService).
const (
	// AbandonedPeriod is how long a payment waits for the customer's
	// action before it counts as abandoned.
	AbandonedPeriod = 24 * time.Hour
	// abandonedRecoveryMonths bounds how old an abandoned payment can be
	// and still be cancelled: cancelling older ones would charge and dun a
	// customer who heard nothing for months.
	abandonedRecoveryMonths = 1
)

// AbandonedRange returns the instants of last change of the payments
// abandoned at now that are still recovered: from a month before now to
// [AbandonedPeriod] before now, both included (lago recovery_range).
func AbandonedRange(now time.Time) (from, to time.Time) {
	return now.AddDate(0, -abandonedRecoveryMonths, 0), now.Add(-AbandonedPeriod)
}

// ProviderStatusRequiresAction is the provider status of a payment waiting
// for the customer's action (Stripe's requires_action).
const ProviderStatusRequiresAction = "requires_action"
