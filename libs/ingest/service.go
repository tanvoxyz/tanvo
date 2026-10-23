package ingest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Mode is how accepted events are written.
type Mode string

// Ingestion modes (architecture §7.1).
const (
	// ModePostgres writes events to PostgreSQL synchronously.
	ModePostgres Mode = "postgres"
	// ModeKafka produces events to the events.raw topic.
	ModeKafka Mode = "kafka"
)

// Config configures a [Service].
type Config struct {
	// Catalog resolves metrics (for expressions) and, in ModePostgres,
	// subscriptions and charges. Required.
	Catalog Catalog
	// Programs evaluates metric expressions; a default one when nil.
	Programs *Programs
	// Clock stamps ingestion times; the system clock when nil.
	Clock core.Clock
	// IDs generates event IDs; UUIDv7 when nil.
	IDs core.IDGenerator
	// Mode is the default mode; ModePostgres when empty.
	Mode Mode
	// ModeFor overrides Mode per organization when set.
	ModeFor func(core.OrganizationID) Mode
	// Postgres is the storage of ModePostgres.
	Postgres TxStore
	// Producer is the producer of ModeKafka.
	Producer RawProducer
	// InAdvance bills in-advance events in ModePostgres; optional.
	InAdvance InAdvanceBiller
	// Signals receives usage refresh signals in ModePostgres; optional.
	Signals UsageSignaler
	// Source is the source stamped on events; [SourceAPI] when empty.
	Source string
}

// Service is the event intake. It is safe for concurrent use.
type Service struct {
	cfg      Config
	enricher *Enricher
}

// NewService returns the intake configured by cfg.
func NewService(cfg Config) (*Service, error) {
	if cfg.Catalog == nil {
		return nil, errors.New("ingest: service: nil catalog")
	}
	if cfg.Programs == nil {
		cfg.Programs = NewPrograms(nil)
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.IDs == nil {
		cfg.IDs = core.UUIDv7Generator{}
	}
	if cfg.Mode == "" {
		cfg.Mode = ModePostgres
	}
	if cfg.Source == "" {
		cfg.Source = SourceAPI
	}
	return &Service{cfg: cfg, enricher: NewEnricher(cfg.Catalog, cfg.Programs, cfg.Clock)}, nil
}

// Outcome is the result of one accepted event.
type Outcome struct {
	// Event is the event as accepted: normalized, with the expression's
	// result in its properties.
	Event *event.Event
	// Duplicate reports that the transaction ID was already ingested (or
	// repeated earlier in the batch): the event was dropped.
	Duplicate bool
}

// BatchError reports the invalid events of a batch by position; nothing of
// the batch was stored. It matches [core.ErrInvalidValue].
type BatchError struct {
	// Errors maps positions to their *core.ValidationError.
	Errors map[int]error
}

// Error implements error.
func (e *BatchError) Error() string {
	var b strings.Builder
	b.WriteString("ingest: invalid events:")
	for _, i := range slices.Sorted(maps.Keys(e.Errors)) {
		fmt.Fprintf(&b, " [%d] %v;", i, e.Errors[i])
	}
	return strings.TrimSuffix(b.String(), ";")
}

// Is reports whether target is [core.ErrInvalidValue].
func (e *BatchError) Is(target error) bool { return target == core.ErrInvalidValue }

func (s *Service) mode(org core.OrganizationID) Mode {
	if s.cfg.ModeFor != nil {
		if m := s.cfg.ModeFor(org); m != "" {
			return m
		}
	}
	return s.cfg.Mode
}

// Create ingests one event of org. An invalid event is a
// *core.ValidationError.
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in Input, md Metadata) (Outcome, error) {
	out, err := s.CreateBatch(ctx, org, []Input{in}, md)
	var be *BatchError
	if errors.As(err, &be) {
		return Outcome{}, be.Errors[0]
	}
	if err != nil {
		return Outcome{}, err
	}
	return out[0], nil
}

