package x402

import (
	"encoding/json"
	"math/big"
)

// Object is a decoded JSON object, decoded with json.Decoder.UseNumber
// so that its numbers stay exact.
type Object = map[string]any

// The x402 protocol terms Tanvo accepts.
const (
	// Version is the x402 protocol version.
	Version = 2
	// SchemeExact is the payment scheme: an exact amount.
	SchemeExact = "exact"
	// MaxTimeoutSeconds is the longest validity of a payment.
	MaxTimeoutSeconds = 60
)

// PaymentPayload reads a payment (the X-PAYMENT header, decoded) and the
// payment requirements it answers; a missing or mistyped value reads as
// its zero.
//
// source: app/services/x402/payment_payload.rb
type PaymentPayload struct {
	// Payment is the agent's signed payment.
	Payment Object
	// Requirements is the requirement the payment answers.
	Requirements Object
}

// X402Version is the payment's protocol version, or 0.
func (p PaymentPayload) X402Version() int {
	n, ok := integer(p.Payment["x402Version"])
	if !ok {
		return 0
	}
	return n
}

// Scheme is the required scheme.
func (p PaymentPayload) Scheme() string { return str(p.Requirements["scheme"]) }

// Network is the required network.
func (p PaymentPayload) Network() Network { return Network(str(p.Requirements["network"])) }

// Asset is the required token address.
func (p PaymentPayload) Asset() string { return str(p.Requirements["asset"]) }

// PayTo is the required payee.
func (p PaymentPayload) PayTo() string { return str(p.Requirements["payTo"]) }

// Amount is the required amount in atomic units; false when it is not an
// unsigned integer.
func (p PaymentPayload) Amount() (*big.Int, bool) { return ParseUnsigned(p.Requirements["amount"]) }

// MaxTimeout is the required validity in seconds; false when it is not
// an integer.
func (p PaymentPayload) MaxTimeout() (int, bool) { return integer(p.Requirements["maxTimeoutSeconds"]) }

// Family is the family of the required network; "" when unsupported.
func (p PaymentPayload) Family() Family {
	f, _ := p.Network().Family()
	return f
}

// Authorization is the EIP-3009 authorization of an EVM payment.
func (p PaymentPayload) Authorization() Object { return object(p.payload()["authorization"]) }

// From is the authorization's payer.
func (p PaymentPayload) From() string { return str(p.Authorization()["from"]) }

// To is the authorization's payee.
func (p PaymentPayload) To() string { return str(p.Authorization()["to"]) }

// Value is the authorized amount; false when it is not an unsigned
// integer.
func (p PaymentPayload) Value() (*big.Int, bool) { return ParseUnsigned(p.Authorization()["value"]) }

// ValidBefore is the authorization's expiry in Unix seconds; false when
// it is not an unsigned integer.
func (p PaymentPayload) ValidBefore() (*big.Int, bool) {
	return ParseUnsigned(p.Authorization()["validBefore"])
}

// Transaction is the base64 Solana transaction of an SVM payment.
func (p PaymentPayload) Transaction() string { return str(p.payload()["transaction"]) }

func (p PaymentPayload) payload() Object { return object(p.Payment["payload"]) }

func object(v any) Object {
	if o, ok := v.(map[string]any); ok {
		return o
	}
	return Object{}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// integer reads a JSON integer (not a string): Ruby's Integer.
func integer(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case json.Number:
		n, err := x.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}
