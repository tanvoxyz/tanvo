package subscription

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// SegmentStatus is where a [BillingSegment] is in its invoicing.
type SegmentStatus string

// Billing segment statuses.
//
// source: app/models/billing_segment.rb:16-21
const (
	// SegmentPending: scheduled, not yet invoiced.
	SegmentPending SegmentStatus = "pending"
	// SegmentProcessing: being invoiced, or a metered in-advance segment
	// priced per event.
	SegmentProcessing SegmentStatus = "processing"
	// SegmentDone: invoiced.
	SegmentDone SegmentStatus = "done"
	// SegmentFailed: invoicing failed.
	SegmentFailed SegmentStatus = "failed"
)

// Valid reports whether s is a known status.
func (s SegmentStatus) Valid() bool {
	switch s {
	case SegmentPending, SegmentProcessing, SegmentDone, SegmentFailed:
		return true
	}
	return false
}

// Rule codes of billing segments.
const (
	// CodeRateRequired: a segment prices through a rate or an override.
	CodeRateRequired = "rate_card_rate_or_rate_override_required"
	// CodeMustBeBeforeStartedAt: a segment's cycle starts by the segment.
	CodeMustBeBeforeStartedAt = "must_be_before_started_at"
)

// BillingSegment is a durable priced slice of a contract rate card's
// billing cycle: a whole cycle has one segment, a rate change inside the
// cycle splits it into several sharing CycleStartedAt. Segments are the
// unit of contract invoicing; the invoice subscriptions of a run group
// them by contract.
//
// Unlike lago, EndedAt is exclusive (the schedule's own end), so segments
// of a card tile time without a microsecond gap.
type BillingSegment struct {
	// ID identifies the segment.
	ID core.BillingSegmentID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the contract.
	SubscriptionID core.SubscriptionID
	// CustomerID is the contract's customer.
	CustomerID core.CustomerID
	// RateCardID is the contract rate card priced.
	RateCardID core.SubscriptionRateCardID
	// InvoiceID is the invoice that billed the segment, once invoiced.
	InvoiceID core.InvoiceID
	// RateCardRateID is the rate card rate in force over the segment, kept
	// even when a phase override prices it (lago keeps rate_card_rate_id
	// beside rate_override_id).
	RateCardRateID core.RateCardRateID
	// RatePhaseID is the phase whose override prices the segment; zero
	// when the rate does.
	RatePhaseID core.RatePhaseID
	// PricingUnitID is the rate card's pricing unit, if any.
	PricingUnitID core.PricingUnitID
	// CycleStartedAt is the start of the billing cycle the segment is in.
	CycleStartedAt time.Time
	// StartedAt is the segment's start.
	StartedAt time.Time
	// EndedAt is the segment's exclusive end.
	EndedAt time.Time
	// BillingAt is when the segment is billed.
	BillingAt time.Time
	// RateModel is the pricing model in force: the override's, else the
	// rate's.
	RateModel catalog.ChargeModel
	// RateProperties are the pricing model's properties in force.
	RateProperties catalog.Properties
	// Currency is the rate card's currency.
	Currency money.Currency
	// ProrationRatio is the share of the full cycle priced, in [0, 1].
	ProrationRatio money.Decimal
	// Status is where the segment is in its invoicing.
	Status SegmentStatus
	// CreatedAt is when the segment was scheduled.
	CreatedAt time.Time
	// UpdatedAt is when the segment last changed.
	UpdatedAt time.Time
}

// Validate checks the segment's fields.
//
// source: app/models/billing_segment.rb:50-60, :124-150
func (s *BillingSegment) Validate() error {
	var v core.Validator
	v.Check(!s.BillingAt.IsZero(), "billing_at", core.CodeRequired)
	v.Check(!s.CycleStartedAt.IsZero(), "cycle_started_at", core.CodeRequired)
	v.Check(!s.StartedAt.IsZero(), "started_at", core.CodeRequired)
	v.Check(!s.EndedAt.IsZero(), "ended_at", core.CodeRequired)
	v.Check(!s.Currency.IsZero(), "currency", core.CodeRequired)
	v.Check(s.Status.Valid(), "status", core.CodeInvalid)
	v.Check(!s.ProrationRatio.IsNegative() && s.ProrationRatio.Cmp(money.NewFromInt(1)) <= 0,
		"proration_ratio", core.CodeOutOfRange)
	v.Check(!s.RateCardRateID.IsZero() || !s.RatePhaseID.IsZero(), "base", CodeRateRequired)
	v.Check(!s.EndedAt.Before(s.StartedAt), "ended_at", CodeMustBeAfterStartedAt)
	v.Check(!s.StartedAt.Before(s.CycleStartedAt), "cycle_started_at", CodeMustBeBeforeStartedAt)
	return v.Err()
}

// Overlaps reports whether s overlaps the window [from, to).
func (s *BillingSegment) Overlaps(from, to time.Time) bool {
	return s.StartedAt.Before(to) && s.EndedAt.After(from)
}

// BillingSegmentRepository stores billing segments. Every method is
// scoped to an organization.
type BillingSegmentRepository interface {
	// InsertAll inserts segs; a segment overlapping a stored one of its
	// card fails with core.ErrConflict.
	InsertAll(ctx context.Context, segs []*BillingSegment) error
	// Update replaces the stored segment s.ID with s.
	Update(ctx context.Context, s *BillingSegment) error
	// ListOverlapping returns the card's segments overlapping [from, to),
	// by start.
	ListOverlapping(ctx context.Context, org core.OrganizationID, card core.SubscriptionRateCardID, from, to time.Time) ([]*BillingSegment, error)
	// MaxCycleStartedAt returns the latest cycle start among the card's
	// segments, or nil when it has none.
	MaxCycleStartedAt(ctx context.Context, org core.OrganizationID, card core.SubscriptionRateCardID) (*time.Time, error)
	// ListAwaitingInvoicing returns the customer's pending or processing
	// segments, leaving out those of metered in-advance rate cards (lago
	// BillingSegment.awaiting_invoicing), by billing time then ID.
	ListAwaitingInvoicing(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]*BillingSegment, error)
	// CustomersAwaitingInvoicing returns the customers, not deleted,
	// having a segment awaiting invoicing, ordered by ID.
	CustomersAwaitingInvoicing(ctx context.Context, org core.OrganizationID) ([]core.CustomerID, error)
	// GeneratingInvoiceIDs returns the still-generating invoices of the
	// customer's done segments, ordered by ID.
	GeneratingInvoiceIDs(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]core.InvoiceID, error)
	// ListByInvoice returns the segments the invoice billed, by start then
	// ID.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*BillingSegment, error)
	// ListBySubscription returns the contract's segments, by start then ID.
	ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*BillingSegment, error)
}
