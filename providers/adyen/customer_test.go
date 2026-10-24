package adyen

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

const (
	routePaymentLinks   = "POST /paymentLinks"
	routePaymentMethods = "POST /paymentMethods"
)

func setupParams() payment.SetupParams {
	return payment.SetupParams{
		CustomerID: testCustomerID,
		Customer: payment.CustomerParams{
			CustomerID: testCustomerID,
			ExternalID: testExternalID,
			Email:      "billing@acme.example",
		},
		Currency: eur,
	}
}

func TestSetupURL(t *testing.T) {
	const src = "spec/services/payment_provider_customers/adyen_service_spec.rb:"
	tests := []struct {
		source   string
		name     string
		edit     func(p *payment.SetupParams)
		reply    reply
		want     string
		wantKind payment.ErrorKind
		check    func(t *testing.T, r request)
	}{
		{
			source: src + "29", name: "zero-amount authorization link",
			reply: fixture("payment_links_response.json"), want: "https://test.adyen.link/test",
			check: func(t *testing.T, r request) {
				want := map[string]string{
					"reference":                "authorization customer " + testExternalID,
					"merchantAccount":          testMerchantAccount,
					"returnUrl":                DefaultSuccessRedirectURL,
					"shopperReference":         testExternalID,
					"storePaymentMethodMode":   "enabled",
					"recurringProcessingModel": "UnscheduledCardOnFile",
					"expiresAt":                "2026-07-15T10:00:00Z",
					"shopperEmail":             "billing@acme.example",
				}
				for k, w := range want {
					if got := r.str(k); got != w {
						t.Errorf("%s: got = %q, want = %q", k, got, w)
					}
				}
				if got := r.str("amount"); got != `{"currency":"EUR","value":0}` {
					t.Errorf("amount: got = %s, want = zero EUR", got)
				}
				if got := r.str("metadata", metaCustomerID); got != testCustomerID.String() {
					t.Errorf("metadata %s: got = %q, want = %q", metaCustomerID, got, testCustomerID)
				}
			},
		},
		{source: src + "34", name: "returns the link URL", reply: fixture("payment_links_response.json"), want: "https://test.adyen.link/test"},
		{source: src + "167", name: "link generated", reply: fixture("payment_links_response.json"), want: "https://test.adyen.link/test"},
		{
			source: src + "184", name: "currency given by the caller (customer's, else the organization's)",
			edit:  func(p *payment.SetupParams) { p.Currency = money.MustCurrency("GBP") },
			reply: fixture("payment_links_response.json"), want: "https://test.adyen.link/test",
			check: func(t *testing.T, r request) {
				if got := r.str("amount", "currency"); got != "GBP" {
					t.Errorf("amount.currency: got = %q, want = GBP", got)
				}
			},
		},
		{
			source: "app/services/payment_provider_customers/adyen_service.rb:130", name: "no email",
			edit:  func(p *payment.SetupParams) { p.Customer.Email = "" },
			reply: fixture("payment_links_response.json"), want: "https://test.adyen.link/test",
			check: func(t *testing.T, r request) {
				if r.has("shopperEmail") {
					t.Errorf("shopperEmail: got = %q, want = absent", r.str("shopperEmail"))
				}
			},
		},
		{source: src + "66", name: "error response", reply: failFixture(http.StatusUnprocessableEntity, "payment_links_error_response.json"), wantKind: payment.KindInvalidRequest},
		{source: src + "85", name: "server error", reply: fail(http.StatusInternalServerError, "internal", "901", "error"), wantKind: payment.KindUnavailable},
		{source: src + "107", name: "authentication error", reply: fail(http.StatusUnauthorized, "security", "000", "error"), wantKind: payment.KindAuthentication},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{routePaymentLinks: {tt.reply}})
			p := setupParams()
			if tt.edit != nil {
				tt.edit(&p)
			}
			got, err := rp.gateway().SetupURL(context.Background(), defaultConn(), p)
			if k := payment.ErrorKindOf(err); k != tt.wantKind || (tt.wantKind == "" && err != nil) {
				t.Fatalf("%s: error: got = %v, want kind = %q", tt.source, err, tt.wantKind)
			}
			if got != tt.want {
				t.Errorf("%s: URL: got = %q, want = %q", tt.source, got, tt.want)
			}
			if tt.check != nil {
				tt.check(t, rp.only(routePaymentLinks))
			}
		})
	}
}

func TestSetupURLErrors(t *testing.T) {
	const src = "spec/services/payment_provider_customers/adyen_service_spec.rb:"
	t.Run("error code is the error type", func(t *testing.T) {
		// source: spec/services/payment_provider_customers/adyen_service_spec.rb:66
		rp := newReplay(t, map[string][]reply{routePaymentLinks: {failFixture(http.StatusUnprocessableEntity, "payment_links_error_response.json")}})
		_, err := rp.gateway().SetupURL(context.Background(), defaultConn(), setupParams())
		pe, ok := payment.AsProviderError(err)
		if !ok || pe.Code != "validation" || pe.Message != "There are no payment methods available for the given parameters." {
			t.Errorf("%s66: got = %+v, want = validation with Adyen's message", src, pe)
		}
	})
	t.Run("missing external ID or currency", func(t *testing.T) {
		rp := newReplay(t, map[string][]reply{})
		p := setupParams()
		p.Customer.ExternalID, p.Currency = "", money.Currency{}
		_, err := rp.gateway().SetupURL(context.Background(), defaultConn(), p)
		var ve *core.ValidationError
		if !errors.As(err, &ve) || !ve.Has("external_id", core.CodeRequired) {
			t.Errorf("error: got = %v, want = external_id required", err)
		}
		if rp.count() != 0 {
			t.Errorf("requests: got = %d, want = 0", rp.count())
		}
	})
}

func TestListPaymentMethods(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routePaymentMethods: {fixture("payment_methods_response.json")}})
	g := rp.gateway()
	ms, err := g.ListPaymentMethods(context.Background(), defaultConn(), testExternalID)
	if err != nil {
		t.Fatalf("ListPaymentMethods: got = %v, want = nil", err)
	}
	want := payment.ProviderMethod{
		ID:                 "STORED_PM_1",
		ProviderCustomerID: testExternalID,
		Details:            payment.MethodDetails{Type: "card", Brand: "visa", Last4: "1234", ExpMonth: 3, ExpYear: 2030},
	}
	if len(ms) != 1 || ms[0] != want {
		t.Errorf("methods: got = %+v, want = [%+v]", ms, want)
	}
	req := rp.only(routePaymentMethods)
	if req.str("shopperReference") != testExternalID || req.str("merchantAccount") != testMerchantAccount {
		t.Errorf("request: got = %v, want = shopper reference and merchant account", req.Body)
	}
	// source: spec/services/payment_providers/adyen/payments/create_service_spec.rb:72 (the first stored method)
	if id, err := g.DefaultPaymentMethod(context.Background(), defaultConn(), testExternalID); err != nil || id != "STORED_PM_1" {
		t.Errorf("DefaultPaymentMethod: got = %q, %v, want = STORED_PM_1", id, err)
	}

	empty := newReplay(t, map[string][]reply{routePaymentMethods: {fixture("payment_methods_empty_response.json")}})
	if id, err := empty.gateway().DefaultPaymentMethod(context.Background(), defaultConn(), testExternalID); err != nil || id != "" {
		t.Errorf("DefaultPaymentMethod without methods: got = %q, %v, want = \"\"", id, err)
	}
}
