package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Re-enrichment is the operator path that enriches stored events again
// after a catalog change (lago lib/tasks/events.rake `events:reprocess`):
// [ReEnricher.Check] finds the subscriptions whose events may have been
// enriched under another definition of their metric and, when asked,
// enqueues one [JobReEnrichSubscription] job per subscription, which
// [ReEnricher.ReEnrichSubscription] runs.
//
// Enriched rows carry no charge, filter, subscription or grouping column
// (architecture §7.2): what enrichment stores from the catalog is the
// value of the event's metric (its aggregation and field name). A metric
// cannot change while a plan charges it, so a subscription's events may
// carry a stale value only when a charge of its plan was added after it
// started, the third check of lago's PreEnrichmentCheckService.

// JobReEnrichSubscription is the job kind re-enriching the events of one
// subscription (lago Events::Stores::Clickhouse::PreEnrichmentCheckJob);
// its arguments are [ReEnrichArgs].
const JobReEnrichSubscription = "events.re_enrich_subscription"

// Defaults of re-enrichment (lago lib/tasks/events.rake:32-33).
const (
	// DefaultReEnrichBatchSize is how many events one batch reads and
	// stores; at most core.MaxPageSize.
	DefaultReEnrichBatchSize = 1000
	// DefaultReEnrichSleep is the pause after each batch.
	DefaultReEnrichSleep = 500 * time.Millisecond
)

// EventReplacer stores events as the latest copy of their deduplication
// key; the backends of libs/eventstore implement it
// (eventstore.Replacer has the same method).
type EventReplacer interface {
	Replace(ctx context.Context, events []metering.Event) error
}

// ReEnrichConfig configures a [ReEnricher].
type ReEnrichConfig struct {
	// Transactor reads the catalog and the subscriptions and enqueues the
	// jobs.
	Transactor port.Transactor
	// Enricher enriches the events again.
	Enricher *Enricher
	// Events reads the stored events of each organization's mode: the raw
	// event log in [ModePostgres], the ClickHouse ledger in [ModeKafka].
	Events *Reader
	// Replacer returns the event store of the organization; nil when
	// only [ReEnricher.Check] is used.
	Replacer func(core.OrganizationID) (EventReplacer, error)
	// Sleep pauses for d unless ctx ends; nil sleeps on a timer (a test
	// hook).
	Sleep func(ctx context.Context, d time.Duration) error
}

// ReEnricher detects and re-enriches the events of subscriptions.
type ReEnricher struct {
	cfg ReEnrichConfig
}

