package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v84"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func TestClassify(t *testing.T) {
	apiErr := func(status int, typ stripe.ErrorType, code stripe.ErrorCode) *stripe.Error {
		return &stripe.Error{HTTPStatusCode: status, Type: typ, Code: code, Msg: "msg", RequestID: "req_1"}
	}
	tests := []struct {
		source   string
		err      error
		want     payment.ErrorKind
		class    error
		passThru bool
	}{
		{"app/services/payment_providers/stripe/payments/create_service.rb:40", apiErr(400, stripe.ErrorTypeInvalidRequest, "amount_too_small"), payment.KindAmountTooSmall, nil, false},
		{"app/services/payment_providers/stripe/payments/create_service.rb:43", apiErr(402, stripe.ErrorTypeCard, "authentication_required"), payment.KindAuthenticationRequired, payment.ErrRejected, false},
		{"spec/services/payment_provider_customers/stripe_service_spec.rb:140", apiErr(401, stripe.ErrorTypeInvalidRequest, ""), payment.KindAuthentication, payment.ErrAuthentication, false},
		{"spec/services/payment_provider_customers/stripe_service_spec.rb:243", apiErr(403, stripe.ErrorTypeInvalidRequest, ""), payment.KindAuthentication, payment.ErrAuthentication, false},
		{"spec/services/payment_providers/stripe/payments/create_service_spec.rb:283", apiErr(402, stripe.ErrorTypeCard, "card_declined"), payment.KindCardDeclined, payment.ErrRejected, false},
		{"app/services/payment_providers/stripe/payments/create_service.rb:48", apiErr(400, stripe.ErrorTypeIdempotency, ""), payment.KindIdempotency, nil, false},
		{"app/services/payment_providers/stripe/payments/create_service.rb:50", apiErr(429, stripe.ErrorTypeInvalidRequest, "rate_limit"), payment.KindRateLimited, payment.ErrRetryable, false},
		{"app/services/payment_providers/stripe/payments/create_service.rb:56", apiErr(500, stripe.ErrorTypeAPI, ""), payment.KindUnavailable, payment.ErrRetryable, false},
		{"spec/services/payment_providers/stripe/payments/retrieve_service_spec.rb:53", apiErr(404, stripe.ErrorTypeInvalidRequest, "resource_missing"), payment.KindNotFound, payment.ErrRejected, false},
		{"spec/services/credit_notes/refunds/stripe_service_spec.rb:151", apiErr(400, stripe.ErrorTypeInvalidRequest, "parameter_invalid"), payment.KindInvalidRequest, payment.ErrRejected, false},
		{"stripe-go 409 conflict", apiErr(409, "", ""), payment.KindUnavailable, payment.ErrRetryable, false},
		{"app/services/payment_providers/stripe/payments/create_service.rb:53", &url.Error{Op: "Post", URL: "https://api.stripe.com", Err: errors.New("connection refused")}, payment.KindUnavailable, payment.ErrRetryable, false},
		{"context", fmt.Errorf("wrapped: %w", context.Canceled), "", nil, true},
		{"adapter error", payment.ErrMissingCountry, "", nil, true},
	}
	for _, tt := range tests {
		got := classify(tt.err)
		if tt.passThru {
			if got != tt.err { //nolint:errorlint // identity: the error is returned unchanged
				t.Errorf("%s: classify: got = %v, want = %v unchanged", tt.source, got, tt.err)
			}
			continue
		}
		if k := payment.ErrorKindOf(got); k != tt.want {
			t.Errorf("%s: kind: got = %q, want = %q", tt.source, k, tt.want)
		}
		if tt.class != nil && !errors.Is(got, tt.class) {
			t.Errorf("%s: class: got = %v, want = %v", tt.source, got, tt.class)
		}
		if again := classify(got); again != got { //nolint:errorlint // identity: the error is returned unchanged
			t.Errorf("%s: classify twice: got = %v, want = unchanged", tt.source, again)
		}
	}
}

func TestClassifyDetails(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routeIntents: {{
		status:  http.StatusTooManyRequests,
		body:    `{"error":{"type":"invalid_request_error","code":"rate_limit","message":"slow down"}}`,
		header:  map[string]string{"Retry-After": "3"},
		fixture: "",
	}}})
	_, err := rp.gateway(WithMaxNetworkRetries(0)).Authorize(context.Background(), connection(`{}`), payment.AuthorizeParams{
		ProviderCustomerID: "cus_123", ProviderMethodID: "pm_1", Amount: 1, Currency: usd,
	})
	pe, ok := payment.AsProviderError(err)
	if !ok {
		t.Fatalf("error: got = %v, want = a provider error", err)
	}
	if pe.RetryAfter != 3*time.Second || pe.RequestID != "req_test" || pe.HTTPStatus != 429 || pe.Code != "rate_limit" || pe.Message != "slow down" || pe.Provider != "stripe" {
		t.Errorf("provider error: got = %+v, want = 429 rate_limit after 3s of req_test", pe)
	}
}

func TestRetriableAuthenticationFailure(t *testing.T) {
	// The on-session retry rule of lago StripeProvider#retriable_authentication_failure?.
	const src = "spec/models/payment_providers/stripe_provider_spec.rb:"
	tests := []struct {
		source   string
		settings string
		reply    reply
		gated    bool
		want     bool
	}{
		{src + "61", `{"supports_3ds":true}`, failFixture(http.StatusPaymentRequired, "payment_intent_card_declined_response.json"), false, false},
		{src + "69", `{"supports_3ds":true}`, failFixture(http.StatusBadRequest, "payment_intent_authentication_required_response.json"), false, true},
		{src + "75", `{}`, failFixture(http.StatusBadRequest, "payment_intent_authentication_required_response.json"), false, false},
		{src + "82", `{}`, failFixture(http.StatusBadRequest, "payment_intent_authentication_required_response.json"), true, true},
	}
	for _, tt := range tests {
		rp := newReplay(t, map[string][]reply{routeIntents: {tt.reply}, routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method"}`)}})
		p := invoicePayment()
		p.ActivatesSubscription = tt.gated
		_, err := rp.gateway().CreatePayment(context.Background(), connection(tt.settings), p)
		pe, ok := payment.AsProviderError(err)
		if !ok {
			t.Fatalf("%s: error: got = %v, want = a provider error", tt.source, err)
		}
		if pe.RetryOnSession != tt.want {
			t.Errorf("%s: RetryOnSession: got = %v, want = %v", tt.source, pe.RetryOnSession, tt.want)
		}
		if tt.want && errors.Is(err, payment.ErrRejected) {
			t.Errorf("%s: an on-session retry: got = rejected, want = not rejected", tt.source)
		}
	}
}
