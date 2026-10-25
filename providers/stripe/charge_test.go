package stripe

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

const (
	routeIntents      = "POST /v1/payment_intents"
	routeCustomer     = "GET /v1/customers/cus_123"
	routeMethods      = "GET /v1/customers/cus_123/payment_methods"
	routeMethod123456 = "GET /v1/customers/cus_123/payment_methods/pm_123456"
)

// invoicePayment is a charge of a 200 EUR-cent invoice by a Canadian
// customer with a saved card (lago create_service_spec.rb:8-53).
func invoicePayment() payment.PaymentParams {
	return payment.PaymentParams{
		PaymentID:          testPaymentID,
		Payable:            payment.InvoicePayable(testInvoiceID),
		InvoiceType:        "subscription",
		IssuingDate:        core.NewDate(2025, 11, 19),
		CustomerID:         testCustomerID,
		BillingEntityID:    testEntityID,
		ProviderCustomerID: "cus_123",
		ProviderMethodID:   "pm_123456",
		MethodTypes:        []string{MethodCard},
		Amount:             200,
		Currency:           eur,
		Description:        "Acme - Invoice ACM-001",
		CustomerCountry:    "CA",
	}
}

func intentJSON(status string) string {
	return `{"id":"pi_123456","object":"payment_intent","status":"` + status + `","amount":200,"currency":"eur"}`
}

func TestStatusOf(t *testing.T) {
	// source: app/models/payment_providers/stripe_provider.rb PROCESSING_STATUSES, SUCCESS_STATUSES, FAILED_STATUSES
	tests := []struct {
		status string
		want   payment.State
	}{
		{"processing", payment.StateProcessing},
		{"requires_capture", payment.StateProcessing},
		{"requires_action", payment.StateProcessing},
		{"requires_confirmation", payment.StateProcessing},
		{"succeeded", payment.StateSucceeded},
		{"canceled", payment.StateFailed},
		{"requires_payment_method", payment.StateFailed},
		{"unknown", payment.StatePending},
	}
	for _, tt := range tests {
		if got := StatusOf(tt.status); got != tt.want {
			t.Errorf("StatusOf(%q): got = %q, want = %q", tt.status, got, tt.want)
		}
	}
}

