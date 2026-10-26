// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package server

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

	"github.com/tanvoxyz/tanvo/ee/analytics/servicetoken"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
)

// scope is the instrumentation scope of the service's telemetry.
const scope = "github.com/tanvoxyz/tanvo/ee/analytics/server"

// organizationKey is the span and log attribute of the organization, as
// the core names it.
const organizationKey = "tanvo.organization_id"

type organizationCtx struct{}

// organization returns the organization of the request, set by [Auth].
func organization(ctx context.Context) string {
	org, _ := ctx.Value(organizationCtx{}).(string)
	return org
}

// WithOrganization returns ctx acting in org, as [Auth] does.
func WithOrganization(ctx context.Context, org string) context.Context {
	return context.WithValue(ctx, organizationCtx{}, org)
}

// Telemetry traces and measures each RPC after the RPC semantic
// conventions, and logs the errors that are the server's fault.
func Telemetry(tp trace.TracerProvider, mp metric.MeterProvider, log *slog.Logger) (connect.ServerInterceptor, error) {
	tracer := tp.Tracer(scope)
	duration, err := mp.Meter(scope).Float64Histogram("rpc.server.call.duration",
		metric.WithUnit("s"), metric.WithDescription("Duration of RPCs served."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10))
	if err != nil {
		return nil, fmt.Errorf("server: telemetry: %w", err)
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
					log.ErrorContext(ctx, "rpc failed", "procedure", spec.Procedure, "code", status, "error", cause(err))
				}
			}
			duration.Record(ctx, time.Since(start).Seconds(),
				metric.WithAttributes(append(attrs, attribute.String("rpc.connect_rpc.status_code", status))...))
			return err
		}
	}, nil
}

// cause returns the cause of a Connect error, or err.
func cause(err error) error {
	if u := errors.Unwrap(err); u != nil {
		return u
	}
	return err
}

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

// Recover turns a handler panic into an Internal error and logs it with
// its stack.
func Recover(log *slog.Logger) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) (err error) {
			defer func() {
				if p := recover(); p != nil {
					log.ErrorContext(ctx, "rpc: panic", "procedure", spec.Procedure, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
					err = connect.NewError(connect.CodeInternal, "internal error")
				}
			}()
			return next(ctx, spec, stream)
		}
	}
}

// License is the license state of the service.
type License interface {
	// Err returns why the service is not licensed, nil when it is.
	Err() error
}

// Licensed refuses every RPC with Unavailable while l is not valid.
func Licensed(l License) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			if err := l.Err(); err != nil {
				return connect.NewError(connect.CodeUnavailable, "the Data API is not licensed: "+err.Error())
			}
			return next(ctx, spec, stream)
		}
	}
}

// Auth authenticates each RPC with a service token of the core, sent as
// "Authorization: Bearer <token>", and requires the permission of the
// RPC's tanvo.common.v1.access option among the token's. The request then
// acts in the token's organization, which goes on the span and the logs.
func Auth(v *servicetoken.Verifier) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			md, ok := spec.Schema.(protoreflect.MethodDescriptor)
			if !ok {
				return connect.NewError(connect.CodePermissionDenied, "procedure has no access rule")
			}
			rule, _ := proto.GetExtension(md.Options(), commonv1.E_Access).(*commonv1.Access)
			if rule == nil || rule.GetPermission() == "" {
				return connect.NewError(connect.CodePermissionDenied, "procedure has no access rule")
			}
			info, _ := connect.CallInfoForServerContext(ctx)
			token, ok := bearer(info)
			if !ok {
				return connect.NewError(connect.CodeUnauthenticated, "missing credential: send Authorization: Bearer <token>")
			}
			c, err := v.Verify(token)
			if err != nil {
				return connect.NewError(connect.CodeUnauthenticated, "invalid service token").WithCause(err)
			}
			if !uuidShaped(c.OrganizationID) {
				return connect.NewError(connect.CodeUnauthenticated, "invalid service token: organization")
			}
			trace.SpanFromContext(ctx).SetAttributes(attribute.String(organizationKey, c.OrganizationID))
			if !c.Has(rule.GetPermission()) {
				return connect.NewError(connect.CodePermissionDenied, "missing permission "+rule.GetPermission())
			}
			return next(WithOrganization(ctx, c.OrganizationID), spec, stream)
		}
	}
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

// uuidShaped reports whether s is a UUID in its canonical text form: an
// organization the row-level security of the core can match, never the
// wildcard.
func uuidShaped(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

// Validate checks every request message with protovalidate.
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
	if err := s.v.Validate(m); err != nil {
		var ve *protovalidate.ValidationError
		if !errors.As(err, &ve) {
			return connect.NewError(connect.CodeInternal, "request validation failed").WithCause(err)
		}
		return connect.NewError(connect.CodeInvalidArgument, ve.Error())
	}
	return nil
}
