// Package storetest runs the billing engine's concurrency tests on the
// PostgreSQL store, where advisory locks block for real (the memstore's
// are no-ops). The tests are skipped when no Docker engine or
// TANVO_TEST_POSTGRES_DSN server is reachable.
package storetest
