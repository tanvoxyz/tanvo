package gocardless

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	gc "github.com/gocardless/gocardless-pro-go/v6"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// GoCardless error types and reasons the adapter acts on
// (https://developer.gocardless.com/api-reference#api-usage-errors).
const (
	typeValidationFailed = "validation_failed"
	typeInvalidAPIUsage  = "invalid_api_usage"
	typeInvalidState     = "invalid_state"

	// reasonIdempotentConflict: a resource was already created with the
	// request's idempotency key; the error links it.
	reasonIdempotentConflict = "idempotent_creation_conflict"
	// reasonCancellationFailed: the payment's state no longer allows a
	// cancellation.
	reasonCancellationFailed = "cancellation_failed"

	// codeNoMandate is the adapter's own code of a charge of a customer
	// with no usable mandate (lago MandateNotFoundError::ERROR_CODE).
	codeNoMandate = "no_mandate_error"
)

// sdkError is a failed client call with the HTTP status of its answer; 0
// when none came.
type sdkError struct {
	status int
	retry  time.Duration
	err    error
}

func (e *sdkError) Error() string { return e.err.Error() }
func (e *sdkError) Unwrap() error { return e.err }

// apiError returns the GoCardless error in err's chain.
func apiError(err error) (*gc.APIError, bool) { return errors.AsType[*gc.APIError](err) }

// reason returns the reason of the error's first entry, else its type.
func reason(ae *gc.APIError) string {
	for _, e := range ae.Errors {
		if e.Reason != "" {
			return e.Reason
		}
	}
	return ae.Type
}

// conflictingResource returns the resource an idempotent creation
// conflict links; "" for any other error.
func conflictingResource(err error) string {
	ae, ok := apiError(err)
	if !ok {
		return ""
	}
	for _, e := range ae.Errors {
		if e.Reason == reasonIdempotentConflict && e.Links.ConflictingResourceID != "" {
			return e.Links.ConflictingResourceID
		}
	}
	return ""
}

// hasReason reports whether err is a GoCardless error with an entry of
// reason r.
func hasReason(err error, r string) bool {
	ae, ok := apiError(err)
	if !ok {
		return false
	}
	for _, e := range ae.Errors {
		if e.Reason == r {
			return true
		}
	}
	return false
}

// classify turns a client or transport error into a
// [payment.ProviderError]; context cancellation, errors already
// classified and the adapter's own errors pass through.
//
// gocardless-pro for Ruby raises by status and type (error.rb, api_service.rb):
// 401 AuthenticationError, 403 PermissionError, 429 RateLimitError,
// validation_failed ValidationError, invalid_state InvalidStateError,
// invalid_api_usage InvalidApiUsageError, 5xx GoCardlessError. Lago fails
// a charge on ValidationError and fails it and re-raises for the job on
// the others (gocardless/payments/create_service.rb:43-46). Here: 401/403
// → authentication; 429 → rate_limited, with the RateLimit-Reset delay;
// 5xx, connection failures and undecodable 2xx answers → unavailable (a
// retry with the same idempotency key is safe); 404 → not_found; an
// idempotent creation conflict → idempotency; validation_failed and
// invalid_api_usage → invalid_request; invalid_state and other answers →
// permanent.
//
// The code is the first error's reason ("mandate_is_inactive"), else the
// error type: lago reports e.code, which the GoCardless API fills with the
// HTTP status.
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
	se, ok := errors.AsType[*sdkError](err)
	if !ok {
		// The adapter's own errors.
		return err
	}
	pe := &payment.ProviderError{Provider: string(Provider), HTTPStatus: se.status, RetryAfter: se.retry, Message: se.err.Error()}
	var typ string
	if ae, ok := apiError(se.err); ok {
		typ = ae.Type
		pe.Code = reason(ae)
		pe.RequestID = ae.RequestID
		if ae.Message != "" {
			pe.Message = ae.Message
		}
		if pe.HTTPStatus == 0 {
			pe.HTTPStatus = ae.Code
		}
	}
	status := pe.HTTPStatus
	switch {
	case status == 0:
		if !isTransport(se.err) {
			// The client failed before sending (an unencodable request).
			pe.Kind = payment.KindPermanent
			break
		}
		pe.Kind = payment.KindUnavailable
	case status < 300:
		// A 2xx answer the client could not decode: the operation may
		// have happened, and a retry with the same key finds it.
		pe.Kind = payment.KindUnavailable
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		pe.Kind = payment.KindAuthentication
	case status == http.StatusTooManyRequests:
		pe.Kind = payment.KindRateLimited
	case status >= 500:
		pe.Kind = payment.KindUnavailable
	case status == http.StatusNotFound:
		pe.Kind = payment.KindNotFound
	case pe.Code == reasonIdempotentConflict:
		pe.Kind = payment.KindIdempotency
	case typ == typeValidationFailed || typ == typeInvalidAPIUsage:
		pe.Kind = payment.KindInvalidRequest
	default:
		pe.Kind = payment.KindPermanent
	}
	return pe
}

// isTransport reports an error of the HTTP exchange: a failed request or
// an interrupted answer.
func isTransport(err error) bool {
	if _, ok := errors.AsType[*url.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}
