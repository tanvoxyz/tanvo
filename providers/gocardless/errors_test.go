package gocardless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	gc "github.com/gocardless/gocardless-pro-go/v6"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func apiErr(code int, typ, reason string) *gc.APIError {
	ae := &gc.APIError{Message: "boom", Type: typ, Code: code, RequestID: "req_1"}
	if reason != "" {
		ae.Errors = []gc.ValidationError{{Reason: reason}}
	}
	return ae
}

func TestClassify(t *testing.T) {
	transport := &url.Error{Op: "Post", URL: "https://api.gocardless.com", Err: io.EOF}
	tests := []struct {
		name   string
		err    error
		kind   payment.ErrorKind
		code   string
		status int
	}{
		{"unauthorized", &sdkError{status: 401, err: apiErr(401, "invalid_api_usage", "access_token_not_found")}, payment.KindAuthentication, "access_token_not_found", 401},
		{"forbidden", &sdkError{status: 403, err: apiErr(403, "invalid_api_usage", "insufficient_permissions")}, payment.KindAuthentication, "insufficient_permissions", 403},
		{"rate limited", &sdkError{status: 429, err: apiErr(429, "invalid_api_usage", "rate_limit_exceeded")}, payment.KindRateLimited, "rate_limit_exceeded", 429},
		{"server", &sdkError{status: 500, err: apiErr(500, "gocardless", "internal_server_error")}, payment.KindUnavailable, "internal_server_error", 500},
		{"not found", &sdkError{status: 404, err: apiErr(404, "invalid_api_usage", "resource_not_found")}, payment.KindNotFound, "resource_not_found", 404},
		{"conflict", &sdkError{status: 409, err: apiErr(409, "invalid_state", reasonIdempotentConflict)}, payment.KindIdempotency, reasonIdempotentConflict, 409},
		{"validation", &sdkError{status: 422, err: apiErr(422, typeValidationFailed, "mandate_is_inactive")}, payment.KindInvalidRequest, "mandate_is_inactive", 422},
		{"api usage", &sdkError{status: 400, err: apiErr(400, typeInvalidAPIUsage, "")}, payment.KindInvalidRequest, typeInvalidAPIUsage, 400},
		{"invalid state", &sdkError{status: 422, err: apiErr(422, typeInvalidState, reasonCancellationFailed)}, payment.KindPermanent, reasonCancellationFailed, 422},
		{"status from body", &sdkError{err: apiErr(503, "gocardless", "")}, payment.KindUnavailable, "gocardless", 503},
		{"transport", &sdkError{err: transport}, payment.KindUnavailable, "", 0},
		{"unexpected eof", &sdkError{err: fmt.Errorf("read: %w", io.ErrUnexpectedEOF)}, payment.KindUnavailable, "", 0},
		{"unsent", &sdkError{err: errors.New("encode request")}, payment.KindPermanent, "", 0},
		{"undecodable 2xx", &sdkError{status: 201, err: errors.New("decode")}, payment.KindUnavailable, "", 201},
	}
	for _, tt := range tests {
		pe, ok := payment.AsProviderError(classify(tt.err))
		if !ok {
			t.Fatalf("%s: got = %v, want = a provider error", tt.name, classify(tt.err))
		}
		if pe.Kind != tt.kind || pe.Code != tt.code || pe.HTTPStatus != tt.status || pe.Provider != string(Provider) {
			t.Errorf("%s: got = %s %q %d, want = %s %q %d", tt.name, pe.Kind, pe.Code, pe.HTTPStatus, tt.kind, tt.code, tt.status)
		}
	}
	pe, _ := payment.AsProviderError(classify(&sdkError{status: 429, retry: 7 * time.Second, err: apiErr(429, "invalid_api_usage", "rate_limit_exceeded")}))
	if pe.RetryAfter != 7*time.Second || pe.RequestID != "req_1" || pe.Message != "boom" {
		t.Errorf("rate limited: got = %+v, want = retry after 7s, request req_1, message boom", pe)
	}
}

func TestClassifyPassThrough(t *testing.T) {
	own := errors.New("own")
	pe := &payment.ProviderError{Kind: payment.KindPermanent}
	for _, err := range []error{nil, own, pe, context.Canceled, &sdkError{err: context.DeadlineExceeded}} {
		got := classify(err)
		if !errors.Is(got, err) {
			t.Errorf("classify(%v): got = %v, want = the error unchanged", err, got)
		}
	}
}

func TestConflictingResource(t *testing.T) {
	ae := apiErr(409, "invalid_state", reasonIdempotentConflict)
	ae.Errors[0].Links.ConflictingResourceID = "PM123"
	if got := conflictingResource(&sdkError{status: 409, err: ae}); got != "PM123" {
		t.Errorf("conflictingResource: got = %q, want = PM123", got)
	}
	for _, err := range []error{errors.New("x"), apiErr(422, typeValidationFailed, "mandate_is_inactive")} {
		if got := conflictingResource(err); got != "" {
			t.Errorf("conflictingResource(%v): got = %q, want = none", err, got)
		}
	}
	if !hasReason(ae, reasonIdempotentConflict) || hasReason(ae, "other") || hasReason(errors.New("x"), "x") {
		t.Errorf("hasReason: got = wrong answers, want = only the entry's reason")
	}
}

func TestErrorsIs(t *testing.T) {
	err := classify(&sdkError{status: http.StatusUnprocessableEntity, err: apiErr(422, typeValidationFailed, "x")})
	if !errors.Is(err, payment.ErrRejected) || errors.Is(err, payment.ErrRetryable) {
		t.Errorf("validation: got = %v, want = rejected only", err)
	}
	err = classify(&sdkError{status: http.StatusBadGateway, err: apiErr(502, "gocardless", "")})
	if errors.Is(err, payment.ErrRejected) || !errors.Is(err, payment.ErrRetryable) {
		t.Errorf("bad gateway: got = %v, want = retryable only", err)
	}
	err = classify(&sdkError{status: http.StatusUnauthorized, err: apiErr(401, "invalid_api_usage", "")})
	if !errors.Is(err, payment.ErrAuthentication) {
		t.Errorf("unauthorized: got = %v, want = %v", err, payment.ErrAuthentication)
	}
}
