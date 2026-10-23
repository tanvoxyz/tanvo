package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"

	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// Defaults of the provider metadata caches and requests.
const (
	// DefaultDiscoveryTTL is how long a discovery document is reused.
	DefaultDiscoveryTTL = time.Hour
	// DefaultJWKSTTL is how long a key set is reused.
	DefaultJWKSTTL = time.Hour
	// DefaultJWKSMinRefresh is the least time between two key set fetches
	// caused by a token signed with an unknown key, so that forged key IDs
	// cannot make Tanvo hammer the provider.
	DefaultJWKSMinRefresh = time.Minute
	// DefaultProviderTimeout bounds one request to a provider when the
	// client has no timeout.
	DefaultProviderTimeout = 10 * time.Second
	// MaxProviderResponseBytes bounds a provider response body (discovery,
	// keys, token, userinfo).
	MaxProviderResponseBytes = 1 << 20
	// ClockSkew is the leeway of the ID token time claims.
	ClockSkew = 30 * time.Second
)

// Field error codes of an issuer checked with [OIDC.CheckIssuer].
const (
	// CodeHTTPSRequired: the issuer or a discovered endpoint is not https.
	CodeHTTPSRequired = "https_required"
	// CodeIssuerUnreachable: the discovery document could not be fetched
	// or decoded (refused address, error status, timeout, oversized).
	CodeIssuerUnreachable = "issuer_unreachable"
	// CodeIssuerMismatch: the discovered issuer differs from the
	// configured one; they must be equal, character for character.
	CodeIssuerMismatch = "issuer_mismatch"
	// CodeDiscoveryIncomplete: the document lacks an endpoint the flow
	// needs, or supports neither the code flow nor an accepted signing
	// algorithm.
	CodeDiscoveryIncomplete = "discovery_incomplete"
	// CodeUnsupportedByProvider: the provider does not list the
	// configured token endpoint authentication method.
	CodeUnsupportedByProvider = "unsupported_by_provider"
)

// signingAlgs are the ID token signature algorithms accepted: asymmetric
// ones only. "none" and the HMAC algorithms are never accepted, so a
// token cannot be forged with the client secret or without a key.
var signingAlgs = []string{
	oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512,
	oidc.PS256, oidc.PS384, oidc.PS512, oidc.EdDSA,
}

func joseAlgs(algs []string) []jose.SignatureAlgorithm {
	out := make([]jose.SignatureAlgorithm, len(algs))
	for i, a := range algs {
		out[i] = jose.SignatureAlgorithm(a)
	}
	return out
}

// providerMetadata are the discovery fields beyond go-oidc's.
type providerMetadata struct {
	JWKSURL          string   `json:"jwks_uri"`
	ResponseTypes    []string `json:"response_types_supported"`
	TokenAuthMethods []string `json:"token_endpoint_auth_methods_supported"`
	SigningAlgs      []string `json:"id_token_signing_alg_values_supported"`
}

// discovered is a provider's cached metadata and keys.
type discovered struct {
	provider   *oidc.Provider
	meta       providerMetadata
	algs       []string
	keys       *jwksCache
	fetchedAt  time.Time
	tokenStyle integration.TokenAuthMethod
}

// discoveryError is a discovery failure with the field error code
// [OIDC.CheckIssuer] reports.
type discoveryError struct {
	code string
	err  error
}

func (e *discoveryError) Error() string { return e.code + ": " + e.err.Error() }

func (e *discoveryError) Unwrap() error { return e.err }

// errResponseTooLarge is returned when a provider response exceeds
// [MaxProviderResponseBytes].
var errResponseTooLarge = errors.New("provider response too large")

// limitTransport bounds response bodies.
type limitTransport struct {
	base http.RoundTripper
	max  int64
}

func (t limitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedBody{rc: resp.Body, left: t.max}
	return resp, nil
}

type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, errResponseTooLarge
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return n, errResponseTooLarge
	}
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }

// providerClient returns c with bounded response bodies and a timeout.
func providerClient(c *http.Client) *http.Client {
	out := *c
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	out.Transport = limitTransport{base: base, max: MaxProviderResponseBytes}
	if out.Timeout == 0 {
		out.Timeout = DefaultProviderTimeout
	}
	return &out
}

// secureURL reports whether raw may be fetched: https, or http when
// insecure providers are allowed.
func (s *OIDC) secureURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "https" || (s.cfg.AllowInsecure && u.Scheme == "http")
}

