package cashfree

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// transportError classifies a failed exchange: context cancellation passes
// through, anything else (connection refused, timeout, interrupted answer)
// is unavailable and retryable.
func transportError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &payment.ProviderError{Provider: string(Provider), Kind: payment.KindUnavailable, Message: err.Error()}
}

// apiError is a Cashfree error answer.
type apiError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
	Type    string `json:"type"`
}

// answerError classifies a Cashfree error answer. Lago turns any non-2xx
// answer into a third-party failure with the HTTP status as error code and
// the body as message (invoices/payments/cashfree_service.rb
// generate_payment_url). Here: 401/403 → authentication; 429 →
// rate_limited with its Retry-After delay; 5xx → unavailable; 404 →
// not_found; 409 → idempotency; other 4xx → invalid_request. The code is
// the HTTP status, as in lago; the message is Cashfree's message followed
// by its error code ("link_amount is invalid (link_post_failed)").
func answerError(res *http.Response, body []byte, now time.Time) error {
	pe := &payment.ProviderError{
		Provider:   string(Provider),
		HTTPStatus: res.StatusCode,
		Code:       strconv.Itoa(res.StatusCode),
		RetryAfter: retryAfter(res.Header.Get("Retry-After"), now),
		Message:    http.StatusText(res.StatusCode),
	}
	var e apiError
	if json.Unmarshal(body, &e) == nil {
		switch m, c := strings.TrimSpace(e.Message), strings.TrimSpace(e.Code); {
		case m != "" && c != "":
			pe.Message = m + " (" + c + ")"
		case m != "":
			pe.Message = m
		case c != "":
			pe.Message = c
		}
	}
	switch s := res.StatusCode; {
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		pe.Kind = payment.KindAuthentication
	case s == http.StatusTooManyRequests:
		pe.Kind = payment.KindRateLimited
	case s >= 500:
		pe.Kind = payment.KindUnavailable
	case s == http.StatusNotFound:
		pe.Kind = payment.KindNotFound
	case s == http.StatusConflict:
		pe.Kind = payment.KindIdempotency
	case s >= 400:
		pe.Kind = payment.KindInvalidRequest
	default:
		// A redirect: the request was not served.
		pe.Kind = payment.KindPermanent
	}
	return pe
}

// retryAfter parses a Retry-After header, in seconds or as an HTTP date.
func retryAfter(h string, now time.Time) time.Duration {
	if h == "" {
		return 0
	}
	if n, err := strconv.Atoi(h); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
