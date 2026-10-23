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

	"github.com/tanvoxyz/tanvo/apps/server/internal/logctx"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
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

// KeyAuthenticator resolves an API key secret (libs/auth APIKeys).
type KeyAuthenticator interface {
	// Authenticate returns the active key whose secret is secret, or
	// auth.ErrUnauthenticated.
	Authenticate(ctx context.Context, secret string) (*access.APIKey, error)
}

type principalKey struct{}

// Principal returns the principal the [Auth] interceptor authenticated.
func Principal(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	return p, ok
}

// WithPrincipal returns ctx carrying p, as [Auth] does.
func WithPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// Auth enforces the tanvo.common.v1.access option of each RPC
// (architecture §4.4): anonymous RPCs pass; the others need an API key in
// "Authorization: Bearer <key>" granted the RPC's api_resource at the
// read level for NO_SIDE_EFFECTS RPCs and the write level otherwise. An
// RPC without the option is refused. The organization of the principal
// is put on the span and on the logs of the request.
func Auth(keys KeyAuthenticator, authz *auth.Authorizer) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			md, ok := spec.Schema.(protoreflect.MethodDescriptor)
			if !ok {
				return connect.NewError(connect.CodePermissionDenied, "procedure has no access rule")
			}
			rule, _ := proto.GetExtension(md.Options(), commonv1.E_Access).(*commonv1.Access)
			if rule == nil {
				return connect.NewError(connect.CodePermissionDenied, "procedure has no access rule")
			}
			if rule.GetAnonymous() {
				return next(ctx, spec, stream)
			}
			info, _ := connect.CallInfoForServerContext(ctx)
			secret, ok := bearer(info)
			if !ok {
				return connect.NewError(connect.CodeUnauthenticated, "missing API key: send Authorization: Bearer <key>")
			}
			key, err := keys.Authenticate(ctx, secret)
			if err != nil {
				return Error(ctx, err)
			}
			p := auth.APIKeyPrincipal(key)
			span := trace.SpanFromContext(ctx)
			span.SetAttributes(attribute.String(logctx.OrganizationKey, p.OrganizationID.String()))
			ctx = logctx.With(WithPrincipal(ctx, p), slog.String(logctx.OrganizationKey, p.OrganizationID.String()))
			if rule.GetApiResource() == "" {
				return connect.NewError(connect.CodePermissionDenied, "API keys cannot call this procedure")
			}
			level := permission.LevelWrite
			if idempotency(md) == descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
				level = permission.LevelRead
			}
			if err := authz.CheckAPI(p, permission.APIResource(rule.GetApiResource()), level); err != nil {
				return Error(ctx, err)
			}
			return next(ctx, spec, stream)
		}
	}
}

func idempotency(md protoreflect.MethodDescriptor) descriptorpb.MethodOptions_IdempotencyLevel {
	if o, ok := md.Options().(*descriptorpb.MethodOptions); ok {
		return o.GetIdempotencyLevel()
	}
	return descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN
}

func bearer(info *connect.CallInfo) (string, bool) {
	if info == nil {
		return "", false
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(info.RequestHeader().Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
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
