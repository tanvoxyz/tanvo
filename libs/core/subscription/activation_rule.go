package subscription

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// ActivationRule is a condition a subscription waits for before it becomes
// active (domain §9.3). Its status follows:
//
//	inactive → pending | not_applicable
//	pending  → satisfied | failed | expired | declined
type ActivationRule struct {
	// ID identifies the rule.
	ID core.ActivationRuleID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the gated subscription.
	SubscriptionID core.SubscriptionID
	// RuleType is the kind of condition (payment).
	RuleType ActivationRuleType
	// TimeoutHours bounds the wait (≥ 0; 0 means no timeout).
	TimeoutHours int
	// Status is the rule's status.
	Status ActivationRuleStatus
	// ExpiresAt is activation + timeout, set when the rule becomes pending
	// with a timeout.
	ExpiresAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewActivationRule returns an inactive rule of type t.
func NewActivationRule(org core.OrganizationID, id core.ActivationRuleID, sub core.SubscriptionID,
	t ActivationRuleType, timeoutHours int, now time.Time,
) *ActivationRule {
	return &ActivationRule{
		ID: id, OrganizationID: org, SubscriptionID: sub, RuleType: t, TimeoutHours: timeoutHours,
		Status: ActivationRuleStatusInactive, CreatedAt: now, UpdatedAt: now,
	}
}

// Validate checks the rule's invariants.
func (r *ActivationRule) Validate() error {
	var v core.Validator
	v.Enum("type", r.RuleType)
	v.Check(r.TimeoutHours >= 0, "timeout_hours", core.CodeOutOfRange)
	v.Enum("status", r.Status)
	return v.Err()
}

// CanTransitionRule reports whether an activation rule may go from one
// status to another.
func CanTransitionRule(from, to ActivationRuleStatus) bool {
	switch from {
	case ActivationRuleStatusInactive:
		return to == ActivationRuleStatusPending || to == ActivationRuleStatusNotApplicable
	case ActivationRuleStatusPending:
		switch to {
		case ActivationRuleStatusSatisfied, ActivationRuleStatusFailed, ActivationRuleStatusExpired, ActivationRuleStatusDeclined:
			return true
		}
	}
	return false
}

func (r *ActivationRule) transition(to ActivationRuleStatus, at time.Time) error {
	if !CanTransitionRule(r.Status, to) {
		return &core.TransitionError{Entity: "activation_rule", From: string(r.Status), To: string(to)}
	}
	r.Status = to
	r.UpdatedAt = at
	return nil
}

// Start makes an inactive rule pending at the activation instant at; with
// a timeout, ExpiresAt = at + timeout.
func (r *ActivationRule) Start(at time.Time) error {
	if err := r.transition(ActivationRuleStatusPending, at); err != nil {
		return err
	}
	if r.TimeoutHours > 0 {
		e := at.UTC().Add(time.Duration(r.TimeoutHours) * time.Hour)
		r.ExpiresAt = &e
	}
	return nil
}

// MarkNotApplicable resolves an inactive rule whose activation produces
// nothing to wait for.
func (r *ActivationRule) MarkNotApplicable(at time.Time) error {
	return r.transition(ActivationRuleStatusNotApplicable, at)
}

// Satisfy resolves a pending rule whose condition was met.
func (r *ActivationRule) Satisfy(at time.Time) error {
	return r.transition(ActivationRuleStatusSatisfied, at)
}

// Fail resolves a pending rule whose condition failed.
func (r *ActivationRule) Fail(at time.Time) error {
	return r.transition(ActivationRuleStatusFailed, at)
}

// Expire resolves a pending rule whose timeout elapsed.
func (r *ActivationRule) Expire(at time.Time) error {
	return r.transition(ActivationRuleStatusExpired, at)
}

// Decline resolves a pending rule whose subscription was terminated.
func (r *ActivationRule) Decline(at time.Time) error {
	return r.transition(ActivationRuleStatusDeclined, at)
}

// Fulfilled reports whether the rule is satisfied or not applicable.
func (r *ActivationRule) Fulfilled() bool {
	return r.Status == ActivationRuleStatusSatisfied || r.Status == ActivationRuleStatusNotApplicable
}

// Rejected reports whether the rule failed, expired or was declined.
func (r *ActivationRule) Rejected() bool {
	switch r.Status {
	case ActivationRuleStatusFailed, ActivationRuleStatusExpired, ActivationRuleStatusDeclined:
		return true
	default:
		return false
	}
}

// Expirable reports whether the rule is pending and its expiry is at or
// before now (lago Subscription::ActivationRule.expirable,
// app/models/subscription/activation_rule.rb:35).
func (r *ActivationRule) Expirable(now time.Time) bool {
	return r.Status == ActivationRuleStatusPending && r.ExpiresAt != nil && !r.ExpiresAt.After(now)
}

// ActivationFacts are the facts about an activation that decide whether a
// payment rule applies.
type ActivationFacts struct {
	// PlanPayInAdvance is the plan's pay_in_advance.
	PlanPayInAdvance bool
	// InTrial reports whether the subscription is in trial at activation.
	InTrial bool
	// HasInAdvanceFixedCharges reports whether the plan has fixed charges
	// paid in advance.
	HasInAdvanceFixedCharges bool
}

// PaymentRuleApplicable reports whether a payment rule applies: the
// activation produces an invoice to pay immediately, that is the plan is
// in advance and the subscription is not in trial, or the plan has
// in-advance fixed charges (domain §9.3).
func PaymentRuleApplicable(f ActivationFacts) bool {
	return (f.PlanPayInAdvance && !f.InTrial) || f.HasInAdvanceFixedCharges
}

// HasPendingRules reports whether any rule is pending.
func HasPendingRules(rules []*ActivationRule) bool {
	for _, r := range rules {
		if r.Status == ActivationRuleStatusPending {
			return true
		}
	}
	return false
}

// Gated reports whether s waits for a rule: it is incomplete and has a
// pending rule.
func Gated(s *Subscription, rules []*ActivationRule) bool {
	return s.Status == StatusIncomplete && HasPendingRules(rules)
}

// PaymentGated reports whether s waits for a payment rule: it is
// incomplete and has a pending payment rule; its starting invoice then
// stays open and unnumbered (domain §9.3).
func PaymentGated(s *Subscription, rules []*ActivationRule) bool {
	if s.Status != StatusIncomplete {
		return false
	}
	for _, r := range rules {
		if r.RuleType == ActivationRuleTypePayment && r.Status == ActivationRuleStatusPending {
			return true
		}
	}
	return false
}
