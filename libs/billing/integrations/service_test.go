package integrations_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var now = time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)

// sealer hex-encodes behind the organization ID, so sealed values neither
// show the plaintext nor open for another organization.
type sealer struct{}

func (sealer) Seal(_ context.Context, org core.OrganizationID, p []byte) ([]byte, error) {
	return []byte(org.String() + ":" + hex.EncodeToString(p)), nil
}

func (sealer) Open(_ context.Context, org core.OrganizationID, s []byte) ([]byte, error) {
	rest, ok := bytes.CutPrefix(s, []byte(org.String()+":"))
	if !ok {
		return nil, errors.New("wrong organization")
	}
	return hex.DecodeString(string(rest))
}

func newService(t *testing.T) (*billingtest.Harness, *integrations.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	return h, integrations.New(h.Deps, sealer{})
}

func fieldCodes(err error) map[string]string {
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		return nil
	}
	out := map[string]string{}
	for _, e := range ve.Errors {
		out[e.Field] = e.Code
	}
	return out
}

func netsuiteInput() integrations.Input {
	return integrations.Input{
		Code: "netsuite1", Name: "Netsuite 1", Provider: integration.ProviderNetsuite,
		Values: map[string]string{
			"client_id": "cl1", "client_secret": "secret", "token_id": "xyz", "token_secret": "zyx",
			"account_id": "Acc 1", "script_endpoint_url": "https://example.com/restlet",
		},
	}
}

