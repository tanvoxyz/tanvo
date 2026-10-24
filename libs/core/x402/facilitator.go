package x402

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// The facilitator failures, in [FacilitatorError.Kind].
var (
	// ErrFacilitatorCredentials is credentials the facilitator refuses
	// (HTTP 401, 402 or 403 other than a screening decline).
	ErrFacilitatorCredentials = errors.New("x402 facilitator: credential error")
	// ErrFacilitatorRateLimited is the facilitator throttling (HTTP 429).
	ErrFacilitatorRateLimited = errors.New("x402 facilitator: rate limited")
	// ErrFacilitatorUnavailable is no usable answer: no response, a
	// server error, a malformed answer, or a payer anomaly.
	ErrFacilitatorUnavailable = errors.New("x402 facilitator: unavailable")
)

// FacilitatorError is a facilitator call that failed; it is its Kind.
//
// source: app/services/x402/facilitator.rb:5-21
type FacilitatorError struct {
	// Kind is one of the facilitator failures.
	Kind error
	// Message says what failed.
	Message string
	// HTTPStatus, ErrorType and CorrelationID are what the facilitator
	// answered, when it did.
	HTTPStatus    int
	ErrorType     string
	CorrelationID string
}

func (e *FacilitatorError) Error() string { return fmt.Sprintf("%v: %s", e.Kind, e.Message) }

// Unwrap returns the kind.
func (e *FacilitatorError) Unwrap() error { return e.Kind }

// Reason is the snake_case name of the failure: credential_error,
// rate_limit_error or unavailable_error.
func (e *FacilitatorError) Reason() string {
	switch {
	case errors.Is(e.Kind, ErrFacilitatorCredentials):
		return "credential_error"
	case errors.Is(e.Kind, ErrFacilitatorRateLimited):
		return "rate_limit_error"
	default:
		return "unavailable_error"
	}
}

// VerifyResult is the facilitator's verdict on a payment.
type VerifyResult struct {
	// Valid is the verdict.
	Valid bool
	// Payer is the payer the facilitator reports.
	Payer string
	// InvalidReason is why an invalid payment is invalid.
	InvalidReason string
	// Response is the facilitator's answer.
	Response Object
}

// SettleOutcome is how a settle call ended.
type SettleOutcome string

// SettleOutcome values.
const (
	// SettleOutcomeSettled is a transfer the facilitator settled.
	SettleOutcomeSettled SettleOutcome = "settled"
	// SettleOutcomeUnconfirmedFailure is a refusal that may still land.
	SettleOutcomeUnconfirmedFailure SettleOutcome = "unconfirmed_failure"
	// SettleOutcomeSettlementPending is a transfer submitted, not final.
	SettleOutcomeSettlementPending SettleOutcome = "settlement_pending"
	// SettleOutcomeServerError is a server error or a malformed answer.
	SettleOutcomeServerError SettleOutcome = "server_error"
	// SettleOutcomeNoResponse is no answer at all.
	SettleOutcomeNoResponse SettleOutcome = "no_response"
)

// SettleResult is the facilitator's answer to a settle call; every
// outcome but settled leaves the transfer pending.
//
// source: app/services/x402/facilitator.rb:29-46
type SettleResult struct {
	// Outcome is how the call ended.
	Outcome SettleOutcome
	// Transaction is the transfer's hash or signature.
	Transaction string
	// Network and Payer are what the facilitator reports.
	Network, Payer string
	// ErrorReason is why it did not settle.
	ErrorReason string
	// Response is the facilitator's answer.
	Response Object
}

// Settled reports a settled transfer.
func (r SettleResult) Settled() bool { return r.Outcome == SettleOutcomeSettled }

// Pending reports a transfer that may still land: every known outcome
// but settled.
func (r SettleResult) Pending() bool { return r.Outcome.Valid() && !r.Settled() }

// Valid reports whether o is a known outcome.
func (o SettleOutcome) Valid() bool {
	switch o {
	case SettleOutcomeSettled, SettleOutcomeUnconfirmedFailure, SettleOutcomeSettlementPending,
		SettleOutcomeServerError, SettleOutcomeNoResponse:
		return true
	}
	return false
}

// SupportedKind is one payment kind the facilitator supports.
type SupportedKind = Object

// SupportedResult is the facilitator's supported payment kinds.
//
// source: app/services/x402/facilitator.rb:48-62
type SupportedResult struct {
	// Kinds are the supported kinds.
	Kinds []SupportedKind
	// Response is the facilitator's answer.
	Response Object
}

// Supports reports whether the exact scheme of x402 version 2 is
// supported on network.
func (r SupportedResult) Supports(network Network) bool { return r.kind(network) != nil }

// FeePayer returns the fee payer of the exact scheme of x402 version 2
// on network, or "".
func (r SupportedResult) FeePayer(network Network) string {
	return str(object(r.kind(network)["extra"])["feePayer"])
}

func (r SupportedResult) kind(network Network) SupportedKind {
	for _, k := range r.Kinds {
		if numberIs(k["x402Version"], Version) && str(k["network"]) == string(network) && str(k["scheme"]) == SchemeExact {
			return k
		}
	}
	return nil
}

// Facilitator verifies and settles payments for one connection. A failed
// call is a *FacilitatorError; Settle reports a missing answer as the
// no_response outcome instead.
type Facilitator interface {
	// Verify asks whether payment answers requirements.
	Verify(ctx context.Context, payment, requirements Object) (VerifyResult, error)
	// Settle submits payment.
	Settle(ctx context.Context, payment, requirements Object) (SettleResult, error)
	// Supported lists the supported payment kinds.
	Supported(ctx context.Context) (SupportedResult, error)
}

// Facilitators opens the facilitator of a connection with its opened
// credentials.
//
// source: app/services/x402/facilitator/client.rb
type Facilitators interface {
	// For returns the facilitator of kind for creds.
	For(kind FacilitatorKind, creds Credentials) (Facilitator, error)
}

// numberIs reports whether the JSON number v equals n, whatever its
// spelling (2 or 2.0).
func numberIs(v any, n int64) bool {
	var r *big.Rat
	switch x := v.(type) {
	case int:
		return int64(x) == n
	case int64:
		return x == n
	case float64:
		return x == float64(n)
	case json.Number:
		var ok bool
		if r, ok = new(big.Rat).SetString(string(x)); !ok {
			return false
		}
	default:
		return false
	}
	return r.IsInt() && r.Num().IsInt64() && r.Num().Int64() == n
}
