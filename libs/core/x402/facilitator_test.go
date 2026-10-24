package x402_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// source: spec/services/x402/facilitator_spec.rb

func TestSettleResult(t *testing.T) {
	t.Parallel()
	// source: facilitator_spec.rb:9-23
	for o, want := range map[x402.SettleOutcome][2]bool{
		x402.SettleOutcomeSettled:            {true, false},
		x402.SettleOutcomeUnconfirmedFailure: {false, true},
		x402.SettleOutcomeSettlementPending:  {false, true},
		x402.SettleOutcomeServerError:        {false, true},
		x402.SettleOutcomeNoResponse:         {false, true},
	} {
		r := x402.SettleResult{Outcome: o}
		if got := [2]bool{r.Settled(), r.Pending()}; got != want {
			t.Errorf("%s: got = %v, want = %v", o, got, want)
		}
	}
	// source: facilitator_spec.rb:25-31
	if x402.SettleOutcome("lost").Valid() {
		t.Error("unknown outcome: got = valid, want = invalid")
	}
}

func supportedFixture(t *testing.T) x402.SupportedResult {
	t.Helper()
	raw, err := os.ReadFile("testdata/supported.json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Kinds []x402.Object }
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return x402.SupportedResult{Kinds: body.Kinds}
}

func TestSupportedResult(t *testing.T) {
	t.Parallel()
	s := supportedFixture(t)
	// source: facilitator_spec.rb:38-48
	for n, want := range map[x402.Network]bool{"eip155:84532": true, "eip155:1": false, "base-sepolia": false} {
		if got := s.Supports(n); got != want {
			t.Errorf("supports %s: got = %v, want = %v", n, got, want)
		}
	}
	// source: facilitator_spec.rb:50-60
	for n, want := range map[x402.Network]string{
		"solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1": "GVJJ7rdGiXr5xaYbRwRbjfaJL7fmwRygFi1H6aGqDveb",
		"solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp": "",
		"eip155:84532": "",
	} {
		if got := s.FeePayer(n); got != want {
			t.Errorf("fee payer %s: got = %q, want = %q", n, got, want)
		}
	}
	// source: facilitator_spec.rb:62-73
	first := x402.SupportedResult{Kinds: []x402.Object{
		{"x402Version": 2.0, "scheme": "upto", "network": "solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1", "extra": x402.Object{"feePayer": "HHU1aLQQCbCzW9ebjFTntq2vkvsQsxkDyPjMsW2WtiLG"}},
		{"x402Version": 2, "scheme": "exact", "network": "solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1", "extra": x402.Object{"feePayer": "GVJJ7rdGiXr5xaYbRwRbjfaJL7fmwRygFi1H6aGqDveb"}},
	}}
	if got := first.FeePayer("solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"); got != "GVJJ7rdGiXr5xaYbRwRbjfaJL7fmwRygFi1H6aGqDveb" {
		t.Errorf("exact kind fee payer: got = %q", got)
	}
}

func TestFacilitatorError(t *testing.T) {
	t.Parallel()
	// source: facilitator_spec.rb:76-82
	err := error(&x402.FacilitatorError{Kind: x402.ErrFacilitatorCredentials, Message: "verify: HTTP 401", HTTPStatus: 401, CorrelationID: "corr-1"})
	var fe *x402.FacilitatorError
	if !errors.As(err, &fe) || !errors.Is(err, x402.ErrFacilitatorCredentials) || fe.HTTPStatus != 401 || fe.CorrelationID != "corr-1" || fe.Reason() != "credential_error" {
		t.Errorf("credential error: got = %v", err)
	}
}
