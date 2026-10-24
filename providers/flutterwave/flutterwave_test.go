package flutterwave

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
	long := "https://example.com/" + strings.Repeat("a", MaxSuccessRedirectURLLength)
	for _, tt := range []struct {
		name     string
		settings string
		secrets  string
		field    string
	}{
		// source: spec/services/payment_providers/flutterwave_service_spec.rb:24
		// source: spec/models/payment_providers/flutterwave_provider_spec.rb:12
		{"valid", `{"success_redirect_url":"https://example.com/success"}`, `{"secret_key":"k"}`, ""},
		// source: spec/models/payment_providers/flutterwave_provider_spec.rb:11
		{"no success url", `{}`, `{"secret_key":"k"}`, ""},
		// source: spec/models/payment_providers/flutterwave_provider_spec.rb:9
		// source: spec/services/payment_providers/flutterwave_service_spec.rb:137
		{"secret key required", `{}`, `{}`, "secret_key"},
		// source: spec/models/payment_providers/flutterwave_provider_spec.rb:14
		{"success url too long", `{"success_redirect_url":"` + long + `"}`, `{"secret_key":"k"}`, "success_redirect_url"},
		// source: spec/models/payment_providers/flutterwave_provider_spec.rb:13
		{"success url invalid", `{"success_redirect_url":"invalid-url"}`, `{"secret_key":"k"}`, "success_redirect_url"},
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
	if err := g.ValidateConnection(connection(`{}`, `[`)); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("malformed secrets: got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestConstants(t *testing.T) {
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:26
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:50
	if New().baseURL != APIURL || APIURL != "https://api.flutterwave.com/v3" {
		t.Errorf("API URL: got = %q, want = %q", New().baseURL, "https://api.flutterwave.com/v3")
	}
	if got := New(WithBaseURL("http://x")).baseURL; got != "http://x" {
		t.Errorf("override: got = %q, want = %q", got, "http://x")
	}
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:44
	if New().Provider() != Provider || Provider != "flutterwave" {
		t.Errorf("Provider: got = %q, want = %q", New().Provider(), "flutterwave")
	}
	for key, want := range map[string]bool{"FLWSECK_TEST-secret": false, "FLWSECK-live": true, "": true} {
		if got := (secrets{SecretKey: key}).livemode(); got != want {
			t.Errorf("livemode(%q): got = %v, want = %v", key, got, want)
		}
	}
}

func TestSuccessRedirectURL(t *testing.T) {
	empty, set := "", "https://example.com/done"
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:22
	if DefaultSuccessRedirectURL != "https://www.flutterwave.com/ng" {
		t.Errorf("default: got = %q, want = %q", DefaultSuccessRedirectURL, "https://www.flutterwave.com/ng")
	}
	for _, tt := range []struct {
		url  *string
		want string
	}{{nil, DefaultSuccessRedirectURL}, {&empty, DefaultSuccessRedirectURL}, {&set, set}} {
		if got := (settings{SuccessRedirectURL: tt.url}).successRedirectURL(); got != tt.want {
			t.Errorf("successRedirectURL: got = %q, want = %q", got, tt.want)
		}
	}
}

func TestRegisterWebhook(t *testing.T) {
	ctx := context.Background()
	g := New(WithWebhookSecrets(func() string { return testGenerated }))
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:57
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:99
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:104
	up, err := g.RegisterWebhook(ctx, connection(`{}`, `{"secret_key":"k","other":1}`), "https://tanvo.example/webhooks")
	if err != nil {
		t.Fatalf("RegisterWebhook: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(up.Secrets, &got); err != nil {
		t.Fatalf("secrets: %v", err)
	}
	if got["webhook_secret"] != testGenerated || got["secret_key"] != "k" || got["other"] != 1.0 || up.Settings != nil {
		t.Errorf("secrets: got = %v, want = the generated secret and the others kept", got)
	}
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:69
	up, err = g.RegisterWebhook(ctx, defaultConn(), "https://tanvo.example/webhooks")
	if err != nil || up.Secrets != nil || up.Settings != nil {
		t.Errorf("existing secret: got = %+v, %v, want = no update", up, err)
	}
	if _, err := g.RegisterWebhook(ctx, connection(`{}`, `[`), ""); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("malformed secrets: got = %v, want = %v", err, core.ErrInvalidValue)
	}
	// source: spec/models/payment_providers/flutterwave_provider_spec.rb:63
	a, b := randomSecret(), randomSecret()
	if len(a) != 64 || a == b {
		t.Errorf("randomSecret: got = %q and %q, want = two different 64-character secrets", a, b)
	}
	if err := g.RefreshWebhook(ctx, defaultConn(), "https://tanvo.example/webhooks"); err != nil {
		t.Errorf("RefreshWebhook: got = %v, want = nil", err)
	}
}

func TestCustomers(t *testing.T) {
	g := New()
	ctx := context.Background()
	conn := defaultConn()
	for name, f := range map[string]func() error{
		// source: spec/services/payment_provider_customers/flutterwave_service_spec.rb:12
		"CreateCustomer": func() error {
			_, err := g.CreateCustomer(ctx, conn, payment.CustomerParams{CustomerID: testCustomerID})
			return err
		},
		// source: spec/services/payment_provider_customers/flutterwave_service_spec.rb:21
		"UpdateCustomer": func() error { return g.UpdateCustomer(ctx, conn, "c", payment.CustomerParams{}) },
		// source: spec/services/payment_provider_customers/flutterwave_service_spec.rb:29
		// source: spec/services/payment_provider_customers/flutterwave_service_spec.rb:38
		"SetupURL":                func() error { _, err := g.SetupURL(ctx, conn, payment.SetupParams{}); return err },
		"ListPaymentMethods":      func() error { _, err := g.ListPaymentMethods(ctx, conn, "c"); return err },
		"DefaultPaymentMethod":    func() error { _, err := g.DefaultPaymentMethod(ctx, conn, "c"); return err },
		"PaymentMethod":           func() error { _, err := g.PaymentMethod(ctx, conn, "m"); return err },
		"SetDefaultPaymentMethod": func() error { return g.SetDefaultPaymentMethod(ctx, conn, "c", "m") },
		"DetachPaymentMethod":     func() error { return g.DetachPaymentMethod(ctx, conn, "m") },
	} {
		if err := f(); !errors.Is(err, payment.ErrUnsupported) {
			t.Errorf("%s: got = %v, want = %v", name, err, payment.ErrUnsupported)
		}
	}
	if got := g.DefaultMethodTypes(); got != nil {
		t.Errorf("DefaultMethodTypes: got = %v, want = nil", got)
	}
	if err := g.ValidateMethodTypes([]string{"card"}); err != nil {
		t.Errorf("ValidateMethodTypes: got = %v, want = nil", err)
	}
}
