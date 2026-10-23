package subscription

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Codes of refused subscription operations.
const (
	// CodeNotEditable: the field cannot be changed in the current status.
	CodeNotEditable = "subscription_not_editable"
	// CodeRulesNotEditable: activation rules change only while pending.
	CodeRulesNotEditable = "activation_rules_not_editable"
	// CodePaymentMethodRequired: a payment rule needs a provider payment
	// method, explicit or the customer default.
	CodePaymentMethodRequired = "provider_payment_method_required"
)

// CanEdit reports whether field can be changed on a subscription in status
// st (domain §9.5): subscription_at only while pending; every other field
// while pending or active.
func CanEdit(st Status, field EditableField) bool {
	switch field {
	case EditableFieldSubscriptionAt:
		return st == StatusPending
	default:
		return st == StatusPending || st == StatusActive
	}
}

// CheckEditable returns a *core.ValidationError with [core.CodeImmutable]
// for each of fields that s cannot change, or nil.
func CheckEditable(s *Subscription, fields ...EditableField) error {
	var v core.Validator
	for _, f := range fields {
		v.Check(CanEdit(s.Status, f), string(f), core.CodeImmutable)
	}
	return v.Err()
}

// GroupingKey is the comparable key under which subscriptions of a customer
// billed at the same instant share an invoice (domain §9.6): same payment
// method, currency, billing entity and purchase order number, and all
// consolidated. A non-consolidated subscription gets a key of its own
// (Solo).
type GroupingKey struct {
	// CustomerID is the customer.
	CustomerID core.CustomerID
	// PaymentMethodType is the subscription's payment method type.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is the subscription's explicit payment method.
	PaymentMethodID core.PaymentMethodID
	// Currency is the ISO code of the plan's currency.
	Currency string
	// BillingEntityID is the applicable billing entity.
	BillingEntityID core.BillingEntityID
	// PurchaseOrderNumber is the purchase order number ("" when none).
	PurchaseOrderNumber string
	// Solo is the subscription's own ID when it is not consolidated.
	Solo core.SubscriptionID
}

// Grouping returns the grouping key of s, given its plan's currency and
// its customer's billing entity.
func Grouping(s *Subscription, currency money.Currency, customerEntity core.BillingEntityID) GroupingKey {
	k := GroupingKey{
		CustomerID:        s.CustomerID,
		PaymentMethodType: s.PaymentMethodType,
		PaymentMethodID:   s.PaymentMethodID,
		Currency:          currency.Code(),
		BillingEntityID:   s.ApplicableBillingEntity(customerEntity),
	}
	if s.PurchaseOrderNumber != nil {
		k.PurchaseOrderNumber = *s.PurchaseOrderNumber
	}
	if !s.ConsolidateInvoice {
		k.Solo = s.ID
	}
	return k
}

// MatchEvent returns the subscription an event received at t for an
// external subscription ID belongs to (domain §10.2), among candidates
// sharing that external ID: an active or terminated one with
// started_at ≤ t < terminated_at (active ones have no end). pending,
// incomplete and canceled subscriptions never match. When none matches
// and recurringFallback is set (recurring metrics), the active one is
// returned. It returns nil when nothing matches.
func MatchEvent(candidates []*Subscription, t time.Time, recurringFallback bool) *Subscription {
	var match, active *Subscription
	for _, s := range candidates {
		if s.Status != StatusActive && s.Status != StatusTerminated {
			continue
		}
		if s.Status == StatusActive {
			active = s
		}
		if s.StartedAt == nil || s.StartedAt.After(t) {
			continue
		}
		if s.Status == StatusTerminated && (s.TerminatedAt == nil || !t.Before(*s.TerminatedAt)) {
			continue
		}
		if match == nil || s.StartedAt.After(*match.StartedAt) {
			match = s
		}
	}
	if match == nil && recurringFallback {
		return active
	}
	return match
}

// CheckActivationRulesEditable refuses a change of activation rules unless
// s is pending (domain §9.3).
func CheckActivationRulesEditable(s *Subscription) error {
	if s.Status != StatusPending {
		return core.NewRuleError(CodeRulesNotEditable, "activation rules can change only while the subscription is pending")
	}
	return nil
}

// CheckPaymentRule refuses a payment activation rule when the subscription
// would not be collected by a provider: its payment method type is manual,
// or it has no explicit method and the customer has no default provider
// method (domain §9.3).
func CheckPaymentRule(s *Subscription, customerHasDefaultProviderMethod bool) error {
	if s.PaymentMethodType == core.PaymentMethodTypeManual ||
		(s.PaymentMethodID.IsZero() && !customerHasDefaultProviderMethod) {
		return core.NewRuleError(CodePaymentMethodRequired, "a payment activation rule requires a provider payment method")
	}
	return nil
}
