package payment

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors of the payment provider port that are not about one provider
// call.
var (
	// ErrUnsupported: the provider lacks the operation.
	ErrUnsupported = errors.New("payment provider: operation not supported")
	// ErrInvalidSignature: a webhook delivery's signature does not verify.
	ErrInvalidSignature = errors.New("payment provider: invalid webhook signature")
	// ErrInvalidPayload: a webhook delivery cannot be decoded.
	ErrInvalidPayload = errors.New("payment provider: invalid webhook payload")
	// ErrNoSetupMethods: none of a customer's allowed method types can be
	// saved through a setup page (lago's no_payment_methods_to_setup_available).
	ErrNoSetupMethods = errors.New("payment provider: no payment methods to set up")
	// ErrNoPaymentMethod: the provider customer has no payment method to
	// charge (lago's customer_has_no_payment_method).
	ErrNoPaymentMethod = errors.New("payment provider: customer has no payment method")
	// ErrMissingCountry: a bank-transfer charge needs a country and neither
	// the customer nor the billing entity has one.
	ErrMissingCountry = errors.New("payment provider: missing country")
	// ErrUnknownStatus: the provider reported a status the adapter cannot
	// map (lago fails validation, credit_notes/refunds/stripe_service_spec.rb:360).
	// It is neither rejected nor retryable: the caller persists nothing
	// and keeps the record's status until a later status update.
	ErrUnknownStatus = errors.New("payment provider: unknown status")
)

// ErrUnknownPayable: a webhook's metadata names a payable type Tanvo does
// not know, so the event cannot be applied to any record (lago raises
// NameError and updates nothing, webhooks/base_service.rb
// payment_service_klass). It matches [ErrRejected]: redelivery cannot
// help, so the handler acknowledges the event with a warning.
var ErrUnknownPayable = fmt.Errorf("payment provider: unknown payable type: %w", ErrRejected)

// Classes of [ProviderError], matched with errors.Is.
var (
	// ErrRetryable matches errors worth retrying later with the same
	// idempotency key: rate limits and provider unavailability.
	ErrRetryable = errors.New("payment provider: retryable")
	// ErrRejected matches definitive refusals: the operation will not
	// succeed as requested, so the payment or refund fails. Charges also
	// fail on [ErrAuthentication] (lago payments/create_service.rb:35
	// rescues AuthenticationError and PermissionError with CardError),
	// while a refund keeps retrying until the credentials are fixed
	// (credit_notes/refunds/stripe_service.rb rescues only
	// InvalidRequestError).
	ErrRejected = errors.New("payment provider: rejected")
	// ErrAuthentication matches invalid or unauthorized credentials of the
	// connection.
	ErrAuthentication = errors.New("payment provider: authentication failed")
)

// ErrorKind classifies a provider failure.
type ErrorKind string

// ErrorKind values.
const (
	// KindRateLimited: the provider throttled the call; retryable.
	KindRateLimited ErrorKind = "rate_limited"
	// KindUnavailable: network failure or provider-side error; retryable.
	KindUnavailable ErrorKind = "unavailable"
	// KindAuthentication: the connection's credentials are invalid or lack
	// a permission.
	KindAuthentication ErrorKind = "authentication"
	// KindCardDeclined: the payment method was declined; rejected.
	KindCardDeclined ErrorKind = "card_declined"
	// KindAuthenticationRequired: the customer must authenticate the
	// charge (3-D Secure); rejected unless [ProviderError.RetryOnSession].
	KindAuthenticationRequired ErrorKind = "authentication_required"
	// KindAmountTooSmall: the amount is below the provider's minimum; the
	// payable stays pending.
	KindAmountTooSmall ErrorKind = "amount_too_small"
	// KindIdempotency: the idempotency key was reused with other
	// parameters, or its first request is still running; the payable stays
	// pending.
	KindIdempotency ErrorKind = "idempotency"
	// KindNotFound: the provider object does not exist; rejected.
	KindNotFound ErrorKind = "not_found"
	// KindInvalidRequest: the provider refused the request's content;
	// rejected.
	KindInvalidRequest ErrorKind = "invalid_request"
	// KindPermanent: any other provider failure that a retry will not fix;
	// rejected.
	KindPermanent ErrorKind = "permanent"
)

// ProviderError is a failed provider call, classified. Adapters return it
// (wrapped or not) for every error the provider reports.
type ProviderError struct {
	// Provider is the vendor ("stripe").
	Provider string
	// Kind classifies the failure.
	Kind ErrorKind
	// Code is the provider's error code ("card_declined",
	// "charge_not_refundable").
	Code string
	// DeclineCode is the issuer's decline code, for declined cards.
	DeclineCode string
	// Message is the provider's message.
	Message string
	// HTTPStatus is the HTTP status of the response; 0 without one.
	HTTPStatus int
	// RequestID is the provider's request ID.
	RequestID string
	// ProviderPaymentID is the payment the provider created despite the
	// failure (a declined PaymentIntent).
	ProviderPaymentID string
	// RetryAfter is the delay the provider asked for; 0 when none.
	RetryAfter time.Duration
	// RetryOnSession reports an authentication-required failure worth
	// retrying on-session, letting the customer authenticate.
	RetryOnSession bool
}

// Error implements error.
func (e *ProviderError) Error() string {
	var b strings.Builder
	b.WriteString(e.Provider)
	b.WriteString(": ")
	b.WriteString(string(e.Kind))
	if e.Code != "" {
		b.WriteString(" (")
		b.WriteString(e.Code)
		b.WriteString(")")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Retryable reports a failure worth retrying with the same key.
func (e *ProviderError) Retryable() bool {
	return e.Kind == KindRateLimited || e.Kind == KindUnavailable
}

// Rejected reports a definitive refusal.
func (e *ProviderError) Rejected() bool {
	switch e.Kind {
	case KindCardDeclined, KindInvalidRequest, KindNotFound, KindPermanent:
		return true
	case KindAuthenticationRequired:
		return !e.RetryOnSession
	default:
		return false
	}
}

// Is matches [ErrRetryable], [ErrRejected] and [ErrAuthentication] by
// class.
func (e *ProviderError) Is(target error) bool {
	switch target {
	case ErrRetryable:
		return e.Retryable()
	case ErrRejected:
		return e.Rejected()
	case ErrAuthentication:
		return e.Kind == KindAuthentication
	default:
		return false
	}
}

// AsProviderError returns the [ProviderError] in err's chain.
func AsProviderError(err error) (*ProviderError, bool) {
	return errors.AsType[*ProviderError](err)
}

// ErrorKindOf returns the kind of the [ProviderError] in err's chain, or
// "" when there is none.
func ErrorKindOf(err error) ErrorKind {
	if pe, ok := AsProviderError(err); ok {
		return pe.Kind
	}
	return ""
}
