package hubspot

import (
	"context"
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

// store records the connection updates of refreshed grants.
type store struct {
	updates []integration.ConnectionUpdate
}

func (s *store) UpdateConnection(_ context.Context, _ integration.Connection, u integration.ConnectionUpdate) error {
	s.updates = append(s.updates, u)
	return nil
}

func newGateway(t *testing.T, routes map[string][]apiclienttest.Reply, opts ...Option) (*Gateway, *apiclienttest.Server) {
	t.Helper()
	s := apiclienttest.New(t, routes)
	opts = append([]Option{
		WithBaseURLs("https://app.hubspot.test", s.URL), WithRateLimit(0, 0), WithClock(func() time.Time { return now }),
		WithOAuthClient(OAuthClient{ClientID: "client", ClientSecret: "shh"}),
	}, opts...)
	return New(opts...), s
}

func connection(t *testing.T, settings map[string]string) integration.Connection {
	t.Helper()
	b, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := json.Marshal(map[string]string{
		KeyAccessToken: "tok", KeyRefreshToken: "ref", KeyTokenExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
	})
	return integration.Connection{
		ID: core.MustParseID[core.IntegrationID]("0190d0a6-0000-7000-8000-000000000001"), Code: "hubspot",
		Provider: Provider, Settings: b, Secrets: sec,
	}
}

var deployed = map[string]string{KeyInvoicesObjectTypeID: "2-111", KeySubscriptionsObjectTypeID: "2-222"}

func TestDeploy(t *testing.T) {
	t.Run("creates the invoice object then its properties", func(t *testing.T) {
		t.Log("source: spec/services/integrations/hubspot/invoices/deploy_object_service_spec.rb:37, 109")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"GET /crm/v3/schemas":                        {apiclienttest.JSON(200, `{"results":[{"name":"Other","objectTypeId":"2-9"}]}`)},
			"POST /crm/v3/schemas":                       {apiclienttest.JSON(201, `{"id":"111","objectTypeId":"2-111","name":"TanvoInvoices"}`)},
			"POST /crm/v3/properties/2-111/batch/create": {apiclienttest.JSON(201, `{"status":"COMPLETE","results":[]}`)},
		})
		u, err := g.Deploy(t.Context(), connection(t, nil), integration.CRMObjectInvoices, integration.CRMSchemaObject|integration.CRMSchemaProperties)
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		want := map[string]string{KeyInvoicesObjectTypeID: "2-111", KeyInvoicesObjectVersion: "1", KeyInvoicesPropertiesVersion: "1"}
		if !equalMaps(u.Settings, want) {
			t.Errorf("settings: got = %v, want = %v", u.Settings, want)
		}
		req := s.Only("POST /crm/v3/schemas")
		if got := req.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization: got = %q, want = Bearer tok", got)
		}
		var obj objectSchema
		if err := json.Unmarshal(req.Body, &obj); err != nil {
			t.Fatal(err)
		}
		if obj.Name != "TanvoInvoices" || obj.PrimaryDisplayProperty != "tanvo_invoice_number" || len(obj.Properties) != 14 ||
			strings.Join(obj.AssociatedObjects, ",") != "COMPANY,CONTACT" {
			t.Errorf("object: got = %+v, want = the TanvoInvoices schema", obj)
		}
		apiclienttest.EqualJSON(t, s.Only("POST /crm/v3/properties/2-111/batch/create").Body, `{"inputs":[{
			"groupName":"tanvoinvoices_information","name":"tanvo_invoice_total_due_amount",
			"label":"Tanvo Invoice Total Due Amount","type":"number","fieldType":"number"}]}`)
	})
	t.Run("finds an existing object", func(t *testing.T) {
		t.Log("source: spec/services/integrations/hubspot/invoices/deploy_object_service_spec.rb:92")
		t.Log("source: spec/services/integrations/aggregator/custom_object_service_spec.rb:41")
		t.Log("source: spec/services/integrations/hubspot/subscriptions/deploy_properties_service_spec.rb:25")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"GET /crm/v3/schemas": {apiclienttest.JSON(200, `{"results":[{"name":"TanvoSubscriptions","objectTypeId":"2-222"}]}`)},
			"POST /crm/v3/properties/2-222/batch/create": {apiclienttest.JSON(207, `{"status":"COMPLETE","errors":[
				{"category":"OBJECT_ALREADY_EXISTS","message":"Property already exists"}]}`)},
		})
		u, err := g.Deploy(t.Context(), connection(t, nil), integration.CRMObjectSubscriptions, integration.CRMSchemaObject|integration.CRMSchemaProperties)
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if u.Settings[KeySubscriptionsObjectTypeID] != "2-222" || u.Settings[KeySubscriptionsPropertiesVersion] != "1" {
			t.Errorf("settings: got = %v, want = the found object type and the properties version", u.Settings)
		}
		if n := len(s.Requests("POST /crm/v3/schemas")); n != 0 {
			t.Errorf("schema creations: got = %d, want = 0", n)
		}
	})
	for _, tt := range []struct {
		source   string
		object   integration.CRMObject
		parts    integration.CRMSchemaPart
		settings map[string]string
	}{
		{
			"spec/services/integrations/hubspot/subscriptions/deploy_object_service_spec.rb:54", integration.CRMObjectSubscriptions,
			integration.CRMSchemaObject | integration.CRMSchemaProperties,
			map[string]string{KeySubscriptionsObjectTypeID: "2-222", KeySubscriptionsObjectVersion: "1", KeySubscriptionsPropertiesVersion: "1"},
		},
		{
			"spec/services/integrations/hubspot/invoices/deploy_object_service_spec.rb:69", integration.CRMObjectInvoices,
			integration.CRMSchemaObject,
			map[string]string{KeyInvoicesObjectTypeID: "2-111", KeyInvoicesObjectVersion: "1"},
		},
		{
			"spec/services/integrations/hubspot/invoices/deploy_properties_service_spec.rb:42", integration.CRMObjectInvoices,
			integration.CRMSchemaProperties,
			map[string]string{KeyInvoicesObjectTypeID: "2-111", KeyInvoicesPropertiesVersion: "1"},
		},
		{
			"spec/services/integrations/hubspot/subscriptions/deploy_properties_service_spec.rb:42", integration.CRMObjectSubscriptions,
			integration.CRMSchemaProperties,
			map[string]string{KeySubscriptionsObjectTypeID: "2-222", KeySubscriptionsPropertiesVersion: "1"},
		},
		{
			"spec/services/integrations/hubspot/companies/deploy_properties_service_spec.rb:42", integration.CRMObjectCompanies,
			integration.CRMSchemaProperties,
			map[string]string{KeyCompaniesPropertiesVersion: "1"},
		},
		{
			"spec/services/integrations/hubspot/contacts/deploy_properties_service_spec.rb:42", integration.CRMObjectContacts,
			integration.CRMSchemaProperties,
			map[string]string{KeyContactsPropertiesVersion: "1"},
		},
	} {
		t.Run("up to date/"+string(tt.object), func(t *testing.T) {
			t.Log("source:", tt.source)
			g, s := newGateway(t, nil)
			u, err := g.Deploy(t.Context(), connection(t, tt.settings), tt.object, tt.parts)
			if err != nil || !u.Empty() || len(s.Requests("")) != 0 {
				t.Errorf("got = %+v, %v with %d requests, want = no change and no request", u, err, len(s.Requests("")))
			}
		})
	}
	t.Run("company properties", func(t *testing.T) {
		t.Log("source: spec/services/integrations/hubspot/companies/deploy_properties_service_spec.rb:25")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"POST /crm/v3/properties/companies/batch/create": {apiclienttest.JSON(201, `{"status":"COMPLETE"}`)},
		})
		u, err := g.Deploy(t.Context(), connection(t, nil), integration.CRMObjectCompanies, integration.CRMSchemaProperties)
		if err != nil || u.Settings[KeyCompaniesPropertiesVersion] != "1" {
			t.Fatalf("Deploy: got = %+v, %v, want = companies_properties_version 1", u, err)
		}
		var body struct {
			Inputs []property `json:"inputs"`
		}
		_ = json.Unmarshal(s.Only("POST /crm/v3/properties/companies/batch/create").Body, &body)
		var names []string
		for _, p := range body.Inputs {
			names = append(names, p.Name)
			if p.GroupName != "companyinformation" {
				t.Errorf("%s group: got = %q, want = companyinformation", p.Name, p.GroupName)
			}
		}
		if got, want := strings.Join(names, ","), "tanvo_customer_id,tanvo_customer_external_id,tanvo_billing_email,tanvo_tax_identification_number,tanvo_customer_link"; got != want {
			t.Errorf("properties: got = %s, want = %s", got, want)
		}
	})
	t.Run("contact properties exist", func(t *testing.T) {
		t.Log("source: spec/services/integrations/hubspot/contacts/deploy_properties_service_spec.rb:25")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"POST /crm/v3/properties/contacts/batch/create": {apiclienttest.JSON(409, `{"status":"error","category":"CONFLICT","message":"exists"}`)},
		})
		u, err := g.Deploy(t.Context(), connection(t, nil), integration.CRMObjectContacts, integration.CRMSchemaProperties)
		if err != nil || u.Settings[KeyContactsPropertiesVersion] != "1" {
			t.Fatalf("Deploy: got = %+v, %v, want = contacts_properties_version 1", u, err)
		}
		var body struct {
			Inputs []property `json:"inputs"`
		}
		_ = json.Unmarshal(s.Only("POST /crm/v3/properties/contacts/batch/create").Body, &body)
		if len(body.Inputs) != 4 || body.Inputs[1].HasUniqueValue == nil || !*body.Inputs[1].HasUniqueValue {
			t.Errorf("inputs: got = %+v, want = 4 with a unique external id", body.Inputs)
		}
	})
	t.Run("object only", func(t *testing.T) {
		t.Log("source: spec/services/integrations/hubspot/subscriptions/deploy_object_service_spec.rb:37")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"GET /crm/v3/schemas":  {apiclienttest.JSON(200, `{"results":[]}`)},
			"POST /crm/v3/schemas": {apiclienttest.JSON(201, `{"objectTypeId":"2-222"}`)},
		})
		u, err := g.Deploy(t.Context(), connection(t, nil), integration.CRMObjectSubscriptions, integration.CRMSchemaObject)
		want := map[string]string{KeySubscriptionsObjectTypeID: "2-222", KeySubscriptionsObjectVersion: "1"}
		if err != nil || !equalMaps(u.Settings, want) {
			t.Errorf("got = %v, %v, want = %v", u.Settings, err, want)
		}
		if n := len(s.Requests("POST /crm/v3/properties/2-222/batch/create")); n != 0 {
			t.Errorf("property creations: got = %d, want = 0", n)
		}
	})
	t.Run("properties only", func(t *testing.T) {
		t.Log("source: spec/services/integrations/hubspot/invoices/deploy_properties_service_spec.rb:25")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"POST /crm/v3/properties/2-111/batch/create": {apiclienttest.JSON(201, `{"status":"COMPLETE"}`)},
		})
		u, err := g.Deploy(t.Context(), connection(t, map[string]string{KeyInvoicesObjectTypeID: "2-111"}),
			integration.CRMObjectInvoices, integration.CRMSchemaProperties)
		want := map[string]string{KeyInvoicesPropertiesVersion: "1"}
		if err != nil || !equalMaps(u.Settings, want) {
			t.Errorf("got = %v, %v, want = %v", u.Settings, err, want)
		}
		if n := len(s.Requests("GET /crm/v3/schemas")); n != 0 {
			t.Errorf("schema lookups: got = %d, want = 0", n)
		}
	})
	t.Run("properties of an undeployed object", func(t *testing.T) {
		g, s := newGateway(t, nil)
		_, err := g.Deploy(t.Context(), connection(t, nil), integration.CRMObjectInvoices, integration.CRMSchemaProperties)
		pe, ok := integration.AsProviderError(err)
		if !ok || pe.Code != codeNotDeployed || len(s.Requests("")) != 0 {
			t.Errorf("got = %v, want = object_not_deployed without a request", err)
		}
	})
	t.Run("refused", func(t *testing.T) {
		t.Log("source: spec/services/integrations/hubspot/invoices/deploy_object_service_spec.rb:125")
		g, _ := newGateway(t, map[string][]apiclienttest.Reply{
			"GET /crm/v3/schemas":  {apiclienttest.JSON(200, `{"results":[]}`)},
			"POST /crm/v3/schemas": {apiclienttest.JSON(403, `{"status":"error","category":"MISSING_SCOPES","message":"This app hasn't been granted all required scopes"}`)},
		})
		_, err := g.Deploy(t.Context(), connection(t, nil), integration.CRMObjectInvoices, integration.CRMSchemaObject|integration.CRMSchemaProperties)
		pe, ok := integration.AsProviderError(err)
		if !ok || pe.Code != "MISSING_SCOPES" || pe.Kind != integration.KindAuthentication {
			t.Errorf("got = %v, want = an authentication error MISSING_SCOPES", err)
		}
	})
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func contact(targeted string) *integration.Contact {
	return &integration.Contact{
		CustomerID: core.MustParseID[core.CustomerID]("0190d0a6-0000-7000-8000-0000000000c1"), ExternalID: "cust-1",
		Name: "Acme", Firstname: "Ada", Lastname: "Lovelace", LegalName: "Acme Inc", Email: "billing@acme.test,ops@acme.test",
		Phone: "+331", URL: "www.acme.test/about", TaxNumber: "FR123", TargetedObject: targeted,
		Link: "https://app.tanvo.test/acme/customer/0190d0a6-0000-7000-8000-0000000000c1",
	}
}

