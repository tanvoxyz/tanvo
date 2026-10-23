package webhook

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// MaxEndpoints is the number of webhook endpoints an organization may have
// (domain §18.1).
const MaxEndpoints = 10

// SecretPrefix starts every Standard Webhooks signing secret.
const SecretPrefix = "whsec_"

// Secret key sizes in bytes accepted by [Endpoint.Validate].
const (
	// MinSecretBytes is the shortest accepted key.
	MinSecretBytes = 24
	// MaxSecretBytes is the longest accepted key.
	MaxSecretBytes = 64
)

// Codes of endpoint rules (lago's names).
const (
	// CodeInvalidURL: the url is not an absolute http(s) URL.
	CodeInvalidURL = "url_invalid"
	// CodeInvalidTypes: event_types holds an unknown type.
	CodeInvalidTypes = "invalid_types"
	// CodeExceededLimit: the organization has [MaxEndpoints] endpoints.
	CodeExceededLimit = "exceeded_limit"
)

// Endpoint is a destination of webhooks (domain §18.1). Its URL is unique
// per organization.
type Endpoint struct {
	// ID identifies the endpoint.
	ID core.WebhookEndpointID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// URL is the absolute http(s) URL receiving the POSTs. HTTPS and the
	// SSRF policy are enforced by libs/webhooks.
	URL string
	// Name is an optional label, ≤ 255 characters.
	Name *string
	// EventTypes filters the events sent; nil sends every type, an empty
	// list sends none.
	EventTypes []EventType
	// Secret is the Standard Webhooks signing secret ("whsec_" + base64
	// key); rotatable.
	Secret string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NormalizeEventTypes trims, lower-cases and de-duplicates raw event types
// and drops blanks; ["*"] and nil give nil (every type). Unknown types are
// kept for [Endpoint.Validate] to report.
func NormalizeEventTypes(raw []string) []EventType {
	if raw == nil {
		return nil
	}
	out := []EventType{}
	for _, s := range raw {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !slices.Contains(out, EventType(s)) {
			out = append(out, EventType(s))
		}
	}
	if len(out) == 1 && out[0] == "*" {
		return nil
	}
	return out
}

// Validate checks the invariants of domain §18.1 that do not need other
// endpoints.
func (e *Endpoint) Validate() error {
	var v core.Validator
	if v.Required("url", e.URL) {
		v.Check(ValidURL(e.URL), "url", CodeInvalidURL)
	}
	v.OptionalString("name", e.Name, 255)
	for _, t := range e.EventTypes {
		if !v.Check(t.Valid(), "event_types", CodeInvalidTypes) {
			break
		}
	}
	v.Check(ValidSecret(e.Secret), "secret", core.CodeInvalidFormat)
	return v.Err()
}

// Subscribes reports whether the endpoint receives events of type t.
func (e *Endpoint) Subscribes(t EventType) bool {
	return e.EventTypes == nil || slices.Contains(e.EventTypes, t)
}

// ValidURL reports whether s is an absolute http or https URL with a host.
func ValidURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Hostname() != ""
}

// ValidSecret reports whether s is "whsec_" followed by the standard
// base64 encoding of a key of [MinSecretBytes]–[MaxSecretBytes] bytes.
func ValidSecret(s string) bool {
	rest, ok := strings.CutPrefix(s, SecretPrefix)
	if !ok {
		return false
	}
	key, err := base64.StdEncoding.DecodeString(rest)
	return err == nil && len(key) >= MinSecretBytes && len(key) <= MaxSecretBytes
}

// NewSecret returns a fresh signing secret with a 32-byte key read from r
// (crypto/rand.Reader in production).
func NewSecret(r io.Reader) (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return "", fmt.Errorf("webhook secret: %w", err)
	}
	return SecretPrefix + base64.StdEncoding.EncodeToString(key), nil
}

// CheckEndpointLimit refuses a new endpoint when the organization already
// has count endpoints and count ≥ [MaxEndpoints].
func CheckEndpointLimit(count int) error {
	if count >= MaxEndpoints {
		return core.NewRuleError(CodeExceededLimit, fmt.Sprintf("at most %d webhook endpoints per organization", MaxEndpoints))
	}
	return nil
}
