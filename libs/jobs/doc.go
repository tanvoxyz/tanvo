// Package jobs runs Tanvo's background jobs on River (architecture §9).
//
// A [Registry] maps job kinds to typed [Handler]s and their retry
// policies. A [Client] built on a pgx pool and a sealed registry inserts
// jobs and, when given queues to work, runs their handlers and the
// periodic jobs of [Schedule] on River's elected leader.
//
// Enqueuing is transactional: [Client.InsertTx] inserts [port.Job]s with
// the caller's pgx.Tx, so a job exists exactly when the transaction that
// requires it commits. libs/store calls it through an inserter hook wired
// by apps/server; this package does not import libs/store, nor libs/store
// this package.
//
// Tenant fairness: each queue is split into shards picked by a hash of the
// job's organization, each shard with its own bounded concurrency, and a
// per-organization token bucket at enqueue time delays the jobs of an
// organization that exceeds its rate instead of letting them fill the
// queue ahead of other tenants.
//
// River keeps its tables in their own PostgreSQL schema (by default
// "river"), migrated by [Migrate] with rivermigrate.
package jobs
