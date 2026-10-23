package webhooks

import "github.com/tanvoxyz/tanvo/libs/core/webhook"

// EventTypeInfo describes an event type endpoints can subscribe to.
type EventTypeInfo struct {
	// Type is the event type.
	Type webhook.EventType
	// Category groups the event types in the console.
	Category string
	// Description says when the event is sent.
	Description string
}

// EventTypes returns the event types endpoints can subscribe to, in the
// order of lago's catalog, without its deprecated ones.
//
// source: config/webhook_event_types.yml, app/graphql/resolvers/event_types_resolver.rb:13-22
func EventTypes() []EventTypeInfo {
	return []EventTypeInfo{
		{Type: "alert.triggered", Category: "Alerts", Description: "One or more thresholds defined in the alert were crossed"},
		{Type: "billable_metric.created", Category: "Billable Metrics", Description: "A new billable metric has been created"},
		{Type: "billable_metric.updated", Category: "Billable Metrics", Description: "A billable metric has been updated"},
		{Type: "billable_metric.deleted", Category: "Billable Metrics", Description: "A billable metric has been deleted"},
		{Type: "customer.created", Category: "Customers", Description: "A new customer has been created"},
		{Type: "customer.updated", Category: "Customers", Description: "A customer has been updated"},
		{Type: "customer.accounting_provider_created", Category: "Customers", Description: "A customer was created on an accouting integration"},
		{Type: "customer.accounting_provider_error", Category: "Customers", Description: "An error was encountered while syncing a customer to an accounting provider"},
		{Type: "customer.crm_provider_created", Category: "Customers", Description: "A customer has been created in the CRM provider"},
		{Type: "customer.crm_provider_error", Category: "Customers", Description: "An error was encountered while syncing a customer to a CRM provider"},
		{Type: "customer.payment_provider_created", Category: "Customers", Description: "A customer has been created on a payment provider"},
		{Type: "customer.payment_provider_error", Category: "Customers", Description: "An error was encountered while syncing a customer to a payment provider"},
		{Type: "customer.checkout_url_generated", Category: "Customers", Description: "A checkout URL was generated for a customer"},
		{Type: "customer.tax_provider_error", Category: "Customers", Description: "An error was encountered while fetching taxes for a customer on a tax provider"},
		{Type: "customer.vies_check", Category: "Customers", Description: "VIES VAT number has been checked for a customer"},
		{Type: "credit_note.created", Category: "Credit Notes", Description: "A new credit note has been created"},
		{Type: "credit_note.generated", Category: "Credit Notes", Description: "A new credit note PDF has been generated"},
		{Type: "credit_note.refund_failure", Category: "Credit Notes", Description: "The refund of a credit note has failed on a payment provider"},
		{Type: "dunning_campaign.finished", Category: "Dunning Campaigns", Description: "The dunning campaign has been completed for a customer"},
		{Type: "events.errors", Category: "Event Ingestion", Description: "Errors were encountered while post-processing some events"},
		{Type: "feature.created", Category: "Features", Description: "A new feature has been created"},
		{Type: "feature.updated", Category: "Features", Description: "A feature has been updated"},
		{Type: "feature.deleted", Category: "Features", Description: "A feature has been deleted"},
		{Type: "fee.created", Category: "Subscriptions and Fees", Description: "A pay in advance fee has been created"},
		{Type: "fee.tax_provider_error", Category: "Subscriptions and Fees", Description: "An error was encountered while fetching taxes for a fee on a tax provider"},
		{Type: "invoice.created", Category: "Invoices", Description: "A new invoice has been emitted"},
		{Type: "invoice.one_off_created", Category: "Invoices", Description: "A new one off invoice has been emitted"},
		{Type: "invoice.paid_credit_added", Category: "Invoices", Description: "A new prepaid credit invoice has been emitted"},
		{Type: "invoice.generated", Category: "Invoices", Description: "A new invoice PDF has been generated"},
		{Type: "invoice.drafted", Category: "Invoices", Description: "A new draft invoice has been emitted"},
		{Type: "invoice.ready_to_finalize", Category: "Invoices", Description: "A draft invoice is ready to be finalized (taxes are resolved)"},
		{Type: "invoice.voided", Category: "Invoices", Description: "An invoice has been voided"},
		{Type: "invoice.deleted", Category: "Invoices", Description: "A draft invoice has been deleted"},
		{Type: "invoice.payment_dispute_lost", Category: "Invoices", Description: "A payment dispute has been lost for an invoice payment"},
		{Type: "invoice.payment_status_updated", Category: "Invoices", Description: "The payment status of an invoice has been updated"},
		{Type: "invoice.payment_overdue", Category: "Invoices", Description: "An invoice payment is overdue"},
		{Type: "invoice.payment_failure", Category: "Invoices", Description: "A payment attempt for an invoice has failed on a payment provider"},
		{Type: "invoice.resynced", Category: "Invoices", Description: "An invoice has been resynced with salesforce"},
		{Type: "integration.provider_error", Category: "Integrations", Description: "An error was encountered while processing data on an integration"},
		{Type: "order.created", Category: "Quotes", Description: "An order has been created from a signed order form"},
		{Type: "order.executed", Category: "Quotes", Description: "An order has been executed"},
		{Type: "order_form.created", Category: "Quotes", Description: "An order form has been generated from an approved quote"},
		{Type: "order_form.signed", Category: "Quotes", Description: "An order form has been marked as signed"},
		{Type: "order_form.expired", Category: "Quotes", Description: "An order form has expired"},
		{Type: "order_form.voided", Category: "Quotes", Description: "An order form has been voided"},
		{Type: "payment.succeeded", Category: "Payments", Description: "A payment has been successfully processed by the payment provider"},
		{Type: "payment.requires_action", Category: "Payments", Description: "An action is required to process a payment"},
		{Type: "payment_provider.error", Category: "Payments", Description: "An error was raised by a payment provider"},
		{Type: "payment_receipt.created", Category: "Payment Receipts", Description: "A new payment receipt has been created"},
		{Type: "payment_receipt.generated", Category: "Payment Receipts", Description: "A new payment receipt PDF has been generated"},
		{Type: "payment_request.created", Category: "Payments", Description: "A new payment request has been created"},
		{Type: "payment_request.payment_failure", Category: "Payments", Description: "A payment attempt for a payment request has failed on a payment provider"},
		{Type: "payment_request.payment_status_updated", Category: "Payments", Description: "The payment status of a payment request has been updated"},
		{Type: "plan.created", Category: "Plans", Description: "A new plan has been created"},
		{Type: "plan.updated", Category: "Plans", Description: "A plan has been updated"},
		{Type: "plan.deleted", Category: "Plans", Description: "A plan has been deleted"},
		{Type: "quote.created", Category: "Quotes", Description: "A new quote has been created"},
		{Type: "quote.approved", Category: "Quotes", Description: "A quote has been approved"},
		{Type: "quote.voided", Category: "Quotes", Description: "A quote has been voided"},
		{Type: "subscription.canceled", Category: "Subscriptions and Fees", Description: "A subscription has been canceled"},
		{Type: "subscription.incomplete", Category: "Subscriptions and Fees", Description: "A subscription is awaiting activation rule resolution before becoming active"},
		{Type: "subscription.terminated", Category: "Subscriptions and Fees", Description: "A subscription has been terminated"},
		{Type: "subscription.started", Category: "Subscriptions and Fees", Description: "An subscription has started"},
		{Type: "subscription.updated", Category: "Subscriptions and Fees", Description: "A subscription has been updated"},
		{Type: "subscription.termination_alert", Category: "Subscriptions and Fees", Description: "A subscription will be terminated in the future"},
		{Type: "subscription.trial_ended", Category: "Subscriptions and Fees", Description: "A subscription trial period has ended"},
		{Type: "subscription.usage_threshold_reached", Category: "Subscriptions and Fees", Description: "A usage threshold has been reached by a subscription"},
		{Type: "wallet.created", Category: "Wallets and Credits", Description: "A new wallet has been created"},
		{Type: "wallet.depleted_ongoing_balance", Category: "Wallets and Credits", Description: "The balance of a wallet has been depleted"},
		{Type: "wallet.terminated", Category: "Wallets and Credits", Description: "A wallet has been terminated"},
		{Type: "wallet.updated", Category: "Wallets and Credits", Description: "A wallet has been updated"},
		{Type: "wallet_transaction.created", Category: "Wallets and Credits", Description: "A new wallet transaction has been created"},
		{Type: "wallet_transaction.updated", Category: "Wallets and Credits", Description: "A wallet transaction has been updated"},
		{Type: "wallet_transaction.payment_failure", Category: "Wallets and Credits", Description: "A payment attempt for a wallet transaction has failed on a payment provider"},
	}
}
