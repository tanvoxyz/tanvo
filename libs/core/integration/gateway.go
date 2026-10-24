package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// The interfaces of gateway.go, tax_gateway.go, accounting_gateway.go and
// crm_gateway.go are the tax, accounting and CRM provider ports
// (architecture §12): every adapter in providers/* implements [Gateway]
// and the port of its category, and apps/server wires one per
// [Provider]. They speak only core types and the neutral documents
// below; each adapter owns its settings and secret keys, its wire
// formats and its error mapping ([ProviderError]).

// Connection is an integration as an adapter sees it: the [Integration]
// with its secrets opened by apps/server.
type Connection struct {
	// ID is the integration.
	ID core.IntegrationID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is the integration code, unique per organization.
	Code string
	// Provider is the vendor.
	Provider Provider
	// Settings is the integration's non-secret settings object.
	Settings json.RawMessage
	// Secrets is the opened secrets object; adapters never log it.
	Secrets json.RawMessage
}

// Setting returns the string value of key in Settings, or "".
func (c Connection) Setting(key string) string {
	s, _ := getSetting(c.Settings, key)
	return s
}

// Secret returns the string value of key in Secrets, or "".
func (c Connection) Secret(key string) string {
	s, _ := getSetting(c.Secrets, key)
	return s
}

// ConnectionUpdate is what an adapter asks apps/server to store on the
// integration after a call (an OAuth token refreshed, a HubSpot object
// deployed): settings and secrets to set by key, an empty value removing
// the key. Keys left out are kept.
type ConnectionUpdate struct {
	// Settings are the settings to set.
	Settings map[string]string
	// Secrets are the secrets to set.
	Secrets map[string]string
}

// Empty reports whether u changes nothing.
func (u ConnectionUpdate) Empty() bool { return len(u.Settings) == 0 && len(u.Secrets) == 0 }

// ConnectionStore persists the [ConnectionUpdate] of an adapter; apps/server
// implements it over the integrations service, sealing the secrets.
type ConnectionStore interface {
	// UpdateConnection merges u into the stored integration conn.ID.
	UpdateConnection(ctx context.Context, conn Connection, u ConnectionUpdate) error
}

// Gateway is what every tax, accounting and CRM adapter implements.
type Gateway interface {
	// Provider returns the vendor the adapter serves.
	Provider() Provider
	// ValidateConnection checks the settings and secrets of a connection
	// without calling the provider; field errors are [core.ValidationError].
	ValidateConnection(conn Connection) error
}

// OAuthGateway is implemented by the adapters of providers connected by
// an OAuth 2.0 authorization code grant (Xero, HubSpot).
type OAuthGateway interface {
	Gateway
	// AuthorizationURL returns the provider's consent page for the
	// connection, redirecting to redirectURI with state.
	AuthorizationURL(conn Connection, redirectURI, state string) (string, error)
	// ExchangeCode trades the authorization code for tokens and returns
	// what to store on the integration (tokens, tenant or portal).
	ExchangeCode(ctx context.Context, conn Connection, redirectURI, code string) (ConnectionUpdate, error)
}

// Errors of the integration ports that are not about one provider call.
var (
	// ErrUnsupported: the provider lacks the operation.
	ErrUnsupported = errors.New("integration provider: operation not supported")
	// ErrNotConnected: the connection lacks the credentials or the OAuth
	// grant the call needs.
	ErrNotConnected = errors.New("integration provider: not connected")
)

// Classes of [ProviderError], matched with errors.Is.
var (
	// ErrRetryable matches failures worth retrying later: rate limits,
	// provider unavailability and contention.
	ErrRetryable = errors.New("integration provider: retryable")
	// ErrRequestLimit matches the provider's rate limit (lago
	// RequestLimitError and ThrottlingError), retried for longer than
	// the other retryable failures.
	ErrRequestLimit = errors.New("integration provider: request limit")
	// ErrRejected matches definitive refusals of the request's content:
	// validation errors, unknown objects, locked transactions.
	ErrRejected = errors.New("integration provider: rejected")
	// ErrAuthentication matches invalid or unauthorized credentials.
	ErrAuthentication = errors.New("integration provider: authentication failed")
)

