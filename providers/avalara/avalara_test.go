package avalara

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/providers/apiclient/apiclienttest"
)

var conn = integration.Connection{
	ID:             core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001"),
	OrganizationID: core.MustParseID[core.OrganizationID]("11111111-2222-3333-4444-555555555555"),
	Code:           "avalara", Provider: Provider,
	Settings: json.RawMessage(`{"account_id":"2000134479","company_code":"DEFAULT","company_id":"12345"}`),
	Secrets:  json.RawMessage(`{"license_key":"lk"}`),
}

func gateway(s *apiclienttest.Server) *Gateway { return New(WithBaseURL(s.URL), WithRateLimit(0)) }

func transaction() *integration.TaxTransaction {
	return &integration.TaxTransaction{
		ID:          "0199a000-0000-7000-8000-0000000000aa",
		IssuingDate: core.NewDate(2024, 3, 7),
		Currency:    money.MustCurrency("USD"),
		Customer: integration.TaxCustomer{
			LinkedID: "123", ExternalID: "cus_1", Name: "John Doe",
			Address: core.Address{Line1: "1 Main St", City: "Seattle", State: "WA", Zipcode: "98101", Country: "US"},
		},
		Origin: core.Address{Line1: "100 Ravine Ln", City: "Bainbridge Island", State: "WA", Zipcode: "98110", Country: "US"},
		Lines: []integration.TaxLine{
			{ItemKey: "fee-1", ItemID: "fee-1", ItemCode: "m1", AmountCents: 200, Units: money.Zero},
			{ItemKey: "fee-2", ItemID: "fee-2", ItemCode: "1", AmountCents: 200, Units: money.Zero},
		},
	}
}

func TestReportTaxes(t *testing.T) {
	// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:252
	// source: spec/services/integrations/aggregator/taxes/invoices/payloads/avalara_spec.rb:6
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /api/v2/transactions/createoradjust": {apiclienttest.Fixture("transaction_response.json")},
	})
	got, err := gateway(s).ReportTaxes(context.Background(), conn, transaction())
	if err != nil {
		t.Fatalf("ReportTaxes() error = %v", err)
	}
	req := s.Only("POST /api/v2/transactions/createoradjust")
	if req.Header.Get("Authorization") != "Basic MjAwMDEzNDQ3OTpsaw==" || req.Query.Get("$include") != "Details" {
		t.Errorf("Authorization = %q, $include = %q, want = basic account:key, Details", req.Header.Get("Authorization"), req.Query.Get("$include"))
	}
	want := `{"createTransactionModel":{"code":"0199a000-0000-7000-8000-0000000000aa","type":"SalesInvoice","companyCode":"DEFAULT","date":"2024-03-07","customerCode":"123","currencyCode":"USD","commit":true,"customerSupplierName":"John Doe",
		"addresses":{"shipFrom":{"line1":"100 Ravine Ln","city":"Bainbridge Island","region":"WA","country":"US","postalCode":"98110"},"shipTo":{"line1":"1 Main St","city":"Seattle","region":"WA","country":"US","postalCode":"98101"}},
		"lines":[{"number":"fee-1","itemCode":"m1","quantity":0,"amount":2,"ref1":"fee-1"},{"number":"fee-2","itemCode":"1","quantity":0,"amount":2,"ref1":"fee-2"}]}}`
	if !apiclienttest.EqualJSON(t, req.Body, want) {
		t.Errorf("body = %s, want = %s", req.Body, want)
	}
	if got.TransactionID != "123456789" || len(got.Lines) != 2 {
		t.Fatalf("result = %+v, want = transaction 123456789 with 2 lines", got)
	}
	l := got.Lines[0]
	if l.ItemKey != "fee-1" || l.AmountCents.String() != "200.0" && l.AmountCents.String() != "200" || *l.TaxAmountCents != 20 || len(l.Breakdown) != 2 {
		t.Fatalf("line 0 = %+v, want = 200 cents, 20 tax, 2 details", l)
	}
	if b := l.Breakdown[0]; b.Name != "WA STATE TAX" || b.Rate.String() != "0.065" || !b.TaxAmount.Equal(money.NewFromInt(13)) || b.Type != "Sales" {
		t.Errorf("breakdown 0 = %+v, want = WA STATE TAX 0.065 13", b)
	}
	if l := got.Lines[1]; *l.TaxAmountCents != 0 || !l.Breakdown[0].Rate.IsZero() {
		t.Errorf("line 1 = %+v, want = exempt", l)
	}
}