func TestContacts(t *testing.T) {
	t.Run("create company", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/companies/payloads/hubspot_spec.rb:30")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"POST /crm/v3/objects/companies": {apiclienttest.JSON(201, `{"id":"501","properties":{"name":"Acme"}}`)},
		})
		got, err := g.CreateContact(t.Context(), connection(t, deployed), contact(integration.TargetedObjectCompanies))
		if err != nil || got.ExternalID != "501" || got.Email != "" {
			t.Fatalf("CreateContact: got = %+v, %v, want = 501", got, err)
		}
		apiclienttest.EqualJSON(t, s.Only("POST /crm/v3/objects/companies").Body, `{"properties":{
			"tanvo_customer_id":"0190d0a6-0000-7000-8000-0000000000c1","tanvo_customer_external_id":"cust-1",
			"tanvo_billing_email":"billing@acme.test,ops@acme.test","tanvo_tax_identification_number":"FR123",
			"tanvo_customer_link":"https://app.tanvo.test/acme/customer/0190d0a6-0000-7000-8000-0000000000c1",
			"name":"Acme","domain":"www.acme.test"}}`)
	})
	t.Run("update company", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/companies/payloads/hubspot_spec.rb:55")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"PATCH /crm/v3/objects/companies/501": {apiclienttest.JSON(200, `{"id":"501"}`)},
		})
		c := contact(integration.TargetedObjectCompanies)
		c.LinkedID = "501"
		got, err := g.UpdateContact(t.Context(), connection(t, deployed), c)
		if err != nil || got.ExternalID != "501" {
			t.Fatalf("UpdateContact: got = %+v, %v, want = 501", got, err)
		}
		apiclienttest.EqualJSON(t, s.Only("PATCH /crm/v3/objects/companies/501").Body, `{"properties":{
			"tanvo_customer_id":"0190d0a6-0000-7000-8000-0000000000c1","tanvo_customer_external_id":"cust-1",
			"tanvo_billing_email":"billing@acme.test,ops@acme.test","tanvo_tax_identification_number":"FR123",
			"tanvo_customer_link":"https://app.tanvo.test/acme/customer/0190d0a6-0000-7000-8000-0000000000c1",
			"name":"Acme","domain":"www.acme.test"}}`)
	})
	t.Run("update company with blank fields", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/companies/payloads/hubspot_spec.rb:62")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"PATCH /crm/v3/objects/companies/501": {apiclienttest.JSON(200, `{"id":"501"}`)},
		})
		c := contact("")
		c.LinkedID, c.Name, c.URL, c.TaxNumber = "501", "", "", ""
		if _, err := g.UpdateContact(t.Context(), connection(t, deployed), c); err != nil {
			t.Fatalf("UpdateContact: %v", err)
		}
		apiclienttest.EqualJSON(t, s.Only("PATCH /crm/v3/objects/companies/501").Body, `{"properties":{
			"tanvo_customer_id":"0190d0a6-0000-7000-8000-0000000000c1","tanvo_customer_external_id":"cust-1",
			"tanvo_billing_email":"billing@acme.test,ops@acme.test","tanvo_tax_identification_number":null,
			"tanvo_customer_link":"https://app.tanvo.test/acme/customer/0190d0a6-0000-7000-8000-0000000000c1"}}`)
	})
	t.Run("create contact", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/contacts/payloads/hubspot_spec.rb:33")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"POST /crm/v3/objects/contacts": {apiclienttest.JSON(201, `{"id":"601","properties":{"email":"billing@acme.test"}}`)},
		})
		got, err := g.CreateContact(t.Context(), connection(t, deployed), contact(integration.TargetedObjectContacts))
		if err != nil || got.ExternalID != "601" || got.Email != "billing@acme.test" {
			t.Fatalf("CreateContact: got = %+v, %v, want = 601 with its email", got, err)
		}
		apiclienttest.EqualJSON(t, s.Only("POST /crm/v3/objects/contacts").Body, `{"properties":{
			"tanvo_customer_id":"0190d0a6-0000-7000-8000-0000000000c1","tanvo_customer_external_id":"cust-1",
			"tanvo_billing_email":"billing@acme.test,ops@acme.test",
			"tanvo_customer_link":"https://app.tanvo.test/acme/customer/0190d0a6-0000-7000-8000-0000000000c1",
			"email":"billing@acme.test,ops@acme.test","firstname":"Ada","lastname":"Lovelace","phone":"+331",
			"company":"Acme Inc","website":"www.acme.test"}}`)
	})
	t.Run("update contact", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/contacts/payloads/hubspot_spec.rb:57")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"PATCH /crm/v3/objects/contacts/601": {apiclienttest.JSON(200, `{"id":"601","properties":{"email":"billing@acme.test"}}`)},
		})
		c := contact(integration.TargetedObjectContacts)
		c.LinkedID = "601"
		if _, err := g.UpdateContact(t.Context(), connection(t, deployed), c); err != nil {
			t.Fatalf("UpdateContact: %v", err)
		}
		apiclienttest.EqualJSON(t, s.Only("PATCH /crm/v3/objects/contacts/601").Body, `{"properties":{
			"email":"billing@acme.test,ops@acme.test","firstname":"Ada","lastname":"Lovelace","phone":"+331",
			"company":"Acme Inc","website":"www.acme.test"}}`)
	})
	t.Run("update contact with blank fields", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/contacts/payloads/hubspot_spec.rb:64")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"PATCH /crm/v3/objects/contacts/601": {apiclienttest.JSON(200, `{"id":"601","properties":{"email":"billing@acme.test"}}`)},
		})
		c := contact(integration.TargetedObjectContacts)
		c.LinkedID, c.Phone, c.URL = "601", "", ""
		if _, err := g.UpdateContact(t.Context(), connection(t, deployed), c); err != nil {
			t.Fatalf("UpdateContact: %v", err)
		}
		apiclienttest.EqualJSON(t, s.Only("PATCH /crm/v3/objects/contacts/601").Body, `{"properties":{
			"email":"billing@acme.test,ops@acme.test","firstname":"Ada","lastname":"Lovelace","company":"Acme Inc"}}`)
	})
	t.Run("conflict", func(t *testing.T) {
		g, _ := newGateway(t, map[string][]apiclienttest.Reply{
			"POST /crm/v3/objects/contacts": {apiclienttest.JSON(409, `{"status":"error","message":"Contact already exists. Existing ID: 42","category":"CONFLICT"}`)},
		})
		_, err := g.CreateContact(t.Context(), connection(t, deployed), contact(integration.TargetedObjectContacts))
		pe, ok := integration.AsProviderError(err)
		if !ok || pe.Code != "CONFLICT" || pe.Message != "Contact already exists. Existing ID: 42" || pe.Kind != integration.KindInvalidRequest {
			t.Errorf("got = %v, want = an invalid request CONFLICT", err)
		}
	})
}

