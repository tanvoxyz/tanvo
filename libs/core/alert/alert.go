package alert

import (
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// MaxThresholds bounds the thresholds of an alert (lago
// AlertThreshold::SOFT_LIMIT).
const MaxThresholds = 20

// Field error codes of alerts (lago's names).
const (
	// CodeTooManyThresholds: more than [MaxThresholds] thresholds.
	CodeTooManyThresholds = "too_many_thresholds"
	// CodeDuplicateThresholdValues: two thresholds share a value.
	CodeDuplicateThresholdValues = "duplicate_threshold_values"
	// CodeRecurringValueIsNegative: the recurring step is not positive.
	CodeRecurringValueIsNegative = "recurring_value_is_negative"
	// CodeMustBeBlank: the field must be empty for the alert type.
	CodeMustBeBlank = "value_must_be_blank"
	// CodeInvalidType: the alert type does not fit the alerted object.
	CodeInvalidType = "invalid_type"
)

// IsWalletType reports whether alerts of type t watch a wallet.
func (t Type) IsWalletType() bool {
	switch t {
	case TypeWalletBalanceAmount, TypeWalletCreditsBalance, TypeWalletOngoingBalanceAmount, TypeWalletCreditsOngoingBalance:
		return true
	default:
		return false
	}
}

// UsesBillableMetric reports whether alerts of type t watch one billable
// metric.
func (t Type) UsesBillableMetric() bool {
	switch t {
	case TypeBillableMetricCurrentUsageAmount, TypeBillableMetricCurrentUsageUnits, TypeBillableMetricLifetimeUsageUnits:
		return true
	default:
		return false
	}
}

// IsCurrentUsage reports whether alerts of type t watch the current
// period's usage, which resets at each period start.
func (t Type) IsCurrentUsage() bool {
	switch t {
	case TypeCurrentUsageAmount, TypeBillableMetricCurrentUsageAmount, TypeBillableMetricCurrentUsageUnits:
		return true
	default:
		return false
	}
}

// IsLifetimeUsage reports whether alerts of type t watch lifetime usage,
// which never resets.
func (t Type) IsLifetimeUsage() bool {
	return t == TypeLifetimeUsageAmount || t == TypeBillableMetricLifetimeUsageUnits
}

// DefaultDirection returns the direction of a new alert of type t:
// decreasing for wallet balances, increasing for usage.
func (t Type) DefaultDirection() Direction {
	if t.IsWalletType() {
		return DirectionDecreasing
	}
	return DirectionIncreasing
}

// Threshold is one threshold of an alert. At most one threshold is
// recurring: it repeats every Value beyond the last one-time threshold.
type Threshold struct {
	// ID identifies the threshold.
	ID core.AlertThresholdID
	// Code is an optional label sent in alert.triggered.
	Code *string
	// Value is the threshold, or the step of a recurring threshold.
	Value money.Decimal
	// Recurring marks the recurring threshold.
	Recurring bool
}

// Alert watches a value of a subscription or wallet (domain §19.1). Its
// code is unique per subscription (or wallet).
type Alert struct {
	// ID identifies the alert.
	ID core.AlertID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique per subscription or wallet, ≤ 255 characters.
	Code string
	// Name is an optional label.
	Name *string
	// Type is what the alert watches.
	Type Type
	// SubscriptionExternalID is the watched subscription (usage types).
	SubscriptionExternalID string
	// WalletID is the watched wallet (wallet types).
	WalletID core.WalletID
	// BillableMetricID is the watched metric (billable_metric_* types).
	BillableMetricID core.BillableMetricID
	// Direction is the direction in which thresholds are crossed.
	Direction Direction
	// Thresholds are the 1–[MaxThresholds] thresholds.
	Thresholds []Threshold
	// PreviousValue is the value of the last evaluation.
	PreviousValue money.Decimal
	// LastProcessedAt is the instant of the last evaluation.
	LastProcessedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the invariants of domain §19.1.
func (a *Alert) Validate() error {
	var v core.Validator
	v.Code("code", a.Code, 255)
	v.OptionalString("name", a.Name, 255)
	if v.Enum("alert_type", a.Type) {
		if a.Type.IsWalletType() {
			v.Check(!a.WalletID.IsZero(), "wallet_id", core.CodeRequired)
			v.Check(a.SubscriptionExternalID == "", "subscription_external_id", CodeMustBeBlank)
		} else {
			v.Required("subscription_external_id", a.SubscriptionExternalID)
			v.Check(a.WalletID.IsZero(), "wallet_id", CodeMustBeBlank)
		}
		if a.Type.UsesBillableMetric() {
			v.Check(!a.BillableMetricID.IsZero(), "billable_metric_id", core.CodeRequired)
		} else {
			v.Check(a.BillableMetricID.IsZero(), "billable_metric_id", CodeMustBeBlank)
		}
		if v.Enum("direction", a.Direction) && a.Direction == DirectionDecreasing {
			v.Check(a.Type.IsWalletType(), "direction", core.CodeForbidden)
		}
	} else {
		v.Enum("direction", a.Direction)
	}
	validateThresholds(&v, a.Thresholds)
	return v.Err()
}

func validateThresholds(v *core.Validator, ths []Threshold) {
	if !v.Check(len(ths) > 0, "thresholds", core.CodeRequired) || !v.Check(len(ths) <= MaxThresholds, "thresholds", CodeTooManyThresholds) {
		return
	}
	recurring := 0
	var values []money.Decimal
	for i, th := range ths {
		if th.Recurring {
			recurring++
			v.Check(th.Value.IsPositive(), indexPath("thresholds", i, "value"), CodeRecurringValueIsNegative)
			continue
		}
		if slices.ContainsFunc(values, th.Value.Equal) {
			v.Add("thresholds", CodeDuplicateThresholdValues)
		}
		values = append(values, th.Value)
		if th.Code != nil {
			v.MaxLen(indexPath("thresholds", i, "code"), *th.Code, 255)
		}
	}
	v.Check(recurring <= 1, "thresholds", core.CodeTooMany)
}

// RecurringThreshold returns the recurring threshold, if any.
func (a *Alert) RecurringThreshold() (Threshold, bool) {
	i := slices.IndexFunc(a.Thresholds, func(t Threshold) bool { return t.Recurring })
	if i < 0 {
		return Threshold{}, false
	}
	return a.Thresholds[i], true
}

// OneTimeValues returns the sorted, de-duplicated values of the one-time
// thresholds.
func (a *Alert) OneTimeValues() []money.Decimal {
	var out []money.Decimal
	for _, t := range a.Thresholds {
		if !t.Recurring && !slices.ContainsFunc(out, t.Value.Equal) {
			out = append(out, t.Value)
		}
	}
	slices.SortFunc(out, money.Decimal.Cmp)
	return out
}

// Evaluate compares current with PreviousValue at now (domain §19.2): it
// returns the triggered alert to record, or nil when no threshold is
// crossed, and in both cases stores current as PreviousValue and now as
// LastProcessedAt.
func (a *Alert) Evaluate(id core.TriggeredAlertID, subscription core.SubscriptionID, current money.Decimal, now time.Time) *TriggeredAlert {
	previous := a.PreviousValue
	crossed := Crossed(a, previous, current)
	a.PreviousValue = current
	a.LastProcessedAt = &now
	a.UpdatedAt = now
	if len(crossed) == 0 {
		return nil
	}
	return &TriggeredAlert{
		ID:                id,
		OrganizationID:    a.OrganizationID,
		AlertID:           a.ID,
		SubscriptionID:    subscription,
		WalletID:          a.WalletID,
		CurrentValue:      current,
		PreviousValue:     previous,
		CrossedThresholds: a.Format(crossed),
		TriggeredAt:       now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

// ResetForPeriod sets PreviousValue to zero at the start of a billing
// period for current-usage alerts; other alerts are unchanged.
func (a *Alert) ResetForPeriod(now time.Time) {
	if a.Type.IsCurrentUsage() {
		a.PreviousValue = money.Decimal{}
		a.UpdatedAt = now
	}
}

// CrossedThreshold is one threshold reported by a triggered alert.
type CrossedThreshold struct {
	// Code is the threshold's code.
	Code *string
	// Value is the crossed value (a multiple of the step for a recurring
	// threshold).
	Value money.Decimal
	// Recurring reports whether the value comes from the recurring
	// threshold.
	Recurring bool
}

// Format describes crossed values (lago formatted_crossed_thresholds):
// values of one-time thresholds first, with their codes, then the other
// values as occurrences of the recurring threshold.
func (a *Alert) Format(values []money.Decimal) []CrossedThreshold {
	oneTime := a.OneTimeValues()
	var regular, recurring []money.Decimal
	for _, v := range values {
		if slices.ContainsFunc(oneTime, v.Equal) {
			regular = append(regular, v)
		} else {
			recurring = append(recurring, v)
		}
	}
	out := []CrossedThreshold{}
	for _, t := range a.Thresholds {
		if !t.Recurring && slices.ContainsFunc(regular, t.Value.Equal) {
			out = append(out, CrossedThreshold{Code: t.Code, Value: t.Value})
		}
	}
	rec, _ := a.RecurringThreshold()
	for _, v := range recurring {
		out = append(out, CrossedThreshold{Code: rec.Code, Value: v, Recurring: true})
	}
	return out
}

// TriggeredAlert records one evaluation that crossed thresholds (domain
// §19.2).
type TriggeredAlert struct {
	// ID identifies the record.
	ID core.TriggeredAlertID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// AlertID is the alert.
	AlertID core.AlertID
	// SubscriptionID is the watched subscription (usage alerts).
	SubscriptionID core.SubscriptionID
	// WalletID is the watched wallet (wallet alerts).
	WalletID core.WalletID
	// CurrentValue is the evaluated value.
	CurrentValue money.Decimal
	// PreviousValue is the value of the previous evaluation.
	PreviousValue money.Decimal
	// CrossedThresholds are the thresholds crossed.
	CrossedThresholds []CrossedThreshold
	// TriggeredAt is the evaluation instant.
	TriggeredAt time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

func indexPath(list string, i int, field string) string {
	return fmt.Sprintf("%s[%d].%s", list, i, field)
}
