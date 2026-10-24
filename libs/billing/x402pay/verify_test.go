package x402pay_test

import (
	"encoding/base64"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/tanvoxyz/tanvo/libs/billing/x402pay"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// verifyCase is the payment of the verify specs: 1.050001 USDC on Base
// Sepolia, verified at 1789649271.
//
// source: spec/services/x402/settlements/verify_service_spec.rb:12-24
type verifyCase struct {
	*env
	connection   *x402.Connection
	payment      x402.Object
	requirements x402.Object
}

func newVerify(t *testing.T) *verifyCase {
	t.Helper()
	e := newEnv(t, now)
	r := evmRequirements(t)
	r["amount"] = "1050001"
	p := evmPayment(t, r)
	authorization(p)["value"] = "1050001"
	return &verifyCase{env: e, connection: e.sharedConnection(), payment: p, requirements: r}
}

// solana switches the case to the Solana payment of 0.01 USDC, verified
// with the buyer as payer.
//
// source: spec/services/x402/settlements/verify_service_spec.rb:284-292
func (v *verifyCase) solana(t *testing.T) {
	v.requirements = svmRequirements(t)
	v.requirements["amount"] = "10000"
	v.payment = svmPayment(t, v.requirements)
	v.fac.verify = func() (x402.VerifyResult, error) {
		return x402.VerifyResult{Valid: true, Payer: svmPayer, Response: x402.Object{"isValid": true, "payer": svmPayer}}, nil
	}
}

func (v *verifyCase) run() (*x402pay.VerifiedPayment, error) {
	return v.svc.Verify(v.h.Ctx, v.connection, v.payment, v.requirements)
}

// refusedBeforeVerify checks the refusal and that /verify was not called.
func (v *verifyCase) refusedBeforeVerify(t *testing.T, want string) {
	t.Helper()
	_, err := v.run()
	wantError(t, err, want)
	if n := v.fac.count("verify"); n != 0 {
		t.Errorf("verify calls: got = %d, want = 0", n)
	}
}

func TestVerifyValidEVM(t *testing.T) {
	// source: spec/services/x402/settlements/verify_service_spec.rb:45-77
	v := newVerify(t)
	vp, err := v.run()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	digest, err := x402.ComputeDigest(v.payment, baseSepolia, sepoliaUSDC)
	if err != nil {
		t.Fatal(err)
	}
	if vp.PayerAddress != evmPayer || vp.PaymentDigest != digest {
		t.Errorf("payer, digest: got = %s %s, want = %s %s", vp.PayerAddress, vp.PaymentDigest, evmPayer, digest)
	}
	if diff := cmp.Diff(x402.Object{"isValid": true, "payer": evmPayer}, vp.VerifyResponse); diff != "" {
		t.Errorf("verify response (-want +got):\n%s", diff)
	}
	if v.fac.count("verify") != 1 || v.fac.count("settle") != 0 {
		t.Errorf("calls: got = %v, want = one verify", v.fac.calls)
	}
	if diff := cmp.Diff(v.payment, v.fac.payments[0]); diff != "" {
		t.Errorf("forwarded payment (-want +got):\n%s", diff)
	}

	variants := []struct {
		name, source string
		change       func(*verifyCase)
	}{
		{"with the bazaar extension", "79-86", func(v *verifyCase) {
			v.payment["extensions"] = map[string]any{"bazaar": map[string]any{"discoverable": true}}
		}},
		{"with the asset in lowercase", "97-103", func(v *verifyCase) { v.requirements["asset"] = strings.ToLower(sepoliaUSDC) }},
		{"with the payTo in lowercase", "105-111", func(v *verifyCase) { v.requirements["payTo"] = strings.ToLower(evmPayout) }},
		{"with the payer in lowercase", "113-119", func(v *verifyCase) { authorization(v.payment)["from"] = strings.ToLower(evmPayer) }},
	}
	for _, tt := range variants {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/verify_service_spec.rb:{tt.source}
			v := newVerify(t)
			tt.change(v)
			vp, err := v.run()
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if vp.PayerAddress != evmPayer {
				t.Errorf("payer: got = %s, want = %s", vp.PayerAddress, evmPayer)
			}
			if diff := cmp.Diff(v.payment, v.fac.payments[0]); diff != "" {
				t.Errorf("forwarded payment (-want +got):\n%s", diff)
			}
		})
	}
	// Symbol-keyed inputs (verify_service_spec.rb:88-95) do not exist in Go.
}

