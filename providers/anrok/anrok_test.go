package anrok

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/providers/apiclient/apiclienttest"
)

var now = time.Date(2024, 3, 7, 12, 0, 0, 0, time.UTC)

func gateway(rp *apiclienttest.Server) *Gateway {
	return New(WithBaseURL(rp.URL), WithClock(func() time.Time { return now }), WithRateLimit(0))
}

func newReplay(t *testing.T, routes map[string]apiclienttest.Reply) *apiclienttest.Server {
	t.Helper()
	r := map[string][]apiclienttest.Reply{}
	for k, v := range routes {
		r["POST "+k] = []apiclienttest.Reply{v}
	}
	return apiclienttest.New(t, r)
}

func body(t *testing.T, rp *apiclienttest.Server) map[string]any {
	t.Helper()
	all := rp.Requests("")
	if len(all) != 1 {
		t.Fatalf("requests: got = %d, want = 1", len(all))
	}
	m, _ := all[0].JSONBody(t).(map[string]any)
	return m
}

var conn = integration.Connection{
	ID:             core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001"),
	OrganizationID: core.MustParseID[core.OrganizationID]("11111111-2222-3333-4444-555555555555"),
	Code:           "anrok", Provider: Provider,
	Secrets: json.RawMessage(`{"api_key":"sacc/secret.123"}`),
}

func transaction() *integration.TaxTransaction {
	return &integration.TaxTransaction{
		ID:          "0199a000-0000-7000-8000-0000000000aa",
		IssuingDate: core.NewDate(2024, 3, 7),
		Currency:    money.MustCurrency("USD"),
		Customer: integration.TaxCustomer{
			LinkedID: "", ExternalID: "cus_lago_12345", Name: "John Doe",
			Address:   core.Address{Line1: "88 rue du Chemin Vert", City: "Paris", Zipcode: "75011", Country: "FR"},
			TaxNumber: "FR86894827773",
		},
		Lines: []integration.TaxLine{
			{ItemKey: "fee-1", ItemID: "fee-1", ItemCode: "m1", AmountCents: 9900},
			{ItemKey: "charge-1", ItemID: "charge-1", ItemCode: "1", AmountCents: 1000},
		},
	}
}

func TestReportTaxes(t *testing.T) {
	// source: spec/services/integrations/aggregator/taxes/invoices/payloads/anrok_spec.rb:6
	// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:122
	rp := newReplay(t, map[string]apiclienttest.Reply{"/v1/seller/transactions/createOrUpdate": {Status: 200, Fixture: "create_or_update_response.json"}})
	got, err := gateway(rp).ReportTaxes(context.Background(), conn, transaction())
	if err != nil {
		t.Fatalf("ReportTaxes() error = %v", err)
	}
	req := rp.Only("POST /v1/seller/transactions/createOrUpdate")
	if got := req.Header.Get("Authorization"); got != "Bearer sacc/secret.123" {
		t.Errorf("Authorization = %q, want = bearer api key", got)
	}
	wantBody := `{"accountingDate":"2024-03-07","currencyCode":"USD","customerAddress":{"city":"Paris","country":"FR","line1":"88 rue du Chemin Vert","postalCode":"75011"},"customerId":"cus_lago_12345","customerName":"John Doe","customerTaxIds":[{"type":"genericVatNumber","value":"FR86894827773"}],"id":"0199a000-0000-7000-8000-0000000000aa","lineItems":[{"amount":9900,"id":"fee-1","productExternalId":"m1"},{"amount":1000,"id":"charge-1","productExternalId":"1"}],"taxDate":"2024-03-07"}`
	if !apiclienttest.EqualJSON(t, req.Body, wantBody) {
		t.Errorf("body = %s, want = %s", req.Body, wantBody)
	}
	if got.TransactionID != "0199a000-0000-7000-8000-0000000000aa" || len(got.Lines) != 2 {
		t.Fatalf("result = %+v, want = 2 lines of the transaction", got)
	}
	l := got.Lines[0]
	if l.ItemKey != "fee-1" || l.ItemCode != "m1" || l.AmountCents.String() != "9900" || *l.TaxAmountCents != 990 {
		t.Errorf("line 0 = %+v, want = fee-1 9900 tax 990", l)
	}
	if len(l.Breakdown) != 2 || l.Breakdown[0].Name != "GST/HST" || l.Breakdown[0].Rate.String() != "0.10" ||
		l.Breakdown[0].TaxAmount.String() != "990" || l.Breakdown[1].Type != "exempt" || l.Breakdown[1].Reason != "reverseCharge" || l.Breakdown[1].Rate != nil {
		t.Errorf("line 0 breakdown = %+v, want = GST/HST 0.10 990 and exempt reverseCharge", l.Breakdown)
	}
	// source: spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:245
	l = got.Lines[1]
	if *l.TaxAmountCents != 0 || len(l.Breakdown) != 2 || l.Breakdown[0].Type != "productNotTaxed" || l.Breakdown[1].TaxAmount.String() != "0.422" {
		t.Errorf("line 1 = %+v, want = not taxed and a 0.422 tax", l)
	}
}

