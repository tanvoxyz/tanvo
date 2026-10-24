package xero

import (
	"context"
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

var now = time.Date(2024, 7, 8, 12, 0, 0, 0, time.UTC)

func connection(expiry time.Time) integration.Connection {
	return integration.Connection{
		ID:             core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000002"),
		OrganizationID: core.MustParseID[core.OrganizationID]("11111111-2222-3333-4444-555555555555"),
		Code:           "xero", Provider: Provider,
		Settings: json.RawMessage(`{"tenant_id":"tenant-1"}`),
		Secrets: json.RawMessage(`{"access_token":"at","refresh_token":"rt","token_expires_at":"` +
			expiry.Format(time.RFC3339) + `"}`),
	}
}

func live() integration.Connection { return connection(now.Add(time.Hour)) }

type store struct {
	got []integration.ConnectionUpdate
}

func (s *store) UpdateConnection(_ context.Context, _ integration.Connection, u integration.ConnectionUpdate) error {
	s.got = append(s.got, u)
	return nil
}

func gateway(s *apiclienttest.Server, st integration.ConnectionStore) *Gateway {
	return New(WithRateLimit(0, 0), WithBaseURLs(s.URL, s.URL, s.URL), WithClock(func() time.Time { return now }),
		WithOAuthClient(OAuthClient{ClientID: "cid", ClientSecret: "csecret"}), WithConnectionStore(st))
}

func ptr(s string) *string { return &s }

func ext(id, account string) *integration.ExternalItem {
	return &integration.ExternalItem{ExternalID: id, ExternalAccountCode: ptr(account)}
}

func fee(t core.FeeType, name, item string, units, unit string, amount, taxes int64) integration.AccountingFee {
	return integration.AccountingFee{
		Type: t, InvoiceName: name, Item: ext(item, item+"-acc"),
		Units: money.MustParse(units), PreciseUnitAmount: money.MustParse(unit), AmountCents: amount, TaxesAmountCents: taxes,
	}
}

// sharedInvoice is the invoice of lago spec/support/shared_examples
// "an integration payload" as invoices/payloads/xero_spec.rb:10-27 builds
// it.
func sharedInvoice() *integration.AccountingInvoice {
	return &integration.AccountingInvoice{
		Number: "INV-1", ExternalCustomerID: "contact-1", IssuingDate: core.NewDate(2024, 7, 8), PaymentDueDate: core.NewDate(2024, 7, 8),
		Currency: money.MustCurrency("EUR"), PurchaseOrderNumber: "PO-123",
		TaxesAmountCents: 300, CouponsAmountCents: 200, PrepaidCreditAmountCents: 300,
		ProgressiveBillingCreditAmountCents: 100, CreditNotesAmountCents: 500,
		Fees: []integration.AccountingFee{
			fee(core.FeeTypeAddOn, "Add-on Fee", "ao", "2", "100", 20_000, 2),
			fee(core.FeeTypeFixedCharge, "Fixed Charge Fee", "fc", "6", "25", 15_000, 2),
			fee(core.FeeTypeCharge, "Standard Charge Fee", "bm", "3", "100", 30_000, 2),
			fee(core.FeeTypeCommitment, "Minimum Commitment Fee", "mc", "4", "100", 40_000, 2),
			fee(core.FeeTypeSubscription, "Plan", "sub", "5", "100", 50_000, 2),
		},
		Items: integration.DocumentItems{Coupon: ext("cp", "cp-acc"), PrepaidCredit: ext("pc", "pc-acc"), CreditNote: ext("cn", "cn-acc")},
	}
}

func TestSyncInvoice(t *testing.T) {
	// source: spec/services/integrations/aggregator/invoices/payloads/xero_spec.rb:9
	// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:369
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /api.xro/2.0/Invoices": {apiclienttest.JSON(200, `{"Invoices":[{"InvoiceID":"xinv-1","HasErrors":false}]}`)},
	})
	id, err := gateway(s, nil).SyncInvoice(context.Background(), live(), sharedInvoice())
	if err != nil || id != "xinv-1" {
		t.Fatalf("SyncInvoice() = %q, %v, want = xinv-1", id, err)
	}
	req := s.Only("POST /api.xro/2.0/Invoices")
	if req.Query.Get("summarizeErrors") != "false" || req.Header.Get("Xero-Tenant-Id") != "tenant-1" || req.Header.Get("Authorization") != "Bearer at" {
		t.Errorf("query = %v, header = %v, want = summarizeErrors false, tenant-1, Bearer at", req.Query, req.Header)
	}
	want := `{"Invoices":[{"Type":"ACCREC","Contact":{"ContactID":"contact-1"},"InvoiceNumber":"INV-1","Status":"AUTHORISED",
		"CurrencyCode":"EUR","Date":"2024-07-08","DueDate":"2024-07-08","Reference":"PO-123","LineItems":[
		{"Description":"Add-on Fee","AccountCode":"ao-acc","ItemCode":"ao","Quantity":2,"UnitAmount":100,"TaxAmount":0.02},
		{"Description":"Fixed Charge Fee","AccountCode":"fc-acc","ItemCode":"fc","Quantity":6,"UnitAmount":25,"TaxAmount":0.02},
		{"Description":"Standard Charge Fee","AccountCode":"bm-acc","ItemCode":"bm","Quantity":3,"UnitAmount":100,"TaxAmount":0.02},
		{"Description":"Minimum Commitment Fee","AccountCode":"mc-acc","ItemCode":"mc","Quantity":4,"UnitAmount":100,"TaxAmount":0.02},
		{"Description":"Subscription","AccountCode":"sub-acc","ItemCode":"sub","Quantity":5,"UnitAmount":100,"TaxAmount":0.02},
		{"Description":"Coupons","AccountCode":"cp-acc","ItemCode":"cp","Quantity":1,"UnitAmount":-2,"TaxAmount":-2.9},
		{"Description":"Prepaid credit","AccountCode":"pc-acc","ItemCode":"pc","Quantity":1,"UnitAmount":-3},
		{"Description":"Usage already billed","AccountCode":"pc-acc","ItemCode":"pc","Quantity":1,"UnitAmount":-1},
		{"Description":"Credit note","AccountCode":"cn-acc","ItemCode":"cn","Quantity":1,"UnitAmount":-5}]}]}`
	if !apiclienttest.EqualJSON(t, req.Body, want) {
		t.Errorf("body = %s, want = %s", req.Body, want)
	}
}