func crmInvoice() *integration.CRMInvoice {
	eur := money.MustCurrency("EUR")
	return &integration.CRMInvoice{
		ID: core.MustParseID[core.InvoiceID]("0190d0a6-0000-7000-8000-0000000000a1"), Number: "ACM-001",
		IssuingDate: core.NewDate(2024, 7, 8), PaymentDueDate: core.NewDate(2024, 8, 7),
		Type: "subscription", Status: "finalized", PaymentStatus: "pending", Currency: eur,
		TotalAmountCents: 12050, TotalDueAmountCents: 12050, SubTotalExcludingTaxesAmountCents: 10000,
		FileURL: "https://files.tanvo.test/inv.pdf", URL: "https://app.tanvo.test/acme/customer/c/invoice/i/overview",
	}
}

func TestSyncInvoice(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/invoices/payloads/hubspot_spec.rb:69")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"POST /crm/v3/objects/2-111": {apiclienttest.JSON(201, `{"id":"901"}`)},
		})
		id, err := g.SyncInvoice(t.Context(), connection(t, deployed), crmInvoice())
		if err != nil || id != "901" {
			t.Fatalf("SyncInvoice: got = %q, %v, want = 901", id, err)
		}
		apiclienttest.EqualJSON(t, s.Only("POST /crm/v3/objects/2-111").Body, `{"associations":[],"properties":{
			"tanvo_invoice_id":"0190d0a6-0000-7000-8000-0000000000a1","tanvo_invoice_number":"ACM-001",
			"tanvo_invoice_purchase_order_number":null,"tanvo_invoice_issuing_date":"2024-07-08",
			"tanvo_invoice_payment_due_date":"2024-08-07","tanvo_invoice_payment_overdue":false,
			"tanvo_invoice_type":"subscription","tanvo_invoice_status":"finalized","tanvo_invoice_payment_status":"pending",
			"tanvo_invoice_currency":"EUR","tanvo_invoice_total_amount":120.5,"tanvo_invoice_total_due_amount":120.5,
			"tanvo_invoice_subtotal_excluding_taxes":100,"tanvo_invoice_file_url":"https://files.tanvo.test/inv.pdf",
			"tanvo_invoice_url":"https://app.tanvo.test/acme/customer/c/invoice/i/overview"}}`)
	})
	t.Run("update", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/invoices/payloads/hubspot_spec.rb:111")
		g, s := newGateway(t, map[string][]apiclienttest.Reply{
			"PATCH /crm/v3/objects/2-111/901": {apiclienttest.JSON(200, `{"id":"901"}`)},
		})
		inv := crmInvoice()
		inv.RecordID, inv.PurchaseOrderNumber, inv.PaymentOverdue = "901", "PO-7", true
		if _, err := g.SyncInvoice(t.Context(), connection(t, deployed), inv); err != nil {
			t.Fatalf("SyncInvoice: %v", err)
		}
		var body map[string]map[string]any
		_ = json.Unmarshal(s.Only("PATCH /crm/v3/objects/2-111/901").Body, &body)
		if _, ok := body["associations"]; ok || body["properties"]["tanvo_invoice_purchase_order_number"] != "PO-7" ||
			body["properties"]["tanvo_invoice_payment_overdue"] != true {
			t.Errorf("body: got = %v, want = the properties only", body)
		}
	})
	t.Run("object not deployed", func(t *testing.T) {
		g, _ := newGateway(t, nil)
		_, err := g.SyncInvoice(t.Context(), connection(t, nil), crmInvoice())
		if pe, ok := integration.AsProviderError(err); !ok || pe.Code != codeNotDeployed {
			t.Errorf("got = %v, want = %s", err, codeNotDeployed)
		}
	})
}