func TestCreatePayment(t *testing.T) {
	declined := failFixture(http.StatusPaymentRequired, "payment_intent_card_declined_response.json")
	authRequired := failFixture(http.StatusBadRequest, "payment_intent_authentication_required_response.json")
	tests := []struct {
		source   string
		name     string
		settings string
		params   func(p *payment.PaymentParams)
		routes   map[string][]reply
		// form is checked on the PaymentIntent creation; "" wants absent.
		form       map[string]string
		absent     []string
		check      func(t *testing.T, res payment.PaymentResult)
		wantKind   payment.ErrorKind
		wantErr    error
		wantRetry  bool
		noIntent   bool
		wantStale  string
		wantPIInEr string
	}{
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:94",
			name:   "creates a succeeded payment",
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {ok(intentJSON("succeeded"))},
			},
			check: func(t *testing.T, res payment.PaymentResult) {
				if res.ProviderPaymentID != "pi_123456" || res.Status != payment.StateSucceeded || res.Amount != 200 || res.Currency != eur {
					t.Errorf("result: got = %+v, want = pi_123456 succeeded 200 EUR", res)
				}
			},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:660",
			name:   "payment intent payload",
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {ok(intentJSON("succeeded"))},
			},
			form: map[string]string{
				"amount":                            "200",
				"currency":                          "eur",
				"customer":                          "cus_123",
				"payment_method":                    "pm_123456",
				"payment_method_types[0]":           "card",
				"confirm":                           "true",
				"off_session":                       "true",
				"return_url":                        DefaultSuccessRedirectURL,
				"error_on_requires_action":          "true",
				"description":                       "Acme - Invoice ACM-001",
				"metadata[tanvo_customer_id]":       testCustomerID.String(),
				"metadata[tanvo_invoice_id]":        testInvoiceID.String(),
				"metadata[tanvo_payment_id]":        testPaymentID.String(),
				"metadata[invoice_issuing_date]":    "2025-11-19",
				"metadata[invoice_type]":            "subscription",
				"metadata[tanvo_payable_id]":        testInvoiceID.String(),
				"metadata[tanvo_payable_type]":      "invoice",
				"metadata[tanvo_organization_id]":   testOrgID.String(),
				"metadata[tanvo_billing_entity_id]": testEntityID.String(),
			},
		},
		{
			source:   "spec/services/payment_providers/stripe/payments/create_service_spec.rb:114",
			name:     "no consent collection on payment intents",
			settings: `{"require_terms_of_service_consent":true}`,
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {ok(intentJSON("succeeded"))},
			},
			absent: []string{"consent_collection"},
		},
		{
			source:   "spec/services/payment_providers/stripe/payments/create_service_spec.rb:669",
			name:     "Indian customers are charged on-session",
			params:   func(p *payment.PaymentParams) { p.CustomerCountry = "IN" },
			settings: `{"success_redirect_url":"https://example.com/done"}`,
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {ok(intentJSON("succeeded"))},
			},
			form: map[string]string{"off_session": "false", "error_on_requires_action": "false", "return_url": "https://example.com/done"},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:156",
			name:   "without a method the first listed one is charged",
			params: func(p *payment.PaymentParams) { p.ProviderMethodID = "" },
			routes: map[string][]reply{
				routeMethods: {ok(`{"object":"list","has_more":false,"data":[{"id":"pm_123456","object":"payment_method","type":"card","created":1749040507}]}`)},
				routeIntents: {ok(intentJSON("succeeded"))},
			},
			form: map[string]string{"payment_method": "pm_123456"},
		},
		{
			source: "spec/services/payment_provider_customers/stripe/check_payment_method_service_spec.rb:53",
			name:   "a method Stripe lost is reported stale and replaced",
			routes: map[string][]reply{
				routeMethod123456: {fail(http.StatusNotFound, "invalid_request_error", "resource_missing", "No such PaymentMethod")},
				routeMethods:      {fixture("customer_list_payment_methods_response.json")},
				routeIntents:      {ok(intentJSON("succeeded"))},
			},
			form:      map[string]string{"payment_method": "pm_1RWGX9Q8iJWBZFaMnSMSDSi0"},
			wantStale: "pm_123456",
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:199",
			name:   "shared payment token when no method is attached",
			params: func(p *payment.PaymentParams) { p.ProviderMethodID = "" },
			routes: map[string][]reply{
				routeMethods:  {fixture("customer_list_payment_methods_empty_response.json")},
				routeCustomer: {ok(`{"id":"cus_123","object":"customer","default_source":null,"invoice_settings":{"default_payment_method":null,"default_shared_payment_token":"spt_test_123"}}`)},
				routeIntents:  {ok(intentJSON("succeeded"))},
			},
			form:   map[string]string{"payment_method_data[shared_payment_granted_token]": "spt_test_123", "error_on_requires_action": "true"},
			absent: []string{"payment_method", "return_url", "off_session"},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:242",
			name:   "an attached method wins over the shared payment token",
			params: func(p *payment.PaymentParams) { p.ProviderMethodID = "" },
			routes: map[string][]reply{
				routeMethods: {ok(`{"object":"list","has_more":false,"data":[{"id":"pm_existing","object":"payment_method","type":"card","created":1}]}`)},
				routeIntents: {ok(intentJSON("succeeded"))},
			},
			form:   map[string]string{"payment_method": "pm_existing"},
			absent: []string{"payment_method_data"},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:283",
			name:   "card declined",
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {declined},
			},
			wantKind:   payment.KindCardDeclined,
			wantErr:    payment.ErrRejected,
			wantPIInEr: "pi_3RWJu0Q8iJWBZFaM2S1rzYRV",
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:317",
			name:   "a server error is retried",
			params: func(p *payment.PaymentParams) {},
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {fail(http.StatusInternalServerError, "api_error", "", "error")},
			},
			wantKind: payment.KindUnavailable,
			wantErr:  payment.ErrRetryable,
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:357",
			name:   "amount too small",
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {fail(http.StatusBadRequest, "invalid_request_error", "amount_too_small", "amount_too_small")},
			},
			wantKind: payment.KindAmountTooSmall,
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:566",
			name:   "amount too large for boleto",
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {fail(http.StatusBadRequest, "invalid_request_error", "amount_too_large", "Amount must be no more than R$ 49,999.99 brl")},
			},
			wantKind: payment.KindInvalidRequest,
			wantErr:  payment.ErrRejected,
		},
		{
			source:   "spec/services/payment_providers/stripe/payments/create_service_spec.rb:401",
			name:     "authentication required with 3DS support is retried on session",
			settings: `{"supports_3ds":true}`,
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {authRequired},
			},
			form:       map[string]string{"confirm": "true", "off_session": "true", "error_on_requires_action": "true"},
			wantKind:   payment.KindAuthenticationRequired,
			wantRetry:  true,
			wantPIInEr: "pi_3SUpk9Q8iJWBZFaM20I3flZT",
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:432",
			name:   "authentication required without 3DS support fails",
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {authRequired},
			},
			wantKind:   payment.KindAuthenticationRequired,
			wantErr:    payment.ErrRejected,
			wantPIInEr: "pi_3SUpk9Q8iJWBZFaM20I3flZT",
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:518",
			name:   "authentication required on a gated subscription is retried on session",
			params: func(p *payment.PaymentParams) { p.ActivatesSubscription = true },
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {authRequired},
			},
			wantKind:  payment.KindAuthenticationRequired,
			wantRetry: true,
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:464",
			name:   "on-session retry reports the next action",
			params: func(p *payment.PaymentParams) { p.OnSession = true },
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {fixture("payment_intent_requires_action_response.json")},
			},
			form:   map[string]string{"confirm": "true"},
			absent: []string{"off_session", "error_on_requires_action"},
			check: func(t *testing.T, res payment.PaymentResult) {
				if res.ProviderPaymentID != "pi_3SUpkBQ8iJWBZFaM0SuylvJC" || res.Status != payment.StateProcessing || !res.RequiresAction {
					t.Errorf("result: got = %+v, want = processing requiring action", res)
				}
				if want := "https://hooks.stripe.com/3d_secure_2/hosted?"; len(res.NextActionURL) < len(want) || res.NextActionURL[:len(want)] != want {
					t.Errorf("next action URL: got = %q, want = prefix %q", res.NextActionURL, want)
				}
				if len(res.NextAction) == 0 {
					t.Errorf("next action: got = empty, want = the redirect_to_url object")
				}
			},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:584",
			name:   "processing",
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {ok(intentJSON("processing"))},
			},
			check: func(t *testing.T, res payment.PaymentResult) {
				if res.Status != payment.StateProcessing || res.RequiresAction {
					t.Errorf("result: got = %+v, want = processing", res)
				}
			},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:621",
			name:   "Indian customer requires action",
			params: func(p *payment.PaymentParams) { p.CustomerCountry = "IN" },
			routes: map[string][]reply{
				routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
				routeIntents:      {ok(`{"id":"pi_123456","object":"payment_intent","status":"requires_action","amount":200,"currency":"eur","next_action":{"redirect_to_url":{"url":"https://foo.bar"}}}`)},
			},
			check: func(t *testing.T, res payment.PaymentResult) {
				if !res.RequiresAction || res.NextActionURL != "https://foo.bar" {
					t.Errorf("result: got = %+v, want = requires action at https://foo.bar", res)
				}
			},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:695",
			name:   "customer balance in EUR",
			params: func(p *payment.PaymentParams) {
				p.MethodTypes = []string{MethodCustomerBalance}
				p.CustomerCountry = "DE"
			},
			routes: map[string][]reply{routeIntents: {ok(intentJSON("requires_action"))}},
			form: map[string]string{
				"payment_method_data[type]":                                                          "customer_balance",
				"payment_method_options[customer_balance][funding_type]":                             "bank_transfer",
				"payment_method_options[customer_balance][bank_transfer][type]":                      "eu_bank_transfer",
				"payment_method_options[customer_balance][bank_transfer][eu_bank_transfer][country]": "DE",
				"off_session":              "false",
				"error_on_requires_action": "true",
			},
			absent: []string{"payment_method"},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:714",
			name:   "customer balance in USD",
			params: func(p *payment.PaymentParams) {
				p.MethodTypes = []string{MethodCustomerBalance}
				p.Currency = usd
			},
			routes: map[string][]reply{routeIntents: {ok(intentJSON("requires_action"))}},
			form:   map[string]string{"payment_method_options[customer_balance][bank_transfer][type]": "us_bank_transfer"},
			absent: []string{"payment_method_options[customer_balance][bank_transfer][eu_bank_transfer]"},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/create_service_spec.rb:729",
			name:   "customer balance in GBP",
			params: func(p *payment.PaymentParams) {
				p.MethodTypes = []string{MethodCustomerBalance}
				p.Currency = gbp
			},
			routes: map[string][]reply{routeIntents: {ok(intentJSON("requires_action"))}},
			form:   map[string]string{"payment_method_options[customer_balance][bank_transfer][type]": "gb_bank_transfer"},
		},
		{
			source: "app/services/payment_providers/stripe/payments/create_service.rb handle_eu_bank_transfer",
			name:   "customer balance in EUR falls back to the billing entity country",
			params: func(p *payment.PaymentParams) {
				p.MethodTypes = []string{MethodCustomerBalance}
				p.CustomerCountry = "US"
				p.BillingEntityCountry = "fr"
			},
			routes: map[string][]reply{routeIntents: {ok(intentJSON("requires_action"))}},
			form:   map[string]string{"payment_method_options[customer_balance][bank_transfer][eu_bank_transfer][country]": "FR"},
		},
		{
			source: "app/services/payment_providers/stripe/payments/create_service.rb handle_eu_bank_transfer",
			name:   "customer balance in EUR without a supported country",
			params: func(p *payment.PaymentParams) {
				p.MethodTypes = []string{MethodCustomerBalance}
				p.CustomerCountry = "US"
			},
			routes:   map[string][]reply{},
			wantErr:  payment.ErrMissingCountry,
			noIntent: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			settings := tt.settings
			if settings == "" {
				settings = `{}`
			}
			rp := newReplay(t, tt.routes)
			p := invoicePayment()
			if tt.params != nil {
				tt.params(&p)
			}
			res, err := rp.gateway().CreatePayment(context.Background(), connection(settings), p)
			switch {
			case tt.wantKind != "":
				if got := payment.ErrorKindOf(err); got != tt.wantKind {
					t.Fatalf("error kind: got = %q (%v), want = %q", got, err, tt.wantKind)
				}
				pe, _ := payment.AsProviderError(err)
				if pe.RetryOnSession != tt.wantRetry {
					t.Errorf("RetryOnSession: got = %v, want = %v", pe.RetryOnSession, tt.wantRetry)
				}
				if tt.wantPIInEr != "" && pe.ProviderPaymentID != tt.wantPIInEr {
					t.Errorf("ProviderPaymentID: got = %q, want = %q", pe.ProviderPaymentID, tt.wantPIInEr)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Errorf("error: got = %v, want = %v", err, tt.wantErr)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("CreatePayment: got = %v, want = nil", err)
			}
			if tt.noIntent {
				if n := len(rp.requests(routeIntents)); n != 0 {
					t.Errorf("intents created: got = %d, want = 0", n)
				}
				return
			}
			req := rp.only(routeIntents)
			if req.IdempotencyKey != p.IdempotencyKey() {
				t.Errorf("Idempotency-Key: got = %q, want = %q", req.IdempotencyKey, p.IdempotencyKey())
			}
			if req.Authorization != "Bearer sk_test_123" {
				t.Errorf("Authorization: got = %q, want = the connection's key", req.Authorization)
			}
			checkForm(t, req.Form, tt.form)
			for _, a := range tt.absent {
				if formHas(req.Form, a) {
					t.Errorf("form %s: got = present, want = absent", a)
				}
			}
			if res.StaleMethodID != tt.wantStale {
				t.Errorf("StaleMethodID: got = %q, want = %q", res.StaleMethodID, tt.wantStale)
			}
			if tt.check != nil {
				tt.check(t, res)
			}
		})
	}
}

