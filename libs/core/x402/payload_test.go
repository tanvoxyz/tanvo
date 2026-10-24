package x402_test

import (
	"encoding/json"
	"maps"
	"math/big"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

func amount(n *big.Int, ok bool) string {
	if !ok {
		return "<nil>"
	}
	return n.String()
}

func timeout(n int, ok bool) any {
	if !ok {
		return nil
	}
	return n
}

func TestPaymentPayload(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/payment_payload_spec.rb:13
	p := x402.PaymentPayload{Payment: evmPayment(t), Requirements: evmRequirements(t)}
	got := []any{
		p.X402Version(), p.Scheme(), p.Network(), p.Asset(), p.PayTo(), amount(p.Amount()), timeout(p.MaxTimeout()),
		p.Family(), p.From(), p.To(), amount(p.Value()), amount(p.ValidBefore()), p.Transaction(),
	}
	want := []any{
		2, "exact", x402.Network("eip155:84532"), "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		"0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5", "1000", 60, x402.FamilyEVM,
		"0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2", "0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5", "1000", "1789649331", "",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attribute %d: got = %v, want = %v", i, got[i], want[i])
		}
	}

	// source: spec/services/x402/payment_payload_spec.rb:56
	s := x402.PaymentPayload{Payment: svmPayment(t), Requirements: svmRequirements(t)}
	if s.Family() != x402.FamilySVM || s.Transaction() == "" || s.From() != "" || s.To() != "" {
		t.Errorf("solana: got = %q, %q, %q, %q", s.Family(), s.Transaction(), s.From(), s.To())
	}
	if _, ok := s.Value(); ok {
		t.Error("solana value: got = set, want = nil")
	}
	if _, ok := s.ValidBefore(); ok {
		t.Error("solana valid before: got = set, want = nil")
	}

	// source: spec/services/x402/payment_payload_spec.rb:72
	r := evmRequirements(t)
	r["network"] = "eip155:1"
	if f := (x402.PaymentPayload{Payment: evmPayment(t), Requirements: r}).Family(); f != "" {
		t.Errorf("unknown network family: got = %q, want = none", f)
	}

	// source: spec/services/x402/payment_payload_spec.rb:78
	n := x402.PaymentPayload{Requirements: evmRequirements(t)}
	if n.X402Version() != 0 || n.From() != "" || n.Transaction() != "" {
		t.Errorf("no payment: got = %d, %q, %q", n.X402Version(), n.From(), n.Transaction())
	}

	// source: spec/services/x402/payment_payload_spec.rb:84
	n = x402.PaymentPayload{Payment: evmPayment(t)}
	if _, ok := n.Amount(); ok || n.Network() != "" {
		t.Errorf("no requirements: got = %q", n.Network())
	}

	// source: spec/services/x402/payment_payload_spec.rb:90
	pm := evmPayment(t)
	pm["payload"] = "x"
	n = x402.PaymentPayload{Payment: pm, Requirements: evmRequirements(t)}
	if len(n.Authorization()) != 0 || n.From() != "" {
		t.Errorf("payload not an object: got = %v, %q", n.Authorization(), n.From())
	}
}

func TestPaymentPayloadAmount(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/payment_payload_spec.rb:96-102
	for _, a := range []any{"1.5", "-1", nil} {
		r := evmRequirements(t)
		r["amount"] = a
		if got, ok := (x402.PaymentPayload{Requirements: r}).Amount(); ok {
			t.Errorf("amount %v: got = %v, want = nil", a, got)
		}
	}
	// source: spec/services/x402/payment_payload_spec.rb:104-110
	for _, v := range []any{"60", 60.0, json.Number("60.0")} {
		r := maps.Clone(evmRequirements(t))
		r["maxTimeoutSeconds"] = v
		if got, ok := (x402.PaymentPayload{Requirements: r}).MaxTimeout(); ok {
			t.Errorf("max timeout %v: got = %d, want = nil", v, got)
		}
	}
}
