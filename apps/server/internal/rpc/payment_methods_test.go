package rpc_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// restPaymentMethod is the JSON of a payment method in a REST response.
type restPaymentMethod struct {
	ID               string `json:"id"`
	CustomerID       string `json:"customer_id"`
	ProviderMethodID string `json:"provider_method_id"`
	IsDefault        bool   `json:"is_default"`
}

// pmStored creates a payment method of the customer.
func pmStored(t *testing.T, s *rpctest.Server, c *customer.Customer, isDefault bool) *payment.Method {
	t.Helper()
	id := core.NewID[core.PaymentMethodID](core.UUIDv7Generator{})
	now := s.Clock.Now()
	m := &payment.Method{
		ID: id, OrganizationID: s.Org.ID, CustomerID: c.ID, ProviderMethodID: "pm_" + id.String(),
		IsDefault: isDefault, CreatedAt: now, UpdatedAt: now,
	}
	s.Fixtures.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Create(ctx, m) })
	s.Clock.Advance(time.Second)
	return m
}

// pmGet reloads a payment method.
func pmGet(t *testing.T, s *rpctest.Server, id core.PaymentMethodID) *payment.Method {
	t.Helper()
	var m *payment.Method
	s.Fixtures.Do(func(ctx context.Context, r port.Repos) (err error) {
		m, err = r.PaymentMethods.Get(ctx, s.Org.ID, id)
		return err
	})
	return m
}

func pmDecode(t *testing.T, body []byte) restPaymentMethod {
	t.Helper()
	return decode[struct {
		PaymentMethod restPaymentMethod `json:"payment_method"`
	}](t, body).PaymentMethod
}

func TestListPaymentMethodsRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	cust := s.Fixtures.Customer()
	m1, m2 := pmStored(t, s, cust, true), pmStored(t, s, cust, false)
	pmStored(t, s, s.Fixtures.Customer(), false)
	tests := []struct {
		name   string
		source string
		path   string
		status int
		want   []string
	}{
		{"unknown customer", "spec/requests/api/v1/customers/payment_methods_controller_spec.rb:21", "/v1/customers/external_id%3Aunknown/payment_methods", http.StatusNotFound, nil},
		{
			"payment methods", "spec/requests/api/v1/customers/payment_methods_controller_spec.rb:46",
			"/v1/customers/external_id%3A" + cust.ExternalID + "/payment_methods", http.StatusOK,
			[]string{m2.ID.String(), m1.ID.String()},
		},
		{"by customer ID", "", "/v1/customers/" + cust.ID.String() + "/payment_methods?page_size=1", http.StatusOK, []string{m2.ID.String()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			status, body := s.REST(t, s.APIKey, http.MethodGet, tt.path, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			var got []string
			for _, m := range decode[struct {
				PaymentMethods []restPaymentMethod `json:"payment_methods"`
			}](t, body).PaymentMethods {
				got = append(got, m.ID)
			}
			if len(got) != len(tt.want) || got[0] != tt.want[0] || len(got) > 1 && got[1] != tt.want[1] {
				t.Errorf("got methods = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestSetDefaultPaymentMethodRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	cust := s.Fixtures.Customer()
	def, other := pmStored(t, s, cust, true), pmStored(t, s, cust, false)
	path := func(c, m string) string { return "/v1/customers/" + c + "/payment_methods/" + m + ":set_default" }
	ext := "external_id%3A" + cust.ExternalID
	tests := []struct {
		name   string
		source string
		path   string
		status int
		want   string
	}{
		{"unknown customer", "spec/requests/api/v1/customers/payment_methods_controller_spec.rb:67", path("external_id%3Aunknown", def.ID.String()), http.StatusNotFound, ""},
		{"unknown method", "spec/requests/api/v1/customers/payment_methods_controller_spec.rb:78", path(ext, core.NewID[core.PaymentMethodID](core.UUIDv7Generator{}).String()), http.StatusNotFound, ""},
		{"already default", "spec/requests/api/v1/customers/payment_methods_controller_spec.rb:97", path(ext, def.ID.String()), http.StatusOK, def.ID.String()},
		{"not default", "spec/requests/api/v1/customers/payment_methods_controller_spec.rb:120", path(ext, other.ID.String()), http.StatusOK, other.ID.String()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodPost, tt.path, `{}`)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			if got := pmDecode(t, body); got.ID != tt.want || !got.IsDefault || got.CustomerID != cust.ID.String() {
				t.Errorf("got %+v, want default method %s", got, tt.want)
			}
		})
	}
	if pmGet(t, s, def.ID).IsDefault {
		t.Error("got previous default still default, want = false")
	}
}

func TestDeletePaymentMethodRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	cust := s.Fixtures.Customer()
	m := pmStored(t, s, cust, true)
	path := func(c, id string) string { return "/v1/customers/" + c + "/payment_methods/" + id }
	ext := "external_id%3A" + cust.ExternalID
	tests := []struct {
		name   string
		source string
		path   string
		status int
	}{
		{"unknown customer", "spec/requests/api/v1/customers/payment_methods_controller_spec.rb:161", path("external_id%3Aunknown", m.ID.String()), http.StatusNotFound},
		{"unknown method", "spec/requests/api/v1/customers/payment_methods_controller_spec.rb:174", path(ext, core.NewID[core.PaymentMethodID](core.UUIDv7Generator{}).String()), http.StatusNotFound},
		{"deletes", "spec/requests/api/v1/customers/payment_methods_controller_spec.rb:183", path(ext, m.ID.String()), http.StatusOK},
		{"deleted", "", path(ext, m.ID.String()), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			status, body := s.REST(t, s.APIKey, http.MethodDelete, tt.path, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
		})
	}
	got := pmGet(t, s, m.ID)
	if got.IsDefault || got.DeletedAt == nil {
		t.Errorf("got default = %t, deleted at = %v, want = false, set", got.IsDefault, got.DeletedAt)
	}
}
