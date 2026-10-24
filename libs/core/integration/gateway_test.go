package integration

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestProviderErrorClasses(t *testing.T) {
	tests := []struct {
		kind                                  ErrorKind
		retryable, limit, rejected, authError bool
	}{
		{KindRequestLimit, true, true, false, false},
		{KindUnavailable, true, false, false, false},
		{KindContention, true, false, false, false},
		{KindAuthentication, false, false, false, true},
		{KindNotFound, false, false, true, false},
		{KindLocked, false, false, true, false},
		{KindInvalidRequest, false, false, true, false},
		{KindPermanent, false, false, true, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			err := fmt.Errorf("sync: %w", &ProviderError{Provider: ProviderAnrok, Kind: tt.kind, Code: "c"})
			got := [4]bool{errors.Is(err, ErrRetryable), errors.Is(err, ErrRequestLimit), errors.Is(err, ErrRejected), errors.Is(err, ErrAuthentication)}
			want := [4]bool{tt.retryable, tt.limit, tt.rejected, tt.authError}
			if got != want {
				t.Errorf("classes = %v, want = %v", got, want)
			}
			if c := ErrorCode(err, "x"); c != "c" {
				t.Errorf("ErrorCode() = %q, want = %q", c, "c")
			}
		})
	}
	if c := ErrorCode(errors.New("plain"), "internal_error"); c != "internal_error" {
		t.Errorf("ErrorCode(plain) = %q, want = internal_error", c)
	}
}

func TestContention(t *testing.T) {
	// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:390,400 (out of memory and API limit raise)
	tests := []struct {
		msg  string
		want bool
	}{
		{"API limit exceeded", true},
		{"We are experiencing resource contention", true},
		{"function_runtime_out_of_memory", true},
		{"customer not found", false},
	}
	for _, tt := range tests {
		if got := Contention(tt.msg); got != tt.want {
			t.Errorf("Contention(%q) = %v, want = %v", tt.msg, got, tt.want)
		}
	}
}

func TestConnectionSettings(t *testing.T) {
	c := Connection{Settings: []byte(`{"account_id":"a1","n":3}`), Secrets: []byte(`{"api_key":"k"}`)}
	if got := c.Setting("account_id"); got != "a1" {
		t.Errorf("Setting(account_id) = %q, want = a1", got)
	}
	if got := c.Setting("missing"); got != "" {
		t.Errorf("Setting(missing) = %q, want = empty", got)
	}
	if got := c.Secret("api_key"); got != "k" {
		t.Errorf("Secret(api_key) = %q, want = k", got)
	}
}

func TestContactHelpers(t *testing.T) {
	billing := core.Address{Line1: "1 Main", City: "Paris", Country: "FR"}
	tests := []struct {
		name               string
		c                  Contact
		email, phone, city string
		same, empty        bool
	}{
		{"lists", Contact{Email: " a@x.io , b@x.io", Phone: "+1,+2", BillingAddress: billing}, "a@x.io", "+1", "Paris", true, false},
		{"shipping differs", Contact{BillingAddress: billing, ShippingAddress: core.Address{City: "Lyon"}}, "", "", "Lyon", false, false},
		{"no address", Contact{}, "", "", "", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.FirstEmail(); got != tt.email {
				t.Errorf("FirstEmail() = %q, want = %q", got, tt.email)
			}
			if got := tt.c.FirstPhone(); got != tt.phone {
				t.Errorf("FirstPhone() = %q, want = %q", got, tt.phone)
			}
			if got := tt.c.EffectiveShippingAddress().City; got != tt.city {
				t.Errorf("EffectiveShippingAddress().City = %q, want = %q", got, tt.city)
			}
			if got := tt.c.SameBillingAndShippingAddress(); got != tt.same {
				t.Errorf("SameBillingAndShippingAddress() = %v, want = %v", got, tt.same)
			}
			if got := tt.c.EmptyBillingAndShippingAddress(); got != tt.empty {
				t.Errorf("EmptyBillingAndShippingAddress() = %v, want = %v", got, tt.empty)
			}
		})
	}
}
