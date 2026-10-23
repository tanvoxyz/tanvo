// Package logctx carries log attributes in a context (the organization,
// job and request IDs) and adds them, with the trace and span IDs, to
// every slog record logged with that context (architecture §15).
package logctx

import (
	"context"
	"log/slog"
	"slices"

	"go.opentelemetry.io/otel/trace"
)

// OrganizationKey is the attribute key of the organization ID on logs
// and spans.
const OrganizationKey = "tanvo.organization.id"

type attrsKey struct{}

// With returns ctx carrying attrs besides those it already carries.
func With(ctx context.Context, attrs ...slog.Attr) context.Context {
	prev, _ := ctx.Value(attrsKey{}).([]slog.Attr)
	return context.WithValue(ctx, attrsKey{}, append(slices.Clip(prev), attrs...))
}

// Attrs returns the attributes ctx carries.
func Attrs(ctx context.Context) []slog.Attr {
	a, _ := ctx.Value(attrsKey{}).([]slog.Attr)
	return a
}

// Handler adds the context's attributes to the records it hands to next.
type Handler struct{ next slog.Handler }

// NewHandler returns a handler adding the context's attributes.
func NewHandler(next slog.Handler) *Handler { return &Handler{next: next} }

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if a := Attrs(ctx); len(a) > 0 {
		r = r.Clone()
		r.AddAttrs(a...)
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{next: h.next.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler { return &Handler{next: h.next.WithGroup(name)} }

// TraceHandler adds trace_id and span_id of the context's span, for sinks
// that do not carry the trace context natively (JSON on stdout).
type TraceHandler struct{ next slog.Handler }

// NewTraceHandler returns a handler adding the trace and span IDs.
func NewTraceHandler(next slog.Handler) *TraceHandler { return &TraceHandler{next: next} }

// Enabled implements slog.Handler.
func (h *TraceHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

// Handle implements slog.Handler.
func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{next: h.next.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{next: h.next.WithGroup(name)}
}
