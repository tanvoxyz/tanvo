// Package memstore is an in-memory implementation of every repository of
// libs/core and of the transactional ports of package port, for the tests
// of libs/billing and the services.
//
// A [Store] serializes transactions with a mutex. Each transaction works
// on a copy-on-write snapshot of all tables, which replaces the committed
// state when the function returns nil and is discarded otherwise, so a
// failed transaction leaves no trace. Rows are copied on every read and
// write, so callers never share memory with the store. The uniqueness and
// organization scoping rules of the repository contracts are enforced.
package memstore
