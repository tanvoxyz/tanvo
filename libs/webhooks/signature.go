package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Standard Webhooks headers.
const (
	// HeaderID carries the message id, the same for every attempt.
	HeaderID = "webhook-id"
	// HeaderTimestamp carries the attempt time in Unix seconds.
	HeaderTimestamp = "webhook-timestamp"
	// HeaderSignature carries space-separated "v1,<base64>" signatures.
	HeaderSignature = "webhook-signature"
)

// DefaultTolerance is the accepted distance between a message's timestamp
// and the verifier's clock.
const DefaultTolerance = 5 * time.Minute

// Verification errors.
var (
	// ErrInvalidSecret: the secret is not "whsec_" + base64.
	ErrInvalidSecret = errors.New("webhooks: invalid signing secret")
	// ErrMissingHeaders: a Standard Webhooks header is missing.
	ErrMissingHeaders = errors.New("webhooks: missing signature headers")
	// ErrTimestamp: the timestamp is malformed or outside the tolerance.
	ErrTimestamp = errors.New("webhooks: timestamp outside tolerance")
	// ErrSignature: no signature matches.
	ErrSignature = errors.New("webhooks: no matching signature")
)

// decodeSecret returns the HMAC key of a "whsec_" secret. The prefix is
// optional, as the specification allows bare base64 keys.
func decodeSecret(secret string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, webhook.SecretPrefix))
	if err != nil || len(key) == 0 {
		return nil, ErrInvalidSecret
	}
	return key, nil
}

// Sign returns the "v1,<base64>" signature of body for message id at
// timestamp ts: HMAC-SHA256 of "id.ts.body" keyed by the decoded secret.
func Sign(secret, id string, ts time.Time, body []byte) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return "v1," + base64.StdEncoding.EncodeToString(mac(key, id, ts.Unix(), body)), nil
}

func mac(key []byte, id string, ts int64, body []byte) []byte {
	h := hmac.New(sha256.New, key)
	fmt.Fprintf(h, "%s.%d.", id, ts)
	h.Write(body)
	return h.Sum(nil)
}

// SetHeaders signs body with every secret (several during a rotation) and
// sets the three Standard Webhooks headers on h.
func SetHeaders(h http.Header, secrets []string, id string, ts time.Time, body []byte) error {
	sigs := make([]string, 0, len(secrets))
	for _, s := range secrets {
		sig, err := Sign(s, id, ts, body)
		if err != nil {
			return err
		}
		sigs = append(sigs, sig)
	}
	h.Set(HeaderID, id)
	h.Set(HeaderTimestamp, strconv.FormatInt(ts.Unix(), 10))
	h.Set(HeaderSignature, strings.Join(sigs, " "))
	return nil
}

// Verify checks the Standard Webhooks headers of a received body against
// secret at now, accepting timestamps within tolerance (≤ 0 uses
// [DefaultTolerance]). Signatures of other versions are ignored.
func Verify(secret string, h http.Header, body []byte, now time.Time, tolerance time.Duration) error {
	key, err := decodeSecret(secret)
	if err != nil {
		return err
	}
	id, tsRaw, sigs := h.Get(HeaderID), h.Get(HeaderTimestamp), h.Get(HeaderSignature)
	if id == "" || tsRaw == "" || sigs == "" {
		return ErrMissingHeaders
	}
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return ErrTimestamp
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrTimestamp
	}
	want := mac(key, id, ts, body)
	for sig := range strings.FieldsSeq(sigs) {
		v, b64, ok := strings.Cut(sig, ",")
		if !ok || v != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrSignature
}
