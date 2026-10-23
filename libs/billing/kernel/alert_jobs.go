package kernel

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// Jobs of usage and wallet alerts.
const (
	// JobProcessLifetimeUsageAlert evaluates a
	// billable_metric_lifetime_usage_units alert against the usage of its
	// subscription since the subscription started (lago
	// UsageMonitoring::ProcessLifetimeUsageAlertJob); its args are
	// [LifetimeUsageAlertArgs]. The subscription activity enqueues it
	// [ActivityInterval] later, once per alert while one is pending or
	// running ([LifetimeUsageAlertKey]).
	JobProcessLifetimeUsageAlert = "billing.process_lifetime_usage_alert"
	// JobProcessWalletAlerts evaluates the alerts of a wallet against its
	// balances (lago UsageMonitoring::ProcessWalletAlertsJob); its args are
	// [WalletArgs]. Every change of a wallet's balance or ongoing balance
	// enqueues it ([EnqueueWalletAlerts]).
	JobProcessWalletAlerts = "billing.process_wallet_alerts"
)

// LifetimeUsageAlertArgs are the args of [JobProcessLifetimeUsageAlert].
type LifetimeUsageAlertArgs struct {
	// AlertID is the alert.
	AlertID core.AlertID `json:"alert_id"`
	// SubscriptionID is the subscription the alert watches.
	SubscriptionID core.SubscriptionID `json:"subscription_id"`
}

// LifetimeUsageAlertKey is the unique key of the
// [JobProcessLifetimeUsageAlert] of alert id (lago
// ProcessLifetimeUsageAlertJob#lock_key_arguments,
// app/jobs/usage_monitoring/process_lifetime_usage_alert_job.rb:20).
func LifetimeUsageAlertKey(id core.AlertID) string {
	return "alert:" + id.String()
}

// EnqueueLifetimeUsageAlert enqueues the [JobProcessLifetimeUsageAlert] of
// alert id of subscription sub to run [ActivityInterval] after now (lago
// set(wait: processing_interval),
// app/services/usage_monitoring/process_subscription_activity_service.rb:47).
func EnqueueLifetimeUsageAlert(ctx context.Context, r port.Repos, org core.OrganizationID, id core.AlertID, sub core.SubscriptionID, now time.Time) error {
	args := LifetimeUsageAlertArgs{AlertID: id, SubscriptionID: sub}
	return Enqueue(ctx, r, org, JobProcessLifetimeUsageAlert, args, LifetimeUsageAlertKey(id), now.Add(ActivityInterval))
}

// EnqueueWalletAlerts enqueues the [JobProcessWalletAlerts] of wallet w,
// once per wallet while one is pending or running (lago unique
// :until_executed, app/jobs/usage_monitoring/process_wallet_alerts_job.rb:13).
// The job becomes visible when the transaction commits, as lago enqueues
// it after commit.
func EnqueueWalletAlerts(ctx context.Context, r port.Repos, w *wallet.Wallet) error {
	return Enqueue(ctx, r, w.OrganizationID, JobProcessWalletAlerts, WalletArgs{WalletID: w.ID}, "wallet:"+w.ID.String(), time.Time{})
}
