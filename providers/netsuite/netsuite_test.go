package netsuite

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/providers/apiclient/apiclienttest"
)

func connection(s *apiclienttest.Server) integration.Connection {
	return integration.Connection{
		ID:             core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001"),
		OrganizationID: core.MustParseID[core.OrganizationID]("11111111-2222-3333-4444-555555555555"),
		Code:           "netsuite", Provider: Provider,
		Settings: json.RawMessage(`{"account_id":"1234567-sb1","client_id":"ck","token_id":"tk","script_endpoint_url":"` +
			s.URL + `/app/site/hosting/restlet.nl?script=12&deploy=1"}`),
		Secrets: json.RawMessage(`{"client_secret":"cs","token_secret":"ts"}`),
	}
}

func gateway(s *apiclienttest.Server) *Gateway {
	return New(WithRateLimit(0), WithSuiteTalkURL(func(string) string { return s.URL }),
		WithSigningClock(func() time.Time { return time.Unix(1700000000, 0) }, func() string { return "nonce" }))
}

func ptr(s string) *string { return &s }

func ext(id, account string) *integration.ExternalItem {
	return &integration.ExternalItem{ExternalID: id, ExternalAccountCode: ptr(account)}
}

var (
	feeSub    = core.MustParseID[core.FeeID]("0199a000-0000-7000-8000-0000000000f1")
	feeCommit = core.MustParseID[core.FeeID]("0199a000-0000-7000-8000-0000000000f2")
	feeCharge = core.MustParseID[core.FeeID]("0199a000-0000-7000-8000-0000000000f3")
	feeZero   = core.MustParseID[core.FeeID]("0199a000-0000-7000-8000-0000000000f4")
)

// invoice is the invoice of lago
// set up in spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb
// (lines 12-24), with its mappings (lines 34-107) and fees (lines 108-145).
func invoice() *integration.AccountingInvoice {
	from := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 6, 30, 23, 59, 59, 0, time.UTC)
	zero := money.NewFromInt(0)
	return &integration.AccountingInvoice{
		ID: core.MustParseID[core.InvoiceID]("0199a000-0000-7000-8000-0000000000aa"), Number: "ORG-1-001-001",
		ExternalCustomerID: "cus-ext", IssuingDate: core.NewDate(2024, 7, 8), PaymentDueDate: core.NewDate(2024, 7, 8),
		Currency: money.MustCurrency("EUR"), URL: "https://app.example/org/customer/c1/invoice/i1/overview", PlanCodes: []string{"plan_code"},
		TaxesAmountCents: 200, TaxesRate: zero, CouponsAmountCents: 2000, PrepaidCreditAmountCents: 4000,
		ProgressiveBillingCreditAmountCents: 100, CreditNotesAmountCents: 6000,
		CouponNames: []string{"coupon"}, ProgressiveBillingNames: []string{"INV-1"}, CreditNoteNames: []string{"INV-2"},
		Fees: []integration.AccountingFee{
			{
				ID: feeSub, Type: core.FeeTypeSubscription, Item: ext("3", "33"), ItemName: "Plan", ItemSource: "plan_code",
				Units: zero, PreciseUnitAmount: zero, AmountCents: 10_000, TaxesAmountCents: 200, TaxesRate: zero, From: from, To: to,
			},
			{
				ID: feeCommit, Type: core.FeeTypeCommitment, Item: ext("4", "44"), ItemName: "Minimum commitment", ItemSource: "plan_code",
				Units: zero, PreciseUnitAmount: zero, AmountCents: 200, TaxesAmountCents: 2, TaxesRate: zero, From: from, To: to,
			},
			{
				ID: feeCharge, Type: core.FeeTypeCharge, Item: ext("m2", "m22"), ItemName: "Metric", ItemSource: "plan_code",
				Units: money.NewFromInt(2), PreciseUnitAmount: money.MustParse("4.12121212123337777"), AmountCents: 200,
				TaxesAmountCents: 2, TaxesRate: zero, From: from, To: to,
			},
			{ID: feeZero, Type: core.FeeTypeCharge, Item: ext("m2", "m22"), Units: zero, PreciseUnitAmount: zero, AmountCents: 0, TaxesRate: zero},
		},
		Items: integration.DocumentItems{
			Coupon: ext("2", "22"), PrepaidCredit: ext("6", "66"), CreditNote: ext("1", "11"),
			Tax: &integration.TaxItem{Nexus: "some_nexus", Type: "some_type", Code: "some_code"},
		},
		Document: json.RawMessage(`{"invoice":{"lago_id":"i1"}}`),
	}
}

