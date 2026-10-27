package clickhouse

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"go.opentelemetry.io/otel/trace"
)

// ReadLimits bound the queries that answer a read (usage, buckets, events,
// dead letters), so that one organization's query cannot hold a node:
// ClickHouse aborts a query past a limit with TIMEOUT_EXCEEDED or
// MEMORY_LIMIT_EXCEEDED. The statements of a write (the deduplication
// lookup, the bucket maintenance, a repair or a backfill) are never
// bounded, as a write that fails on a limit would fail on every retry.
// Zero leaves a limit to the server's settings profile.
type ReadLimits struct {
	// MaxExecutionTime is max_execution_time, rounded up to a second.
	MaxExecutionTime time.Duration
	// MaxMemoryUsage is max_memory_usage, in bytes.
	MaxMemoryUsage int64
}

// WithReadLimits sets the limits of the store's reads.
func WithReadLimits(l ReadLimits) Option { return func(s *Store) { s.limits = l } }

// The parser limits of every statement. A charge with thousands of filters,
// or a price lookup with 20000 entries per key set, serializes into a
// statement past the defaults (256 KiB, 50000 nodes). They are sent with
// the query, which the parser reads before the statement.
const (
	maxQuerySize   = 4_194_304
	maxASTElements = 500_000
)

type (
	settingsKey struct{}
	writingKey  struct{}
)

// withSettings returns ctx with settings added to those ctx holds, which
// [ch.WithSettings] alone would replace.
func withSettings(ctx context.Context, settings ch.Settings) context.Context {
	merged := maps.Clone(settingsOf(ctx))
	if merged == nil {
		merged = ch.Settings{}
	}
	maps.Copy(merged, settings)
	ctx = context.WithValue(ctx, settingsKey{}, merged)
	return ch.Context(ctx, ch.WithSettings(merged))
}

func settingsOf(ctx context.Context) ch.Settings {
	s, _ := ctx.Value(settingsKey{}).(ch.Settings)
	return s
}

// writing marks ctx as running the statements of a write, which the read
// limits do not bound.
func writing(ctx context.Context) context.Context {
	return context.WithValue(ctx, writingKey{}, true)
}

func isWriting(ctx context.Context) bool {
	w, _ := ctx.Value(writingKey{}).(bool)
	return w
}

// settingsConn sends with every statement the parser limits, a log_comment
// tying the query in system.query_log to its trace, the trace context
// (which ClickHouse records in system.opentelemetry_span_log when tracing
// is enabled on the server), and, on the reads, the read limits. The
// settings of the statement's context win.
type settingsConn struct {
	driver.Conn
	limits ReadLimits
}

func (c settingsConn) prepare(ctx context.Context, read bool) context.Context {
	settings := ch.Settings{
		"max_query_size":   maxQuerySize,
		"max_ast_elements": maxASTElements,
	}
	if read && !isWriting(ctx) {
		if c.limits.MaxExecutionTime > 0 {
			settings["max_execution_time"] = int(math.Ceil(c.limits.MaxExecutionTime.Seconds()))
			settings["timeout_overflow_mode"] = "throw"
		}
		if c.limits.MaxMemoryUsage > 0 {
			settings["max_memory_usage"] = c.limits.MaxMemoryUsage
		}
	}
	opts := []ch.QueryOption{}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		comment, _ := json.Marshal(map[string]string{"trace_id": sc.TraceID().String(), "span_id": sc.SpanID().String()})
		settings["log_comment"] = string(comment)
		opts = append(opts, ch.WithSpan(sc))
	}
	maps.Copy(settings, settingsOf(ctx))
	return ch.Context(ctx, append(opts, ch.WithSettings(settings))...)
}

// isRead reports whether query is a read: SELECT, WITH or SHOW.
func isRead(query string) bool {
	op, _ := operation(query)
	return op == "SELECT" || op == "WITH" || op == "SHOW"
}

// Exec implements [driver.Conn].
func (c settingsConn) Exec(ctx context.Context, query string, args ...any) error {
	return c.Conn.Exec(c.prepare(ctx, false), query, args...)
}

// Query implements [driver.Conn].
func (c settingsConn) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	return c.Conn.Query(c.prepare(ctx, isRead(query)), query, args...)
}

// QueryRow implements [driver.Conn].
func (c settingsConn) QueryRow(ctx context.Context, query string, args ...any) driver.Row {
	return c.Conn.QueryRow(c.prepare(ctx, isRead(query)), query, args...)
}

// PrepareBatch implements [driver.Conn].
func (c settingsConn) PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error) {
	return c.Conn.PrepareBatch(c.prepare(ctx, false), query, opts...)
}
