package moneyhash

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func TestValidateConnection(t *testing.T) {
	g := New()
	for _, tt := range []struct {
		name     string
		settings string
		secrets  string
		field    string
	}{
		// source: spec/models/payment_providers/moneyhash_provider_spec.rb:26
		// source: spec/models/payment_providers/moneyhash_provider_spec.rb:36
		{"valid", `{"flow_id":"test_flow_id"}`, `{"api_key":"k"}`, ""},
		// source: spec/models/payment_providers/moneyhash_provider_spec.rb:11
		{"api key required", `{"flow_id":"test_flow_id"}`, `{}`, "api_key"},
		// source: spec/models/payment_providers/moneyhash_provider_spec.rb:12
		{"flow id required", `{}`, `{"api_key":"k"}`, "flow_id"},
		// source: spec/models/payment_providers/moneyhash_provider_spec.rb:13
		{"flow id at most 20", `{"flow_id":"` + strings.Repeat("f", 21) + `"}`, `{"api_key":"k"}`, "flow_id"},
		{"flow id of 20", `{"flow_id":"` + strings.Repeat("f", 20) + `"}`, `{"api_key":"k"}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := g.ValidateConnection(connection(tt.settings, tt.secrets))
			if tt.field == "" {
				if err != nil {
					t.Fatalf("ValidateConnection: got = %v, want = nil", err)
				}
				return
			}
			var ve *core.ValidationError
			if !errors.As(err, &ve) || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("ValidateConnection: got = %v, want = an error on %s", err, tt.field)
			}
		})
	}
	if err := g.ValidateConnection(connection(`{`, `{}`)); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("malformed settings: got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestEndpoint(t *testing.T) {
	g := New()
	// source: spec/models/payment_providers/moneyhash_provider_spec.rb:47
	if got := g.endpoint(settings{}); got != LiveURL || LiveURL != "https://web.moneyhash.io" {
		t.Errorf("live endpoint: got = %q, want = %q", got, "https://web.moneyhash.io")
	}
	// source: spec/models/payment_providers/moneyhash_provider_spec.rb:57
	if got := g.endpoint(settings{Sandbox: true}); got != SandboxURL || SandboxURL != "https://staging-web.moneyhash.io" {
		t.Errorf("sandbox endpoint: got = %q, want = %q", got, "https://staging-web.moneyhash.io")
	}
	if got := New(WithBaseURL("http://x")).endpoint(settings{Sandbox: true}); got != "http://x" {
		t.Errorf("overridden endpoint: got = %q, want = %q", got, "http://x")
	}
}

func TestRegisterWebhook(t *testing.T) {
	t.Run("fetches the signature key", func(t *testing.T) {
		// source: spec/services/payment_providers/moneyhash_service_spec.rb:20
		rp := newReplay(t, map[string][]reply{routeSignatureKey: {fixture("webhook_signature_response.json")}})
		conn := connection(`{"flow_id":"`+testFlowID+`"}`, `{"api_key":"`+testAPIKey+`"}`)
		up, err := rp.gateway().RegisterWebhook(context.Background(), conn, testWebhookURL)
		if err != nil {
			t.Fatalf("RegisterWebhook: %v", err)
		}
		var sc map[string]string
		if err := json.Unmarshal(up.Secrets, &sc); err != nil {
			t.Fatalf("decode secrets: %v", err)
		}
		if want := "oVZo2bro-a7UBN3k9QFlsSz6cN_Zrdt9KCWk4_5q2lo="; sc["signature_key"] != want {
			t.Errorf("signature_key: got = %q, want = %q", sc["signature_key"], want)
		}
		if sc["api_key"] != testAPIKey {
			t.Errorf("api_key: got = %q, want = %q", sc["api_key"], testAPIKey)
		}
		if r := rp.only(routeSignatureKey); r.APIKey != testAPIKey {
			t.Errorf("x-Api-Key: got = %q, want = %q", r.APIKey, testAPIKey)
		}
	})
	t.Run("keeps an existing signature key", func(t *testing.T) {
		// source: spec/services/payment_providers/moneyhash_service_spec.rb:34
		rp := newReplay(t, nil)
		up, err := rp.gateway().RegisterWebhook(context.Background(), defaultConn(), testWebhookURL)
		if err != nil {
			t.Fatalf("RegisterWebhook: %v", err)
		}
		if up.Secrets != nil || rp.count() != 0 {
			t.Errorf("update: got = %s with %d requests, want = none", up.Secrets, rp.count())
		}
	})
	t.Run("missing api key", func(t *testing.T) {
		rp := newReplay(t, nil)
		_, err := rp.gateway().RegisterWebhook(context.Background(), connection(`{}`, `{}`), testWebhookURL)
		if pe, ok := payment.AsProviderError(err); !ok || pe.Kind != payment.KindAuthentication {
			t.Errorf("RegisterWebhook: got = %v, want = an authentication error", err)
		}
	})
	if err := New().RefreshWebhook(context.Background(), defaultConn(), testWebhookURL); err != nil {
		t.Errorf("RefreshWebhook: got = %v, want = nil", err)
	}
}
