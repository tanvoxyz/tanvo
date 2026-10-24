package x402

import (
	"context"
	"errors"
	"regexp"
	"time"
)

// The chain reader failures.
//
// source: app/services/x402/chain.rb
var (
	// ErrChainUnreachable is a JSON-RPC endpoint that gave no usable
	// answer.
	ErrChainUnreachable = errors.New("x402 chain: unreachable")
	// ErrChainInconclusive is a chain state the reader cannot conclude
	// from.
	ErrChainInconclusive = errors.New("x402 chain: inconclusive")
)

// HexWord matches a 32-byte hexadecimal word: an EVM transaction hash.
//
// source: app/services/x402/chain/evm_reader.rb:19
var HexWord = regexp.MustCompile(`\A0x[0-9a-fA-F]{64}\z`)

// SVMSignatureBytes is the size of a Solana transaction signature.
const SVMSignatureBytes = 64

// SVMExpiryProof is how long after a payment a slot must be dated before
// an invalid blockhash proves the Solana transaction expired; it also
// delays the reconciliation of a pending Solana settlement.
//
// source: app/services/x402/chain/svm_reader.rb:11
const SVMExpiryProof = 120 * time.Second

// ChainReader reads on chain whether a stored payment settled.
type ChainReader interface {
	// Settled reports whether the payment's transfer landed and
	// succeeded.
	Settled(ctx context.Context) (bool, error)
	// Identifier is the landed transfer's hash or signature, once
	// settled.
	Identifier(ctx context.Context) (string, error)
	// Final reports whether the reading can no longer change: the
	// transfer landed, failed, or can no longer land.
	Final(ctx context.Context) (bool, error)
}

// ChainReaders opens the reader of a stored payment on its network; the
// payment was first seen at since.
type ChainReaders interface {
	// For returns the reader of payment on network.
	For(network Network, payment, requirements Object, since time.Time) (ChainReader, error)
}