func TestSyncInvoice(t *testing.T) {
	// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:422
	// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:416
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /app/site/hosting/restlet.nl": {apiclienttest.JSON(200, `"456"`)},
	})
	id, err := gateway(s).SyncInvoice(context.Background(), connection(s), invoice())
	if err != nil || id != "456" {
		t.Fatalf("SyncInvoice() = %q, %v, want = 456", id, err)
	}
	req := s.Only("POST /app/site/hosting/restlet.nl")
	want := `{"type":"invoice","isDynamic":true,
		"columns":{"tranid":"ORG-1-001-001","custbody_ava_disable_tax_calculation":true,"custbody_lago_invoice_link":"https://app.example/org/customer/c1/invoice/i1/overview",
			"trandate":"7/8/2024","duedate":"7/8/2024","taxdetailsoverride":true,"custbody_lago_id":"0199a000-0000-7000-8000-0000000000aa","entity":"cus-ext",
			"lago_plan_codes":"plan_code","nexus":"some_nexus","taxregoverride":true},
		"lines":[{"sublistId":"item","lineItems":[
			{"item":"3","account":"33","quantity":"0","rate":"0","amount":100.0,"taxdetailsreference":"` + feeSub.String() + `","custcol_service_period_date_from":"6/1/2024","custcol_service_period_date_to":"6/30/2024","description":"Plan","item_source":"plan_code"},
			{"item":"4","account":"44","quantity":"0","rate":"0","amount":2.0,"taxdetailsreference":"` + feeCommit.String() + `","custcol_service_period_date_from":"6/1/2024","custcol_service_period_date_to":"6/30/2024","description":"Minimum commitment","item_source":"plan_code"},
			{"item":"m2","account":"m22","quantity":"2","rate":"4.1212121212334","amount":2.0,"taxdetailsreference":"` + feeCharge.String() + `","custcol_service_period_date_from":"6/1/2024","custcol_service_period_date_to":"6/30/2024","description":"Metric","item_source":"plan_code"},
			{"item":"2","account":"22","quantity":1,"rate":-20.0,"taxdetailsreference":"coupon_item","description":"coupon","item_source":"coupons"},
			{"item":"6","account":"66","quantity":1,"rate":-40.0,"taxdetailsreference":"credit_item","description":"Prepaid credits","item_source":"prepaid_credits"},
			{"item":"6","account":"66","quantity":1,"rate":-1.0,"taxdetailsreference":"credit_item_progressive_billing","description":"INV-1","item_source":"progressive_billing_credits"},
			{"item":"1","account":"11","quantity":1,"rate":-60.0,"taxdetailsreference":"credit_note_item","description":"INV-2","item_source":"credit_note_credits"}]}],
		"options":{"ignoreMandatoryFields":false,"fullInvoicePayload":{"invoice_payload":{"invoice":{"lago_id":"i1"}}}},
		"taxdetails":[{"sublistId":"taxdetails","lineItems":[
			{"taxdetailsreference":"` + feeSub.String() + `","taxamount":2.0,"taxbasis":1,"taxrate":0.0,"taxtype":"some_type","taxcode":"some_code"},
			{"taxdetailsreference":"` + feeCommit.String() + `","taxamount":0.02,"taxbasis":1,"taxrate":0.0,"taxtype":"some_type","taxcode":"some_code"},
			{"taxdetailsreference":"` + feeCharge.String() + `","taxamount":0.02,"taxbasis":1,"taxrate":0.0,"taxtype":"some_type","taxcode":"some_code"},
			{"taxbasis":1,"taxamount":-0.04,"taxrate":0.0,"taxtype":"some_type","taxcode":"some_code","taxdetailsreference":"coupon_item"},
			{"taxbasis":1,"taxamount":0,"taxrate":0.0,"taxtype":"some_type","taxcode":"some_code","taxdetailsreference":"credit_item"},
			{"taxbasis":1,"taxamount":0,"taxrate":0.0,"taxtype":"some_type","taxcode":"some_code","taxdetailsreference":"credit_item_progressive_billing"},
			{"taxbasis":1,"taxamount":0,"taxrate":0.0,"taxtype":"some_type","taxcode":"some_code","taxdetailsreference":"credit_note_item"}]}]}`
	if !apiclienttest.EqualJSON(t, req.Body, want) {
		t.Errorf("body = %s, want = %s", req.Body, want)
	}
	if req.Query.Get("script") != "12" || !strings.HasPrefix(req.Header.Get("Authorization"), `OAuth realm="1234567_SB1"`) {
		t.Errorf("query = %v, Authorization = %q, want = script 12, realm 1234567_SB1", req.Query, req.Header.Get("Authorization"))
	}
}

