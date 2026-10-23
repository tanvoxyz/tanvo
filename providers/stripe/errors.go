package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	stripe "github.com/stripe/stripe-go/v84"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Stripe error codes the adapter acts on.
const (
	codeAmountTooSmall          = "amount_too_small"        // lago StripeProvider::AMOUNT_TOO_SMALL_ERROR_CODE
	codeAuthenticationRequired  = "authentication_required" // lago StripeProvider::NEED_3DS_ERROR_CODE
	codeResourceMissing         = "resource_missing"        // object not found
	codePaymentIntentUnexpected = "payment_intent_unexpected_state"
	codeChargeNotRefundable     = "charge_not_refundable" // lago CreditNotes::Refunds::StripeService::INVALID_PAYMENT_METHOD_ERROR
)

// classify turns a stripe-go or transport error into a
// [payment.ProviderError]; context cancellation, errors already
// classified and the adapter's own errors pass through.
//
// The kinds follow the exception classes stripe-ruby derives from the HTTP
// status and lago's handling of each (payments/create_service.rb,
// credit_notes/refunds/stripe_service.rb): 401/403 Authentication and
// Permission errors → authentication; 402 CardError → card_declined;
// 409 IdempotencyError → idempotency; 429 RateLimitError → rate_limited;
// APIConnectionError and 5xx → unavailable (lago re-raises other
// StripeErrors for the job to retry, create_service.rb:56); 400/404 InvalidRequestError →
// invalid_request, or not_found for resource_missing; the codes
// amount_too_small and authentication_required take precedence.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := payment.AsProviderError(err); ok {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	se, ok := errors.AsType[*stripe.Error](err)
	if !ok {
		if isTransport(err) {
			// No API response: a connection failure (stripe-ruby
			// APIConnectionError, retried by lago's jobs).
			return &payment.ProviderError{Provider: string(Provider), Kind: payment.KindUnavailable, Message: err.Error()}
		}
		// The adapter's own errors ([payment.ErrMissingCountry], …).
		return err
	}
	pe := &payment.ProviderError{
		Provider:    string(Provider),
		Code:        string(se.Code),
		DeclineCode: string(se.DeclineCode),
		Message:     se.Msg,
		HTTPStatus:  se.HTTPStatusCode,
		RequestID:   se.RequestID,
	}
	if se.PaymentIntent != nil {
		pe.ProviderPaymentID = se.PaymentIntent.ID
	}
	if se.LastResponse != nil {
		if s, err := strconv.Atoi(se.LastResponse.Header.Get("Retry-After")); err == nil && s > 0 {
			pe.RetryAfter = time.Duration(s) * time.Second
		}
		if pe.RequestID == "" {
			pe.RequestID = se.LastResponse.RequestID
		}
	}
	switch {
	case pe.Code == codeAmountTooSmall:
		pe.Kind = payment.KindAmountTooSmall
	case pe.Code == codeAuthenticationRequired:
		pe.Kind = payment.KindAuthenticationRequired
	case se.HTTPStatusCode == http.StatusUnauthorized || se.HTTPStatusCode == http.StatusForbidden:
		pe.Kind = payment.KindAuthentication
	case se.Type == stripe.ErrorTypeCard || se.HTTPStatusCode == http.StatusPaymentRequired:
		pe.Kind = payment.KindCardDeclined
	case se.Type == stripe.ErrorTypeIdempotency:
		pe.Kind = payment.KindIdempotency
	case se.HTTPStatusCode == http.StatusTooManyRequests:
		pe.Kind = payment.KindRateLimited
	case se.HTTPStatusCode >= 500 || se.Type == stripe.ErrorTypeAPI:
		pe.Kind = payment.KindUnavailable
	case se.HTTPStatusCode == http.StatusNotFound || pe.Code == codeResourceMissing:
		pe.Kind = payment.KindNotFound
	case se.Type == stripe.ErrorTypeInvalidRequest || se.HTTPStatusCode == http.StatusBadRequest:
		pe.Kind = payment.KindInvalidRequest
	case se.HTTPStatusCode == http.StatusConflict:
		// A 409 conflict on a locked object; stripe-go does not retry
		// it, the caller's job does.
		pe.Kind = payment.KindUnavailable
	default:
		pe.Kind = payment.KindPermanent
	}
	return pe
}

// isTransport reports an error of the HTTP exchange: a failed request
// or an unreadable answer.
func isTransport(err error) bool {
	if _, ok := errors.AsType[*url.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// stripe-go reports an answer it cannot decode as a plain error.
	_, isJSON := errors.AsType[*json.SyntaxError](err)
	return isJSON
}

// providerError returns the [payment.ProviderError] in err's chain.
func providerError(err error) *payment.ProviderError {
	pe, _ := payment.AsProviderError(err)
	return pe
}

// hasCode reports a provider error with the given Stripe code.
func hasCode(err error, code string) bool {
	pe := providerError(classify(err))
	return pe != nil && pe.Code == code
}

// isKind reports a provider error of kind k.
func isKind(err error, k payment.ErrorKind) bool {
	return payment.ErrorKindOf(classify(err)) == k
}
