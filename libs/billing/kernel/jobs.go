package kernel

import (
	"time"

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
	// JobFinalizeDraft finalizes a draft invoice whose grace period ended.
	JobFinalizeDraft = "billing.finalize_draft"
	// JobRefreshDraft recomputes a draft invoice.
	JobRefreshDraft = "billing.refresh_draft"
	// JobCreateInAdvanceChargeFees bills an event of pay-in-advance
	// charges.
	JobCreateInAdvanceChargeFees = "billing.create_in_advance_charge_fees"
	// JobPullProviderTaxes retries the taxes of an invoice waiting for its
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

	// JobCreateInvoicePayment collects the payment of a finalized invoice.
	JobCreateInvoicePayment = "payments.create_invoice_payment"
	// JobGenerateInvoiceDocument renders the PDF of a finalized invoice.
	JobGenerateInvoiceDocument = "documents.generate_invoice"
	// JobGenerateCreditNoteDocument renders the PDF of a credit note.
	JobGenerateCreditNoteDocument = "documents.generate_credit_note"
)

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
}

// CreditNoteArgs are the args of jobs about one credit note.
type CreditNoteArgs struct {
	// CreditNoteID is the credit note.
	CreditNoteID core.CreditNoteID `json:"credit_note_id"`
}

// InAdvanceChargeArgs are the args of [JobCreateInAdvanceChargeFees].
type InAdvanceChargeArgs struct {
	// SubscriptionID is the subscription the event belongs to.
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
	case JobCreateInvoicePayment:
		return port.QueuePayments
	case JobGenerateInvoiceDocument, JobGenerateCreditNoteDocument:
		return port.QueueDocuments
	case JobPullProviderTaxes, JobVoidProviderTaxes, JobReportCreditNoteTaxes:
		return port.QueueIntegrations
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
