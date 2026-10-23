package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// dropped returns the tanvo.api_log.dropped counts by reason.
func dropped(t *testing.T, r *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "tanvo.api_log.dropped" {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				reason, _ := p.Attributes.Value("reason")
				got[reason.AsString()] += p.Value
			}
		}
	}
	return got
}

func TestAPILogWriter(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := memstore.New()
	org := core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	reader := sdkmetric.NewManualReader()
	w, err := NewAPILogWriter(st, nil, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), APILogWriterConfig{Capacity: 3, BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	logs := make([]*auditlog.APILog, 4)
	for i := range logs {
		logs[i] = &auditlog.APILog{ID: uuid.New(), OrganizationID: org, HTTPMethod: "post", HTTPStatus: 200, LoggedAt: now, CreatedAt: now}
	}
	// The fourth log does not fit in the queue and is dropped, without
	// blocking.
	for _, l := range logs {
		w.Enqueue(ctx, l)
	}
	if got := dropped(t, reader); got["overflow"] != 1 {
		t.Errorf("dropped got = %v, want = overflow 1", got)
	}
	w.Flush(ctx)
	count := func() int {
		n := 0
		_ = st.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			for _, l := range logs {
				if _, err := r.APILogs.Get(ctx, org, l.ID); err == nil {
					n++
				}
			}
			return nil
		})
		return n
	}
	if got := count(); got != 3 {
		t.Errorf("stored logs got = %d, want = 3", got)
	}

	// Run writes what is queued when it stops.
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error)
	go func() { done <- w.Run(runCtx) }()
	w.Enqueue(ctx, logs[3])
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run() got = %v, want = nil", err)
	}
	if got := count(); got != 4 {
		t.Errorf("stored logs after Run got = %d, want = 4", got)
	}
}
