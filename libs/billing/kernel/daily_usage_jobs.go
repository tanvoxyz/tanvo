package kernel

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Jobs of the daily usage snapshots (subscription.DailyUsage).
const (
	// JobComputeDailyUsages enqueues the [JobComputeDailyUsage] of every
	// subscription of an organization entering a new day in its time zone
	// whose usage may have changed (lago Clock::ComputeAllDailyUsagesJob
	// and DailyUsages::ComputeAllService); its args are [InstantArgs].
	JobComputeDailyUsages = "billing.compute_daily_usages"
	// JobComputeDailyUsage stores the daily usage of one subscription for
	// the day before the args' instant (lago DailyUsages::ComputeJob); its
	// args are [DailyUsageArgs], unique per subscription and local day
	// ([DailyUsageKey]).
	JobComputeDailyUsage = "billing.compute_daily_usage"
	// JobFillDailyUsageFromInvoice stores the daily usage of the last day
	// of the periods an invoice bills (lago DailyUsages::FillFromInvoiceJob);
	// its args are [FillDailyUsageFromInvoiceArgs].
	JobFillDailyUsageFromInvoice = "billing.fill_daily_usage_from_invoice"
	// JobFillDailyUsageHistory stores the missing daily usages of one
	// subscription over a range of days (lago DailyUsages::FillHistoryJob);
	// its args are [FillDailyUsageHistoryArgs].
	JobFillDailyUsageHistory = "billing.fill_daily_usage_history"
	// JobRebuildDailyUsageHistory deletes, when asked, the daily usages of
	// an organization's subscriptions from a day on and enqueues their
	// [JobFillDailyUsageHistory] (lago rake daily_usages:fill_history,
	// lib/tasks/daily_usages.rake); its args are
	// [RebuildDailyUsageHistoryArgs].
	JobRebuildDailyUsageHistory = "billing.rebuild_daily_usage_history"
)

// RebuildDailyUsageHistoryArgs are the args of
// [JobRebuildDailyUsageHistory].
type RebuildDailyUsageHistoryArgs struct {
	// From is the first day; zero means
	// subscription.DefaultDailyUsageHistoryDays days ago (lago
	// lib/tasks/daily_usages.rake:17-18).
	From core.Date `json:"from_date,omitzero"`
	// SubscriptionIDs, when not empty, restricts the rebuild to these
	// subscriptions; empty rebuilds every one in scope.
	SubscriptionIDs []core.SubscriptionID `json:"subscription_ids,omitempty"`
	// DeleteExisting deletes the stored daily usages of the subscriptions
	// in scope from From on first (lago lib/tasks/daily_usages.rake:52-71).
	DeleteExisting bool `json:"delete_existing,omitempty"`
}

// DefaultDailyUsageJitter is the window the [JobComputeDailyUsage] of one
// run are spread over when [Deps.DailyUsageJitter] is not positive (lago
// LAGO_DAILY_USAGE_SCHEDULING_JITTER_SECONDS, 30 minutes by default,
// app/services/daily_usages/compute_all_service.rb:171-178).
const DefaultDailyUsageJitter = 30 * time.Minute

// DailyUsageArgs are the args of [JobComputeDailyUsage].
type DailyUsageArgs struct {
	// SubscriptionID is the subscription.
	SubscriptionID core.SubscriptionID `json:"subscription_id"`
	// Timestamp is the instant of the run that enqueued the job.
	Timestamp time.Time `json:"timestamp"`
}

// FillDailyUsageFromInvoiceArgs are the args of
// [JobFillDailyUsageFromInvoice].
type FillDailyUsageFromInvoiceArgs struct {
	// InvoiceID is the invoice.
	InvoiceID core.InvoiceID `json:"invoice_id"`
	// SubscriptionIDs are the invoice's subscriptions to fill.
	SubscriptionIDs []core.SubscriptionID `json:"subscription_ids"`
}

// FillDailyUsageHistoryArgs are the args of [JobFillDailyUsageHistory].
type FillDailyUsageHistoryArgs struct {
	// SubscriptionID is the subscription.
	SubscriptionID core.SubscriptionID `json:"subscription_id"`
	// From is the first day, in the customer's time zone.
	From core.Date `json:"from_date"`
	// To is the last day; nil means yesterday (or the termination day).
	To *core.Date `json:"to_date,omitempty"`
	// Sandbox computes the usages without storing them.
	Sandbox bool `json:"sandbox,omitempty"`
}

// DailyUsageKey is the unique key of the [JobComputeDailyUsage] of
// subscription sub for local day day, the day of the run's instant in
// the customer's time zone (lago DailyUsages::ComputeJob unique
// :until_and_while_executing, app/jobs/daily_usages/compute_job.rb:14-28).
func DailyUsageKey(sub core.SubscriptionID, day core.Date) string {
	return "daily_usage:" + sub.String() + ":" + day.String()
}

// DailyUsageInvoiceReasons are the invoicing reasons whose invoices fill
// the daily usages (lago USAGE_TRACKABLE_REASONS,
// app/services/invoices/subscription_service.rb:257).
var DailyUsageInvoiceReasons = []invoice.InvoicingReason{
	invoice.InvoicingReasonSubscriptionPeriodic, invoice.InvoicingReasonSubscriptionTerminating,
}

// EnqueueFillDailyUsage enqueues the [JobFillDailyUsageFromInvoice] of
// invoice inv for subscriptions subs, when there are any (lago
// Invoices::SubscriptionService#fill_daily_usage,
// app/services/invoices/subscription_service.rb:258-270).
func EnqueueFillDailyUsage(ctx context.Context, r port.Repos, inv *invoice.Invoice, subs []core.SubscriptionID) error {
	if len(subs) == 0 {
		return nil
	}
	args := FillDailyUsageFromInvoiceArgs{InvoiceID: inv.ID, SubscriptionIDs: subs}
	return Enqueue(ctx, r, inv.OrganizationID, JobFillDailyUsageFromInvoice, args, "", time.Time{})
}
