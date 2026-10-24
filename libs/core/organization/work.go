package organization

import (
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Work names a kind of due work the periodic jobs look for across
// organizations (architecture §9): a clock job lists the organizations
// with work of its kind and dispatches one job to each of them, which
// applies the exact rule within the organization. A scan may therefore
// return organizations that turn out to have nothing due (its condition
// is a superset of the job's), but never misses one that has.
type Work string

// Work values.
const (
	// WorkActiveSubscriptions: organizations with active subscriptions
	// (the periodic billing run and the end of free trials).
	WorkActiveSubscriptions Work = "active_subscriptions"
	// WorkPendingSubscriptions: pending subscriptions whose subscription
	// date is at most [ScanDayMargin] after the instant (activation in the
	// customer's time zone).
	WorkPendingSubscriptions Work = "pending_subscriptions"
	// WorkEndingSubscriptions: active subscriptions ending within
	// [EndingMargin] of the instant (termination on the local ending day).
	WorkEndingSubscriptions Work = "ending_subscriptions"
	// WorkDraftInvoices: draft invoices (finalization at the end of the
	// grace period).
	WorkDraftInvoices Work = "draft_invoices"
	// WorkRefreshRequested: draft invoices flagged for a refresh.
	WorkRefreshRequested Work = "refresh_requested"
	// WorkLifetimeUsages: lifetime usages whose invoiced usage is flagged
	// for recalculation.
	WorkLifetimeUsages Work = "lifetime_usages"
	// WorkAwaitingWalletRefresh: non-deleted customers flagged for the
	// refresh of their wallets' ongoing balances that have an active wallet.
	WorkAwaitingWalletRefresh Work = "awaiting_wallet_refresh"
	// WorkExpiredWallets: active wallets or recurring transaction rules
	// whose expiration is at or before the instant.
	WorkExpiredWallets Work = "expired_wallets"
	// WorkIntervalRules: active interval recurring transaction rules.
	WorkIntervalRules Work = "interval_rules"
	// WorkExpiredCoupons: active time-limited coupons whose expiration is
	// at or before the instant.
	WorkExpiredCoupons Work = "expired_coupons"
	// WorkOverdueInvoices: finalized invoices, not overdue, not paid and
	// without a lost dispute, whose payment due date is on or before the
	// instant's UTC date (the overdue marking).
	WorkOverdueInvoices Work = "overdue_invoices"
	// WorkEndingAtSubscriptions: active subscriptions with an ending date
	// on or after the start of the instant's UTC date (the termination
	// alerts, whose day offsets are configured).
	WorkEndingAtSubscriptions Work = "ending_at_subscriptions"
	// WorkExpirableSubscriptions: incomplete subscriptions with a pending
	// activation rule whose expiry is at or before the instant.
	WorkExpirableSubscriptions Work = "expirable_subscriptions"
	// WorkExpiringOrderForms: generated order forms whose expiry is at most
	// [ScanDayMargin] after the instant (expiry on the local expiry day).
	WorkExpiringOrderForms Work = "expiring_order_forms"
	// WorkExecutableOrders: created orders whose scheduled execution is at
	// or before the instant (lago Order.executable).
	WorkExecutableOrders Work = "executable_orders"
	// WorkDailyUsages: active subscriptions that do not skip daily usage
	// (the daily usage snapshots, taken as each customer's day starts).
	WorkDailyUsages Work = "daily_usages"
	// WorkDunningCustomers: live customers not excluded from dunning with
	// overdue invoices that are not self-billed (the dunning campaigns).
	WorkDunningCustomers Work = "dunning_customers"
	// WorkEventsToValidate: organizations with a webhook endpoint and raw
	// events created in the post-validation window of the instant
	// (event.ValidationWindow).
	WorkEventsToValidate Work = "events_to_validate"
	// WorkExpiredRecordDeletions: record deletions deleted before the
	// retention cutoff of the instant (export.RecordDeletionCutoff).
	WorkExpiredRecordDeletions Work = "expired_record_deletions"
	// WorkStuckGeneratingInvoices: subscription invoices still generating
	// [invoice.GeneratingRetryAge] after their creation, without an
	// invoice_generation_error detail (invoice.Invoice.StuckGenerating).
	WorkStuckGeneratingInvoices Work = "stuck_generating_invoices"
	// WorkTaxRetriedInvoices: failed invoices with a tax_error detail whose
	// message contains invoice.RetriedTaxErrorMessage (the periodic retry
	// of invoices failed on the tax provider's API limit).
	WorkTaxRetriedInvoices Work = "tax_retried_invoices"
	// WorkAwaitingBillingSegments: contract billing segments pending or
	// processing (the segment invoicing, domain §21.4).
	WorkAwaitingBillingSegments Work = "awaiting_billing_segments"
	// WorkInboundWebhooks: inbound provider webhooks not succeeded and
	// received within payment.WebhookRetryWindow of the instant (the
	// periodic retry of inbound webhooks).
	WorkInboundWebhooks Work = "inbound_webhooks"
	// WorkExpiredInboundWebhooks: inbound provider webhooks unchanged for
	// payment.WebhookRetention at the instant (their cleanup).
	WorkExpiredInboundWebhooks Work = "expired_inbound_webhooks"
	// WorkAbandonedPayments: provider payments of invoices processing at
	// the provider while it waits for the customer's action, last changed
	// within payment.AbandonedRange of the instant (their cancellation).
	WorkAbandonedPayments Work = "abandoned_payments"
)

// ScanDayMargin is how far past the instant a scan of local dates looks:
// a day plus the largest UTC offset, rounded up.
const ScanDayMargin = 48 * time.Hour

// EndingMargin is the window around the instant in which
// [WorkEndingSubscriptions] looks for ending dates (lago-api's
// ENDING_AT_PREFILTER, app/jobs/clock/terminate_ended_subscriptions_job.rb:5).
const EndingMargin = 72 * time.Hour

// WorkValues returns every [Work].
func WorkValues() []Work {
	return []Work{
		WorkActiveSubscriptions, WorkPendingSubscriptions, WorkEndingSubscriptions, WorkDraftInvoices,
		WorkRefreshRequested, WorkLifetimeUsages, WorkAwaitingWalletRefresh, WorkExpiredWallets, WorkIntervalRules,
		WorkExpiredCoupons, WorkOverdueInvoices, WorkEndingAtSubscriptions, WorkExpirableSubscriptions,
		WorkExpiringOrderForms, WorkDailyUsages, WorkDunningCustomers, WorkEventsToValidate,
		WorkExpiredRecordDeletions, WorkStuckGeneratingInvoices, WorkExecutableOrders, WorkTaxRetriedInvoices,
		WorkAwaitingBillingSegments, WorkInboundWebhooks, WorkExpiredInboundWebhooks, WorkAbandonedPayments,
	}
}

// Valid reports whether w is a declared value.
func (w Work) Valid() bool {
	return slices.Contains(WorkValues(), w)
}

// String returns w.
func (w Work) String() string { return string(w) }

// CheckWork validates the arguments of [Repository.WithWork].
func CheckWork(w Work, at time.Time) error {
	if !w.Valid() || at.IsZero() {
		return fmt.Errorf("organizations with work %q at %v: %w", w, at, core.ErrInvalidValue)
	}
	return nil
}
