package clickhouse

import (
	"cmp"
	"context"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// scopeName is the instrumentation scope of the store's spans.
const scopeName = "github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"

// WithTracerProvider sets the provider of the client spans the store makes
// around its ClickHouse statements, following the database semantic
// conventions (db.system.name, db.operation.name, db.collection.name for
// inserts, and the parameterized db.query.text). Without it the store
// makes no spans.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(s *Store) { s.tracer = tp }
}

// tracedConn wraps the statements of a connection in client spans; the
// methods it does not override (the store does not use them) are not
// traced.
type tracedConn struct {
	driver.Conn
	tracer trace.Tracer
}

// operation returns the operation of a statement, its first keyword, and
// the table of an INSERT.
func operation(query string) (op, table string) {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return "", ""
	}
	op = strings.ToUpper(fields[0])
	if op == "INSERT" && len(fields) > 2 && strings.EqualFold(fields[1], "INTO") {
		table, _, _ = strings.Cut(fields[2], "(")
	}
	return op, table
}

func (c tracedConn) start(ctx context.Context, query string) (context.Context, trace.Span) {
	op, table := operation(query)
	attrs := []attribute.KeyValue{
		attribute.String("db.system.name", "clickhouse"),
		attribute.String("db.query.text", query),
	}
	name := "clickhouse"
	if op != "" {
		attrs = append(attrs, attribute.String("db.operation.name", op))
		name = op
	}
	if table != "" {
		attrs = append(attrs, attribute.String("db.collection.name", table))
		name += " " + table
	}
	return c.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
}

// end ends the span, recording err.
func end(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// Exec implements [driver.Conn].
func (c tracedConn) Exec(ctx context.Context, query string, args ...any) error {
	ctx, span := c.start(ctx, query)
	err := c.Conn.Exec(ctx, query, args...)
	end(span, err)
	return err
}

// Query implements [driver.Conn]. The span ends when the rows are closed.
func (c tracedConn) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	ctx, span := c.start(ctx, query)
	rows, err := c.Conn.Query(ctx, query, args...)
	if err != nil {
		end(span, err)
		return nil, err
	}
	return &tracedRows{Rows: rows, span: span}, nil
}

// QueryRow implements [driver.Conn].
func (c tracedConn) QueryRow(ctx context.Context, query string, args ...any) driver.Row {
	ctx, span := c.start(ctx, query)
	row := c.Conn.QueryRow(ctx, query, args...)
	end(span, row.Err())
	return row
}

// PrepareBatch implements [driver.Conn]. The span ends when the batch is
// sent or aborted.
func (c tracedConn) PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error) {
	ctx, span := c.start(ctx, query)
	b, err := c.Conn.PrepareBatch(ctx, query, opts...)
	if err != nil {
		end(span, err)
		return nil, err
	}
	return &tracedBatch{Batch: b, span: span}, nil
}

// tracedRows ends the span of a query when closed.
type tracedRows struct {
	driver.Rows
	span trace.Span
	done bool
}

// Close implements [driver.Rows].
func (r *tracedRows) Close() error {
	err := r.Rows.Close()
	if !r.done {
		r.done = true
		end(r.span, cmp.Or(r.Err(), err))
	}
	return err
}

// tracedBatch ends the span of a batch insert when sent or aborted,
// recording its number of rows.
type tracedBatch struct {
	driver.Batch
	span trace.Span
	done bool
}

func (b *tracedBatch) finish(err error) {
	if !b.done {
		b.done = true
		b.span.SetAttributes(attribute.Int("db.operation.batch.size", b.Rows()))
		end(b.span, err)
	}
}

// Send implements [driver.Batch].
func (b *tracedBatch) Send() error {
	err := b.Batch.Send()
	b.finish(err)
	return err
}

// Abort implements [driver.Batch].
func (b *tracedBatch) Abort() error {
	err := b.Batch.Abort()
	b.finish(nil)
	return err
}
