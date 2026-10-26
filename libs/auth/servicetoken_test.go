package auth

import (
	"bytes"
	"cmp"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
)

// serviceKey returns the key id whose seed is 32 bytes of b.
func serviceKey(t *testing.T, id string, b byte) ServiceTokenKey {
	t.Helper()
	k, err := ParseServiceTokenKey(id + ":" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newServiceTokens(t *testing.T, c core.Clock, keys ...ServiceTokenKey) *ServiceTokens {
	t.Helper()
	s, err := NewServiceTokens(ServiceTokenConfig{Keys: keys}, c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseServiceTokenKey(t *testing.T) {
	seed := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	tests := []struct {
		name  string
		entry string
		id    string
		ok    bool
	}{
		{name: "valid", entry: "k1:" + seed, id: "k1", ok: true},
		{name: "spaces trimmed", entry: "  key_2-b:" + seed + " ", id: "key_2-b", ok: true},
		{name: "no separator", entry: seed},
		{name: "empty id", entry: ":" + seed},
		{name: "dot in id", entry: "k.1:" + seed},
		{name: "padded seed", entry: "k1:" + base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))},
		{name: "standard alphabet", entry: "k1:" + strings.Repeat("/", 43)},
		{name: "short seed", entry: "k1:" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 31))},
		{name: "long id", entry: strings.Repeat("k", 65) + ":" + seed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := ParseServiceTokenKey(tt.entry)
			if (err == nil) != tt.ok {
				t.Fatalf("error got = %v, want ok = %v", err, tt.ok)
			}
			if !tt.ok {
				return
			}
			if k.ID != tt.id {
				t.Errorf("id got = %q, want = %q", k.ID, tt.id)
			}
			if got, want := k.Encode(), tt.id+":"+seed; got != want {
				t.Errorf("Encode() got = %q, want = %q", got, want)
			}
		})
	}
}

func TestParseServiceTokenKeys(t *testing.T) {
	seed := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	tests := []struct {
		name    string
		entries []string
		ids     []string
		ok      bool
	}{
		{name: "none", ok: true},
		{name: "blank skipped", entries: []string{"", " ", "a:" + seed}, ids: []string{"a"}, ok: true},
		{name: "order kept", entries: []string{"b:" + seed, "a:" + seed}, ids: []string{"b", "a"}, ok: true},
		{name: "duplicate", entries: []string{"a:" + seed, "a:" + seed}},
		{name: "invalid entry", entries: []string{"a:" + seed, "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, err := ParseServiceTokenKeys(tt.entries)
			if (err == nil) != tt.ok {
				t.Fatalf("error got = %v, want ok = %v", err, tt.ok)
			}
			var ids []string
			for _, k := range keys {
				ids = append(ids, k.ID)
			}
			if !slices.Equal(ids, tt.ids) {
				t.Errorf("ids got = %v, want = %v", ids, tt.ids)
			}
		})
	}
}

