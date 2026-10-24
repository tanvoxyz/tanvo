package integrations_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// fakeOAuth is an OAuth gateway granting the token "tok" for the code
// "code".
type fakeOAuth struct{ p integration.Provider }

func (g fakeOAuth) Provider() integration.Provider                { return g.p }
func (fakeOAuth) ValidateConnection(integration.Connection) error { return nil }

func (fakeOAuth) AuthorizationURL(_ integration.Connection, redirectURI, state string) (string, error) {
	return "https://provider.test/authorize?redirect_uri=" + redirectURI + "&state=" + state, nil
}

func (fakeOAuth) ExchangeCode(_ context.Context, _ integration.Connection, redirectURI, code string) (integration.ConnectionUpdate, error) {
	if code != "code" || redirectURI != "https://app.test/callback" {
		return integration.ConnectionUpdate{}, errors.New("bad code")
	}
	return integration.ConnectionUpdate{Secrets: map[string]string{"access_token": "tok"}}, nil
}

func TestGocardlessOAuth(t *testing.T) {
	// source: app/services/payment_providers/gocardless_service.rb:10-11
	ctx := t.Context()
	h := billingtest.New(t, now)
	set, err := port.NewIntegrationProviderSet(fakeOAuth{integration.ProviderGocardless})
	if err != nil {
		t.Fatal(err)
	}
	svc := integrations.New(h.Deps, sealer{}, integrations.WithProviders(set))
	i, err := svc.Create(ctx, h.OrgID(), integrations.Input{Code: "gc", Name: "GoCardless", Provider: integration.ProviderGocardless})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	start, err := svc.StartOAuth(ctx, h.OrgID(), i.ID, "https://app.test/callback")
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	if _, err := svc.CompleteOAuth(ctx, h.OrgID(), i.ID, "code", "other"); err == nil {
		t.Errorf("CompleteOAuth with another state: got = nil, want = an error")
	}
	got, err := svc.CompleteOAuth(ctx, h.OrgID(), i.ID, "code", start.State)
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	secrets, err := svc.Secrets(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if secrets["access_token"] != "tok" || len(secrets["webhook_secret"]) != 50 {
		t.Errorf("secrets: got = %v, want = access_token tok and the webhook secret", secrets)
	}
	if want := []string{kernel.JobRegisterProviderWebhook}; !slices.Equal(h.JobKinds(), want) {
		t.Errorf("jobs: got = %v, want = %v", h.JobKinds(), want)
	}
}
