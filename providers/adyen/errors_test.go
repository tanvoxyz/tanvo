package adyen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/adyen/adyen-go-api-library/v21/src/common"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func TestClassify(t *testing.T) {
	resp := func(status int, retry string) *http.Response {
		h := http.Header{}
		if retry != "" {
			h.Set("Retry-After", retry)
		}
		return &http.Response{StatusCode: status, Header: h}
	}
	api := func(status int, errorType, code string) error {
		return common.APIError{Status: float64(status), Type: errorType, Code: code, Message: "msg", RawBody: []byte(`{"pspReference":"PSP_ERR"}`)}
	}
	urlErr := &url.Error{Op: "Post", URL: "https://checkout-test.adyen.com", Err: errors.New("connection refused")}
	tests := []struct {
		name      string
		err       error
		wantKind  payment.ErrorKind
		wantCode  string
		wantRetry time.Duration
	}{
		{"unauthorized", sdkErr(resp(401, ""), api(401, "security", "000")), payment.KindAuthentication, "security", 0},
		{"forbidden", sdkErr(resp(403, ""), api(403, "security", "010")), payment.KindAuthentication, "security", 0},
		{"rate limited", sdkErr(resp(429, "7"), api(429, "throttling", "")), payment.KindRateLimited, "throttling", 7 * time.Second},
		{"server error", sdkErr(resp(500, ""), api(500, "internal", "901")), payment.KindUnavailable, "internal", 0},
		{"in progress", sdkErr(resp(422, ""), api(422, "validation", codeInProgress)), payment.KindIdempotency, "validation", 0},
		{"not found", sdkErr(resp(404, ""), api(404, "validation", "000")), payment.KindNotFound, "validation", 0},
		{"validation", sdkErr(resp(422, ""), api(422, "validation", "14_030")), payment.KindInvalidRequest, "validation", 0},
		{"undecodable 2xx", sdkErr(resp(200, ""), errors.New("json: cannot unmarshal")), payment.KindUnavailable, "", 0},
		{"no answer, transport", sdkErr(nil, urlErr), payment.KindUnavailable, "", 0},
		{"no answer, encoding", sdkErr(nil, errors.New("json: unsupported value")), payment.KindPermanent, "", 0},
		{"bare transport error", urlErr, payment.KindUnavailable, "", 0},
		{"interrupted answer", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), payment.KindUnavailable, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pe, ok := payment.AsProviderError(classify(tt.err))
			if !ok {
				t.Fatalf("classify: got = %v, want = a provider error", classify(tt.err))
			}
			if pe.Kind != tt.wantKind || pe.Code != tt.wantCode || pe.RetryAfter != tt.wantRetry || pe.Provider != string(Provider) {
				t.Errorf("got = %s %q %v %s, want = %s %q %v adyen", pe.Kind, pe.Code, pe.RetryAfter, pe.Provider, tt.wantKind, tt.wantCode, tt.wantRetry)
			}
			if tt.wantCode != "" && (pe.RequestID != "PSP_ERR" || pe.Message != "msg") {
				t.Errorf("request ID and message: got = %q %q, want = PSP_ERR msg", pe.RequestID, pe.Message)
			}
		})
	}
}

func TestClassifyPassThrough(t *testing.T) {
	if classify(nil) != nil {
		t.Errorf("classify(nil): got = non-nil, want = nil")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, payment.ErrNoPaymentMethod} {
		if got := classify(err); !errors.Is(got, err) {
			t.Errorf("classify(%v): got = %v, want = unchanged", err, got)
		}
	}
	pe := &payment.ProviderError{Provider: "adyen", Kind: payment.KindPermanent}
	if got := classify(pe); !errors.Is(got, pe) {
		t.Errorf("classified error: got = %v, want = unchanged", got)
	}
	if got := statusOf(sdkErr(&http.Response{StatusCode: http.StatusUnprocessableEntity, Header: http.Header{}}, errors.New("x"))); got != 422 {
		t.Errorf("statusOf: got = %d, want = 422", got)
	}
	if got := statusOf(errors.New("x")); got != 0 {
		t.Errorf("statusOf(plain): got = %d, want = 0", got)
	}
}
