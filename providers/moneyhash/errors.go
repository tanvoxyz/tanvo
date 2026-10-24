package moneyhash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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

// answerError classifies a MoneyHash error answer. Lago raises
// LagoHttpClient::HttpError on any non-2xx answer, fails the payment and
// re-raises for the job (moneyhash/payments/create_service.rb:30-31,
// prepare_failed_result), reporting the HTTP status as the error code and
// the body as the message. Here: 401/403 → authentication; 429 →
// rate_limited with its Retry-After delay; 5xx → unavailable; 404 →
// not_found; 409 → idempotency; other 4xx → invalid_request. The code is
// the HTTP status, as in lago; the message is the answer's status message
// and its field errors ("card_token: This field may not be null.").
func answerError(res *http.Response, body []byte, now time.Time) error {
	pe := &payment.ProviderError{
		Provider:   string(Provider),
		HTTPStatus: res.StatusCode,
		Code:       strconv.Itoa(res.StatusCode),
		RetryAfter: retryAfter(res.Header.Get("Retry-After"), now),
		Message:    http.StatusText(res.StatusCode),
	}
	var env envelope
	if json.Unmarshal(body, &env) == nil {
		if m := message(env); m != "" {
			pe.Message = m
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

// message joins an error answer's status message and field errors.
// MoneyHash lists errors as strings or as objects of field → message or
// messages.
func message(env envelope) string {
	var parts []string
	if m := strings.TrimSpace(env.Status.Message); m != "" {
		parts = append(parts, m)
	}
	var list []json.RawMessage
	if json.Unmarshal(env.Status.Errors, &list) != nil {
		return strings.Join(parts, "; ")
	}
	for _, raw := range list {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			parts = append(parts, s)
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			continue
		}
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s: %s", k, text(fields[k])))
		}
	}
	return strings.Join(parts, "; ")
}

// text renders a field error: a string, a list of strings, or raw JSON.
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var l []string
	if json.Unmarshal(raw, &l) == nil {
		return strings.Join(l, " ")
	}
	return string(raw)
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