func TestGenerateServiceTokenKey(t *testing.T) {
	rnd := bytes.NewReader(bytes.Repeat([]byte{9}, 64))
	k, err := GenerateServiceTokenKey(rnd, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := k.ID, "09090909"; got != want {
		t.Errorf("id got = %q, want = %q", got, want)
	}
	back, err := ParseServiceTokenKey(k.Encode())
	if err != nil {
		t.Fatalf("ParseServiceTokenKey(Encode()) error got = %v, want = nil", err)
	}
	if !back.PrivateKey.Equal(k.PrivateKey) {
		t.Error("round trip: got = another key, want = the same key")
	}
	if _, err := GenerateServiceTokenKey(rnd, "bad id"); err == nil {
		t.Error("invalid id: got = nil error, want = an error")
	}
	named, err := GenerateServiceTokenKey(bytes.NewReader(bytes.Repeat([]byte{1}, 32)), "2026-10")
	if err != nil || named.ID != "2026-10" {
		t.Errorf("named key got = %q, %v, want = 2026-10, nil", named.ID, err)
	}
	pub := named.Public().String()
	want := "2026-10:" + base64.RawURLEncoding.EncodeToString(named.PrivateKey.Public().(ed25519.PublicKey))
	if pub != want {
		t.Errorf("public got = %q, want = %q", pub, want)
	}
}

func TestNewServiceTokens(t *testing.T) {
	k1 := serviceKey(t, "k1", 1)
	tests := []struct {
		name string
		keys []ServiceTokenKey
		ok   bool
	}{
		{name: "one key", keys: []ServiceTokenKey{k1}, ok: true},
		{name: "no key"},
		{name: "duplicate", keys: []ServiceTokenKey{k1, k1}},
		{name: "invalid id", keys: []ServiceTokenKey{{ID: "a:b", PrivateKey: k1.PrivateKey}}},
		{name: "short key", keys: []ServiceTokenKey{{ID: "k2", PrivateKey: k1.PrivateKey[:10]}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewServiceTokens(ServiceTokenConfig{Keys: tt.keys}, core.SystemClock{})
			if (err == nil) != tt.ok {
				t.Errorf("error got = %v, want ok = %v", err, tt.ok)
			}
		})
	}
}

func TestServiceTokensIssue(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 30, 15, 500, time.UTC)
	c := &testClock{t: now}
	k1, k2 := serviceKey(t, "k1", 1), serviceKey(t, "k2", 2)
	s := newServiceTokens(t, c, k1, k2)
	org := core.MustParseID[core.OrganizationID]("0190a000-0000-7000-8000-000000000001")
	tok, claims, err := s.Issue(ServiceTokenAudienceDataAPI, org, "user:u1", []permission.Permission{"data_api:read", "analytics:read", "data_api:read"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claims.ExpiresAt, now.Truncate(time.Second).Add(15*time.Minute); !got.Equal(want) {
		t.Errorf("expires_at got = %s, want = %s", got, want)
	}
	// The format of tanvo.access.v1.ServiceTokenClaims, decoded with the
	// generated message as an enterprise service does.
	p, sig, ok := strings.Cut(tok, ".")
	if !ok || strings.Contains(tok, "=") {
		t.Fatalf("token got = %q, want = two unpadded base64url parts", tok)
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		t.Fatal(err)
	}
	sigRaw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(k1.Public().PublicKey, append([]byte("tanvo.service_token.v1."), raw...), sigRaw) {
		t.Error("signature: got = invalid with the current key, want = valid")
	}
	var msg accessv1.ServiceTokenClaims
	if err := proto.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	got := []any{msg.GetVersion(), msg.GetKeyId(), msg.GetAudience(), msg.GetOrganizationId(), msg.GetSubject(), strings.Join(msg.GetPermissions(), ","), msg.GetIssuedAt().AsTime().Format(time.RFC3339), msg.GetExpiresAt().AsTime().Format(time.RFC3339)}
	want := []any{int32(1), "k1", "data_api", org.String(), "user:u1", "analytics:read,data_api:read", "2026-10-27T09:30:15Z", "2026-10-27T09:45:15Z"}
	if !slices.Equal(got, want) {
		t.Errorf("claims got = %v, want = %v", got, want)
	}
	if pubs := s.PublicKeys(); len(pubs) != 2 || pubs[0].String() != k1.Public().String() || pubs[1].String() != k2.Public().String() {
		t.Errorf("PublicKeys() got = %v, want = k1 then k2", pubs)
	}
	if _, _, err := s.Issue(ServiceTokenAudienceDataAPI, org, "", nil); err == nil {
		t.Error("no subject: got = nil error, want = an error")
	}
}

func TestServiceTokensVerify(t *testing.T) {
	org := core.MustParseID[core.OrganizationID]("0190a000-0000-7000-8000-000000000001")
	k1, k2 := serviceKey(t, "k1", 1), serviceKey(t, "k2", 2)
	// forge signs msg with key under its ID.
	forge := func(key ServiceTokenKey, edit func(*accessv1.ServiceTokenClaims)) string {
		msg := accessv1.ServiceTokenClaims_builder{
			Version: new(int32(1)), KeyId: new(key.ID), Audience: new("data_api"), OrganizationId: new(org.String()),
			Subject: new("api_key:k"), ExpiresAt: timestamppb.New(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)),
		}.Build()
		edit(msg)
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		enc := base64.RawURLEncoding
		return enc.EncodeToString(raw) + "." + enc.EncodeToString(ed25519.Sign(key.PrivateKey, append([]byte("tanvo.service_token.v1."), raw...)))
	}
	tests := []struct {
		name     string
		mutate   func(tok string, c *testClock) (string, *ServiceTokens)
		audience string
		ok       bool
	}{
		{name: "valid", ok: true},
		{name: "just before expiry", mutate: func(tok string, c *testClock) (string, *ServiceTokens) {
			c.Advance(DefaultServiceTokenTTL - time.Second)
			return tok, nil
		}, ok: true},
		{name: "expired after 15 minutes", mutate: func(tok string, c *testClock) (string, *ServiceTokens) {
			c.Advance(DefaultServiceTokenTTL)
			return tok, nil
		}},
		{name: "other audience", audience: "mcp"},
		{name: "tampered claims", mutate: func(tok string, _ *testClock) (string, *ServiceTokens) {
			p, s, _ := strings.Cut(tok, ".")
			raw, _ := base64.RawURLEncoding.DecodeString(p)
			raw = bytes.Replace(raw, []byte("user:u1"), []byte("user:u2"), 1)
			return base64.RawURLEncoding.EncodeToString(raw) + "." + s, nil
		}},
		{name: "tampered signature", mutate: func(tok string, _ *testClock) (string, *ServiceTokens) {
			p, s, _ := strings.Cut(tok, ".")
			sig, _ := base64.RawURLEncoding.DecodeString(s)
			sig[10] ^= 0xff
			return p + "." + base64.RawURLEncoding.EncodeToString(sig), nil
		}},
		{name: "garbage", mutate: func(string, *testClock) (string, *ServiceTokens) { return "not-a-token", nil }},
		{name: "rotated key still verifies old tokens", mutate: func(tok string, c *testClock) (string, *ServiceTokens) {
			return tok, newServiceTokens(t, c, k2, k1)
		}, ok: true},
		{name: "retired key", mutate: func(tok string, c *testClock) (string, *ServiceTokens) {
			return tok, newServiceTokens(t, c, k2)
		}},
		{name: "same key id, other key", mutate: func(tok string, c *testClock) (string, *ServiceTokens) {
			return tok, newServiceTokens(t, c, serviceKey(t, "k1", 3))
		}},
		{name: "signed under another key id", mutate: func(string, *testClock) (string, *ServiceTokens) {
			return forge(ServiceTokenKey{ID: "k1", PrivateKey: k2.PrivateKey}, func(*accessv1.ServiceTokenClaims) {}), nil
		}},
		{name: "other version", mutate: func(string, *testClock) (string, *ServiceTokens) {
			return forge(k1, func(m *accessv1.ServiceTokenClaims) { m.SetVersion(2) }), nil
		}},
		{name: "forged with the current key", mutate: func(string, *testClock) (string, *ServiceTokens) {
			return forge(k1, func(m *accessv1.ServiceTokenClaims) {
				m.SetSubject("user:u1")
				m.SetPermissions([]string{"data_api:read"})
			}), nil
		}, ok: true},
		{name: "no expiry", mutate: func(string, *testClock) (string, *ServiceTokens) {
			return forge(k1, func(m *accessv1.ServiceTokenClaims) { m.ClearExpiresAt() }), nil
		}},
		{name: "no subject", mutate: func(string, *testClock) (string, *ServiceTokens) {
			return forge(k1, func(m *accessv1.ServiceTokenClaims) { m.ClearSubject() }), nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &testClock{t: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)}
			s := newServiceTokens(t, c, k1)
			tok, _, err := s.Issue(ServiceTokenAudienceDataAPI, org, "user:u1", []permission.Permission{"data_api:read"})
			if err != nil {
				t.Fatal(err)
			}
			verifier := s
			if tt.mutate != nil {
				var v *ServiceTokens
				if tok, v = tt.mutate(tok, c); v != nil {
					verifier = v
				}
			}
			audience := cmp.Or(tt.audience, ServiceTokenAudienceDataAPI)
			claims, err := verifier.Verify(tok, audience)
			if tt.ok {
				if err != nil {
					t.Fatalf("Verify() error got = %v, want = nil", err)
				}
				if claims.OrganizationID != org || claims.Subject != "user:u1" || !slices.Equal(claims.Permissions, []permission.Permission{"data_api:read"}) || claims.KeyID != "k1" {
					t.Errorf("claims got = %+v, want = org, user:u1, data_api:read, k1", claims)
				}
				return
			}
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("Verify() error got = %v, want = ErrUnauthenticated", err)
			}
		})
	}
}

