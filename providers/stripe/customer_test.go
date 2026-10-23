package stripe

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

const (
	routeCreateCustomer = "POST /v1/customers"
	routeListCustomers  = "GET /v1/customers"
	routeUpdateCustomer = "POST /v1/customers/cus_123456"
)

func TestValidateMethodTypes(t *testing.T) {
	const src = "spec/models/payment_provider_customers/stripe_customer_spec.rb:"
	all := append(slices.Clone(MethodsWithSetup), MethodCrypto)
	tests := []struct {
		source string
		types  []string
		want   string // "" is valid
	}{
		{src + "25", []string{}, core.CodeRequired},
		{src + "33", nil, core.CodeRequired},
		{src + "41", []string{"invalid"}, core.CodeInvalid},
		{src + "49", []string{"card", "cash"}, core.CodeInvalid},
		{src + "57", []string{"card"}, ""},
		{src + "65", all, ""},
		{src + "74", []string{"link"}, core.CodeInvalid},
		{src + "82", []string{"link", "card"}, ""},
		{src + "91", []string{"customer_balance", "card"}, CodeCustomerBalanceNotExclusive},
		{src + "99", []string{"customer_balance"}, ""},
	}
	g := New()
	for _, tt := range tests {
		err := g.ValidateMethodTypes(tt.types)
		if tt.want == "" {
			if err != nil {
				t.Errorf("%s: ValidateMethodTypes(%v): got = %v, want = nil", tt.source, tt.types, err)
			}
			continue
		}
		var ve *core.ValidationError
		if !errors.As(err, &ve) || !ve.Has("provider_payment_methods", tt.want) {
			t.Errorf("%s: ValidateMethodTypes(%v): got = %v, want = provider_payment_methods: %s", tt.source, tt.types, err, tt.want)
		}
	}
}

func TestDefaultMethodTypes(t *testing.T) {
	// source: app/models/payment_provider_customers/stripe_customer.rb provider_payment_methods default
	if got := New().DefaultMethodTypes(); !slices.Equal(got, []string{MethodCard}) {
		t.Errorf("DefaultMethodTypes: got = %v, want = [card]", got)
	}
}

func TestSetupMethods(t *testing.T) {
	// source: spec/models/payment_provider_customers/stripe_customer_spec.rb:132,144
	tests := []struct {
		types []string
		want  []string
	}{
		{[]string{"card"}, []string{"card"}},
		{[]string{"card", "crypto"}, []string{"card"}},
		{[]string{"crypto"}, nil},
		{[]string{"customer_balance"}, nil},
	}
	for _, tt := range tests {
		if got := setupMethods(tt.types); !slices.Equal(got, tt.want) {
			t.Errorf("setupMethods(%v): got = %v, want = %v", tt.types, got, tt.want)
		}
	}
}

func customerParams() payment.CustomerParams {
	return payment.CustomerParams{
		CustomerID:     testCustomerID,
		ExternalID:     "cust_ext_1",
		Name:           "Big inc",
		Email:          "billing@big.example",
		Phone:          "+33600000000",
		Address:        core.Address{Line1: "1 rue de Rivoli", City: "Paris", Zipcode: "75001", Country: "FR"},
		IdempotencyKey: payment.CustomerIdempotencyKey(testCustomerID, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)),
	}
}

