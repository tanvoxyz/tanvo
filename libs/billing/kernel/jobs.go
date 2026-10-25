package kernel

import (
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Job kinds billing enqueues or that drive billing; libs/jobs registers a
// handler per kind that decodes the args and calls the billing engine.
const (
	// JobBillSubscriptions bills the subscriptions due at a billing
	// instant (the hourly billing clock, domain §22.2).
	JobBillSubscriptions = "billing.bill_subscriptions"
	// JobBillSubscription bills a group of subscriptions of one customer.
	JobBillSubscription = "billing.bill_subscription"
	// JobRetryGeneratingInvoices enqueues a [JobBillSubscription] for every
	// subscription invoice of an organization left generating a day
	// before the clock instant (lago
	// Clock::RetryGeneratingSubscriptionInvoicesJob); its args are
	// [InstantArgs].
	JobRetryGeneratingInvoices = "billing.retry_generating_invoices"
	// JobFinalizeDraft finalizes a draft invoice whose grace period ended.
	JobFinalizeDraft = "billing.finalize_draft"
	// JobRefreshDraft recomputes a draft invoice.
	JobRefreshDraft = "billing.refresh_draft"
	// JobCreateInAdvanceChargeFees bills an event of pay-in-advance
	// charges.
	JobCreateInAdvanceChargeFees = "billing.create_in_advance_charge_fees"
	// JobPullProviderTaxes pulls the taxes of an invoice waiting for its
	// tax provider.
	JobPullProviderTaxes = "billing.pull_provider_taxes"
	// JobVoidProviderTaxes tells the tax provider a finalized invoice was
	// voided.
	JobVoidProviderTaxes = "billing.void_provider_taxes"
	// JobRefundCreditNote sends a credit note's refund to the payment
	// provider.
	JobRefundCreditNote = "billing.refund_credit_note"
	// JobCheckUsageThresholds checks the progressive billing thresholds of
	// a subscription after usage changed.
	JobCheckUsageThresholds = "billing.check_usage_thresholds"
	// JobRefreshLifetimeUsage recomputes the lifetime usage of a
	// subscription.
	JobRefreshLifetimeUsage = "billing.refresh_lifetime_usage"
	// JobRefreshWalletOngoingBalance recomputes a wallet's ongoing balance.
	JobRefreshWalletOngoingBalance = "billing.refresh_wallet_ongoing_balance"
	// JobWalletThresholdTopUp checks the threshold top-up rules of a
	// wallet.
	JobWalletThresholdTopUp = "billing.wallet_threshold_top_up"
	// JobWalletIntervalTopUps runs the interval top-up rules due at an
	// instant.
	JobWalletIntervalTopUps = "billing.wallet_interval_top_ups"
	// JobExpireWallets terminates the wallets whose expiration passed.
	JobExpireWallets = "billing.expire_wallets"
	// JobTerminateEndedSubscriptions terminates subscriptions whose ending
	// date passed.
	JobTerminateEndedSubscriptions = "billing.terminate_ended_subscriptions"
	// JobActivatePendingSubscriptions starts the subscriptions whose start
	// date arrived.
	JobActivatePendingSubscriptions = "billing.activate_pending_subscriptions"
	// JobFinalizeDueDrafts finalizes every draft whose grace period ended.
	JobFinalizeDueDrafts = "billing.finalize_due_drafts"
	// JobRefreshFlaggedDrafts refreshes the drafts flagged for refresh.
	JobRefreshFlaggedDrafts = "billing.refresh_flagged_drafts"
	// JobRetryTaxFailedInvoices retries the failed invoices of an
	// organization whose tax error tells of the tax provider's API limit
	// (lago Clock::RetryFailedInvoicesJob).
	JobRetryTaxFailedInvoices = "billing.retry_tax_failed_invoices"
	// JobRetryInvoices retries the failed invoices of an organization
	// listed in its [InvoiceIDsArgs] (lago Invoices::RetryAllJob).
	JobRetryInvoices = "billing.retry_invoices"
	// JobRetryInvoicePayments retries the payments of the invoices of an
	// organization listed in its [InvoiceIDsArgs] (lago
	// Invoices::Payments::RetryAllJob).
	JobRetryInvoicePayments = "billing.retry_invoice_payments"

	// JobCreateInvoicePayment collects the payment of a finalized invoice.
	JobCreateInvoicePayment = "payments.create_invoice_payment"
	// JobGenerateInvoiceDocument renders the PDF of a finalized invoice.
	JobGenerateInvoiceDocument = "documents.generate_invoice"
	// JobGenerateCreditNoteDocument renders the PDF of a credit note.
	JobGenerateCreditNoteDocument = "documents.generate_credit_note"
	// JobGeneratePaymentReceiptDocument renders the documents of a payment
	// receipt, announces them with payment_receipt.generated and emails the
	// receipt when its billing entity sends payment_receipt.created emails
	// (lago PaymentReceipts::GenerateDocumentsJob).
	JobGeneratePaymentReceiptDocument = "documents.generate_payment_receipt"
	// JobSendDocumentEmail emails an invoice, a credit note or a payment
	// receipt (args [DocumentEmailArgs]; lago InvoiceMailer#created,
	// CreditNoteMailer#created and PaymentReceiptMailer#created delivered
	// by SendEmailJob).
	JobSendDocumentEmail = "mail.send_document"
)

// Documents emailed by [JobSendDocumentEmail].
const (
	// DocumentInvoice is an invoice.
	DocumentInvoice = "invoice"
	// DocumentCreditNote is a credit note.
	DocumentCreditNote = "credit_note"
	// DocumentPaymentReceipt is a payment receipt.
	DocumentPaymentReceipt = "payment_receipt"
)

// DocumentEmailArgs are the args of [JobSendDocumentEmail].
type DocumentEmailArgs struct {
	// Type is [DocumentInvoice], [DocumentCreditNote] or
	// [DocumentPaymentReceipt].
	Type string `json:"type"`
	// ID is the document's ID.
	ID uuid.UUID `json:"id"`
	// To replaces the customer's addresses when set; Cc and Bcc are
	// copied (lago Emails::ResendService params).
	To []string `json:"to,omitempty"`
	// Cc are the copied recipients.
	Cc []string `json:"cc,omitempty"`
	// Bcc are the hidden recipients.
	Bcc []string `json:"bcc,omitempty"`
	// Resend marks a manual resend: email settings are ignored and the
	// email.sent activity log is "resent" from the system source, as
	// lago's Emails::ResendService passes no user or API key
	// (app/services/emails/resend_service.rb:44-47).
	Resend bool `json:"resend,omitempty"`
}

// BillSubscriptionsArgs are the args of [JobBillSubscriptions].
type BillSubscriptionsArgs struct {
	// BillingAt is the billing instant.
	BillingAt time.Time `json:"billing_at"`
}

// BillSubscriptionArgs are the args of [JobBillSubscription].
type BillSubscriptionArgs struct {
	// SubscriptionIDs are the billed subscriptions (one customer).
	SubscriptionIDs []core.SubscriptionID `json:"subscription_ids"`
	// BillingAt is the billing instant.
	BillingAt time.Time `json:"billing_at"`
	// Reason is the invoicing reason.
	Reason string `json:"reason"`
	// InvoiceID retries the generation of an existing invoice.
	InvoiceID core.InvoiceID `json:"invoice_id,omitzero"`
}

// InvoiceArgs are the args of jobs about one invoice.
type InvoiceArgs struct {
	// InvoiceID is the invoice.
	InvoiceID core.InvoiceID `json:"invoice_id"`
	// At is the instant the job was scheduled for, when it matters: a
	// [JobFinalizeDraft] enqueued by the due-drafts sweep dates the invoice
	// from the sweep's instant, not from when a worker ran it. Zero means
	// the worker's clock.
	At time.Time `json:"at,omitzero"`
	// PaymentMethodType and PaymentMethodID override the payment method
	// of a [JobCreateInvoicePayment] retried with one (lago
	// PaymentMethods::DetermineService payment_method_params); zero
	// derives it from the invoice.
	PaymentMethodType core.PaymentMethodType `json:"payment_method_type,omitempty"`
	// PaymentMethodID is the override payment method.
	PaymentMethodID core.PaymentMethodID `json:"payment_method_id,omitzero"`
	// Notify emails a [JobGenerateInvoiceDocument]'s invoice once its
	// documents are rendered (lago GenerateDocumentsJob notify:).
	Notify bool `json:"notify,omitempty"`
	// Force renders the documents of a [JobGenerateInvoiceDocument] again
	// even when they exist (lago's admin context).
	Force bool `json:"force,omitempty"`
}

// InvoiceIDsArgs are the args of jobs about several invoices.
type InvoiceIDsArgs struct {
	// InvoiceIDs are the invoices.
	InvoiceIDs []core.InvoiceID `json:"invoice_ids"`
}

// CreditNoteArgs are the args of jobs about one credit note.
type CreditNoteArgs struct {
	// CreditNoteID is the credit note.
	CreditNoteID core.CreditNoteID `json:"credit_note_id"`
	// Force renders the documents of a [JobGenerateCreditNoteDocument]
	// again even when they exist (lago's admin context).
	Force bool `json:"force,omitempty"`
}

// PaymentReceiptArgs are the args of [JobGeneratePaymentReceiptDocument].
type PaymentReceiptArgs struct {
	// PaymentReceiptID is the receipt.
	PaymentReceiptID core.PaymentReceiptID `json:"payment_receipt_id"`
	// Notify emails the receipt once rendered: its billing entity sent
	// payment_receipt.created emails when it was issued.
	Notify bool `json:"notify,omitempty"`
	// Force renders the documents again even when they exist (lago's
	// admin context).
	Force bool `json:"force,omitempty"`
}

// InAdvanceChargeArgs are the args of [JobCreateInAdvanceChargeFees].
type InAdvanceChargeArgs struct {
	// SubscriptionID is the subscription the event belongs to, or the
	// contract whose rate cards bill it in advance.
	SubscriptionID core.SubscriptionID `json:"subscription_id"`
	// EventID is the stored event's ID, set on the fees it bills (lago
	// fees.pay_in_advance_event_id); zero when unknown.
	EventID core.EventID `json:"event_id,omitzero"`
	// TransactionID is the event's transaction ID.
	TransactionID string `json:"transaction_id"`
	// Timestamp is the event's timestamp.
	Timestamp time.Time `json:"timestamp"`
	// Code is the billable metric code.
	Code string `json:"code"`
	// Properties are the event properties.
	Properties map[string]string `json:"properties,omitempty"`
	// PreciseTotalAmountCents is the amount the event carries for dynamic
	// charges, or nil.
	PreciseTotalAmountCents *money.Decimal `json:"precise_total_amount_cents,omitempty"`
}

// SubscriptionArgs are the args of jobs about one subscription.
type SubscriptionArgs struct {
	// SubscriptionID is the subscription.
	SubscriptionID core.SubscriptionID `json:"subscription_id"`
}

// WalletArgs are the args of jobs about one wallet.
type WalletArgs struct {
	// WalletID is the wallet.
	WalletID core.WalletID `json:"wallet_id"`
}

// InstantArgs are the args of periodic jobs run at an instant.
type InstantArgs struct {
	// At is the run instant.
	At time.Time `json:"at"`
}

// QueueOf returns the queue of a job kind.
func QueueOf(kind string) port.Queue {
	switch kind {
	case JobCreateInvoicePayment, JobProcessDunningCampaigns, JobProcessDunningCustomer, JobProcessDunningAttempt,
		JobCreateRequestPayment, JobSendPaymentRequestEmail, JobSyncProviderCustomer, JobSetPaymentMethod,
		JobExpireCheckout, JobCancelAbandonedPayments, JobCancelAbandonedPayment, JobRegisterProviderWebhook, JobExpireConnectionCheckouts,
		JobUpdatePaymentReference, JobCancelPayment, JobCancelAuthorization:
		return port.QueuePayments
	case JobSendDocumentEmail:
		// lago SendEmailJob queue_as "mailers"; Tanvo's emails run on
		// the default queue.
		return port.QueueDefault
	case JobGenerateInvoiceDocument, JobGenerateCreditNoteDocument, JobGeneratePaymentReceiptDocument:
		return port.QueueDocuments
	case JobPullProviderTaxes, JobVoidProviderTaxes, JobReportCreditNoteTaxes:
		return port.QueueIntegrations
	case JobFetchTaxCompany, JobReportInvoiceTaxes, JobSyncContact, JobSyncInvoice, JobSyncCreditNote, JobSyncPayment,
		JobCreateCRMInvoice, JobUpdateCRMInvoice, JobAssociateCRMInvoice, JobCreateCRMSubscription,
		JobUpdateCRMSubscription, JobAssociateCRMSubscription, JobFetchIntegrationItems, JobDeployCRM, JobSaveCRMAccount:
		// lago queue_as "integrations" (app/jobs/integrations/).
		return port.QueueIntegrations
	case JobProcessSubscriptionActivity, JobProcessLifetimeUsageAlert:
		// lago app/jobs/usage_monitoring/process_subscription_activity_job.rb:6-10
		// and process_lifetime_usage_alert_job.rb:6-10.
		return port.QueueAlerts
	case JobProcessWalletAlerts:
		// lago app/jobs/usage_monitoring/process_wallet_alerts_job.rb:5-10.
		return port.QueueAlertsHighPriority
	case JobRefreshWalletOngoingBalance:
		// lago app/jobs/customers/refresh_wallet_job.rb:6-11.
		return port.QueueWallets
	case JobComputeDailyUsage, JobFillDailyUsageFromInvoice, JobFillDailyUsageHistory, JobRebuildDailyUsageHistory:
		// lago runs them on the low_priority and long_running queues
		// (app/jobs/daily_usages/), away from billing.
		return port.QueueDefault
	case JobValidateEvents, JobCleanRecordDeletions:
		// lago runs Events::PostValidationJob on the default queue
		// (app/jobs/events/post_validation_job.rb:5-11) and the record
		// deletion cleanup is housekeeping, away from billing.
		return port.QueueDefault
	case JobDestroyMetricFilters:
		// lago app/jobs/billable_metric_filters/destroy_all_job.rb:5.
		return port.QueueDefault
	case JobDefineUsageBuckets:
		// A backfill reads every stored event of a code: away from
		// billing.
		return port.QueueDefault
	default:
		return port.QueueBilling
	}
}

// Jobs of the periodic run and of coupons.
const (
	// JobSwapDowngrade terminates a subscription at its billing instant and
	// starts its pending downgrade (args [SwapDowngradeArgs]); the periodic
	// run enqueues one per subscription, as lago enqueues
	// Subscriptions::TerminateJob.
	JobSwapDowngrade = "billing.swap_downgrade"
	// JobTerminateCoupons terminates the expired coupons of an
	// organization at the clock instant (args [InstantArgs]).
	JobTerminateCoupons = "billing.terminate_coupons"
)

// SwapDowngradeArgs are the args of [JobSwapDowngrade].
type SwapDowngradeArgs struct {
	// SubscriptionID is the subscription with a pending downgrade.
	SubscriptionID core.SubscriptionID `json:"subscription_id"`
	// BillingAt is the billing instant of the periodic run.
	BillingAt time.Time `json:"billing_at"`
}
