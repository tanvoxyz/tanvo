package x402

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// jwtTTL is how long a request token is valid.
//
// source: app/services/x402/cdp/jwt.rb:6
const jwtTTL = 120 * time.Second

// errNotAKey is a CDP API key secret that is neither a base64 Ed25519 key
// nor an EC P-256 key in PEM.
var errNotAKey = errors.New("x402 cdp: the API key secret is not an Ed25519 or EC P-256 key")

// cdpToken is a request token of the CDP API.
//
// source: app/services/x402/cdp/jwt.rb
type cdpToken struct {
	keyID, secret      string
	method, host, path string
	now                time.Time
	random             io.Reader
}

type jwtClaims struct {
	Sub string   `json:"sub"`
	Iss string   `json:"iss"`
	Aud []string `json:"aud"`
	Nbf int64    `json:"nbf"`
	Iat int64    `json:"iat"`
	Exp int64    `json:"exp"`
	URI string   `json:"uri"`
}

type jwtHeader struct {
	Alg   string `json:"alg"`
	Kid   string `json:"kid"`
	Typ   string `json:"typ"`
	Nonce string `json:"nonce"`
}

// sign returns the token: EdDSA for a base64 64-byte Ed25519 key, ES256
// for a PEM key. Escaped newlines of a PEM secret are restored.
//
// source: app/services/x402/cdp/jwt.rb:12-21,46-58
func (t cdpToken) sign() (string, error) {
	secret := strings.ReplaceAll(strings.TrimSpace(t.secret), `\n`, "\n")
	nonce := make([]byte, 16)
	if _, err := io.ReadFull(t.random, nonce); err != nil {
		return "", fmt.Errorf("x402 cdp: token nonce: %w", err)
	}
	now := t.now.Unix()
	claims := jwtClaims{
		Sub: t.keyID, Iss: "cdp", Aud: []string{"cdp_service"},
		Nbf: now, Iat: now, Exp: now + int64(jwtTTL/time.Second),
		URI: strings.ToUpper(t.method) + " " + t.host + t.path,
	}
	header := jwtHeader{Kid: t.keyID, Typ: "JWT", Nonce: hex.EncodeToString(nonce)}
	if seed, ok := ed25519Seed(secret); ok {
		header.Alg = "EdDSA"
		input, err := signingInput(header, claims)
		if err != nil {
			return "", err
		}
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(input))
		return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
	}
	key, err := ecKey(secret)
	if err != nil {
		return "", err
	}
	header.Alg = "ES256"
	input, err := signingInput(header, claims)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(t.random, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("x402 cdp: sign token: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ed25519Seed returns the seed of a base64 secret of 64 bytes (seed and
// public key) that is not PEM.
func ed25519Seed(secret string) ([]byte, bool) {
	if strings.Contains(secret, "BEGIN") {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(secret)
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, false
	}
	return b[:ed25519.SeedSize], true
}

// ecKey parses an EC P-256 private key in SEC 1 or PKCS #8 PEM.
func ecKey(secret string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(secret))
	if block == nil {
		return nil, errNotAKey
	}
	var key *ecdsa.PrivateKey
	switch block.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errNotAKey, err)
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errNotAKey, err)
		}
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errNotAKey
		}
		key = ec
	default:
		return nil, errNotAKey
	}
	if key.Curve != elliptic.P256() {
		return nil, errNotAKey
	}
	return key, nil
}

func signingInput(header jwtHeader, claims jwtClaims) (string, error) {
	h, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("x402 cdp: token header: %w", err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("x402 cdp: token claims: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c), nil
}