func TestCreate(t *testing.T) {
	ctx := context.Background()
	t.Run("netsuite", func(t *testing.T) {
		// source: spec/services/integrations/netsuite/create_service_spec.rb:61
		h, svc := newService(t)
		i, err := svc.Create(ctx, h.OrgID(), netsuiteInput())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if i.Category != integration.CategoryAccounting || i.Name != "Netsuite 1" {
			t.Errorf("got = %s %q, want = accounting \"Netsuite 1\"", i.Category, i.Name)
		}
		settings, _ := integrations.Settings(i)
		secrets, err := svc.Secrets(ctx, i)
		if err != nil {
			t.Fatalf("Secrets: %v", err)
		}
		if settings["token_id"] != "xyz" || secrets["token_secret"] != "zyx" || secrets["client_secret"] != "secret" {
			t.Errorf("got = %v %v, want = token_id xyz, token_secret zyx", settings, secrets)
		}
		// netsuite_integration.rb:16-18.
		if settings["account_id"] != "acc-1" {
			t.Errorf("account_id: got = %q, want = acc-1", settings["account_id"])
		}
		if _, ok := settings["token_secret"]; ok || bytes.Contains(i.Secrets, []byte("zyx")) {
			t.Error("secret stored in plaintext")
		}
	})
	t.Run("missing name", func(t *testing.T) {
		// source: spec/services/integrations/netsuite/create_service_spec.rb:93
		h, svc := newService(t)
		in := netsuiteInput()
		in.Name = ""
		_, err := svc.Create(ctx, h.OrgID(), in)
		if got := fieldCodes(err)["name"]; got != core.CodeRequired {
			t.Errorf("got = %q (%v), want = %q", got, err, core.CodeRequired)
		}
	})
	t.Run("missing credentials", func(t *testing.T) {
		// source: spec/services/integrations/anrok/create_service_spec.rb:61
		h, svc := newService(t)
		_, err := svc.Create(ctx, h.OrgID(), integrations.Input{Code: "anrok", Name: "Anrok", Provider: integration.ProviderAnrok})
		if got := fieldCodes(err)["api_key"]; got != core.CodeRequired {
			t.Errorf("got = %q (%v), want = %q", got, err, core.CodeRequired)
		}
	})
	t.Run("unknown key", func(t *testing.T) {
		h, svc := newService(t)
		_, err := svc.Create(ctx, h.OrgID(), integrations.Input{
			Code: "anrok", Name: "Anrok", Provider: integration.ProviderAnrok,
			Values: map[string]string{"api_key": "k", "flow_id": "x"},
		})
		if got := fieldCodes(err)["flow_id"]; got != core.CodeForbidden {
			t.Errorf("got = %q (%v), want = %q", got, err, core.CodeForbidden)
		}
	})
	t.Run("invalid url", func(t *testing.T) {
		// source: app/models/payment_providers/stripe_provider.rb:13 (UrlValidator)
		h, svc := newService(t)
		_, err := svc.Create(ctx, h.OrgID(), integrations.Input{
			Code: "stripe", Name: "Stripe", Provider: integration.ProviderStripe,
			Values: map[string]string{"secret_key": "sk", "success_redirect_url": "not a url"},
		})
		if got := fieldCodes(err)["success_redirect_url"]; got != integrations.CodeURLInvalid {
			t.Errorf("got = %q (%v), want = %q", got, err, integrations.CodeURLInvalid)
		}
	})
	t.Run("sync not supported", func(t *testing.T) {
		h, svc := newService(t)
		_, err := svc.Create(ctx, h.OrgID(), integrations.Input{
			Code: "anrok", Name: "Anrok", Provider: integration.ProviderAnrok,
			Values: map[string]string{"api_key": "k"}, SyncInvoices: true,
		})
		if got := fieldCodes(err)["sync_invoices"]; got != integration.CodeSyncNotSupported {
			t.Errorf("got = %q (%v), want = %q", got, err, integration.CodeSyncNotSupported)
		}
	})
	t.Run("duplicate code", func(t *testing.T) {
		// source: app/models/integrations/base_integration.rb:16 (code uniqueness)
		h, svc := newService(t)
		if _, err := svc.Create(ctx, h.OrgID(), netsuiteInput()); err != nil {
			t.Fatalf("Create: %v", err)
		}
		_, err := svc.Create(ctx, h.OrgID(), netsuiteInput())
		if got := fieldCodes(err)["code"]; got != core.CodeAlreadyExists {
			t.Errorf("got = %q (%v), want = %q", got, err, core.CodeAlreadyExists)
		}
	})
	t.Run("gocardless webhook secret", func(t *testing.T) {
		// source: app/services/payment_providers/gocardless_service.rb:37
		h, svc := newService(t)
		i, err := svc.Create(ctx, h.OrgID(), integrations.Input{Code: "gc", Name: "GoCardless", Provider: integration.ProviderGocardless})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		secrets, _ := svc.Secrets(ctx, i)
		if len(secrets["webhook_secret"]) != 50 {
			t.Errorf("webhook_secret: got = %q, want = 50 alphanumerics", secrets["webhook_secret"])
		}
	})
	t.Run("flutterwave webhook secret", func(t *testing.T) {
		// source: app/services/payment_providers/flutterwave_service.rb:26
		h, svc := newService(t)
		i, err := svc.Create(ctx, h.OrgID(), integrations.Input{
			Code: "fw", Name: "Flutterwave", Provider: integration.ProviderFlutterwave,
			Values: map[string]string{"secret_key": "FLWSECK"},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		secrets, _ := svc.Secrets(ctx, i)
		if len(secrets["webhook_secret"]) != 64 {
			t.Errorf("webhook_secret: got = %q, want = 64 hex digits", secrets["webhook_secret"])
		}
	})
	t.Run("moneyhash flow id length", func(t *testing.T) {
		// source: app/models/payment_providers/moneyhash_provider.rb:9
		h, svc := newService(t)
		_, err := svc.Create(ctx, h.OrgID(), integrations.Input{
			Code: "mh", Name: "Moneyhash", Provider: integration.ProviderMoneyhash,
			Values: map[string]string{"api_key": "k", "flow_id": "123456789012345678901"},
		})
		if got := fieldCodes(err)["flow_id"]; got != core.CodeTooLong {
			t.Errorf("got = %q (%v), want = %q", got, err, core.CodeTooLong)
		}
	})
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	t.Run("netsuite", func(t *testing.T) {
		// source: spec/services/integrations/netsuite/update_service_spec.rb:53
		h, svc := newService(t)
		i, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
		got, err := svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{
			Name: core.Some("Netsuite 2"), Values: map[string]string{"script_endpoint_url": "https://example.com/v2"},
			SyncInvoices: core.Some(true),
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		settings, _ := integrations.Settings(got)
		secrets, _ := svc.Secrets(ctx, got)
		if got.Name != "Netsuite 2" || settings["script_endpoint_url"] != "https://example.com/v2" || !got.SyncInvoices {
			t.Errorf("got = %q %v %v, want = updated", got.Name, settings, got.SyncInvoices)
		}
		if secrets["token_secret"] != "zyx" {
			t.Errorf("token_secret: got = %q, want = zyx (kept)", secrets["token_secret"])
		}
	})
	t.Run("missing name", func(t *testing.T) {
		// source: spec/services/integrations/netsuite/update_service_spec.rb:82
		h, svc := newService(t)
		i, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
		_, err := svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{Name: core.Some("")})
		if got := fieldCodes(err)["name"]; got != core.CodeRequired {
			t.Errorf("got = %q (%v), want = %q", got, err, core.CodeRequired)
		}
	})
	t.Run("immutable key", func(t *testing.T) {
		// source: app/services/integrations/netsuite/update_service.rb:23-28
		h, svc := newService(t)
		i, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
		_, err := svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{Values: map[string]string{"token_secret": "new"}})
		if got := fieldCodes(err)["token_secret"]; got != core.CodeImmutable {
			t.Errorf("got = %q (%v), want = %q", got, err, core.CodeImmutable)
		}
	})
	t.Run("anrok api key", func(t *testing.T) {
		// source: spec/services/integrations/anrok/update_service_spec.rb:38
		h, svc := newService(t)
		i, _ := svc.Create(ctx, h.OrgID(), integrations.Input{
			Code: "anrok", Name: "Anrok", Provider: integration.ProviderAnrok, Values: map[string]string{"api_key": "old"},
		})
		got, err := svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{Values: map[string]string{"api_key": "new"}})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		secrets, _ := svc.Secrets(ctx, got)
		if secrets["api_key"] != "new" {
			t.Errorf("api_key: got = %q, want = new", secrets["api_key"])
		}
	})
	t.Run("required key cleared", func(t *testing.T) {
		// source: spec/services/integrations/anrok/update_service_spec.rb:57
		h, svc := newService(t)
		i, _ := svc.Create(ctx, h.OrgID(), integrations.Input{
			Code: "anrok", Name: "Anrok", Provider: integration.ProviderAnrok, Values: map[string]string{"api_key": "old"},
		})
		_, err := svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{Values: map[string]string{"api_key": ""}})
		if got := fieldCodes(err)["api_key"]; got != core.CodeRequired {
			t.Errorf("got = %q (%v), want = %q", got, err, core.CodeRequired)
		}
	})
	t.Run("not found", func(t *testing.T) {
		h, svc := newService(t)
		_, err := svc.Update(ctx, h.OrgID(), core.NewID[core.IntegrationID](h.IDs), integrations.Update{})
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("got = %v, want = not found", err)
		}
	})
}

