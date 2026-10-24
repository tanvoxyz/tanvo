package rpc_test

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	integrationv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/integration/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/integration/v1/integrationv1connect"
)

func integrationsClient(s *rpctest.Server, c rpctest.Credential) integrationv1connect.IntegrationServiceClient {
	return integrationv1connect.NewIntegrationServiceClient(s.Connect(c))
}

// restIntegration is the JSON of an integration in a REST response.
type restIntegration struct {
	ID       string            `json:"id"`
	Code     string            `json:"code"`
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Category string            `json:"category"`
	Netsuite map[string]string `json:"netsuite"`
	Anrok    map[string]string `json:"anrok"`
	Stripe   map[string]any    `json:"stripe"`

	Flutterwave map[string]string `json:"flutterwave"`
}

func integrationBody(t *testing.T, body []byte) restIntegration {
	t.Helper()
	return decode[struct {
		Integration restIntegration `json:"integration"`
	}](t, body).Integration
}

const netsuiteBody = `{"code":"netsuite1","name":"Netsuite 1","netsuite":{"account_id":"acc1","client_id":"cl1",
	"client_secret":"secret","token_id":"xyz","token_secret":"zyx","script_endpoint_url":"https://example.com/restlet"}}`

func createIntegration(t *testing.T, s *rpctest.Server, body string) restIntegration {
	t.Helper()
	status, out := s.REST(t, s.Session, http.MethodPost, "/v1/integrations", body)
	if status != http.StatusOK {
		t.Fatalf("create: got status = %d (%s), want = 200", status, out)
	}
	return integrationBody(t, out)
}

func TestCreateIntegration(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	t.Run("netsuite", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integrations/netsuite/create_spec.rb:63")
		i := createIntegration(t, s, netsuiteBody)
		if i.Provider != "INTEGRATION_PROVIDER_NETSUITE" || i.Category != "INTEGRATION_CATEGORY_ACCOUNTING" || i.Name != "Netsuite 1" {
			t.Errorf("got = %+v, want = a netsuite accounting integration", i)
		}
		if i.Netsuite["token_id"] != "xyz" || i.Netsuite["script_endpoint_url"] != "https://example.com/restlet" {
			t.Errorf("netsuite: got = %v, want = token_id and script_endpoint_url", i.Netsuite)
		}
		if i.Netsuite["token_secret"] != "" || i.Netsuite["client_secret"] != "" {
			t.Errorf("netsuite: got = %v, want = no credentials", i.Netsuite)
		}
	})
	t.Run("anrok", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integrations/anrok/create_spec.rb:49")
		i := createIntegration(t, s, `{"code":"anrok1","name":"Anrok 1","anrok":{"api_key":"acct42/sk_123"}}`)
		if i.Anrok["external_account_id"] != "acct42" || i.Anrok["api_key"] != "" {
			t.Errorf("anrok: got = %v, want = external_account_id acct42 only", i.Anrok)
		}
	})
	t.Run("stripe", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/payment_providers/stripe/create_spec.rb:34")
		i := createIntegration(t, s, `{"code":"stripe1","name":"Stripe","stripe":{"secret_key":"sk_test_1234567890","success_redirect_url":"https://example.com/ok","supports_3ds":true,"require_terms_of_service_consent":false}}`)
		if i.Category != "INTEGRATION_CATEGORY_PAYMENT" || i.Stripe["success_redirect_url"] != "https://example.com/ok" || i.Stripe["secret_key"] != nil {
			t.Errorf("got = %+v, want = payment integration without the secret key", i)
		}
		if i.Stripe["supports_3ds"] != true || i.Stripe["require_terms_of_service_consent"] != nil {
			t.Errorf("stripe flags: got = %v, want = supports_3ds only", i.Stripe)
		}
	})
	t.Run("flutterwave webhook secret", func(t *testing.T) {
		t.Log("source: app/models/payment_providers/flutterwave_provider.rb:18,30 and app/graphql/types/payment_providers/flutterwave.rb:13")
		i := createIntegration(t, s, `{"code":"fw1","name":"Flutterwave","flutterwave":{"secret_key":"FLWSECK_TEST-1"}}`)
		if len(i.Flutterwave["webhook_secret"]) != 64 || i.Flutterwave["secret_key"] != "" {
			t.Errorf("flutterwave: got = %v, want = a 64-digit webhook_secret without the secret key", i.Flutterwave)
		}
		status, out := s.REST(t, s.Session, http.MethodGet, "/v1/integrations/"+i.ID, "")
		if got := integrationBody(t, out).Flutterwave["webhook_secret"]; status != http.StatusOK || got != i.Flutterwave["webhook_secret"] {
			t.Errorf("get: got = %d %q, want = 200 %q", status, got, i.Flutterwave["webhook_secret"])
		}
	})
	t.Run("missing credentials", func(t *testing.T) {
		t.Log("source: spec/services/integrations/anrok/create_service_spec.rb:61")
		_, err := integrationsClient(s, s.Session).CreateIntegration(t.Context(), integrationv1.CreateIntegrationRequest_builder{
			Integration: integrationv1.Integration_builder{
				Code: new("anrok2"), Name: new("Anrok"), Anrok: integrationv1.AnrokSettings_builder{}.Build(),
			}.Build(),
		}.Build())
		v := rpc.ErrorViolations(err)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || len(v) != 1 || v[0].Field != "integration.anrok.api_key" {
			t.Errorf("got = %v (%+v), want = InvalidArgument on integration.anrok.api_key", err, v)
		}
	})
	t.Run("duplicate code", func(t *testing.T) {
		t.Log("source: app/models/integrations/base_integration.rb:16")
		status, body := s.REST(t, s.Session, http.MethodPost, "/v1/integrations", netsuiteBody)
		if status != http.StatusBadRequest || !strings.Contains(string(body), "value_already_exist") {
			t.Errorf("got = %d %s, want = 400 value_already_exist", status, body)
		}
	})
	t.Run("api key", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integrations/netsuite/create_spec.rb (requires permission organization:integrations:create)")
		status, _ := s.REST(t, s.APIKey, http.MethodPost, "/v1/integrations", netsuiteBody)
		if status != http.StatusForbidden {
			t.Errorf("got = %d, want = 403", status)
		}
	})
}

