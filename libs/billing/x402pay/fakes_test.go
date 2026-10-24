package x402pay_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/x402pay"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// The shared x402 payment fixtures.
//
// source: spec/support/shared_context/x402_payment.rb
const (
	evmPayout   = "0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5"
	svmPayout   = "HHU1aLQQCbCzW9ebjFTntq2vkvsQsxkDyPjMsW2WtiLG"
	evmPayer    = "0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2"
	svmPayer    = "BprZ3eTVMHAcqC2wcE4XY71tvjdxJ6C6pSYjVmD75ujf"
	baseSepolia = x402.Network("eip155:84532")
	devnet      = x402.Network("solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1")
	sepoliaUSDC = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	devnetUSDC  = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"
	feePayer    = "GVJJ7rdGiXr5xaYbRwRbjfaJL7fmwRygFi1H6aGqDveb"
	otherPayTo  = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"
	otherSVM    = "2wKupLR9q6wXYppw8Gr2NvWxKBUqm4PPJKkQfoxHDBg4"
)

// now is the instant of the verify and settle specs.
var now = time.Unix(1_789_649_271, 0).UTC()

// decode decodes JSON the way payments are decoded: numbers stay exact.
func decode(t *testing.T, s string) x402.Object {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var o x402.Object
	if err := d.Decode(&o); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return o
}

func evmRequirements(t *testing.T) x402.Object {
	return decode(t, `{"scheme":"exact","network":"eip155:84532","amount":"1000",
		"asset":"0x036CbD53842c5426634e7929541eC2318f3dCF7e","payTo":"0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5",
		"maxTimeoutSeconds":60,"extra":{"name":"USDC","version":"2"}}`)
}

func evmPayment(t *testing.T, requirements x402.Object) x402.Object {
	p := decode(t, `{"x402Version":2,
		"resource":{"url":"https://api.example.com/report","description":"Report","mimeType":"application/json"},
		"payload":{"signature":"0x`+strings.Repeat("ab", 65)+`","authorization":{
			"from":"0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2","to":"0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5",
			"value":"1000","validAfter":"1789648671","validBefore":"1789649331",
			"nonce":"0x8568d530303a96028f64623cfc5c7bbb166c4aa2889b25bbe09e1d699193f174"}},
		"extensions":{}}`)
	p["accepted"] = requirements
	return p
}

// authorization returns the authorization of an EVM payment.
func authorization(p x402.Object) x402.Object {
	return p["payload"].(map[string]any)["authorization"].(map[string]any)
}

func svmRequirements(t *testing.T) x402.Object {
	return decode(t, `{"scheme":"exact","network":"solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1","amount":"1000",
		"asset":"4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU","payTo":"HHU1aLQQCbCzW9ebjFTntq2vkvsQsxkDyPjMsW2WtiLG",
		"maxTimeoutSeconds":60,"extra":{"feePayer":"GVJJ7rdGiXr5xaYbRwRbjfaJL7fmwRygFi1H6aGqDveb"}}`)
}

// svmPayment is the devnet transfer of the fixture without the fee
// payer's signature: what the agent sends.
func svmPayment(t *testing.T, requirements x402.Object) x402.Object {
	t.Helper()
	raw, err := os.ReadFile("testdata/solana_devnet_transfer.json")
	if err != nil {
		t.Fatal(err)
	}
	var landed struct{ Transaction []string }
	if err := json.Unmarshal(raw, &landed); err != nil {
		t.Fatal(err)
	}
	b, err := base64.StdEncoding.DecodeString(landed.Transaction[0])
	if err != nil {
		t.Fatal(err)
	}
	copy(b[1:65], make([]byte, 64))
	return x402.Object{
		"x402Version": json.Number("2"), "accepted": requirements,
		"payload": map[string]any{"transaction": base64.StdEncoding.EncodeToString(b)},
	}
}

// sealer seals by prefixing the organization.
type sealer struct{}

func (sealer) Seal(_ context.Context, org core.OrganizationID, plaintext []byte) ([]byte, error) {
	return append([]byte(org.String()+":"), plaintext...), nil
}

func (sealer) Open(_ context.Context, org core.OrganizationID, sealed []byte) ([]byte, error) {
	rest, ok := bytes.CutPrefix(sealed, []byte(org.String()+":"))
	if !ok {
		return nil, errors.New("sealed for another organization")
	}
	return rest, nil
}

// facilitator is a scripted facilitator recording its calls.
type facilitator struct {
	mu        sync.Mutex
	verify    func() (x402.VerifyResult, error)
	settle    func() (x402.SettleResult, error)
	supported []func() (x402.SupportedResult, error)
	calls     []string
	creds     []x402.Credentials
	payments  []x402.Object
	onSettle  func()
}