func TestInvoiceLines(t *testing.T) {
	eur := money.MustCurrency("EUR")
	single := func(fees ...integration.AccountingFee) *integration.AccountingInvoice {
		return &integration.AccountingInvoice{Currency: eur, Fees: fees}
	}
	grouped := fee(core.FeeTypeCharge, "Storage usage", "metric_ext_id", "10", "100", 1000, 0)
	grouped.GroupedByDisplay = " • green"
	filtered := fee(core.FeeTypeCharge, "Storage usage", "metric_ext_id", "10", "100", 1000, 0)
	filtered.FilterDisplayName = "eu, gold"
	tests := []struct {
		name string
		inv  *integration.AccountingInvoice
		want []object
	}{
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/xero_spec.rb:159
			name: "precise unit amount over 2 decimals",
			inv:  single(fee(core.FeeTypeCharge, "Usage", "metric_ext_id", "74759000", "0.000018", 89_566, 0)),
			want: []object{{"Description": "Usage", "AccountCode": "metric_ext_id-acc", "ItemCode": "metric_ext_id", "Quantity": json.Number("1"), "UnitAmount": json.Number("895.66")}},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/xero_spec.rb:186
			name: "grouped by",
			inv:  single(grouped),
			want: []object{{"Description": "Storage usage • green", "AccountCode": "metric_ext_id-acc", "ItemCode": "metric_ext_id", "Quantity": json.Number("10"), "UnitAmount": json.Number("100")}},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/xero_spec.rb:238
			name: "charge filter",
			inv:  single(filtered),
			want: []object{{"Description": "eu, gold", "AccountCode": "metric_ext_id-acc", "ItemCode": "metric_ext_id", "Quantity": json.Number("10"), "UnitAmount": json.Number("100")}},
		},
		{
			// source: spec/services/integrations/aggregator/invoices/payloads/xero_spec.rb:262
			name: "zero amount fee kept",
			inv: single(fee(core.FeeTypeCharge, "Paid usage", "m", "2", "100", 200, 0),
				fee(core.FeeTypeCharge, "Free usage", "m", "5", "0", 0, 0)),
			want: []object{
				{"Description": "Paid usage", "AccountCode": "m-acc", "ItemCode": "m", "Quantity": json.Number("2"), "UnitAmount": json.Number("100")},
				{"Description": "Free usage", "AccountCode": "m-acc", "ItemCode": "m", "Quantity": json.Number("5")},
			},
		},
		{
			// source: app/services/integrations/aggregator/invoices/payloads/base_payload.rb:52-64
			name: "tax remainder on the first fee above it",
			inv: func() *integration.AccountingInvoice {
				inv := single(fee(core.FeeTypeAddOn, "A", "a", "1", "1", 100, 1), fee(core.FeeTypeAddOn, "B", "b", "1", "1", 100, 10))
				inv.TaxesAmountCents = 13
				return inv
			}(),
			want: []object{
				{"Description": "A", "AccountCode": "a-acc", "ItemCode": "a", "Quantity": json.Number("1"), "UnitAmount": json.Number("1"), "TaxAmount": json.Number("0.01")},
				{"Description": "B", "AccountCode": "b-acc", "ItemCode": "b", "Quantity": json.Number("1"), "UnitAmount": json.Number("1"), "TaxAmount": json.Number("0.12")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := invoiceBody(tt.inv)
			if err != nil {
				t.Fatalf("invoiceBody() error = %v", err)
			}
			got, _ := json.Marshal(body["LineItems"])
			want, _ := json.Marshal(tt.want)
			if !apiclienttest.EqualJSON(t, got, string(want)) {
				t.Errorf("LineItems = %s, want = %s", got, want)
			}
		})
	}
}

