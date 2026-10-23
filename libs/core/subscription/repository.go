package subscription

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Filter narrows [Repository.List]; zero fields do not filter.
type Filter struct {
	// CustomerID keeps the customer's subscriptions.
	CustomerID core.CustomerID
	// PlanID keeps the subscriptions of the plan.
	PlanID core.PlanID
	// ExternalID keeps the subscriptions with that external ID.
	ExternalID string
	// Statuses keeps the subscriptions in one of these statuses.
	Statuses []Status
}

// Repository stores subscriptions. Every method is scoped to an
// organization.
//
// external_id is unique per status among the live statuses: at most one
// pending, one incomplete and one active subscription share an external
// ID (a downgrade's pending subscription shares it with the active one it
// replaces); a violation fails with core.ErrConflict.
type Repository interface {
	// Create inserts s.
	Create(ctx context.Context, s *Subscription) error
	// Update replaces the stored subscription s.ID with s.
	Update(ctx context.Context, s *Subscription) error
	// Get returns the subscription id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) (*Subscription, error)
	// GetByExternalID returns the live subscription with that external ID,
	// preferring active, then incomplete, then pending; or
	// core.ErrNotFound.
	GetByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (*Subscription, error)
	// List returns the subscriptions matching f in creation order.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Subscription], error)
	// MatchEvent returns the subscription an event with that external
	// subscription ID and timestamp belongs to, as [MatchEvent] decides,
	// or core.ErrNotFound.
	MatchEvent(ctx context.Context, org core.OrganizationID, externalID string, t time.Time, recurringFallback bool) (*Subscription, error)
	// ListByCustomer returns the customer's subscriptions in one of
	// statuses (all statuses when empty), in creation order.
	ListByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID, statuses ...Status) ([]*Subscription, error)
	// InitialStartedAt returns the earliest started_at of the customer's
	// subscriptions with externalID, or nil when none has started (lago
	// Subscription#initial_started_at).
	InitialStartedAt(ctx context.Context, org core.OrganizationID, customer core.CustomerID, externalID string) (*time.Time, error)
	// ListActiveOrPendingByPlan returns the plan's active and pending
	// subscriptions (plan deletion and plan updates).
	ListActiveOrPendingByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) ([]*Subscription, error)
	// ListDueForBilling returns the active subscriptions with
	// next_billing_at ≤ t, ordered by next_billing_at.
	ListDueForBilling(ctx context.Context, org core.OrganizationID, t time.Time, page core.PageRequest) (core.Page[*Subscription], error)
	// ListPendingToActivate returns the pending subscriptions without a
	// previous subscription whose subscription_at ≤ t.
	ListPendingToActivate(ctx context.Context, org core.OrganizationID, t time.Time) ([]*Subscription, error)
	// GetNext returns the pending subscription whose previous subscription
	// is id (a scheduled downgrade), or core.ErrNotFound.
	GetNext(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) (*Subscription, error)
	// Chain returns id and its predecessors through
	// previous_subscription_id, oldest first.
	Chain(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) ([]*Subscription, error)
	// ListEndingBetween returns the active subscriptions with
	// from ≤ ending_at < to (termination alerts and ending_at
	// termination).
	ListEndingBetween(ctx context.Context, org core.OrganizationID, from, to time.Time) ([]*Subscription, error)
	// ListIncompleteWithExpiredRules returns the incomplete subscriptions
	// having a pending activation rule expired at now.
	ListIncompleteWithExpiredRules(ctx context.Context, org core.OrganizationID, now time.Time) ([]*Subscription, error)
}

// ActivationRuleRepository stores activation rules. Every method is
// scoped to an organization.
type ActivationRuleRepository interface {
	// Create inserts r.
	Create(ctx context.Context, r *ActivationRule) error
	// Update replaces the stored rule r.ID with r.
	Update(ctx context.Context, r *ActivationRule) error
	// ListBySubscription returns the subscription's rules in creation
	// order.
	ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*ActivationRule, error)
	// ReplaceForSubscription replaces the subscription's rules with rules.
	ReplaceForSubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, rules []*ActivationRule) error
}

// FixedChargeEventRepository stores fixed charge events. Every method is
// scoped to an organization.
type FixedChargeEventRepository interface {
	// Record inserts e, first removing any event of the same subscription
	// and fixed charge on the same day in loc (domain §7.5).
	Record(ctx context.Context, e *FixedChargeEvent, loc *time.Location) error
	// ListBySubscription returns the subscription's events for the fixed
	// charge (every fixed charge when zero), ordered by timestamp.
	ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, fixedCharge core.FixedChargeID) ([]*FixedChargeEvent, error)
}

// AppliedUsageThresholdRepository stores applied usage thresholds. Every
// method is scoped to an organization.
type AppliedUsageThresholdRepository interface {
	// Create inserts a; the same threshold applied twice by one invoice
	// fails with core.ErrConflict.
	Create(ctx context.Context, a *AppliedUsageThreshold) error
	// ListBySubscription returns the subscription's records in creation
	// order.
	ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*AppliedUsageThreshold, error)
	// ListByInvoice returns the invoice's records in creation order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*AppliedUsageThreshold, error)
}

// LifetimeUsageRepository stores lifetime usage, one record per
// subscription chain (external subscription ID). Every method is scoped
// to an organization.
type LifetimeUsageRepository interface {
	// GetByExternalID returns the chain's record, or core.ErrNotFound.
	GetByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (*LifetimeUsage, error)
	// GetBySubscription returns the record whose current subscription is
	// sub, or core.ErrNotFound.
	GetBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) (*LifetimeUsage, error)
	// Upsert inserts l, or replaces the chain's record (same external
	// subscription ID), keeping the stored ID.
	Upsert(ctx context.Context, l *LifetimeUsage) error
	// ListToRecalculate returns the records flagged for recalculation in
	// creation order.
	ListToRecalculate(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*LifetimeUsage], error)
}