// NewReEnricher returns a re-enricher.
func NewReEnricher(cfg ReEnrichConfig) (*ReEnricher, error) {
	if cfg.Transactor == nil {
		return nil, errors.New("ingest: re-enrich: missing transactor")
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	return &ReEnricher{cfg: cfg}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ReEnrichArgs are the arguments of a [JobReEnrichSubscription] job.
type ReEnrichArgs struct {
	// SubscriptionID is the subscription whose events are re-enriched.
	SubscriptionID core.SubscriptionID `json:"subscription_id"`
	// Codes keeps the events of these metric codes; every code when
	// empty.
	Codes []string `json:"codes,omitempty"`
	// BatchSize is how many events a batch reads and stores, in 1 to
	// core.MaxPageSize.
	BatchSize int `json:"batch_size"`
	// Sleep is the pause after each batch.
	Sleep time.Duration `json:"sleep"`
}

func (a ReEnrichArgs) validate() error {
	if a.BatchSize < 1 || a.BatchSize > core.MaxPageSize {
		return fmt.Errorf("ingest: re-enrich: batch size %d not in 1..%d: %w", a.BatchSize, core.MaxPageSize, core.ErrInvalidValue)
	}
	if a.Sleep < 0 {
		return fmt.Errorf("ingest: re-enrich: negative sleep: %w", core.ErrInvalidValue)
	}
	return nil
}

// CheckOptions are the options of [ReEnricher.Check].
type CheckOptions struct {
	// Reprocess enqueues the re-enrichment of the subscriptions found; a
	// dry run otherwise.
	Reprocess bool
	// BatchSize and Sleep are passed to the jobs ([ReEnrichArgs]).
	BatchSize int
	Sleep     time.Duration
}

// Candidate is a subscription whose events may need re-enrichment, with
// the metric codes concerned, sorted.
type Candidate struct {
	SubscriptionID core.SubscriptionID
	Codes          []string
}

// Check returns the active subscriptions of org with a charge (or a kept
// filter of a charge) created after the subscription started, with the
// codes of those charges' metrics, by subscription ID; with
// opts.Reprocess it enqueues one [JobReEnrichSubscription] job for each,
// in the same transaction (lago PreEnrichmentCheckService).
//
// source: app/services/events/stores/clickhouse/pre_enrichment_check_service.rb:23-34, 61-76, 107-111
func (r *ReEnricher) Check(ctx context.Context, org core.OrganizationID, opts CheckOptions) ([]Candidate, error) {
	if opts.Reprocess {
		if err := (ReEnrichArgs{BatchSize: opts.BatchSize, Sleep: opts.Sleep}).validate(); err != nil {
			return nil, err
		}
	}
	var out []Candidate
	err := r.cfg.Transactor.InTx(ctx, func(ctx context.Context, rs port.Repos) error {
		var err error
		if out, err = candidates(ctx, rs, org); err != nil || !opts.Reprocess || len(out) == 0 {
			return err
		}
		jobs := make([]port.Job, len(out))
		for i, c := range out {
			args, err := json.Marshal(ReEnrichArgs{SubscriptionID: c.SubscriptionID, Codes: c.Codes, BatchSize: opts.BatchSize, Sleep: opts.Sleep})
			if err != nil {
				return err
			}
			jobs[i] = port.Job{Kind: JobReEnrichSubscription, Queue: port.QueueDefault, OrganizationID: org, Args: args}
		}
		return rs.Jobs.Enqueue(ctx, jobs...)
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: re-enrich check: %w", err)
	}
	return out, nil
}

// candidates lists the subscriptions of [ReEnricher.Check].
func candidates(ctx context.Context, rs port.Repos, org core.OrganizationID) ([]Candidate, error) {
	charges := map[core.PlanID][]*catalog.Charge{}
	codes := map[core.BillableMetricID]string{}
	code := func(id core.BillableMetricID) (string, error) {
		if c, ok := codes[id]; ok {
			return c, nil
		}
		m, err := rs.Metrics.Get(ctx, org, id)
		switch {
		case errors.Is(err, core.ErrNotFound):
			// A discarded metric is left out, as lago's join does.
		case err != nil:
			return "", err
		default:
			codes[id] = m.Code
		}
		return codes[id], nil
	}
	var out []Candidate
	page := core.PageRequest{Size: core.MaxPageSize}
	for {
		p, err := rs.Subscriptions.List(ctx, org, subscription.Filter{Statuses: []subscription.Status{subscription.StatusActive}}, page)
		if err != nil {
			return nil, err
		}
		for _, s := range p.Items {
			if s.StartedAt == nil {
				continue
			}
			cs, ok := charges[s.PlanID]
			if !ok {
				if cs, err = rs.Charges.ListByPlan(ctx, org, s.PlanID); err != nil {
					return nil, err
				}
				charges[s.PlanID] = cs
			}
			set := map[string]bool{}
			for _, c := range cs {
				if !addedAfter(c, *s.StartedAt) {
					continue
				}
				m, err := code(c.BillableMetricID)
				if err != nil {
					return nil, err
				}
				if m != "" {
					set[m] = true
				}
			}
			if len(set) > 0 {
				out = append(out, Candidate{SubscriptionID: s.ID, Codes: slices.Sorted(maps.Keys(set))})
			}
		}
		if p.NextToken == "" {
			break
		}
		page.Token = p.NextToken
	}
	slices.SortFunc(out, func(a, b Candidate) int { return a.SubscriptionID.Compare(b.SubscriptionID) })
	return out, nil
}

// addedAfter reports whether c, or a kept filter of c, was created after
// t: the filters when c has kept ones, c itself otherwise (lago's
// COALESCE(charge_filters.created_at, charges.created_at) over a left
// join of the kept filters).
//
// source: app/services/events/stores/clickhouse/pre_enrichment_check_service.rb:61-69
func addedAfter(c *catalog.Charge, t time.Time) bool {
	kept := false
	for _, f := range c.Filters {
		if f.DeletedAt != nil {
			continue
		}
		kept = true
		if f.CreatedAt.After(t) {
			return true
		}
	}
	return !kept && c.CreatedAt.After(t)
}

// ReEnrichResult counts the work of [ReEnricher.ReEnrichSubscription].
type ReEnrichResult struct {
	// Events is the number of events re-enriched.
	Events int
	// Batches is the number of batches stored.
	Batches int
}

// ReEnrichSubscription enriches again, against the current catalog, the
// stored events of the subscription's external ID with a timestamp from
// its start (to its termination when it is terminated), of args.Codes
// when set, and stores them back as the current copy of their key, batch
// after batch with a pause after each (lago
// ReEnrichSubscriptionEventsService, run by PreEnrichmentCheckJob). Each
// key is read once, in its current copy. The events keep their ingestion
// instant and properties: their expressions are not evaluated again, and
// they are neither billed in advance nor signalled for usage refresh, as
// lago marks them api_post_processed. A missing or never started
// subscription has no events to re-enrich.
//
// source: app/services/events/stores/clickhouse/re_enrich_subscription_events_service.rb:18-60
// source: app/jobs/events/stores/clickhouse/pre_enrichment_check_job.rb:9-15
func (r *ReEnricher) ReEnrichSubscription(ctx context.Context, org core.OrganizationID, args ReEnrichArgs) (ReEnrichResult, error) {
	var res ReEnrichResult
	if err := args.validate(); err != nil {
		return res, err
	}
	if r.cfg.Enricher == nil || r.cfg.Events == nil || r.cfg.Replacer == nil {
		return res, errors.New("ingest: re-enrich: missing enricher, event reader or replacer")
	}
	var sub *subscription.Subscription
	err := r.cfg.Transactor.InTx(ctx, func(ctx context.Context, rs port.Repos) error {
		var err error
		sub, err = rs.Subscriptions.Get(ctx, org, args.SubscriptionID)
		return err
	})
	switch {
	case errors.Is(err, core.ErrNotFound):
		return res, nil
	case err != nil:
		return res, fmt.Errorf("ingest: re-enrich %s: %w", args.SubscriptionID, err)
	case sub.StartedAt == nil:
		return res, nil
	}
	replacer, err := r.cfg.Replacer(org)
	if err != nil {
		return res, fmt.Errorf("ingest: re-enrich %s: %w", args.SubscriptionID, err)
	}
	f := event.ListFilter{ExternalSubscriptionID: sub.ExternalID, TimestampFrom: sub.StartedAt}
	if sub.Status == subscription.StatusTerminated {
		f.TimestampTo = sub.TerminatedAt
	}
	codes := slices.Compact(slices.Sorted(slices.Values(args.Codes)))
	if len(codes) == 0 {
		codes = []string{""}
	}
	for _, c := range codes {
		f.Code = c
		page := core.PageRequest{Size: args.BatchSize}
		for {
			p, err := r.cfg.Events.List(ctx, org, ListInput{ListFilter: f}, page)
			if err != nil {
				return res, fmt.Errorf("ingest: re-enrich %s: %w", args.SubscriptionID, err)
			}
			if len(p.Items) == 0 {
				break
			}
			batch := make([]metering.Event, len(p.Items))
			for i, e := range p.Items {
				en, err := r.cfg.Enricher.Enrich(ctx, e, SourceAPI, false)
				if err != nil {
					return res, fmt.Errorf("ingest: re-enrich %s: %w", args.SubscriptionID, err)
				}
				batch[i] = en.Metering()
			}
			if err := replacer.Replace(ctx, batch); err != nil {
				return res, fmt.Errorf("ingest: re-enrich %s: %w", args.SubscriptionID, err)
			}
			res.Events += len(batch)
			res.Batches++
			if err := r.cfg.Sleep(ctx, args.Sleep); err != nil {
				return res, err
			}
			if p.NextToken == "" {
				break
			}
			page.Token = p.NextToken
		}
	}
	return res, nil
}
