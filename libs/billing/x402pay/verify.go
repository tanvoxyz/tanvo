package x402pay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// clockSkewTolerance is how far past the longest validity an EVM
// authorization may expire.
//
// source: app/services/x402/settlements/verify_service.rb:7
const clockSkewTolerance = 30 * time.Second

// VerifiedPayment is a payment the facilitator verified against the
// connection's terms: what a settlement records.
//
// source: app/services/x402/settlements/verified_payment.rb
type VerifiedPayment struct {
	// Connection is the connection paid through.
	Connection *x402.Connection
	// Payload is the payment and its requirements.
	Payload x402.PaymentPayload
	// PayerAddress is the payer derived from the payment, normalized.
	PayerAddress string
	// PaymentDigest identifies the payment.
	PaymentDigest string
	// VerifyResponse is the facilitator's verify answer.
	VerifyResponse x402.Object
}

// Network is the payment's network.
func (p *VerifiedPayment) Network() x402.Network { return p.Payload.Network() }

// Family is the family of the payment's network.
func (p *VerifiedPayment) Family() x402.Family { return p.Payload.Family() }

// Asset is the token address the requirements name.
func (p *VerifiedPayment) Asset() string { return p.Payload.Asset() }

// PayeeAddress is the transfer's recipient: the authorization's on EVM,
// the requirements' payTo on Solana.
func (p *VerifiedPayment) PayeeAddress() string {
	if p.Family() == x402.FamilyEVM {
		return p.Payload.To()
	}
	return p.Payload.PayTo()
}

// SettledAmountAtomic is the transferred amount in atomic units: the
// authorization's value on EVM, the requirements' amount on Solana; nil
// when it is not an unsigned integer.
func (p *VerifiedPayment) SettledAmountAtomic() *big.Int {
	var n *big.Int
	if p.Family() == x402.FamilyEVM {
		n, _ = p.Payload.Value()
	} else {
		n, _ = p.Payload.Amount()
	}
	return n
}

// SettledAmountCents is the transferred amount in cents, floored; nil
// when the amount or the token is unknown.
func (p *VerifiedPayment) SettledAmountCents() *big.Int {
	a, ok := x402.LookupAsset(p.Connection.Asset, p.Network())
	atomic := p.SettledAmountAtomic()
	if !ok || atomic == nil {
		return nil
	}
	return a.CentsFromAtomic(atomic)
}

// Verify checks payment against requirements, the connection's terms and
// the facilitator, and returns the verified payment. A payment the terms
// or the facilitator refuse is a validation failure of payment or
// payment_requirements; a facilitator failure, or a facilitator reporting
// another payer, is a *x402.FacilitatorError.
//
// source: app/services/x402/settlements/verify_service.rb
func (s *Service) Verify(ctx context.Context, c *x402.Connection, payment, requirements x402.Object) (*VerifiedPayment, error) {
	if !serialisable(payment) {
		return nil, fieldError("payment", "invalid_payment")
	}
	if !serialisable(requirements) {
		return nil, fieldError("payment_requirements", "invalid_payment_requirements")
	}
	p := x402.PaymentPayload{Payment: payment, Requirements: requirements}
	if field, code := termsFailure(c, p); code != "" {
		return nil, fieldError(field, code)
	}
	digest, err := x402.ComputeDigest(payment, p.Network(), p.Asset())
	var de *x402.DigestError
	if errors.As(err, &de) {
		return nil, fieldError("payment", de.Code)
	}
	if err != nil {
		return nil, fmt.Errorf("x402 payment digest: %w", err)
	}
	var payer string
	if p.Family() == x402.FamilyEVM {
		if code := s.evmFailure(c, p); code != "" {
			return nil, fieldError("payment", code)
		}
		payer = x402.NormalizeAddress(p.From(), x402.FamilyEVM)
	} else {
		sp, err := x402.ReadSolanaPayment(payment)
		if errors.Is(err, x402.ErrUnreadablePayment) {
			return nil, fieldError("payment", "unsupported_transaction")
		}
		if err != nil {
			return nil, fmt.Errorf("read solana payment: %w", err)
		}
		payer = sp.Payer()
	}
	fac, err := s.facilitator(ctx, c)
	if err != nil {
		return nil, err
	}
	verification, err := fac.Verify(ctx, payment, requirements)
	if err != nil {
		return nil, fmt.Errorf("x402 verify: %w", err)
	}
	if !verification.Valid {
		return nil, fieldError("payment", verification.InvalidReason)
	}
	var reported string
	if verification.Payer != "" {
		reported = x402.NormalizeAddress(verification.Payer, p.Family())
	}
	var anomaly string
	switch {
	case reported == "" && p.Family() == x402.FamilySVM:
		anomaly = "payer_missing"
	case reported != "" && reported != payer:
		anomaly = "payer_mismatch"
	}
	if anomaly != "" {
		s.log.WarnContext(ctx, "x402 verify failed", "reason", anomaly, "network", p.Network().String())
		correlation, _ := verification.Response["correlationId"].(string)
		return nil, &x402.FacilitatorError{
			Kind: x402.ErrFacilitatorUnavailable, Message: "verify: " + anomaly, ErrorType: anomaly, CorrelationID: correlation,
		}
	}
	return &VerifiedPayment{
		Connection: c, Payload: p, PayerAddress: payer, PaymentDigest: digest, VerifyResponse: verification.Response,
	}, nil
}