func TestInvoiceUnmapped(t *testing.T) {
	// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:489
	inv := sharedInvoice()
	inv.Fees[2].Item = nil
	if _, err := invoiceBody(inv); !errors.Is(err, integration.ErrNoMapping) {
		t.Errorf("invoiceBody() error = %v, want = ErrNoMapping", err)
	}
}

func TestSyncInvoiceValidationErrors(t *testing.T) {
	// source: spec/services/integrations/aggregator/invoices/create_service_spec.rb:395
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /api.xro/2.0/Invoices": {apiclienttest.JSON(200, `{"Invoices":[{"InvoiceID":"00000000-0000-0000-0000-000000000000","HasErrors":true,
			"ValidationErrors":[{"Message":"Email address must be valid."},{"Message":"Account code '400' is not valid."}]}]}`)},
	})
	_, err := gateway(s, nil).SyncInvoice(context.Background(), live(), sharedInvoice())
	pe, ok := integration.AsProviderError(err)
	if !ok || pe.Code != integration.CodeValidationError || pe.Message != "Email address must be valid.. Account code '400' is not valid." {
		t.Errorf("error = %v, want = Validation error with joined messages", err)
	}
}

func TestSyncCreditNote(t *testing.T) {
	// source: spec/services/integrations/aggregator/credit_notes/payloads/xero_spec.rb:9
	// source: spec/services/integrations/aggregator/credit_notes/payloads/xero_spec.rb:66
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /api.xro/2.0/CreditNotes": {apiclienttest.JSON(200, `{"CreditNotes":[{"CreditNoteID":"xcn-1"}]}`)},
	})
	item := func(id string, f integration.AccountingFee, cents int64) integration.AccountingCreditNoteItem {
		return integration.AccountingCreditNoteItem{ID: id, Fee: f, AmountCents: cents}
	}
	cn := &integration.AccountingCreditNote{
		Number: "CN-1", ExternalCustomerID: "contact-1", IssuingDate: core.NewDate(2024, 7, 8), Currency: money.MustCurrency("EUR"),
		TaxesRate: money.Zero, CouponsAdjustmentAmountCents: 50,
		Items: []integration.AccountingCreditNoteItem{
			item("1", fee(core.FeeTypeAddOn, "Add-on Fee", "ao", "0", "0", 0, 0), 190),
			item("2", fee(core.FeeTypeSubscription, "Plan", "sub", "0", "0", 0, 0), 160),
			item("3", fee(core.FeeTypeCharge, "Zero", "bm", "0", "0", 0, 0), 0),
		},
		Collections: integration.DocumentItems{Coupon: ext("cp", "cp-acc")},
	}
	if id, err := gateway(s, nil).SyncCreditNote(context.Background(), live(), cn); err != nil || id != "xcn-1" {
		t.Fatalf("SyncCreditNote() = %q, %v, want = xcn-1", id, err)
	}
	want := `{"CreditNotes":[{"Type":"ACCRECCREDIT","Contact":{"ContactID":"contact-1"},"CreditNoteNumber":"CN-1","Status":"AUTHORISED",
		"CurrencyCode":"EUR","Date":"2024-07-08","LineItems":[
		{"Description":"Add-on Fee","AccountCode":"ao-acc","ItemCode":"ao","Quantity":1,"UnitAmount":1.9},
		{"Description":"Subscription","AccountCode":"sub-acc","ItemCode":"sub","Quantity":1,"UnitAmount":1.6},
		{"Description":"Zero","AccountCode":"bm-acc","ItemCode":"bm"},
		{"Description":"Coupons","AccountCode":"cp-acc","ItemCode":"cp","Quantity":1,"UnitAmount":-0.5}]}]}`
	if req := s.Only("POST /api.xro/2.0/CreditNotes"); !apiclienttest.EqualJSON(t, req.Body, want) {
		t.Errorf("body = %s, want = %s", req.Body, want)
	}
}

func TestCreditNoteTaxes(t *testing.T) {
	// source: spec/services/integrations/aggregator/credit_notes/payloads/xero_spec.rb:9
	eur := money.MustCurrency("EUR")
	cn := &integration.AccountingCreditNote{
		Currency: eur, TaxesRate: money.NewFromInt(20), TaxesAmountCents: 134,
		Items: []integration.AccountingCreditNoteItem{
			{ID: "1", Fee: fee(core.FeeTypeAddOn, "A", "a", "0", "0", 0, 0), AmountCents: 333},
			{ID: "2", Fee: fee(core.FeeTypeAddOn, "B", "b", "0", "0", 0, 0), AmountCents: 333},
		},
	}
	body, err := creditNoteBody(cn)
	if err != nil {
		t.Fatalf("creditNoteBody() error = %v", err)
	}
	// 333 × 20% = 66.6 cents each; the 0.8 cent remainder goes to the
	// first line.
	lines := body["LineItems"].([]object)
	if got := lines[0]["TaxAmount"]; got != json.Number("0.674") {
		t.Errorf("TaxAmount[0] = %v, want = 0.674", got)
	}
	if got := lines[1]["TaxAmount"]; got != json.Number("0.666") {
		t.Errorf("TaxAmount[1] = %v, want = 0.666", got)
	}
}

func TestSyncPayment(t *testing.T) {
	// source: spec/services/integrations/aggregator/payments/payloads/xero_spec.rb:9
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"PUT /api.xro/2.0/Payments": {apiclienttest.JSON(200, `{"Payments":[{"PaymentID":"xpay-1","HasValidationErrors":false}]}`)},
	})
	p := &integration.AccountingPayment{
		InvoiceExternalID: "xinv-1", Account: ext("acc", "090"), AmountCents: 12_345, Currency: money.MustCurrency("EUR"),
		CreatedAt: time.Date(2024, 7, 9, 23, 30, 0, 0, time.UTC),
	}
	if id, err := gateway(s, nil).SyncPayment(context.Background(), live(), p); err != nil || id != "xpay-1" {
		t.Fatalf("SyncPayment() = %q, %v, want = xpay-1", id, err)
	}
	want := `{"Payments":[{"Amount":123.45,"Account":{"Code":"090"},"Date":"2024-07-09","Invoice":{"InvoiceID":"xinv-1"}}]}`
	if req := s.Only("PUT /api.xro/2.0/Payments"); !apiclienttest.EqualJSON(t, req.Body, want) {
		t.Errorf("body = %s, want = %s", req.Body, want)
	}
	p.Account = nil
	if _, err := gateway(s, nil).SyncPayment(context.Background(), live(), p); !errors.Is(err, integration.ErrNoMapping) {
		t.Errorf("SyncPayment() without account error = %v, want = ErrNoMapping", err)
	}
}

func TestContacts(t *testing.T) {
	c := &integration.Contact{
		Name: "Acme", Firstname: "John", Lastname: "Doe", Email: "a@acme.test, b@acme.test", Phone: "123,456",
		BillingAddress: core.Address{Line1: "1 Main St", City: "Paris", Zipcode: "75001", Country: "FR"},
	}
	tests := []struct {
		name   string
		update bool
		want   string
	}{
		{
			// source: spec/services/integrations/aggregator/contacts/payloads/xero_spec.rb:39
			name: "create",
			want: `{"Contacts":[{"Name":"Acme","FirstName":"John","LastName":"Doe","EmailAddress":"a@acme.test","Phones":[{"PhoneType":"DEFAULT","PhoneNumber":"123"}],
				"Addresses":[{"AddressType":"POBOX","City":"Paris","PostalCode":"75001","Country":"FR"}]}]}`,
		},
		{
			// source: spec/services/integrations/aggregator/contacts/payloads/xero_spec.rb:94
			name: "update", update: true,
			want: `{"Contacts":[{"ContactID":"xc-1","Name":"Acme","FirstName":"John","LastName":"Doe","EmailAddress":"a@acme.test","Phones":[{"PhoneType":"DEFAULT","PhoneNumber":"123"}],
				"Addresses":[{"AddressType":"POBOX","City":"Paris","PostalCode":"75001","Country":"FR"}]}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
				"POST /api.xro/2.0/Contacts": {apiclienttest.JSON(200, `{"Contacts":[{"ContactID":"xc-1","EmailAddress":"a@acme.test","HasValidationErrors":false}]}`)},
			})
			g := gateway(s, nil)
			var (
				got integration.ContactResult
				err error
			)
			if tt.update {
				c.LinkedID = "xc-1"
				got, err = g.UpdateContact(context.Background(), live(), c)
			} else {
				got, err = g.CreateContact(context.Background(), live(), c)
			}
			if err != nil || got.ExternalID != "xc-1" || got.Email != "a@acme.test" {
				t.Fatalf("contact = %+v, %v, want = xc-1", got, err)
			}
			if req := s.Only("POST /api.xro/2.0/Contacts"); !apiclienttest.EqualJSON(t, req.Body, tt.want) {
				t.Errorf("body = %s, want = %s", req.Body, tt.want)
			}
		})
	}
}