// CodeValidationError is the [ProviderError] code of a document the
// provider answered with validation errors instead of creating it (lago
// aggregator process_hash_result: the failed record's validation_errors
// messages joined by ". ").
const CodeValidationError = "Validation error"

// ErrorKind classifies a provider failure.
type ErrorKind string

// ErrorKind values.
const (
	// KindRequestLimit: the provider throttled the call; retryable.
	KindRequestLimit ErrorKind = "request_limit"
	// KindUnavailable: network failure, timeout, bad gateway or another
	// provider-side error; retryable.
	KindUnavailable ErrorKind = "unavailable"
	// KindContention: the provider reported contention on its side (an
	// API limit, resource contention, out of memory); retryable.
	KindContention ErrorKind = "contention"
	// KindAuthentication: the connection's credentials are invalid or
	// lack a permission.
	KindAuthentication ErrorKind = "authentication"
	// KindNotFound: the provider object does not exist; rejected.
	KindNotFound ErrorKind = "not_found"
	// KindLocked: the provider refuses to change a transaction already
	// reported (Anrok transactionFrozenForFiling, Avalara
	// CannotModifyLockedTransaction); rejected.
	KindLocked ErrorKind = "locked"
	// KindInvalidRequest: the provider refused the request's content
	// (validation errors); rejected.
	KindInvalidRequest ErrorKind = "invalid_request"
	// KindPermanent: any other failure a retry will not fix; rejected.
	KindPermanent ErrorKind = "permanent"
)

// ProviderError is a failed provider call, classified. Adapters return it
// (wrapped or not) for every error the provider reports.
type ProviderError struct {
	// Provider is the vendor ("anrok").
	Provider Provider
	// Kind classifies the failure.
	Kind ErrorKind
	// Code is the provider's error code ("transactionFrozenForFiling",
	// "CannotModifyLockedTransaction"), or a Tanvo code when the provider
	// gives none.
	Code string
	// Message is the provider's message.
	Message string
	// HTTPStatus is the HTTP status of the response; 0 without one.
	HTTPStatus int
	// RetryAfter is the delay the provider asked for; 0 when none.
	RetryAfter time.Duration
}

// Error implements error.
func (e *ProviderError) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Provider))
	b.WriteString(": ")
	b.WriteString(string(e.Kind))
	if e.Code != "" {
		b.WriteString(" (")
		b.WriteString(e.Code)
		b.WriteString(")")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Retryable reports a failure worth retrying.
func (e *ProviderError) Retryable() bool {
	return e.Kind == KindRequestLimit || e.Kind == KindUnavailable || e.Kind == KindContention
}

// Rejected reports a definitive refusal of the request.
func (e *ProviderError) Rejected() bool {
	switch e.Kind {
	case KindNotFound, KindLocked, KindInvalidRequest, KindPermanent:
		return true
	default:
		return false
	}
}

// Is matches [ErrRetryable], [ErrRequestLimit], [ErrRejected] and
// [ErrAuthentication] by class.
func (e *ProviderError) Is(target error) bool {
	switch target {
	case ErrRetryable:
		return e.Retryable()
	case ErrRequestLimit:
		return e.Kind == KindRequestLimit
	case ErrRejected:
		return e.Rejected()
	case ErrAuthentication:
		return e.Kind == KindAuthentication
	default:
		return false
	}
}

// AsProviderError returns the [ProviderError] in err's chain.
func AsProviderError(err error) (*ProviderError, bool) {
	return errors.AsType[*ProviderError](err)
}

// ErrorCode returns the provider code of the [ProviderError] in err's
// chain, or fallback.
func ErrorCode(err error, fallback string) string {
	if pe, ok := AsProviderError(err); ok && pe.Code != "" {
		return pe.Code
	}
	return fallback
}

// Contention reports whether a provider message tells of its API limit
// or of contention or memory exhaustion on its side (lago
// app/services/integrations/aggregator/taxes/base_service.rb:67-68,112-113).
func Contention(message string) bool {
	return strings.Contains(message, "API limit") || strings.Contains(message, "resource contention") ||
		strings.Contains(message, "function_runtime_out_of_memory")
}
