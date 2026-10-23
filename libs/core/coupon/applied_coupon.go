package coupon

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// AppliedCoupon is a coupon applied to a customer, with the terms captured
// at application (domain §8.2).
type AppliedCoupon struct {
	// ID identifies the applied coupon.
	ID core.AppliedCouponID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CouponID is the applied coupon.
	CouponID core.CouponID
	// CustomerID is the customer the coupon applies to.
	CustomerID core.CustomerID
	// Amount is the fixed discount per application, in minor units of
	// Currency; nil for a percentage coupon.
	Amount *int64
	// Currency is the currency of Amount, equal to the customer's currency.
	Currency money.Currency
	// PercentageRate is the discount in percent; nil for a fixed_amount
	// coupon.
	PercentageRate *money.Decimal
	// Frequency is once, recurring or forever.
	Frequency Frequency
	// FrequencyDuration is the number of billing periods of a recurring
	// coupon.
	FrequencyDuration *int
	// FrequencyDurationRemaining is the number of billing periods left of a
	// recurring coupon.
	FrequencyDurationRemaining *int
	// RemainingAmount is what is left of a once + fixed_amount coupon, in
	// minor units; decremented by each invoice and restored by voiding.
	RemainingAmount *int64
	// Status is active or terminated.
	Status Status
	// TerminatedAt is set when terminated.
	TerminatedAt *time.Time
	// CreatedAt is the application instant; deductions use the oldest
	// application first.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Overrides are the terms that may replace the coupon's at application.
// Unset fields keep the coupon's value.
type Overrides struct {
	// Amount replaces the coupon's amount, in minor units.
	Amount *int64
	// Currency replaces the coupon's currency.
	Currency money.Currency
	// PercentageRate replaces the coupon's rate.
	PercentageRate *money.Decimal
	// Frequency replaces the coupon's frequency.
	Frequency Frequency
	// FrequencyDuration replaces the coupon's duration.
	FrequencyDuration *int
}

// Apply returns an active applied coupon of c for a customer, capturing the
// coupon's terms overridden by o. FrequencyDurationRemaining starts at the
// duration and RemainingAmount at the amount of a once + fixed_amount
// coupon. It does not run [CheckApplication].
func Apply(c *Coupon, id core.AppliedCouponID, customer core.CustomerID, o Overrides, now time.Time) *AppliedCoupon {
	a := &AppliedCoupon{
		ID:                id,
		OrganizationID:    c.OrganizationID,
		CouponID:          c.ID,
		CustomerID:        customer,
		Frequency:         c.Frequency,
		FrequencyDuration: clonePtr(c.FrequencyDuration),
		Status:            StatusActive,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	switch c.Type {
	case TypeFixedAmount:
		a.Amount = clonePtr(c.Amount)
		a.Currency = c.Currency
		if o.Amount != nil {
			a.Amount = clonePtr(o.Amount)
		}
		if !o.Currency.IsZero() {
			a.Currency = o.Currency
		}
	case TypePercentage:
		a.PercentageRate = clonePtr(c.PercentageRate)
		if o.PercentageRate != nil {
			a.PercentageRate = clonePtr(o.PercentageRate)
		}
	}
	if o.Frequency != "" {
		a.Frequency = o.Frequency
	}
	if o.FrequencyDuration != nil {
		a.FrequencyDuration = clonePtr(o.FrequencyDuration)
	}
	if a.Frequency != FrequencyRecurring {
		a.FrequencyDuration = nil
	}
	a.FrequencyDurationRemaining = clonePtr(a.FrequencyDuration)
	if a.Frequency == FrequencyOnce && a.Amount != nil {
		a.RemainingAmount = clonePtr(a.Amount)
	}
	return a
}

// Percentage reports whether the applied coupon discounts a percentage.
func (a *AppliedCoupon) Percentage() bool { return a.PercentageRate != nil }

// Active reports whether the applied coupon still applies to invoices.
func (a *AppliedCoupon) Active() bool { return a.Status == StatusActive }

// Validate checks the invariants of domain §8.2.
func (a *AppliedCoupon) Validate() error {
	var v core.Validator
	v.Check(!a.CouponID.IsZero(), "coupon_id", core.CodeRequired)
	v.Check(!a.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	switch {
	case a.Amount != nil && a.PercentageRate != nil:
		v.Add("percentage_rate", core.CodeForbidden)
	case a.Amount != nil:
		v.NonNegative("amount_cents", *a.Amount)
		v.Currency("amount_currency", a.Currency)
	case a.PercentageRate != nil:
		r := *a.PercentageRate
		v.Check(r.IsPositive() && r.LessThanOrEqual(hundred), "percentage_rate", core.CodeOutOfRange)
	default:
		v.Add("amount_cents", core.CodeRequired)
	}
	if v.Enum("frequency", a.Frequency) && a.Frequency == FrequencyRecurring {
		if v.Check(a.FrequencyDuration != nil, "frequency_duration", core.CodeRequired) {
			v.Check(*a.FrequencyDuration > 0, "frequency_duration", core.CodeOutOfRange)
		}
		if v.Check(a.FrequencyDurationRemaining != nil, "frequency_duration_remaining", core.CodeRequired) {
			v.Check(*a.FrequencyDurationRemaining >= 0, "frequency_duration_remaining", core.CodeOutOfRange)
		}
	}
	if a.RemainingAmount != nil {
		v.NonNegative("remaining_amount", *a.RemainingAmount)
	}
	v.Enum("status", a.Status)
	return v.Err()
}

// Terminate terminates the applied coupon at now.
func (a *AppliedCoupon) Terminate(now time.Time) error {
	if !CanTransition(a.Status, StatusTerminated) {
		return &core.TransitionError{Entity: "applied_coupon", From: string(a.Status), To: string(StatusTerminated)}
	}
	a.Status = StatusTerminated
	a.TerminatedAt = &now
	a.UpdatedAt = now
	return nil
}

// ApplicationFacts are what [CheckApplication] needs to know about the
// customer.
type ApplicationFacts struct {
	// CustomerCurrency is the customer's currency; zero when unset, in which
	// case the caller sets it to the coupon's currency.
	CustomerCurrency money.Currency
	// AppliedCount is the number of coupons of this coupon ever applied to
	// the customer.
	AppliedCount int
	// Active are the customer's active applied coupons with their coupons.
	Active []*Coupon
	// PlanMetrics maps each plan to the billable metrics its charges use;
	// it must cover the plans of the new coupon and of Active.
	PlanMetrics map[core.PlanID][]core.BillableMetricID
}

// CheckApplication enforces the application rules of domain §8.2 for the
// applied coupon a of coupon c. It returns a *core.RuleError
// ([CodeCouponNotActive], [CodeNotReusable], [CodePlanOverlapping] or
// core.CodeCurrencyMismatch).
func CheckApplication(c *Coupon, a *AppliedCoupon, f ApplicationFacts) error {
	if c.Status != StatusActive {
		return core.NewRuleError(CodeCouponNotActive, "coupon is terminated")
	}
	if !c.Reusable && f.AppliedCount > 0 {
		return core.NewRuleError(CodeNotReusable, "coupon is not reusable and already applied to the customer")
	}
	if slices.ContainsFunc(f.Active, func(o *Coupon) bool { return Overlaps(c, o, f.PlanMetrics) }) {
		return core.NewRuleError(CodePlanOverlapping, "an applied coupon of the customer targets the same plans or metrics")
	}
	if a.Amount != nil && !f.CustomerCurrency.IsZero() && a.Currency.Code() != f.CustomerCurrency.Code() {
		return core.NewRuleError(core.CodeCurrencyMismatch, "coupon currency differs from the customer's")
	}
	return nil
}

// Overlaps reports whether limited coupons a and b target a common plan or
// metric: the same plan, the same metric, or a plan whose charges use a
// metric of the other (planMetrics). Unlimited coupons never overlap.
func Overlaps(a, b *Coupon, planMetrics map[core.PlanID][]core.BillableMetricID) bool {
	if !a.Limited() || !b.Limited() {
		return false
	}
	if slices.ContainsFunc(a.LimitedPlanIDs, func(p core.PlanID) bool { return slices.Contains(b.LimitedPlanIDs, p) }) {
		return true
	}
	if slices.ContainsFunc(a.LimitedBillableMetricIDs, func(m core.BillableMetricID) bool {
		return slices.Contains(b.LimitedBillableMetricIDs, m)
	}) {
		return true
	}
	planUses := func(plans []core.PlanID, metrics []core.BillableMetricID) bool {
		return slices.ContainsFunc(plans, func(p core.PlanID) bool {
			return slices.ContainsFunc(planMetrics[p], func(m core.BillableMetricID) bool { return slices.Contains(metrics, m) })
		})
	}
	return planUses(a.LimitedPlanIDs, b.LimitedBillableMetricIDs) || planUses(b.LimitedPlanIDs, a.LimitedBillableMetricIDs)
}

// Discount returns the discount of the applied coupon on base (minor units),
// per domain §22.7: a percentage gives min(base, round_half_up(base × rate
// / 100)); a recurring or forever fixed amount min(amount, base); a once
// fixed amount min(remaining amount, base). It is 0 when base ≤ 0 or the
// coupon is not active.
func (a *AppliedCoupon) Discount(base int64) int64 {
	if base <= 0 || !a.Active() {
		return 0
	}
	if a.Percentage() {
		d := money.NewFromInt(base).Mul(*a.PercentageRate).Div(hundred).Round(0, money.HalfUp)
		n, _ := d.Int64()
		return min(n, base)
	}
	if a.Frequency == FrequencyOnce && a.RemainingAmount != nil {
		return max(min(*a.RemainingAmount, base), 0)
	}
	if a.Amount == nil {
		return 0
	}
	return max(min(*a.Amount, base), 0)
}

// Consume records that an invoice deducted credit (minor units) at now, per
// the "after application" rules of domain §22.7: a recurring coupon
// decrements its remaining duration and terminates at 0; a once coupon
// decrements its remaining amount and terminates when it is a percentage
// or the amount is used up; a forever coupon never terminates.
func (a *AppliedCoupon) Consume(credit int64, now time.Time) error {
	if !a.Active() {
		return &core.TransitionError{Entity: "applied_coupon", From: string(a.Status), To: string(StatusTerminated)}
	}
	a.UpdatedAt = now
	switch a.Frequency {
	case FrequencyRecurring:
		r := 0
		if a.FrequencyDurationRemaining != nil {
			r = max(*a.FrequencyDurationRemaining-1, 0)
		}
		a.FrequencyDurationRemaining = &r
		if r == 0 {
			return a.Terminate(now)
		}
	case FrequencyOnce:
		if a.Percentage() {
			return a.Terminate(now)
		}
		if a.RemainingAmount != nil {
			r := max(*a.RemainingAmount-credit, 0)
			a.RemainingAmount = &r
			if r == 0 {
				return a.Terminate(now)
			}
		}
	case FrequencyForever:
	}
	return nil
}

// Restore gives back what a voided invoice consumed (domain §11.4): credit
// returns to the remaining amount of a once coupon, a recurring coupon
// gets one more period, and a terminated applied coupon is reactivated
// unless it is forever (it never terminates by use) or couponTerminated
// (its coupon is no longer valid).
func (a *AppliedCoupon) Restore(credit int64, couponTerminated bool, now time.Time) {
	switch a.Frequency {
	case FrequencyOnce:
		if a.RemainingAmount != nil {
			r := *a.RemainingAmount + credit
			if a.Amount != nil {
				r = min(r, *a.Amount)
			}
			a.RemainingAmount = &r
		}
	case FrequencyRecurring:
		r := 1
		if a.FrequencyDurationRemaining != nil {
			r = *a.FrequencyDurationRemaining + 1
		}
		a.FrequencyDurationRemaining = &r
	case FrequencyForever:
	}
	if a.Status == StatusTerminated && a.Frequency != FrequencyForever && !couponTerminated {
		a.Status = StatusActive
		a.TerminatedAt = nil
	}
	a.UpdatedAt = now
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
