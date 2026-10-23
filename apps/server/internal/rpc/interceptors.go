package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// scope is the instrumentation scope of the RPC telemetry.
const scope = "github.com/tanvoxyz/tanvo/apps/server/internal/rpc"

// Recover turns a handler panic into an Internal error and logs it with
// its stack.
func Recover() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) (err error) {
			defer func() {
				if p := recover(); p != nil {
					slog.ErrorContext(ctx, "rpc: panic", "procedure", spec.Procedure, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
					err = connect.NewError(connect.CodeInternal, "internal error")
				}
			}()
			return next(ctx, spec, stream)
		}
	}
}

// Telemetry traces and measures each RPC after the RPC semantic
// conventions: a server span named after the procedure, a child of the
// HTTP span, and the rpc.server.call.duration histogram, whose attributes
// are bounded (service, method, status code).
func Telemetry(tp trace.TracerProvider, mp metric.MeterProvider) (connect.ServerInterceptor, error) {
	tracer := tp.Tracer(scope)
	duration, err := mp.Meter(scope).Float64Histogram("rpc.server.call.duration",
		metric.WithUnit("s"), metric.WithDescription("Duration of RPCs served."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10))
	if err != nil {
		return nil, fmt.Errorf("rpc: telemetry: %w", err)
	}
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			service, method := splitProcedure(spec.Procedure)
			attrs := []attribute.KeyValue{
				attribute.String("rpc.system", "connect_rpc"),
				attribute.String("rpc.service", service),
				attribute.String("rpc.method", method),
			}
			ctx, span := tracer.Start(ctx, strings.TrimPrefix(spec.Procedure, "/"),
				trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...))
			defer span.End()
			start := time.Now()
			err := next(ctx, spec, stream)
			status := "ok"
			if err != nil {
				code := connect.CodeOf(err)
				status = code.String()
				span.SetAttributes(attribute.String("rpc.connect_rpc.error_code", status))
				if serverFault(code) {
					span.SetStatus(codes.Error, err.Error())
				}
			}
			duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(append(attrs, attribute.String("rpc.connect_rpc.status_code", status))...))
			return err
		}
	}, nil
}

// serverFault reports the codes that are the server's fault, which mark
// the span as failed (the semantic conventions leave client errors
// unset).
func serverFault(c connect.Code) bool {
	switch c {
	case connect.CodeUnknown, connect.CodeDeadlineExceeded, connect.CodeUnimplemented,
		connect.CodeInternal, connect.CodeUnavailable, connect.CodeDataLoss:
		return true
	}
	return false
}

func splitProcedure(p string) (service, method string) {
	p = strings.TrimPrefix(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

func idempotencyLevel(md protoreflect.MethodDescriptor) descriptorpb.MethodOptions_IdempotencyLevel {
	if o, ok := md.Options().(*descriptorpb.MethodOptions); ok {
		return o.GetIdempotencyLevel()
	}
	return descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN
}

// Validate checks every request message with protovalidate and rejects
// an invalid one with InvalidArgument and a BadRequest detail.
func Validate(v protovalidate.Validator) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			return next(ctx, spec, &validatingStream{ServerStream: stream, v: v})
		}
	}
}

type validatingStream struct {
	connect.ServerStream
	v protovalidate.Validator
}

func (s *validatingStream) Receive(msg any) error {
	if err := s.ServerStream.Receive(msg); err != nil {
		return err
	}
	m, ok := msg.(proto.Message)
	if !ok {
		return nil
	}
	err := s.v.Validate(m)
	if err == nil {
		return nil
	}
	var ve *protovalidate.ValidationError
	if !errors.As(err, &ve) {
		return connect.NewError(connect.CodeInternal, "request validation failed").WithCause(err)
	}
	violations := make([]FieldViolation, 0, len(ve.Violations))
	for _, v := range ve.Violations {
		violations = append(violations, FieldViolation{
			Field:       protovalidate.FieldPathString(v.Proto.GetField()),
			Reason:      v.Proto.GetRuleId(),
			Description: v.Proto.GetMessage(),
		})
	}
	return InvalidArgument("invalid request", violations)
}
