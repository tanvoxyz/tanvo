package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
)

// RequestIDHeader carries the client's request ID, which becomes the ID
// of the request's API log when it is a UUID (lago request.request_id).
const RequestIDHeader = "X-Request-Id"

// apiLogSkipped are the services whose RPCs are not logged (lago
// skip_audit_logs!: the events, activity, API and security log
// controllers).
var apiLogSkipped = []string{"tanvo.event.v1.EventService", "tanvo.log.v1.LogService"}

type httpRequestKey struct{}

// httpRequest is the original HTTP request of a REST call.
type httpRequest struct {
	method, path, origin string
}

// RESTRequests records the method, path and origin of the HTTP requests
// next serves, for the API logs of REST calls, which reach the RPC
// handlers as Connect calls.
func RESTRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		hr := httpRequest{method: r.Method, path: r.URL.Path, origin: scheme + "://" + r.Host}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), httpRequestKey{}, hr)))
	})
}

// APILogs records the API logs of the RPCs that API keys call, except
// NO_SIDE_EFFECTS RPCs and the services of [apiLogSkipped] (lago
// ApiLoggable, app/controllers/concerns/api_loggable.rb, and
// Utils::ApiLog): the request and its response or error, after the RPC.
// The log is queued on w, off the request path. It runs after [Auth].
func APILogs(w *APILogWriter, clock core.Clock, ids core.IDGenerator) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			p, ok := Principal(ctx)
			md, isMethod := spec.Schema.(protoreflect.MethodDescriptor)
			if !ok || p.Kind != auth.PrincipalAPIKey || p.APIKey == nil || !isMethod ||
				spec.StreamType != connect.StreamTypeUnary || idempotencyLevel(md) == descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
				return next(ctx, spec, stream)
			}
			service, _ := splitProcedure(spec.Procedure)
			for _, s := range apiLogSkipped {
				if service == s {
					return next(ctx, spec, stream)
				}
			}
			s := &loggingStream{ServerStream: stream}
			err := next(ctx, spec, s)
			l := apiLog(ctx, spec, p, s, err)
			l.ID = requestID(ctx, ids)
			l.LoggedAt = clock.Now().UTC()
			l.CreatedAt = l.LoggedAt
			w.Enqueue(ctx, l)
			return err
		}
	}
}

// loggingStream keeps the request and response messages of a unary RPC.
type loggingStream struct {
	connect.ServerStream
	req, resp proto.Message
}

func (s *loggingStream) Receive(msg any) error {
	if err := s.ServerStream.Receive(msg); err != nil {
		return err
	}
	if m, ok := msg.(proto.Message); ok {
		s.req = m
	}
	return nil
}

func (s *loggingStream) Send(msg any) error {
	if m, ok := msg.(proto.Message); ok {
		s.resp = m
	}
	return s.ServerStream.Send(msg)
}

func apiLog(ctx context.Context, spec connect.Spec, p auth.Principal, s *loggingStream, err error) *auditlog.APILog {
	l := &auditlog.APILog{
		OrganizationID: p.OrganizationID,
		APIKeyID:       p.APIKey.ID,
		HTTPMethod:     "post",
		HTTPStatus:     http.StatusOK,
		RequestPath:    spec.Procedure,
		RequestBody:    messageJSON(s.req),
		Response:       messageJSON(s.resp),
	}
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		l.Client = info.RequestHeader().Get("User-Agent")
	}
	if hr, ok := ctx.Value(httpRequestKey{}).(httpRequest); ok {
		l.HTTPMethod, l.RequestPath, l.RequestOrigin = strings.ToLower(hr.method), hr.path, hr.origin
	}
	// The API version: the REST path's first segment ("/v1/customers"),
	// or the package version of the procedure ("/tanvo.customer.v1.…").
	if seg, _, _ := strings.Cut(strings.TrimPrefix(l.RequestPath, "/"), "/"); isVersion(seg) {
		l.APIVersion = seg
	} else {
		service, _ := splitProcedure(spec.Procedure)
		parts := strings.Split(service, ".")
		if len(parts) >= 2 && isVersion(parts[len(parts)-2]) {
			l.APIVersion = parts[len(parts)-2]
		}
	}
	if err != nil {
		var ce *connect.Error
		code := connect.CodeUnknown
		msg := err.Error()
		if errors.As(err, &ce) {
			code, msg = ce.Code(), ce.Message()
		}
		l.HTTPStatus = httpStatus(code)
		l.Response = map[string]any{"code": code.String(), "message": msg}
	}
	return l
}

func isVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// requestID returns the client's request ID when it is a UUID, a new ID
// otherwise.
func requestID(ctx context.Context, ids core.IDGenerator) uuid.UUID {
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		if id, err := uuid.Parse(info.RequestHeader().Get(RequestIDHeader)); err == nil && id != uuid.Nil {
			return id
		}
	}
	return ids.NewUUID()
}

// messageJSON returns m as a JSON document with the proto field names; nil
// for nil.
func messageJSON(m proto.Message) map[string]any {
	if m == nil {
		return nil
	}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// httpStatus is the HTTP status of a Connect error code (the Connect
// protocol's mapping).
func httpStatus(c connect.Code) int {
	switch c {
	case connect.CodeCanceled:
		return 499
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeOutOfRange:
		return http.StatusBadRequest
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeAlreadyExists, connect.CodeAborted:
		return http.StatusConflict
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}