func TestDeleteAndList(t *testing.T) {
	ctx := context.Background()
	// source: spec/services/integrations/destroy_service_spec.rb:14
	h, svc := newService(t)
	a, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
	b, err := svc.Create(ctx, h.OrgID(), integrations.Input{Code: "xero", Name: "Xero", Provider: integration.ProviderXero})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	p, err := svc.List(ctx, h.OrgID(), integration.Filter{}, core.PageRequest{})
	if err != nil || len(p.Items) != 2 {
		t.Fatalf("List: got = %d (%v), want = 2", len(p.Items), err)
	}
	if _, err := svc.GetByCode(ctx, h.OrgID(), "xero"); err != nil {
		t.Errorf("GetByCode: %v", err)
	}
	if _, err := svc.Delete(ctx, h.OrgID(), a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, h.OrgID(), a.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get after delete: got = %v, want = not found", err)
	}
	p, _ = svc.List(ctx, h.OrgID(), integration.Filter{}, core.PageRequest{})
	if len(p.Items) != 1 || p.Items[0].ID != b.ID {
		t.Errorf("List after delete: got = %d items, want = the xero integration", len(p.Items))
	}
}

func TestMappings(t *testing.T) {
	ctx := context.Background()
	h, svc := newService(t)
	i, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
	addOn := h.AddOn(100)
	t.Run("create and replace", func(t *testing.T) {
		// source: spec/services/integration_mappings/create_service_spec.rb:26
		// source: spec/services/integration_mappings/update_service_spec.rb:27
		m, err := svc.UpsertMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeAddOn, Mappable: refs.ByID(addOn.ID), ExternalID: "ext-1",
		})
		if err != nil {
			t.Fatalf("UpsertMapping: %v", err)
		}
		again, err := svc.UpsertMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeAddOn, Mappable: refs.ByCode(addOn.Code), ExternalID: "ext-2",
		})
		if err != nil || again.ID != m.ID || again.ExternalID != "ext-2" {
			t.Errorf("got = %v %v (%v), want = %v ext-2", again.ID, again.ExternalID, err, m.ID)
		}
	})
	t.Run("billing entity", func(t *testing.T) {
		// source: spec/services/integration_mappings/create_service_spec.rb:54
		m, err := svc.UpsertMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeAddOn, Mappable: refs.ByID(addOn.ID), ExternalID: "ext-be",
			BillingEntity: refs.ByCode(h.BillingEntity.Code),
		})
		if err != nil || m.BillingEntityID != h.BillingEntity.ID {
			t.Fatalf("got = %v (%v), want = %v", m, err, h.BillingEntity.ID)
		}
		p, err := svc.Mappings(ctx, h.OrgID(), i.ID, integrations.MappingFilter{BillingEntity: refs.ByID(h.BillingEntity.ID)}, core.PageRequest{})
		if err != nil || len(p.Items) != 1 || p.Items[0].ID != m.ID {
			t.Errorf("Mappings: got = %d (%v), want = 1", len(p.Items), err)
		}
		all, _ := svc.Mappings(ctx, h.OrgID(), i.ID, integrations.MappingFilter{MappableType: integration.MappableTypeAddOn}, core.PageRequest{})
		if len(all.Items) != 2 {
			t.Errorf("Mappings: got = %d, want = 2", len(all.Items))
		}
	})
	t.Run("billing entity of another organization", func(t *testing.T) {
		// source: spec/services/integration_mappings/create_service_spec.rb:66
		_, err := svc.UpsertMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeAddOn, Mappable: refs.ByID(addOn.ID), ExternalID: "x",
			BillingEntity: refs.Ref{ID: uuid.New()},
		})
		var nf *core.NotFoundError
		if !errors.As(err, &nf) || nf.Resource != integrations.ResourceBillingEntity {
			t.Errorf("got = %v, want = billing_entity not found", err)
		}
	})
	t.Run("missing mappable", func(t *testing.T) {
		// source: spec/services/integration_mappings/create_service_spec.rb:78
		_, err := svc.UpsertMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeBillableMetric, Mappable: refs.ByCode("nope"), ExternalID: "x",
		})
		if got := fieldCodes(err)["mappable"]; got != integrations.CodeRelationMustExist {
			t.Errorf("got = %q (%v), want = %q", got, err, integrations.CodeRelationMustExist)
		}
	})
	t.Run("metric and delete", func(t *testing.T) {
		// source: spec/services/integration_mappings/destroy_service_spec.rb:14
		metric := h.Metric("calls", catalog.AggregationTypeCount, "")
		m, err := svc.UpsertMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeBillableMetric, Mappable: refs.ByID(metric.ID), ExternalID: "m",
		})
		if err != nil {
			t.Fatalf("UpsertMapping: %v", err)
		}
		if err := svc.DeleteMapping(ctx, h.OrgID(), i.ID, m.ID); err != nil {
			t.Fatalf("DeleteMapping: %v", err)
		}
		if err := svc.DeleteMapping(ctx, h.OrgID(), i.ID, m.ID); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("got = %v, want = not found", err)
		}
	})
	t.Run("provider without mappings", func(t *testing.T) {
		// source: app/services/integration_mappings/factory.rb:6-17
		hub, err := svc.Create(ctx, h.OrgID(), integrations.Input{
			Code: "hub", Name: "Hubspot", Provider: integration.ProviderHubspot,
			Values: map[string]string{"default_targeted_object": "companies"},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		_, err = svc.UpsertMapping(ctx, h.OrgID(), hub.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeAddOn, Mappable: refs.ByID(addOn.ID), ExternalID: "x",
		})
		var re *core.RuleError
		if !errors.As(err, &re) || re.Code != integrations.CodeMappingNotSupported {
			t.Errorf("got = %v, want = %s", err, integrations.CodeMappingNotSupported)
		}
	})
}

