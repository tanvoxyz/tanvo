// Package app wires the Tanvo server (architecture §13): it opens the
// PostgreSQL pool, checks or applies the schemas, builds the store, the
// job client, the event stores, ingestion, webhooks and mail, and runs the
// components of the configured roles (api, worker, consumer) until the
// context ends, then shuts them down in order.
//
// Each component's wiring lives in its own file, so that a change to one
// library touches one file: postgres.go, migrate.go, jobs.go,
// eventstore.go, ingest.go, kafka.go, webhooks.go, mail.go, api.go and
// health.go. Only this package (with internal/telemetry) builds concrete
// implementations; the libraries receive them through their constructors.
package app
