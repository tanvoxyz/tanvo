package x402_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

const (
	solanaAddress = "2wKupLR9q6wXYppw8Gr2NvWxKBUqm4PPJKkQfoxHDBg4"
	checksummed   = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"
)

func TestNetworkFamily(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/network_spec.rb:14, 20
	for n, want := range map[x402.Network]x402.Family{"eip155:84532": x402.FamilyEVM, "solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1": x402.FamilySVM} {
		if got, ok := n.Family(); !ok || got != want {
			t.Errorf("%s family: got = %q, %v, want = %q", n, got, ok, want)
		}
	}
	// source: spec/services/x402/network_spec.rb:23-27
	for _, n := range []x402.Network{"eip155:1", "solana:4uhcVJyU9pJkvQyS88uRDiswHXSCkY3z", "sui:mainnet", "base-sepolia", "eip155:", ""} {
		if got, ok := n.Family(); ok {
			t.Errorf("%q family: got = %q, want = unsupported", n, got)
		}
	}
}

func TestFamilyOfAddress(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/network_spec.rb:37-41
	for _, a := range []string{"0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed", "0x5AAEB6053F3E94C9B9A09F33669435E7EF1BEAED", checksummed} {
		if got := x402.FamilyOfAddress(a); got != x402.FamilyEVM {
			t.Errorf("%s: got = %q, want = evm", a, got)
		}
	}
	// source: spec/services/x402/network_spec.rb:48
	if got := x402.FamilyOfAddress(solanaAddress); got != x402.FamilySVM {
		t.Errorf("solana: got = %q, want = svm", got)
	}
	// source: spec/services/x402/network_spec.rb:50-64
	for _, a := range []string{
		"0x5aaeb6053f3e94c9b9a09f33669435e7ef1bea", "5aaeb6053f3e94c9b9a09f33669435e7ef1beaed",
		x402.Base58Encode(bytes.Repeat([]byte{1}, 64)), "2wKupLR9q6wXYppw8Gr2NvWxKBUqm4PPJKkQf0xHDBg4",
		"eip155:8453:0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed", strings.Repeat("z", 44), "",
	} {
		if got := x402.FamilyOfAddress(a); got != "" {
			t.Errorf("%q: got = %q, want = none", a, got)
		}
	}
}

func TestChecksum(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/network_spec.rb:80
	for _, v := range []string{
		"0x52908400098527886E0F7030069857D2E4169EE7", "0x8617E340B3D01FA5F11F306F4090FD50E238070D",
		"0xde709f2102306220921060314715629080e2fb77", "0x27b1fdb04752bbc536007a920d24acb045561c26",
		checksummed, "0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359",
		"0xdbF03B407c01E7cD3CBea99509d93f8DDDC8C6FB", "0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb",
	} {
		if got, ok := x402.Checksum(strings.ToLower(v)); !ok || got != v {
			t.Errorf("checksum: got = %q, want = %q", got, v)
		}
	}
	// source: spec/services/x402/network_spec.rb:85
	if _, ok := x402.Checksum(solanaAddress); ok {
		t.Error("checksum of a Solana address: got = ok, want = not an EVM address")
	}
}

func TestValidAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		source, address string
		family          x402.Family
		want            bool
	}{
		{"spec/services/x402/network_spec.rb:104", "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed", x402.FamilyEVM, true},
		{"spec/services/x402/network_spec.rb:104", "0x5AAEB6053F3E94C9B9A09F33669435E7EF1BEAED", x402.FamilyEVM, true},
		{"spec/services/x402/network_spec.rb:104", checksummed, x402.FamilyEVM, true},
		{"spec/services/x402/network_spec.rb:111", "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeD", x402.FamilyEVM, false},
		{"spec/services/x402/network_spec.rb:117", solanaAddress, x402.FamilyEVM, false},
		{"spec/services/x402/network_spec.rb:127", solanaAddress, x402.FamilySVM, true},
		{"spec/services/x402/network_spec.rb:133", strings.Repeat("z", 44), x402.FamilySVM, false},
		// Adapted: an unknown family has no valid address instead of raising.
		{"spec/services/x402/network_spec.rb:141", solanaAddress, "sui", false},
	}
	for _, tc := range cases {
		if got := x402.ValidAddress(tc.address, tc.family); got != tc.want {
			t.Errorf("%s: got = %v, want = %v", tc.source, got, tc.want)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/network_spec.rb:156
	for _, a := range []string{"0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed", checksummed} {
		if got := x402.NormalizeAddress(a, x402.FamilyEVM); got != checksummed {
			t.Errorf("%s: got = %q, want = %q", a, got, checksummed)
		}
	}
	// source: spec/services/x402/network_spec.rb:163
	if bad := "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeD"; x402.NormalizeAddress(bad, x402.FamilyEVM) != bad {
		t.Error("wrong checksum: got = changed, want = unchanged")
	}
	// source: spec/services/x402/network_spec.rb:172
	if got := x402.NormalizeAddress(solanaAddress, x402.FamilySVM); got != solanaAddress {
		t.Errorf("solana: got = %q, want = verbatim", got)
	}
}

func TestEnvironment(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/network_spec.rb:185
	for n, want := range map[x402.Network]x402.Environment{
		x402.NetworkBase: x402.EnvironmentMainnet, x402.NetworkBaseSepolia: x402.EnvironmentTestnet,
		x402.NetworkSolana: x402.EnvironmentMainnet, x402.NetworkSolanaDevnet: x402.EnvironmentTestnet,
	} {
		if got, ok := n.Environment(); !ok || got != want {
			t.Errorf("%s: got = %q, want = %q", n, got, want)
		}
	}
	// source: spec/services/x402/network_spec.rb:190
	if _, ok := x402.Network("eip155:1").Environment(); ok {
		t.Error("eip155:1: got = an environment, want = unsupported")
	}
}

func TestAsset(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/asset_spec.rb:20
	for n, want := range map[x402.Network]struct {
		address string
		domain  map[string]any
	}{
		x402.NetworkBase:         {"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", map[string]any{"name": "USD Coin", "version": "2"}},
		x402.NetworkBaseSepolia:  {"0x036CbD53842c5426634e7929541eC2318f3dCF7e", map[string]any{"name": "USDC", "version": "2"}},
		x402.NetworkSolana:       {"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", nil},
		x402.NetworkSolanaDevnet: {"4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU", nil},
	} {
		a, ok := x402.LookupAsset(x402.AssetUSDC, n)
		gotDomain, _ := json.Marshal(a.EIP712Domain())
		wantDomain, _ := json.Marshal(want.domain)
		if !ok || a.Address != want.address || a.Decimals != 6 || !bytes.Equal(gotDomain, wantDomain) {
			t.Errorf("%s: got = %+v, want = %s %s", n, a, want.address, wantDomain)
		}
	}
	// source: spec/services/x402/asset_spec.rb:26, 32
	if _, ok := x402.LookupAsset(x402.AssetUSDC, "eip155:1"); ok {
		t.Error("eip155:1: got = an asset, want = none")
	}
	for _, n := range x402.Networks() {
		if _, ok := x402.LookupAsset(x402.AssetUSDC, n); !ok {
			t.Errorf("%s: got = no asset, want = USDC", n)
		}
	}
	a, _ := x402.LookupAsset(x402.AssetUSDC, x402.NetworkBaseSepolia)
	// source: spec/services/x402/asset_spec.rb:38, 45
	if got := a.AtomicUnitsPerCent(); got.Int64() != 10_000 {
		t.Errorf("atomic units per cent: got = %s, want = 10000", got)
	}
	if got := (x402.Asset{Decimals: 7}).AtomicUnitsPerCent(); got.Int64() != 100_000 {
		t.Errorf("7 decimals: got = %s, want = 100000", got)
	}
	// source: spec/services/x402/asset_spec.rb:52, 58
	if got := a.CentsFromAtomic(big.NewInt(10_000_015)); got.Int64() != 1000 {
		t.Errorf("cents from atomic: got = %s, want = 1000", got)
	}
	if n, _ := x402.ParseUnsigned("010000"); a.CentsFromAtomic(n).Int64() != 1 {
		t.Error("leading zero: got != 1 cent")
	}
	// source: spec/services/x402/asset_spec.rb:72
	if got := a.AtomicFromCents(1000); got.Int64() != 10_000_000 {
		t.Errorf("atomic from cents: got = %s, want = 10000000", got)
	}
}

func TestBase58(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/base58_spec.rb:18, 22
	for _, k := range []string{
		"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU",
		"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", "ComputeBudget111111111111111111111111111111",
	} {
		b, ok := x402.Base58Decode(k)
		if !ok || len(b) != 32 || x402.Base58Encode(b) != k {
			t.Errorf("%s: got = %d bytes, round trip %q", k, len(b), x402.Base58Encode(b))
		}
	}
	// source: spec/services/x402/base58_spec.rb:31, 39
	if b, _ := x402.Base58Decode(strings.Repeat("1", 32)); !bytes.Equal(b, make([]byte, 32)) {
		t.Errorf("leading ones: got = %x, want = 32 zero bytes", b)
	}
	if b, _ := x402.Base58Decode(strings.Repeat("z", 44)); len(b) != 33 {
		t.Errorf("44 z: got = %d bytes, want = 33", len(b))
	}
	// source: spec/services/x402/base58_spec.rb:44-48
	for _, s := range []string{"0OIl", "abc0", "abcO", "abcI", "abcl", "eip155:8453", "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed", ""} {
		if b, ok := x402.Base58Decode(s); ok {
			t.Errorf("%q: got = %x, want = none", s, b)
		}
	}
	// source: spec/services/x402/base58_spec.rb:58, 65
	sig := make([]byte, 64)
	_, _ = rand.Read(sig)
	if b, _ := x402.Base58Decode(x402.Base58Encode(sig)); !bytes.Equal(b, sig) {
		t.Error("64-byte signature: got = no round trip")
	}
	if got := x402.Base58Encode(nil); got != "" {
		t.Errorf("no bytes: got = %q, want = empty", got)
	}
}

func TestKeccak256(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/keccak256_spec.rb:21, 28, 35
	for in, want := range map[string]string{
		"":                       "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
		"abc":                    "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45",
		strings.Repeat("a", 135): "34367dc248bbd832f4e3e69dfaac2f92638bd0bbd18f2912ba4ef454919cf446",
		strings.Repeat("a", 136): "a6c4d403279fe3e0af03729caada8374b5ca54d8065329a3ebcaeb4b60aa386e",
		strings.Repeat("a", 137): "d869f639c7046b4929fc92a4d988a8b22c55fbadb802c0c66ebcd484f1915f39",
		strings.Repeat("a", 271): "132f47effd6c8b1b299efa53fe68aece77ec8ae4eb2e294f668eec94f76001e1",
		strings.Repeat("b", 272): "5033eb030ec6c7bed2a3969d11847ddf89ec685b1e0b4ec9601ddba40a3b2ebf",
	} {
		if got := hex.EncodeToString(x402.Keccak256([]byte(in))); got != want {
			t.Errorf("%d bytes: got = %s, want = %s", len(in), got, want)
		}
	}
}

func TestParseUnsigned(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/unsigned_integer_spec.rb:13
	for _, tc := range []struct {
		in   any
		want int64
	}{{1000, 1000}, {0, 0}, {"1000", 1000}, {"0", 0}, {"010", 10}, {json.Number("1000"), 1000}} {
		if got, ok := x402.ParseUnsigned(tc.in); !ok || got.Int64() != tc.want {
			t.Errorf("%#v: got = %v, %v, want = %d", tc.in, got, ok, tc.want)
		}
	}
	// source: spec/services/x402/unsigned_integer_spec.rb:21
	for _, in := range []any{-1, 1.9, "0x10", "1_000", " 7 ", "1.5", "-1", "", nil, []any{}, json.Number("1.5"), json.Number("1e3")} {
		if got, ok := x402.ParseUnsigned(in); ok {
			t.Errorf("%#v: got = %v, want = none", in, got)
		}
	}
	// source: spec/services/x402/asset_spec.rb:62, 66 (strict parsing)
	if _, ok := x402.ParseUnsigned("0x10000"); ok {
		t.Error("0x10000: got = parsed, want = refused")
	}
}

func TestExternalIDs(t *testing.T) {
	t.Parallel()
	// source: spec/services/x402/external_ids_spec.rb:10, 14, 18
	if got, _ := x402.CustomerExternalID(strings.ToLower(checksummed), x402.FamilyEVM); got != "x402_"+checksummed {
		t.Errorf("customer: got = %q", got)
	}
	upper := "0x" + strings.ToUpper(checksummed[2:])
	a, _ := x402.CustomerExternalID(upper, x402.FamilyEVM)
	b, _ := x402.CustomerExternalID(checksummed, x402.FamilyEVM)
	if a != b {
		t.Errorf("spellings: got = %q and %q, want = equal", a, b)
	}
	if got, _ := x402.CustomerExternalID(solanaAddress, x402.FamilySVM); got != "x402_"+solanaAddress {
		t.Errorf("solana: got = %q", got)
	}
	// source: spec/services/x402/external_ids_spec.rb:22, 36
	if _, ok := x402.CustomerExternalID("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeD", x402.FamilyEVM); ok {
		t.Error("invalid address: got = an ID, want = none")
	}
	if _, ok := x402.SubscriptionExternalID("", "agents", x402.FamilySVM); ok {
		t.Error("invalid svm address: got = an ID, want = none")
	}
	// source: spec/services/x402/external_ids_spec.rb:28, 32
	if got, _ := x402.SubscriptionExternalID(strings.ToLower(checksummed), "agents", x402.FamilyEVM); got != "x402_"+checksummed+"_agents" {
		t.Errorf("subscription: got = %q", got)
	}
}

func TestNetworksDeclared(t *testing.T) {
	t.Parallel()
	if !slices.Equal(x402.Networks(), []x402.Network{x402.NetworkBase, x402.NetworkBaseSepolia, x402.NetworkSolana, x402.NetworkSolanaDevnet}) {
		t.Error("networks: got = an unexpected list")
	}
}