func TestPrincipalServiceSubject(t *testing.T) {
	org := core.MustParseID[core.OrganizationID]("0190a000-0000-7000-8000-000000000001")
	user := &access.User{ID: core.MustParseID[core.UserID]("0190a000-0000-7000-8000-000000000002")}
	key := &access.APIKey{ID: core.MustParseID[core.APIKeyID]("0190a000-0000-7000-8000-000000000003"), OrganizationID: org}
	tests := []struct {
		name string
		p    Principal
		want string
		err  error
	}{
		{name: "member", p: Principal{Kind: PrincipalUser, OrganizationID: org, User: user}, want: "user:0190a000-0000-7000-8000-000000000002"},
		{name: "api key", p: APIKeyPrincipal(key), want: "api_key:0190a000-0000-7000-8000-000000000003"},
		{name: "user without organization", p: Principal{Kind: PrincipalUser, User: user}, err: ErrPermissionDenied},
		{name: "portal", p: Principal{Kind: PrincipalPortal, OrganizationID: org}, err: ErrPermissionDenied},
		{name: "none", err: ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.p.ServiceSubject()
			if got != tt.want || !errors.Is(err, tt.err) || (tt.err == nil && err != nil) {
				t.Errorf("ServiceSubject() got = %q, %v, want = %q, %v", got, err, tt.want, tt.err)
			}
		})
	}
}