func TestReportReturn(t *testing.T) {
	// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:334
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /api/v2/transactions/createoradjust": {apiclienttest.JSON(200, `{"id":1,"lines":[]}`)},
	})
	tx := transaction()
	tx.Return = true
	for i := range tx.Lines {
		tx.Lines[i].AmountCents = -tx.Lines[i].AmountCents
	}
	if _, err := gateway(s).ReportTaxes(context.Background(), conn, tx); err != nil {
		t.Fatal(err)
	}
	var body struct {
		M struct {
			Type  string `json:"type"`
			Lines []struct {
				Amount json.Number `json:"amount"`
			} `json:"lines"`
		} `json:"createTransactionModel"`
	}
	_ = json.Unmarshal(s.Only("POST /api/v2/transactions/createoradjust").Body, &body)
	if body.M.Type != "ReturnInvoice" || body.M.Lines[0].Amount != "-2.00" {
		t.Errorf("type = %s, amount = %s, want = ReturnInvoice, -2.00", body.M.Type, body.M.Lines[0].Amount)
	}
}

func TestQuoteTaxes(t *testing.T) {
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /api/v2/transactions/create": {apiclienttest.Fixture("transaction_response.json")},
	})
	tx := transaction()
	tx.ID = ""
	got, err := gateway(s).QuoteTaxes(context.Background(), conn, tx)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(s.Only("POST /api/v2/transactions/create").Body, &body)
	if body["type"] != "SalesOrder" || body["commit"] != false || body["code"] != nil {
		t.Errorf("body = %v, want = uncommitted SalesOrder without code", body)
	}
	if got.TransactionID != "" || *got.Lines[0].TaxAmountCents != 20 {
		t.Errorf("result = %+v, want = no transaction, 20 tax", got)
	}
}

func TestVoidTaxes(t *testing.T) {
	tests := []struct {
		name string
		rep  apiclienttest.Reply
		kind integration.ErrorKind
	}{
		// source: spec/services/integrations/aggregator/taxes/invoices/void_service_spec.rb:170
		{"voided", apiclienttest.JSON(200, `{"id":1,"status":"Cancelled"}`), ""},
		// source: spec/services/invoices/provider_taxes/void_service_spec.rb:234
		{"locked", apiclienttest.Reply{Status: 400, Fixture: "locked_transaction_error.json"}, integration.KindLocked},
		{"unknown", apiclienttest.JSON(404, `{"error":{"code":"EntityNotFoundError","message":"not found"}}`), integration.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := apiclienttest.New(t, map[string][]apiclienttest.Reply{"POST /api/v2/companies/DEFAULT/transactions/inv-1/void": {tt.rep}})
			err := gateway(s).VoidTaxes(context.Background(), conn, "inv-1")
			if tt.kind == "" {
				if err != nil {
					t.Fatalf("VoidTaxes() error = %v", err)
				}
				if b := s.Only("POST /api/v2/companies/DEFAULT/transactions/inv-1/void").Body; !apiclienttest.EqualJSON(t, b, `{"code":"DocVoided"}`) {
					t.Errorf("body = %s, want = DocVoided", b)
				}
				return
			}
			pe, ok := integration.AsProviderError(err)
			if !ok || pe.Kind != tt.kind || pe.Message != "Service failure" {
				t.Errorf("VoidTaxes() error = %#v, want = %s, Service failure", err, tt.kind)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name string
		rep  apiclienttest.Reply
		kind integration.ErrorKind
		code string
	}{
		{"validation", apiclienttest.JSON(400, `{"error":{"code":"InvalidAddress","message":"The address is not deliverable."}}`), integration.KindInvalidRequest, "InvalidAddress"},
		{"authentication", apiclienttest.JSON(401, `{"error":{"code":"AuthenticationException","message":"Authentication failed."}}`), integration.KindAuthentication, "AuthenticationException"},
		{"rate limit", apiclienttest.JSON(429, `{"error":{"code":"TooManyRequests","message":"slow"}}`), integration.KindRequestLimit, "TooManyRequests"},
		// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:415
		{"bad gateway", apiclienttest.JSON(502, `<html>502 Bad Gateway</html>`), integration.KindUnavailable, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := apiclienttest.New(t, map[string][]apiclienttest.Reply{"POST /api/v2/transactions/createoradjust": {tt.rep}})
			_, err := gateway(s).ReportTaxes(context.Background(), conn, transaction())
			pe, ok := integration.AsProviderError(err)
			if !ok || pe.Kind != tt.kind || pe.Code != tt.code {
				t.Errorf("error = %#v, want = %s %s", err, tt.kind, tt.code)
			}
		})
	}
}

func TestFindCompany(t *testing.T) {
	// source: spec/services/integrations/aggregator/taxes/avalara/fetch_company_id_service_spec.rb:49
	// source: spec/services/integrations/aggregator/taxes/avalara/fetch_company_id_service_spec.rb:63
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"GET /api/v2/companies": {apiclienttest.Fixture("companies_response.json"), apiclienttest.Fixture("companies_empty_response.json")},
	})
	id, err := gateway(s).FindCompany(context.Background(), conn)
	if err != nil || id != "12345" {
		t.Fatalf("FindCompany() = %q, %v, want = 12345", id, err)
	}
	if f := s.Requests("GET /api/v2/companies")[0].Query.Get("$filter"); f != "companyCode eq 'DEFAULT'" {
		t.Errorf("$filter = %q, want = companyCode eq 'DEFAULT'", f)
	}
	if _, err := gateway(s).FindCompany(context.Background(), conn); !errors.Is(err, integration.ErrCompanyNotFound) {
		t.Errorf("FindCompany() error = %v, want = ErrCompanyNotFound", err)
	}
}

