package x402

import (
	"encoding/hex"
	"regexp"
	"strings"

	"golang.org/x/crypto/sha3"
)

// Family is the chain family of a network or an address.
type Family string

// Family values.
const (
	// FamilyEVM is "evm": the EVM chains (CAIP-2 namespace eip155).
	FamilyEVM Family = "evm"
	// FamilySVM is "svm": Solana (CAIP-2 namespace solana).
	FamilySVM Family = "svm"
)

// Valid reports whether f is a declared family.
func (f Family) Valid() bool { return f == FamilyEVM || f == FamilySVM }

// String returns the stored form of f.
func (f Family) String() string { return string(f) }

// Network is a CAIP-2 chain identifier.
type Network string

// The networks payments settle on.
const (
	// NetworkBase is Base mainnet.
	NetworkBase Network = "eip155:8453"
	// NetworkBaseSepolia is the Base Sepolia testnet.
	NetworkBaseSepolia Network = "eip155:84532"
	// NetworkSolana is Solana mainnet.
	NetworkSolana Network = "solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp"
	// NetworkSolanaDevnet is the Solana devnet.
	NetworkSolanaDevnet Network = "solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"
)

// Environment tells a mainnet from a testnet.
type Environment string

// Environment values.
const (
	// EnvironmentMainnet is a production network.
	EnvironmentMainnet Environment = "mainnet"
	// EnvironmentTestnet is a test network.
	EnvironmentTestnet Environment = "testnet"
)

var environments = map[Network]Environment{
	NetworkBase:         EnvironmentMainnet,
	NetworkBaseSepolia:  EnvironmentTestnet,
	NetworkSolana:       EnvironmentMainnet,
	NetworkSolanaDevnet: EnvironmentTestnet,
}

var families = map[string]Family{"eip155": FamilyEVM, "solana": FamilySVM}

// Networks returns every supported network, in declaration order.
func Networks() []Network {
	return []Network{NetworkBase, NetworkBaseSepolia, NetworkSolana, NetworkSolanaDevnet}
}

// Valid reports whether n is a supported network.
func (n Network) Valid() bool {
	_, ok := environments[n]
	return ok
}

// String returns the CAIP-2 form of n.
func (n Network) String() string { return string(n) }

// Family returns the chain family of n; false for an unsupported network.
//
// source: app/services/x402/network.rb:16-22
func (n Network) Family() (Family, bool) {
	if !n.Valid() {
		return "", false
	}
	ns, _, _ := strings.Cut(string(n), ":")
	return families[ns], true
}

// Environment returns whether n is a mainnet or a testnet; false for an
// unsupported network.
//
// source: app/services/x402/network.rb:55-57
func (n Network) Environment() (Environment, bool) {
	e, ok := environments[n]
	return e, ok
}

// Reference is the chain reference of n, after the namespace: the EVM
// chain ID or the prefix of the Solana genesis hash.
func (n Network) Reference() string {
	_, ref, _ := strings.Cut(string(n), ":")
	return ref
}

// evmAddress matches an EVM address in any letter case.
var evmAddress = regexp.MustCompile(`\A0x[0-9a-fA-F]{40}\z`)

// svmAddressBytes is the size of a Solana public key.
const svmAddressBytes = 32

// IsEVMAddress reports whether s has the shape of an EVM address (0x and
// 40 hexadecimal digits, any case); it does not check the checksum.
func IsEVMAddress(s string) bool { return evmAddress.MatchString(s) }

// FamilyOfAddress returns the family whose address shape s has, or ""
// when it has none.
//
// source: app/services/x402/network.rb:24-30
func FamilyOfAddress(s string) Family {
	if IsEVMAddress(s) {
		return FamilyEVM
	}
	if b, ok := Base58Decode(s); ok && len(b) == svmAddressBytes {
		return FamilySVM
	}
	return ""
}

// Checksum returns the EIP-55 spelling of the EVM address s; false when s
// is not an EVM address.
//
// source: app/services/x402/network.rb:32-40
func Checksum(s string) (string, bool) {
	if !IsEVMAddress(s) {
		return "", false
	}
	lower := strings.ToLower(s[2:])
	digest := hex.EncodeToString(Keccak256([]byte(lower)))
	out := []byte(lower)
	for i, c := range out {
		if c >= 'a' && c <= 'f' && digest[i] >= '8' {
			out[i] = c - 'a' + 'A'
		}
	}
	return "0x" + string(out), true
}

// ValidAddress reports whether s is an address of family f: for EVM,
// all lowercase, all uppercase or correctly checksummed; for Solana, the
// base58 of 32 bytes. An unknown family has no valid address.
//
// source: app/services/x402/network.rb:42-48,59-65
func ValidAddress(s string, f Family) bool {
	switch f {
	case FamilyEVM:
		if !IsEVMAddress(s) {
			return false
		}
		h := s[2:]
		if h == strings.ToLower(h) || h == strings.ToUpper(h) {
			return true
		}
		c, _ := Checksum(s)
		return c == s
	case FamilySVM:
		b, ok := Base58Decode(s)
		return ok && len(b) == svmAddressBytes
	default:
		return false
	}
}

// NormalizeAddress returns the stored spelling of the address s of family
// f: the EIP-55 checksum of a valid EVM address; anything else unchanged,
// for the format check to report.
//
// source: app/services/x402/network.rb:50-53
func NormalizeAddress(s string, f Family) string {
	if f != FamilyEVM || !ValidAddress(s, f) {
		return s
	}
	c, _ := Checksum(s)
	return c
}

// NormalizeAgentAddress normalizes s in the family its shape has; s is
// returned unchanged when it has none.
//
// source: app/models/customer.rb:432-438
func NormalizeAgentAddress(s string) string {
	if f := FamilyOfAddress(s); f != "" {
		return NormalizeAddress(s, f)
	}
	return s
}

// ValidAgentAddress reports whether s is a valid address of the family
// its shape has.
//
// source: app/models/customer.rb:440-446
func ValidAgentAddress(s string) bool {
	f := FamilyOfAddress(s)
	return f != "" && ValidAddress(s, f)
}

// Keccak256 returns the legacy Keccak-256 digest of b (the Ethereum hash,
// not the FIPS 202 SHA3-256).
//
// source: app/services/x402/keccak256.rb
func Keccak256(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}