func TestQuoteTaxes(t *testing.T) {
	// source: spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:122
	rp := newReplay(t, map[string]apiclienttest.Reply{"/v1/seller/transactions/createEphemeral": {Status: 200, Fixture: "create_or_update_response.json"}})
	tx := transaction()
	tx.ID = ""
	tx.Customer.TaxNumber = ""
	tx.Customer.LinkedID = "anrok-cus"
	got, err := gateway(rp).QuoteTaxes(context.Background(), conn, tx)
	if err != nil {
		t.Fatalf("QuoteTaxes() error = %v", err)
	}
	body := body(t, rp)
	if _, ok := body["id"]; ok {
		t.Errorf("body id = %v, want = absent", body["id"])
	}
	if _, ok := body["customerTaxIds"]; ok {
		t.Errorf("customerTaxIds = %v, want = absent without tax number", body["customerTaxIds"])
	}
	if body["customerId"] != "anrok-cus" {
		t.Errorf("customerId = %v, want = the integration customer's", body["customerId"])
	}
	if got.TransactionID != "" || len(got.Lines) != 2 {
		t.Errorf("result = %+v, want = no transaction, 2 lines", got)
	}
}

func TestDateCap(t *testing.T) {
	// source: spec/services/integrations/aggregator/taxes/invoices/payloads/anrok_spec.rb:63
	rp := newReplay(t, map[string]apiclienttest.Reply{"/v1/seller/transactions/createEphemeral": {Status: 200, Body: `{"lineItems":[]}`}})
	tx := transaction()
	tx.IssuingDate = core.NewDate(2024, 6, 1)
	tx.TaxDate = core.NewDate(2024, 3, 1)
	if _, err := gateway(rp).QuoteTaxes(context.Background(), conn, tx); err != nil {
		t.Fatal(err)
	}
	body := body(t, rp)
	if body["accountingDate"] != "2024-04-06" || body["taxDate"] != "2024-03-01" {
		t.Errorf("dates = %v, %v, want = 2024-04-06, 2024-03-01", body["accountingDate"], body["taxDate"])
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		rep     apiclienttest.Reply
		kind    integration.ErrorKind
		code    string
		message string
	}{
		{"conflict", "spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:357", apiclienttest.Reply{Status: 409, Body: `{"type":"taxDateTooFarInFuture"}`}, integration.KindInvalidRequest, "taxDateTooFarInFuture", "Service failure"},
		{"bad request", "spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:406", apiclienttest.Reply{Status: 400, Body: `Request body: "lineItems": Required.`}, integration.KindInvalidRequest, "validationError", `Request body: "lineItems": Required.`},
		{"api limit", "spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:403", apiclienttest.Reply{Status: 429, Body: "You've exceeded your API limit of 10 per second", Header: map[string]string{"Retry-After": "1"}}, integration.KindRequestLimit, "", "You've exceeded your API limit of 10 per second"},
		{"bad gateway", "spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:415", apiclienttest.Reply{Status: 502, Body: "<html>502 Bad Gateway</html>"}, integration.KindUnavailable, "", "<html>502 Bad Gateway</html>"},
		{"server error", "spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:427", apiclienttest.Reply{Status: 500, Body: "boom"}, integration.KindUnavailable, "", "boom"},
		{"unauthorized", "", apiclienttest.Reply{Status: 401, Body: "Unauthorized"}, integration.KindAuthentication, "", "Unauthorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			rp := newReplay(t, map[string]apiclienttest.Reply{"/v1/seller/transactions/createOrUpdate": tt.rep})
			_, err := gateway(rp).ReportTaxes(context.Background(), conn, transaction())
			pe, ok := integration.AsProviderError(err)
			if !ok || pe.Kind != tt.kind || pe.Code != tt.code || pe.Message != tt.message {
				t.Errorf("error = %#v, want = %s %q %q", err, tt.kind, tt.code, tt.message)
			}
		})
	}
}

