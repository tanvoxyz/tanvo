package x402pay

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// Requirement is one way to pay a top-up: an exact transfer of the
// connection's token on one network.
type Requirement struct {
	// Scheme is "exact".
	Scheme string
	// Network is the network.
	Network x402.Network
	// Asset is the token's code.
	Asset x402.AssetCode
	// AssetAddress is the token's address on the network.
	AssetAddress string
	// Extra is the token's EIP-712 domain on EVM, {"feePayer": …} on
	// Solana.
	Extra map[string]any
	// PayTo is the connection's payout address of the network's family.
	PayTo string
	// AmountAtomic is the amount in the token's atomic units.
	AmountAtomic string
	// MaxTimeoutSeconds is the longest validity of a payment.
	MaxTimeoutSeconds int
}

// ThirdPartyError is a facilitator failure (lago's third_party_failure):
// Code is the reason of the *x402.FacilitatorError it wraps.
type ThirdPartyError struct {
	// ThirdParty is the facilitator.
	ThirdParty x402.FacilitatorKind
	// Code is credential_error, rate_limit_error or unavailable_error.
	Code string
	// Message is "<code>: <facilitator message>".
	Message string
	// Err is the facilitator failure.
	Err error
}

func (e *ThirdPartyError) Error() string { return e.Message }

// Unwrap returns the facilitator failure.
func (e *ThirdPartyError) Unwrap() error { return e.Err }

// thirdParty turns a facilitator failure of c into a *ThirdPartyError;
// other errors are returned as they are.
func thirdParty(c *x402.Connection, err error) error {
	var fe *x402.FacilitatorError
	if !errors.As(err, &fe) {
		return err
	}
	reason := fe.Reason()
	return &ThirdPartyError{ThirdParty: c.Facilitator, Code: reason, Message: reason + ": " + fe.Message, Err: err}
}

// PaymentRequirements returns the requirements of a top-up of amountCents
// through c, one per network of c, in network order. A Solana
// requirement carries the fee payer the facilitator lists, read afresh;
// a facilitator failure, or no valid fee payer, is a *ThirdPartyError.
//
// source: app/services/x402/payment_requirements_service.rb
func (s *Service) PaymentRequirements(ctx context.Context, c *x402.Connection, amountCents int64) ([]Requirement, error) {
	var supported *x402.SupportedResult
	out := make([]Requirement, 0, len(c.Networks))
	for _, n := range c.Networks {
		a, ok := x402.LookupAsset(c.Asset, n)
		f, fok := n.Family()
		if !ok || !fok {
			return nil, fmt.Errorf("x402 connection %s: no %s on %s", c.ID, c.Asset, n)
		}
		req := Requirement{
			Scheme: x402.SchemeExact, Network: n, Asset: c.Asset, AssetAddress: a.Address, Extra: a.EIP712Domain(),
			PayTo: c.PayoutAddresses[f], AmountAtomic: a.AtomicFromCents(amountCents).String(),
			MaxTimeoutSeconds: x402.MaxTimeoutSeconds,
		}
		if f == x402.FamilySVM {
			if supported == nil {
				fac, err := s.facilitator(ctx, c)
				if err != nil {
					return nil, err
				}
				r, err := fac.Supported(ctx)
				if err != nil {
					return nil, thirdParty(c, err)
				}
				supported = &r
			}
			payer := supported.FeePayer(n)
			if !x402.ValidAddress(payer, x402.FamilySVM) {
				return nil, thirdParty(c, &x402.FacilitatorError{
					Kind: x402.ErrFacilitatorUnavailable, Message: fmt.Sprintf("supported: no valid fee payer for %s", n),
				})
			}
			req.Extra = map[string]any{"feePayer": payer}
		}
		out = append(out, req)
	}
	return out, nil
}
