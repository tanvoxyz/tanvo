package alert

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Filter narrows [Repository.List].
type Filter struct {
	// SubscriptionExternalID keeps the alerts of one subscription; ""
	// keeps all.
	SubscriptionExternalID string
	// WalletID keeps the alerts of one wallet; zero keeps all.
	WalletID core.WalletID
	// Types keeps alerts of these types; empty keeps all.
	Types []Type
}

// Repository stores alerts with their thresholds.
type Repository interface {
	// Create inserts a; a code already used by an alert of the same
	// subscription (or wallet), or a second alert of the same type on a
	// wallet, fails with core.ErrConflict.
	Create(ctx context.Context, a *Alert) error
	// Update replaces the stored alert a.ID with a and its thresholds
	// (same uniqueness rules as Create).
	Update(ctx context.Context, a *Alert) error
	// Get returns the alert id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.AlertID) (*Alert, error)
	// GetBySubscriptionCode returns the alert with that code on a
	// subscription, or core.ErrNotFound.
	GetBySubscriptionCode(ctx context.Context, org core.OrganizationID, subscriptionExternalID, code string) (*Alert, error)
	// GetByWalletCode returns the alert with that code on a wallet, or
	// core.ErrNotFound.
	GetByWalletCode(ctx context.Context, org core.OrganizationID, wallet core.WalletID, code string) (*Alert, error)
	// ListBySubscription returns the alerts of a subscription in creation
	// order, for evaluation after a usage refresh.
	ListBySubscription(ctx context.Context, org core.OrganizationID, subscriptionExternalID string) ([]*Alert, error)
	// ListByWallet returns the alerts of a wallet in creation order, for
	// evaluation after a balance refresh.
	ListByWallet(ctx context.Context, org core.OrganizationID, wallet core.WalletID) ([]*Alert, error)
	// ListByBillableMetric returns the alerts watching a billable metric
	// in creation order.
	ListByBillableMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID) ([]*Alert, error)
	// List returns the organization's alerts in creation order.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Alert], error)
	// Discard deletes the thresholds of the kept alert id and soft-deletes
	// it at at (lago destroy_alert_service.rb:15-17), or fails with
	// core.ErrNotFound. Triggered alerts are kept.
	Discard(ctx context.Context, org core.OrganizationID, id core.AlertID, at time.Time) error
	// DiscardByBillableMetric soft-deletes at at the kept alerts watching
	// metric, keeping their thresholds (lago
	// billable_metrics/destroy_service.rb:35), and returns how many.
	DiscardByBillableMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID, at time.Time) (int64, error)
}

// TriggeredFilter narrows [TriggeredAlertRepository.List].
type TriggeredFilter struct {
	// AlertID keeps the records of one alert; zero keeps all.
	AlertID core.AlertID
	// SubscriptionID keeps the records of one subscription; zero keeps
	// all.
	SubscriptionID core.SubscriptionID
	// WalletID keeps the records of one wallet; zero keeps all.
	WalletID core.WalletID
}

// TriggeredAlertRepository stores triggered alerts.
type TriggeredAlertRepository interface {
	// Create inserts t.
	Create(ctx context.Context, t *TriggeredAlert) error
	// Get returns the record id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.TriggeredAlertID) (*TriggeredAlert, error)
	// List returns the records in creation order.
	List(ctx context.Context, org core.OrganizationID, f TriggeredFilter, page core.PageRequest) (core.Page[*TriggeredAlert], error)
}