func TestVoidTaxes(t *testing.T) {
	tests := []struct {
		name string
		rep  apiclienttest.Reply
		want error
		kind integration.ErrorKind
	}{
		// source: spec/services/integrations/aggregator/taxes/invoices/void_service_spec.rb:133
		{"voided", apiclienttest.Reply{Status: 200, Body: `{}`}, nil, ""},
		// source: spec/services/invoices/provider_taxes/void_service_spec.rb:203
		{"frozen", apiclienttest.Reply{Status: 409, Body: `{"type":"transactionFrozenForFiling"}`}, integration.ErrRejected, integration.KindLocked},
		{"unknown", apiclienttest.Reply{Status: 409, Body: `{"type":"transactionIdNotFound"}`}, integration.ErrRejected, integration.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string]apiclienttest.Reply{"/v1/seller/transactions/id:inv-1/void": tt.rep})
			err := gateway(rp).VoidTaxes(context.Background(), conn, "inv-1")
			if tt.want == nil {
				if err != nil {
					t.Fatalf("VoidTaxes() error = %v, want = nil", err)
				}
				if b, _ := json.Marshal(body(t, rp)); string(b) != "{}" {
					t.Errorf("body = %s, want = {}", b)
				}
				return
			}
			pe, ok := integration.AsProviderError(err)
			if !errors.Is(err, tt.want) || !ok || pe.Kind != tt.kind {
				t.Errorf("VoidTaxes() error = %v, want = %s", err, tt.kind)
			}
		})
	}
}

func TestNegateTaxes(t *testing.T) {
	// source: spec/services/integrations/aggregator/taxes/invoices/negate_service_spec.rb:74
	rp := newReplay(t, map[string]apiclienttest.Reply{"/v1/seller/transactions/createNegation": {Status: 200, Body: `{}`}})
	if err := gateway(rp).NegateTaxes(context.Background(), conn, "inv-1", "inv-1_voided"); err != nil {
		t.Fatalf("NegateTaxes() error = %v", err)
	}
	if b, _ := json.Marshal(body(t, rp)); string(b) != `{"newTransactionId":"inv-1_voided","originalTransactionId":"inv-1"}` {
		t.Errorf("body = %s, want = original and new IDs", b)
	}
}

func TestMissingKey(t *testing.T) {
	g := New()
	c := conn
	c.Secrets = nil
	err := g.VoidTaxes(context.Background(), c, "x")
	if !errors.Is(err, integration.ErrAuthentication) {
		t.Errorf("VoidTaxes() error = %v, want = authentication", err)
	}
	if err := g.ValidateConnection(c); err == nil {
		t.Errorf("ValidateConnection() = nil, want = api_key required")
	}
	if err := g.ValidateConnection(conn); err != nil {
		t.Errorf("ValidateConnection() = %v, want = nil", err)
	}
}
