package moneyhash

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestCreateCustomer(t *testing.T) {
	// source: spec/services/payment_provider_customers/moneyhash_service_spec.rb:41
	rp := newReplay(t, map[string][]reply{routeCustomers: {fixture("create_customer.json")}})
	p := testCustomerParams()
	p.IdempotencyKey = "customer:key"
	id, err := rp.gateway().CreateCustomer(context.Background(), defaultConn(), p)
	if err != nil {
		t.Fatalf("CreateCustomer: %v", err)
	}
	if want := "dc98b896-c008-4446-97b8-8af852f476ef"; id != want {
		t.Errorf("id: got = %q, want = %q", id, want)
	}
	r := rp.only(routeCustomers)
	for path, want := range map[string]string{
		"type":                                   "COMPANY",
		"first_name":                             "Ahmed",
		"last_name":                              "Shahwan",
		"email":                                  "a.shahwan@moneyhash.io",
		"phone_number":                           "+201087654321",
		"company_name":                           "legal name cust00x3",
		"tax_id":                                 "76543456",
		"address":                                "first line addr second line",
		"contact_person_name":                    "Ahmed Shahwan",
		"custom_fields." + fieldCustomerID:       testCustomerID.String(),
		"custom_fields." + fieldExternalID:       "ext_cust_004",
		"custom_fields." + fieldConnectionID:     testConnID.String(),
		"custom_fields." + fieldConnectionCode:   "mh-tanvo",
		"custom_fields." + fieldOrganizationID:   testOrgID.String(),
		"custom_fields." + fieldCurrency:         "USD",
		"custom_fields." + fieldLegalNumber:      "LN-1",
		"custom_fields." + fieldTaxID:            "76543456",
		"custom_fields." + fieldRequest:          requestCreateCustomer,
		"custom_fields." + fieldProviderCustomer: "",
		"custom_fields." + fieldCustomerName:     "Shahwan Trading",
		"custom_fields." + fieldLegalName:        "legal name cust00x3",
	} {
		if got := r.str(strings.Split(path, ".")...); got != want {
			t.Errorf("%s: got = %q, want = %q", path, got, want)
		}
	}
	if r.IdempotencyKey != "customer:key" || r.APIKey != testAPIKey {
		t.Errorf("headers: got = %q, %q, want = %q, %q", r.IdempotencyKey, r.APIKey, "customer:key", testAPIKey)
	}
}

func TestCreateCustomerSparse(t *testing.T) {
	// A tax ID with letters is left out (lago sends "0"); blank fields are
	// not sent.
	rp := newReplay(t, map[string][]reply{routeCustomers: {fixture("create_customer.json")}})
	p := payment.CustomerParams{CustomerID: testCustomerID, TaxIdentificationNumber: "FR123"}
	if _, err := rp.gateway().CreateCustomer(context.Background(), defaultConn(), p); err != nil {
		t.Fatalf("CreateCustomer: %v", err)
	}
	r := rp.only(routeCustomers)
	for _, k := range []string{"type", "tax_id", "first_name", "email", "company_name", "address", "contact_person_name"} {
		if r.has(k) {
			t.Errorf("%s: got = %q, want = absent", k, r.str(k))
		}
	}
}

func TestCreateCustomerFailures(t *testing.T) {
	for name, tt := range map[string]struct {
		rep  reply
		kind payment.ErrorKind
	}{
		"no id":        {raw(http.StatusOK, `{"status":{"code":200},"data":{}}`), payment.KindPermanent},
		"unauthorized": {raw(http.StatusUnauthorized, `{"status":{"code":401,"message":"Invalid API key."}}`), payment.KindAuthentication},
		"undecodable":  {raw(http.StatusOK, `not json`), payment.KindUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routeCustomers: {tt.rep}})
			_, err := rp.gateway().CreateCustomer(context.Background(), defaultConn(), testCustomerParams())
			if pe, ok := payment.AsProviderError(err); !ok || pe.Kind != tt.kind {
				t.Fatalf("CreateCustomer: got = %v, want = kind %s", err, tt.kind)
			}
		})
	}
}

func TestUpdateCustomer(t *testing.T) {
	// source: spec/services/payment_provider_customers/moneyhash_service_spec.rb:53
	rp := newReplay(t, nil)
	if err := rp.gateway().UpdateCustomer(context.Background(), defaultConn(), testCustomer, testCustomerParams()); err != nil {
		t.Errorf("UpdateCustomer: got = %v, want = nil", err)
	}
	if rp.count() != 0 {
		t.Errorf("requests: got = %d, want = 0", rp.count())
	}
}