func TestContacts(t *testing.T) {
	// source: spec/services/integrations/aggregator/contacts/payloads/avalara_spec.rb:61
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /api/v2/companies/12345/customers":                                     {apiclienttest.JSON(201, `[{"id":9,"customerCode":"x"}]`)},
		"PUT /api/v2/companies/12345/customers/0199a000-0000-7000-8000-000000000002": {apiclienttest.JSON(200, `{"id":9}`)},
	})
	c := &integration.Contact{
		CustomerID: core.MustParseID[core.CustomerID]("0199a000-0000-7000-8000-000000000002"),
		Firstname:  "Jane", Lastname: "Doe", TaxNumber: "TIN",
		BillingAddress:  core.Address{Line1: "1 Main", City: "Seattle", State: "WA", Zipcode: "98101", Country: "US"},
		ShippingAddress: core.Address{City: "Tacoma"},
	}
	res, err := gateway(s).CreateContact(context.Background(), conn, c)
	if err != nil || res.ExternalID != "0199a000-0000-7000-8000-000000000002" {
		t.Fatalf("CreateContact() = %+v, %v, want = the customer code", res, err)
	}
	want := `[{"companyId":12345,"customerCode":"0199a000-0000-7000-8000-000000000002","name":"Jane Doe","line1":"1 Main","city":"Tacoma","postalCode":"98101","country":"US","region":"WA","taxpayerIdNumber":"TIN"}]`
	if b := s.Only("POST /api/v2/companies/12345/customers").Body; !apiclienttest.EqualJSON(t, b, want) {
		t.Errorf("body = %s, want = %s", b, want)
	}
	if _, err := gateway(s).UpdateContact(context.Background(), conn, c); err != nil {
		t.Errorf("UpdateContact() error = %v", err)
	}
	noCompany := conn
	noCompany.Settings = json.RawMessage(`{"account_id":"a","company_code":"DEFAULT"}`)
	if _, err := gateway(s).CreateContact(context.Background(), noCompany, c); !errors.Is(err, integration.ErrNotConnected) {
		t.Errorf("CreateContact() without company error = %v, want = ErrNotConnected", err)
	}
}

func TestContactName(t *testing.T) {
	tests := []struct {
		name, source              string
		full, first, last, wanted string
	}{
		{"name wins", "spec/services/integrations/aggregator/contacts/payloads/avalara_spec.rb:52", "Mark Doe", "John", "Doe", "Mark Doe"},
		{"first and last names", "spec/services/integrations/aggregator/contacts/payloads/avalara_spec.rb:61, 187", "", "John", "Doe", "John Doe"},
		{"no names", "spec/services/integrations/aggregator/contacts/payloads/avalara_spec.rb:71", "", "", "", ""},
		{"blank last name", "spec/services/integrations/aggregator/contacts/payloads/avalara_spec.rb:81", "", "John", "", "John"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			m, err := customer(conn, &integration.Contact{Name: tt.full, Firstname: tt.first, Lastname: tt.last})
			if err != nil || m.Name != tt.wanted || m.CompanyID != 12345 {
				t.Errorf("customer() = %q (company %d), %v, want = %q (company 12345)", m.Name, m.CompanyID, err, tt.wanted)
			}
		})
	}
}

func TestContactShippingAddress(t *testing.T) {
	billing := core.Address{Line1: "Billing 1", City: "Billing City", Zipcode: "12345", State: "Billing State", Country: "FR"}
	shipping := core.Address{Line1: "Shipping 1", City: "Shipping City", Zipcode: "67890", State: "Shipping State", Country: "US"}
	tests := []struct {
		name, source string
		shipping     core.Address
		want         core.Address
	}{
		// Go strings have no nil: empty and missing shipping fields are the same case.
		{"empty shipping fields", "spec/services/integrations/aggregator/contacts/payloads/avalara_spec.rb:115, 133", core.Address{}, billing},
		{"shipping fields", "spec/services/integrations/aggregator/contacts/payloads/avalara_spec.rb:151", shipping, shipping},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			m, err := customer(conn, &integration.Contact{Firstname: "John", Lastname: "Doe", BillingAddress: billing, ShippingAddress: tt.shipping})
			got := core.Address{Line1: m.Line1, City: m.City, Zipcode: m.PostalCode, State: m.Region, Country: m.Country}
			if err != nil || got != tt.want {
				t.Errorf("customer() address = %+v, %v, want = %+v", got, err, tt.want)
			}
		})
	}
}

func TestValidateConnection(t *testing.T) {
	g := New()
	if err := g.ValidateConnection(conn); err != nil {
		t.Errorf("ValidateConnection() = %v, want = nil", err)
	}
	if err := g.ValidateConnection(integration.Connection{}); err == nil {
		t.Errorf("ValidateConnection(empty) = nil, want = required fields")
	}
}
