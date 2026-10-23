package logctx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/logctx"
)

func TestHandlers(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(logctx.NewHandler(logctx.NewTraceHandler(slog.NewJSONHandler(&buf, nil))))
	ctx := logctx.With(context.Background(), slog.String(logctx.OrganizationKey, "org-1"))
	ctx = logctx.With(ctx, slog.Int64("job.id", 7))
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled})
	ctx = trace.ContextWithSpanContext(ctx, sc)
	log.InfoContext(ctx, "hello")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", buf.String(), err)
	}
	want := map[string]any{
		logctx.OrganizationKey: "org-1",
		"job.id":               float64(7),
		"trace_id":             sc.TraceID().String(),
		"span_id":              sc.SpanID().String(),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s got = %v, want = %v", k, got[k], v)
		}
	}
}

func TestWithDoesNotShareBacking(t *testing.T) {
	base := logctx.With(context.Background(), slog.String("a", "1"))
	x := logctx.With(base, slog.String("b", "x"))
	y := logctx.With(base, slog.String("b", "y"))
	if got := logctx.Attrs(x)[1].Value.String(); got != "x" {
		t.Errorf("x got = %s, want = x", got)
	}
	if got := logctx.Attrs(y)[1].Value.String(); got != "y" {
		t.Errorf("y got = %s, want = y", got)
	}
}