func TestSetupURL(t *testing.T) {
	// source: spec/services/payment_provider_customers/moneyhash_service_spec.rb:41
	rp := newReplay(t, map[string][]reply{routeIntents: {fixture("checkout_url_response.json")}})
	url, err := rp.gateway().SetupURL(context.Background(), defaultConn(), payment.SetupParams{
		CustomerID: testCustomerID, ProviderCustomerID: testCustomer, Customer: testCustomerParams(), Currency: usd,
	})
	if err != nil {
		t.Fatalf("SetupURL: %v", err)
	}
	if want := "https://stg-embed.moneyhash.io/embed/payment/LYPoo59"; url != want {
		t.Errorf("url: got = %q, want = %q", url, want)
	}
	r := rp.only(routeIntents)
	for path, want := range map[string]string{
		"amount":                                 "5.00",
		"amount_currency":                        "USD",
		"flow_id":                                testFlowID,
		"webhook_url":                            testWebhookURL,
		"merchant_initiated":                     "false",
		"tokenize_card":                          "true",
		"payment_type":                           paymentTypeUnscheduled,
		"recurring_data.agreement_id":            testCustomerID.String(),
		"customer":                               testCustomer,
		"billing_data.first_name":                "Ahmed",
		"billing_data.address1":                  "second line",
		"billing_data.postal_code":               "11511",
		"custom_fields." + fieldMIT:              "false",
		"custom_fields." + fieldRequest:          requestCheckoutURL,
		"custom_fields." + fieldProviderCustomer: testCustomer,
	} {
		if got := r.str(strings.Split(path, ".")...); got != want {
			t.Errorf("%s: got = %q, want = %q", path, got, want)
		}
	}
	if r.has("billing_data", "state") {
		t.Errorf("billing_data.state: got = %q, want = absent", r.str("billing_data", "state"))
	}
}

func TestSetupURLCurrency(t *testing.T) {
	// source: spec/services/payment_provider_customers/moneyhash_service_spec.rb:270
	// The caller passes the organization's default currency when the
	// customer has none; the adapter sends the one it is given, in its
	// exponent.
	for _, tt := range []struct {
		cur  money.Currency
		want string
	}{{usd, "5.00"}, {jpy, "5"}, {kwd, "5.000"}} {
		rp := newReplay(t, map[string][]reply{routeIntents: {fixture("checkout_url_response.json")}})
		if _, err := rp.gateway().SetupURL(context.Background(), defaultConn(), payment.SetupParams{CustomerID: testCustomerID, Currency: tt.cur}); err != nil {
			t.Fatalf("SetupURL(%s): %v", tt.cur, err)
		}
		r := rp.only(routeIntents)
		if r.str("amount_currency") != tt.cur.Code() || r.str("amount") != tt.want {
			t.Errorf("amount: got = %s %s, want = %s %s", r.str("amount"), r.str("amount_currency"), tt.want, tt.cur.Code())
		}
		if r.has("customer") {
			t.Errorf("customer: got = %q, want = absent", r.str("customer"))
		}
	}
	rp := newReplay(t, nil)
	if _, err := rp.gateway().SetupURL(context.Background(), defaultConn(), payment.SetupParams{CustomerID: testCustomerID}); err == nil {
		t.Errorf("SetupURL without a currency: got = nil, want = an error")
	}
}

func TestSetupURLNoEmbed(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routeIntents: {raw(http.StatusOK, `{"status":{"code":200},"data":{"id":"x"}}`)}})
	_, err := rp.gateway().SetupURL(context.Background(), defaultConn(), payment.SetupParams{CustomerID: testCustomerID, Currency: usd})
	if pe, ok := payment.AsProviderError(err); !ok || pe.Kind != payment.KindPermanent {
		t.Errorf("SetupURL: got = %v, want = a permanent error", err)
	}
}

func TestMethodsUnsupported(t *testing.T) {
	g := New()
	ctx, conn := context.Background(), defaultConn()
	if g.DefaultMethodTypes() != nil || g.ValidateMethodTypes([]string{"card"}) != nil {
		t.Errorf("method types: got = %v, want = none accepted freely", g.DefaultMethodTypes())
	}
	_, e1 := g.ListPaymentMethods(ctx, conn, testCustomer)
	_, e2 := g.DefaultPaymentMethod(ctx, conn, testCustomer)
	_, e3 := g.PaymentMethod(ctx, conn, testCard)
	e4 := g.SetDefaultPaymentMethod(ctx, conn, testCustomer, testCard)
	e5 := g.DetachPaymentMethod(ctx, conn, testCard)
	for i, err := range []error{e1, e2, e3, e4, e5} {
		if !errors.Is(err, payment.ErrUnsupported) {
			t.Errorf("method call %d: got = %v, want = %v", i+1, err, payment.ErrUnsupported)
		}
	}
}
