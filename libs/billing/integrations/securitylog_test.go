package integrations_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

type logEntry struct {
	event     string
	resources map[string]any
}

type securityLog struct {
	entries []logEntry
	err     error
}

func (l *securityLog) RecordIntegration(_ context.Context, _ core.OrganizationID, event string, resources map[string]any) error {
	if l.err != nil {
		return l.err
	}
	l.entries = append(l.entries, logEntry{event, resources})
	return nil
}

func TestSecurityLog(t *testing.T) {
	ctx := context.Background()
	newLogged := func(t *testing.T) (*securityLog, *integrations.Service, core.OrganizationID) {
		t.Helper()
		h, svc := newService(t)
		l := &securityLog{}
		return l, svc.WithSecurityLog(l), h.OrgID()
	}
	stripeInput := integrations.Input{
		Code: "stripe1", Name: "Stripe 1", Provider: integration.ProviderStripe,
		Values: map[string]string{integrations.KeySecretKey: "sk_test"},
	}

	t.Run("create, update and delete", func(t *testing.T) {
		// source: spec/services/integrations/xero/update_service_spec.rb:64
		// source: spec/graphql/mutations/integrations/destroy_spec.rb:41
		// source: spec/services/integrations/destroy_service_spec.rb:22
		l, svc, org := newLogged(t)
		i, err := svc.Create(ctx, org, integrations.Input{
			Code: "xero1", Name: "Xero 1", Provider: integration.ProviderXero, SyncInvoices: true,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := svc.Update(ctx, org, i.ID, integrations.Update{
			Name: core.Some("Xero 2"), SyncPayments: core.Some(true),
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if _, err := svc.Update(ctx, org, i.ID, integrations.Update{}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if _, err := svc.Delete(ctx, org, i.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		before := map[string]any{"sync_credit_notes": false, "sync_invoices": true, "sync_payments": false}
		after := map[string]any{"sync_credit_notes": false, "sync_invoices": true, "sync_payments": true}
		want := []logEntry{
			{integrations.EventCreated, map[string]any{"integration_name": "Xero 1", "integration_type": "xero"}},
			{integrations.EventUpdated, map[string]any{
				"integration_name": "Xero 2", "integration_type": "xero",
				"name":     map[string]any{"deleted": "Xero 1", "added": "Xero 2"},
				"settings": map[string]any{"deleted": before, "added": after},
			}},
			{integrations.EventUpdated, map[string]any{"integration_name": "Xero 2", "integration_type": "xero"}},
			{integrations.EventDeleted, map[string]any{"integration_name": "Xero 2", "integration_type": "xero"}},
		}
		if !reflect.DeepEqual(l.entries, want) {
			t.Errorf("got = %v, want = %v", l.entries, want)
		}
	})

	t.Run("payment provider", func(t *testing.T) {
		// source: spec/services/payment_providers/stripe_service_spec.rb:119
		// source: spec/services/payment_providers/destroy_service_spec.rb:18
		l, svc, org := newLogged(t)
		i, err := svc.Create(ctx, org, stripeInput)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := svc.Update(ctx, org, i.ID, integrations.Update{
			Code: core.Some("stripe2"), Values: map[string]string{integrations.KeySuccessRedirectURL: "https://example.com/done"},
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if _, err := svc.Delete(ctx, org, i.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		want := []logEntry{
			{integrations.EventCreated, map[string]any{"integration_name": "Stripe 1", "integration_type": "stripe"}},
			{integrations.EventUpdated, map[string]any{
				"integration_name": "Stripe 1", "integration_type": "stripe",
				"code": map[string]any{"deleted": "stripe1", "added": "stripe2"},
				"settings": map[string]any{
					"deleted": map[string]any{},
					"added":   map[string]any{"success_redirect_url": "https://example.com/done"},
				},
			}},
			{integrations.EventDeleted, map[string]any{"integration_name": "Stripe 1", "integration_type": "stripe"}},
		}
		if !reflect.DeepEqual(l.entries, want) {
			t.Errorf("got = %v, want = %v", l.entries, want)
		}
	})

	t.Run("failed change logs nothing", func(t *testing.T) {
		l, svc, org := newLogged(t)
		in := stripeInput
		in.Name = ""
		if _, err := svc.Create(ctx, org, in); err == nil {
			t.Fatal("Create: got = nil, want = an error")
		}
		if _, err := svc.Delete(ctx, org, core.NewID[core.IntegrationID](core.UUIDv7Generator{})); err == nil {
			t.Fatal("Delete: got = nil, want = an error")
		}
		if len(l.entries) != 0 {
			t.Errorf("got = %v, want = no entries", l.entries)
		}
	})

	t.Run("log failure rolls back", func(t *testing.T) {
		l, svc, org := newLogged(t)
		l.err = errors.New("log down")
		if _, err := svc.Create(ctx, org, stripeInput); !errors.Is(err, l.err) {
			t.Fatalf("Create: got = %v, want = %v", err, l.err)
		}
		if _, err := svc.GetByCode(ctx, org, "stripe1"); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("GetByCode: got = %v, want = not found", err)
		}
	})
}