func TestSyncSubscription(t *testing.T) {
	t.Log("source: spec/services/integrations/aggregator/subscriptions/payloads/hubspot_spec.rb:55")
	g, s := newGateway(t, map[string][]apiclienttest.Reply{
		"POST /crm/v3/objects/2-222": {apiclienttest.JSON(201, `{"id":"801"}`)},
	})
	id, err := g.SyncSubscription(t.Context(), connection(t, deployed), &integration.CRMSubscription{
		ID: core.MustParseID[core.SubscriptionID]("0190d0a6-0000-7000-8000-0000000000b1"), ExternalID: "sub-1",
		BillingTime: "anniversary", PlanCode: "pro", Status: "active",
		CreatedAt: time.Date(2024, 7, 1, 23, 30, 0, 0, time.UTC), StartedAt: time.Date(2024, 7, 2, 0, 0, 0, 0, time.UTC),
		SubscriptionAt: time.Date(2024, 7, 2, 0, 0, 0, 0, time.UTC), URL: "https://app.tanvo.test/acme/customer/c/subscription/s/overview",
	})
	if err != nil || id != "801" {
		t.Fatalf("SyncSubscription: got = %q, %v, want = 801", id, err)
	}
	apiclienttest.EqualJSON(t, s.Only("POST /crm/v3/objects/2-222").Body, `{"associations":[],"properties":{
		"tanvo_subscription_id":"0190d0a6-0000-7000-8000-0000000000b1","tanvo_external_subscription_id":"sub-1",
		"tanvo_billing_time":"anniversary","tanvo_subscription_name":null,"tanvo_subscription_plan_code":"pro",
		"tanvo_subscription_status":"active","tanvo_subscription_created_at":"2024-07-01",
		"tanvo_subscription_started_at":"2024-07-02","tanvo_subscription_ending_at":null,
		"tanvo_subscription_at":"2024-07-02","tanvo_subscription_terminated_at":null,
		"tanvo_subscription_trial_ended_at":null,
		"tanvo_subscription_link":"https://app.tanvo.test/acme/customer/c/subscription/s/overview"}}`)
}

