// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Package store reads the core's export views and maintains the analytics
// schema (architecture §14.1): the MRR intervals of each subscription, the
// recognition schedules of each fee, and a catalog of the plans seen.
//
// The analytics schema holds derived data only. An organization is built
// on its first report and rebuilt, in one transaction, when the core data
// it was built from changes (Store.Refresh); Store.Rebuild rebuilds it on
// demand. Every transaction sets tanvo.organization_id, so that the
// row-level security of the core's tables applies to the views.
package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Store is the analytics store.
type Store struct {
	primary *pgxpool.Pool
	replica *pgxpool.Pool
	now     func() time.Time
	log     *slog.Logger
	tracer  trace.Tracer
}

// Options configures [New].
type Options struct {
	// Replica, when set, serves the reports; the primary serves them
	// otherwise. Rebuilds always write to the primary.
	Replica *pgxpool.Pool
	// Now is the clock; time.Now by default.
	Now func() time.Time
	// Log is the logger; slog.Default() by default.
	Log *slog.Logger
	// Tracer traces rebuilds and reports; a no-op tracer by default.
	Tracer trace.Tracer
}

// New returns the store over the core database's primary.
func New(primary *pgxpool.Pool, o Options) *Store {
	s := &Store{primary: primary, replica: o.Replica, now: o.Now, log: o.Log, tracer: o.Tracer}
	if s.replica == nil {
		s.replica = primary
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.tracer == nil {
		s.tracer = noop.NewTracerProvider().Tracer("")
	}
	return s
}

// inOrg runs fn in a transaction of pool acting in org.
func inOrg(ctx context.Context, pool *pgxpool.Pool, org string, opts pgx.TxOptions, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, pool, opts, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('tanvo.organization_id', $1, true)", org); err != nil {
			return fmt.Errorf("store: organization: %w", err)
		}
		return fn(tx)
	})
}

// readOnly returns the options of report transactions: one snapshot.
func readOnly() pgx.TxOptions {
	return pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
}

// read runs a report in org, building org first when it never was. A
// report right after a build reads the primary, which the replica may
// not have caught up with.
func (s *Store) read(ctx context.Context, org string, fn func(pgx.Tx) error) error {
	built, err := s.built(ctx, s.replica, org)
	if err != nil {
		return err
	}
	pool := s.replica
	if !built {
		if err := s.Rebuild(ctx, org); err != nil {
			return err
		}
		pool = s.primary
	}
	return inOrg(ctx, pool, org, readOnly(), fn)
}

// built reports whether org was built.
func (s *Store) built(ctx context.Context, pool *pgxpool.Pool, org string) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM analytics.refresh_state WHERE organization_id = $1)", org).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: refresh state: %w", err)
	}
	return ok, nil
}

// watermarkSQL is the latest change of the core data an organization's
// analytics are built from.
const watermarkSQL = `SELECT GREATEST(
  (SELECT max(updated_at) FROM exports_invoices WHERE organization_id = $1),
  (SELECT max(updated_at) FROM exports_fees WHERE organization_id = $1),
  (SELECT max(updated_at) FROM exports_subscriptions WHERE organization_id = $1),
  (SELECT max(updated_at) FROM exports_customers WHERE organization_id = $1),
  (SELECT max(updated_at) FROM exports_plans WHERE organization_id = $1),
  (SELECT max(updated_at) FROM exports_record_deletions WHERE organization_id = $1),
  'epoch'::timestamptz)`

// plansGoneSQL reports plans of the catalog gone from the views: deleting
// a plan removes it from exports_plans, which moves no watermark.
const plansGoneSQL = `SELECT EXISTS (SELECT 1 FROM analytics.plans p WHERE p.organization_id = $1 AND NOT p.deleted
  AND NOT EXISTS (SELECT 1 FROM exports_plans e WHERE e.organization_id = $1 AND e.id = p.id))`

// Refresh rebuilds the organizations built before whose core data
// changed since, or whose plans were deleted since. It logs the organizations it fails to rebuild and
// returns the first error.
func (s *Store) Refresh(ctx context.Context) error {
	type state struct {
		org       string
		watermark time.Time
	}
	var states []state
	if err := inOrg(ctx, s.primary, "*", pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT organization_id::text, watermark FROM analytics.refresh_state ORDER BY refreshed_at")
		if err != nil {
			return err
		}
		states, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (state, error) {
			var st state
			return st, r.Scan(&st.org, &st.watermark)
		})
		return err
	}); err != nil {
		return fmt.Errorf("store: refresh: %w", err)
	}
	var first error
	for _, st := range states {
		var wm time.Time
		var plansGone bool
		if err := inOrg(ctx, s.primary, st.org, readOnly(), func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, watermarkSQL, st.org).Scan(&wm); err != nil {
				return err
			}
			return tx.QueryRow(ctx, plansGoneSQL, st.org).Scan(&plansGone)
		}); err != nil {
			first = cmpErr(first, fmt.Errorf("store: refresh %s: %w", st.org, err))
			continue
		}
		if !wm.After(st.watermark) && !plansGone {
			continue
		}
		if err := s.Rebuild(ctx, st.org); err != nil {
			s.log.ErrorContext(ctx, "analytics rebuild failed", "organization_id", st.org, "error", err)
			first = cmpErr(first, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return first
}

func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

// Rebuild recomputes org's analytics from the core data, in one
// transaction holding the organization's advisory lock.
func (s *Store) Rebuild(ctx context.Context, org string) (err error) {
	ctx, span := s.tracer.Start(ctx, "analytics.rebuild", trace.WithAttributes(attribute.String("tanvo.organization_id", org)))
	defer func() {
		if err != nil {
			span.RecordError(err)
		}
		span.End()
	}()
	start := s.now()
	err = inOrg(ctx, s.primary, org, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('tanvo.analytics.' || $1, 0))", org); err != nil {
			return fmt.Errorf("lock: %w", err)
		}
		var wm time.Time
		if err := tx.QueryRow(ctx, watermarkSQL, org).Scan(&wm); err != nil {
			return fmt.Errorf("watermark: %w", err)
		}
		if err := rebuildPlans(ctx, tx, org); err != nil {
			return err
		}
		if err := rebuildMRR(ctx, tx, org); err != nil {
			return err
		}
		if err := rebuildRecognition(ctx, tx, org); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO analytics.refresh_state (organization_id, watermark, refreshed_at) VALUES ($1, $2, $3)
			ON CONFLICT (organization_id) DO UPDATE SET watermark = EXCLUDED.watermark, refreshed_at = EXCLUDED.refreshed_at`,
			org, wm, s.now())
		return err
	})
	if err != nil {
		return fmt.Errorf("store: rebuild %s: %w", org, err)
	}
	s.log.InfoContext(ctx, "analytics rebuilt", "organization_id", org, "duration", s.now().Sub(start))
	return nil
}
