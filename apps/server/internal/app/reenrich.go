package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// Re-enrichment after catalog changes (parity §1.17): `tanvo events
// reprocess` checks an organization's subscriptions and enqueues the
// re-enrichment jobs ([ReprocessEvents]); the workers run them on the
// event store of the organization's mode ([jobHandlers.registerReEnrichment]).

// reEnrichKind is the job kind of the re-enrichment of a subscription
// (lago PreEnrichmentCheckJob, queue low_priority, default retries).
var reEnrichKind = jobs.Kind{Name: ingest.JobReEnrichSubscription}

// reEnricher returns the re-enricher over the store tx and the event
// stores s.
func reEnricher(tx port.Transactor, s *EventStores) (*ingest.ReEnricher, error) {
	rc := ingest.ReaderConfig{Transactor: tx, ModeFor: s.modeOf}
	if s.ch != nil {
		rc.Raw, rc.Enriched = s.ch, s.ch
	}
	reader, err := ingest.NewReader(rc)
	if err != nil {
		return nil, err
	}
	return ingest.NewReEnricher(ingest.ReEnrichConfig{
		Transactor: tx,
		Enricher:   ingest.NewEnricher(ingest.RepoCatalog{Transactor: tx}, nil, nil),
		Events:     reader,
		Replacer: func(org core.OrganizationID) (ingest.EventReplacer, error) {
			if s.modeOf(org) == ingest.ModeKafka {
				if s.ch == nil {
					return nil, errors.New("eventstore: kafka mode without clickhouse")
				}
				return s.ch, nil
			}
			return s.pg, nil
		},
	})
}

// registerReEnrichment registers the re-enrichment job of a subscription.
func (h jobHandlers) registerReEnrichment(reg *jobs.Registry) error {
	if h.stores == nil {
		return nil
	}
	r, err := reEnricher(h.tx, h.stores)
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}
	return jobs.Register(reg, reEnrichKind, jobs.HandlerFunc[ingest.ReEnrichArgs](func(ctx context.Context, j *jobs.Job[ingest.ReEnrichArgs]) error {
		res, err := r.ReEnrichSubscription(ctx, j.OrganizationID, j.Args)
		if err != nil {
			return err
		}
		h.log.InfoContext(ctx, "ingest: re-enriched subscription events", "subscription_id", j.Args.SubscriptionID.String(),
			"events", res.Events, "batches", res.Batches)
		return nil
	}))
}

// ReprocessInput are the options of [ReprocessEvents] (lago
// events:reprocess's variables).
type ReprocessInput struct {
	// OrganizationID is the organization checked.
	OrganizationID string
	// Reprocess enqueues the re-enrichment jobs; a dry run otherwise.
	Reprocess bool
	// BatchSize and Sleep are passed to the jobs.
	BatchSize int
	Sleep     time.Duration
}

// ReprocessEvents finds the subscriptions of in.OrganizationID whose
// events may need re-enrichment and, with in.Reprocess, enqueues their
// re-enrichment for the workers, in one transaction.
//
// source: lib/tasks/events.rake:27-54
func ReprocessEvents(ctx context.Context, cfg config.Config, in ReprocessInput, tp trace.TracerProvider, mp metric.MeterProvider) ([]ingest.Candidate, error) {
	org, err := core.ParseID[core.OrganizationID](in.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("events reprocess: organization: %w", err)
	}
	pool, err := openPool(ctx, cfg.DatabaseURL, poolOptions{MaxConns: 2, Name: "events-reprocess"}, tp, mp)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	if err := store.CheckSchema(ctx, pool); err != nil {
		return nil, fmt.Errorf("events reprocess: %w", err)
	}
	// An insert-only job client: the workers run the jobs.
	reg := jobs.NewRegistry()
	if err := jobs.Register(reg, reEnrichKind, jobs.HandlerFunc[ingest.ReEnrichArgs](func(context.Context, *jobs.Job[ingest.ReEnrichArgs]) error {
		return errors.New("events reprocess: the workers run the re-enrichment")
	})); err != nil {
		return nil, err
	}
	jc, err := jobs.New(pool, reg, jobs.Config{Schema: cfg.JobsSchema})
	if err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}
	opts := []store.Option{store.WithJobInserter(jobInserter(func() *jobs.Client { return jc }))}
	if cfg.DatabaseRowLevelSecurity {
		opts = append(opts, store.WithRowLevelSecurity())
	}
	st := store.New(pool, opts...)
	err = st.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.Organizations.Get(ctx, org); err != nil {
			return fmt.Errorf("organization %s: %w", org, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("events reprocess: %w", err)
	}
	r, err := ingest.NewReEnricher(ingest.ReEnrichConfig{Transactor: st})
	if err != nil {
		return nil, err
	}
	found, err := r.Check(ctx, org, ingest.CheckOptions{Reprocess: in.Reprocess, BatchSize: in.BatchSize, Sleep: in.Sleep})
	if err != nil {
		return nil, fmt.Errorf("events reprocess: %w", err)
	}
	return found, nil
}
