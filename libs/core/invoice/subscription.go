package invoice

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Subscription links an invoice to a subscription it bills (domain §11.1
// InvoiceSubscription). It is unique per (subscription, reason, period
// start) for periodic reasons, which makes billing idempotent.
type Subscription struct {
	// ID identifies the link.
	ID core.InvoiceSubscriptionID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the invoice.
	InvoiceID core.InvoiceID
	// SubscriptionID is the billed subscription.
	SubscriptionID core.SubscriptionID
	// Reason is why the invoice bills the subscription.
	Reason InvoicingReason
	// Period is the subscription (base fee) period.
	Period period.Period
	// ChargesPeriod is the usage charges period.
	ChargesPeriod period.Period
	// FixedChargesPeriod is the fixed charges period.
	FixedChargesPeriod period.Period
	// Timestamp is the billing instant.
	Timestamp time.Time
	// Recurring tells whether the invoice is a periodic one.
	Recurring bool
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// Periodic reports whether the reason is one whose (subscription, reason,
// period start) key is unique: every reason except in_advance_charge, which
// is idempotent per event instead.
func (r InvoicingReason) Periodic() bool {
	return r.Valid() && r != InvoicingReasonInAdvanceCharge
}

// IdempotencyKey is the uniqueness key of a periodic invoice subscription.
type IdempotencyKey struct {
	// SubscriptionID is the subscription.
	SubscriptionID core.SubscriptionID
	// Reason is the invoicing reason.
	Reason InvoicingReason
	// PeriodStart is the start of the subscription period (UTC).
	PeriodStart time.Time
}

// Key returns the idempotency key of s and whether it applies (periodic
// reasons only).
func (s *Subscription) Key() (IdempotencyKey, bool) {
	return IdempotencyKey{s.SubscriptionID, s.Reason, s.Period.From.UTC()}, s.Reason.Periodic()
}

// Validate checks the invariants of an invoice subscription.
func (s *Subscription) Validate() error {
	var v core.Validator
	v.Check(!s.InvoiceID.IsZero(), "invoice_id", core.CodeRequired)
	v.Check(!s.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	v.Enum("invoicing_reason", s.Reason)
	for _, p := range []struct {
		field string
		p     period.Period
	}{{"from_datetime", s.Period}, {"charges_from_datetime", s.ChargesPeriod}, {"fixed_charges_from_datetime", s.FixedChargesPeriod}} {
		if !p.p.IsZero() {
			v.Check(!p.p.To.Before(p.p.From), p.field, core.CodeInvalid)
		}
	}
	return v.Err()
}

// DifferentChargesBoundaries reports whether the charges period differs
// from the subscription period (yearly plans billing charges monthly).
func (s *Subscription) DifferentChargesBoundaries() bool {
	return !s.ChargesPeriod.From.Equal(s.Period.From) || !s.ChargesPeriod.To.Equal(s.Period.To)
}

// DifferentFixedChargesBoundaries reports whether the fixed charges period
// differs from the subscription period.
func (s *Subscription) DifferentFixedChargesBoundaries() bool {
	return !s.FixedChargesPeriod.From.Equal(s.Period.From) || !s.FixedChargesPeriod.To.Equal(s.Period.To)
}
