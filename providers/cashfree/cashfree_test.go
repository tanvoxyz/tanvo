package cashfree

import (
	"context"
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
		fields   []string
	}{
		// source: spec/services/payment_providers/cashfree_service_spec.rb:19
		{"valid", `{"success_redirect_url":"https://example.com/success"}`, `{"client_id":"id","client_secret":"secret"}`, nil},
		{"no success url", `{}`, `{"client_id":"id","client_secret":"secret"}`, nil},
		// source: spec/services/payment_providers/cashfree_service_spec.rb:120
		{"credentials required", `{}`, `{}`, []string{"client_id", "client_secret"}},
		{"success url too long", `{"success_redirect_url":"` + long + `"}`, `{"client_id":"id","client_secret":"secret"}`, []string{"success_redirect_url"}},
		{"success url invalid", `{"success_redirect_url":"not a url"}`, `{"client_id":"id","client_secret":"secret"}`, []string{"success_redirect_url"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := g.ValidateConnection(connection(tt.settings, tt.secrets))
			if len(tt.fields) == 0 {
				if err != nil {
					t.Fatalf("ValidateConnection: got = %v, want = nil", err)
				}
				return
			}
			var ve *core.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("ValidateConnection: got = %v, want = a validation error", err)
			}
			for _, f := range tt.fields {
				if !strings.Contains(err.Error(), f) {
					t.Errorf("ValidateConnection: got = %v, want = an error on %s", err, f)
				}
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

func TestEndpoint(t *testing.T) {
	g := New()
	if got := g.endpoint(settings{}); got != LiveURL || LiveURL != "https://api.cashfree.com/pg" {
		t.Errorf("live endpoint: got = %q, want = %q", got, "https://api.cashfree.com/pg")
	}
	if got := g.endpoint(settings{Sandbox: true}); got != SandboxURL || SandboxURL != "https://sandbox.cashfree.com/pg" {
		t.Errorf("sandbox endpoint: got = %q, want = %q", got, "https://sandbox.cashfree.com/pg")
	}
	if got := New(WithBaseURL("http://x")).endpoint(settings{Sandbox: true}); got != "http://x" {
		t.Errorf("override: got = %q, want = %q", got, "http://x")
	}
}

func TestSuccessRedirectURL(t *testing.T) {
	empty, set := "", "https://example.com/done"
	for _, tt := range []struct {
		url  *string
		want string
	}{{nil, DefaultSuccessRedirectURL}, {&empty, DefaultSuccessRedirectURL}, {&set, set}} {
		if got := (settings{SuccessRedirectURL: tt.url}).successRedirectURL(); got != tt.want {
			t.Errorf("successRedirectURL: got = %q, want = %q", got, tt.want)
		}
	}
}

func TestWebhookRegistration(t *testing.T) {
	g := New()
	up, err := g.RegisterWebhook(context.Background(), defaultConn(), "https://tanvo.example/webhooks")
	if err != nil || up.Settings != nil || up.Secrets != nil {
		t.Errorf("RegisterWebhook: got = %+v, %v, want = no update", up, err)
	}
	if err := g.RefreshWebhook(context.Background(), defaultConn(), "https://tanvo.example/webhooks"); err != nil {
		t.Errorf("RefreshWebhook: got = %v, want = nil", err)
	}
}

func TestCustomers(t *testing.T) {
	g := New()
	ctx := context.Background()
	conn := defaultConn()
	// source: spec/services/payment_providers/cashfree/customers/create_service_spec.rb:15
	if _, err := g.CreateCustomer(ctx, conn, payment.CustomerParams{CustomerID: testCustomerID}); !errors.Is(err, payment.ErrUnsupported) {
		t.Errorf("CreateCustomer: got = %v, want = %v", err, payment.ErrUnsupported)
	}
	if err := g.UpdateCustomer(ctx, conn, "c", payment.CustomerParams{}); !errors.Is(err, payment.ErrUnsupported) {
		t.Errorf("UpdateCustomer: got = %v, want = %v", err, payment.ErrUnsupported)
	}
	for name, f := range map[string]func() error{
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
