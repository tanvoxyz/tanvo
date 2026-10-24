package x402pay_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/tanvoxyz/tanvo/libs/billing/x402pay"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// source: spec/services/x402/payment_requirements_service_spec.rb
func TestPaymentRequirements(t *testing.T) {
	const rotated = "HHU1aLQQCbCzW9ebjFTntq2vkvsQsxkDyPjMsW2WtiLG"
	solana := func(e *env) *x402.Connection {
		return e.connection([]x402.Network{devnet}, map[x402.Family]string{x402.FamilySVM: otherSVM})
	}
	requirements := func(t *testing.T, e *env, c *x402.Connection) ([]x402pay.Requirement, error) {
		t.Helper()
		return e.svc.PaymentRequirements(e.h.Ctx, c, 1_000)
	}

	// source: spec/services/x402/payment_requirements_service_spec.rb:20-34
	t.Run("with an EVM network", func(t *testing.T) {
		e := newEnv(t, now)
		got, err := requirements(t, e, e.connection([]x402.Network{baseSepolia}, map[x402.Family]string{x402.FamilyEVM: otherPayTo}))
		if err != nil {
			t.Fatal(err)
		}
		want := []x402pay.Requirement{{
			Scheme: "exact", Network: baseSepolia, Asset: x402.AssetUSDC, AssetAddress: sepoliaUSDC,
			Extra: map[string]any{"name": "USDC", "version": "2"}, PayTo: otherPayTo, AmountAtomic: "10000000", MaxTimeoutSeconds: 60,
		}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("requirements (-want +got):\n%s", diff)
		}
		if n := e.fac.count("supported"); n != 0 {
			t.Errorf("supported calls: got = %d, want = 0", n)
		}
	})
	// source: spec/services/x402/payment_requirements_service_spec.rb:36-47
	t.Run("with a Solana network", func(t *testing.T) {
		e := newEnv(t, now)
		got, err := requirements(t, e, solana(e))
		if err != nil {
			t.Fatal(err)
		}
		want := []x402pay.Requirement{{
			Scheme: "exact", Network: devnet, Asset: x402.AssetUSDC, AssetAddress: devnetUSDC,
			Extra: map[string]any{"feePayer": feePayer}, PayTo: otherSVM, AmountAtomic: "10000000", MaxTimeoutSeconds: 60,
		}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("requirements (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]x402.Credentials{{APIKeyID: "test-key-id", APIKeySecret: "test-key-secret"}}, e.fac.creds); diff != "" {
			t.Errorf("credentials (-want +got):\n%s", diff)
		}
	})
	// source: spec/services/x402/payment_requirements_service_spec.rb:49-62
	t.Run("when the fee payer rotates between challenges", func(t *testing.T) {
		e := newEnv(t, now)
		e.fac.supported = []func() (x402.SupportedResult, error){
			func() (x402.SupportedResult, error) { return supportedWith(devnet, feePayer), nil },
			func() (x402.SupportedResult, error) { return supportedWith(devnet, rotated), nil },
		}
		c := solana(e)
		var payers []any
		for range 2 {
			got, err := requirements(t, e, c)
			if err != nil {
				t.Fatal(err)
			}
			payers = append(payers, got[0].Extra["feePayer"])
		}
		if diff := cmp.Diff([]any{feePayer, rotated}, payers); diff != "" {
			t.Errorf("fee payers (-want +got):\n%s", diff)
		}
	})
	// source: spec/services/x402/payment_requirements_service_spec.rb:64-81
	t.Run("with a mainnet connection offering both families", func(t *testing.T) {
		e := newEnv(t, now)
		mainnet := x402.Network("solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp")
		e.fac.supported = []func() (x402.SupportedResult, error){func() (x402.SupportedResult, error) {
			return supportedWith(mainnet, feePayer), nil
		}}
		c := e.connection([]x402.Network{"eip155:8453", mainnet}, map[x402.Family]string{x402.FamilyEVM: otherPayTo, x402.FamilySVM: otherSVM})
		got, err := requirements(t, e, c)
		if err != nil {
			t.Fatal(err)
		}
		type entry struct {
			Network      x402.Network
			AssetAddress string
			Extra        map[string]any
		}
		var entries []entry
		for _, r := range got {
			entries = append(entries, entry{r.Network, r.AssetAddress, r.Extra})
		}
		want := []entry{
			{"eip155:8453", "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", map[string]any{"name": "USD Coin", "version": "2"}},
			{mainnet, "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", map[string]any{"feePayer": feePayer}},
		}
		if diff := cmp.Diff(want, entries); diff != "" {
			t.Errorf("entries (-want +got):\n%s", diff)
		}
	})
	failures := []struct {
		name, source string
		supported    func() (x402.SupportedResult, error)
		code, msg    string
	}{
		{"when /supported rejects the credentials", "83-92", func() (x402.SupportedResult, error) {
			return x402.SupportedResult{}, &x402.FacilitatorError{Kind: x402.ErrFacilitatorCredentials, Message: "supported: HTTP 401", HTTPStatus: 401}
		}, "credential_error", "credential_error: supported: HTTP 401"},
		{"when /supported is unavailable", "94-102", func() (x402.SupportedResult, error) {
			return x402.SupportedResult{}, &x402.FacilitatorError{Kind: x402.ErrFacilitatorUnavailable, Message: "supported: HTTP 503", HTTPStatus: 503}
		}, "unavailable_error", "unavailable_error: supported: HTTP 503"},
		{"when /supported lists no fee payer for the network", "104-112", func() (x402.SupportedResult, error) {
			return supportedWith(baseSepolia, feePayer), nil
		}, "unavailable_error", "unavailable_error: supported: no valid fee payer for " + string(devnet)},
		{"when the listed fee payer is not a Solana address", "114-122", func() (x402.SupportedResult, error) {
			return supportedWith(devnet, "0xnotsolana"), nil
		}, "unavailable_error", "unavailable_error: supported: no valid fee payer for " + string(devnet)},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/payment_requirements_service_spec.rb:{tt.source}
			e := newEnv(t, now)
			e.fac.supported = []func() (x402.SupportedResult, error){tt.supported}
			_, err := requirements(t, e, solana(e))
			var tp *x402pay.ThirdPartyError
			if !errors.As(err, &tp) {
				t.Fatalf("error: got = %v, want = a third-party error", err)
			}
			if tp.ThirdParty != x402.FacilitatorCoinbaseCDP || tp.Code != tt.code || tp.Message != tt.msg {
				t.Errorf("error: got = %s %s %q, want = coinbase_cdp %s %q", tp.ThirdParty, tp.Code, tp.Message, tt.code, tt.msg)
			}
		})
	}
}
