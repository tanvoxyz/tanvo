package integrations_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
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
		// source: spec/models/integrations/netsuite_integration_spec.rb:31
		if settings["account_id"] != "acc-1" {
			t.Errorf("account_id: got = %q, want = acc-1", settings["account_id"])
		}
		// source: spec/models/integrations/netsuite_integration_spec.rb:37
		in := netsuiteInput()
		in.Code, in.Values["account_id"] = "netsuite2", "  THIS is    account  id  "
		if i2, err := svc.Create(ctx, h.OrgID(), in); err != nil {
			t.Errorf("Create: %v", err)
		} else if s, _ := integrations.Settings(i2); s["account_id"] != "this-is-account-id" {
			t.Errorf("account_id: got = %q, want = this-is-account-id", s["account_id"])
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
		// source: app/graphql/types/payment_providers/flutterwave.rb:13
		shown, err := svc.ShownSecrets(ctx, i)
		if err != nil {
			t.Fatalf("ShownSecrets: %v", err)
		}
		if want := map[string]string{"webhook_secret": secrets["webhook_secret"]}; !maps.Equal(shown, want) {
			t.Errorf("ShownSecrets: got = %v, want = %v", shown, want)
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

func TestPaymentConnectionJobs(t *testing.T) {
	ctx := t.Context()
	h, svc := newService(t)
	// source: app/services/payment_providers/stripe_service.rb:37-39
	i, err := svc.Create(ctx, h.OrgID(), integrations.Input{
		Code: "stripe", Name: "Stripe", Provider: integration.ProviderStripe,
		Values: map[string]string{"secret_key": "sk_test"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, want := h.JobKinds(), []string{kernel.JobRegisterProviderWebhook}; !slices.Equal(got, want) {
		t.Errorf("create jobs: got = %v, want = %v", got, want)
	}
	// source: app/services/payment_providers/stripe_service.rb:41-45
	if _, err := svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{Name: core.Some("Stripe 2")}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := len(h.JobKinds()); got != 1 {
		t.Errorf("update without consent change: got = %d jobs, want = 1", got)
	}
	if _, err := svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{
		Values: map[string]string{"require_terms_of_service_consent": "true"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := h.JobKinds(); !slices.Contains(got, kernel.JobExpireConnectionCheckouts) {
		t.Errorf("consent change jobs: got = %v, want = %s", got, kernel.JobExpireConnectionCheckouts)
	}

	// A GoCardless connection waits for its grant.
	h2, svc2 := newService(t)
	if _, err := svc2.Create(ctx, h2.OrgID(), integrations.Input{
		Code: "gc", Name: "GoCardless", Provider: integration.ProviderGocardless,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := h2.JobKinds(); len(got) != 0 {
		t.Errorf("gocardless create jobs: got = %v, want = none", got)
	}
}

func TestDeleteAndList(t *testing.T) {
	ctx := context.Background()
	// source: spec/services/integrations/destroy_service_spec.rb:17
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

	// source: spec/services/integrations/destroy_service_spec.rb:29
	if _, err := svc.Delete(ctx, h.OrgID(), a.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Delete again: got = %v, want = not found", err)
	}
}

// otherBillingEntity stores a second billing entity of the organization.
func otherBillingEntity(t *testing.T, h *billingtest.Harness, code string) *organization.BillingEntity {
	t.Helper()
	be := *h.BillingEntity
	be.ID, be.Code, be.IsDefault = core.NewID[core.BillingEntityID](h.IDs), code, false
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, &be) })
	return &be
}

// secondNetsuite connects a second NetSuite integration.
func secondNetsuite(t *testing.T, h *billingtest.Harness, svc *integrations.Service) *integration.Integration {
	t.Helper()
	in := netsuiteInput()
	in.Code, in.Name = "netsuite2", "Netsuite 2"
	i, err := svc.Create(context.Background(), h.OrgID(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return i
}

func TestMappings(t *testing.T) {
	ctx := context.Background()
	h, svc := newService(t)
	i, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
	addOn := h.AddOn(100)
	var created *integration.Mapping
	t.Run("create", func(t *testing.T) {
		// source: spec/services/integration_mappings/create_service_spec.rb:26, 36
		// source: spec/models/integration_mappings/base_mapping_spec.rb:132
		code, name := "code-1", "Name"
		m, err := svc.CreateMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeAddOn, Mappable: refs.ByID(addOn.ID), ExternalID: "ext-1",
			ExternalAccountCode: &code, ExternalName: &name,
		})
		if err != nil {
			t.Fatalf("CreateMapping: %v", err)
		}
		if m.IntegrationID != i.ID || m.MappableID != addOn.ID.UUID() || m.ExternalID != "ext-1" || *m.ExternalAccountCode != code || *m.ExternalName != name {
			t.Errorf("got = %+v, want = the add-on's mapping", m)
		}
		created = m
	})
	t.Run("update", func(t *testing.T) {
		// source: spec/services/integration_mappings/update_service_spec.rb:25, 35
		code, name := "code-2", "Name1"
		m, err := svc.UpdateMapping(ctx, h.OrgID(), i.ID, created.ID, integrations.MappingUpdate{
			ExternalID: core.Some("456"), ExternalAccountCode: core.Some(&code), ExternalName: core.Some(&name),
		})
		if err != nil {
			t.Fatalf("UpdateMapping: %v", err)
		}
		if m.ID != created.ID || m.ExternalID != "456" || *m.ExternalAccountCode != code || *m.ExternalName != name {
			t.Errorf("got = %+v, want = 456 code-2 Name1", m)
		}
		// Fields not given stay (update_service.rb:17-19).
		other := "Other"
		m, err = svc.UpdateMapping(ctx, h.OrgID(), i.ID, created.ID, integrations.MappingUpdate{ExternalName: core.Some(&other)})
		if err != nil || m.ExternalID != "456" || *m.ExternalAccountCode != code || *m.ExternalName != other {
			t.Errorf("got = %+v (%v), want = 456 code-2 Other", m, err)
		}
		// source: app/services/integration_mappings/update_service.rb:15
		if _, err := svc.UpdateMapping(ctx, h.OrgID(), i.ID, core.NewID[core.IntegrationMappingID](h.IDs), integrations.MappingUpdate{}); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("unknown mapping: got = %v, want = not found", err)
		}
	})
	t.Run("uniqueness", func(t *testing.T) {
		// source: spec/models/integration_mappings/base_mapping_spec.rb:48, 58, 77, 87
		be := otherBillingEntity(t, h, "be-mappings")
		other := secondNetsuite(t, h, svc)
		metric := h.Metric("mapped", catalog.AggregationTypeCount, "")
		for _, tc := range []struct {
			name  string
			integ core.IntegrationID
			in    integrations.MappingInput
		}{
			{"another integration", other.ID, integrations.MappingInput{MappableType: integration.MappableTypeAddOn, Mappable: refs.ByID(addOn.ID)}},
			{"another object", i.ID, integrations.MappingInput{MappableType: integration.MappableTypeBillableMetric, Mappable: refs.ByID(metric.ID)}},
			{"a billing entity", i.ID, integrations.MappingInput{MappableType: integration.MappableTypeAddOn, Mappable: refs.ByID(addOn.ID), BillingEntity: refs.ByID(be.ID)}},
			{"another billing entity", i.ID, integrations.MappingInput{MappableType: integration.MappableTypeAddOn, Mappable: refs.ByID(addOn.ID), BillingEntity: refs.ByID(h.BillingEntity.ID)}},
		} {
			tc.in.ExternalID = "x"
			if _, err := svc.CreateMapping(ctx, h.OrgID(), tc.integ, tc.in); err != nil {
				t.Errorf("%s: got = %v, want = created", tc.name, err)
			}
		}
		for _, ref := range []refs.Ref{{}, refs.ByID(be.ID)} {
			_, err := svc.CreateMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
				MappableType: integration.MappableTypeAddOn, Mappable: refs.ByCode(addOn.Code), ExternalID: "y", BillingEntity: ref,
			})
			if got := fieldCodes(err)["mappable_type"]; got != core.CodeAlreadyExists {
				t.Errorf("duplicate %v: got = %q (%v), want = %s", ref, got, err, core.CodeAlreadyExists)
			}
		}
	})
	t.Run("billing entity", func(t *testing.T) {
		// source: spec/services/integration_mappings/create_service_spec.rb:54
		// source: spec/graphql/mutations/integration_mappings/create_spec.rb:71
		// source: spec/models/integration_mappings/base_mapping_spec.rb:118
		metric := h.Metric("scoped", catalog.AggregationTypeCount, "")
		m, err := svc.CreateMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeBillableMetric, Mappable: refs.ByID(metric.ID), ExternalID: "ext-be",
			BillingEntity: refs.ByCode(h.BillingEntity.Code),
		})
		if err != nil || m.BillingEntityID != h.BillingEntity.ID {
			t.Fatalf("got = %v (%v), want = %v", m, err, h.BillingEntity.ID)
		}
		p, err := svc.Mappings(ctx, h.OrgID(), i.ID, integrations.MappingFilter{
			MappableType: integration.MappableTypeBillableMetric, Mappable: refs.ByID(metric.ID), BillingEntity: refs.ByID(h.BillingEntity.ID),
		}, core.PageRequest{})
		if err != nil || len(p.Items) != 1 || p.Items[0].ID != m.ID {
			t.Errorf("Mappings: got = %d (%v), want = 1", len(p.Items), err)
		}
	})
	t.Run("billing entity of another organization", func(t *testing.T) {
		// The service looks the billing entity up in the organization, so
		// the model's own check is never reached.
		// source: spec/services/integration_mappings/create_service_spec.rb:69
		// source: spec/graphql/mutations/integration_mappings/create_spec.rb:89
		// source: spec/models/integration_mappings/base_mapping_spec.rb:104
		_, err := svc.CreateMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeAddOn, Mappable: refs.ByID(addOn.ID), ExternalID: "x",
			BillingEntity: refs.Ref{ID: uuid.New()},
		})
		var nf *core.NotFoundError
		if !errors.As(err, &nf) || nf.Resource != integrations.ResourceBillingEntity {
			t.Errorf("got = %v, want = billing_entity not found", err)
		}
	})
	t.Run("missing mappable", func(t *testing.T) {
		// source: spec/services/integration_mappings/create_service_spec.rb:86
		_, err := svc.CreateMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeBillableMetric, Mappable: refs.ByCode("nope"), ExternalID: "x",
		})
		if got := fieldCodes(err)["mappable"]; got != integrations.CodeRelationMustExist {
			t.Errorf("got = %q (%v), want = %q", got, err, integrations.CodeRelationMustExist)
		}
	})
	t.Run("metric and delete", func(t *testing.T) {
		// source: spec/services/integration_mappings/destroy_service_spec.rb:18, 27
		metric := h.Metric("calls", catalog.AggregationTypeCount, "")
		m, err := svc.CreateMapping(ctx, h.OrgID(), i.ID, integrations.MappingInput{
			MappableType: integration.MappableTypeBillableMetric, Mappable: refs.ByID(metric.ID), ExternalID: "m",
		})
		if err != nil {
			t.Fatalf("CreateMapping: %v", err)
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
		_, err = svc.CreateMapping(ctx, h.OrgID(), hub.ID, integrations.MappingInput{
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
	fallback := func(be refs.Ref) integrations.CollectionMappingInput {
		return integrations.CollectionMappingInput{Type: integration.CollectionMappingTypeFallbackItem, ExternalID: &ext, BillingEntity: be}
	}
	var created *integration.CollectionMapping
	t.Run("create", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/create_service_spec.rb:25, 39
		// source: spec/models/integration_collection_mappings/base_collection_mapping_spec.rb:118
		nexus := "tax-nexus-1"
		in := fallback(refs.Ref{})
		in.TaxNexus = &nexus
		m, err := svc.CreateCollectionMapping(ctx, h.OrgID(), i.ID, in)
		if err != nil {
			t.Fatalf("CreateCollectionMapping: %v", err)
		}
		if m.Type != integration.CollectionMappingTypeFallbackItem || *m.ExternalID != ext || *m.TaxNexus != nexus || !m.BillingEntityID.IsZero() {
			t.Errorf("got = %+v, want = the fallback item", m)
		}
		created = m
	})
	t.Run("update", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/update_service_spec.rb:28, 42
		// source: spec/graphql/mutations/integration_collection_mappings/update_spec.rb:53
		id, name, code, nexus, taxCode, taxType := "456", "Name1", "code-2", "updated-123", "updated-456", "updated-tax-type-1"
		m, err := svc.UpdateCollectionMapping(ctx, h.OrgID(), i.ID, created.ID, integrations.CollectionMappingUpdate{
			ExternalID: core.Some(&id), ExternalName: core.Some(&name), ExternalAccountCode: core.Some(&code),
			TaxNexus: core.Some(&nexus), TaxCode: core.Some(&taxCode), TaxType: core.Some(&taxType),
		})
		if err != nil {
			t.Fatalf("UpdateCollectionMapping: %v", err)
		}
		if m.ID != created.ID || *m.ExternalID != id || *m.ExternalName != name || *m.ExternalAccountCode != code ||
			*m.TaxNexus != nexus || *m.TaxCode != taxCode || *m.TaxType != taxType {
			t.Errorf("got = %+v, want = the updated values", m)
		}
		// Fields not given stay (update_service.rb:17-26).
		other := "Other"
		m, err = svc.UpdateCollectionMapping(ctx, h.OrgID(), i.ID, created.ID, integrations.CollectionMappingUpdate{ExternalName: core.Some(&other)})
		if err != nil || *m.ExternalID != id || *m.TaxNexus != nexus || *m.ExternalName != other {
			t.Errorf("got = %+v (%v), want = only the name changed", m, err)
		}
		// source: app/services/integration_collection_mappings/update_service.rb:15
		if _, err := svc.UpdateCollectionMapping(ctx, h.OrgID(), i.ID, core.NewID[core.IntegrationCollectionMappingID](h.IDs), integrations.CollectionMappingUpdate{}); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("unknown mapping: got = %v, want = not found", err)
		}
	})
	t.Run("uniqueness", func(t *testing.T) {
		// source: spec/models/integration_collection_mappings/base_collection_mapping_spec.rb:41, 51, 73, 83
		be := otherBillingEntity(t, h, "be-collections")
		other := secondNetsuite(t, h, svc)
		coupon := fallback(refs.Ref{})
		coupon.Type = integration.CollectionMappingTypeCoupon
		for _, tc := range []struct {
			name  string
			integ core.IntegrationID
			in    integrations.CollectionMappingInput
		}{
			{"another type", i.ID, coupon},
			{"another integration", other.ID, fallback(refs.Ref{})},
			{"a billing entity", i.ID, fallback(refs.ByID(be.ID))},
			{"another integration and billing entity", other.ID, fallback(refs.ByID(be.ID))},
		} {
			if _, err := svc.CreateCollectionMapping(ctx, h.OrgID(), tc.integ, tc.in); err != nil {
				t.Errorf("%s: got = %v, want = created", tc.name, err)
			}
		}
		for _, ref := range []refs.Ref{{}, refs.ByID(be.ID)} {
			_, err := svc.CreateCollectionMapping(ctx, h.OrgID(), i.ID, fallback(ref))
			if got := fieldCodes(err)["mapping_type"]; got != core.CodeAlreadyExists {
				t.Errorf("duplicate %v: got = %q (%v), want = %s", ref, got, err, core.CodeAlreadyExists)
			}
		}
	})
	t.Run("billing entity", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/create_service_spec.rb:59
		// source: spec/graphql/mutations/integration_collection_mappings/create_spec.rb:89
		// source: spec/models/integration_collection_mappings/base_collection_mapping_spec.rb:101
		// source: spec/models/integration_collection_mappings/netsuite_collection_mapping_spec.rb:163
		m, err := svc.CreateCollectionMapping(ctx, h.OrgID(), i.ID, fallback(refs.ByID(h.BillingEntity.ID)))
		if err != nil || m.BillingEntityID != h.BillingEntity.ID {
			t.Fatalf("got = %v (%v), want = billing entity", m, err)
		}
		be, _ := svc.CollectionMappings(ctx, h.OrgID(), i.ID, "", refs.ByID(h.BillingEntity.ID))
		if len(be) != 1 || be[0].ID != m.ID {
			t.Errorf("got = %d, want = 1", len(be))
		}
	})
	t.Run("non-existent billing entity", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/create_service_spec.rb:98
		_, err := svc.CreateCollectionMapping(ctx, h.OrgID(), i.ID, fallback(refs.ByCode("nope")))
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("got = %v, want = not found", err)
		}
	})
	t.Run("billing entity of another organization", func(t *testing.T) {
		// The service looks the billing entity up in the organization, so
		// the model's own check is never reached.
		// source: spec/services/integration_collection_mappings/create_service_spec.rb:80
		// source: spec/graphql/mutations/integration_collection_mappings/create_spec.rb:106
		// source: spec/models/integration_collection_mappings/base_collection_mapping_spec.rb:109
		_, err := svc.CreateCollectionMapping(ctx, h.OrgID(), i.ID, fallback(refs.Ref{ID: uuid.New()}))
		var nf *core.NotFoundError
		if !errors.As(err, &nf) || nf.Resource != integrations.ResourceBillingEntity {
			t.Errorf("got = %v, want = billing_entity not found", err)
		}
	})
	t.Run("missing integration", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/create_service_spec.rb:115
		_, err := svc.CreateCollectionMapping(ctx, h.OrgID(), core.NewID[core.IntegrationID](h.IDs), fallback(refs.Ref{}))
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("got = %v, want = not found", err)
		}
	})
	t.Run("currency validations", func(t *testing.T) {
		currencies := func(c map[string]string) integrations.CollectionMappingInput {
			return integrations.CollectionMappingInput{Type: integration.CollectionMappingTypeCurrencies, Currencies: c}
		}
		withFallback := fallback(refs.Ref{})
		withFallback.Currencies = map[string]string{"EUR": "12"}
		emptyFallback := fallback(refs.Ref{})
		emptyFallback.Currencies = map[string]string{}
		tests := []struct {
			name, source string
			in           integrations.CollectionMappingInput
			field, want  string
		}{
			{"nil", "spec/models/integration_collection_mappings/netsuite_collection_mapping_spec.rb:70, 81, 115", currencies(nil), "currencies", core.CodeRequired},
			{"empty", "spec/models/integration_collection_mappings/netsuite_collection_mapping_spec.rb:81", currencies(map[string]string{}), "currencies", integrations.CodeCannotBeEmpty},
			{"unknown currency", "spec/models/integration_collection_mappings/netsuite_collection_mapping_spec.rb:103", currencies(map[string]string{"invalid": "8"}), "currencies", integrations.CodeInvalidFormat},
			{"blank value", "spec/models/integration_collection_mappings/netsuite_collection_mapping_spec.rb:103", currencies(map[string]string{"USD": ""}), "currencies", integrations.CodeInvalidFormat},
			{"lowercase key", "spec/services/integration_collection_mappings/create_service_spec.rb:133", currencies(map[string]string{"yolo": "true"}), "currencies", integrations.CodeInvalidFormat},
			{"on another type", "spec/models/integration_collection_mappings/netsuite_collection_mapping_spec.rb:121", withFallback, "currencies", integrations.CodeMustBeBlank},
			{"empty on another type", "app/models/integration_collection_mappings/netsuite_collection_mapping.rb:21-36", emptyFallback, "currencies", integrations.CodeCannotBeEmpty},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Log("source: " + tt.source)
				_, err := svc.CreateCollectionMapping(ctx, h.OrgID(), i.ID, tt.in)
				if got := fieldCodes(err)[tt.field]; got != tt.want {
					t.Errorf("%s: got = %q (%v), want = %q", tt.field, got, err, tt.want)
				}
			})
		}
	})
	t.Run("currencies per billing entity", func(t *testing.T) {
		// source: spec/models/integration_collection_mappings/netsuite_collection_mapping_spec.rb:142
		_, err := svc.CreateCollectionMapping(ctx, h.OrgID(), i.ID, integrations.CollectionMappingInput{
			Type: integration.CollectionMappingTypeCurrencies, Currencies: map[string]string{"USD": "1"},
			BillingEntity: refs.ByID(h.BillingEntity.ID),
		})
		if got := fieldCodes(err)["billing_entity"]; got != integrations.CodeMustBeBlank {
			t.Errorf("got = %q (%v), want = %s", got, err, integrations.CodeMustBeBlank)
		}
	})
	var cur *integration.CollectionMapping
	t.Run("currencies", func(t *testing.T) {
		// source: spec/models/integration_collection_mappings/netsuite_collection_mapping_spec.rb:150
		m, err := svc.CreateCollectionMapping(ctx, h.OrgID(), i.ID, integrations.CollectionMappingInput{
			Type: integration.CollectionMappingTypeCurrencies, Currencies: map[string]string{"USD": "1"},
		})
		if err != nil {
			t.Fatalf("CreateCollectionMapping: %v", err)
		}
		// source: spec/services/integration_collection_mappings/update_service_spec.rb:53
		if m, err = svc.UpdateCollectionMapping(ctx, h.OrgID(), i.ID, m.ID, integrations.CollectionMappingUpdate{
			Currencies: core.Some(map[string]string{"USD": "799344"}),
		}); err != nil || len(m.Currencies) != 1 || m.Currencies["USD"] != "799344" {
			t.Fatalf("got = %v (%v), want = USD 799344", m, err)
		}
		// source: spec/services/integration_collection_mappings/update_service_spec.rb:61
		_, err = svc.UpdateCollectionMapping(ctx, h.OrgID(), i.ID, m.ID, integrations.CollectionMappingUpdate{
			Currencies: core.Some(map[string]string{"yolo": "true"}),
		})
		if got := fieldCodes(err)["currencies"]; got != integrations.CodeInvalidFormat {
			t.Errorf("got = %q (%v), want = %q", got, err, integrations.CodeInvalidFormat)
		}
		_, err = svc.UpdateCollectionMapping(ctx, h.OrgID(), i.ID, m.ID, integrations.CollectionMappingUpdate{
			Currencies: core.Some(map[string]string{}),
		})
		if got := fieldCodes(err)["currencies"]; got != integrations.CodeCannotBeEmpty {
			t.Errorf("empty: got = %q (%v), want = %q", got, err, integrations.CodeCannotBeEmpty)
		}
		cur = m
	})
	t.Run("another provider", func(t *testing.T) {
		// Only NetSuite mappings have the tax fields and currencies; lago
		// raises NoMethodError on the others (create_service.rb:38-41).
		x, err := svc.Create(ctx, h.OrgID(), integrations.Input{Code: "xero", Name: "Xero", Provider: integration.ProviderXero})
		if err != nil {
			t.Fatalf("Create xero: %v", err)
		}
		nexus := "n"
		in := fallback(refs.Ref{})
		in.TaxNexus, in.Currencies = &nexus, map[string]string{"USD": "1"}
		_, err = svc.CreateCollectionMapping(ctx, h.OrgID(), x.ID, in)
		if got := fieldCodes(err); got["tax_nexus"] != core.CodeForbidden || got["currencies"] != core.CodeForbidden {
			t.Errorf("got = %v, want = tax_nexus and currencies %s", got, core.CodeForbidden)
		}
		// A Xero currencies mapping has no currencies to check.
		if _, err := svc.CreateCollectionMapping(ctx, h.OrgID(), x.ID, integrations.CollectionMappingInput{Type: integration.CollectionMappingTypeCurrencies}); err != nil {
			t.Errorf("xero currencies: got = %v, want = created", err)
		}
	})
	t.Run("delete", func(t *testing.T) {
		// source: spec/services/integration_collection_mappings/destroy_service_spec.rb:18
		if err := svc.DeleteCollectionMapping(ctx, h.OrgID(), i.ID, cur.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		// source: spec/services/integration_collection_mappings/destroy_service_spec.rb:27
		// source: spec/graphql/mutations/integration_collection_mappings/destroy_spec.rb:41
		if err := svc.DeleteCollectionMapping(ctx, h.OrgID(), i.ID, cur.ID); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("delete again: got = %v, want = not found", err)
		}
		left, _ := svc.CollectionMappings(ctx, h.OrgID(), i.ID, "", refs.Ref{})
		if slices.ContainsFunc(left, func(m *integration.CollectionMapping) bool { return m.ID == cur.ID }) {
			t.Error("mapping still listed")
		}
	})
}

