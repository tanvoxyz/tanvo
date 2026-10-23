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
		t.Log("source: spec/graphql/resolvers/integration_resolver_spec.rb")
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
	t.Run("upsert", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/integration_mappings/create_spec.rb:52")
		status, body := s.REST(t, s.Session, http.MethodPut, base+"/mappings",
			`{"mappable_type":"MAPPABLE_TYPE_ADD_ON","mappable_id":"code:setup","external_id":"ext-1","external_name":"Setup"}`)
		m := decode[struct {
			Mapping map[string]string `json:"mapping"`
		}](t, body).Mapping
		if status != http.StatusOK || m["external_id"] != "ext-1" || m["id"] == "" {
			t.Fatalf("got = %d %s, want = the mapping", status, body)
		}
		mappingID = m["id"]
		t.Log("source: spec/graphql/mutations/integration_mappings/update_spec.rb:36")
		_, body = s.REST(t, s.Session, http.MethodPut, base+"/mappings",
			`{"mappable_type":"MAPPABLE_TYPE_ADD_ON","mappable_id":"code:setup","external_id":"ext-2"}`)
		m2 := decode[struct {
			Mapping map[string]string `json:"mapping"`
		}](t, body).Mapping
		if m2["id"] != mappingID || m2["external_id"] != "ext-2" {
			t.Errorf("got = %v, want = %s updated", m2, mappingID)
		}
	})
	t.Run("missing mappable", func(t *testing.T) {
		status, body := s.REST(t, s.Session, http.MethodPut, base+"/mappings",
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
		status, body := s.REST(t, s.Session, http.MethodPut, base+"/collection_mappings",
			`{"mapping_type":"COLLECTION_MAPPING_TYPE_CURRENCIES","currencies":{"USD":"799344"}}`)
		cm := decode[struct {
			CollectionMapping struct {
				ID         string            `json:"id"`
				Currencies map[string]string `json:"currencies"`
			} `json:"collection_mapping"`
		}](t, body).CollectionMapping
		if status != http.StatusOK || cm.Currencies["USD"] != "799344" {
			t.Fatalf("got = %d %s, want = the currencies mapping", status, body)
		}
		t.Log("source: spec/graphql/resolvers/integration_collection_mappings_resolver_spec.rb:29")
		res, err := integrationsClient(s, s.Session).ListCollectionMappings(t.Context(), integrationv1.ListCollectionMappingsRequest_builder{
			IntegrationId: new(ns.ID),
		}.Build())
		if err != nil || len(res.GetCollectionMappings()) != 1 {
			t.Errorf("got = %v (%v), want = 1", res, err)
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
