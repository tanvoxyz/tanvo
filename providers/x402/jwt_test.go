package x402

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// source: spec/services/x402/cdp/jwt_spec.rb

var tokenAt = time.Unix(1789649271, 0)

func token(t *testing.T, secret, method, path string) string {
	t.Helper()
	tok, err := cdpToken{
		keyID: cdpKeyID, secret: secret, method: method, host: CDPHost, path: path, now: tokenAt, random: rand.Reader,
	}.sign()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func tokenParts(t *testing.T, tok string) (header, claims map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}
	for i, out := range []*map[string]any{&header, &claims} {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	return header, claims
}

func TestTokenEd25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := base64.StdEncoding.EncodeToString(priv)
	tok := token(t, secret, "POST", "/platform/v2/x402/settle")

	t.Run("is signed by the key", func(t *testing.T) { // :23-27
		i := strings.LastIndex(tok, ".")
		sig, err := base64.RawURLEncoding.DecodeString(tok[i+1:])
		if err != nil || !ed25519.Verify(pub, []byte(tok[:i]), sig) {
			t.Error("got a signature the key does not verify")
		}
	})

	t.Run("carries an EdDSA header naming the key", func(t *testing.T) { // :29-31
		h, _ := tokenParts(t, tok)
		if h["alg"] != "EdDSA" || h["kid"] != cdpKeyID || h["typ"] != "JWT" || !regexp.MustCompile(`\A[0-9a-f]{32}\z`).MatchString(h["nonce"].(string)) {
			t.Errorf("got header = %v", h)
		}
	})

	t.Run("binds the claims to the request, for two minutes", func(t *testing.T) { // :33-43
		_, c := tokenParts(t, tok)
		now := float64(tokenAt.Unix())
		want := map[string]any{
			"sub": cdpKeyID, "iss": "cdp", "aud": []any{"cdp_service"},
			"nbf": now, "iat": now, "exp": now + 120,
			"uri": "POST api.cdp.coinbase.com/platform/v2/x402/settle",
		}
		if !reflect.DeepEqual(c, want) {
			t.Errorf("got claims = %v, want = %v", c, want)
		}
	})

	t.Run("is a new token on every request", func(t *testing.T) { // :45-47
		if token(t, secret, "POST", "/platform/v2/x402/settle") == tok {
			t.Error("got the same token twice")
		}
	})

	t.Run("binds the method and path of a GET", func(t *testing.T) { // :49-56
		_, c := tokenParts(t, token(t, secret, "get", "/platform/v2/x402/supported"))
		if c["uri"] != "GET api.cdp.coinbase.com/platform/v2/x402/supported" {
			t.Errorf("got uri = %v", c["uri"])
		}
	})
}

func TestTokenES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sec1 := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	p8der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8der}))

	for name, secret := range map[string]string{
		"SEC 1":            sec1,                                 // :63-67
		"PKCS 8":           pkcs8,                                // OpenSSL::PKey.read reads both
		"escaped newlines": strings.ReplaceAll(sec1, "\n", `\n`), // :69-75
	} {
		t.Run(name, func(t *testing.T) {
			tok := token(t, secret, "POST", "/platform/v2/x402/settle")
			i := strings.LastIndex(tok, ".")
			sig, err := base64.RawURLEncoding.DecodeString(tok[i+1:])
			if err != nil || len(sig) != 64 {
				t.Fatalf("got signature of %d bytes, %v", len(sig), err)
			}
			digest := sha256.Sum256([]byte(tok[:i]))
			if !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
				t.Error("got a signature the key does not verify")
			}
			h, c := tokenParts(t, tok)
			if h["alg"] != "ES256" || h["kid"] != cdpKeyID || c["uri"] != "POST api.cdp.coinbase.com/platform/v2/x402/settle" {
				t.Errorf("got header = %v, claims = %v", h, c)
			}
		})
	}
}

func TestTokenNotAKey(t *testing.T) {
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	edDER, _ := x509.MarshalPKCS8PrivateKey(ed)
	for name, secret := range map[string]string{
		"text":             "not a key",
		"short base64":     base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"an Ed25519 PEM":   string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: edDER})),
		"a public key PEM": "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := cdpToken{keyID: cdpKeyID, secret: secret, method: "POST", host: CDPHost, path: "/x", now: tokenAt, random: rand.Reader}.sign()
			if err == nil {
				t.Error("got no error, want errNotAKey")
			}
		})
	}
}