func TestItems(t *testing.T) {
	ctx := context.Background()
	// Search by name, ID or account code.
	// source: spec/queries/integration_items_query_spec.rb:103
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

// checkingGateway refuses a connection whose script_endpoint_url is not
// under example.com and records the connections it checked; it calls no
// accounting method.
type checkingGateway struct {
	integration.AccountingGateway
	checked []integration.Connection
}

func (g *checkingGateway) Provider() integration.Provider { return integration.ProviderNetsuite }

func (g *checkingGateway) ValidateConnection(conn integration.Connection) error {
	g.checked = append(g.checked, conn)
	var v core.Validator
	v.Check(strings.HasPrefix(conn.Setting("script_endpoint_url"), "https://example.com/"), "script_endpoint_url", core.CodeInvalid)
	v.Check(conn.Secret("client_secret") == "secret", "client_secret", core.CodeInvalid)
	return v.Err()
}

func TestValidateConnection(t *testing.T) {
	t.Log("source: app/models/integrations/netsuite_integration.rb:5 (validated on create and update)")
	ctx := context.Background()
	h := billingtest.New(t, now)
	gw := &checkingGateway{}
	set, err := port.NewIntegrationProviderSet(gw)
	if err != nil {
		t.Fatal(err)
	}
	svc := integrations.New(h.Deps, sealer{}, integrations.WithProviders(set))
	bad := netsuiteInput()
	bad.Values["script_endpoint_url"] = "https://other.com/restlet"
	if _, err := svc.Create(ctx, h.OrgID(), bad); fieldCodes(err)["script_endpoint_url"] != core.CodeInvalid {
		t.Fatalf("Create: got = %v, want = script_endpoint_url invalid", err)
	}
	i, err := svc.Create(ctx, h.OrgID(), netsuiteInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{Values: map[string]string{"script_endpoint_url": "https://other.com/v2"}})
	if fieldCodes(err)["script_endpoint_url"] != core.CodeInvalid {
		t.Errorf("Update: got = %v, want = script_endpoint_url invalid", err)
	}
	if got := len(gw.checked); got != 3 {
		t.Errorf("checks: got = %d, want = 3", got)
	}
}

func TestItemsQuery(t *testing.T) {
	ctx := context.Background()
	h, svc := newService(t)
	i, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
	other := secondNetsuite(t, h, svc)
	name := "Item"
	store := func(integ core.IntegrationID, id core.IntegrationItemID, ext string, typ integration.ItemType, at time.Time) {
		h.Do(func(ctx context.Context, r port.Repos) error {
			return r.IntegrationItems.Upsert(ctx, &integration.Item{
				ID: id, OrganizationID: h.OrgID(), IntegrationID: integ, ItemType: typ, ExternalID: ext, ExternalName: &name,
				CreatedAt: at, UpdatedAt: at,
			})
		})
	}
	// Same name and creation time: the smaller ID comes first.
	store(i.ID, core.MustParseID[core.IntegrationItemID]("ffffffff-0000-7000-8000-000000000000"), "1", integration.ItemTypeTax, now)
	store(i.ID, core.MustParseID[core.IntegrationItemID]("00000000-0000-7000-8000-000000000000"), "2", integration.ItemTypeStandard, now)
	store(i.ID, core.NewID[core.IntegrationItemID](h.IDs), "3", integration.ItemTypeStandard, now.Add(-time.Hour))
	store(other.ID, core.NewID[core.IntegrationItemID](h.IDs), "4", integration.ItemTypeStandard, now)
	list := func(f integration.ItemFilter, page core.PageRequest) core.Page[*integration.Item] {
		t.Helper()
		p, err := svc.Items(ctx, h.OrgID(), i.ID, f, page)
		if err != nil {
			t.Fatalf("Items: %v", err)
		}
		return p
	}
	ids := func(p core.Page[*integration.Item]) []string {
		var out []string
		for _, it := range p.Items {
			out = append(out, it.ExternalID)
		}
		return out
	}
	t.Run("the integration's items, ordered", func(t *testing.T) {
		// Newest first, then by ID, within one name.
		// source: spec/queries/integration_items_query_spec.rb:53, 79
		if got, want := ids(list(integration.ItemFilter{}, core.PageRequest{})), []string{"2", "1", "3"}; !slices.Equal(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	t.Run("item type", func(t *testing.T) {
		// source: spec/queries/integration_items_query_spec.rb:91
		if got := ids(list(integration.ItemFilter{ItemType: integration.ItemTypeTax}, core.PageRequest{})); !slices.Equal(got, []string{"1"}) {
			t.Errorf("got = %v, want = [1]", got)
		}
	})
	t.Run("pagination", func(t *testing.T) {
		// source: spec/queries/integration_items_query_spec.rb:65
		first := list(integration.ItemFilter{}, core.PageRequest{Size: 2})
		if len(first.Items) != 2 || first.NextToken == "" {
			t.Fatalf("first page: got = %v next %q, want = 2 items and a next page", ids(first), first.NextToken)
		}
		second := list(integration.ItemFilter{}, core.PageRequest{Size: 2, Token: first.NextToken})
		if got := ids(second); !slices.Equal(got, []string{"3"}) || second.NextToken != "" {
			t.Errorf("second page: got = %v next %q, want = [3] and no next page", got, second.NextToken)
		}
	})
}

func TestCollectionMappingsQuery(t *testing.T) {
	// source: spec/queries/integration_collection_mappings_query_spec.rb:60, 72
	ctx := context.Background()
	h, svc := newService(t)
	i, _ := svc.Create(ctx, h.OrgID(), netsuiteInput())
	other := secondNetsuite(t, h, svc)
	ext := "1"
	store := func(integ core.IntegrationID, id core.IntegrationCollectionMappingID, typ integration.CollectionMappingType, at time.Time) {
		h.Do(func(ctx context.Context, r port.Repos) error {
			return r.IntegrationCollectionMappings.Create(ctx, &integration.CollectionMapping{
				ID: id, OrganizationID: h.OrgID(), IntegrationID: integ, Type: typ, ExternalID: &ext, CreatedAt: at, UpdatedAt: at,
			})
		})
	}
	store(i.ID, core.MustParseID[core.IntegrationCollectionMappingID]("ffffffff-0000-7000-8000-000000000000"), integration.CollectionMappingTypeFallbackItem, now)
	store(i.ID, core.MustParseID[core.IntegrationCollectionMappingID]("00000000-0000-7000-8000-000000000000"), integration.CollectionMappingTypeCoupon, now)
	store(i.ID, core.NewID[core.IntegrationCollectionMappingID](h.IDs), integration.CollectionMappingTypeSubscriptionFee, now.Add(time.Hour))
	store(other.ID, core.NewID[core.IntegrationCollectionMappingID](h.IDs), integration.CollectionMappingTypeMinimumCommitment, now)
	got, err := svc.CollectionMappings(ctx, h.OrgID(), i.ID, "", refs.Ref{})
	if err != nil {
		t.Fatalf("CollectionMappings: %v", err)
	}
	var types []integration.CollectionMappingType
	for _, m := range got {
		types = append(types, m.Type)
	}
	want := []integration.CollectionMappingType{
		integration.CollectionMappingTypeSubscriptionFee, integration.CollectionMappingTypeCoupon, integration.CollectionMappingTypeFallbackItem,
	}
	if !slices.Equal(types, want) {
		t.Errorf("got = %v, want = %v (newest first, then by ID)", types, want)
	}
}
