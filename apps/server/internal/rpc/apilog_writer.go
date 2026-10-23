package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// APILogWriterConfig sizes an [APILogWriter]; zero fields take the
// defaults.
type APILogWriterConfig struct {
	// Capacity is the number of logs queued before new ones are dropped;
	// default 10 000.
	Capacity int
	// BatchSize is the number of logs written per transaction; default
	// 500.
	BatchSize int
	// Interval is the longest a queued log waits to be written; default
	// one second.
	Interval time.Duration
	// FlushTimeout bounds the final write when Run stops; default ten
	// seconds.
	FlushTimeout time.Duration
}

// APILogWriter stores API logs off the request path: [APILogWriter.Enqueue]
// never blocks, and [APILogWriter.Run] writes the queue in batches, one
// transaction per batch (lago produces its API logs to Kafka, which a
// consumer stores, app/services/utils/api_log.rb). A log that does not fit
// in the queue, or whose batch fails to be written, is dropped and counted
// by the tanvo.api_log.dropped counter.
type APILogWriter struct {
	tx      port.Transactor
	log     *slog.Logger
	cfg     APILogWriterConfig
	queue   chan *auditlog.APILog
	dropped metric.Int64Counter
}

// Reasons of the dropped counter.
var (
	apiLogOverflow   = metric.WithAttributeSet(attribute.NewSet(attribute.String("reason", "overflow")))
	apiLogWriteError = metric.WithAttributeSet(attribute.NewSet(attribute.String("reason", "write_error")))
)

// NewAPILogWriter returns a writer storing through tx; mp receives the
// dropped counter, and log, when not nil, the write failures.
func NewAPILogWriter(tx port.Transactor, log *slog.Logger, mp metric.MeterProvider, cfg APILogWriterConfig) (*APILogWriter, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = 10_000
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = 10 * time.Second
	}
	dropped, err := mp.Meter(scope).Int64Counter("tanvo.api_log.dropped",
		metric.WithUnit("{log}"), metric.WithDescription("API logs dropped because the queue was full or their write failed."))
	if err != nil {
		return nil, fmt.Errorf("rpc: api log writer: %w", err)
	}
	return &APILogWriter{tx: tx, log: log, cfg: cfg, queue: make(chan *auditlog.APILog, cfg.Capacity), dropped: dropped}, nil
}

// Enqueue queues l for writing; when the queue is full, l is dropped.
func (w *APILogWriter) Enqueue(ctx context.Context, l *auditlog.APILog) {
	select {
	case w.queue <- l:
	default:
		w.dropped.Add(ctx, 1, apiLogOverflow)
	}
}

// Run writes the queued logs until ctx ends, then writes what is left
// within the flush timeout. Only one Run may be active.
func (w *APILogWriter) Run(ctx context.Context) error {
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	batch := make([]*auditlog.APILog, 0, w.cfg.BatchSize)
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.cfg.FlushTimeout)
			defer cancel()
			w.write(fctx, batch)
			w.Flush(fctx)
			return nil
		case l := <-w.queue:
			if batch = append(batch, l); len(batch) >= w.cfg.BatchSize {
				w.write(ctx, batch)
				batch = batch[:0]
			}
		case <-t.C:
			w.write(ctx, batch)
			batch = batch[:0]
		}
	}
}

// Flush writes the logs queued now, in batches. It is for a writer whose
// Run is not active (tests) and for Run's final write.
func (w *APILogWriter) Flush(ctx context.Context) {
	batch := make([]*auditlog.APILog, 0, w.cfg.BatchSize)
	for {
		select {
		case l := <-w.queue:
			if batch = append(batch, l); len(batch) >= w.cfg.BatchSize {
				w.write(ctx, batch)
				batch = batch[:0]
			}
		default:
			w.write(ctx, batch)
			return
		}
	}
}

// write stores batch in one transaction; on failure the batch is dropped.
func (w *APILogWriter) write(ctx context.Context, batch []*auditlog.APILog) {
	if len(batch) == 0 {
		return
	}
	err := w.tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		for _, l := range batch {
			if err := r.APILogs.Create(ctx, l); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.dropped.Add(ctx, int64(len(batch)), apiLogWriteError)
		w.log.ErrorContext(ctx, "rpc: write api logs", "count", len(batch), "error", err)
	}
}
