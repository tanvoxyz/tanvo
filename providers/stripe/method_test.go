package stripe

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

const routeSessions = "POST /v1/checkout/sessions"

func TestDefaultPaymentMethod(t *testing.T) {
	const src = "spec/services/payment_provider_customers/stripe/retrieve_latest_payment_method_service_spec.rb:"
	tests := []struct {
		source string
		routes map[string][]reply
		want   string
	}{
		{
			source: src + "15",
			routes: map[string][]reply{routeCustomer: {ok(`{"id":"cus_123","object":"customer","invoice_settings":{"default_payment_method":"pm_default"}}`)}},
			want:   "pm_default",
		},
		{
			source: src + "28",
			routes: map[string][]reply{
				routeCustomer: {fixture("customer_retrieve_response.json")},
				routeMethods: {ok(`{"object":"list","has_more":false,"data":[
					{"id":"pm_old","object":"payment_method","type":"card","created":100},
					{"id":"pm_sepa","object":"payment_method","type":"sepa_debit","created":300},
					{"id":"pm_new","object":"payment_method","type":"card","created":200}]}`)},
			},
			want: "pm_new",
		},
		{
			source: src + "42",
			routes: map[string][]reply{
				routeCustomer: {fixture("customer_retrieve_response.json")},
				routeMethods:  {fixture("customer_list_payment_methods_empty_response.json")},
			},
		},
		{
			source: "app/services/payment_provider_customers/stripe/retrieve_latest_payment_method_service.rb (errors are swallowed)",
			routes: map[string][]reply{
				routeCustomer: {fail(http.StatusNotFound, "invalid_request_error", "resource_missing", "No such customer")},
				routeMethods:  {fail(http.StatusNotFound, "invalid_request_error", "resource_missing", "No such customer")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			rp := newReplay(t, tt.routes)
			got, err := rp.gateway().DefaultPaymentMethod(context.Background(), connection(`{}`), "cus_123")
			if err != nil {
				t.Fatalf("DefaultPaymentMethod: got = %v, want = nil", err)
			}
			if got != tt.want {
				t.Errorf("method: got = %q, want = %q", got, tt.want)
			}
			if lists := rp.requests(routeMethods); len(lists) > 0 {
				checkForm(t, lists[0].Form, map[string]string{"limit": "10"})
			}
		})
	}
}

func TestListPaymentMethods(t *testing.T) {
	// source: spec/services/payment_providers/stripe/customers/create_service_spec.rb:38 (methods synced from Stripe)
	rp := newReplay(t, map[string][]reply{routeMethods: {fixture("customer_list_payment_methods_response.json")}})
	got, err := rp.gateway().ListPaymentMethods(context.Background(), connection(`{}`), "cus_123")
	if err != nil {
		t.Fatalf("ListPaymentMethods: got = %v, want = nil", err)
	}
	want := payment.ProviderMethod{
		ID:                 "pm_1RWGX9Q8iJWBZFaMnSMSDSi0",
		ProviderCustomerID: "cus_SR8o2vcGRkVb6v",
		Details:            payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 6, ExpYear: 2026},
		CreatedAt:          time.Unix(1749040507, 0).UTC(),
	}
	if len(got) != 1 || got[0].ID != want.ID || got[0].ProviderCustomerID != want.ProviderCustomerID || !got[0].CreatedAt.Equal(want.CreatedAt) || got[0].Details.Type != "card" || got[0].Details.Brand != "visa" {
		t.Errorf("methods: got = %+v, want = [%+v]", got, want)
	}
}

func TestPaymentMethod(t *testing.T) {
	const route = "GET /v1/payment_methods/pm_123"
	tests := []struct {
		source   string
		reply    reply
		want     payment.MethodDetails
		wantKind payment.ErrorKind
	}{
		{
			source: "spec/services/payment_providers/stripe/customers/fetch_default_payment_method_service_spec.rb:80",
			reply:  ok(`{"id":"pm_123","object":"payment_method","type":"card","created":1,"customer":"cus_123","card":{"brand":"visa","display_brand":"cartes_bancaires","last4":"4242","exp_month":12,"exp_year":2030}}`),
			want:   payment.MethodDetails{Type: "card", Brand: "cartes_bancaires", Last4: "4242", ExpMonth: 12, ExpYear: 2030},
		},
		{
			source: "spec/services/payment_providers/stripe/customers/fetch_default_payment_method_service_spec.rb:112",
			reply:  ok(`{"id":"pm_123","object":"payment_method","type":"sepa_debit","created":1,"customer":"cus_123"}`),
			want:   payment.MethodDetails{Type: "sepa_debit"},
		},
		{
			// The method of a payment, and the existence check of a saved method.
			source: "spec/services/payments/set_payment_method_data_service_spec.rb:15,33 spec/services/payment_provider_customers/stripe/check_payment_method_service_spec.rb:32,102",
			reply:  ok(`{"id":"pm_123","object":"payment_method","type":"card","created":1,"customer":"cus_123","card":{"brand":"visa","display_brand":"visa","last4":"4242","exp_month":8,"exp_year":2026}}`),
			want:   payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 8, ExpYear: 2026},
		},
		{
			source:   "spec/services/payment_provider_customers/stripe/check_payment_method_service_spec.rb:53",
			reply:    fail(http.StatusNotFound, "invalid_request_error", "resource_missing", "No such PaymentMethod"),
			wantKind: payment.KindNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{route: {tt.reply}})
			got, err := rp.gateway().PaymentMethod(context.Background(), connection(`{}`), "pm_123")
			if k := payment.ErrorKindOf(err); k != tt.wantKind {
				t.Fatalf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
			}
			if tt.wantKind != "" {
				return
			}
			if got.ID != "pm_123" || got.ProviderCustomerID != "cus_123" || got.Details != tt.want {
				t.Errorf("method: got = %+v, want = pm_123 of cus_123 with %+v", got, tt.want)
			}
		})
	}
}