func TestGetListUpdateDeleteIntegration(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	ns := createIntegration(t, s, netsuiteBody)
	createIntegration(t, s, `{"code":"xero1","name":"Xero","xero":{}}`)

	t.Run("get by code", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/integration_resolver_spec.rb:37")
		status, body := s.REST(t, s.Session, http.MethodGet, "/v1/integrations/code%3Anetsuite1", "")
		if status != http.StatusOK || integrationBody(t, body).ID != ns.ID {
			t.Errorf("got = %d %s, want = the netsuite integration", status, body)
		}
	})
	t.Run("list filtered", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/integrations_resolver_spec.rb:38")
		res, err := integrationsClient(s, s.Session).ListIntegrations(t.Context(), integrationv1.ListIntegrationsRequest_builder{
			Filter: new(`provider = "xero"`),
		}.Build())
		if err != nil || len(res.GetIntegrations()) != 1 || res.GetIntegrations()[0].GetCode() != "xero1" {
			t.Errorf("got = %v (%v), want = the xero integration", res, err)
		}
		all, _ := integrationsClient(s, s.Session).ListIntegrations(t.Context(), integrationv1.ListIntegrationsRequest_builder{}.Build())
		if len(all.GetIntegrations()) != 2 {
			t.Errorf("got = %d, want = 2", len(all.GetIntegrations()))
		}
	})
	t.Run("update", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integrations/netsuite/update_spec.rb")
		status, body := s.REST(t, s.Session, http.MethodPatch, "/v1/integrations/"+ns.ID,
			`{"name":"Netsuite 2","sync_invoices":true,"netsuite":{"script_endpoint_url":"https://example.com/v2"}}`)
		i := integrationBody(t, body)
		if status != http.StatusOK || i.Name != "Netsuite 2" || i.Netsuite["script_endpoint_url"] != "https://example.com/v2" {
			t.Errorf("got = %d %s, want = updated", status, body)
		}
	})
	t.Run("update immutable setting", func(t *testing.T) {
		t.Log("source: app/services/integrations/netsuite/update_service.rb:23-28")
		status, body := s.REST(t, s.Session, http.MethodPatch, "/v1/integrations/"+ns.ID, `{"netsuite":{"token_secret":"new"}}`)
		if status != http.StatusBadRequest || !strings.Contains(string(body), "integration.netsuite.token_secret") {
			t.Errorf("got = %d %s, want = 400 on integration.netsuite.token_secret", status, body)
		}
	})
	t.Run("update other provider", func(t *testing.T) {
		status, body := s.REST(t, s.Session, http.MethodPatch, "/v1/integrations/"+ns.ID, `{"anrok":{"api_key":"k"}}`)
		if status != http.StatusBadRequest || !strings.Contains(string(body), "integration.anrok") {
			t.Errorf("got = %d %s, want = 400 on integration.anrok", status, body)
		}
	})
	t.Run("update code", func(t *testing.T) {
		t.Log("source: app/services/integrations/netsuite/update_service.rb (code is updated when given)")
		status, body := s.REST(t, s.Session, http.MethodPatch, "/v1/integrations/"+ns.ID, `{"code":"netsuite2"}`)
		if i := integrationBody(t, body); status != http.StatusOK || i.Code != "netsuite2" {
			t.Errorf("got = %d %s, want = code netsuite2", status, body)
		}
		status, body = s.REST(t, s.Session, http.MethodPatch, "/v1/integrations/"+ns.ID, `{"code":"xero1"}`)
		if status != http.StatusBadRequest || !strings.Contains(string(body), "value_already_exist") {
			t.Errorf("duplicate: got = %d %s, want = 400 value_already_exist", status, body)
		}
	})
	t.Run("update stripe flags", func(t *testing.T) {
		t.Log("source: spec/services/payment_providers/stripe_service_spec.rb:20 (supports_3ds, require_terms_of_service_consent)")
		st := createIntegration(t, s, `{"code":"stripe1","name":"Stripe","stripe":{"secret_key":"sk_test_1"}}`)
		status, body := s.REST(t, s.Session, http.MethodPatch, "/v1/integrations/"+st.ID, `{"stripe":{"require_terms_of_service_consent":true}}`)
		if i := integrationBody(t, body); status != http.StatusOK || i.Stripe["require_terms_of_service_consent"] != true {
			t.Errorf("got = %d %s, want = require_terms_of_service_consent", status, body)
		}
		status, body = s.REST(t, s.Session, http.MethodPatch, "/v1/integrations/"+st.ID, `{"stripe":{"require_terms_of_service_consent":false}}`)
		if i := integrationBody(t, body); status != http.StatusOK || i.Stripe["require_terms_of_service_consent"] != nil {
			t.Errorf("got = %d %s, want = consent cleared", status, body)
		}
	})
	t.Run("delete payment provider", func(t *testing.T) {
		t.Log("source: spec/services/payment_providers/destroy_service_spec.rb:18 (discarded; the code can be reused)")
		st := createIntegration(t, s, `{"code":"stripe2","name":"Stripe","stripe":{"secret_key":"sk_test_2"}}`)
		if status, body := s.REST(t, s.Session, http.MethodDelete, "/v1/integrations/"+st.ID, ""); status != http.StatusOK {
			t.Fatalf("got = %d %s, want = 200", status, body)
		}
		if status, _ := s.REST(t, s.Session, http.MethodGet, "/v1/integrations/"+st.ID, ""); status != http.StatusNotFound {
			t.Errorf("get after delete: got = %d, want = 404", status)
		}
		if again := createIntegration(t, s, `{"code":"stripe2","name":"Stripe","stripe":{"secret_key":"sk_test_3"}}`); again.ID == st.ID {
			t.Errorf("recreate: got = the discarded integration, want = a new one")
		}
	})
	t.Run("delete", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integrations/destroy_spec.rb:37")
		if status, body := s.REST(t, s.Session, http.MethodDelete, "/v1/integrations/"+ns.ID, ""); status != http.StatusOK {
			t.Fatalf("got = %d %s, want = 200", status, body)
		}
		t.Log("source: spec/graphql/resolvers/integration_resolver_spec.rb:55")
		if status, _ := s.REST(t, s.Session, http.MethodGet, "/v1/integrations/"+ns.ID, ""); status != http.StatusNotFound {
			t.Errorf("get after delete: got = %d, want = 404", status)
		}
	})
	t.Run("security logs", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integrations/destroy_spec.rb:41")
		t.Log("source: spec/graphql/mutations/integrations/netsuite/update_spec.rb (produces a security log)")
		var logs []*auditlog.SecurityLog
		if err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
			p, err := r.SecurityLogs.List(ctx, s.Org.ID, auditlog.SecurityLogFilter{Types: []string{integrations.SecurityLogType}}, core.PageRequest{})
			logs = p.Items
			return err
		}); err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		var nsDeleted map[string]any
		for _, l := range logs {
			counts[l.Event]++
			if l.Event == integrations.EventDeleted && l.Resources["integration_type"] == "netsuite" {
				nsDeleted = l.Resources
			}
		}
		want := map[string]int{integrations.EventCreated: 5, integrations.EventUpdated: 4, integrations.EventDeleted: 2}
		if !maps.Equal(counts, want) {
			t.Errorf("got = %v, want = %v", counts, want)
		}
		if nsDeleted["integration_name"] != "Netsuite 2" {
			t.Errorf("netsuite deleted: got = %v, want = integration_name Netsuite 2", nsDeleted)
		}
	})
}

