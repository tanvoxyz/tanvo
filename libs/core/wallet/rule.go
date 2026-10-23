package wallet

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// CodeTargetBelowThreshold: a target rule triggered by a threshold aims
// below that threshold.
const CodeTargetBelowThreshold = "must_be_greater_than_or_equal_threshold"

// ThresholdSuppression is how long threshold triggers of a rule stay
// suppressed after a failed paid top-up (domain §14.3).
const ThresholdSuppression = time.Hour

// RecurringTransactionRule tops a wallet up at each interval or when its
// ongoing balance falls to a threshold (domain §14.3).
type RecurringTransactionRule struct {
	// ID identifies the rule.
	ID core.RecurringTransactionRuleID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// WalletID is the wallet topped up.
	WalletID core.WalletID
	// Trigger tells when the rule runs.
	Trigger RecurringTrigger
	// Interval is the schedule of an interval trigger; required for it.
	Interval period.Interval
	// ThresholdCredits is the ongoing balance at or below which a threshold
	// trigger runs; required for it.
	ThresholdCredits *money.Decimal
	// Method tells how many credits a run adds.
	Method RecurringMethod
	// PaidCredits are the credits bought by a fixed run; ≥ 0.
	PaidCredits money.Decimal
	// GrantedCredits are the credits granted by a fixed run; ≥ 0.
	GrantedCredits money.Decimal
	// TargetOngoingBalance is the balance a target run tops up to; required
	// for target, ≥ ThresholdCredits for a threshold trigger.
	TargetOngoingBalance *money.Decimal
	// GrantsTargetTopUp grants target top-ups instead of buying them; only
	// for target.
	GrantsTargetTopUp bool
	// StartedAt is the first interval anchor; the wallet creation when nil.
	StartedAt *time.Time
	// ExpirationAt is the instant the rule stops running.
	ExpirationAt *time.Time
	// Status is the lifecycle status.
	Status RuleStatus
	// InvoiceRequiresSuccessfulPayment is copied to the transactions.
	InvoiceRequiresSuccessfulPayment bool
	// TransactionMetadata is copied to the transactions; ≤ 15 items.
	TransactionMetadata core.Metadata
	// TransactionName is copied to the transactions, 1–255 characters.
	TransactionName *string
	// IgnorePaidTopUpLimits skips the wallet's paid top-up limits.
	IgnorePaidTopUpLimits bool
	// PaymentMethodType is how the credit invoices are collected; "" inherits
	// the wallet's.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is the payment method of the credit invoices.
	PaymentMethodID core.PaymentMethodID
	// TerminatedAt is set once terminated.
	TerminatedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the invariants of domain §14.3.
func (r *RecurringTransactionRule) Validate() error {
	var v core.Validator
	v.Enum("trigger", r.Trigger)
	v.Enum("method", r.Method)
	v.Enum("status", r.Status)
	switch r.Trigger {
	case RecurringTriggerInterval:
		v.Check(r.Interval.Valid(), "interval", core.CodeRequired)
	case RecurringTriggerThreshold:
		if v.Check(r.ThresholdCredits != nil, "threshold_credits", core.CodeRequired) {
			v.DecimalNonNegative("threshold_credits", *r.ThresholdCredits)
		}
	}
	v.DecimalNonNegative("paid_credits", r.PaidCredits)
	v.DecimalNonNegative("granted_credits", r.GrantedCredits)
	if r.Method == RecurringMethodTarget {
		if v.Check(r.TargetOngoingBalance != nil, "target_ongoing_balance", core.CodeRequired) &&
			r.Trigger == RecurringTriggerThreshold && r.ThresholdCredits != nil {
			v.Check(r.TargetOngoingBalance.Cmp(*r.ThresholdCredits) >= 0, "target_ongoing_balance", CodeTargetBelowThreshold)
		}
	} else {
		v.Check(!r.GrantsTargetTopUp, "grants_target_top_up", core.CodeForbidden)
	}
	if r.TransactionName != nil && v.Required("transaction_name", *r.TransactionName) {
		v.MaxLen("transaction_name", *r.TransactionName, 255)
	}
	v.Merge("transaction_metadata", r.TransactionMetadata.Validate(core.WalletTransactionMetadataLimits()))
	v.OptionalEnum("payment_method_type", r.PaymentMethodType)
	return v.Err()
}

// CurrentlyActive reports whether the rule is active and not expired at
// now.
func (r *RecurringTransactionRule) CurrentlyActive(now time.Time) bool {
	return r.Status == RuleStatusActive && (r.ExpirationAt == nil || r.ExpirationAt.After(now))
}

// EligibleForTermination reports whether the rule is active but its
// expiration has passed at now.
func (r *RecurringTransactionRule) EligibleForTermination(now time.Time) bool {
	return r.Status == RuleStatusActive && r.ExpirationAt != nil && !r.ExpirationAt.After(now)
}

// CanRuleTransition reports whether a rule may go from one status to
// another: only active → terminated.
func CanRuleTransition(from, to RuleStatus) bool {
	return from == RuleStatusActive && to == RuleStatusTerminated
}

// Terminate terminates the rule at now, keeping an earlier TerminatedAt.
func (r *RecurringTransactionRule) Terminate(now time.Time) error {
	if !CanRuleTransition(r.Status, RuleStatusTerminated) {
		return &core.TransitionError{Entity: "recurring_transaction_rule", From: string(r.Status), To: string(RuleStatusTerminated)}
	}
	r.Status = RuleStatusTerminated
	if r.TerminatedAt == nil {
		r.TerminatedAt = &now
	}
	r.UpdatedAt = now
	return nil
}

// ThresholdReached reports whether a threshold rule should run for the
// ongoing credits balance: at or below ThresholdCredits.
func (r *RecurringTransactionRule) ThresholdReached(ongoingCredits money.Decimal) bool {
	return r.Trigger == RecurringTriggerThreshold && r.ThresholdCredits != nil && ongoingCredits.Cmp(*r.ThresholdCredits) <= 0
}

// ApplyMinTopUpLimit raises credits to the wallet's minimum paid top-up,
// unless the rule ignores the limits.
func (r *RecurringTransactionRule) ApplyMinTopUpLimit(w *Wallet, credits money.Decimal) money.Decimal {
	if m := w.PaidTopUpMinCredits(); m != nil && !r.IgnorePaidTopUpLimits {
		return money.Max(credits, *m)
	}
	return credits
}

// ApplyMaxTopUpLimit lowers credits to the wallet's maximum paid top-up,
// unless the rule ignores the limits.
func (r *RecurringTransactionRule) ApplyMaxTopUpLimit(w *Wallet, credits money.Decimal) money.Decimal {
	if m := w.PaidTopUpMaxCredits(); m != nil && !r.IgnorePaidTopUpLimits {
		return money.Min(credits, *m)
	}
	return credits
}

// ComputePaidCredits returns the credits a run buys for wallet w, given its
// ongoing credits balance and the credits of pending purchases (domain
// §14.3):
//   - target: the gap to the target (raised to the wallet's minimum top-up),
//     or 0 when the rule grants its top-ups;
//   - fixed, threshold trigger: PaidCredits, or, when the gap
//     threshold − ongoing − granted − pending is ≥ PaidCredits,
//     PaidCredits × (⌊gap / PaidCredits⌋ + 1) capped by the wallet's
//     maximum top-up;
//   - fixed, interval trigger: PaidCredits.
func (r *RecurringTransactionRule) ComputePaidCredits(w *Wallet, ongoing, pending money.Decimal) money.Decimal {
	switch {
	case r.Method == RecurringMethodTarget:
		if r.GrantsTargetTopUp {
			return money.Decimal{}
		}
		return r.targetGap(w, ongoing)
	case r.Trigger == RecurringTriggerThreshold:
		if r.PaidCredits.IsZero() || r.ThresholdCredits == nil {
			return r.PaidCredits
		}
		gap := r.ThresholdCredits.Sub(ongoing).Sub(r.GrantedCredits).Sub(pending)
		if gap.Cmp(r.PaidCredits) < 0 {
			return r.PaidCredits
		}
		n := gap.Div(r.PaidCredits).Round(0, money.Floor).Add(money.NewFromInt(1))
		return r.ApplyMaxTopUpLimit(w, r.PaidCredits.Mul(n))
	default:
		return r.PaidCredits
	}
}

// ComputeGrantedCredits returns the credits a run grants to wallet w: the gap to
// the target for a target rule that grants its top-ups (no limits apply),
// 0 for other target rules, GrantedCredits for fixed rules.
func (r *RecurringTransactionRule) ComputeGrantedCredits(w *Wallet) money.Decimal {
	if r.Method != RecurringMethodTarget {
		return r.GrantedCredits
	}
	if !r.GrantsTargetTopUp {
		return money.Decimal{}
	}
	return r.targetGap(w, w.CreditsOngoingBalance)
}

// targetGap returns target − ongoing, or 0 when the target is reached. Paid
// top-ups are raised to the wallet's minimum; the maximum never applies, as
// reaching the target matters most.
func (r *RecurringTransactionRule) targetGap(w *Wallet, ongoing money.Decimal) money.Decimal {
	if r.TargetOngoingBalance == nil || ongoing.Cmp(*r.TargetOngoingBalance) >= 0 {
		return money.Decimal{}
	}
	gap := r.TargetOngoingBalance.Sub(ongoing)
	if r.GrantsTargetTopUp {
		return gap
	}
	return r.ApplyMinTopUpLimit(w, gap)
}

// ThresholdSuppressed reports whether threshold triggers of a rule are
// suppressed at now because a paid top-up failed at lastFailure (nil when
// none) less than [ThresholdSuppression] ago.
func ThresholdSuppressed(lastFailure *time.Time, now time.Time) bool {
	return lastFailure != nil && now.Sub(*lastFailure) < ThresholdSuppression
}