func TestContactNames(t *testing.T) {
	for _, tt := range []struct {
		name, source        string
		first, last         string
		update              bool
		wantFirst, wantLast bool
	}{
		{"create, blank", "spec/services/integrations/aggregator/contacts/payloads/xero_spec.rb:34", "", "", false, false, false},
		{"create, first name", "spec/services/integrations/aggregator/contacts/payloads/xero_spec.rb:52", "John", "", false, true, false},
		{"create, last name", "spec/services/integrations/aggregator/contacts/payloads/xero_spec.rb:61", "", "Doe", false, false, true},
		{"update, blank", "spec/services/integrations/aggregator/contacts/payloads/xero_spec.rb:89", "", "", true, false, false},
		{"update, first name", "spec/services/integrations/aggregator/contacts/payloads/xero_spec.rb:107", "John", "", true, true, false},
		{"update, last name", "spec/services/integrations/aggregator/contacts/payloads/xero_spec.rb:116", "", "Doe", true, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			id := ""
			if tt.update {
				id = "xc-1"
			}
			o := contactBody(&integration.Contact{Name: "Acme", Firstname: tt.first, Lastname: tt.last}, id)
			_, first := o["FirstName"]
			_, last := o["LastName"]
			if first != tt.wantFirst || last != tt.wantLast || o["Name"] != "Acme" {
				t.Errorf("got = %v, want = FirstName %v, LastName %v", o, tt.wantFirst, tt.wantLast)
			}
			if _, ok := o["ContactID"]; ok != tt.update {
				t.Errorf("ContactID: got = %v, want = %v", ok, tt.update)
			}
		})
	}
}