func TestVerifyRefusals(t *testing.T) {
	tests := []struct {
		name, source string
		change       func(*verifyCase)
		want         string
	}{
		{"when the payment is missing", "122-126", func(v *verifyCase) { v.payment = nil }, "payment: invalid_payment"},
		{"when the requirements are missing", "128-132", func(v *verifyCase) { v.requirements = nil }, "payment_requirements: invalid_payment_requirements"},
		{"when the payment holds invalid UTF-8", "134-138", func(v *verifyCase) { v.payment["resource"] = "\xff" }, "payment: invalid_payment"},
		{"when the payment holds a NUL in a nested value", "140-144", func(v *verifyCase) {
			v.payment["extensions"] = map[string]any{"note": "a\x00b"}
		}, "payment: invalid_payment"},
		{"when the payment holds a NUL in a key", "146-150", func(v *verifyCase) {
			v.payment["extensions"] = map[string]any{"a\x00b": "note"}
		}, "payment: invalid_payment"},
		{"when the payment holds a NUL in an array", "152-156", func(v *verifyCase) {
			v.payment["extensions"] = map[string]any{"notes": []any{"ok", "a\x00b"}}
		}, "payment: invalid_payment"},
		{"when the requirements hold a NUL in extra", "158-162", func(v *verifyCase) {
			v.requirements = maps.Clone(v.requirements) // the payment keeps the clean copy
			v.requirements["extra"] = map[string]any{"name": "a\x00b"}
		}, "payment_requirements: invalid_payment_requirements"},
		{"with x402Version 1", "164-168", func(v *verifyCase) { v.payment["x402Version"] = 1 }, "payment: unsupported_x402_version"},
		{"with another scheme", "170-174", func(v *verifyCase) { v.requirements["scheme"] = "upto" }, "payment_requirements: unsupported_scheme"},
		{"with a network the merchant does not offer", "176-180", func(v *verifyCase) { v.requirements["network"] = "eip155:8453" }, "payment_requirements: unsupported_network"},
		{"with an unknown network", "182-186", func(v *verifyCase) { v.requirements["network"] = "eip155:1" }, "payment_requirements: unsupported_network"},
		{"with another asset", "188-192", func(v *verifyCase) {
			v.requirements["asset"] = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
		}, "payment_requirements: unsupported_asset"},
		{"with another payTo", "194-198", func(v *verifyCase) { v.requirements["payTo"] = otherPayTo }, "payment_requirements: invalid_pay_to"},
		{"with an amount of zero", "200-204", func(v *verifyCase) { v.requirements["amount"] = "0" }, "payment_requirements: invalid_amount"},
		{"with an amount below a cent", "206-210", func(v *verifyCase) { v.requirements["amount"] = "9999" }, "payment_requirements: invalid_amount"},
		{"with a maxTimeoutSeconds of 0", "212-216", func(v *verifyCase) { v.requirements["maxTimeoutSeconds"] = 0 }, "payment_requirements: invalid_max_timeout_seconds"},
		{"with a maxTimeoutSeconds of 61", "218-222", func(v *verifyCase) { v.requirements["maxTimeoutSeconds"] = 61 }, "payment_requirements: invalid_max_timeout_seconds"},
		{"with a string maxTimeoutSeconds", "224-228", func(v *verifyCase) { v.requirements["maxTimeoutSeconds"] = "60" }, "payment_requirements: invalid_max_timeout_seconds"},
		{"with an authorization without a nonce", "230-234", func(v *verifyCase) { delete(authorization(v.payment), "nonce") }, "payment: invalid_authorization"},
		{"with a payer whose checksum is wrong", "236-240", func(v *verifyCase) {
			authorization(v.payment)["from"] = "0xF4a43b9cc729c9E4E139CB86808f48e3eD09Dcb2"
		}, "payment: invalid_authorization"},
		{"with an authorization paying another address", "242-246", func(v *verifyCase) { authorization(v.payment)["to"] = otherPayTo }, "payment: invalid_pay_to"},
		{"when the agent's clock is 31 seconds ahead", "271-275", func(v *verifyCase) {
			authorization(v.payment)["validBefore"] = "1789649362"
		}, "payment: invalid_valid_before"},
		{"with a maxTimeoutSeconds of 10", "277-281", func(v *verifyCase) { v.requirements["maxTimeoutSeconds"] = 10 }, "payment: invalid_valid_before"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/verify_service_spec.rb:{tt.source}
			v := newVerify(t)
			tt.change(v)
			v.refusedBeforeVerify(t, tt.want)
		})
	}
	// source: spec/services/x402/settlements/verify_service_spec.rb:248-269
	for _, validBefore := range []string{"1789649331", "1789649332", "1789649361"} {
		v := newVerify(t)
		authorization(v.payment)["validBefore"] = validBefore
		if _, err := v.run(); err != nil {
			t.Errorf("validBefore %s: got = %v, want = success", validBefore, err)
		}
	}
}

