package catalog

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Field error codes of usage thresholds and entitlements.
const (
	// CodeOneOfPlanOrSubscriptionRequired: exactly one of plan_id and
	// subscription_id must be set.
	CodeOneOfPlanOrSubscriptionRequired = "one_of_plan_or_subscription_required"
	// CodeMultipleRecurringThresholds: an owner has more than one recurring
	// usage threshold.
	CodeMultipleRecurringThresholds = "multiple_recurring_thresholds"
)

// UsageThreshold triggers a progressive-billing invoice when the usage
// amount of the current period reaches Amount (domain §7.9). It belongs to
// a plan or to a subscription, never both.
type UsageThreshold struct {
	// ID identifies the threshold.
	ID core.UsageThresholdID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// PlanID is the owning plan; zero for a subscription's threshold.
	PlanID core.PlanID
	// SubscriptionID is the owning subscription; zero for a plan's
	// threshold.
	SubscriptionID core.SubscriptionID
	// Amount is the usage amount that triggers the threshold, in minor
	// units of the plan's currency; > 0. For a recurring threshold it is
	// the step repeated after the largest non-recurring threshold.
	Amount int64
	// Recurring repeats the threshold every Amount beyond the largest
	// non-recurring threshold.
	Recurring bool
	// ThresholdDisplayName is printed on the progressive-billing invoice.
	ThresholdDisplayName *string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the invariants of one threshold (domain §7.9).
func (u *UsageThreshold) Validate() error {
	var v core.Validator
	v.Check(u.PlanID.IsZero() != u.SubscriptionID.IsZero(), "base", CodeOneOfPlanOrSubscriptionRequired)
	v.Positive("amount", u.Amount)
	v.OptionalString("threshold_display_name", u.ThresholdDisplayName, 255)
	return v.Err()
}

// ValidateUsageThresholds checks the thresholds of one owner as a set:
// each threshold is valid, non-recurring amounts are unique and there is at
// most one recurring threshold (domain §7.9). Errors are reported on
// "usage_thresholds[i]".
func ValidateUsageThresholds(ts []UsageThreshold) error {
	var v core.Validator
	seen := map[int64]bool{}
	recurring := 0
	for i, t := range ts {
		field := fmt.Sprintf("usage_thresholds[%d]", i)
		v.Merge(field, t.Validate())
		if t.Recurring {
			recurring++
			continue
		}
		v.Check(!seen[t.Amount], field+".amount", core.CodeDuplicated)
		seen[t.Amount] = true
	}
	v.Check(recurring <= 1, "usage_thresholds", CodeMultipleRecurringThresholds)
	return v.Err()
}

// EffectiveUsageThresholds returns the thresholds that drive progressive
// billing of a subscription (domain §7.9): none when progressive billing
// is disabled on the subscription, otherwise the subscription's own
// thresholds when it has any, else the plan's. Non-recurring thresholds
// come first by ascending amount, then the recurring one.
func EffectiveUsageThresholds(plan, subscription []UsageThreshold, progressiveBillingDisabled bool) []UsageThreshold {
	if progressiveBillingDisabled {
		return nil
	}
	src := plan
	if len(subscription) > 0 {
		src = subscription
	}
	out := slices.Clone(src)
	slices.SortStableFunc(out, func(a, b UsageThreshold) int {
		if a.Recurring != b.Recurring {
			if a.Recurring {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.Amount, b.Amount)
	})
	return out
}