func TestFetchItemsAndAccounts(t *testing.T) {
	// source: spec/services/integrations/aggregator/items_service_spec.rb:66
	// source: spec/services/integrations/aggregator/accounts_service_spec.rb:37
	// A Xero item's external ID is its code.
	// source: spec/models/integrations/xero_integration_spec.rb:46
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"GET /api.xro/2.0/Items":    {apiclienttest.JSON(200, `{"Items":[{"ItemID":"i1","Code":"test-lead","Name":"Lead","SalesDetails":{"AccountCode":"200"}},{"ItemID":"i2","Code":"bare"}]}`)},
		"GET /api.xro/2.0/Accounts": {apiclienttest.JSON(200, `{"Accounts":[{"AccountID":"a1","Code":"200","Name":"Sales","Status":"ACTIVE"},{"AccountID":"a2","Code":"300","Name":"Old","Status":"ARCHIVED"}]}`)},
	})
	g := gateway(s, nil)
	items, err := g.FetchItems(context.Background(), live())
	want := []integration.ExternalRecord{{ID: "test-lead", Name: "Lead", AccountCode: "200"}, {ID: "bare", Name: "bare"}}
	if err != nil || len(items) != 2 || items[0] != want[0] || items[1] != want[1] {
		t.Errorf("FetchItems() = %+v, %v, want = %+v", items, err, want)
	}
	accounts, err := g.FetchAccounts(context.Background(), live())
	if err != nil || len(accounts) != 1 || accounts[0] != (integration.ExternalRecord{ID: "a1", Name: "Sales", AccountCode: "200"}) {
		t.Errorf("FetchAccounts() = %+v, %v, want = the active account", accounts, err)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		header map[string]string
		kind   integration.ErrorKind
		code   string
		msg    string
	}{
		{
			name: "rate limit", status: 429, body: `{"Title":"Too Many Requests","Status":429}`, header: map[string]string{"Retry-After": "30"},
			kind: integration.KindRequestLimit, code: "Too Many Requests", msg: "Too Many Requests",
		},
		{
			name: "validation exception", status: 400,
			body: `{"ErrorNumber":10,"Type":"ValidationException","Message":"A validation exception occurred","Elements":[{"ValidationErrors":[{"Message":"Invalid contact"}]}]}`,
			kind: integration.KindInvalidRequest, code: "ValidationException", msg: "Invalid contact",
		},
		{
			name: "unauthorized", status: 401, body: `{"Type":null,"Title":"Unauthorized","Status":401,"Detail":"AuthenticationUnsuccessful"}`,
			kind: integration.KindAuthentication, code: "Unauthorized", msg: "AuthenticationUnsuccessful",
		},
		{
			name: "server error", status: 503, body: `upstream`, kind: integration.KindUnavailable, code: codeUnexpected, msg: "upstream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := apiclienttest.New(t, map[string][]apiclienttest.Reply{"GET /api.xro/2.0/Items": {{Status: tt.status, Body: tt.body, Header: tt.header}}})
			_, err := gateway(s, nil).FetchItems(context.Background(), live())
			pe, ok := integration.AsProviderError(err)
			if !ok || pe.Kind != tt.kind || pe.Code != tt.code || pe.Message != tt.msg {
				t.Fatalf("error = %v, want = %s %s %q", err, tt.kind, tt.code, tt.msg)
			}
			if tt.header != nil && pe.RetryAfter != 30*time.Second {
				t.Errorf("RetryAfter = %v, want = 30s", pe.RetryAfter)
			}
		})
	}
}