func TestUpdateSubscription(t *testing.T) {
	t.Log("source: spec/services/integrations/aggregator/subscriptions/payloads/hubspot_spec.rb:87")
	g, s := newGateway(t, map[string][]apiclienttest.Reply{
		"PATCH /crm/v3/objects/2-222/801": {apiclienttest.JSON(200, `{"id":"801"}`)},
	})
	end := time.Date(2025, 7, 2, 0, 0, 0, 0, time.UTC)
	id, err := g.SyncSubscription(t.Context(), connection(t, deployed), &integration.CRMSubscription{
		RecordID: "801", ID: core.MustParseID[core.SubscriptionID]("0190d0a6-0000-7000-8000-0000000000b1"), ExternalID: "sub-1",
		BillingTime: "calendar", Name: "Pro", PlanCode: "pro", Status: "terminated",
		CreatedAt: time.Date(2024, 7, 1, 23, 30, 0, 0, time.UTC), StartedAt: time.Date(2024, 7, 2, 0, 0, 0, 0, time.UTC),
		SubscriptionAt: time.Date(2024, 7, 2, 0, 0, 0, 0, time.UTC), EndingAt: end, TerminatedAt: end,
		URL: "https://app.tanvo.test/acme/customer/c/subscription/s/overview",
	})
	if err != nil || id != "801" {
		t.Fatalf("SyncSubscription: got = %q, %v, want = 801", id, err)
	}
	apiclienttest.EqualJSON(t, s.Only("PATCH /crm/v3/objects/2-222/801").Body, `{"properties":{
		"tanvo_subscription_id":"0190d0a6-0000-7000-8000-0000000000b1","tanvo_external_subscription_id":"sub-1",
		"tanvo_billing_time":"calendar","tanvo_subscription_name":"Pro","tanvo_subscription_plan_code":"pro",
		"tanvo_subscription_status":"terminated","tanvo_subscription_created_at":"2024-07-01",
		"tanvo_subscription_started_at":"2024-07-02","tanvo_subscription_ending_at":"2025-07-02",
		"tanvo_subscription_at":"2024-07-02","tanvo_subscription_terminated_at":"2025-07-02",
		"tanvo_subscription_trial_ended_at":null,
		"tanvo_subscription_link":"https://app.tanvo.test/acme/customer/c/subscription/s/overview"}}`)
}