func TestInvoiceBodyMappings(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*integration.AccountingInvoice)
		check  func(t *testing.T, body object)
		errNil bool
	}{
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:352
			name: "currency mapped", errNil: true,
			edit: func(inv *integration.AccountingInvoice) {
				inv.Items.Currencies = map[string]string{"EUR": "312", "USD": "7"}
			},
			check: func(t *testing.T, body object) {
				if got := body["columns"].(object)["currency"]; got != "312" {
					t.Errorf("currency = %v, want = 312", got)
				}
			},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:365
			name: "currency not mapped", errNil: true,
			edit: func(inv *integration.AccountingInvoice) {
				inv.Items.Currencies = map[string]string{"USD": "7", "EUR": ""}
			},
			check: func(t *testing.T, body object) {
				if _, ok := body["columns"].(object)["currency"]; ok {
					t.Errorf("columns = %v, want = no currency", body["columns"])
				}
			},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:347
			name: "no currency mapping", errNil: true,
			edit: func(inv *integration.AccountingInvoice) { inv.Items.Currencies = nil },
			check: func(t *testing.T, body object) {
				if _, ok := body["columns"].(object)["currency"]; ok {
					t.Errorf("columns = %v, want = no currency", body["columns"])
				}
			},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:378
			name: "currency mapping without the invoice currency", errNil: true,
			edit: func(inv *integration.AccountingInvoice) {
				inv.Items.Currencies = map[string]string{"USD": "7", "GBP": "312"}
			},
			check: func(t *testing.T, body object) {
				if _, ok := body["columns"].(object)["currency"]; ok {
					t.Errorf("columns = %v, want = no currency", body["columns"])
				}
			},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:412, 416
			name: "tax item without nexus", errNil: true,
			edit: func(inv *integration.AccountingInvoice) {
				inv.Items.Tax = &integration.TaxItem{}
			},
			check: func(t *testing.T, body object) {
				if _, ok := body["taxdetails"]; ok {
					t.Errorf("body has taxdetails, want = none")
				}
				if _, ok := body["columns"].(object)["nexus"]; ok {
					t.Errorf("columns = %v, want = no nexus", body["columns"])
				}
			},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:542, 546
			name: "tax item with only a nexus", errNil: true,
			edit: func(inv *integration.AccountingInvoice) { inv.Items.Tax = &integration.TaxItem{Nexus: "some_nexus"} },
			check: func(t *testing.T, body object) {
				if _, ok := body["taxdetails"]; ok {
					t.Errorf("body has taxdetails, want = none")
				}
				if got := body["columns"].(object)["nexus"]; got != "some_nexus" {
					t.Errorf("nexus = %v, want = some_nexus", got)
				}
			},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:553
			name: "tax item incomplete", errNil: true,
			edit: func(inv *integration.AccountingInvoice) { inv.Items.Tax = &integration.TaxItem{} },
			check: func(t *testing.T, body object) {
				if _, ok := body["taxdetails"]; ok {
					t.Errorf("body has taxdetails, want = none")
				}
				if _, ok := body["columns"].(object)["nexus"]; ok {
					t.Errorf("columns = %v, want = no nexus", body["columns"])
				}
			},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:577
			name: "quantity over the NetSuite limit", errNil: true,
			edit: func(inv *integration.AccountingInvoice) { inv.Fees[2].Units = money.NewFromInt(20_000_000_000) },
			check: func(t *testing.T, body object) {
				l := body["lines"].([]object)[0]["lineItems"].([]object)[2]
				if l["quantity"] != 1 || l["rate"] != json.Number("2.0") {
					t.Errorf("line = %v, want = quantity 1, rate 2.0", l)
				}
			},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:489
			name: "unmapped fee",
			edit: func(inv *integration.AccountingInvoice) { inv.Fees[1].Item = nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := invoice()
			tt.edit(inv)
			body, err := invoiceBody(inv)
			if !tt.errNil {
				if !errors.Is(err, integration.ErrNoMapping) {
					t.Fatalf("invoiceBody() error = %v, want = ErrNoMapping", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("invoiceBody() error = %v", err)
			}
			tt.check(t, body)
		})
	}
}

