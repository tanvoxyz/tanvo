package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	logv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/log/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/log/v1/logv1connect"
)

// Logs serves tanvo.log.v1.LogService over the audit log service (lago
// Api::V1::ActivityLogsController, ApiLogsController and
// SecurityLogsController).
type Logs struct {
	logv1connect.UnimplementedLogServiceHandler

	svc *auth.AuditLogs
}

var _ logv1connect.LogServiceHandler = (*Logs)(nil)

// NewLogs returns the service over svc.
func NewLogs(svc *auth.AuditLogs) *Logs { return &Logs{svc: svc} }

var activitySources = NewEnum(map[auditlog.ActivitySource]logv1.ActivitySource{
	auditlog.ActivitySourceAPI:    logv1.ActivitySource_ACTIVITY_SOURCE_API,
	auditlog.ActivitySourceFront:  logv1.ActivitySource_ACTIVITY_SOURCE_DASHBOARD,
	auditlog.ActivitySourceSystem: logv1.ActivitySource_ACTIVITY_SOURCE_SYSTEM,
})

// GetActivityLog implements LogService.GetActivityLog.
func (s *Logs) GetActivityLog(ctx context.Context, req *logv1.GetActivityLogRequest) (*logv1.GetActivityLogResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("activity_id", req.GetActivityId())
	if err != nil {
		return nil, err
	}
	l, err := s.svc.GetActivityLog(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return logv1.GetActivityLogResponse_builder{ActivityLog: activityLogProto(l)}.Build(), nil
}

// ListActivityLogs implements LogService.ListActivityLogs (lago
// ActivityLogsQuery).
func (s *Logs) ListActivityLogs(ctx context.Context, req *logv1.ListActivityLogsRequest) (*logv1.ListActivityLogsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	q, err := activityLogsQuery(req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListActivityLogs(ctx, p.OrganizationID, q, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return logv1.ListActivityLogsResponse_builder{
		ActivityLogs:  Items(page, activityLogProto),
		NextPageToken: &page.NextToken,
	}.Build(), nil
}

// GetApiLog implements LogService.GetApiLog.
func (s *Logs) GetApiLog(ctx context.Context, req *logv1.GetApiLogRequest) (*logv1.GetApiLogResponse, error) { //nolint:revive // generated name
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("request_id", req.GetRequestId())
	if err != nil {
		return nil, err
	}
	l, err := s.svc.GetAPILog(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return logv1.GetApiLogResponse_builder{ApiLog: apiLogProto(l)}.Build(), nil
}

// ListApiLogs implements LogService.ListApiLogs (lago ApiLogsQuery).
func (s *Logs) ListApiLogs(ctx context.Context, req *logv1.ListApiLogsRequest) (*logv1.ListApiLogsResponse, error) { //nolint:revive // generated name
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := apiLogsFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListAPILogs(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return logv1.ListApiLogsResponse_builder{
		ApiLogs:       Items(page, apiLogProto),
		NextPageToken: &page.NextToken,
	}.Build(), nil
}

// GetSecurityLog implements LogService.GetSecurityLog.
func (s *Logs) GetSecurityLog(ctx context.Context, req *logv1.GetSecurityLogRequest) (*logv1.GetSecurityLogResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("log_id", req.GetLogId())
	if err != nil {
		return nil, err
	}
	l, err := s.svc.GetSecurityLog(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return logv1.GetSecurityLogResponse_builder{SecurityLog: securityLogProto(l)}.Build(), nil
}

// ListSecurityLogs implements LogService.ListSecurityLogs (lago
// SecurityLogsQuery): an upper bound on logged_at is required.
func (s *Logs) ListSecurityLogs(ctx context.Context, req *logv1.ListSecurityLogsRequest) (*logv1.ListSecurityLogsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := securityLogsFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListSecurityLogs(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return logv1.ListSecurityLogsResponse_builder{
		SecurityLogs:  Items(page, securityLogProto),
		NextPageToken: &page.NextToken,
	}.Build(), nil
}

// logFilter splits a filter into its bounds on logged_at (`>=` and `<=`,
// joined by AND at the top level) and the equalities on allowed.
func logFilter(s string, allowed ...string) (map[string][]string, auditlog.Range, error) {
	var rg auditlog.Range
	e, err := ParseFilter(s)
	if err != nil || e == nil {
		return map[string][]string{}, rg, err
	}
	terms := []FilterExpr{e}
	if and, ok := e.(FilterAnd); ok {
		terms = and
	}
	rest := make(FilterAnd, 0, len(terms))
	for _, t := range terms {
		r, ok := t.(Restriction)
		if !ok || r.Field != "logged_at" {
			rest = append(rest, t)
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, r.Value)
		if err != nil {
			return nil, rg, filterError(fmt.Sprintf("field \"logged_at\": invalid RFC 3339 time %q", r.Value))
		}
		at = at.UTC()
		var dst **time.Time
		switch r.Op {
		case CmpGe:
			dst = &rg.From
		case CmpLe:
			dst = &rg.To
		default:
			return nil, rg, filterError("field \"logged_at\" is compared with >= or <=")
		}
		if *dst != nil {
			return nil, rg, filterError("field \"logged_at\" is bounded twice on one side")
		}
		*dst = &at
	}
	var rem FilterExpr
	switch len(rest) {
	case 0:
	case 1:
		rem = rest[0]
	default:
		rem = rest
	}
	eq, err := Equalities(rem, allowed...)
	return eq, rg, err
}

func activityLogsQuery(s string) (auth.ActivityLogQuery, error) {
	var q auth.ActivityLogQuery
	eq, rg, err := logFilter(s, "activity_id", "activity_type", "activity_source", "user_email", "api_key_id",
		"resource_id", "resource_type", "external_customer_id", "external_subscription_id")
	if err != nil {
		return q, err
	}
	f := &q.ActivityLogFilter
	f.LoggedAt = rg
	if f.IDs, err = filterUUIDs("activity_id", eq["activity_id"]); err != nil {
		return q, err
	}
	if f.ResourceIDs, err = filterUUIDs("resource_id", eq["resource_id"]); err != nil {
		return q, err
	}
	if f.APIKeyIDs, err = filterIDs[core.APIKeyID]("api_key_id", eq["api_key_id"]); err != nil {
		return q, err
	}
	sources, err := FilterEnum[logv1.ActivitySource]("activity_source", eq["activity_source"], logv1.ActivitySource(0).Descriptor())
	if err != nil {
		return q, err
	}
	for _, src := range sources {
		f.Sources = append(f.Sources, activitySources.Core(src))
	}
	f.Types, f.ResourceTypes, q.UserEmails = eq["activity_type"], eq["resource_type"], eq["user_email"]
	if f.ExternalCustomerID, err = FilterSingle("external_customer_id", eq["external_customer_id"]); err != nil {
		return q, err
	}
	if f.ExternalSubscriptionID, err = FilterSingle("external_subscription_id", eq["external_subscription_id"]); err != nil {
		return q, err
	}
	return q, nil
}

func apiLogsFilter(s string) (auditlog.APILogFilter, error) {
	var f auditlog.APILogFilter
	eq, rg, err := logFilter(s, "request_id", "api_key_id", "http_method", "http_status", "request_path", "client", "api_version")
	if err != nil {
		return f, err
	}
	f.LoggedAt = rg
	if f.IDs, err = filterUUIDs("request_id", eq["request_id"]); err != nil {
		return f, err
	}
	if f.APIKeyIDs, err = filterIDs[core.APIKeyID]("api_key_id", eq["api_key_id"]); err != nil {
		return f, err
	}
	for _, v := range eq["http_status"] {
		switch v {
		case "succeeded":
			f.Succeeded = true
		case "failed":
			f.Failed = true
		default:
			code, err := strconv.Atoi(v)
			if err != nil || code < 100 || code > 599 {
				return f, filterError(fmt.Sprintf("field \"http_status\": %q is neither a status code nor succeeded or failed", v))
			}
			f.HTTPStatuses = append(f.HTTPStatuses, code)
		}
	}
	for _, m := range eq["http_method"] {
		f.HTTPMethods = append(f.HTTPMethods, strings.ToLower(m))
	}
	f.RequestPaths, f.Clients = eq["request_path"], eq["client"]
	if f.APIVersion, err = FilterSingle("api_version", eq["api_version"]); err != nil {
		return f, err
	}
	return f, nil
}

func securityLogsFilter(s string) (auditlog.SecurityLogFilter, error) {
	var f auditlog.SecurityLogFilter
	eq, rg, err := logFilter(s, "user_id", "api_key_id", "log_type", "log_event")
	if err != nil {
		return f, err
	}
	if rg.To == nil {
		// lago app/queries/security_logs_query.rb:17 (to_date value_is_mandatory).
		return f, InvalidArgument("invalid filter", []FieldViolation{{Field: FilterField, Reason: core.CodeRequired, Description: "an upper bound on logged_at (`logged_at <= …`) is required"}})
	}
	f.LoggedAt = rg
	if f.UserIDs, err = filterIDs[core.UserID]("user_id", eq["user_id"]); err != nil {
		return f, err
	}
	if f.APIKeyIDs, err = filterIDs[core.APIKeyID]("api_key_id", eq["api_key_id"]); err != nil {
		return f, err
	}
	f.Types, f.Events = eq["log_type"], eq["log_event"]
	return f, nil
}

func filterIDs[I core.Identifier[I]](field string, values []string) ([]I, error) {
	out := make([]I, 0, len(values))
	for _, v := range values {
		id, err := core.ParseID[I](v)
		if err != nil {
			return nil, filterError(fmt.Sprintf("field %q: %q is not an ID", field, v))
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func filterUUIDs(field string, values []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(values))
	for _, v := range values {
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, filterError(fmt.Sprintf("field %q: %q is not an ID", field, v))
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseUUID parses the UUID of a request field.
func parseUUID(field, s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, InvalidArgument("invalid ID", []FieldViolation{{Field: field, Reason: core.CodeInvalid, Description: "must be an ID"}})
	}
	return id, nil
}

func activityLogProto(l auth.ActivityLogView) *logv1.ActivityLog {
	b := logv1.ActivityLog_builder{
		ActivityId:             new(l.ID.String()),
		ActivityType:           &l.Type,
		ActivitySource:         new(activitySources.Proto(l.Source)),
		ActivityObject:         jsonStruct(l.Object),
		ActivityObjectChanges:  jsonStruct(l.ObjectChanges),
		UserEmail:              StringPtr(l.UserEmail),
		ApiKeyId:               idString(l.APIKeyID),
		ResourceType:           &l.ResourceType,
		ExternalCustomerId:     StringPtr(l.ExternalCustomerID),
		ExternalSubscriptionId: StringPtr(l.ExternalSubscriptionID),
		LoggedAt:               Timestamp(l.LoggedAt),
	}
	if l.ResourceID != uuid.Nil {
		b.ResourceId = new(l.ResourceID.String())
	}
	return b.Build()
}

func apiLogProto(l *auditlog.APILog) *logv1.ApiLog {
	return logv1.ApiLog_builder{
		RequestId:       new(l.ID.String()),
		ApiKeyId:        idString(l.APIKeyID),
		Client:          StringPtr(l.Client),
		HttpMethod:      &l.HTTPMethod,
		HttpStatus:      new(int32(l.HTTPStatus)), //nolint:gosec // an HTTP status code
		RequestOrigin:   StringPtr(l.RequestOrigin),
		RequestPath:     &l.RequestPath,
		RequestBody:     jsonStruct(l.RequestBody),
		RequestResponse: jsonStruct(l.Response),
		ApiVersion:      StringPtr(l.APIVersion),
		LoggedAt:        Timestamp(l.LoggedAt),
	}.Build()
}

func securityLogProto(l auth.SecurityLogView) *logv1.SecurityLog {
	return logv1.SecurityLog_builder{
		LogId:      new(l.ID.String()),
		LogType:    &l.Type,
		LogEvent:   &l.Event,
		UserEmail:  StringPtr(l.UserEmail),
		Resources:  jsonStruct(l.Resources),
		DeviceInfo: jsonStruct(l.DeviceInfo),
		LoggedAt:   Timestamp(l.LoggedAt),
		CreatedAt:  Timestamp(l.CreatedAt),
	}.Build()
}

// idString returns the string of a non-zero ID, nil for the zero one.
func idString[I core.Identifier[I]](id I) *string {
	if id.UUID() == uuid.Nil {
		return nil
	}
	return new(id.UUID().String())
}

// jsonStruct converts a JSON document to a Struct (nil for nil), through
// its JSON text when it holds values Struct does not take directly.
func jsonStruct(m map[string]any) *structpb.Struct {
	if m == nil {
		return nil
	}
	if s, err := structpb.NewStruct(m); err == nil {
		return s
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var s structpb.Struct
	if err := protojson.Unmarshal(b, &s); err != nil {
		return nil
	}
	return &s
}
