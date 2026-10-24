package x402pay_test

import (
	"math/big"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/x402pay"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// source: spec/services/x402/settlements/verified_payment_spec.rb
func TestVerifiedPayment(t *testing.T) {
	e := newEnv(t, now)
	c := e.sharedConnection()
	tests := []struct {
		name, source  string
		requirements  func() x402.Object
		payment       func(x402.Object) x402.Object
		family        x402.Family
		payee         string
		atomic, cents int64
	}{
		{
			"with an EVM payment", "22-34", func() x402.Object { return evmRequirements(t) },
			func(r x402.Object) x402.Object { return evmPayment(t, r) }, x402.FamilyEVM, evmPayout, 1_000, 0,
		},
		{"when the authorization value differs from the requirement amount", "36-43", func() x402.Object {
			r := evmRequirements(t)
			r["amount"] = "1050000"
			return r
		}, func(r x402.Object) x402.Object {
			p := evmPayment(t, r)
			authorization(p)["value"] = "1050001"
			return p
		}, x402.FamilyEVM, evmPayout, 1_050_001, 105},
		{"with a Solana payment", "46-59", func() x402.Object {
			r := svmRequirements(t)
			r["amount"] = "10000"
			return r
		}, func(r x402.Object) x402.Object { return svmPayment(t, r) }, x402.FamilySVM, svmPayout, 10_000, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/verified_payment_spec.rb:{tt.source}
			r := tt.requirements()
			vp := &x402pay.VerifiedPayment{
				Connection: c, Payload: x402.PaymentPayload{Payment: tt.payment(r), Requirements: r},
				PayerAddress: evmPayer, PaymentDigest: "digest", VerifyResponse: x402.Object{"isValid": true},
			}
			if vp.Family() != tt.family || vp.PayeeAddress() != tt.payee {
				t.Errorf("family, payee: got = %s %s, want = %s %s", vp.Family(), vp.PayeeAddress(), tt.family, tt.payee)
			}
			if got := vp.SettledAmountAtomic(); got == nil || got.Cmp(big.NewInt(tt.atomic)) != 0 {
				t.Errorf("settled_amount_atomic: got = %v, want = %d", got, tt.atomic)
			}
			if got := vp.SettledAmountCents(); got == nil || got.Cmp(big.NewInt(tt.cents)) != 0 {
				t.Errorf("settled_amount_cents: got = %v, want = %d", got, tt.cents)
			}
			if tt.family == x402.FamilyEVM && (vp.Network() != baseSepolia || vp.Asset() != sepoliaUSDC) {
				t.Errorf("network, asset: got = %s %s, want = %s %s", vp.Network(), vp.Asset(), baseSepolia, sepoliaUSDC)
			}
		})
	}
}
