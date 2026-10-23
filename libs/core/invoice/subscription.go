package invoice

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Subscription links an invoice to a subscription it bills (domain §11.1
// InvoiceSubscription). Its [Subscription.Keys] make billing idempotent.
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
	// RegeneratedInvoiceID is the invoice regenerated from this row's
	// voided invoice, which bills the subscription in its place; zero
	// otherwise. A regenerated row has no [Subscription.Keys] (lago
	// invoice_subscriptions.regenerated_invoice_id, whose unique indexes
	// skip such rows).
	RegeneratedInvoiceID core.InvoiceID
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// Periodic reports whether the reason bills a subscription period:
// every reason except in_advance_charge, which bills one event.
func (r InvoicingReason) Periodic() bool {
	return r.Valid() && r != InvoicingReasonInAdvanceCharge
}

// KeyKind names one uniqueness rule of invoice subscriptions.
type KeyKind int

// The uniqueness rules of invoice subscriptions (lago's unique indexes
// index_uniq_invoice_subscriptions_on_charges_from_to_datetime,
// index_uniq_invoice_subscriptions_on_fixed_charges_boundaries,
// index_unique_starting_invoice_subscription and
// index_unique_terminating_invoice_subscription, db/structure.sql).
const (
	// KeyCharges: one recurring row per subscription and charges period.
	KeyCharges KeyKind = iota + 1
	// KeyFixedCharges: one recurring row per subscription and fixed
	// charges period.
	KeyFixedCharges
	// KeyStarting: one subscription_starting row per subscription.
	KeyStarting
	// KeyTerminating: one subscription_terminating row per subscription.
	KeyTerminating
)

// IdempotencyKey is one uniqueness key of an invoice subscription.
type IdempotencyKey struct {
	// SubscriptionID is the subscription.
	SubscriptionID core.SubscriptionID
	// Kind is the rule.
	Kind KeyKind
	// Period is the charges period ([KeyCharges]) or the fixed charges
	// period ([KeyFixedCharges]), in UTC; zero for the other kinds.
	Period period.Period
}

// Equal reports whether k and o are the same key.
func (k IdempotencyKey) Equal(o IdempotencyKey) bool {
	return k.SubscriptionID == o.SubscriptionID && k.Kind == o.Kind &&
		k.Period.From.Equal(o.Period.From) && k.Period.To.Equal(o.Period.To)
}

// Has reports whether k is one of the keys of s.
func (s *Subscription) Has(k IdempotencyKey) bool {
	return slices.ContainsFunc(s.Keys(), k.Equal)
}

// Keys returns the uniqueness keys of s: a recurring row is unique per
// charges period and per fixed charges period (each when billed, that is
// non-zero), so that a yearly plan billing charges monthly bills every
// month once; a starting or terminating row is unique per subscription.
// Other rows (in-advance charges, progressive billing) and the rows of a
// regenerated invoice have none.
func (s *Subscription) Keys() []IdempotencyKey {
	var out []IdempotencyKey
	if !s.RegeneratedInvoiceID.IsZero() {
		return nil
	}
	if s.Recurring {
		if !s.ChargesPeriod.IsZero() {
			out = append(out, IdempotencyKey{SubscriptionID: s.SubscriptionID, Kind: KeyCharges, Period: utcPeriod(s.ChargesPeriod)})
		}
		if !s.FixedChargesPeriod.IsZero() {
			out = append(out, IdempotencyKey{SubscriptionID: s.SubscriptionID, Kind: KeyFixedCharges, Period: utcPeriod(s.FixedChargesPeriod)})
		}
	}
	switch s.Reason {
	case InvoicingReasonSubscriptionStarting:
		out = append(out, IdempotencyKey{SubscriptionID: s.SubscriptionID, Kind: KeyStarting})
	case InvoicingReasonSubscriptionTerminating:
		out = append(out, IdempotencyKey{SubscriptionID: s.SubscriptionID, Kind: KeyTerminating})
	}
	return out
}

func utcPeriod(p period.Period) period.Period {
	return period.Period{From: p.From.UTC(), To: p.To.UTC()}
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