func TestLimitedRate(t *testing.T) {
	// source: spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:569 (the rate at line 198)
	tests := []struct{ in, want string }{
		{"4.12121212123337777", "4.1212121212334"},
		{"0.5", "0.5"},
		{"1234.5", "1234.5"},
		{"12.3456789012345678", "12.3456789012346"},
		{"-4.12121212123337777", "-4.121212121233"},
		{"0", "0"},
	}
	for _, tt := range tests {
		if got := limitedRate(money.MustParse(tt.in)).String(); got != tt.want {
			t.Errorf("limitedRate(%s) = %s, want = %s", tt.in, got, tt.want)
		}
	}
}

func TestSyncCreditNote(t *testing.T) {
	// source: spec/services/integrations/aggregator/credit_notes/payloads/netsuite_spec.rb:9
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /app/site/hosting/restlet.nl": {apiclienttest.JSON(200, `"789"`)},
	})
	cn := &integration.AccountingCreditNote{
		ID: core.MustParseID[core.CreditNoteID]("0199a000-0000-7000-8000-0000000000cc"), Number: "CN-1", ExternalCustomerID: "cus-ext",
		Currency: money.MustCurrency("EUR"), TaxesAmountCents: 0, TaxesRate: money.NewFromInt(0),
		Items: []integration.AccountingCreditNoteItem{
			{ID: "item-1", AmountCents: 190, Fee: integration.AccountingFee{Item: ext("ao", "ao-acc"), ItemName: "Add-on", TaxesRate: money.NewFromInt(0)}},
			{ID: "item-2", AmountCents: 2500, Fee: integration.AccountingFee{Item: ext("fc-ext-id", "fc-account"), ItemName: "Fixed Charge Add-on", TaxesRate: money.NewFromInt(0)}},
		},
		Document: json.RawMessage(`{"credit_note":{"lago_id":"cn1"}}`),
	}
	id, err := gateway(s).SyncCreditNote(context.Background(), connection(s), cn)
	if err != nil || id != "789" {
		t.Fatalf("SyncCreditNote() = %q, %v, want = 789", id, err)
	}
	want := `{"type":"creditmemo","isDynamic":true,
		"columns":{"tranid":"CN-1","entity":"cus-ext","taxregoverride":true,"taxdetailsoverride":true,"otherrefnum":"CN-1",
			"custbody_ava_disable_tax_calculation":true,"custbody_lago_id":"0199a000-0000-7000-8000-0000000000cc","tranId":"0199a000-0000-7000-8000-0000000000cc"},
		"lines":[{"sublistId":"item","lineItems":[
			{"item":"ao","account":"ao-acc","quantity":1,"rate":1.9,"taxdetailsreference":"item-1","description":"Add-on"},
			{"item":"fc-ext-id","account":"fc-account","quantity":1,"rate":25.0,"taxdetailsreference":"item-2","description":"Fixed Charge Add-on"}]}],
		"options":{"ignoreMandatoryFields":false,"fullCreditNotePayload":{"credit_note_payload":{"credit_note":{"lago_id":"cn1"}}}}}`
	if req := s.Only("POST /app/site/hosting/restlet.nl"); !apiclienttest.EqualJSON(t, req.Body, want) {
		t.Errorf("body = %s, want = %s", req.Body, want)
	}
}

func TestSyncPayment(t *testing.T) {
	// source: spec/services/integrations/aggregator/payments/payloads/netsuite_spec.rb:58
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /app/site/hosting/restlet.nl": {apiclienttest.JSON(200, `"999"`)},
	})
	p := &integration.AccountingPayment{InvoiceExternalID: "inv-ext", ExternalCustomerID: "cus-ext", AmountCents: 10_000, Currency: money.MustCurrency("EUR")}
	if id, err := gateway(s).SyncPayment(context.Background(), connection(s), p); err != nil || id != "999" {
		t.Fatalf("SyncPayment() = %q, %v, want = 999", id, err)
	}
	want := `{"isDynamic":true,"columns":{"customer":"cus-ext","payment":100.0},"options":{"ignoreMandatoryFields":false},"type":"customerpayment",
		"lines":[{"lineItems":[{"amount":100.0,"apply":true,"doc":"inv-ext"}],"sublistId":"apply"}]}`
	if req := s.Only("POST /app/site/hosting/restlet.nl"); !apiclienttest.EqualJSON(t, req.Body, want) {
		t.Errorf("body = %s, want = %s", req.Body, want)
	}
}