func TestSetDefaultPaymentMethod(t *testing.T) {
	// source: spec/services/payment_providers/stripe/webhooks/setup_intent_succeeded_service_spec.rb:73
	rp := newReplay(t, map[string][]reply{routeUpdateCustomer: {ok(`{"id":"cus_123456","object":"customer"}`)}})
	if err := rp.gateway().SetDefaultPaymentMethod(context.Background(), connection(`{}`), "cus_123456", "pm_123"); err != nil {
		t.Fatalf("SetDefaultPaymentMethod: got = %v, want = nil", err)
	}
	checkForm(t, rp.only(routeUpdateCustomer).Form, map[string]string{"invoice_settings[default_payment_method]": "pm_123"})
}

func TestDetachPaymentMethod(t *testing.T) {
	const route = "POST /v1/payment_methods/pm_123/detach"
	tests := []struct {
		name     string
		reply    reply
		wantKind payment.ErrorKind
	}{
		{"detaches", ok(`{"id":"pm_123","object":"payment_method","customer":null}`), ""},
		{"already detached", fail(http.StatusBadRequest, "invalid_request_error", "payment_method_unexpected_state", "The payment method you provided is not attached to a customer"), ""},
		{"unknown method", fail(http.StatusNotFound, "invalid_request_error", "resource_missing", "No such PaymentMethod"), ""},
		{"server error", fail(http.StatusInternalServerError, "api_error", "", "boom"), payment.KindUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{route: {tt.reply}})
			err := rp.gateway().DetachPaymentMethod(context.Background(), connection(`{}`), "pm_123")
			if k := payment.ErrorKindOf(err); k != tt.wantKind {
				t.Errorf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
			}
		})
	}
}

func TestSetupURL(t *testing.T) {
	const src = "spec/services/payment_provider_customers/stripe_service_spec.rb:"
	session := ok(`{"id":"cs_123","object":"checkout.session","url":"https://example.com"}`)
	tests := []struct {
		source   string
		name     string
		settings string
		types    []string
		reply    reply
		want     string
		form     map[string]string
		wantErr  error
		wantKind payment.ErrorKind
	}{
		{
			// source: spec/services/payment_provider_customers/stripe_service_spec.rb:520 (no consent collection by default)
			source: src + "510", name: "returns the checkout URL", types: []string{"card", "crypto"}, reply: session, want: "https://example.com",
			form: map[string]string{
				"mode": "setup", "customer": "cus_123", "payment_method_types[0]": "card", "payment_method_types[1]": "",
				"success_url": DefaultSuccessRedirectURL, "consent_collection[terms_of_service]": "",
			},
		},
		{
			source: src + "534", name: "requires terms of service consent", settings: `{"require_terms_of_service_consent":true}`,
			types: []string{"card"}, reply: session, want: "https://example.com",
			form: map[string]string{"consent_collection[terms_of_service]": "required"},
		},
		{
			source: src + "628", name: "uses the connection's success URL", settings: `{"success_redirect_url":"https://acme.example/thanks"}`,
			types: []string{"card"}, reply: session, want: "https://example.com",
			form: map[string]string{"success_url": "https://acme.example/thanks"},
		},
		{source: src + "611", name: "no method needs setup", types: []string{"crypto"}, wantErr: payment.ErrNoSetupMethods},
		{
			source: src + "573", name: "invalid request", types: []string{"card"},
			reply: fail(http.StatusBadRequest, "invalid_request_error", "", "wrong request!"), wantKind: payment.KindInvalidRequest,
		},
		{
			source: src + "587", name: "authentication error", types: []string{"card"},
			reply: fail(http.StatusUnauthorized, "invalid_request_error", "", "Expired API Key provided"), wantKind: payment.KindAuthentication,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			settings := tt.settings
			if settings == "" {
				settings = `{}`
			}
			rp := newReplay(t, map[string][]reply{routeSessions: {tt.reply}})
			got, err := rp.gateway().SetupURL(context.Background(), connection(settings), payment.SetupParams{
				CustomerID: testCustomerID, ProviderCustomerID: "cus_123", MethodTypes: tt.types,
			})
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
				}
				if rp.count() != 0 {
					t.Errorf("requests: got = %d, want = 0", rp.count())
				}
				return
			case tt.wantKind != "":
				if k := payment.ErrorKindOf(err); k != tt.wantKind {
					t.Fatalf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
				}
				return
			case err != nil:
				t.Fatalf("SetupURL: got = %v, want = nil", err)
			}
			if got != tt.want {
				t.Errorf("URL: got = %q, want = %q", got, tt.want)
			}
			checkForm(t, rp.only(routeSessions).Form, tt.form)
		})
	}
}