func TestIntegrationMappings(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	ns := createIntegration(t, s, netsuiteBody)
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/add_ons", `{"code":"setup","name":"Setup","amount":{"currency":"USD","amount":"1"}}`)
	if status != http.StatusOK {
		t.Fatalf("create add-on: got = %d %s", status, body)
	}
	base := "/v1/integrations/" + ns.ID
	var mappingID string
	t.Run("create and update", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integration_mappings/create_spec.rb:52")
		status, body := s.REST(t, s.Session, http.MethodPost, base+"/mappings",
			`{"mappable_type":"MAPPABLE_TYPE_ADD_ON","mappable_id":"code:setup","external_id":"ext-1","external_name":"Setup"}`)
		m := decode[struct {
			Mapping map[string]string `json:"mapping"`
		}](t, body).Mapping
		if status != http.StatusOK || m["external_id"] != "ext-1" || m["id"] == "" {
			t.Fatalf("got = %d %s, want = the mapping", status, body)
		}
		mappingID = m["id"]
		status, body = s.REST(t, s.Session, http.MethodPost, base+"/mappings",
			`{"mappable_type":"MAPPABLE_TYPE_ADD_ON","mappable_id":"code:setup","external_id":"ext-2"}`)
		if status != http.StatusBadRequest || !strings.Contains(string(body), core.CodeAlreadyExists) {
			t.Errorf("duplicate: got = %d %s, want = 400 %s", status, body, core.CodeAlreadyExists)
		}
		t.Log("source: spec/graphql/mutations/integration_mappings/update_spec.rb:36")
		status, body = s.REST(t, s.Session, http.MethodPatch, base+"/mappings/"+mappingID, `{"external_id":"ext-2"}`)
		m2 := decode[struct {
			Mapping map[string]string `json:"mapping"`
		}](t, body).Mapping
		if status != http.StatusOK || m2["id"] != mappingID || m2["external_id"] != "ext-2" || m2["external_name"] != "Setup" {
			t.Errorf("got = %d %v, want = %s updated", status, m2, mappingID)
		}
		status, body = s.REST(t, s.Session, http.MethodPatch, base+"/mappings/"+mappingID+"?update_mask=mappable_id", `{"mappable_id":"code:other"}`)
		if status != http.StatusBadRequest || !strings.Contains(string(body), "update_mask") {
			t.Errorf("mappable_id: got = %d %s, want = 400 update_mask", status, body)
		}
	})
	t.Run("missing mappable", func(t *testing.T) {
		status, body := s.REST(t, s.Session, http.MethodPost, base+"/mappings",
			`{"mappable_type":"MAPPABLE_TYPE_BILLABLE_METRIC","mappable_id":"code:nope","external_id":"x"}`)
		if status != http.StatusBadRequest || !strings.Contains(string(body), integrations.CodeRelationMustExist) {
			t.Errorf("got = %d %s, want = 400 relation_must_exist", status, body)
		}
	})
	t.Run("list", func(t *testing.T) {
		res, err := integrationsClient(s, s.Session).ListIntegrationMappings(t.Context(), integrationv1.ListIntegrationMappingsRequest_builder{
			IntegrationId: new("code:netsuite1"), Filter: new(`mappable_type = "add_on"`),
		}.Build())
		if err != nil || len(res.GetMappings()) != 1 {
			t.Errorf("got = %v (%v), want = 1 mapping", res, err)
		}
	})
	t.Run("delete", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integration_mappings/destroy_spec.rb:22")
		if status, body := s.REST(t, s.Session, http.MethodDelete, base+"/mappings/"+mappingID, ""); status != http.StatusOK {
			t.Fatalf("got = %d %s, want = 200", status, body)
		}
		t.Log("source: spec/graphql/mutations/integration_mappings/destroy_spec.rb:41")
		if status, _ := s.REST(t, s.Session, http.MethodDelete, base+"/mappings/"+mappingID, ""); status != http.StatusNotFound {
			t.Errorf("got = %d, want = 404", status)
		}
	})
	t.Run("collection mappings", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integration_collection_mappings/create_spec.rb:53")
		t.Log("source: spec/graphql/mutations/integration_collection_mappings/create_spec.rb:78")
		status, body := s.REST(t, s.Session, http.MethodPost, base+"/collection_mappings",
			`{"mapping_type":"COLLECTION_MAPPING_TYPE_CURRENCIES","currencies":{"USD":"1"}}`)
		if status != http.StatusOK {
			t.Fatalf("create: got = %d %s", status, body)
		}
		created := decode[struct {
			CollectionMapping struct {
				ID string `json:"id"`
			} `json:"collection_mapping"`
		}](t, body).CollectionMapping
		t.Log("source: spec/graphql/mutations/integration_collection_mappings/update_spec.rb:134")
		status, body = s.REST(t, s.Session, http.MethodPatch, base+"/collection_mappings/"+created.ID,
			`{"currencies":{"USD":"799344"}}`)
		cm := decode[struct {
			CollectionMapping struct {
				ID         string            `json:"id"`
				Currencies map[string]string `json:"currencies"`
			} `json:"collection_mapping"`
		}](t, body).CollectionMapping
		if status != http.StatusOK || cm.Currencies["USD"] != "799344" {
			t.Fatalf("got = %d %s, want = the currencies mapping", status, body)
		}
		t.Log("source: spec/graphql/mutations/integration_collection_mappings/update_spec.rb:101")
		status, body = s.REST(t, s.Session, http.MethodPatch, base+"/collection_mappings/"+created.ID+"?update_mask=currencies", `{}`)
		if status != http.StatusBadRequest || !strings.Contains(string(body), integrations.CodeCannotBeEmpty) {
			t.Errorf("empty currencies: got = %d %s, want = 400 %s", status, body, integrations.CodeCannotBeEmpty)
		}
		t.Log("source: spec/graphql/mutations/integration_collection_mappings/update_spec.rb:118")
		status, body = s.REST(t, s.Session, http.MethodPatch, base+"/collection_mappings/"+created.ID, `{"currencies":{"USD":""}}`)
		if status != http.StatusBadRequest || !strings.Contains(string(body), integrations.CodeInvalidFormat) {
			t.Errorf("blank value: got = %d %s, want = 400 %s", status, body, integrations.CodeInvalidFormat)
		}
		t.Log("source: spec/graphql/resolvers/integration_collection_mappings_resolver_spec.rb:29")
		res, err := integrationsClient(s, s.Session).ListCollectionMappings(t.Context(), integrationv1.ListCollectionMappingsRequest_builder{
			IntegrationId: new(ns.ID),
		}.Build())
		if err != nil || len(res.GetCollectionMappings()) != 1 {
			t.Fatalf("got = %v (%v), want = 1", res, err)
		}
		t.Log("source: spec/graphql/resolvers/integration_collection_mappings_resolver_spec.rb:48")
		if got := res.GetCollectionMappings()[0]; got.GetMappingType() != integrationv1.CollectionMappingType_COLLECTION_MAPPING_TYPE_CURRENCIES ||
			got.GetCurrencies()["USD"] != "799344" || got.GetExternalId() != "" {
			t.Errorf("got = %v, want = the currencies mapping without an external id", got)
		}
		t.Log("source: spec/graphql/resolvers/integration_collection_mappings_resolver_spec.rb:67")
		if _, err := integrationsClient(s, s.Session).ListCollectionMappings(t.Context(), integrationv1.ListCollectionMappingsRequest_builder{}.Build()); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("without integration: got = %v, want = InvalidArgument", err)
		}
		t.Log("source: spec/graphql/mutations/integration_collection_mappings/destroy_spec.rb:26")
		if status, body := s.REST(t, s.Session, http.MethodDelete, base+"/collection_mappings/"+cm.ID, ""); status != http.StatusOK {
			t.Errorf("got = %d %s, want = 200", status, body)
		}
	})
	t.Run("items", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/integration_items_resolver_spec.rb:33")
		res, err := integrationsClient(s, s.Session).ListIntegrationItems(t.Context(), integrationv1.ListIntegrationItemsRequest_builder{
			IntegrationId: new(ns.ID), Filter: new(`item_type = "standard"`),
		}.Build())
		if err != nil || len(res.GetItems()) != 0 {
			t.Errorf("got = %v (%v), want = no items", res, err)
		}
		t.Log("source: spec/graphql/resolvers/integration_items_resolver_spec.rb:55")
		if _, err := integrationsClient(s, s.Session).ListIntegrationItems(t.Context(), integrationv1.ListIntegrationItemsRequest_builder{}.Build()); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("without integration: got = %v, want = InvalidArgument", err)
		}
	})
	t.Run("provider calls unimplemented", func(t *testing.T) {
		_, err := integrationsClient(s, s.Session).SyncIntegrationItems(t.Context(), integrationv1.SyncIntegrationItemsRequest_builder{
			IntegrationId: new(ns.ID),
		}.Build())
		if connect.CodeOf(err) != connect.CodeUnimplemented {
			t.Errorf("got = %v, want = Unimplemented", err)
		}
	})
}