// CreateBatch ingests up to [event.MaxBatchSize] events of org, all or
// nothing: an empty or oversized batch is a *core.ValidationError on
// "events", invalid events a [*BatchError]. The outcomes are in the order
// of the inputs.
func (s *Service) CreateBatch(ctx context.Context, org core.OrganizationID, ins []Input, md Metadata) ([]Outcome, error) {
	var v core.Validator
	v.Check(len(ins) > 0, "events", core.CodeRequired)
	v.Check(len(ins) <= event.MaxBatchSize, "events", event.CodeTooManyEvents)
	if err := v.Err(); err != nil {
		return nil, err
	}
	now := s.cfg.Clock.Now()
	events := make([]*event.Event, len(ins))
	errs := map[int]error{}
	for i, in := range ins {
		e, err := normalize(org, in, now)
		if err != nil {
			errs[i] = err
			continue
		}
		if err := s.evaluate(ctx, e); err != nil {
			var ve *core.ValidationError
			if !errors.As(err, &ve) {
				return nil, err
			}
			errs[i] = err
			continue
		}
		e.ID = core.NewID[core.EventID](s.cfg.IDs)
		events[i] = e
	}
	if len(errs) > 0 {
		return nil, &BatchError{Errors: errs}
	}
	switch m := s.mode(org); m {
	case ModePostgres:
		return s.writePostgres(ctx, org, events)
	case ModeKafka:
		return s.produce(ctx, events, md)
	default:
		return nil, fmt.Errorf("ingest: unknown mode %q", m)
	}
}

// evaluate applies the metric's expression to e; an expression that cannot
// be evaluated is a field error on properties.
func (s *Service) evaluate(ctx context.Context, e *event.Event) error {
	m, err := s.cfg.Catalog.Metric(ctx, e.OrganizationID, e.Code)
	if err != nil {
		return err
	}
	err = s.cfg.Programs.Apply(ctx, m, e)
	var xe *ExpressionError
	if errors.As(err, &xe) {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "properties", Code: CodeExpressionFailed}}}
	}
	if err != nil {
		return err
	}
	var v core.Validator
	checkProperties(&v, e.Properties)
	return v.Err()
}

func (s *Service) writePostgres(ctx context.Context, org core.OrganizationID, events []*event.Event) ([]Outcome, error) {
	if s.cfg.Postgres == nil {
		return nil, errors.New("ingest: postgres mode without a store")
	}
	enriched := make(map[*event.Event]*Enriched, len(events))
	for _, e := range events {
		en, err := s.enricher.Enrich(ctx, e, s.cfg.Source, false)
		if err != nil {
			return nil, err
		}
		enriched[e] = en
	}
	dup := map[*event.Event]bool{}
	err := s.cfg.Postgres.InTx(ctx, func(ctx context.Context, log event.Log, w EventWriter) error {
		clear(dup)
		accepted, duplicates, err := log.Append(ctx, org, events)
		if err != nil {
			return fmt.Errorf("ingest: append events: %w", err)
		}
		for _, e := range duplicates {
			dup[e] = true
		}
		rows := make([]metering.Event, len(accepted))
		for i, e := range accepted {
			rows[i] = enriched[e].Metering()
		}
		written, err := w.Write(ctx, rows)
		if err != nil {
			return fmt.Errorf("ingest: write events: %w", err)
		}
		for _, i := range written {
			dup[accepted[i]] = true
		}
		var stored []*Enriched
		for _, e := range accepted {
			if !dup[e] {
				stored = append(stored, enriched[e])
			}
		}
		if adv := inAdvance(stored); len(adv) > 0 && s.cfg.InAdvance != nil {
			if err := s.cfg.InAdvance.BillInAdvance(ctx, adv); err != nil {
				return fmt.Errorf("ingest: bill in advance: %w", err)
			}
		}
		if sig := Signals(stored); len(sig) > 0 && s.cfg.Signals != nil {
			if err := s.cfg.Signals.SignalUsage(ctx, sig); err != nil {
				return fmt.Errorf("ingest: signal usage: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Outcome, len(events))
	for i, e := range events {
		out[i] = Outcome{Event: e, Duplicate: dup[e]}
	}
	return out, nil
}

func (s *Service) produce(ctx context.Context, events []*event.Event, md Metadata) ([]Outcome, error) {
	if s.cfg.Producer == nil {
		return nil, errors.New("ingest: kafka mode without a producer")
	}
	out := make([]Outcome, len(events))
	// The repeats of a deduplication key within the batch.
	type key struct{ sub, tx string }
	seen := map[key]bool{}
	msgs := make([]RawMessage, 0, len(events))
	for i, e := range events {
		out[i].Event = e
		k := key{e.ExternalSubscriptionID, e.TransactionID}
		if seen[k] {
			out[i].Duplicate = true
			continue
		}
		seen[k] = true
		msgs = append(msgs, rawMessage(e, s.cfg.Source, md))
	}
	if err := s.cfg.Producer.ProduceRaw(ctx, msgs); err != nil {
		return nil, fmt.Errorf("ingest: produce events: %w", err)
	}
	return out, nil
}