func TestAssociate(t *testing.T) {
	tests := []struct {
		name, source string
		a            integration.CRMAssociation
		key          string
	}{
		{
			"invoice to company", "spec/services/integrations/aggregator/invoices/payloads/hubspot_spec.rb:137",
			integration.CRMAssociation{Object: integration.CRMObjectInvoices, RecordID: "901", TargetedObject: "companies", CustomerRecordID: "501"},
			"PUT /crm/v4/objects/2-111/901/associations/default/company/501",
		},
		{
			"subscription to contact", "spec/services/integrations/aggregator/subscriptions/payloads/hubspot_spec.rb:105",
			integration.CRMAssociation{Object: integration.CRMObjectSubscriptions, RecordID: "801", TargetedObject: "contacts", CustomerRecordID: "601"},
			"PUT /crm/v4/objects/2-222/801/associations/default/contact/601",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			g, s := newGateway(t, map[string][]apiclienttest.Reply{tt.key: {apiclienttest.JSON(200, `{"status":"COMPLETE"}`)}})
			if err := g.Associate(t.Context(), connection(t, deployed), tt.a); err != nil {
				t.Fatalf("Associate: %v", err)
			}
			s.Only(tt.key)
		})
	}
}

func TestAccountID(t *testing.T) {
	t.Log("source: spec/services/integrations/hubspot/save_portal_id_service_spec.rb:19")
	g, _ := newGateway(t, map[string][]apiclienttest.Reply{
		"GET /account-info/v3/details": {apiclienttest.JSON(200, `{"portalId":12345678,"accountType":"STANDARD"}`)},
	})
	id, err := g.AccountID(t.Context(), connection(t, nil))
	if err != nil || id != "12345678" {
		t.Errorf("AccountID: got = %q, %v, want = 12345678", id, err)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name   string
		reply  apiclienttest.Reply
		target error
		code   string
	}{
		{"rate limit", apiclienttest.JSON(429, `{"status":"error","message":"You have reached your ten_secondly_rolling limit.","category":"RATE_LIMITS"}`), integration.ErrRequestLimit, "RATE_LIMITS"},
		{"expired", apiclienttest.JSON(401, `{"status":"error","message":"Authentication credentials not found.","category":"INVALID_AUTHENTICATION"}`), integration.ErrAuthentication, "INVALID_AUTHENTICATION"},
		{"validation", apiclienttest.JSON(400, `{"status":"error","message":"Property values were not valid","category":"VALIDATION_ERROR"}`), integration.ErrRejected, "VALIDATION_ERROR"},
		{"outage", apiclienttest.JSON(502, `<html>bad gateway</html>`), integration.ErrRetryable, codeUnexpected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, _ := newGateway(t, map[string][]apiclienttest.Reply{"GET /account-info/v3/details": {tt.reply}})
			_, err := g.AccountID(t.Context(), connection(t, nil))
			pe, ok := integration.AsProviderError(err)
			if !errors.Is(err, tt.target) || !ok || pe.Code != tt.code {
				t.Errorf("got = %v, want = %v with code %s", err, tt.target, tt.code)
			}
		})
	}
}