func TestCreatePaymentValidates(t *testing.T) {
	rp := newReplay(t, map[string][]reply{})
	p := invoicePayment()
	p.Amount = 0
	if _, err := rp.gateway().CreatePayment(context.Background(), connection(`{}`), p); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("error: got = %v, want = %v", err, core.ErrInvalidValue)
	}
	if rp.count() != 0 {
		t.Errorf("requests: got = %d, want = 0", rp.count())
	}
}

func TestPaymentRequestMetadata(t *testing.T) {
	// source: spec/services/payment_providers/stripe/payments/create_service_spec.rb:401 (payable_type payment_request)
	rp := newReplay(t, map[string][]reply{
		routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method","type":"card"}`)},
		routeIntents:      {ok(intentJSON("succeeded"))},
	})
	p := invoicePayment()
	p.Payable = payment.RequestPayable(testRequestID)
	if _, err := rp.gateway().CreatePayment(context.Background(), connection(`{}`), p); err != nil {
		t.Fatalf("CreatePayment: got = %v, want = nil", err)
	}
	req := rp.only(routeIntents)
	checkForm(t, req.Form, map[string]string{
		"metadata[tanvo_payable_type]": "payment_request",
		"metadata[tanvo_payable_id]":   testRequestID.String(),
		"metadata[tanvo_invoice_id]":   "",
	})
	if want := "payment:payment_request:" + testRequestID.String() + ":" + testPaymentID.String(); req.IdempotencyKey != want {
		t.Errorf("Idempotency-Key: got = %q, want = %q", req.IdempotencyKey, want)
	}
}

