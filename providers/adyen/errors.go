package adyen

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

	"github.com/adyen/adyen-go-api-library/v21/src/common"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Adyen error codes the adapter acts on
// (https://docs.adyen.com/development-resources/error-codes).
const (
	// codeInProgress: a request with the same idempotency key was already
	// processed or is in progress.
	codeInProgress = "704"
	// codeNoPaymentMethod is the adapter's own code of a charge with no
	// stored method to use.
	codeNoPaymentMethod = "no_payment_method"
)

// sdkError is a failed SDK call with the HTTP status of its answer; 0
// when none came.
type sdkError struct {
	status int
	retry  time.Duration
	err    error
}

func (e *sdkError) Error() string { return e.err.Error() }
func (e *sdkError) Unwrap() error { return e.err }

// sdkErr records the answer's status with an SDK error; nil stays nil.
func sdkErr(res *http.Response, err error) error {
	if err == nil {
		return nil
	}
	e := &sdkError{err: err}
	if res != nil {
		e.status = res.StatusCode
		if s, perr := strconv.Atoi(res.Header.Get("Retry-After")); perr == nil && s > 0 {
			e.retry = time.Duration(s) * time.Second
		}
	}
	return e
}

// classify turns an SDK or transport error into a [payment.ProviderError];
// context cancellation, errors already classified and the adapter's own
// errors pass through.
//
// adyen-ruby (7.3.1, client.rb) raises AuthenticationError on 401 and
// PermissionError on 403 and returns every other answer to lago, which
// fails a charge on any status above 400 (adyen/payments/create_service.rb:29)
// and re-raises connection failures for the job (create_service.rb:46).
// Tanvo keeps the failure for definitive refusals and retries what Adyen
// documents as transient, with the same idempotency key: 401/403 →
// authentication; 429 → rate_limited; 5xx, connection failures and
// unreadable answers → unavailable; error code 704 (same idempotency key
// in progress) → idempotency; 404 → not_found; other 4xx →
// invalid_request.
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
		if isTransport(err) {
			return &payment.ProviderError{Provider: string(Provider), Kind: payment.KindUnavailable, Message: err.Error()}
		}
		// The adapter's own errors.
		return err
	}
	pe := &payment.ProviderError{Provider: string(Provider), HTTPStatus: se.status, RetryAfter: se.retry, Message: se.err.Error()}
	var errorCode string
	if ae, ok := errors.AsType[common.APIError](se.err); ok {
		// The code is Adyen's errorType ("validation", "security"), as
		// lago reports it (lib/lago/adyen/error_handlable.rb:9,
		// adyen/payments/create_service.rb:30-31); errorCode is
		// Adyen's finer code.
		pe.Code = ae.Type
		errorCode = ae.Code
		if ae.Message != "" {
			pe.Message = ae.Message
		}
		var body struct {
			PspReference string `json:"pspReference"`
		}
		if json.Unmarshal(ae.RawBody, &body) == nil {
			pe.RequestID = body.PspReference
		}
	}
	switch {
	case se.status == 0:
		if !isTransport(se.err) {
			// The SDK failed before sending (an unencodable request).
			pe.Kind = payment.KindPermanent
			break
		}
		pe.Kind = payment.KindUnavailable
	case se.status < 300:
		// A 2xx answer the SDK could not decode: the operation may have
		// happened, and a retry with the same key is answered from
		// Adyen's idempotency cache.
		pe.Kind = payment.KindUnavailable
	case se.status == http.StatusUnauthorized || se.status == http.StatusForbidden:
		pe.Kind = payment.KindAuthentication
	case se.status == http.StatusTooManyRequests:
		pe.Kind = payment.KindRateLimited
	case se.status >= 500:
		pe.Kind = payment.KindUnavailable
	case errorCode == codeInProgress:
		pe.Kind = payment.KindIdempotency
	case se.status == http.StatusNotFound:
		pe.Kind = payment.KindNotFound
	case se.status >= 400:
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

// statusOf returns the HTTP status of a classified error; 0 without one.
func statusOf(err error) int {
	if pe, ok := payment.AsProviderError(classify(err)); ok {
		return pe.HTTPStatus
	}
	return 0
}
