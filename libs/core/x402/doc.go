// Package x402 models x402 payments (parity §1.36): stablecoin payments
// by AI agents over HTTP 402. It holds the supported networks and assets,
// the address checks of each chain family (EIP-55 for EVM, base58 keys for
// Solana), the connections of an organization to a facilitator, the
// settlements of verified payments, the payment payload and its digest,
// the decoding of a Solana transaction, and the ports of the facilitator
// and of the chain readers.
package x402
