package clickhouse_test

import (
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

func TestTracerProvider(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	store := clickhouse.New(database(t), clickhouse.WithTracerProvider(tp))
	ts := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	if _, err := store.Write(t.Context(), []metering.Event{event("a", ts)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Count(t.Context(), metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", From: ts, To: ts}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range rec.Ended() {
		names = append(names, s.Name())
		attrs := attribute.NewSet(s.Attributes()...)
		if v, _ := attrs.Value("db.system.name"); v.AsString() != "clickhouse" {
			t.Errorf("span %q db.system.name got = %q, want = clickhouse", s.Name(), v.AsString())
		}
		if v, _ := attrs.Value("db.query.text"); v.AsString() == "" {
			t.Errorf("span %q db.query.text got = empty, want = the statement", s.Name())
		}
	}
	for _, want := range []string{"SELECT", "INSERT events_enriched", "INSERT events_raw"} {
		if !slices.Contains(names, want) {
			t.Errorf("spans got = %v, want = one named %q", names, want)
		}
	}
}
