package clickhouse_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// TestStatementSettings checks in system.query_log the settings the store
// sends: the trace in log_comment on every statement, and the read limits
// on the reads only, not on the statements of a write.
func TestStatementSettings(t *testing.T) {
	conn := database(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	store := clickhouse.New(conn, clickhouse.WithTracerProvider(tp),
		clickhouse.WithReadLimits(clickhouse.ReadLimits{MaxExecutionTime: 6500 * time.Millisecond, MaxMemoryUsage: 3_000_000_000}))

	ctx, span := tp.Tracer("test").Start(t.Context(), "test")
	ts := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	if _, err := store.Write(ctx, []metering.Event{event("a", ts)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Count(ctx, metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", From: ts, To: ts.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	span.End()
	trace := span.SpanContext().TraceID().String()

	// Each node logs the statements it ran.
	var reads, writes int
	for _, conn := range nodes() {
		func() {
			if err := conn.Exec(t.Context(), "SYSTEM FLUSH LOGS"); err != nil {
				t.Fatal(err)
			}
			rows, err := conn.Query(t.Context(), `SELECT query, log_comment, Settings['max_execution_time'], Settings['max_memory_usage'], Settings['max_query_size']
				FROM system.query_log
				WHERE type = 'QueryFinish' AND JSONExtractString(log_comment, 'trace_id') = $1
				ORDER BY event_time_microseconds`, trace)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var query, comment, timeout, memory, size string
				if err := rows.Scan(&query, &comment, &timeout, &memory, &size); err != nil {
					t.Fatal(err)
				}
				var c struct {
					TraceID string `json:"trace_id"`
					SpanID  string `json:"span_id"`
				}
				if err := json.Unmarshal([]byte(comment), &c); err != nil || c.SpanID == "" {
					t.Errorf("log_comment got = %q, want = the trace and span IDs", comment)
				}
				if size != "4194304" {
					t.Errorf("max_query_size of %.40q got = %q, want = 4194304", query, size)
				}
				switch {
				case strings.Contains(query, "toInt64(count())"):
					reads++
					if timeout != "7" || memory != "3000000000" {
						t.Errorf("limits of the count got = %q s, %q bytes, want = 7 s, 3000000000 bytes", timeout, memory)
					}
				default:
					writes++
					if timeout != "" || memory != "" {
						t.Errorf("limits of %.60q in the write got = %q s, %q bytes, want = none", query, timeout, memory)
					}
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		}()
	}
	if reads != 1 || writes == 0 {
		t.Errorf("statements of the trace got = %d reads, %d of the write, want = 1 read and the write's", reads, writes)
	}
}