func TestOAuth(t *testing.T) {
	g, s := newGateway(t, map[string][]apiclienttest.Reply{
		"POST /oauth/v1/token": {apiclienttest.JSON(200, `{"access_token":"new","refresh_token":"ref2","expires_in":1800,"token_type":"bearer"}`)},
	})
	u, err := g.AuthorizationURL(integration.Connection{}, "https://app.tanvo.test/callback", "st")
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	if pu.Host != "app.hubspot.test" || pu.Path != "/oauth/authorize" || pu.Query().Get("client_id") != "client" ||
		pu.Query().Get("state") != "st" || !strings.Contains(pu.Query().Get("scope"), "crm.schemas.custom.write") {
		t.Errorf("AuthorizationURL: got = %s, want = the consent page", u)
	}
	up, err := g.ExchangeCode(t.Context(), connection(t, nil), "https://app.tanvo.test/callback", "code1")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	want := map[string]string{KeyAccessToken: "new", KeyRefreshToken: "ref2", KeyTokenExpiresAt: "2024-07-08T12:30:00Z"}
	if !equalMaps(up.Secrets, want) {
		t.Errorf("secrets: got = %v, want = %v", up.Secrets, want)
	}
	form, _ := url.ParseQuery(string(s.Only("POST /oauth/v1/token").Body))
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code1" || form.Get("client_secret") != "shh" ||
		form.Get("redirect_uri") != "https://app.tanvo.test/callback" {
		t.Errorf("token form: got = %v, want = the authorization code grant", form)
	}
}

