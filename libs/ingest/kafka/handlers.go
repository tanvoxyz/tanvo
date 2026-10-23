package kafka

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// DefaultConcurrency bounds the events enriched at once.
const DefaultConcurrency = 16

// Enrichment returns the handler of the raw events topic: it enriches
// every record and produces the enriched events to out. Undecodable
// records and expressions that cannot be evaluated are dead; catalog and
// produce failures are retried. concurrency bounds the events enriched at
// once (DefaultConcurrency when not positive).
func Enrichment(en *ingest.Enricher, out EnrichedSink, concurrency int) Handler {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	return func(ctx context.Context, recs []*kgo.Record) []Result {
		results := make([]Result, len(recs))
		enriched := make([]*ingest.Enriched, len(recs))
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		for i, r := range recs {
			m, e, err := ingest.DecodeRaw(r.Value)
			if err != nil {
				results[i] = Result{Disposition: Dead, Code: ingest.ErrorDecode, Err: err}
				continue
			}
			sem <- struct{}{}
			wg.Go(func() {
				defer func() { <-sem }()
				x, err := en.Enrich(ctx, e, m.Source, !m.ExpressionEvaluated)
				results[i].IngestedAt = m.IngestedAt
				var xe *ingest.ExpressionError
				switch {
				case errors.As(err, &xe):
					results[i].Disposition, results[i].Code, results[i].Err = Dead, ingest.ErrorExpression, err
				case err != nil:
					results[i].Disposition, results[i].Code, results[i].Err = Retry, ingest.ErrorCatalog, err
				default:
					enriched[i] = x
				}
			})
		}
		wg.Wait()
		var batch []*ingest.Enriched
		var at []int
		for i, e := range enriched {
			if e != nil {
				batch = append(batch, e)
				at = append(at, i)
			}
		}
		if err := out.ProduceEnriched(ctx, batch); err != nil {
			for _, i := range at {
				results[i].Disposition, results[i].Code, results[i].Err = Retry, ingest.ErrorProduce, err
			}
		}
		return results
	}
}

// decodeEnriched decodes the records of the enriched topic, marking the
// undecodable ones dead.
func decodeEnriched(recs []*kgo.Record, results []Result) ([]*ingest.Enriched, []int) {
	var events []*ingest.Enriched
	var at []int
	for i, r := range recs {
		e, err := ingest.DecodeEnriched(r.Value)
		if err != nil {
			results[i] = Result{Disposition: Dead, Code: ingest.ErrorDecode, Err: err}
			continue
		}
		results[i].IngestedAt = e.IngestedAt
		events = append(events, e)
		at = append(at, i)
	}
	return events, at
}

// Writer returns the handler of the enriched events topic that stores
// events: each polled batch is one [ingest.EventWriter.Write], which drops
// the duplicates of stored events (by organization, external subscription
// ID and transaction ID). After a successful write, the
// batch's usage refresh signals go to signals (when not nil); a failed
// signal is logged, not retried, since the periodic sweep stays
// authoritative. A failed write retries the batch.
func Writer(w ingest.EventWriter, signals ingest.UsageSignaler, logger *slog.Logger) Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, recs []*kgo.Record) []Result {
		results := make([]Result, len(recs))
		events, at := decodeEnriched(recs, results)
		if len(events) == 0 {
			return results
		}
		rows := make([]metering.Event, len(events))
		for i, e := range events {
			rows[i] = e.Metering()
		}
		if _, err := w.Write(ctx, rows); err != nil {
			for _, i := range at {
				results[i].Disposition, results[i].Code, results[i].Err = Retry, ingest.ErrorWrite, err
			}
			return results
		}
		if signals != nil {
			if sig := ingest.Signals(events); len(sig) > 0 {
				if err := signals.SignalUsage(ctx, sig); err != nil {
					logger.WarnContext(ctx, "kafka: signal usage", "error", err)
				}
			}
		}
		return results
	}
}

// InAdvance returns the handler of the enriched events topic that routes
// in-advance events to billing; it runs in its own consumer group. A
// failed billing retries the batch's in-advance events; the biller is
// idempotent on (organization, external subscription ID, transaction ID).
func InAdvance(b ingest.InAdvanceBiller) Handler {
	return func(ctx context.Context, recs []*kgo.Record) []Result {
		results := make([]Result, len(recs))
		events, at := decodeEnriched(recs, results)
		var batch []*ingest.Enriched
		var billed []int
		for j, e := range events {
			if e.InAdvance && !e.SubscriptionID.IsZero() {
				batch = append(batch, e)
				billed = append(billed, at[j])
			}
		}
		if len(batch) == 0 {
			return results
		}
		if err := b.BillInAdvance(ctx, batch); err != nil {
			for _, i := range billed {
				results[i].Disposition, results[i].Code, results[i].Err = Retry, ingest.ErrorInAdvance, err
			}
		}
		return results
	}
}