func TestGetPayment(t *testing.T) {
	const route = "GET /v1/payment_intents/pi_123"
	tests := []struct {
		source  string
		name    string
		reply   reply
		want    payment.PaymentResult
		wantErr payment.ErrorKind
	}{
		{
			source: "spec/services/payment_providers/stripe/payments/retrieve_service_spec.rb:34",
			name:   "status and expanded method",
			reply: ok(`{"id":"pi_123","object":"payment_intent","status":"succeeded","amount":1000,"currency":"usd",
				"payment_method":{"id":"pm_1","object":"payment_method","type":"card","card":{"display_brand":"visa","last4":"4242","exp_month":12,"exp_year":2030}}}`),
			want: payment.PaymentResult{
				ProviderPaymentID: "pi_123", ProviderStatus: "succeeded", Status: payment.StateSucceeded,
				ProviderMethodID: "pm_1", Amount: 1000, Currency: usd,
				MethodDetails: payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2030},
			},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/retrieve_service_spec.rb:42",
			name:   "status without a method",
			reply:  ok(`{"id":"pi_123","object":"payment_intent","status":"requires_payment_method","amount":1000,"currency":"usd","payment_method":null}`),
			want: payment.PaymentResult{
				ProviderPaymentID: "pi_123", ProviderStatus: "requires_payment_method", Status: payment.StateFailed, Amount: 1000, Currency: usd,
			},
		},
		{
			source:  "spec/services/payment_providers/stripe/payments/retrieve_service_spec.rb:53",
			name:    "errors are the caller's to interpret",
			reply:   fail(http.StatusNotFound, "invalid_request_error", "resource_missing", "No such payment_intent"),
			wantErr: payment.KindNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, map[string][]reply{route: {tt.reply}})
			got, err := rp.gateway().GetPayment(context.Background(), connection(`{}`), "pi_123")
			if tt.wantErr != "" {
				if k := payment.ErrorKindOf(err); k != tt.wantErr {
					t.Fatalf("error kind: got = %q, want = %q", k, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetPayment: got = %v, want = nil", err)
			}
			// source: spec/services/payment_providers/stripe/payments/retrieve_service_spec.rb:25
			checkForm(t, rp.only(route).Form, map[string]string{"expand[0]": "payment_method"})
			if got.ProviderPaymentID != tt.want.ProviderPaymentID || got.Status != tt.want.Status || got.ProviderStatus != tt.want.ProviderStatus ||
				got.ProviderMethodID != tt.want.ProviderMethodID || got.MethodDetails != tt.want.MethodDetails || got.Amount != tt.want.Amount || got.Currency != tt.want.Currency {
				t.Errorf("result: got = %+v, want = %+v", got, tt.want)
			}
		})
	}
}

