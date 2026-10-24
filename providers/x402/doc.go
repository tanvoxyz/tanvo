// Package x402 is the HTTP adapter of the x402 ports of libs/core/x402
// (parity §1.36): the Coinbase Developer Platform facilitator behind
// [x402.Facilitators], and the JSON-RPC chain readers behind
// [x402.ChainReaders].
//
// # Facilitator
//
// [Facilitators] opens a Coinbase CDP facilitator for a connection's
// opened credentials. Each call (POST /verify, POST /settle, GET
// /supported under https://api.cdp.coinbase.com/platform/v2/x402) is
// authenticated by a fresh two-minute JWT bound to the method, host and
// path, signed with the API key secret: a base64 Ed25519 key (CDP's
// default) as EdDSA, or an EC P-256 key in PEM as ES256. Calls are never
// retried. Answers are classified as the facilitator port documents:
// 401, 402 and 403 are credential errors, 429 a rate-limit error, except
// a 403 kyt_risk_detected screening decline, which is a verdict;
// a settle without an answer is the no_response outcome, a verify or
// supported without one is an unavailable error.
//
// # Chain readers
//
// [ChainReaders] reads on chain whether a stored payment settled. The
// EVM reader asks the asset contract's authorizationState at the safe
// block, then finds the AuthorizationUsed or AuthorizationCanceled event
// of the nonce in a bounded block window and checks the transfer that
// follows it in the receipt. The Solana reader checks the stored
// transaction's blockhash, then pages through the buyer's finalized
// signatures around the payment for a transaction carrying the buyer's
// signature. JSON-RPC calls retry a transient failure twice. Each
// network's endpoint defaults to the public one and can be configured
// with [WithRPCURLs], usually from [ParseRPCURLs].
//
// # Telemetry
//
// HTTP calls go through an otelhttp transport over the injected tracer
// and meter providers; facilitator calls also open a client span.
//
// # Differences from lago
//
//   - A transport failure or an unusable JSON-RPC answer is described by
//     a fixed phrase (timeout, connection refused, invalid JSON, …), not
//     an exception class name; the endpoint URL, which may carry an API
//     key, is never in an error.
//   - [ParseRPCURLs] validates the configured endpoints once, so a
//     malformed configuration stops the server at startup rather than
//     failing every chain read.
//   - The call timeouts are total deadlines per attempt: the 5-second
//     connect allowance plus the 10-second (20-second for a settle) read
//     allowance.
//   - A settle answer whose success carries a transaction that is not a
//     string is a malformed_response server error; the port's
//     transaction is a string.
//   - A payment body holding a string that is not valid UTF-8 is refused
//     before any request, as serialising it would be.
//   - The authorization's validAfter and validBefore are saturated at
//     2^62 seconds, which only affects payments no chain could settle.
package x402
