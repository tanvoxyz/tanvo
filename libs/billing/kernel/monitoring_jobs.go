package kernel

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Jobs of usage monitoring and of the periodic sweeps.
const (
	// JobProcessSubscriptionActivity recomputes the lifetime usage of a
	// subscription that received events, bills the usage thresholds it
	// crosses and evaluates its usage alerts (lago
	// UsageMonitoring::ProcessSubscriptionActivityJob); its args are
	// [SubscriptionArgs]. Ingestion's usage signals enqueue it for the next
	// [ActivityInterval] boundary, once per subscription and interval: the
	// pending job is the subscription activity lago records in a table.
	JobProcessSubscriptionActivity = "billing.process_subscription_activity"
	// JobRefreshLifetimeUsages enqueues the [JobRefreshLifetimeUsage] of
	// every lifetime usage of an organization flagged for recalculation
	// (lago Clock::RefreshLifetimeUsagesJob); it has no args.
	JobRefreshLifetimeUsages = "billing.refresh_lifetime_usages"
	// JobRefreshOngoingBalances enqueues the
	// [JobRefreshWalletOngoingBalance] of every customer of an
	// organization with an active wallet (lago
	// Clock::RefreshWalletsOngoingBalanceJob); it has no args.
	JobRefreshOngoingBalances = "billing.refresh_ongoing_balances"
)

// ActivityInterval is the processing interval of subscription activities
// (lago LAGO_SUBSCRIPTION_ACTIVITY_PROCESSING_INTERVAL_SECONDS, one minute
// by default, app/services/usage_monitoring/process_subscription_activity_service.rb:74).
const ActivityInterval = time.Minute

// ActivityCommitGrace is how long after the end of its interval the
// [JobProcessSubscriptionActivity] runs: a signal placed in the interval
// just before its end, whose transaction commits within the grace, is
// seen by the job instead of collapsing into a job already reading the
// usage.
const ActivityCommitGrace = 5 * time.Second

// ActivityKey returns the unique key and the run instant of the
// [JobProcessSubscriptionActivity] of subscription sub for usage signalled
// at now, read from the queue's clock ([QueueNow]): the end of the
// interval holding now, run [ActivityCommitGrace] later. Signals within
// one interval collapse into one job; a signal arriving once that job may
// run belongs to the next interval, so it is not lost.
func ActivityKey(sub core.SubscriptionID, now time.Time) (string, time.Time) {
	end := now.UTC().Truncate(ActivityInterval).Add(ActivityInterval)
	return "activity:" + sub.String() + ":" + end.Format(time.RFC3339), end.Add(ActivityCommitGrace)
}

// JobKinds returns every job kind the billing engine runs
// (billing.Engine.HandleJob), in a stable order. It leaves out the kinds
// billing only enqueues for other libraries (payments and documents).
func JobKinds() []string {
	kinds := []string{
		JobBillSubscriptions, JobBillSubscription, JobRetryGeneratingInvoices, JobBillAdvanceCharges, JobSwapDowngrade,
		JobFinalizeDraft, JobRefreshDraft, JobFinalizeDueDrafts, JobRefreshFlaggedDrafts,
		JobRetryTaxFailedInvoices, JobRetryInvoices, JobRetryInvoicePayments,
		JobCreateInAdvanceChargeFees, JobPullProviderTaxes, JobVoidProviderTaxes,
		JobRefundCreditNote, JobReportCreditNoteTaxes,
		JobCheckUsageThresholds, JobRefreshLifetimeUsage, JobRefreshLifetimeUsages, JobProcessSubscriptionActivity,
		JobRefreshWalletOngoingBalance, JobRefreshOngoingBalances, JobWalletThresholdTopUp, JobWalletIntervalTopUps, JobExpireWallets,
		JobActivatePendingSubscriptions, JobTerminateEndedSubscriptions, JobEndTrials, JobTerminateCoupons,
		JobDestroyPlan,
		JobProcessLifetimeUsageAlert, JobProcessWalletAlerts,
		JobMarkOverdueInvoices, JobMarkInvoiceOverdue, JobAlertTerminatingSubscriptions,
		JobExpireIncompleteSubscriptions, JobExpireIncompleteSubscription, JobExpireOrderForms, JobExpireOrderForm,
		JobValidateEvents, JobCleanRecordDeletions,
		JobComputeDailyUsages, JobComputeDailyUsage, JobFillDailyUsageFromInvoice, JobFillDailyUsageHistory, JobRebuildDailyUsageHistory,
		JobProcessDunningCampaigns, JobProcessDunningCustomer, JobProcessDunningAttempt,
		JobDefineUsageBuckets,
	}
	kinds = append(kinds, planJobKinds()...)
	kinds = append(kinds, chargeJobKinds()...)
	kinds = append(kinds, orderJobKinds()...)
	kinds = append(kinds, gateJobKinds()...)
	kinds = append(kinds, contractJobKinds()...)
	return append(kinds, fixedChargeJobKinds()...)
}

// InAdvanceKey is the unique key of the [JobCreateInAdvanceChargeFees] of
// the event with transaction ID txn of subscription sub (lago
// Events::PayInAdvanceJob#lock_key_arguments,
// app/jobs/events/pay_in_advance_job.rb:19).
func InAdvanceKey(sub core.SubscriptionID, txn string) string {
	return "event:" + sub.String() + ":" + txn
}