func TestCreateCustomer(t *testing.T) {
	tests := []struct {
		source   string
		name     string
		params   func(p *payment.CustomerParams)
		routes   map[string][]reply
		want     string
		wantKind payment.ErrorKind
		wantErr  error
		form     map[string]string
		noList   bool
	}{
		{
			source: "spec/services/payment_provider_customers/stripe_service_spec.rb:33",
			name:   "creates the customer with its name",
			routes: map[string][]reply{routeCreateCustomer: {ok(`{"id":"cus_123456","object":"customer"}`)}},
			want:   "cus_123456",
			form: map[string]string{
				"name":                        "Big inc",
				"email":                       "billing@big.example",
				"phone":                       "+33600000000",
				"address[line1]":              "1 rue de Rivoli",
				"address[city]":               "Paris",
				"address[postal_code]":        "75001",
				"address[country]":            "FR",
				"address[line2]":              "",
				"metadata[tanvo_customer_id]": testCustomerID.String(),
				"metadata[customer_id]":       "cust_ext_1",
			},
		},
		{
			source: "spec/services/payment_provider_customers/stripe_service_spec.rb:63",
			name:   "creates the customer with first and last names",
			params: func(p *payment.CustomerParams) { p.Name = "Ada Lovelace" },
			routes: map[string][]reply{routeCreateCustomer: {ok(`{"id":"cus_123456","object":"customer"}`)}},
			want:   "cus_123456",
			form:   map[string]string{"name": "Ada Lovelace"},
		},
		{
			source:   "spec/services/payment_provider_customers/stripe_service_spec.rb:140",
			name:     "incorrect API key",
			routes:   map[string][]reply{routeCreateCustomer: {fail(http.StatusUnauthorized, "invalid_request_error", "", "API key invalid.")}},
			wantKind: payment.KindAuthentication,
			wantErr:  payment.ErrAuthentication,
		},
		{
			source:   "spec/services/payment_provider_customers/stripe_service_spec.rb:162",
			name:     "invalid request",
			routes:   map[string][]reply{routeCreateCustomer: {fail(http.StatusBadRequest, "invalid_request_error", "", "error")}},
			wantKind: payment.KindInvalidRequest,
			wantErr:  payment.ErrRejected,
		},
		{
			source: "spec/services/payment_provider_customers/stripe_service_spec.rb:191",
			name:   "idempotency conflict adopts the customer with the email",
			routes: map[string][]reply{
				routeCreateCustomer: {fail(http.StatusBadRequest, "idempotency_error", "", "idempotency")},
				routeListCustomers:  {ok(`{"object":"list","has_more":false,"data":[{"id":"cus_123456","object":"customer"}]}`)},
			},
			want: "cus_123456",
		},
		{
			source: "app/services/payment_provider_customers/stripe_service.rb:161",
			name:   "idempotency conflict with several customers of the email",
			routes: map[string][]reply{
				routeCreateCustomer: {fail(http.StatusBadRequest, "idempotency_error", "", "idempotency")},
				routeListCustomers:  {ok(`{"object":"list","has_more":true,"data":[{"id":"cus_1","object":"customer"},{"id":"cus_2","object":"customer"}]}`)},
			},
			wantKind: payment.KindIdempotency,
		},
		{
			source:   "app/services/payment_provider_customers/stripe_service.rb:161",
			name:     "idempotency conflict without an email is not resolved by listing",
			params:   func(p *payment.CustomerParams) { p.Email = "" },
			routes:   map[string][]reply{routeCreateCustomer: {fail(http.StatusBadRequest, "idempotency_error", "", "idempotency")}},
			wantKind: payment.KindIdempotency,
			noList:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, tt.routes)
			p := customerParams()
			if tt.params != nil {
				tt.params(&p)
			}
			got, err := rp.gateway().CreateCustomer(context.Background(), connection(`{}`), p)
			if tt.wantKind != "" {
				if k := payment.ErrorKindOf(err); k != tt.wantKind {
					t.Fatalf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Errorf("error: got = %v, want = %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("CreateCustomer: got = %v, want = nil", err)
			}
			if got != tt.want {
				t.Errorf("provider customer: got = %q, want = %q", got, tt.want)
			}
			req := rp.only(routeCreateCustomer)
			if req.IdempotencyKey != p.IdempotencyKey {
				t.Errorf("Idempotency-Key: got = %q, want = %q", req.IdempotencyKey, p.IdempotencyKey)
			}
			checkForm(t, req.Form, tt.form)
			lists := rp.requests(routeListCustomers)
			if tt.noList && len(lists) != 0 {
				t.Errorf("customer lists: got = %d, want = 0", len(lists))
			}
			if len(lists) > 0 {
				checkForm(t, lists[0].Form, map[string]string{"email": p.Email})
			}
		})
	}
}

func TestUpdateCustomer(t *testing.T) {
	tests := []struct {
		source   string
		name     string
		reply    reply
		wantKind payment.ErrorKind
	}{
		{
			source: "spec/services/payment_provider_customers/stripe_service_spec.rb:297",
			name:   "updates the customer",
			reply:  ok(`{"id":"cus_123456","object":"customer"}`),
		},
		{
			source:   "spec/services/payment_provider_customers/stripe_service_spec.rb:217",
			name:     "invalid request",
			reply:    fail(http.StatusBadRequest, "invalid_request_error", "", "Invalid request"),
			wantKind: payment.KindInvalidRequest,
		},
		{
			source:   "spec/services/payment_provider_customers/stripe_service_spec.rb:243",
			name:     "permission error",
			reply:    fail(http.StatusForbidden, "invalid_request_error", "", "Permission error"),
			wantKind: payment.KindAuthentication,
		},
		{
			source:   "spec/services/payment_provider_customers/stripe_service_spec.rb:269",
			name:     "authentication error",
			reply:    fail(http.StatusUnauthorized, "invalid_request_error", "", "Invalid username."),
			wantKind: payment.KindAuthentication,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			rp := newReplay(t, map[string][]reply{routeUpdateCustomer: {tt.reply}})
			p := customerParams()
			p.Phone = ""
			p.Address.City = ""
			err := rp.gateway().UpdateCustomer(context.Background(), connection(`{}`), "cus_123456", p)
			if k := payment.ErrorKindOf(err); k != tt.wantKind {
				t.Fatalf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
			}
			if tt.wantKind != "" {
				pe, _ := payment.AsProviderError(err)
				if pe.Message == "" {
					t.Errorf("message: got = empty, want = Stripe's message")
				}
				return
			}
			req := rp.only(routeUpdateCustomer)
			// Blank attributes are sent empty, clearing them at Stripe.
			checkForm(t, req.Form, map[string]string{"name": "Big inc", "email": "billing@big.example", "metadata[tanvo_customer_id]": ""})
			for _, k := range []string{"phone", "address[city]", "address[state]"} {
				if v, present := req.Form[k]; !present || v[0] != "" {
					t.Errorf("form %s: got = %q, want = sent empty", k, v)
				}
			}
		})
	}
}