// discover fetches and checks the discovery document of issuer: the
// issuer must be https (unless insecure providers are allowed) and equal
// the discovered one exactly, and the document must name the endpoints
// of the code flow over the same scheme rule.
func (s *OIDC) discover(ctx context.Context, issuer string) (*discovered, error) {
	if !integration.ValidIssuerURL(issuer) {
		return nil, &discoveryError{integration.CodeIssuerURLInvalid, fmt.Errorf("issuer %q", issuer)}
	}
	if !s.secureURL(issuer) {
		return nil, &discoveryError{CodeHTTPSRequired, fmt.Errorf("issuer %q", issuer)}
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, s.client), issuer)
	if mis := (*oidc.IssuerMismatchError)(nil); errors.As(err, &mis) {
		return nil, &discoveryError{CodeIssuerMismatch, err}
	}
	if err != nil {
		return nil, &discoveryError{CodeIssuerUnreachable, err}
	}
	var meta providerMetadata
	if err := p.Claims(&meta); err != nil {
		return nil, &discoveryError{CodeIssuerUnreachable, err}
	}
	ep := p.Endpoint()
	if ep.AuthURL == "" || ep.TokenURL == "" || meta.JWKSURL == "" ||
		(len(meta.ResponseTypes) > 0 && !slices.Contains(meta.ResponseTypes, "code")) {
		return nil, &discoveryError{CodeDiscoveryIncomplete, fmt.Errorf("issuer %q: missing endpoints or code flow", issuer)}
	}
	for _, u := range []string{ep.AuthURL, ep.TokenURL, meta.JWKSURL, p.UserInfoEndpoint()} {
		if u != "" && !s.secureURL(u) {
			return nil, &discoveryError{CodeHTTPSRequired, fmt.Errorf("endpoint %q", u)}
		}
	}
	// Without a list the specification's default, RS256, applies.
	algs := []string{oidc.RS256}
	if len(meta.SigningAlgs) > 0 {
		algs = slices.DeleteFunc(slices.Clone(meta.SigningAlgs), func(a string) bool { return !slices.Contains(signingAlgs, a) })
	}
	if len(algs) == 0 {
		return nil, &discoveryError{CodeDiscoveryIncomplete, fmt.Errorf("issuer %q: no accepted signing algorithm in %v", issuer, meta.SigningAlgs)}
	}
	d := &discovered{provider: p, meta: meta, algs: algs, fetchedAt: s.d.now(), tokenStyle: integration.TokenAuthClientSecretBasic}
	for _, m := range meta.TokenAuthMethods {
		if m := integration.TokenAuthMethod(m); m == integration.TokenAuthClientSecretBasic || m == integration.TokenAuthClientSecretPost {
			d.tokenStyle = m
			break
		}
	}
	return d, nil
}

// provider returns the cached metadata of issuer, refetching it after
// [OIDCConfig.DiscoveryTTL]; a failed refetch keeps the previous document
// so that a provider hiccup does not stop sign-ins. The key set is kept
// across refetches while its URL does not change.
func (s *OIDC) provider(ctx context.Context, issuer string) (*discovered, error) {
	s.mu.Lock()
	old, ok := s.providers[issuer]
	s.mu.Unlock()
	if ok && s.d.now().Sub(old.fetchedAt) < s.cfg.DiscoveryTTL {
		return old, nil
	}
	d, err := s.discover(ctx, issuer)
	if err != nil {
		if ok {
			s.d.logger().WarnContext(ctx, "oidc rediscovery", "issuer", issuer, "error", err)
			return old, nil
		}
		return nil, err
	}
	if ok && old.meta.JWKSURL == d.meta.JWKSURL {
		d.keys = old.keys
	} else {
		d.keys = &jwksCache{url: d.meta.JWKSURL, algs: d.algs, s: s}
	}
	s.mu.Lock()
	s.providers[issuer] = d
	s.mu.Unlock()
	return d, nil
}

// CheckIssuer fetches the discovery document of issuer once, as a
// connection is created or changed, and reports what would fail at
// sign-in as field errors: issuer_url (https_required,
// issuer_url_invalid, issuer_unreachable, issuer_mismatch,
// discovery_incomplete) and token_endpoint_auth_method
// (unsupported_by_provider when the provider lists methods without m).
func (s *OIDC) CheckIssuer(ctx context.Context, issuer string, m integration.TokenAuthMethod) error {
	d, err := s.discover(ctx, issuer)
	if de := (*discoveryError)(nil); errors.As(err, &de) {
		s.d.logger().InfoContext(ctx, "oidc issuer check", "issuer", issuer, "error", err)
		return fieldError("issuer_url", de.code)
	}
	if err != nil {
		return err
	}
	if m != "" && len(d.meta.TokenAuthMethods) > 0 && !slices.Contains(d.meta.TokenAuthMethods, string(m)) {
		return fieldError("token_endpoint_auth_method", CodeUnsupportedByProvider)
	}
	return nil
}

