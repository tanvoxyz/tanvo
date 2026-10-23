package jobs

import (
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Timing is when a periodic job runs. Times are UTC.
type Timing interface {
	// Next returns the first run strictly after t.
	Next(t time.Time) time.Time
	// String describes the timing ("every 5m0s", "hourly at :05").
	String() string
}

// Every runs every d, at the multiples of d since the zero time (every 5
// minutes is at :00, :05, …).
func Every(d time.Duration) Timing { return every(d) }

type every time.Duration

func (e every) Next(t time.Time) time.Time {
	d := time.Duration(e)
	return t.UTC().Truncate(d).Add(d)
}

func (e every) String() string { return "every " + time.Duration(e).String() }

// HourlyAt runs every hour at minute m.
func HourlyAt(m int) Timing { return hourlyAt(m) }

type hourlyAt int

func (h hourlyAt) Next(t time.Time) time.Time {
	t = t.UTC()
	n := t.Truncate(time.Hour).Add(time.Duration(h) * time.Minute)
	if !n.After(t) {
		n = n.Add(time.Hour)
	}
	return n
}

func (h hourlyAt) String() string { return fmt.Sprintf("hourly at :%02d", int(h)) }

// DailyAt runs every day at hour:minute UTC.
func DailyAt(hour, minute int) Timing { return dailyAt{hour, minute} }

type dailyAt struct{ hour, minute int }

func (d dailyAt) Next(t time.Time) time.Time {
	t = t.UTC()
	n := time.Date(t.Year(), t.Month(), t.Day(), d.hour, d.minute, 0, 0, time.UTC)
	if !n.After(t) {
		n = n.AddDate(0, 0, 1)
	}
	return n
}

func (d dailyAt) String() string { return fmt.Sprintf("daily at %02d:%02d", d.hour, d.minute) }

// Periodic is one entry of the clock schedule: a job kind enqueued on a
// timing by River's elected leader. The work is done by the handler that
// another library registers for the kind; an entry whose kind has no
// handler is not scheduled.
type Periodic struct {
	// Kind is the job kind.
	Kind string
	// Queue is the queue the job runs on (its shard 0).
	Queue port.Queue
	// Timing is when it runs.
	Timing Timing
	// Source is the lago-api clockwork entry ("clock.rb
	// schedule:activate_subscriptions").
	Source string
	// Job is the lago-api job class file.
	Job string
}

// Periodic job kinds: lago-api's clock jobs.
const (
	KindActivateSubscriptions            = "clock.activate_subscriptions"
	KindActivateContracts                = "clock.activate_contracts"
	KindRefreshDraftInvoices             = "clock.refresh_draft_invoices"
	KindProcessSubscriptionActivities    = "clock.process_subscription_activities"
	KindRefreshLifetimeUsages            = "clock.refresh_lifetime_usages"
	KindRefreshWalletsOngoingBalance     = "clock.refresh_wallets_ongoing_balance"
	KindTerminateEndedSubscriptions      = "clock.terminate_ended_subscriptions"
	KindValidateEvents                   = "clock.validate_events"
	KindBillSubscriptions                = "clock.bill_subscriptions"
	KindCreateBillingSegments            = "clock.create_billing_segments"
	KindTrackAPIKeyUsage                 = "clock.track_api_key_usage"
	KindComputeDailyUsages               = "clock.compute_daily_usages"
	KindProcessBillingSegments           = "clock.process_billing_segments"
	KindExpireIncompleteSubscriptions    = "clock.expire_incomplete_subscriptions"
	KindFinalizeInvoices                 = "clock.finalize_invoices"
	KindMarkInvoicesPaymentOverdue       = "clock.mark_invoices_payment_overdue"
	KindRetryGeneratingInvoices          = "clock.retry_generating_subscription_invoices"
	KindTerminateCoupons                 = "clock.terminate_coupons"
	KindBillEndedTrials                  = "clock.bill_ended_trial_subscriptions"
	KindExpireOrderForms                 = "clock.expire_order_forms"
	KindCancelAbandonedPayments          = "clock.cancel_abandoned_payments"
	KindTerminateWallets                 = "clock.terminate_wallets"
	KindExecuteScheduledOrders           = "clock.execute_scheduled_orders"
	KindProcessDunningCampaigns          = "clock.process_dunning_campaigns"
	KindAlertSubscriptionsToBeTerminated = "clock.alert_subscriptions_to_be_terminated"
	KindTerminateRecurringRules          = "clock.terminate_recurring_transaction_rules"
	KindCreateIntervalWalletTransactions = "clock.create_interval_wallet_transactions"
	KindRetryFailedInvoices              = "clock.retry_failed_invoices"
	KindRetryInboundWebhooks             = "clock.retry_inbound_webhooks"
	KindCleanWebhooks                    = "clock.clean_webhooks"
	KindCleanInboundWebhooks             = "clock.clean_inbound_webhooks"
	KindCleanRecordDeletions             = "clock.clean_record_deletions"
)

// Schedule returns lago-api's clock schedule (clock.rb at 25b8b7b09) as
// periodic jobs, in the order of clock.rb. Differences:
//
//   - Intervals ("every 5.minutes") are aligned on the clock instead of
//     on the clock process's start.
//   - The dedicated-organization variants (process_dedicated_orgs_*,
//     refresh_dedicated_org_wallets) are left out: queue sharding and
//     token buckets replace dedicated workers.
//   - refresh_flagged_subscriptions (every 10 seconds, draining a Redis
//     queue) is left out: the ingest consumers refresh on change
//     notifications.
//   - The wallet refresh does not depend on a cache being configured.
//   - Each job runs on the queue of its workload instead of a clock queue.
func Schedule() []Periodic {
	const rb = "clock.rb schedule:"
	const dir = "app/jobs/clock/"
	return []Periodic{
		{KindActivateSubscriptions, port.QueueBilling, Every(5 * time.Minute), rb + "activate_subscriptions", dir + "activate_subscriptions_job.rb"},
		{KindActivateContracts, port.QueueBilling, Every(5 * time.Minute), rb + "activate_contracts", dir + "activate_contracts_job.rb"},
		{KindRefreshDraftInvoices, port.QueueBilling, Every(5 * time.Minute), rb + "refresh_draft_invoices", dir + "refresh_draft_invoices_job.rb"},
		{KindProcessSubscriptionActivities, port.QueueBilling, Every(time.Minute), rb + "process_subscription_activity", dir + "process_all_subscription_activities_job.rb"},
		{KindRefreshLifetimeUsages, port.QueueBilling, Every(5 * time.Minute), rb + "refresh_lifetime_usages", dir + "refresh_lifetime_usages_job.rb"},
		{KindRefreshWalletsOngoingBalance, port.QueueBilling, Every(5 * time.Minute), rb + "refresh_wallets_ongoing_balance", dir + "refresh_wallets_ongoing_balance_job.rb"},
		{KindTerminateEndedSubscriptions, port.QueueBilling, HourlyAt(5), rb + "terminate_ended_subscriptions", dir + "terminate_ended_subscriptions_job.rb"},
		{KindBillSubscriptions, port.QueueBilling, HourlyAt(10), rb + "bill_customers", dir + "subscriptions_biller_job.rb"},
		{KindCreateBillingSegments, port.QueueBilling, HourlyAt(12), rb + "create_billing_segments", dir + "create_billing_segments_job.rb"},
		{KindTrackAPIKeyUsage, port.QueueDefault, HourlyAt(15), rb + "api_keys_track_usage", dir + "api_keys/track_usage_job.rb"},
		{KindProcessBillingSegments, port.QueueBilling, HourlyAt(17), rb + "process_billing_segments", dir + "process_billing_segments_job.rb"},
		{KindExpireIncompleteSubscriptions, port.QueueBilling, HourlyAt(20), rb + "expire_incomplete_subscriptions", dir + "expire_incomplete_subscriptions_job.rb"},
		{KindRetryGeneratingInvoices, port.QueueBilling, HourlyAt(30), rb + "retry_generating_subscription_invoices", dir + "retry_generating_subscription_invoices_job.rb"},
		{KindFinalizeInvoices, port.QueueBilling, HourlyAt(20), rb + "finalize_invoices", dir + "finalize_invoices_job.rb"},
		{KindMarkInvoicesPaymentOverdue, port.QueueBilling, HourlyAt(25), rb + "mark_invoices_as_payment_overdue", dir + "mark_invoices_as_payment_overdue_job.rb"},
		{KindTerminateCoupons, port.QueueBilling, HourlyAt(30), rb + "terminate_coupons", dir + "terminate_coupons_job.rb"},
		{KindBillEndedTrials, port.QueueBilling, HourlyAt(35), rb + "bill_ended_trial_subscriptions", dir + "free_trial_subscriptions_biller_job.rb"},
		{KindTerminateWallets, port.QueueBilling, HourlyAt(45), rb + "terminate_wallets", dir + "terminate_wallets_job.rb"},
		{KindAlertSubscriptionsToBeTerminated, port.QueueBilling, HourlyAt(50), rb + "termination_alert", dir + "subscriptions_to_be_terminated_job.rb"},
		{KindTerminateRecurringRules, port.QueueBilling, HourlyAt(50), rb + "terminate_expired_wallet_transaction_rules", dir + "terminate_recurring_transaction_rules_job.rb"},
		{KindCreateIntervalWalletTransactions, port.QueueBilling, HourlyAt(55), rb + "top_up_wallet_interval_credits", dir + "create_interval_wallet_transactions_job.rb"},
		{KindExpireOrderForms, port.QueueBilling, HourlyAt(40), rb + "expire_order_forms", dir + "expire_order_forms_job.rb"},
		{KindExecuteScheduledOrders, port.QueueBilling, HourlyAt(45), rb + "execute_scheduled_orders", dir + "execute_scheduled_orders_job.rb"},
		{KindCleanWebhooks, port.QueueWebhooks, DailyAt(1, 0), rb + "clean_webhooks", dir + "webhooks_cleanup_job.rb"},
		{KindCleanInboundWebhooks, port.QueuePayments, DailyAt(1, 10), rb + "clean_inbound_webhooks", dir + "inbound_webhooks_cleanup_job.rb"},
		{KindCleanRecordDeletions, port.QueueDefault, DailyAt(1, 20), rb + "clean_record_deletions", dir + "record_deletions_cleanup_job.rb"},
		{KindValidateEvents, port.QueueBilling, HourlyAt(5), rb + "post_validate_events", dir + "events_validation_job.rb"},
		{KindComputeDailyUsages, port.QueueBilling, HourlyAt(15), rb + "compute_daily_usage", dir + "compute_all_daily_usages_job.rb"},
		{KindCancelAbandonedPayments, port.QueuePayments, HourlyAt(40), rb + "cancel_abandoned_payments", dir + "cancel_abandoned_payments_job.rb"},
		{KindProcessDunningCampaigns, port.QueuePayments, HourlyAt(45), rb + "process_dunning_campaigns", dir + "process_dunning_campaigns_job.rb"},
		{KindRetryFailedInvoices, port.QueueBilling, Every(15 * time.Minute), rb + "retry_failed_invoices", dir + "retry_failed_invoices_job.rb"},
		{KindRetryInboundWebhooks, port.QueuePayments, Every(15 * time.Minute), rb + "retry_inbound_webhooks", dir + "inbound_webhooks_retry_job.rb"},
	}
}