func TestCancelPayment(t *testing.T) {
	const route = "POST /v1/payment_intents/pi_123/cancel"
	tests := []struct {
		source     string
		name       string
		reply      reply
		wantErr    payment.ErrorKind
		wantState  payment.State
		wantStatus string
	}{
		{
			source:     "spec/services/payment_providers/stripe/payments/cancel_service_spec.rb:23,43,48,54",
			name:       "cancels with reason abandoned",
			reply:      ok(`{"id":"pi_123","object":"payment_intent","status":"canceled"}`),
			wantState:  payment.StateFailed,
			wantStatus: "canceled",
		},
		{
			source: "spec/services/payment_providers/stripe/payments/cancel_service_spec.rb:74,86",
			name:   "a non-cancelable intent is left as is",
			reply:  fail(http.StatusBadRequest, "invalid_request_error", "payment_intent_unexpected_state", "You cannot cancel this PaymentIntent because it has a status of succeeded."),
		},
		{
			source:  "spec/services/payment_providers/stripe/payments/cancel_service_spec.rb:104",
			name:    "other invalid requests propagate",
			reply:   fail(http.StatusBadRequest, "invalid_request_error", "parameter_invalid_empty", "bad"),
			wantErr: payment.KindInvalidRequest,
		},
		{
			source:  "spec/services/payment_providers/stripe/payments/cancel_service_spec.rb:115",
			name:    "server errors propagate for a retry",
			reply:   fail(http.StatusServiceUnavailable, "api_error", "", "unavailable"),
			wantErr: payment.KindUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, map[string][]reply{route: {tt.reply}})
			res, err := rp.gateway(WithMaxNetworkRetries(0)).CancelPayment(context.Background(), connection(`{}`), "pi_123")
			if got := payment.ErrorKindOf(err); got != tt.wantErr {
				t.Fatalf("error kind: got = %q (%v), want = %q", got, err, tt.wantErr)
			}
			if res.Status != tt.wantState || res.ProviderStatus != tt.wantStatus {
				t.Errorf("result: got = %q (%q), want = %q (%q)", res.Status, res.ProviderStatus, tt.wantState, tt.wantStatus)
			}
			req := rp.only(route)
			// source: spec/services/payment_providers/stripe/payments/cancel_service_spec.rb:29,36
			checkForm(t, req.Form, map[string]string{"cancellation_reason": "abandoned"})
			if req.Authorization != "Bearer sk_test_123" {
				t.Errorf("Authorization: got = %q, want = the connection's key", req.Authorization)
			}
		})
	}
}