func contact() *integration.Contact {
	return &integration.Contact{
		CustomerID: core.MustParseID[core.CustomerID]("0199a000-0000-7000-8000-0000000000c1"), ExternalID: "cust-1",
		Name: "Acme", Email: "a@acme.test, b@acme.test", Phone: "123", SubsidiaryID: "2", Link: "https://app.example/org/customer/c1",
		BillingAddress: core.Address{Line1: "1 Main St", City: "Paris", Zipcode: "75001", Country: "FR"},
	}
}

func TestCreateContact(t *testing.T) {
	// source: spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:145
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /app/site/hosting/restlet.nl": {apiclienttest.JSON(200, `"1"`)},
	})
	got, err := gateway(s).CreateContact(context.Background(), connection(s), contact())
	if err != nil || got.ExternalID != "1" {
		t.Fatalf("CreateContact() = %+v, %v, want = 1", got, err)
	}
	want := `{"type":"customer","isDynamic":true,
		"columns":{"isperson":"F","subsidiary":"2","custentity_lago_id":"0199a000-0000-7000-8000-0000000000c1","custentity_lago_sf_id":null,
			"custentity_lago_customer_link":"https://app.example/org/customer/c1","email":"a@acme.test","phone":"123","entityid":"cust-1","autoname":false,"companyname":"Acme"},
		"options":{"ignoreMandatoryFields":false},
		"lines":[{"lineItems":[{"defaultshipping":true,"defaultbilling":true,"subObjectId":"addressbookaddress",
			"subObject":{"addr1":"1 Main St","addr2":null,"city":"Paris","zip":"75001","state":null,"country":"FR"}}],"sublistId":"addressbook"}]}`
	if req := s.Only("POST /app/site/hosting/restlet.nl"); !apiclienttest.EqualJSON(t, req.Body, want) {
		t.Errorf("body = %s, want = %s", req.Body, want)
	}
}

func TestUpdateContactIndividual(t *testing.T) {
	// source: spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:344
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"PUT /app/site/hosting/restlet.nl": {apiclienttest.JSON(200, `"1"`)},
	})
	c := contact()
	c.LinkedID, c.Individual, c.Name, c.Firstname, c.Lastname = "1", true, "", "John", strings.Repeat("x", 40)
	if _, err := gateway(s).UpdateContact(context.Background(), connection(s), c); err != nil {
		t.Fatalf("UpdateContact() error = %v", err)
	}
	want := `{"type":"customer","recordId":"1",
		"columns":{"isperson":"T","subsidiary":"2","custentity_lago_sf_id":null,"custentity_lago_customer_link":"https://app.example/org/customer/c1",
			"email":"a@acme.test","phone":"123","entityid":"cust-1","autoname":false,"firstname":"John","lastname":"` + strings.Repeat("x", 30) + `"},
		"options":{"isDynamic":false}}`
	if req := s.Only("PUT /app/site/hosting/restlet.nl"); !apiclienttest.EqualJSON(t, req.Body, want) {
		t.Errorf("body = %s, want = %s", req.Body, want)
	}
}

func TestFindInvoice(t *testing.T) {
	// source: spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:33
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /services/rest/query/v1/suiteql": {apiclienttest.JSON(200, `{"items":[{"id":"321"}],"hasMore":false}`)},
	})
	id, found, err := gateway(s).FindInvoice(context.Background(), connection(s), "O'1")
	if err != nil || !found || id != "321" {
		t.Fatalf("FindInvoice() = %q, %v, %v, want = 321 found", id, found, err)
	}
	req := s.Only("POST /services/rest/query/v1/suiteql")
	if !apiclienttest.EqualJSON(t, req.Body, `{"q":"SELECT id FROM transaction WHERE type = 'CustInvc' AND tranid = 'O''1'"}`) || req.Header.Get("Prefer") != "transient" {
		t.Errorf("body = %s, Prefer = %q, want = escaped tranid query, transient", req.Body, req.Header.Get("Prefer"))
	}
}

