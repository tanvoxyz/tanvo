package kernel

import "github.com/tanvoxyz/tanvo/libs/core"

// Jobs of the invoice, subscription and order form clock jobs.
const (
	// JobMarkOverdueInvoices enqueues the [JobMarkInvoiceOverdue] of every
	// overdue candidate of an organization at the clock instant (lago
	// Clock::MarkInvoicesAsPaymentOverdueJob); its args are [InstantArgs].
	JobMarkOverdueInvoices = "billing.mark_overdue_invoices"
	// JobMarkInvoiceOverdue marks one invoice payment overdue and emits
	// invoice.payment_overdue (lago Invoices::Payments::MarkOverdueJob);
	// its args are [InvoiceArgs], unique per invoice ([OverdueKey]).
	JobMarkInvoiceOverdue = "billing.mark_invoice_overdue"
	// JobAlertTerminatingSubscriptions emits subscription.termination_alert
	// for the active subscriptions of an organization ending a configured
	// number of days after the clock instant, once per subscription and
	// day (lago Clock::SubscriptionsToBeTerminatedJob); its args are
	// [InstantArgs].
	JobAlertTerminatingSubscriptions = "billing.alert_terminating_subscriptions"
	// JobExpireIncompleteSubscriptions enqueues the
	// [JobExpireIncompleteSubscription] of every incomplete subscription of
	// an organization whose activation rule expired at the clock instant
	// (lago Clock::ExpireIncompleteSubscriptionsJob); its args are
	// [InstantArgs].
	JobExpireIncompleteSubscriptions = "billing.expire_incomplete_subscriptions"
	// JobExpireIncompleteSubscription cancels an incomplete subscription
	// whose activation rule expired (lago
	// Subscriptions::ActivationRules::ExpireIncompleteJob); its args are
	// [SubscriptionArgs], unique per subscription ([ExpireIncompleteKey]).
	JobExpireIncompleteSubscription = "billing.expire_incomplete_subscription"
	// JobExpireOrderForms enqueues the [JobExpireOrderForm] of every order
	// form of an organization due to expire at the clock instant (lago
	// Clock::ExpireOrderFormsJob); its args are [InstantArgs].
	JobExpireOrderForms = "billing.expire_order_forms"
	// JobExpireOrderForm expires one order form and voids its quote version
	// (lago OrderForms::ExpireOrderFormJob); its args are [OrderFormArgs],
	// unique per order form ([ExpireOrderFormKey]).
	JobExpireOrderForm = "billing.expire_order_form"
	// JobValidateEvents post-validates the events an organization received
	// in the hour before the clock instant and emits events.errors (lago
	// Clock::EventsValidationJob and Events::PostValidationJob); its args
	// are [InstantArgs].
	JobValidateEvents = "billing.validate_events"
	// JobCleanRecordDeletions removes the record deletions of an
	// organization older than one month before the clock instant (lago
	// Clock::RecordDeletionsCleanupJob); its args are [InstantArgs].
	JobCleanRecordDeletions = "billing.clean_record_deletions"
)

// OrderFormArgs are the args of jobs about one order form.
type OrderFormArgs struct {
	// OrderFormID is the order form.
	OrderFormID core.OrderFormID `json:"order_form_id"`
}

// OverdueKey is the unique key of the [JobMarkInvoiceOverdue] of invoice
// id (lago unique :until_executed,
// app/jobs/invoices/payments/mark_overdue_job.rb:6).
func OverdueKey(id core.InvoiceID) string { return "overdue:" + id.String() }

// ExpireIncompleteKey is the unique key of the
// [JobExpireIncompleteSubscription] of subscription id (lago unique
// :until_executed, app/jobs/subscriptions/activation_rules/expire_incomplete_job.rb:14).
func ExpireIncompleteKey(id core.SubscriptionID) string { return "expire_incomplete:" + id.String() }

// ExpireOrderFormKey is the unique key of the [JobExpireOrderForm] of
// order form id (lago unique :until_executed,
// app/jobs/order_forms/expire_order_form_job.rb:7).
func ExpireOrderFormKey(id core.OrderFormID) string { return "expire_order_form:" + id.String() }