func TestVerifySolana(t *testing.T) {
	// source: spec/services/x402/settlements/verify_service_spec.rb:294-305
	t.Run("derives the Solana signer", func(t *testing.T) {
		v := newVerify(t)
		v.solana(t)
		vp, err := v.run()
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if vp.PayerAddress != svmPayer || v.fac.count("verify") != 1 {
			t.Errorf("payer, verify calls: got = %s %d, want = %s 1", vp.PayerAddress, v.fac.count("verify"), svmPayer)
		}
	})
	transaction := func(v *verifyCase, b []byte) {
		v.payment["payload"] = map[string]any{"transaction": base64.StdEncoding.EncodeToString(b)}
	}
	refusals := []struct {
		name, source string
		change       func(*testing.T, *verifyCase)
		want         string
	}{
		{"with a durable nonce", "307-318", func(t *testing.T, v *verifyCase) {
			transaction(v, buildSolana(t, []string{feePayerKey, svmPayer, systemProgram, tokenProgram}, []instruction{
				{2, []byte{1, 0, 1}, []byte{4, 0, 0, 0}}, {3, []byte{1, 1, 1, 1}, transferCheckedData(10_000)},
			}, [][]byte{sig(0), sig(2)}))
		}, "payment: unsupported_transaction"},
		{"without a TransferChecked", "320-324", func(t *testing.T, v *verifyCase) {
			transaction(v, buildSolana(t, []string{svmPayer}, nil, [][]byte{sig(2)}))
		}, "payment: unsupported_transaction"},
		{"when the authority does not sign", "326-337", func(t *testing.T, v *verifyCase) {
			transaction(v, buildSolana(t, []string{feePayerKey, svmPayer, tokenProgram}, []instruction{
				{2, []byte{1, 1, 1, 1}, transferCheckedData(10_000)},
			}, [][]byte{sig(0)}))
		}, "payment: unsupported_transaction"},
		{"when the transaction is not base64", "339-343", func(_ *testing.T, v *verifyCase) {
			v.payment["payload"] = map[string]any{"transaction": "not base64!"}
		}, "payment: invalid_transaction"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/verify_service_spec.rb:{tt.source}
			v := newVerify(t)
			v.solana(t)
			tt.change(t, v)
			v.refusedBeforeVerify(t, tt.want)
		})
	}
	anomalies := []struct {
		name, source, payer, reason string
	}{
		{"when verify answers no payer", "345-371", "", "payer_missing"},
		{"when verify answers another payer", "373-379", svmPayout, "payer_mismatch"},
	}
	for _, tt := range anomalies {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/verify_service_spec.rb:{tt.source}
			v := newVerify(t)
			v.solana(t)
			v.fac.verify = func() (x402.VerifyResult, error) {
				return x402.VerifyResult{Valid: true, Payer: tt.payer, Response: x402.Object{"isValid": true, "correlationId": "corr-1"}}, nil
			}
			_, err := v.run()
			wantAnomaly(t, v.env, err, tt.reason, devnet)
		})
	}
}

// wantAnomaly checks a payer anomaly: an unavailable facilitator error of
// the reason, logged once, with no settle call.
func wantAnomaly(t *testing.T, e *env, err error, reason string, network x402.Network) {
	t.Helper()
	var fe *x402.FacilitatorError
	if !errors.As(err, &fe) || !errors.Is(err, x402.ErrFacilitatorUnavailable) || fe.ErrorType != reason {
		t.Fatalf("error: got = %v, want = an unavailable error of %s", err, reason)
	}
	if e.fac.count("settle") != 0 {
		t.Errorf("settle calls: got = %d, want = 0", e.fac.count("settle"))
	}
	want := "reason=" + reason + " network=" + string(network)
	if got := e.log.String(); strings.Count(got, "level=WARN") != 1 || !strings.Contains(got, want) {
		t.Errorf("log: got = %q, want = one warning with %q", got, want)
	}
}

func TestVerifyFacilitatorAnswers(t *testing.T) {
	// source: spec/services/x402/settlements/verify_service_spec.rb:383-402
	for _, reason := range []string{"invalid_exact_evm_payload_signature", "kyt_risk_detected"} {
		v := newVerify(t)
		v.fac.verify = func() (x402.VerifyResult, error) {
			return x402.VerifyResult{Valid: false, InvalidReason: reason, Payer: evmPayer}, nil
		}
		_, err := v.run()
		wantError(t, err, "payment: "+reason)
	}
	// source: spec/services/x402/settlements/verify_service_spec.rb:404-457
	for _, kind := range []error{x402.ErrFacilitatorUnavailable, x402.ErrFacilitatorCredentials, x402.ErrFacilitatorRateLimited} {
		v := newVerify(t)
		v.fac.verify = func() (x402.VerifyResult, error) {
			return x402.VerifyResult{}, &x402.FacilitatorError{Kind: kind, Message: "verify: failed"}
		}
		if _, err := v.run(); !errors.Is(err, kind) {
			t.Errorf("error: got = %v, want = %v", err, kind)
		}
	}
	// source: spec/services/x402/settlements/verify_service_spec.rb:460-475
	for _, payer := range []string{"", strings.ToLower(evmPayer)} {
		v := newVerify(t)
		v.fac.verify = func() (x402.VerifyResult, error) { return x402.VerifyResult{Valid: true, Payer: payer}, nil }
		vp, err := v.run()
		if err != nil || vp.PayerAddress != evmPayer {
			t.Errorf("payer %q: got = %v, %v, want = %s", payer, vp, err, evmPayer)
		}
	}
	// source: spec/services/x402/settlements/verify_service_spec.rb:477-493
	v := newVerify(t)
	v.fac.verify = func() (x402.VerifyResult, error) { return x402.VerifyResult{Valid: true, Payer: otherPayTo}, nil }
	_, err := v.run()
	wantAnomaly(t, v.env, err, "payer_mismatch", baseSepolia)
}