func TestRefresh(t *testing.T) {
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /connect/token":    {apiclienttest.JSON(200, `{"access_token":"at2","refresh_token":"rt2","expires_in":1800,"token_type":"Bearer"}`)},
		"GET /api.xro/2.0/Items": {apiclienttest.JSON(200, `{"Items":[]}`)},
	})
	st := &store{}
	g := gateway(s, st)
	expired := connection(now.Add(30 * time.Second))
	for range 2 {
		if _, err := g.FetchItems(context.Background(), expired); err != nil {
			t.Fatalf("FetchItems() error = %v", err)
		}
	}
	if n := len(s.Requests("POST /connect/token")); n != 1 {
		t.Errorf("token requests = %d, want = 1", n)
	}
	form, _ := url.ParseQuery(string(s.Only("POST /connect/token").Body))
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rt" {
		t.Errorf("token form = %v, want = refresh_token rt", form)
	}
	if auth := s.Only("POST /connect/token").Header.Get("Authorization"); auth != "Basic "+base64.StdEncoding.EncodeToString([]byte("cid:csecret")) {
		t.Errorf("Authorization = %q, want = client basic auth", auth)
	}
	want := map[string]string{KeyAccessToken: "at2", KeyRefreshToken: "rt2", KeyTokenExpiresAt: "2024-07-08T12:30:00Z"}
	if len(st.got) != 1 || len(st.got[0].Secrets) != 3 || st.got[0].Secrets[KeyAccessToken] != want[KeyAccessToken] ||
		st.got[0].Secrets[KeyRefreshToken] != want[KeyRefreshToken] || st.got[0].Secrets[KeyTokenExpiresAt] != want[KeyTokenExpiresAt] {
		t.Errorf("stored = %+v, want = %v", st.got, want)
	}
	for _, r := range s.Requests("GET /api.xro/2.0/Items") {
		if r.Header.Get("Authorization") != "Bearer at2" {
			t.Errorf("Authorization = %q, want = Bearer at2", r.Header.Get("Authorization"))
		}
	}
}