func TestSubsidiaries(t *testing.T) {
	// source: spec/services/integrations/aggregator/subsidiaries_service_spec.rb:35
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /services/rest/query/v1/suiteql": {apiclienttest.JSON(200, `{"items":[{"id":"1","name":"Holo, Inc."},{"id":"2","name":"Holo, Inc. : Te SRL"}],"hasMore":false}`)},
	})
	got, err := gateway(s).Subsidiaries(context.Background(), connection(s))
	if err != nil || len(got) != 2 || got[0].ID != "1" || got[0].Name != "Holo, Inc." || got[1].ID != "2" {
		t.Fatalf("Subsidiaries() = %+v, %v, want = 1 Holo, Inc. and 2", got, err)
	}
	req := s.Only("POST /services/rest/query/v1/suiteql")
	if !apiclienttest.EqualJSON(t, req.Body, `{"q":"SELECT id, name FROM subsidiary WHERE isinactive = 'F' ORDER BY id"}`) {
		t.Errorf("body = %s, want = the active subsidiaries query", req.Body)
	}
}

func TestFetchItemsPages(t *testing.T) {
	// source: spec/services/integrations/aggregator/items_service_spec.rb:34
	// The NetSuite id is the external ID: source: spec/services/integrations/aggregator/items_service_spec.rb:27
	// source: spec/models/integrations/base_integration_spec.rb:116
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /services/rest/query/v1/suiteql": {
			apiclienttest.JSON(200, `{"items":[{"id":"755","itemid":"Ice"}],"hasMore":true}`),
			apiclienttest.JSON(200, `{"items":[{"id":"745","itemid":"Coffee"}],"hasMore":false}`),
		},
	})
	items, err := gateway(s).FetchItems(context.Background(), connection(s))
	if err != nil || len(items) != 2 || items[1].ID != "745" || items[1].Name != "Coffee" {
		t.Fatalf("FetchItems() = %+v, %v, want = 2 items", items, err)
	}
	if reqs := s.Requests("POST /services/rest/query/v1/suiteql"); reqs[1].Query.Get("offset") != "1000" {
		t.Errorf("offset = %q, want = 1000", reqs[1].Query.Get("offset"))
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		kind     integration.ErrorKind
		code     string
		message  string
		matchErr error
	}{
		{
			// source: spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:187
			// source: spec/services/integrations/aggregator/base_service_spec.rb:16
			name: "request limit", status: 400, body: `{"error":{"code":"SSS_REQUEST_LIMIT_EXCEEDED","message":"Request limit exceeded"}}`,
			kind: integration.KindRequestLimit, code: "SSS_REQUEST_LIMIT_EXCEEDED", message: "Request limit exceeded", matchErr: integration.ErrRequestLimit,
		},
		{
			// source: spec/services/integrations/aggregator/contacts/create_service_spec.rb:304 (the case at line 277)
			name: "invalid login", status: 500, body: `{"error":{"code":"INVALID_LOGIN_ATTEMPT","message":"Invalid login attempt."}}`,
			kind: integration.KindAuthentication, code: CodeInvalidLoginAttempt, message: "Invalid login attempt.", matchErr: integration.ErrAuthentication,
		},
		{
			// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:470
			// Not a request limit: source: spec/services/integrations/aggregator/base_service_spec.rb:24
			name: "script error", status: 400, body: `{"type":"error.SuiteScriptError","error":{"payload":{"message":"Invalid item"}}}`,
			kind: integration.KindInvalidRequest, code: "error.SuiteScriptError", message: "Invalid item", matchErr: integration.ErrRejected,
		},
		{
			// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:456
			name: "server error", status: 502, body: `bad gateway`,
			kind: integration.KindUnavailable, code: codeUnexpected, message: "bad gateway", matchErr: integration.ErrRetryable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := apiclienttest.New(t, map[string][]apiclienttest.Reply{"POST /app/site/hosting/restlet.nl": {apiclienttest.JSON(tt.status, tt.body)}})
			_, err := gateway(s).SyncPayment(context.Background(), connection(s), &integration.AccountingPayment{Currency: money.MustCurrency("EUR")})
			pe, ok := integration.AsProviderError(err)
			if !ok {
				t.Fatalf("error = %v, want = ProviderError", err)
			}
			if pe.Kind != tt.kind || pe.Code != tt.code || pe.Message != tt.message || pe.HTTPStatus != tt.status || !errors.Is(err, tt.matchErr) {
				t.Errorf("error = %+v, want = %s %s %q %d", pe, tt.kind, tt.code, tt.message, tt.status)
			}
		})
	}
}

