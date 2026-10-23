package coupon

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Stable codes of coupon rules (lago's codes).
const (
	// CodeOnlyOneLimitationType: a coupon targets plans or billable
	// metrics, not both.
	CodeOnlyOneLimitationType = "only_one_limitation_type_per_coupon_allowed"
	// CodeNotReusable: a non-reusable coupon is already applied to the
	// customer (a validation error on field "coupon").
	CodeNotReusable = "coupon_is_not_reusable"
	// CodePlanOverlapping: an active applied coupon of the customer already
	// targets one of the same plans or metrics.
	CodePlanOverlapping = "plan_overlapping"
)

// Coupon is a discount that can be applied to customers (domain §8.1).
type Coupon struct {
	// ID identifies the coupon.
	ID core.CouponID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique among the organization's coupons that are not
	// deleted; required, ≤ 255 characters.
	Code string
	// Name is required, ≤ 255 characters.
	Name string
	// Description is free text.
	Description *string
	// Type is fixed_amount or percentage.
	Type Type
	// Amount is the discount of a fixed_amount coupon, in minor units of
	// Currency; required (> 0) for fixed_amount.
	Amount *int64
	// Currency is the currency of Amount; required for fixed_amount.
	Currency money.Currency
	// PercentageRate is the discount of a percentage coupon, in percent;
	// required for percentage, 0 < rate ≤ 100.
	PercentageRate *money.Decimal
	// Frequency is once, recurring or forever.
	Frequency Frequency
	// FrequencyDuration is the number of billing periods of a recurring
	// coupon; required (> 0) for recurring.
	FrequencyDuration *int
	// Reusable allows applying the coupon several times to a customer;
	// true by default.
	Reusable bool
	// Expiration is no_expiration or time_limit.
	Expiration Expiration
	// ExpirationAt is the expiry instant of a time_limit coupon.
	ExpirationAt *time.Time
	// LimitedPlanIDs restrict the coupon to fees of these plans (and their
	// children).
	LimitedPlanIDs []core.PlanID
	// LimitedBillableMetricIDs restrict the coupon to charge fees of these
	// metrics. A coupon targets plans or metrics, not both.
	LimitedBillableMetricIDs []core.BillableMetricID
	// Status is active or terminated.
	Status Status
	// TerminatedAt is set when terminated.
	TerminatedAt *time.Time
	// DeletedAt is set when deleted: a deleted coupon keeps its status,
	// is hidden from reads by code and from lists, and frees its code; its
	// applied coupons still reference it.
	DeletedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// New returns an active, reusable, non-expiring coupon applied once.
func New(org core.OrganizationID, id core.CouponID, code, name string, typ Type, now time.Time) *Coupon {
	return &Coupon{
		ID:             id,
		OrganizationID: org,
		Code:           code,
		Name:           name,
		Type:           typ,
		Frequency:      FrequencyOnce,
		Reusable:       true,
		Expiration:     ExpirationNoExpiration,
		Status:         StatusActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

var hundred = money.NewFromInt(100)

// Validate checks the invariants of domain §8.1. now is used for the
// "expiration in the future" rule; pass the zero time to skip it (for
// example when revalidating a stored coupon).
func (c *Coupon) Validate(now time.Time) error {
	var v core.Validator
	v.Code("code", c.Code, 255)
	v.String("name", c.Name, 255)
	v.Enum("coupon_type", c.Type)
	switch c.Type {
	case TypeFixedAmount:
		if v.Check(c.Amount != nil, "amount_cents", core.CodeRequired) {
			v.Positive("amount_cents", *c.Amount)
		}
		v.Check(!c.Currency.IsZero(), "amount_currency", core.CodeRequired)
	case TypePercentage:
		if v.Check(c.PercentageRate != nil, "percentage_rate", core.CodeRequired) {
			r := *c.PercentageRate
			v.Check(r.IsPositive() && r.LessThanOrEqual(hundred), "percentage_rate", core.CodeOutOfRange)
		}
	}
	if v.Enum("frequency", c.Frequency) && c.Frequency == FrequencyRecurring {
		if v.Check(c.FrequencyDuration != nil, "frequency_duration", core.CodeRequired) {
			v.Check(*c.FrequencyDuration > 0, "frequency_duration", core.CodeOutOfRange)
		}
	}
	if v.Enum("expiration", c.Expiration) && c.Expiration == ExpirationTimeLimit {
		if v.Check(c.ExpirationAt != nil, "expiration_at", core.CodeRequired) && !now.IsZero() {
			v.Future("expiration_at", *c.ExpirationAt, now)
		}
	}
	if len(c.LimitedPlanIDs) > 0 && len(c.LimitedBillableMetricIDs) > 0 {
		v.Add("limitations", CodeOnlyOneLimitationType)
	}
	v.Check(!hasDuplicates(c.LimitedPlanIDs), "limited_plans", core.CodeDuplicated)
	v.Check(!hasDuplicates(c.LimitedBillableMetricIDs), "limited_billable_metrics", core.CodeDuplicated)
	v.Enum("status", c.Status)
	return v.Err()
}

// LimitedToPlans reports whether the coupon targets plans.
func (c *Coupon) LimitedToPlans() bool { return len(c.LimitedPlanIDs) > 0 }

// LimitedToBillableMetrics reports whether the coupon targets metrics.
func (c *Coupon) LimitedToBillableMetrics() bool { return len(c.LimitedBillableMetricIDs) > 0 }

// Limited reports whether the coupon targets plans or metrics.
func (c *Coupon) Limited() bool { return c.LimitedToPlans() || c.LimitedToBillableMetrics() }

// Expired reports whether a time_limit coupon is past its expiration at
// now; the periodic job terminates such coupons (domain §8.1).
func (c *Coupon) Expired(now time.Time) bool {
	return c.Expiration == ExpirationTimeLimit && c.ExpirationAt != nil && !now.Before(*c.ExpirationAt)
}

// CanTransition reports whether a coupon or applied coupon may move from
// one status to another: only active → terminated.
func CanTransition(from, to Status) bool { return from == StatusActive && to == StatusTerminated }

// Terminate terminates the coupon at now. Terminating a coupon does not
// affect its applied coupons (domain §8.1).
func (c *Coupon) Terminate(now time.Time) error {
	if !CanTransition(c.Status, StatusTerminated) {
		return &core.TransitionError{Entity: "coupon", From: string(c.Status), To: string(StatusTerminated)}
	}
	c.Status = StatusTerminated
	c.TerminatedAt = &now
	c.UpdatedAt = now
	return nil
}

// Deleted reports whether the coupon is deleted.
func (c *Coupon) Deleted() bool { return c.DeletedAt != nil }

// Delete marks the coupon deleted at now; its status is unchanged.
func (c *Coupon) Delete(now time.Time) {
	c.DeletedAt = &now
	c.UpdatedAt = now
}

// KeepLocked gives next the fields of old that are locked once a coupon
// is applied: every field but the name, description and expiration
// (domain §8.1). Changes to locked fields are ignored, not rejected (lago
// Coupons::UpdateService).
func KeepLocked(old, next *Coupon) {
	next.Code, next.Type = old.Code, old.Type
	next.Amount, next.Currency, next.PercentageRate = old.Amount, old.Currency, old.PercentageRate
	next.Frequency, next.FrequencyDuration, next.Reusable = old.Frequency, old.FrequencyDuration, old.Reusable
	next.LimitedPlanIDs = slices.Clone(old.LimitedPlanIDs)
	next.LimitedBillableMetricIDs = slices.Clone(old.LimitedBillableMetricIDs)
}

func hasDuplicates[T comparable](s []T) bool {
	seen := make(map[T]bool, len(s))
	for _, x := range s {
		if seen[x] {
			return true
		}
		seen[x] = true
	}
	return false
}