// jwksCache is a provider's key set, refetched after [OIDCConfig.JWKSTTL]
// and when a token names an unknown key (rotation), at most once per
// [OIDCConfig.JWKSMinRefresh]. It implements [oidc.KeySet].
type jwksCache struct {
	url  string
	algs []string
	s    *OIDC

	fetch     sync.Mutex // serializes fetches
	mu        sync.Mutex
	keys      []jose.JSONWebKey
	fetchedAt time.Time
	forcedAt  time.Time // last refetch for an unknown key
}

var _ oidc.KeySet = (*jwksCache)(nil)

// VerifySignature implements [oidc.KeySet]: it returns the payload of a
// compact JWS signed by one of the provider's keys with an accepted
// algorithm.
func (k *jwksCache) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	jws, err := jose.ParseSigned(raw, joseAlgs(k.algs))
	if err != nil {
		return nil, fmt.Errorf("parse jws: %w", err)
	}
	if len(jws.Signatures) != 1 {
		return nil, errors.New("jws: exactly one signature is required")
	}
	h := jws.Signatures[0].Header
	keys, fresh := k.cached()
	if !fresh {
		keys = k.refresh(ctx, false)
	}
	if p, ok := verifyWith(jws, keys, h); ok {
		return p, nil
	}
	if p, ok := verifyWith(jws, k.refresh(ctx, true), h); ok {
		return p, nil
	}
	return nil, fmt.Errorf("jws: no key %q of %s verifies the signature", h.KeyID, k.url)
}

func (k *jwksCache) cached() ([]jose.JSONWebKey, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys, len(k.keys) > 0 && k.s.d.now().Sub(k.fetchedAt) < k.s.cfg.JWKSTTL
}

// refresh fetches the key set: for an unknown key unless such a refetch
// happened within the minimum interval, otherwise when the set is missing
// or older than the TTL (a concurrent caller may just have fetched it).
// It returns the keys known afterwards, the previous ones when the fetch
// fails.
func (k *jwksCache) refresh(ctx context.Context, unknownKey bool) []jose.JSONWebKey {
	k.fetch.Lock()
	defer k.fetch.Unlock()
	k.mu.Lock()
	now := k.s.d.now()
	keys, forcedAt, fetchedAt := k.keys, k.forcedAt, k.fetchedAt
	k.mu.Unlock()
	if unknownKey && !forcedAt.IsZero() && now.Sub(forcedAt) < k.s.cfg.JWKSMinRefresh {
		return keys
	}
	if !unknownKey && len(keys) > 0 && now.Sub(fetchedAt) < k.s.cfg.JWKSTTL {
		return keys
	}
	fetched, err := k.get(ctx)
	k.mu.Lock()
	defer k.mu.Unlock()
	if unknownKey {
		k.forcedAt = now
	}
	if err != nil {
		k.s.d.logger().WarnContext(ctx, "oidc jwks", "url", k.url, "error", err)
		return k.keys
	}
	k.keys, k.fetchedAt = fetched, now
	return fetched
}

func (k *jwksCache) get(ctx context.Context) ([]jose.JSONWebKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return nil, fmt.Errorf("jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := k.s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read jwks: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch jwks: %s", resp.Status)
	}
	// Keys are decoded one by one: a key of a type go-jose does not know
	// is skipped rather than failing the set.
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}
	keys := make([]jose.JSONWebKey, 0, len(set.Keys))
	for _, raw := range set.Keys {
		var key jose.JSONWebKey
		if err := key.UnmarshalJSON(raw); err == nil && key.Valid() && key.IsPublic() {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// verifyWith verifies jws with the keys matching its header: the key ID
// when it names one, signature use and the algorithm when the key
// restricts them.
func verifyWith(jws *jose.JSONWebSignature, keys []jose.JSONWebKey, h jose.Header) ([]byte, bool) {
	for _, key := range keys {
		if (h.KeyID != "" && key.KeyID != h.KeyID) || (key.Use != "" && key.Use != "sig") ||
			(key.Algorithm != "" && key.Algorithm != h.Algorithm) {
			continue
		}
		if p, err := jws.Verify(key); err == nil {
			return p, true
		}
	}
	return nil, false
}