func TestUpdatePaymentReference(t *testing.T) {
	const route = "POST /v1/payment_intents/pi_123"
	tests := []struct {
		source    string
		name      string
		paymentID string
		number    string
		reply     reply
		wantCalls int
	}{
		{
			source: "spec/services/payment_providers/stripe/payments/update_reference_service_spec.rb:32,45", name: "sets the invoice number",
			paymentID: "pi_123", number: "ACM-001", reply: ok(`{"id":"pi_123","object":"payment_intent"}`), wantCalls: 1,
		},
		{
			source: "spec/services/payment_providers/stripe/payments/update_reference_service_spec.rb:53,59", name: "no provider payment",
			number: "ACM-001",
		},
		{
			source: "spec/services/payment_providers/stripe/payments/update_reference_service_spec.rb:87", name: "no invoice number",
			paymentID: "pi_123",
		},
		{
			source: "spec/services/payment_providers/stripe/payments/update_reference_service_spec.rb:100,108", name: "Stripe errors are swallowed",
			paymentID: "pi_123", number: "ACM-001", reply: fail(http.StatusBadRequest, "invalid_request_error", "", "boom"), wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, map[string][]reply{route: {tt.reply}})
			if err := rp.gateway().UpdatePaymentReference(context.Background(), connection(`{}`), tt.paymentID, tt.number); err != nil {
				t.Fatalf("UpdatePaymentReference: got = %v, want = nil", err)
			}
			reqs := rp.requests(route)
			if len(reqs) != tt.wantCalls {
				t.Fatalf("calls: got = %d, want = %d", len(reqs), tt.wantCalls)
			}
			if tt.wantCalls > 0 {
				checkForm(t, reqs[0].Form, map[string]string{"description": tt.number, "metadata[tanvo_invoice_number]": tt.number})
			}
		})
	}
}

