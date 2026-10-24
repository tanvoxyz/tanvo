// Package x402pay runs x402 payments (parity §1.36): the gate check a
// resource server asks before an agent's call, the payment requirements
// of a top-up, and the verification and settlement of a payment through
// the connection's facilitator. The facilitator is a port
// ([x402.Facilitators]); its credentials are sealed per organization.
package x402pay
