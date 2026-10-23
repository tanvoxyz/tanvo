package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var (
	portalKey1 = []byte("0123456789abcdef0123456789abcdef")
	portalKey2 = []byte("fedcba9876543210fedcba9876543210")
)

func newPortal(t *testing.T, c *testClock, keys map[string][]byte, current string) *PortalTokens {
	t.Helper()
	p, err := NewPortalTokens(PortalConfig{Keys: keys, CurrentKeyID: current, FrontURL: "https://app.tanvo.test/"}, c)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPortalTokens(t *testing.T) {
	ids := core.UUIDv7Generator{}
	org, cus := core.NewID[core.OrganizationID](ids), core.NewID[core.CustomerID](ids)
	tests := []struct {
		name   string
		mutate func(tok string, c *testClock) (string, *PortalTokens)
		ok     bool
	}{
		{name: "valid", ok: true},
		{name: "just before expiry", mutate: func(tok string, c *testClock) (string, *PortalTokens) {
			c.Advance(DefaultPortalTokenTTL - time.Second)
			return tok, nil
		}, ok: true},
		{name: "expired after 12 hours", mutate: func(tok string, c *testClock) (string, *PortalTokens) {
			c.Advance(DefaultPortalTokenTTL)
			return tok, nil
		}},
		{name: "tampered payload", mutate: func(tok string, _ *testClock) (string, *PortalTokens) {
			p, s, _ := strings.Cut(tok, ".")
			raw, _ := base64.RawURLEncoding.DecodeString(p)
			raw = []byte(strings.Replace(string(raw), cus.String(), core.NewID[core.CustomerID](ids).String(), 1))
			return base64.RawURLEncoding.EncodeToString(raw) + "." + s, nil
		}},
		{name: "tampered signature", mutate: func(tok string, _ *testClock) (string, *PortalTokens) { return tok[:len(tok)-2] + "AA", nil }},
		{name: "garbage", mutate: func(string, *testClock) (string, *PortalTokens) { return "not-a-token", nil }},
		{name: "rotated key still verifies old tokens", mutate: func(tok string, c *testClock) (string, *PortalTokens) {
			return tok, newPortal(t, c, map[string][]byte{"k1": portalKey1, "k2": portalKey2}, "k2")
		}, ok: true},
		{name: "retired key", mutate: func(tok string, c *testClock) (string, *PortalTokens) {
			return tok, newPortal(t, c, map[string][]byte{"k2": portalKey2}, "k2")
		}},
		{name: "same key id, other secret", mutate: func(tok string, c *testClock) (string, *PortalTokens) {
			return tok, newPortal(t, c, map[string][]byte{"k1": portalKey2}, "k1")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &testClock{t: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)}
			p := newPortal(t, c, map[string][]byte{"k1": portalKey1}, "k1")
			tok, claims, err := p.Issue(org, cus)
			if err != nil {
				t.Fatal(err)
			}
			if !claims.ExpiresAt.Equal(c.Now().Add(12 * time.Hour)) {
				t.Errorf("expires_at = %s, want now + 12h", claims.ExpiresAt)
			}
			verifier := p
			if tt.mutate != nil {
				var v *PortalTokens
				if tok, v = tt.mutate(tok, c); v != nil {
					verifier = v
				}
			}
			got, err := verifier.Verify(tok)
			if (err == nil) != tt.ok {
				t.Fatalf("got = %v, want ok = %v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("got = %v, want = ErrUnauthenticated", err)
			}
			if err == nil && (got.OrganizationID != org || got.CustomerID != cus) {
				t.Errorf("claims = %+v", got)
			}
		})
	}
	t.Run("configuration", func(t *testing.T) {
		c := &testClock{}
		for name, cfg := range map[string]PortalConfig{
			"short key":       {Keys: map[string][]byte{"k": []byte("short")}, CurrentKeyID: "k"},
			"missing current": {Keys: map[string][]byte{"k": portalKey1}, CurrentKeyID: "x"},
			"dotted key id":   {Keys: map[string][]byte{"k.1": portalKey1}, CurrentKeyID: "k.1"},
		} {
			if _, err := NewPortalTokens(cfg, c); err == nil {
				t.Errorf("%s: got = nil, want an error", name)
			}
		}
	})
}

func TestPortalGenerateURL(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	cus := customer.New(o.ID, core.NewID[core.CustomerID](e.deps.IDs), "cus_1", core.BillingEntityID{}, e.clock.Now())
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Customers.Create(ctx, cus) })
	p := newPortal(t, e.clock, map[string][]byte{"k1": portalKey1}, "k1")
	// source: spec/services/customer_portal/generate_url_service_spec.rb:11
	u, err := p.GenerateURL(e.ctx, e.store, o.ID, cus.ID)
	if err != nil {
		t.Fatal(err)
	}
	tok, ok := strings.CutPrefix(u, "https://app.tanvo.test/customer-portal/")
	if !ok {
		t.Fatalf("url = %s", u)
	}
	if c, err := p.Verify(tok); err != nil || c.CustomerID != cus.ID {
		t.Errorf("Verify = %+v, %v", c, err)
	}
	// source: spec/services/customer_portal/generate_url_service_spec.rb:21
	if _, err := p.GenerateURL(e.ctx, e.store, e.org().ID, cus.ID); code(err) != "customer_not_found" {
		t.Errorf("other organization = %s, want = customer_not_found", code(err))
	}
}

func TestPortalAuthenticator(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	cus := customer.New(o.ID, core.NewID[core.CustomerID](e.deps.IDs), "cus_1", core.BillingEntityID{}, e.clock.Now())
	gone := customer.New(o.ID, core.NewID[core.CustomerID](e.deps.IDs), "cus_2", core.BillingEntityID{}, e.clock.Now())
	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := r.Customers.Create(ctx, cus); err != nil {
			return err
		}
		return r.Customers.Create(ctx, gone)
	})
	p := newPortal(t, e.clock, map[string][]byte{"k1": portalKey1}, "k1")
	a := PortalAuthenticator{Tokens: p, Tx: e.store}
	live, _, err := p.Issue(o.ID, cus.ID)
	if err != nil {
		t.Fatal(err)
	}
	deleted, _, err := p.Issue(o.ID, gone.ID)
	if err != nil {
		t.Fatal(err)
	}
	unknown, _, err := p.Issue(o.ID, core.NewID[core.CustomerID](e.deps.IDs))
	if err != nil {
		t.Fatal(err)
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		at := e.clock.Now()
		gone.DeletedAt = &at
		return r.Customers.Update(ctx, gone)
	})
	if _, err := p.GenerateURL(e.ctx, e.store, o.ID, gone.ID); code(err) != "customer_not_found" {
		t.Errorf("GenerateURL(deleted) got = %s, want = customer_not_found", code(err))
	}
	tests := []struct {
		name  string
		token string
		ok    bool
	}{
		{name: "live customer", token: live, ok: true},
		// lago CustomerPortalUser finds the customer through its kept scope.
		{name: "deleted customer", token: deleted},
		{name: "unknown customer", token: unknown},
		{name: "garbage", token: "x.y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := a.Verify(e.ctx, tt.token)
			if tt.ok {
				if err != nil || c.CustomerID != cus.ID {
					t.Errorf("Verify() got = %+v, %v, want = %s", c, err, cus.ID)
				}
				return
			}
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("Verify() error got = %v, want = %v", err, ErrUnauthenticated)
			}
		})
	}
}