func (f *facilitator) For(kind x402.FacilitatorKind, creds x402.Credentials) (x402.Facilitator, error) {
	if kind != x402.FacilitatorCoinbaseCDP {
		return nil, errors.New("unknown facilitator")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds = append(f.creds, creds)
	return f, nil
}

func (f *facilitator) record(call string, payment x402.Object) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.payments = append(f.payments, payment)
}

func (f *facilitator) Verify(_ context.Context, payment, _ x402.Object) (x402.VerifyResult, error) {
	f.record("verify", payment)
	if f.verify == nil {
		return x402.VerifyResult{Valid: true, Payer: evmPayer, Response: x402.Object{"isValid": true, "payer": evmPayer}}, nil
	}
	return f.verify()
}

func (f *facilitator) Settle(_ context.Context, payment, _ x402.Object) (x402.SettleResult, error) {
	f.record("settle", payment)
	if f.onSettle != nil {
		f.onSettle()
	}
	if f.settle == nil {
		return settled(settledHash, string(baseSepolia)), nil
	}
	return f.settle()
}

func (f *facilitator) Supported(context.Context) (x402.SupportedResult, error) {
	f.mu.Lock()
	n := 0
	for _, c := range f.calls {
		if c == "supported" {
			n++
		}
	}
	f.mu.Unlock()
	f.record("supported", nil)
	if len(f.supported) == 0 {
		return supportedWith(devnet, feePayer), nil
	}
	return f.supported[min(n, len(f.supported)-1)]()
}

// count returns how many times call was made.
func (f *facilitator) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

var settledHash = "0x" + strings.Repeat("cd", 32)

// settled is a settled answer of a hash on a network.
func settled(hash, network string) x402.SettleResult {
	return x402.SettleResult{
		Outcome: x402.SettleOutcomeSettled, Transaction: hash, Network: network,
		Response: x402.Object{"success": true, "transaction": hash, "network": network},
	}
}

// supportedWith lists the exact scheme on network with a fee payer.
func supportedWith(network x402.Network, payer string) x402.SupportedResult {
	k := x402.Object{
		"x402Version": json.Number("2"), "scheme": "exact", "network": string(network),
		"extra": map[string]any{"feePayer": payer},
	}
	return x402.SupportedResult{Kinds: []x402.SupportedKind{k}, Response: x402.Object{"kinds": []any{k}}}
}

// env is a harness with an x402 service over a scripted facilitator.
type env struct {
	t   *testing.T
	h   *billingtest.Harness
	svc *x402pay.Service
	fac *facilitator
	log *bytes.Buffer
}

func newEnv(t *testing.T, at time.Time) *env {
	t.Helper()
	h := billingtest.New(t, at)
	return newEnvOn(t, h)
}

func newEnvOn(t *testing.T, h *billingtest.Harness) *env {
	t.Helper()
	var log bytes.Buffer
	h.Deps.Logger = slog.New(slog.NewTextHandler(&log, nil))
	fac := &facilitator{}
	svc, err := x402pay.New(h.Deps, sealer{}, fac)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &env{t: t, h: h, svc: svc, fac: fac, log: &log}
}

// connection creates a connection on networks paying out to payouts.
func (e *env) connection(networks []x402.Network, payouts map[x402.Family]string, opts ...func(*x402pay.ConnectionInput)) *x402.Connection {
	e.t.Helper()
	in := x402pay.ConnectionInput{
		Code: "coinbase_cdp_" + core.NewID[core.X402ConnectionID](e.h.IDs).String()[30:], Name: "Coinbase CDP",
		Networks: networks, PayoutAddresses: payouts,
		Credentials: x402.Credentials{APIKeyID: "test-key-id", APIKeySecret: "test-key-secret"},
	}
	for _, o := range opts {
		o(&in)
	}
	c, err := e.svc.CreateConnection(e.h.Ctx, e.h.OrgID(), in)
	if err != nil {
		e.t.Fatalf("CreateConnection: %v", err)
	}
	return c
}

// sharedConnection is the connection of the shared payment context.
func (e *env) sharedConnection() *x402.Connection {
	return e.connection([]x402.Network{baseSepolia, devnet}, map[x402.Family]string{x402.FamilyEVM: evmPayout, x402.FamilySVM: svmPayout})
}

// errorText renders a service error: the field errors of a validation
// failure ("field: code"), the error text otherwise.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	if fes := core.FieldErrors(err); len(fes) > 0 {
		s := make([]string, len(fes))
		for i, fe := range fes {
			s[i] = fe.String()
		}
		return strings.Join(s, ", ")
	}
	return err.Error()
}

func wantError(t *testing.T, err error, want string) {
	t.Helper()
	if got := errorText(err); got != want {
		t.Errorf("error: got = %q, want = %q", got, want)
	}
}