func TestPreviewInvoiceTaxes(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	if status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/add_ons", `{"code":"setup","name":"Setup","amount":{"currency":"USD","amount":"1"}}`); status != http.StatusOK {
		t.Fatalf("create add-on: got = %d %s", status, body)
	}
	if status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/customers", `{"external_id":"cus_1","name":"Foo","currency":"USD"}`); status != http.StatusOK {
		t.Fatalf("create customer: got = %d %s", status, body)
	}
	req := `{"customer_id":"external_id:cus_1","fees":[{"add_on_id":"code:setup","units":"2"}]}`
	t.Run("customer taxed by no provider", func(t *testing.T) {
		status, body := s.REST(t, s.Session, http.MethodPost, "/v1/invoices:preview_taxes", req)
		if status != http.StatusOK || strings.Contains(string(body), "item_id") {
			t.Errorf("got = %d %s, want = 200 without fees", status, body)
		}
	})
	t.Run("unknown customer", func(t *testing.T) {
		status, body := s.REST(t, s.Session, http.MethodPost, "/v1/invoices:preview_taxes",
			`{"customer_id":"external_id:unknown","fees":[{"add_on_id":"code:setup"}]}`)
		if status != http.StatusNotFound {
			t.Errorf("got = %d %s, want = 404", status, body)
		}
	})
	t.Run("members only", func(t *testing.T) {
		// The mutation needs a member (fetch_draft_invoice_taxes.rb:6).
		status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/invoices:preview_taxes", req)
		if status != http.StatusForbidden && status != http.StatusUnauthorized {
			t.Errorf("got = %d %s, want = refused for an API key", status, body)
		}
	})
}

