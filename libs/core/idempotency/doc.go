// Package idempotency holds the records of the API's Idempotency-Key
// header (architecture §4.2): the response of a write called with a key is
// kept for [TTL] per organization and key, and a retry of the same request
// with that key gets it back instead of running the write again.
package idempotency