// termsFailure checks the payment against the connection's terms; it
// returns the failing field and code, or "" codes.
func termsFailure(c *x402.Connection, p x402.PaymentPayload) (field, code string) {
	n, f := p.Network(), p.Family()
	a, known := x402.LookupAsset(c.Asset, n)
	amount, _ := p.Amount()
	timeout, timeoutOK := p.MaxTimeout()
	switch {
	case p.X402Version() != x402.Version:
		return "payment", "unsupported_x402_version"
	case p.Scheme() != x402.SchemeExact:
		return "payment_requirements", "unsupported_scheme"
	case !c.Offers(n):
		return "payment_requirements", "unsupported_network"
	case !known || x402.NormalizeAddress(p.Asset(), f) != a.Address:
		return "payment_requirements", "unsupported_asset"
	case x402.NormalizeAddress(p.PayTo(), f) != c.PayoutAddresses[f]:
		return "payment_requirements", "invalid_pay_to"
	case amount == nil || amount.Cmp(a.AtomicUnitsPerCent()) < 0:
		return "payment_requirements", "invalid_amount"
	case !timeoutOK || timeout < 1 || timeout > x402.MaxTimeoutSeconds:
		return "payment_requirements", "invalid_max_timeout_seconds"
	}
	return "", ""
}

// evmFailure checks an EIP-3009 authorization: a valid payer, the
// connection's payout address as recipient, and an expiry within the
// longest validity.
func (s *Service) evmFailure(c *x402.Connection, p x402.PaymentPayload) string {
	timeout, _ := p.MaxTimeout()
	limit := big.NewInt(s.d.Clock.Now().Unix() + int64(timeout) + int64(clockSkewTolerance/time.Second))
	validBefore, ok := p.ValidBefore()
	switch {
	case !x402.ValidAddress(p.From(), x402.FamilyEVM):
		return "invalid_authorization"
	case x402.NormalizeAddress(p.To(), x402.FamilyEVM) != c.PayoutAddresses[x402.FamilyEVM]:
		return "invalid_pay_to"
	case !ok || validBefore.Cmp(limit) > 0:
		return "invalid_valid_before"
	}
	return ""
}

// serialisable reports whether v is an object that serialises to JSON
// without a NUL character.
func serialisable(v x402.Object) bool {
	if v == nil || !clean(v) {
		return false
	}
	_, err := json.Marshal(v)
	return err == nil
}

// clean reports whether v holds only valid UTF-8 strings without NUL.
func clean(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for k, nested := range v {
			if !cleanString(k) || !clean(nested) {
				return false
			}
		}
	case []any:
		for _, nested := range v {
			if !clean(nested) {
				return false
			}
		}
	case string:
		return cleanString(v)
	}
	return true
}

func cleanString(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
