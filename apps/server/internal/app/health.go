package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Health endpoints (architecture §13.3): /healthz is liveness (the
// process serves HTTP), /readyz is readiness (the dependencies of the
// process's roles answer, and the process is not shutting down).
const (
	livenessPath  = "/healthz"
	readinessPath = "/readyz"
	metricsPath   = "/metrics"
)

// checkTimeout bounds each readiness check.
const checkTimeout = 2 * time.Second

// check is one readiness check.
type check struct {
	name string
	run  func(context.Context) error
}

// health serves the health endpoints.
type health struct {
	log      *slog.Logger
	checks   []check
	draining atomic.Bool
}

// add adds a readiness check.
func (h *health) add(name string, run func(context.Context) error) {
	h.checks = append(h.checks, check{name: name, run: run})
}

// readiness is the body of /readyz.
type readiness struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

func (h *health) mount(mux *http.ServeMux) {
	mux.HandleFunc("GET "+livenessPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, readiness{Status: "ok"})
	})
	mux.HandleFunc("GET "+readinessPath, h.ready)
}

// ready runs the checks concurrently.
func (h *health) ready(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, readiness{Status: "shutting_down"})
		return
	}
	res := readiness{Status: "ok", Checks: make(map[string]string, len(h.checks))}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range h.checks {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
			defer cancel()
			status := "ok"
			if err := c.run(ctx); err != nil {
				// The reason is logged, not shown: the endpoint is
				// unauthenticated.
				status = "unavailable"
				h.log.WarnContext(ctx, "readiness check failed", "check", c.name, "error", err)
			}
			mu.Lock()
			res.Checks[c.name] = status
			if status != "ok" {
				res.Status = "unavailable"
			}
			mu.Unlock()
		})
	}
	wg.Wait()
	code := http.StatusOK
	if res.Status != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, res)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