func TestNotConnected(t *testing.T) {
	conn := integration.Connection{Provider: Provider, Settings: json.RawMessage(`{"account_id":"1"}`)}
	if _, err := New().SyncPayment(context.Background(), conn, &integration.AccountingPayment{}); !errors.Is(err, integration.ErrNotConnected) {
		t.Errorf("error = %v, want = ErrNotConnected", err)
	}
}

func TestSignature(t *testing.T) {
	// RFC 5849 §3.4: the signature base string and key, with HMAC-SHA256.
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{"POST /app/site/hosting/restlet.nl": {apiclienttest.JSON(200, `"1"`)}})
	if _, err := gateway(s).SyncPayment(context.Background(), connection(s), &integration.AccountingPayment{Currency: money.MustCurrency("EUR")}); err != nil {
		t.Fatalf("SyncPayment() error = %v", err)
	}
	req := s.Only("POST /app/site/hosting/restlet.nl")
	params := "deploy=1&oauth_consumer_key=ck&oauth_nonce=nonce&oauth_signature_method=HMAC-SHA256&oauth_timestamp=1700000000&oauth_token=tk&oauth_version=1.0&script=12"
	base := "POST&" + url.QueryEscape(strings.ToLower(s.URL)+"/app/site/hosting/restlet.nl") + "&" + url.QueryEscape(params)
	mac := hmac.New(sha256.New, []byte("cs&ts"))
	mac.Write([]byte(base))
	sig := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	if h := req.Header.Get("Authorization"); !strings.Contains(h, `oauth_signature="`+sig+`"`) {
		t.Errorf("Authorization = %q, want = signature %s", h, sig)
	}
}

func TestContactAddressLines(t *testing.T) {
	billing := core.Address{Line1: "1 Main St", Line2: "Floor 2", City: "Paris", Zipcode: "75001", State: "IDF", Country: "FR"}
	shipping := core.Address{Line1: "9 Dock Rd", City: "Lyon", Zipcode: "69001", Country: "FR"}
	sub := func(a core.Address) object {
		return object{
			"addr1": nullable(a.Line1), "addr2": nullable(a.Line2), "city": nullable(a.City),
			"zip": nullable(a.Zipcode), "state": nullable(a.State), "country": nullable(a.Country),
		}
	}
	item := func(shipping, billing bool, a core.Address) object {
		return object{"defaultshipping": shipping, "defaultbilling": billing, "subObjectId": "addressbookaddress", "subObject": sub(a)}
	}
	tests := []struct {
		name, source      string
		billing, shipping core.Address
		want              []object
	}{
		{
			"shipping differs", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:84", billing, shipping,
			[]object{item(false, true, billing), item(true, false, shipping)},
		},
		{"shipping is the billing address", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:115", billing, billing, []object{item(true, true, billing)}},
		{"no address", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:176", core.Address{}, core.Address{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			s := apiclienttest.New(t, map[string][]apiclienttest.Reply{"POST /app/site/hosting/restlet.nl": {apiclienttest.JSON(200, `"1"`)}})
			c := contact()
			c.BillingAddress, c.ShippingAddress = tt.billing, tt.shipping
			if _, err := gateway(s).CreateContact(context.Background(), connection(s), c); err != nil {
				t.Fatal(err)
			}
			var body struct {
				Lines []struct {
					LineItems []object `json:"lineItems"`
				} `json:"lines"`
			}
			if err := json.Unmarshal(s.Only("POST /app/site/hosting/restlet.nl").Body, &body); err != nil {
				t.Fatal(err)
			}
			var got []object
			if len(body.Lines) > 0 {
				got = body.Lines[0].LineItems
			}
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(tt.want)
			if string(gb) != string(wb) {
				t.Errorf("line items: got = %s, want = %s", gb, wb)
			}
		})
	}
}