func TestRefresh(t *testing.T) {
	st := &store{}
	g, s := newGateway(t, map[string][]apiclienttest.Reply{
		"POST /oauth/v1/token":         {apiclienttest.JSON(200, `{"access_token":"fresh","expires_in":1800}`)},
		"GET /account-info/v3/details": {apiclienttest.JSON(200, `{"portalId":1}`)},
	}, WithConnectionStore(st))
	conn := connection(t, nil)
	conn.Secrets, _ = json.Marshal(map[string]string{KeyAccessToken: "old", KeyRefreshToken: "ref", KeyTokenExpiresAt: now.Add(30 * time.Second).Format(time.RFC3339)})
	if _, err := g.AccountID(t.Context(), conn); err != nil {
		t.Fatalf("AccountID: %v", err)
	}
	if got := s.Only("GET /account-info/v3/details").Header.Get("Authorization"); got != "Bearer fresh" {
		t.Errorf("authorization: got = %q, want = Bearer fresh", got)
	}
	if len(st.updates) != 1 || st.updates[0].Secrets[KeyRefreshToken] != "ref" || st.updates[0].Secrets[KeyAccessToken] != "fresh" {
		t.Errorf("stored: got = %+v, want = the fresh grant keeping the refresh token", st.updates)
	}
	// The cached grant serves the next call.
	if _, err := g.AccountID(t.Context(), conn); err != nil {
		t.Fatalf("AccountID: %v", err)
	}
	if n := len(s.Requests("POST /oauth/v1/token")); n != 1 {
		t.Errorf("refreshes: got = %d, want = 1", n)
	}
}

func TestRefreshRevoked(t *testing.T) {
	g, _ := newGateway(t, map[string][]apiclienttest.Reply{
		"POST /oauth/v1/token": {apiclienttest.JSON(400, `{"status":"BAD_REFRESH_TOKEN","message":"missing or invalid refresh token"}`)},
	})
	conn := connection(t, nil)
	conn.Secrets, _ = json.Marshal(map[string]string{KeyRefreshToken: "gone"})
	_, err := g.AccountID(t.Context(), conn)
	if !errors.Is(err, integration.ErrAuthentication) {
		t.Errorf("got = %v, want = ErrAuthentication", err)
	}
}

func TestNotConnected(t *testing.T) {
	g, _ := newGateway(t, nil)
	conn := connection(t, nil)
	conn.Secrets = nil
	if _, err := g.AccountID(t.Context(), conn); !errors.Is(err, integration.ErrNotConnected) {
		t.Errorf("got = %v, want = ErrNotConnected", err)
	}
}

func TestCleanURL(t *testing.T) {
	t.Log("source: app/services/integrations/aggregator/contacts/payloads/base_payload.rb:67-72")
	tests := map[string]string{
		"https://www.acme.test/x": "www.acme.test", "acme.test": "acme.test", "http://acme.test:8080/": "acme.test", "": "",
	}
	for in, want := range tests {
		if got := cleanURL(in); got != want {
			t.Errorf("cleanURL(%q) = %q, want = %q", in, got, want)
		}
	}
}