func TestCreateUpdateProviders(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	type raw = map[string]any
	get := func(t *testing.T, body []byte, provider string) (raw, raw) {
		t.Helper()
		i := decode[struct {
			Integration raw `json:"integration"`
		}](t, body).Integration
		settings, _ := i[provider].(raw)
		return i, settings
	}
	cases := []struct {
		name, provider, create, update string
		check                          func(t *testing.T, created, updated raw)
	}{{
		// source: spec/graphql/mutations/integrations/anrok/update_spec.rb:55
		name: "anrok", provider: "anrok",
		create: `{"code":"anrok1","name":"Anrok","anrok":{"api_key":"acct1/sk_1"}}`,
		update: `{"name":"Anrok 2","anrok":{"api_key":"acct2/sk_2"}}`,
		check: func(t *testing.T, _, u raw) {
			if u["external_account_id"] != "acct2" {
				t.Errorf("updated: got = %v, want = external_account_id acct2", u)
			}
		},
	}, {
		// source: spec/graphql/mutations/integrations/avalara/create_spec.rb:54
		// source: spec/graphql/mutations/integrations/avalara/update_spec.rb:59
		name: "avalara", provider: "avalara",
		create: `{"code":"avalara1","name":"Avalara","avalara":{"account_id":"acc-1","license_key":"lic","company_code":"DEFAULT"}}`,
		// Only the name and code change (avalara/update_service.rb:23-24).
		update: `{"name":"Avalara 2","code":"avalara2"}`,
		check: func(t *testing.T, c, u raw) {
			if c["account_id"] != "acc-1" || c["company_code"] != "DEFAULT" || c["license_key"] != nil {
				t.Errorf("created: got = %v, want = account_id and company_code without the license key", c)
			}
			if u["account_id"] != "acc-1" || u["company_code"] != "DEFAULT" {
				t.Errorf("updated: got = %v, want = the settings kept", u)
			}
		},
	}, {
		// source: spec/graphql/mutations/integrations/hubspot/create_spec.rb:54
		// source: spec/graphql/mutations/integrations/hubspot/update_spec.rb:57
		name: "hubspot", provider: "hubspot",
		create: `{"code":"hubspot1","name":"Hubspot","hubspot":{"default_targeted_object":"HUBSPOT_TARGETED_OBJECT_COMPANIES"}}`,
		update: `{"name":"Hubspot 2","hubspot":{"default_targeted_object":"HUBSPOT_TARGETED_OBJECT_CONTACTS"}}`,
		check: func(t *testing.T, c, u raw) {
			if c["default_targeted_object"] != "HUBSPOT_TARGETED_OBJECT_COMPANIES" || u["default_targeted_object"] != "HUBSPOT_TARGETED_OBJECT_CONTACTS" {
				t.Errorf("targeted object: got = %v then %v, want = companies then contacts", c, u)
			}
		},
	}, {
		// source: spec/graphql/mutations/integrations/salesforce/create_spec.rb:50
		// source: spec/graphql/mutations/integrations/salesforce/update_spec.rb:56
		name: "salesforce", provider: "salesforce",
		create: `{"code":"salesforce1","name":"Salesforce","salesforce":{"instance_id":"https://a.my.salesforce.com"}}`,
		update: `{"name":"Salesforce 2","salesforce":{"instance_id":"https://b.my.salesforce.com"}}`,
		check: func(t *testing.T, c, u raw) {
			if c["instance_id"] != "https://a.my.salesforce.com" || u["instance_id"] != "https://b.my.salesforce.com" {
				t.Errorf("instance id: got = %v then %v, want = a then b", c, u)
			}
		},
	}, {
		// source: spec/graphql/mutations/integrations/xero/create_spec.rb:52
		// source: spec/graphql/mutations/integrations/xero/update_spec.rb:56
		name: "xero", provider: "xero",
		create: `{"code":"xero1","name":"Xero","xero":{}}`,
		update: `{"name":"Xero 2","sync_invoices":true}`,
	}, {
		// source: spec/graphql/mutations/integrations/netsuite/update_spec.rb:61
		name: "netsuite", provider: "netsuite",
		create: netsuiteBody,
		update: `{"name":"Netsuite 2","sync_credit_notes":true}`,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := s.REST(t, s.Session, http.MethodPost, "/v1/integrations", tc.create)
			if status != http.StatusOK {
				t.Fatalf("create: got = %d %s, want = 200", status, body)
			}
			c, cs := get(t, body, tc.provider)
			if !strings.EqualFold(c["provider"].(string), "INTEGRATION_PROVIDER_"+tc.provider) {
				t.Errorf("provider: got = %v, want = %s", c["provider"], tc.provider)
			}
			status, body = s.REST(t, s.Session, http.MethodPatch, "/v1/integrations/"+c["id"].(string), tc.update)
			if status != http.StatusOK {
				t.Fatalf("update: got = %d %s, want = 200", status, body)
			}
			u, us := get(t, body, tc.provider)
			if u["id"] != c["id"] || !strings.HasSuffix(u["name"].(string), " 2") {
				t.Errorf("update: got = %v, want = the integration renamed", u)
			}
			if tc.check != nil {
				tc.check(t, cs, us)
			}
		})
	}
	t.Run("security logs", func(t *testing.T) {
		// Every create and update is logged.
		// source: spec/graphql/mutations/integrations/anrok/create_spec.rb:60
		// source: spec/graphql/mutations/integrations/anrok/update_spec.rb:63
		// source: spec/graphql/mutations/integrations/avalara/create_spec.rb:66
		// source: spec/graphql/mutations/integrations/avalara/update_spec.rb:66
		// source: spec/graphql/mutations/integrations/hubspot/create_spec.rb:62
		// source: spec/graphql/mutations/integrations/hubspot/update_spec.rb:64
		// source: spec/graphql/mutations/integrations/netsuite/create_spec.rb:74
		// source: spec/graphql/mutations/integrations/netsuite/update_spec.rb:69
		// source: spec/graphql/mutations/integrations/salesforce/create_spec.rb:58
		// source: spec/graphql/mutations/integrations/salesforce/update_spec.rb:64
		// source: spec/graphql/mutations/integrations/xero/create_spec.rb:60
		// source: spec/graphql/mutations/integrations/xero/update_spec.rb:63
		var logs []*auditlog.SecurityLog
		if err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
			p, err := r.SecurityLogs.List(ctx, s.Org.ID, auditlog.SecurityLogFilter{Types: []string{integrations.SecurityLogType}}, core.PageRequest{})
			logs = p.Items
			return err
		}); err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, l := range logs {
			got[l.Event+" "+l.Resources["integration_type"].(string)]++
		}
		for _, tc := range cases {
			for _, ev := range []string{integrations.EventCreated, integrations.EventUpdated} {
				if got[ev+" "+tc.provider] != 1 {
					t.Errorf("%s %s: got = %d logs, want = 1 (%v)", ev, tc.provider, got[ev+" "+tc.provider], got)
				}
			}
		}
	})
}
