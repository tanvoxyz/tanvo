package x402

import "math/big"

// AssetCode is the token a connection accepts.
type AssetCode string

// AssetCode values.
const (
	// AssetUSDC is "usdc".
	AssetUSDC AssetCode = "usdc"
)

// Valid reports whether c is a declared asset.
func (c AssetCode) Valid() bool { return c == AssetUSDC }

// String returns the stored form of c.
func (c AssetCode) String() string { return string(c) }

// Asset is a token on one network.
type Asset struct {
	// Code is the token.
	Code AssetCode
	// Network is where the token lives.
	Network Network
	// Address is the token's contract (EVM) or mint (Solana).
	Address string
	// Decimals is the number of decimals of its atomic unit.
	Decimals int
	// EIP712Name and EIP712Version are the EIP-712 domain of an EVM
	// token; empty on Solana.
	EIP712Name, EIP712Version string
}

type assetKey struct {
	code    AssetCode
	network Network
}

var assets = map[assetKey]Asset{
	{AssetUSDC, NetworkBase}:         {AssetUSDC, NetworkBase, "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", 6, "USD Coin", "2"},
	{AssetUSDC, NetworkBaseSepolia}:  {AssetUSDC, NetworkBaseSepolia, "0x036CbD53842c5426634e7929541eC2318f3dCF7e", 6, "USDC", "2"},
	{AssetUSDC, NetworkSolana}:       {AssetUSDC, NetworkSolana, "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", 6, "", ""},
	{AssetUSDC, NetworkSolanaDevnet}: {AssetUSDC, NetworkSolanaDevnet, "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU", 6, "", ""},
}

// LookupAsset returns the token code on network; false when it is not
// accepted there.
//
// source: app/services/x402/asset.rb:5-14
func LookupAsset(code AssetCode, network Network) (Asset, bool) {
	a, ok := assets[assetKey{code, network}]
	return a, ok
}

// AtomicUnitsPerCent is the number of atomic units in one cent.
func (a Asset) AtomicUnitsPerCent() *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(a.Decimals-2)), nil)
}

// CentsFromAtomic converts atomic units to cents, flooring the sub-cent
// dust; atomic must not be negative.
//
// source: app/services/x402/asset.rb:20-22
func (a Asset) CentsFromAtomic(atomic *big.Int) *big.Int {
	return new(big.Int).Quo(atomic, a.AtomicUnitsPerCent())
}

// AtomicFromCents converts cents to atomic units.
//
// source: app/services/x402/asset.rb:24-26
func (a Asset) AtomicFromCents(cents int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(cents), a.AtomicUnitsPerCent())
}

// EIP712Domain returns the EIP-712 domain of an EVM token ({"name",
// "version"}), or nil on Solana.
//
// source: app/services/x402/asset.rb:28-32
func (a Asset) EIP712Domain() map[string]any {
	if a.EIP712Name == "" {
		return nil
	}
	return map[string]any{"name": a.EIP712Name, "version": a.EIP712Version}
}
