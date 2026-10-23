// Package store implements the transactional ports of libs/core on
// PostgreSQL 18 with pgx/v5 (architecture §5).
//
// # Layout
//
//   - schema/*.sql is the single source of truth of the database schema,
//     one file per domain, applied in file-name order.
//   - migrations/ holds the versioned migrations Atlas computes from it
//     (internal/cmd/migrate-diff); [Migrate] applies them.
//   - queries/*.sql are compiled by sqlc into internal/db; sqlc and pgx types
//     never leave this package.
//
// # Transactions
//
// [Store.InTx] runs a function in one READ COMMITTED transaction. Isolation
// comes from explicit locks, as domain §2.3 prescribes: [port.Locker] takes
// transaction-scoped advisory locks (pg_advisory_xact_lock) and sequences
// lock their counter row. READ COMMITTED takes a new snapshot per statement,
// so whatever a transaction reads after taking a lock reflects every
// transaction that held the lock before; REPEATABLE READ would freeze the
// snapshot at the first statement (possibly before the lock) and turn lock
// waits into serialization failures.
//
// A failed write does not poison the transaction when the failure is an
// expected one: inserts use ON CONFLICT DO NOTHING and report a conflict
// when no row was written, and updates or deletes that may violate a
// uniqueness or foreign key rule run under a savepoint. Unexpected
// database errors abort the transaction, which then fails as a whole.
//
// # Tenancy and row-level security
//
// Every business table carries organization_id, leads its primary key with
// it, and references other tables of the same organization through
// composite foreign keys (organization_id, id), so no row can point at
// another organization's row. Every query also filters by organization_id.
//
// Every table with organization_id has row-level security enabled with the
// policy tenant_isolation: a row is visible when its organization_id equals
// the transaction setting tanvo.organization_id, or when the setting is "*"
// (the explicitly cross-organization lookups: authentication by token hash,
// sign-in memberships, scheduler scans). With [WithRowLevelSecurity] the
// store sets the setting with set_config(…, true) — transaction-local, like
// SET LOCAL — before the first statement of each organization in a
// transaction (one extra round trip, only when the organization changes).
//
// RLS is enabled but not forced: a table owner bypasses it. Self-hosted
// installations connect as the owner (the role that ran the migrations) and
// leave [WithRowLevelSecurity] off; Tanvo Cloud runs the migrations as the
// owner and serves traffic as a separate role without BYPASSRLS, with the
// option on, as defense in depth.
package store
