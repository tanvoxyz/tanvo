package gocardless

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func customerParams() payment.CustomerParams {
	return payment.CustomerParams{
		CustomerID:     testCustomerID,
		ExternalID:     "cus_ext_1",
		Name:           "Acme",
		CompanyName:    "Acme",
		Firstname:      "Ada",
		Lastname:       "Lovelace",
		Email:          "billing@acme.example",
		IdempotencyKey: "customer:" + testCustomerID.String(),
	}
}

func TestCreateCustomer(t *testing.T) {
	// source: spec/services/payment_provider_customers/gocardless_service_spec.rb:29
	// source: spec/services/payment_provider_customers/gocardless_service_spec.rb:44
	rp := newReplay(t, map[string][]reply{routeCustomers: {created("customers_response.json")}})
	id, err := rp.gateway().CreateCustomer(context.Background(), defaultConn(), customerParams())
	if err != nil || id != "CU123" {
		t.Fatalf("CreateCustomer: got = %q, %v, want = CU123", id, err)
	}
	req := rp.only(routeCustomers)
	want := map[string]any{
		"email":        "billing@acme.example",
		"company_name": "Acme",
		"given_name":   "Ada",
		"family_name":  "Lovelace",
	}
	if got := req.Body["customers"]; !reflect.DeepEqual(got, want) {
		t.Errorf("customers: got = %v, want = %v", got, want)
	}
	if req.IdempotencyKey != "customer:"+testCustomerID.String() {
		t.Errorf("Idempotency-Key: got = %q, want = the customer key", req.IdempotencyKey)
	}

	// Blank names are left out (lago .presence + compact).
	rp = newReplay(t, map[string][]reply{routeCustomers: {created("customers_response.json")}})
	p := customerParams()
	p.CompanyName, p.Firstname, p.Lastname, p.IdempotencyKey = "", "", "", ""
	if _, err := rp.gateway().CreateCustomer(context.Background(), defaultConn(), p); err != nil {
		t.Fatalf("CreateCustomer: got = %v, want = nil", err)
	}
	req = rp.only(routeCustomers)
	if got := req.Body["customers"]; !reflect.DeepEqual(got, map[string]any{"email": "billing@acme.example"}) {
		t.Errorf("customers: got = %v, want = the email only", got)
	}
	if req.IdempotencyKey == "" {
		t.Errorf("Idempotency-Key: got = none, want = the client's generated key")
	}
}

func TestCreateCustomerConflict(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routeCustomers: {failFixture(http.StatusConflict, "customer_conflict_response.json")}})
	id, err := rp.gateway().CreateCustomer(context.Background(), defaultConn(), customerParams())
	if err != nil || id != "CU123" {
		t.Errorf("CreateCustomer: got = %q, %v, want = the existing customer CU123", id, err)
	}
}

func TestCreateCustomerError(t *testing.T) {
	// source: spec/services/payment_provider_customers/gocardless_service_spec.rb:80
	rp := newReplay(t, map[string][]reply{routeCustomers: {failFixture(http.StatusUnprocessableEntity, "validation_failed_response.json")}})
	_, err := rp.gateway().CreateCustomer(context.Background(), defaultConn(), customerParams())
	pe, ok := payment.AsProviderError(err)
	if !ok || pe.Kind != payment.KindInvalidRequest || pe.Message != "Validation failed" || pe.Code != "mandate_is_inactive" {
		t.Errorf("error: got = %v, want = the provider's message and code for the error webhook", err)
	}
}

func TestUpdateCustomer(t *testing.T) {
	// source: spec/services/payment_provider_customers/gocardless_service_spec.rb:101
	rp := newReplay(t, map[string][]reply{})
	if err := rp.gateway().UpdateCustomer(context.Background(), defaultConn(), "CU123", customerParams()); err != nil {
		t.Errorf("UpdateCustomer: got = %v, want = nil", err)
	}
	if rp.count() != 0 {
		t.Errorf("requests: got = %d, want = 0", rp.count())
	}
}

