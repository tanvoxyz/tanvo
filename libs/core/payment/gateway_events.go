package payment

import (
	"encoding/json"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Event is a typed provider event decoded from a webhook delivery
// ([WebhookGateway.ParseWebhook]): one of [PaymentEvent], [RefundEvent],
// [PaymentMethodAttached], [PaymentMethodSetupFailed],
// [PaymentMethodDetached], [CustomerUpdated], [DisputeClosed] and
// [CustomerBalanceFunded].
type Event interface {
	// Meta returns the event's envelope.
	Meta() EventMeta
	event()
}

// EventMeta is the envelope of a provider event.
type EventMeta struct {
	// ProviderEventID is the provider's event ID, the deduplication key.
	ProviderEventID string
	// Type is the provider's event type ("payment_intent.succeeded").
	Type string
	// Livemode is false for the provider's test mode; handlers ignore
	// unknown objects of test-mode events (lago handle_event_service.rb).
	Livemode bool
	// OccurredAt is when the provider created the event, in UTC.
	OccurredAt time.Time
}

// Meta implements [Event].
func (m EventMeta) Meta() EventMeta { return m }

// Refs are the Tanvo identifiers an adapter attached to a provider object
// as metadata and reads back from its events. Zero fields were absent.
type Refs struct {
	// CustomerID is the customer.
	CustomerID core.CustomerID
	// Payable is the paid document; its Type is zero when absent.
	Payable Payable
	// PaymentID is the payment.
	PaymentID core.PaymentID
	// CreditNoteID is the refunded credit note.
	CreditNoteID core.CreditNoteID
	// InvoiceID is the invoice of a refund or a checkout page.
	InvoiceID core.InvoiceID
}

// PaymentEvent reports a payment's new state.
type PaymentEvent struct {
	EventMeta
	// ProviderPaymentID is the provider's payment.
	ProviderPaymentID string
	// ProviderStatus is the raw provider status.
	ProviderStatus string
	// Status is the mapped payment state.
	Status State
	// RequiresAction reports that the customer must act.
	RequiresAction bool
	// NextActionURL is where the customer acts.
	NextActionURL string
	// NextAction is the raw next-action object.
	NextAction json.RawMessage
	// Amount is the amount, in minor units.
	Amount int64
	// Currency is the currency.
	Currency money.Currency
	// ErrorCode is the provider's code of the last failure.
	ErrorCode string
	// ErrorMessage is the provider's message of the last failure.
	ErrorMessage string
	// ProviderCustomerID is the provider customer.
	ProviderCustomerID string
	// ProviderMethodID is the method charged.
	ProviderMethodID string
	// OneTime reports a payment made through a hosted checkout page,
	// unknown to Tanvo until this event.
	OneTime bool
	// Refs are the Tanvo identifiers attached to the payment.
	Refs Refs
}

// RefundEvent reports a refund's new state.
type RefundEvent struct {
	EventMeta
	// ProviderRefundID is the provider's refund.
	ProviderRefundID string
	// ProviderStatus is the raw provider status.
	ProviderStatus string
	// Status is the mapped refund status.
	Status core.RefundStatus
	// Refs are the Tanvo identifiers attached to the refund.
	Refs Refs
}

// PaymentMethodAttached reports a method saved for a provider customer.
type PaymentMethodAttached struct {
	EventMeta
	// ProviderCustomerID is the provider customer.
	ProviderCustomerID string
	// ProviderMethodID is the saved method.
	ProviderMethodID string
	// Details describe the method when the event carries them; otherwise
	// the handler fetches them ([MethodGateway.PaymentMethod]).
	Details MethodDetails
	// SetAsDefault reports a method saved through a setup page: the
	// handler checks it is still attached, makes it the customer's default
	// at the provider ([MethodGateway.SetDefaultPaymentMethod]) and in
	// Tanvo (lago setup_intent_succeeded_service.rb).
	SetAsDefault bool
	// Refs are the Tanvo identifiers attached to the setup.
	Refs Refs
}

// PaymentMethodSetupFailed reports a failed attempt to save a payment method
// for a customer (a refused Adyen zero-amount authorization, a failed
// Moneyhash card tokenization): the handler reports it with the
// customer.payment_provider_error webhook (lago
// payment_provider_customers/adyen_service.rb preauthorise).
type PaymentMethodSetupFailed struct {
	EventMeta
	// ProviderCustomerID is the provider customer, or the shopper
	// reference the setup page was made for.
	ProviderCustomerID string
	// ErrorCode is the provider's failure code.
	ErrorCode string
	// ErrorMessage is the provider's failure message.
	ErrorMessage string
	// Refs are the Tanvo identifiers attached to the setup.
	Refs Refs
}

// PaymentMethodDetached reports a method removed from a provider customer.
type PaymentMethodDetached struct {
	EventMeta
	// ProviderCustomerID is the provider customer it was attached to.
	ProviderCustomerID string
	// ProviderMethodID is the removed method.
	ProviderMethodID string
	// Refs are the Tanvo identifiers attached to the method.
	Refs Refs
}

// CustomerUpdated reports a provider customer change.
type CustomerUpdated struct {
	EventMeta
	// ProviderCustomerID is the provider customer.
	ProviderCustomerID string
	// DefaultMethodID is its default method; "" when none.
	DefaultMethodID string
	// Refs are the Tanvo identifiers attached to the customer.
	Refs Refs
}

// DisputeClosed reports a closed payment dispute.
type DisputeClosed struct {
	EventMeta
	// ProviderPaymentID is the disputed payment.
	ProviderPaymentID string
	// ProviderStatus is the dispute's raw final status.
	ProviderStatus string
	// Lost reports a dispute lost by the merchant.
	Lost bool
	// Reason is the dispute's reason.
	Reason string
	// ClosedAt is when the dispute closed (the event's creation).
	ClosedAt time.Time
}

// CustomerBalanceFunded reports a bank transfer credited to a provider
// customer's balance.
type CustomerBalanceFunded struct {
	EventMeta
	// ProviderCustomerID is the provider customer.
	ProviderCustomerID string
	// EndingBalance is the unallocated balance after the transfer, in
	// minor units; positive.
	EndingBalance int64
	// Currency is the balance's currency.
	Currency money.Currency
}

func (PaymentEvent) event()             {}
func (RefundEvent) event()              {}
func (PaymentMethodAttached) event()    {}
func (PaymentMethodSetupFailed) event() {}
func (PaymentMethodDetached) event()    {}
func (CustomerUpdated) event()          {}
func (DisputeClosed) event()            {}
func (CustomerBalanceFunded) event()    {}