func TestCollectionMappings(t *testing.T) {
	ctx := context.Background()
	h, svc := newService(t)
	i, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
	ext := "ext"
	t.Run("create and replace", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/create_service_spec.rb:25
		// source: spec/services/integration_collection_mappings/update_service_spec.rb:28
		m, err := svc.UpsertCollectionMapping(ctx, h.OrgID(), i.ID, integrations.CollectionMappingInput{
			Type: integration.CollectionMappingTypeFallbackItem, ExternalID: &ext,
		})
		if err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		name := "Fallback"
		again, err := svc.UpsertCollectionMapping(ctx, h.OrgID(), i.ID, integrations.CollectionMappingInput{
			Type: integration.CollectionMappingTypeFallbackItem, ExternalID: &ext, ExternalName: &name,
		})
		if err != nil || again.ID != m.ID || again.ExternalName == nil || *again.ExternalName != name {
			t.Errorf("got = %v (%v), want = %v renamed", again, err, m.ID)
		}
	})
	t.Run("billing entity", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/create_service_spec.rb:59
		m, err := svc.UpsertCollectionMapping(ctx, h.OrgID(), i.ID, integrations.CollectionMappingInput{
			Type: integration.CollectionMappingTypeFallbackItem, ExternalID: &ext, BillingEntity: refs.ByID(h.BillingEntity.ID),
		})
		if err != nil || m.BillingEntityID != h.BillingEntity.ID {
			t.Fatalf("got = %v (%v), want = billing entity", m, err)
		}
		all, _ := svc.CollectionMappings(ctx, h.OrgID(), i.ID, "", refs.Ref{})
		be, _ := svc.CollectionMappings(ctx, h.OrgID(), i.ID, "", refs.ByID(h.BillingEntity.ID))
		if len(all) != 2 || len(be) != 1 {
			t.Errorf("got = %d, %d, want = 2, 1", len(all), len(be))
		}
	})
	t.Run("non-existent billing entity", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/create_service_spec.rb:89
		_, err := svc.UpsertCollectionMapping(ctx, h.OrgID(), i.ID, integrations.CollectionMappingInput{
			Type: integration.CollectionMappingTypeFallbackItem, BillingEntity: refs.ByCode("nope"),
		})
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("got = %v, want = not found", err)
		}
	})
	t.Run("currencies", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/update_service_spec.rb:53
		m, err := svc.UpsertCollectionMapping(ctx, h.OrgID(), i.ID, integrations.CollectionMappingInput{
			Type: integration.CollectionMappingTypeCurrencies, Currencies: map[string]string{"USD": "799344"},
		})
		if err != nil || m.Currencies["USD"] != "799344" {
			t.Fatalf("got = %v (%v), want = USD 799344", m, err)
		}
	})
	t.Run("invalid currencies", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/create_service_spec.rb:124
		_, err := svc.UpsertCollectionMapping(ctx, h.OrgID(), i.ID, integrations.CollectionMappingInput{
			Type: integration.CollectionMappingTypeCurrencies, Currencies: map[string]string{"yolo": "true"},
		})
		if got := fieldCodes(err)["currencies"]; got != integrations.CodeInvalidFormat {
			t.Errorf("got = %q (%v), want = %q", got, err, integrations.CodeInvalidFormat)
		}
	})
	t.Run("currencies per billing entity", func(t *testing.T) {
		// source: app/models/integration_collection_mappings/netsuite_collection_mapping.rb:13-17
		_, err := svc.UpsertCollectionMapping(ctx, h.OrgID(), i.ID, integrations.CollectionMappingInput{
			Type: integration.CollectionMappingTypeCurrencies, Currencies: map[string]string{"USD": "1"},
			BillingEntity: refs.ByID(h.BillingEntity.ID),
		})
		if got := fieldCodes(err)["billing_entity"]; got != "value_must_be_blank" {
			t.Errorf("got = %q (%v), want = value_must_be_blank", got, err)
		}
	})
	t.Run("delete", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/destroy_service_spec.rb:14
		all, _ := svc.CollectionMappings(ctx, h.OrgID(), i.ID, integration.CollectionMappingTypeCurrencies, refs.Ref{})
		if len(all) != 1 {
			t.Fatalf("got = %d, want = 1", len(all))
		}
		if err := svc.DeleteCollectionMapping(ctx, h.OrgID(), i.ID, all[0].ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		left, _ := svc.CollectionMappings(ctx, h.OrgID(), i.ID, "", refs.Ref{})
		if slices.ContainsFunc(left, func(m *integration.CollectionMapping) bool { return m.ID == all[0].ID }) {
			t.Error("mapping still listed")
		}
	})
}

