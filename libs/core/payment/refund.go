package payment

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Refund returns money of a payment through the provider that collected it
// (domain §15.3).
type Refund struct {
	// ID identifies the refund.
	ID core.RefundID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// PaymentID is the refunded payment.
	PaymentID core.PaymentID
	// Reason tells why the refund was issued.
	Reason RefundReason
	// CreditNoteID is the credit note whose refund amount is refunded.
	CreditNoteID core.CreditNoteID
	// SubscriptionID is the payment-gated subscription whose activation
	// expired, for that reason.
	SubscriptionID core.SubscriptionID
	// IntegrationID is the provider connection that collected the payment.
	IntegrationID core.IntegrationID
	// PaymentProviderCustomerID is the customer's connection to it.
	PaymentProviderCustomerID core.PaymentProviderCustomerID
	// Amount is the refunded amount, in minor units of Currency; > 0.
	Amount int64
	// Currency is the payment's currency.
	Currency money.Currency
	// Status follows the provider refund.
	Status core.RefundStatus
	// ProviderRefundID is the provider's refund identifier.
	ProviderRefundID *string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the refund: it refunds a credit note or, for an expired
// activation, a subscription.
func (r *Refund) Validate() error {
	var v core.Validator
	v.Check(!r.PaymentID.IsZero(), "payment_id", core.CodeRequired)
	v.OptionalEnum("reason", r.Reason)
	switch {
	case r.Reason == RefundReasonSubscriptionActivationExpired:
		v.Check(!r.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	case r.CreditNoteID.IsZero():
		v.Check(!r.SubscriptionID.IsZero(), "refundable", core.CodeRequired)
	}
	v.Positive("amount", r.Amount)
	v.Currency("currency", r.Currency)
	v.Enum("status", r.Status)
	return v.Err()
}

// CanRefundTransition reports whether a refund may go from one status to
// another: pending → succeeded or failed.
func CanRefundTransition(from, to core.RefundStatus) bool {
	return from == core.RefundStatusPending && (to == core.RefundStatusSucceeded || to == core.RefundStatusFailed)
}

// Transition moves the refund to status to at now.
func (r *Refund) Transition(to core.RefundStatus, now time.Time) error {
	if !CanRefundTransition(r.Status, to) {
		return &core.TransitionError{Entity: "refund", From: string(r.Status), To: string(to)}
	}
	r.Status = to
	r.UpdatedAt = now
	return nil
}