func TestAuthorize(t *testing.T) {
	params := payment.AuthorizeParams{
		ProviderCustomerID: "cus_123",
		ProviderMethodID:   "pm_from_payment_method",
		Amount:             20,
		Currency:           usd,
		IdempotencyKey:     "authorization:abc",
		Metadata:           map[string]string{"tanvo_subscription_id": "sub"},
	}
	tests := []struct {
		source     string
		name       string
		settings   string
		method     string
		routes     map[string][]reply
		wantMethod string
		wantHeld   int64
		wantErr    error
	}{
		{
			source: "spec/services/payment_providers/stripe/payments/authorize_service_spec.rb:54", name: "creates a manual-capture intent",
			method: "pm_from_payment_method", wantMethod: "pm_from_payment_method", wantHeld: 20,
			routes: map[string][]reply{routeIntents: {ok(`{"id":"pi_auth","object":"payment_intent","status":"requires_capture","amount":20,"amount_capturable":20,"currency":"usd"}`)}},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/authorize_service_spec.rb:90", name: "no consent collection",
			settings: `{"require_terms_of_service_consent":true}`, method: "pm_from_payment_method", wantMethod: "pm_from_payment_method",
			routes: map[string][]reply{routeIntents: {ok(`{"id":"pi_auth","object":"payment_intent","status":"requires_capture"}`)}},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/authorize_service_spec.rb:124", name: "falls back to Stripe's default method",
			wantMethod: "pm_from_stripe",
			routes: map[string][]reply{
				routeCustomer: {ok(`{"id":"cus_123","object":"customer","invoice_settings":{"default_payment_method":"pm_from_stripe"}}`)},
				routeIntents:  {ok(`{"id":"pi_auth","object":"payment_intent","status":"requires_capture"}`)},
			},
		},
		{
			source: "spec/services/payment_providers/stripe/payments/authorize_service_spec.rb:38", name: "fails without any method",
			routes: map[string][]reply{
				routeCustomer: {fixture("customer_retrieve_response.json")},
				routeMethods:  {fixture("customer_list_payment_methods_empty_response.json")},
			},
			wantErr: payment.ErrNoPaymentMethod,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			settings := tt.settings
			if settings == "" {
				settings = `{}`
			}
			rp := newReplay(t, tt.routes)
			p := params
			p.ProviderMethodID = tt.method
			res, err := rp.gateway().Authorize(context.Background(), connection(settings), p)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Authorize: got = %v, want = nil", err)
			}
			if res.ProviderPaymentID != "pi_auth" {
				t.Errorf("ProviderPaymentID: got = %q, want = pi_auth", res.ProviderPaymentID)
			}
			if res.AmountCapturable != tt.wantHeld {
				t.Errorf("AmountCapturable: got = %d, want = %d", res.AmountCapturable, tt.wantHeld)
			}
			req := rp.only(routeIntents)
			// source: spec/services/payment_providers/stripe/payments/authorize_service_spec.rb:61,70
			checkForm(t, req.Form, map[string]string{
				"capture_method":                     "manual",
				"confirm":                            "true",
				"payment_method":                     tt.wantMethod,
				"amount":                             "20",
				"currency":                           "usd",
				"automatic_payment_methods[enabled]": "true",
				"automatic_payment_methods[allow_redirects]": "never",
				"metadata[tanvo_subscription_id]":            "sub",
				"return_url":                                 "",
			})
			for _, a := range []string{"payment_method_options", "consent_collection"} {
				if formHas(req.Form, a) {
					t.Errorf("form %s: got = present, want = absent", a)
				}
			}
			if req.IdempotencyKey != "authorization:abc" {
				t.Errorf("Idempotency-Key: got = %q, want = authorization:abc", req.IdempotencyKey)
			}
		})
	}
}