func TestItems(t *testing.T) {
	ctx := context.Background()
	// source: spec/queries/integration_items_query_spec.rb (search by name, ID or account code)
	h, svc := newService(t)
	i, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
	code := "4000"
	name := "Consulting"
	h.Do(func(ctx context.Context, r port.Repos) error {
		for k, it := range []*integration.Item{
			{ExternalID: "1", ExternalName: &name, ItemType: integration.ItemTypeStandard},
			{ExternalID: "2", ExternalAccountCode: &code, ItemType: integration.ItemTypeStandard},
		} {
			it.ID = core.NewID[core.IntegrationItemID](h.IDs)
			it.OrganizationID, it.IntegrationID = h.OrgID(), i.ID
			it.CreatedAt = now.Add(time.Duration(k) * time.Second)
			it.UpdatedAt = it.CreatedAt
			if err := r.IntegrationItems.Upsert(ctx, it); err != nil {
				return err
			}
		}
		return nil
	})
	for _, tc := range []struct {
		search string
		want   string
	}{{"consult", "1"}, {"400", "2"}} {
		p, err := svc.Items(ctx, h.OrgID(), i.ID, integration.ItemFilter{Search: tc.search}, core.PageRequest{})
		if err != nil || len(p.Items) != 1 || p.Items[0].ExternalID != tc.want {
			t.Errorf("search %q: got = %v (%v), want = item %s", tc.search, p.Items, err, tc.want)
		}
	}
}