func TestSetupURL(t *testing.T) {
	// source: spec/services/payment_provider_customers/gocardless_service_spec.rb:121
	// source: spec/services/payment_provider_customers/gocardless_service_spec.rb:128
	tests := []struct {
		settings, wantReturn string
	}{
		{`{"success_redirect_url":"https://acme.example/return"}`, "https://acme.example/return"},
		{`{}`, DefaultSuccessRedirectURL},
	}
	for _, tt := range tests {
		rp := newReplay(t, map[string][]reply{
			routeBillingRequests:    {created("billing_requests_response.json")},
			routeBillingRequestFlow: {created("billing_request_flows_response.json")},
		})
		u, err := rp.gateway().SetupURL(context.Background(), connection(tt.settings), payment.SetupParams{
			CustomerID: testCustomerID, ProviderCustomerID: testCustomer,
		})
		if err != nil || u != "https://pay.gocardless.com/billing/static/flow?id=BRF123" {
			t.Fatalf("SetupURL: got = %q, %v, want = the flow's authorisation URL", u, err)
		}
		br := rp.only(routeBillingRequests)
		if got := br.str("billing_requests", "mandate_request", "scheme"); got != "bacs" {
			t.Errorf("mandate_request.scheme: got = %q, want = bacs", got)
		}
		if got := br.str("billing_requests", "links", "customer"); got != testCustomer {
			t.Errorf("links.customer: got = %q, want = %q", got, testCustomer)
		}
		flow := rp.only(routeBillingRequestFlow)
		for _, k := range []string{"redirect_uri", "exit_uri"} {
			if got := flow.str("billing_request_flows", k); got != tt.wantReturn {
				t.Errorf("%s of %s: got = %q, want = %q", k, tt.settings, got, tt.wantReturn)
			}
		}
		if got := flow.str("billing_request_flows", "links", "billing_request"); got != "BRQ123" {
			t.Errorf("links.billing_request: got = %q, want = BRQ123", got)
		}
	}
}

func TestSetupURLErrors(t *testing.T) {
	for _, routes := range []map[string][]reply{
		{routeBillingRequests: {failFixture(http.StatusUnprocessableEntity, "validation_failed_response.json")}},
		{
			routeBillingRequests:    {created("billing_requests_response.json")},
			routeBillingRequestFlow: {failFixture(http.StatusInternalServerError, "server_error_response.json")},
		},
	} {
		rp := newReplay(t, routes)
		if _, err := rp.gateway().SetupURL(context.Background(), defaultConn(), payment.SetupParams{ProviderCustomerID: testCustomer}); err == nil {
			t.Errorf("SetupURL: got = nil, want = an error")
		}
	}
}

func TestListPaymentMethods(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routeMandates: {fixture("mandates_list_response.json")}})
	g := rp.gateway()
	ms, err := g.ListPaymentMethods(context.Background(), defaultConn(), testCustomer)
	if err != nil {
		t.Fatalf("ListPaymentMethods: got = %v, want = nil", err)
	}
	want := []payment.ProviderMethod{
		{ID: "MD123", ProviderCustomerID: "CU123", Details: payment.MethodDetails{Type: "bacs"}, CreatedAt: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)},
		{ID: "MD100", ProviderCustomerID: "CU123", Details: payment.MethodDetails{Type: "sepa_core"}, CreatedAt: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)},
	}
	if !reflect.DeepEqual(ms, want) {
		t.Errorf("methods: got = %+v, want = %+v", ms, want)
	}
	def, err := g.DefaultPaymentMethod(context.Background(), defaultConn(), testCustomer)
	if err != nil || def != "MD123" {
		t.Errorf("DefaultPaymentMethod: got = %q, %v, want = the newest mandate MD123", def, err)
	}

	rp = newReplay(t, map[string][]reply{routeMandates: {fixture("mandates_empty_response.json")}})
	if def, err := rp.gateway().DefaultPaymentMethod(context.Background(), defaultConn(), testCustomer); err != nil || def != "" {
		t.Errorf("DefaultPaymentMethod without mandates: got = %q, %v, want = none", def, err)
	}
	rp = newReplay(t, map[string][]reply{routeMandates: {failFixture(http.StatusInternalServerError, "server_error_response.json")}})
	if _, err := rp.gateway().DefaultPaymentMethod(context.Background(), defaultConn(), testCustomer); !errors.Is(err, payment.ErrRetryable) {
		t.Errorf("DefaultPaymentMethod: got = %v, want = retryable", err)
	}
}

func TestPaymentMethod(t *testing.T) {
	rp := newReplay(t, map[string][]reply{"GET /mandates/index_ID_123": {fixture("mandate_response.json")}})
	pm, err := rp.gateway().PaymentMethod(context.Background(), defaultConn(), "index_ID_123")
	want := payment.ProviderMethod{
		ID: "index_ID_123", ProviderCustomerID: "CU123456",
		Details: payment.MethodDetails{Type: "bacs"}, CreatedAt: time.Date(2026, 2, 3, 10, 50, 0, 0, time.UTC),
	}
	if err != nil || !reflect.DeepEqual(pm, want) {
		t.Errorf("PaymentMethod: got = %+v, %v, want = %+v", pm, err, want)
	}
	rp = newReplay(t, map[string][]reply{"GET /mandates/MD404": {failFixture(http.StatusNotFound, "not_found_response.json")}})
	if _, err := rp.gateway().PaymentMethod(context.Background(), defaultConn(), "MD404"); payment.ErrorKindOf(err) != payment.KindNotFound {
		t.Errorf("missing mandate: got = %v, want = %s", err, payment.KindNotFound)
	}
}
