package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/logctx"
	"github.com/tanvoxyz/tanvo/apps/server/internal/telemetry"
)

func TestSetupSDK(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	var out bytes.Buffer
	tel, err := telemetry.Setup(t.Context(), telemetry.Options{Version: "1.2.3", Role: "api", LogLevel: slog.LevelInfo, Stdout: &out, Prometheus: true, Environ: []string{}})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	defer func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	}()

	counter, err := tel.Meter().Int64Counter("tanvo.test.calls")
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := tel.Tracer().Start(t.Context(), "test")
	counter.Add(ctx, 1)
	ctx = logctx.With(ctx, slog.String(logctx.OrganizationKey, "org-1"))
	tel.Logger.InfoContext(ctx, "hello")
	tel.Logger.DebugContext(ctx, "hidden")
	span.End()

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err != nil {
		t.Fatalf("log got = %q, want = one JSON record: %v", out.String(), err)
	}
	if rec["msg"] != "hello" || rec[logctx.OrganizationKey] != "org-1" || rec["trace_id"] == nil || rec["span_id"] == nil {
		t.Errorf("log record got = %v, want = hello with the organization and trace ids", rec)
	}

	rr := httptest.NewRecorder()
	tel.Metrics.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "tanvo_test_calls") {
		t.Errorf("/metrics got = %d %q, want = 200 with tanvo_test_calls", rr.Code, rr.Body.String())
	}
}

func TestSetupDisabled(t *testing.T) {
	var out bytes.Buffer
	tel, err := telemetry.Setup(t.Context(), telemetry.Options{LogText: true, Stdout: &out, Prometheus: true, Environ: []string{"OTEL_SDK_DISABLED=true"}})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if tel.Metrics != nil {
		t.Error("Metrics got = a handler, want = nil with the SDK disabled")
	}
	tel.Logger.Info("hello")
	if !strings.Contains(out.String(), "msg=hello") {
		t.Errorf("log got = %q, want = a text record", out.String())
	}
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown() error = %v", err)
	}
}