func TestRefreshRevoked(t *testing.T) {
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /connect/token": {apiclienttest.JSON(400, `{"error":"invalid_grant"}`)},
	})
	_, err := gateway(s, &store{}).FetchItems(context.Background(), connection(now))
	if !errors.Is(err, integration.ErrAuthentication) || integration.ErrorCode(err, "") != "invalid_grant" {
		t.Errorf("error = %v, want = authentication invalid_grant", err)
	}
}

func jwt(claims string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(claims)) + ".sig"
}

func TestOAuth(t *testing.T) {
	token := jwt(`{"authentication_event_id":"ev-2"}`)
	s := apiclienttest.New(t, map[string][]apiclienttest.Reply{
		"POST /connect/token": {apiclienttest.JSON(200, `{"access_token":"`+token+`","refresh_token":"rt","expires_in":1800}`)},
		"GET /connections": {apiclienttest.JSON(200, `[{"authEventId":"ev-1","tenantId":"t-old","tenantType":"ORGANISATION","tenantName":"Old"},
			{"authEventId":"ev-2","tenantId":"t-new","tenantType":"ORGANISATION","tenantName":"Acme Ltd"}]`)},
	})
	g := gateway(s, nil)
	conn := integration.Connection{ID: live().ID, Provider: Provider}
	u, err := g.AuthorizationURL(conn, "https://app.example/callback", "st")
	if err != nil || !strings.HasPrefix(u, s.URL+"/identity/connect/authorize?") {
		t.Fatalf("AuthorizationURL() = %q, %v", u, err)
	}
	q, _ := url.Parse(u)
	if v := q.Query(); v.Get("client_id") != "cid" || v.Get("state") != "st" || v.Get("redirect_uri") != "https://app.example/callback" ||
		v.Get("response_type") != "code" || v.Get("scope") != strings.Join(DefaultScopes, " ") {
		t.Errorf("authorization query = %v", v)
	}
	up, err := g.ExchangeCode(context.Background(), conn, "https://app.example/callback", "code-1")
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}
	if up.Settings[KeyTenantID] != "t-new" || up.Settings[KeyTenantName] != "Acme Ltd" || up.Secrets[KeyAccessToken] != token ||
		up.Secrets[KeyRefreshToken] != "rt" || up.Secrets[KeyTokenExpiresAt] != "2024-07-08T12:30:00Z" {
		t.Errorf("ExchangeCode() = %+v, want = tenant t-new and the tokens", up)
	}
	form, _ := url.ParseQuery(string(s.Only("POST /connect/token").Body))
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code-1" || form.Get("redirect_uri") != "https://app.example/callback" {
		t.Errorf("token form = %v", form)
	}
	if got := s.Only("GET /connections").Header.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("connections Authorization = %q", got)
	}
}

func TestPickTenant(t *testing.T) {
	a := tenant{AuthEventID: "e1", TenantID: "a"}
	b := tenant{AuthEventID: "e2", TenantID: "b"}
	tests := []struct {
		name    string
		tenants []tenant
		event   string
		want    string
		wantErr bool
	}{
		{name: "event match", tenants: []tenant{a, b}, event: "e2", want: "b"},
		{name: "single", tenants: []tenant{a}, event: "", want: "a"},
		{name: "ambiguous", tenants: []tenant{a, b}, event: "e3", wantErr: true},
		{name: "none", wantErr: true},
	}
	for _, tt := range tests {
		got, err := pickTenant(tt.tenants, tt.event)
		if (err != nil) != tt.wantErr || got.TenantID != tt.want {
			t.Errorf("%s: pickTenant() = %+v, %v, want = %q", tt.name, got, err, tt.want)
		}
	}
}

func TestNotConnected(t *testing.T) {
	conn := live()
	conn.Settings = json.RawMessage(`{}`)
	if _, err := New().FetchItems(context.Background(), conn); !errors.Is(err, integration.ErrNotConnected) {
		t.Errorf("error = %v, want = ErrNotConnected", err)
	}
}
