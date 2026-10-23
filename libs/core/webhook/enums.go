package webhook

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Status is the delivery status of a webhook (domain §18.2).
type Status string

// Status values.
const (
	// StatusPending is "pending": not attempted yet.
	StatusPending Status = "pending"
	// StatusRetrying is "retrying": failed at least once, will be retried.
	StatusRetrying Status = "retrying"
	// StatusSucceeded is "succeeded": delivered with a 2xx response.
	StatusSucceeded Status = "succeeded"
	// StatusFailed is "failed": every automatic attempt failed.
	StatusFailed Status = "failed"
)

// StatusValues returns every Status, in declaration order.
func StatusValues() []Status {
	return []Status{StatusPending, StatusRetrying, StatusSucceeded, StatusFailed}
}

// Valid reports whether v is one of the declared Status values.
func (v Status) Valid() bool {
	switch v {
	case StatusPending, StatusRetrying, StatusSucceeded, StatusFailed:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Status) String() string { return string(v) }

// ParseStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseStatus(s string) (Status, error) {
	if v := Status(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("webhook.Status", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Status) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Status) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// EventType is the type of a domain event delivered by webhooks (domain §18.3).
type EventType string

// EventType values.
const (
	// EventTypeAlertTriggered is the EventType "alert.triggered".
	EventTypeAlertTriggered EventType = "alert.triggered"
	// EventTypeBillableMetricCreated is the EventType "billable_metric.created".
	EventTypeBillableMetricCreated EventType = "billable_metric.created"
	// EventTypeBillableMetricUpdated is the EventType "billable_metric.updated".
	EventTypeBillableMetricUpdated EventType = "billable_metric.updated"
	// EventTypeBillableMetricDeleted is the EventType "billable_metric.deleted".
	EventTypeBillableMetricDeleted EventType = "billable_metric.deleted"
	// EventTypePlanCreated is the EventType "plan.created".
	EventTypePlanCreated EventType = "plan.created"
	// EventTypePlanUpdated is the EventType "plan.updated".
	EventTypePlanUpdated EventType = "plan.updated"
	// EventTypePlanDeleted is the EventType "plan.deleted".
	EventTypePlanDeleted EventType = "plan.deleted"
	// EventTypeFeatureCreated is the EventType "feature.created".
	EventTypeFeatureCreated EventType = "feature.created"
	// EventTypeFeatureUpdated is the EventType "feature.updated".
	EventTypeFeatureUpdated EventType = "feature.updated"
	// EventTypeFeatureDeleted is the EventType "feature.deleted".
	EventTypeFeatureDeleted EventType = "feature.deleted"
	// EventTypeCustomerCreated is the EventType "customer.created".
	EventTypeCustomerCreated EventType = "customer.created"
	// EventTypeCustomerUpdated is the EventType "customer.updated".
	EventTypeCustomerUpdated EventType = "customer.updated"
	// EventTypeCustomerVIESCheck is the EventType "customer.vies_check".
	EventTypeCustomerVIESCheck EventType = "customer.vies_check"
	// EventTypeCustomerAccountingProviderCreated is the EventType "customer.accounting_provider_created".
	EventTypeCustomerAccountingProviderCreated EventType = "customer.accounting_provider_created"
	// EventTypeCustomerAccountingProviderError is the EventType "customer.accounting_provider_error".
	EventTypeCustomerAccountingProviderError EventType = "customer.accounting_provider_error"
	// EventTypeCustomerCRMProviderCreated is the EventType "customer.crm_provider_created".
	EventTypeCustomerCRMProviderCreated EventType = "customer.crm_provider_created"
	// EventTypeCustomerCRMProviderError is the EventType "customer.crm_provider_error".
	EventTypeCustomerCRMProviderError EventType = "customer.crm_provider_error"
	// EventTypeCustomerPaymentProviderCreated is the EventType "customer.payment_provider_created".
	EventTypeCustomerPaymentProviderCreated EventType = "customer.payment_provider_created"
	// EventTypeCustomerPaymentProviderError is the EventType "customer.payment_provider_error".
	EventTypeCustomerPaymentProviderError EventType = "customer.payment_provider_error"
	// EventTypeCustomerCheckoutURLGenerated is the EventType "customer.checkout_url_generated".
	EventTypeCustomerCheckoutURLGenerated EventType = "customer.checkout_url_generated"
	// EventTypeCustomerTaxProviderError is the EventType "customer.tax_provider_error".
	EventTypeCustomerTaxProviderError EventType = "customer.tax_provider_error"
	// EventTypeSubscriptionStarted is the EventType "subscription.started".
	EventTypeSubscriptionStarted EventType = "subscription.started"
	// EventTypeSubscriptionIncomplete is the EventType "subscription.incomplete".
	EventTypeSubscriptionIncomplete EventType = "subscription.incomplete"
	// EventTypeSubscriptionUpdated is the EventType "subscription.updated".
	EventTypeSubscriptionUpdated EventType = "subscription.updated"
	// EventTypeSubscriptionTrialEnded is the EventType "subscription.trial_ended".
	EventTypeSubscriptionTrialEnded EventType = "subscription.trial_ended"
	// EventTypeSubscriptionTerminationAlert is the EventType "subscription.termination_alert".
	EventTypeSubscriptionTerminationAlert EventType = "subscription.termination_alert"
	// EventTypeSubscriptionTerminated is the EventType "subscription.terminated".
	EventTypeSubscriptionTerminated EventType = "subscription.terminated"
	// EventTypeSubscriptionCanceled is the EventType "subscription.canceled".
	EventTypeSubscriptionCanceled EventType = "subscription.canceled"
	// EventTypeSubscriptionUsageThresholdReached is the EventType "subscription.usage_threshold_reached".
	EventTypeSubscriptionUsageThresholdReached EventType = "subscription.usage_threshold_reached"
	// EventTypeEventsErrors is the EventType "events.errors".
	EventTypeEventsErrors EventType = "events.errors"
	// EventTypeFeeCreated is the EventType "fee.created".
	EventTypeFeeCreated EventType = "fee.created"
	// EventTypeFeeTaxProviderError is the EventType "fee.tax_provider_error".
	EventTypeFeeTaxProviderError EventType = "fee.tax_provider_error"
	// EventTypeInvoiceCreated is the EventType "invoice.created".
	EventTypeInvoiceCreated EventType = "invoice.created"
	// EventTypeInvoiceDrafted is the EventType "invoice.drafted".
	EventTypeInvoiceDrafted EventType = "invoice.drafted"
	// EventTypeInvoiceOneOffCreated is the EventType "invoice.one_off_created".
	EventTypeInvoiceOneOffCreated EventType = "invoice.one_off_created"
	// EventTypeInvoicePaidCreditAdded is the EventType "invoice.paid_credit_added".
	EventTypeInvoicePaidCreditAdded EventType = "invoice.paid_credit_added"
	// EventTypeInvoiceGenerated is the EventType "invoice.generated".
	EventTypeInvoiceGenerated EventType = "invoice.generated"
	// EventTypeInvoiceReadyToFinalize is the EventType "invoice.ready_to_finalize".
	EventTypeInvoiceReadyToFinalize EventType = "invoice.ready_to_finalize"
	// EventTypeInvoiceVoided is the EventType "invoice.voided".
	EventTypeInvoiceVoided EventType = "invoice.voided"
	// EventTypeInvoiceDeleted is the EventType "invoice.deleted".
	EventTypeInvoiceDeleted EventType = "invoice.deleted"
	// EventTypeInvoicePaymentStatusUpdated is the EventType "invoice.payment_status_updated".
	EventTypeInvoicePaymentStatusUpdated EventType = "invoice.payment_status_updated"
	// EventTypeInvoicePaymentOverdue is the EventType "invoice.payment_overdue".
	EventTypeInvoicePaymentOverdue EventType = "invoice.payment_overdue"
	// EventTypeInvoicePaymentFailure is the EventType "invoice.payment_failure".
	EventTypeInvoicePaymentFailure EventType = "invoice.payment_failure"
	// EventTypeInvoicePaymentDisputeLost is the EventType "invoice.payment_dispute_lost".
	EventTypeInvoicePaymentDisputeLost EventType = "invoice.payment_dispute_lost"
	// EventTypeInvoiceResynced is the EventType "invoice.resynced".
	EventTypeInvoiceResynced EventType = "invoice.resynced"
	// EventTypeCreditNoteCreated is the EventType "credit_note.created".
	EventTypeCreditNoteCreated EventType = "credit_note.created"
	// EventTypeCreditNoteGenerated is the EventType "credit_note.generated".
	EventTypeCreditNoteGenerated EventType = "credit_note.generated"
	// EventTypeCreditNoteRefundFailure is the EventType "credit_note.refund_failure".
	EventTypeCreditNoteRefundFailure EventType = "credit_note.refund_failure"
	// EventTypePaymentSucceeded is the EventType "payment.succeeded".
	EventTypePaymentSucceeded EventType = "payment.succeeded"
	// EventTypePaymentRequiresAction is the EventType "payment.requires_action".
	EventTypePaymentRequiresAction EventType = "payment.requires_action"
	// EventTypePaymentProviderError is the EventType "payment_provider.error".
	EventTypePaymentProviderError EventType = "payment_provider.error"
	// EventTypePaymentReceiptCreated is the EventType "payment_receipt.created".
	EventTypePaymentReceiptCreated EventType = "payment_receipt.created"
	// EventTypePaymentReceiptGenerated is the EventType "payment_receipt.generated".
	EventTypePaymentReceiptGenerated EventType = "payment_receipt.generated"
	// EventTypePaymentRequestCreated is the EventType "payment_request.created".
	EventTypePaymentRequestCreated EventType = "payment_request.created"
	// EventTypePaymentRequestPaymentStatusUpdated is the EventType "payment_request.payment_status_updated".
	EventTypePaymentRequestPaymentStatusUpdated EventType = "payment_request.payment_status_updated"
	// EventTypePaymentRequestPaymentFailure is the EventType "payment_request.payment_failure".
	EventTypePaymentRequestPaymentFailure EventType = "payment_request.payment_failure"
	// EventTypeWalletCreated is the EventType "wallet.created".
	EventTypeWalletCreated EventType = "wallet.created"
	// EventTypeWalletUpdated is the EventType "wallet.updated".
	EventTypeWalletUpdated EventType = "wallet.updated"
	// EventTypeWalletTerminated is the EventType "wallet.terminated".
	EventTypeWalletTerminated EventType = "wallet.terminated"
	// EventTypeWalletDepletedOngoingBalance is the EventType "wallet.depleted_ongoing_balance".
	EventTypeWalletDepletedOngoingBalance EventType = "wallet.depleted_ongoing_balance"
	// EventTypeWalletTransactionCreated is the EventType "wallet_transaction.created".
	EventTypeWalletTransactionCreated EventType = "wallet_transaction.created"
	// EventTypeWalletTransactionUpdated is the EventType "wallet_transaction.updated".
	EventTypeWalletTransactionUpdated EventType = "wallet_transaction.updated"
	// EventTypeWalletTransactionPaymentFailure is the EventType "wallet_transaction.payment_failure".
	EventTypeWalletTransactionPaymentFailure EventType = "wallet_transaction.payment_failure"
	// EventTypeDunningCampaignFinished is the EventType "dunning_campaign.finished".
	EventTypeDunningCampaignFinished EventType = "dunning_campaign.finished"
	// EventTypeIntegrationProviderError is the EventType "integration.provider_error".
	EventTypeIntegrationProviderError EventType = "integration.provider_error"
	// EventTypeQuoteCreated is the EventType "quote.created".
	EventTypeQuoteCreated EventType = "quote.created"
	// EventTypeQuoteApproved is the EventType "quote.approved".
	EventTypeQuoteApproved EventType = "quote.approved"
	// EventTypeQuoteVoided is the EventType "quote.voided".
	EventTypeQuoteVoided EventType = "quote.voided"
	// EventTypeOrderFormCreated is the EventType "order_form.created".
	EventTypeOrderFormCreated EventType = "order_form.created"
	// EventTypeOrderFormSigned is the EventType "order_form.signed".
	EventTypeOrderFormSigned EventType = "order_form.signed"
	// EventTypeOrderFormExpired is the EventType "order_form.expired".
	EventTypeOrderFormExpired EventType = "order_form.expired"
	// EventTypeOrderFormVoided is the EventType "order_form.voided".
	EventTypeOrderFormVoided EventType = "order_form.voided"
	// EventTypeOrderCreated is the EventType "order.created".
	EventTypeOrderCreated EventType = "order.created"
	// EventTypeOrderExecuted is the EventType "order.executed".
	EventTypeOrderExecuted EventType = "order.executed"
)

// EventTypeValues returns every EventType, in declaration order.
func EventTypeValues() []EventType {
	return []EventType{EventTypeAlertTriggered, EventTypeBillableMetricCreated, EventTypeBillableMetricUpdated, EventTypeBillableMetricDeleted, EventTypePlanCreated, EventTypePlanUpdated, EventTypePlanDeleted, EventTypeFeatureCreated, EventTypeFeatureUpdated, EventTypeFeatureDeleted, EventTypeCustomerCreated, EventTypeCustomerUpdated, EventTypeCustomerVIESCheck, EventTypeCustomerAccountingProviderCreated, EventTypeCustomerAccountingProviderError, EventTypeCustomerCRMProviderCreated, EventTypeCustomerCRMProviderError, EventTypeCustomerPaymentProviderCreated, EventTypeCustomerPaymentProviderError, EventTypeCustomerCheckoutURLGenerated, EventTypeCustomerTaxProviderError, EventTypeSubscriptionStarted, EventTypeSubscriptionIncomplete, EventTypeSubscriptionUpdated, EventTypeSubscriptionTrialEnded, EventTypeSubscriptionTerminationAlert, EventTypeSubscriptionTerminated, EventTypeSubscriptionCanceled, EventTypeSubscriptionUsageThresholdReached, EventTypeEventsErrors, EventTypeFeeCreated, EventTypeFeeTaxProviderError, EventTypeInvoiceCreated, EventTypeInvoiceDrafted, EventTypeInvoiceOneOffCreated, EventTypeInvoicePaidCreditAdded, EventTypeInvoiceGenerated, EventTypeInvoiceReadyToFinalize, EventTypeInvoiceVoided, EventTypeInvoiceDeleted, EventTypeInvoicePaymentStatusUpdated, EventTypeInvoicePaymentOverdue, EventTypeInvoicePaymentFailure, EventTypeInvoicePaymentDisputeLost, EventTypeInvoiceResynced, EventTypeCreditNoteCreated, EventTypeCreditNoteGenerated, EventTypeCreditNoteRefundFailure, EventTypePaymentSucceeded, EventTypePaymentRequiresAction, EventTypePaymentProviderError, EventTypePaymentReceiptCreated, EventTypePaymentReceiptGenerated, EventTypePaymentRequestCreated, EventTypePaymentRequestPaymentStatusUpdated, EventTypePaymentRequestPaymentFailure, EventTypeWalletCreated, EventTypeWalletUpdated, EventTypeWalletTerminated, EventTypeWalletDepletedOngoingBalance, EventTypeWalletTransactionCreated, EventTypeWalletTransactionUpdated, EventTypeWalletTransactionPaymentFailure, EventTypeDunningCampaignFinished, EventTypeIntegrationProviderError, EventTypeQuoteCreated, EventTypeQuoteApproved, EventTypeQuoteVoided, EventTypeOrderFormCreated, EventTypeOrderFormSigned, EventTypeOrderFormExpired, EventTypeOrderFormVoided, EventTypeOrderCreated, EventTypeOrderExecuted}
}

// Valid reports whether v is one of the declared EventType values.
func (v EventType) Valid() bool {
	switch v {
	case EventTypeAlertTriggered, EventTypeBillableMetricCreated, EventTypeBillableMetricUpdated, EventTypeBillableMetricDeleted, EventTypePlanCreated, EventTypePlanUpdated, EventTypePlanDeleted, EventTypeFeatureCreated, EventTypeFeatureUpdated, EventTypeFeatureDeleted, EventTypeCustomerCreated, EventTypeCustomerUpdated, EventTypeCustomerVIESCheck, EventTypeCustomerAccountingProviderCreated, EventTypeCustomerAccountingProviderError, EventTypeCustomerCRMProviderCreated, EventTypeCustomerCRMProviderError, EventTypeCustomerPaymentProviderCreated, EventTypeCustomerPaymentProviderError, EventTypeCustomerCheckoutURLGenerated, EventTypeCustomerTaxProviderError, EventTypeSubscriptionStarted, EventTypeSubscriptionIncomplete, EventTypeSubscriptionUpdated, EventTypeSubscriptionTrialEnded, EventTypeSubscriptionTerminationAlert, EventTypeSubscriptionTerminated, EventTypeSubscriptionCanceled, EventTypeSubscriptionUsageThresholdReached, EventTypeEventsErrors, EventTypeFeeCreated, EventTypeFeeTaxProviderError, EventTypeInvoiceCreated, EventTypeInvoiceDrafted, EventTypeInvoiceOneOffCreated, EventTypeInvoicePaidCreditAdded, EventTypeInvoiceGenerated, EventTypeInvoiceReadyToFinalize, EventTypeInvoiceVoided, EventTypeInvoiceDeleted, EventTypeInvoicePaymentStatusUpdated, EventTypeInvoicePaymentOverdue, EventTypeInvoicePaymentFailure, EventTypeInvoicePaymentDisputeLost, EventTypeInvoiceResynced, EventTypeCreditNoteCreated, EventTypeCreditNoteGenerated, EventTypeCreditNoteRefundFailure, EventTypePaymentSucceeded, EventTypePaymentRequiresAction, EventTypePaymentProviderError, EventTypePaymentReceiptCreated, EventTypePaymentReceiptGenerated, EventTypePaymentRequestCreated, EventTypePaymentRequestPaymentStatusUpdated, EventTypePaymentRequestPaymentFailure, EventTypeWalletCreated, EventTypeWalletUpdated, EventTypeWalletTerminated, EventTypeWalletDepletedOngoingBalance, EventTypeWalletTransactionCreated, EventTypeWalletTransactionUpdated, EventTypeWalletTransactionPaymentFailure, EventTypeDunningCampaignFinished, EventTypeIntegrationProviderError, EventTypeQuoteCreated, EventTypeQuoteApproved, EventTypeQuoteVoided, EventTypeOrderFormCreated, EventTypeOrderFormSigned, EventTypeOrderFormExpired, EventTypeOrderFormVoided, EventTypeOrderCreated, EventTypeOrderExecuted:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v EventType) String() string { return string(v) }

// ParseEventType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseEventType(s string) (EventType, error) {
	if v := EventType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("webhook.EventType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v EventType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *EventType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseEventType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