func TestContactColumns(t *testing.T) {
	t.Run("company update", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:367
		columns := object{}
		names(columns, &integration.Contact{Name: "Acme", Firstname: "John", Lastname: "Doe"})
		if len(columns) != 1 || columns["companyname"] != "Acme" {
			t.Errorf("names: got = %v, want = companyname only", columns)
		}
	})
	for _, tt := range []struct {
		name, source string
		c            integration.Contact
		want         object
		person       string
	}{
		{"no type", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:423, 476", integration.Contact{Name: "Gavin"}, object{"companyname": "Gavin"}, "F"},
		// A company and a customer without a type are both not Individual.
		{"company", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:434, 484", integration.Contact{Name: "Hooli"}, object{"companyname": "Hooli"}, "F"},
		{
			"individual with a name", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:449, 492",
			integration.Contact{Name: "Gavin", Firstname: "Gavin", Lastname: "Belson", Individual: true},
			object{"companyname": "Gavin", "firstname": "Gavin", "lastname": "Belson"},
			"T",
		},
		{
			"individual without a name", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:461",
			integration.Contact{Firstname: "Gavin", Lastname: "Belson", Individual: true},
			object{"firstname": "Gavin", "lastname": "Belson"},
			"T",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			columns := object{}
			names(columns, &tt.c)
			gb, _ := json.Marshal(columns)
			wb, _ := json.Marshal(tt.want)
			if string(gb) != string(wb) {
				t.Errorf("names: got = %s, want = %s", gb, wb)
			}
			if got := isPerson(&tt.c); got != tt.person {
				t.Errorf("isperson: got = %s, want = %s", got, tt.person)
			}
		})
	}
	for _, tt := range []struct {
		name, source, list, want string
	}{
		{"no email", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:381, 389", "", ""},
		{"one email", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:397", "a@acme.test", "a@acme.test"},
		{"several emails", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:407", "a@acme.test,b@acme.test", "a@acme.test"},
		{"no phone", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:506, 514", "", ""},
		{"one phone", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:522", "+33 1 23", "+33 1 23"},
		{"several phones", "spec/services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:532", "+33 1 23,+33 4 56", "+33 1 23"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			c := integration.Contact{Email: tt.list, Phone: tt.list}
			if strings.Contains(tt.name, "email") && nullable(c.FirstEmail()) != nullable(tt.want) {
				t.Errorf("email: got = %v, want = %v", nullable(c.FirstEmail()), nullable(tt.want))
			}
			if strings.Contains(tt.name, "phone") && nullable(c.FirstPhone()) != nullable(tt.want) {
				t.Errorf("phone: got = %v, want = %v", nullable(c.FirstPhone()), nullable(tt.want))
			}
		})
	}
}

func TestTaxItemComplete(t *testing.T) {
	tests := []struct {
		name, source string
		item         integration.TaxItem
		want         bool
	}{
		{"complete", "spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:763", integration.TaxItem{Nexus: "n", Type: "t", Code: "c"}, true},
		{"no nexus", "spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:773", integration.TaxItem{Type: "t", Code: "c"}, false},
		{"no type", "spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:783", integration.TaxItem{Nexus: "n", Code: "c"}, false},
		{"no code", "spec/services/integrations/aggregator/invoices/payloads/netsuite_spec.rb:793", integration.TaxItem{Nexus: "n", Type: "t"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			if got := tt.item.Complete(); got != tt.want {
				t.Errorf("Complete() = %v, want = %v", got, tt.want)
			}
		})
	}
}

// The invoice record keeps the positive fees, or every fee when none is
// positive, in the order of the document (oldest first).
func TestPositiveFees(t *testing.T) {
	fee := func(n int64) integration.AccountingFee { return integration.AccountingFee{AmountCents: n} }
	tests := []struct {
		name, source string
		fees, want   []integration.AccountingFee
	}{
		{"positive", "spec/services/integrations/aggregator/invoices/payloads/base_payload_spec.rb:25", []integration.AccountingFee{fee(2000), fee(1000)}, []integration.AccountingFee{fee(2000), fee(1000)}},
		{"none positive", "spec/services/integrations/aggregator/invoices/payloads/base_payload_spec.rb:39", []integration.AccountingFee{fee(-1000), fee(0)}, []integration.AccountingFee{fee(-1000), fee(0)}},
		{"positive and zero", "spec/services/integrations/aggregator/invoices/payloads/base_payload_spec.rb:54", []integration.AccountingFee{fee(200), fee(100), fee(0)}, []integration.AccountingFee{fee(200), fee(100)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			got := positiveFees(tt.fees)
			if len(got) != len(tt.want) {
				t.Fatalf("fees: got = %d, want = %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].AmountCents != tt.want[i].AmountCents {
					t.Errorf("fee %d: got = %d, want = %d", i, got[i].AmountCents, tt.want[i].AmountCents)
				}
			}
		})
	}
}
