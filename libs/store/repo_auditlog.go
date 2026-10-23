package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindAuditLog binds the audit log repositories.
func (t *txState) bindAuditLog(r *port.Repos) {
	r.SecurityLogs = securityLogRepo{t}
	r.APILogs = apiLogRepo{t}
	r.ActivityLogs = activityLogRepo{t}
}

// logPageArgs returns the cursor and row limit of an audit log list.
func logPageArgs(p core.PageRequest) (afterAt *time.Time, after *uuid.UUID, lim int32, err error) {
	lim = int32(p.Limit()) + 1 // Limit is at most core.MaxPageSize
	if p.Token == "" {
		return nil, nil, lim, nil
	}
	c, err := core.DecodeCursor(p.Token)
	if err != nil {
		return nil, nil, 0, err
	}
	at, err := time.Parse(time.RFC3339Nano, c.Key)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	return new(at.UTC()), &c.ID, lim, nil
}

// logToken returns the page token after the entry logged at at with id.
func logToken(at time.Time, id uuid.UUID) string {
	return core.Cursor{Key: at.UTC().Format(time.RFC3339Nano), ID: id}.Encode()
}

// encodeMap encodes m for a nullable jsonb column (nil to NULL).
func encodeMap(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return encodeJSON(m)
}

// decodeMap decodes a nullable jsonb column.
func decodeMap(b []byte) (map[string]any, error) {
	var m map[string]any
	if err := decodeJSON(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// globLike returns the LIKE pattern of a path pattern where "*" matches
// any run of characters.
func globLike(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`, `*`, `%`)
	return r.Replace(p)
}

// securityLogRepo implements auditlog.SecurityLogRepository.
type securityLogRepo struct{ t *txState }

var _ auditlog.SecurityLogRepository = securityLogRepo{}

func (r securityLogRepo) Create(ctx context.Context, l *auditlog.SecurityLog) error {
	q, err := r.t.queries(ctx, l.OrganizationID)
	if err != nil {
		return err
	}
	res, err := encodeMap(l.Resources)
	if err != nil {
		return err
	}
	dev, err := encodeMap(l.DeviceInfo)
	if err != nil {
		return err
	}
	if err := q.CreateSecurityLog(ctx, db.CreateSecurityLogParams{
		OrganizationID: l.OrganizationID.UUID(), ID: l.ID, UserID: optUUID(l.UserID), ApiKeyID: optUUID(l.APIKeyID),
		LogType: l.Type, LogEvent: l.Event, Resources: res, DeviceInfo: dev, LoggedAt: utc(l.LoggedAt), CreatedAt: utc(l.CreatedAt),
	}); err != nil {
		return fmt.Errorf("create security log: %w", mapErr(err))
	}
	return nil
}

func (r securityLogRepo) Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.SecurityLog, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetSecurityLog(ctx, db.GetSecurityLogParams{OrganizationID: org.UUID(), ID: id})
	if err != nil {
		return nil, fmt.Errorf("get security log: %w", mapErr(err))
	}
	return securityLogOf(row)
}

func (r securityLogRepo) List(ctx context.Context, org core.OrganizationID, f auditlog.SecurityLogFilter, page core.PageRequest) (core.Page[*auditlog.SecurityLog], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*auditlog.SecurityLog]{}, err
	}
	afterAt, after, lim, err := logPageArgs(page)
	if err != nil {
		return core.Page[*auditlog.SecurityLog]{}, err
	}
	rows, err := q.ListSecurityLogs(ctx, db.ListSecurityLogsParams{
		OrganizationID: org.UUID(), FromAt: optUTC(f.LoggedAt.From), ToAt: optUTC(f.LoggedAt.To),
		UserIds: uuids(f.UserIDs), ApiKeyIds: uuids(f.APIKeyIDs), LogTypes: nonNil(f.Types), LogEvents: nonNil(f.Events),
		After: after, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*auditlog.SecurityLog]{}, fmt.Errorf("list security logs: %w", mapErr(err))
	}
	return pageOf(rows, page, securityLogOf, func(l *auditlog.SecurityLog) string { return logToken(l.LoggedAt, l.ID) })
}

func securityLogOf(row db.SecurityLog) (*auditlog.SecurityLog, error) {
	res, err := decodeMap(row.Resources)
	if err != nil {
		return nil, err
	}
	dev, err := decodeMap(row.DeviceInfo)
	if err != nil {
		return nil, err
	}
	return &auditlog.SecurityLog{
		ID: row.ID, OrganizationID: idOf[core.OrganizationID](row.OrganizationID),
		UserID: optIDOf[core.UserID](row.UserID), APIKeyID: optIDOf[core.APIKeyID](row.ApiKeyID),
		Type: row.LogType, Event: row.LogEvent, Resources: res, DeviceInfo: dev,
		LoggedAt: utc(row.LoggedAt), CreatedAt: utc(row.CreatedAt),
	}, nil
}

// apiLogRepo implements auditlog.APILogRepository.
type apiLogRepo struct{ t *txState }

var _ auditlog.APILogRepository = apiLogRepo{}

func (r apiLogRepo) Create(ctx context.Context, l *auditlog.APILog) error {
	q, err := r.t.queries(ctx, l.OrganizationID)
	if err != nil {
		return err
	}
	body, err := encodeMap(l.RequestBody)
	if err != nil {
		return err
	}
	resp, err := encodeMap(l.Response)
	if err != nil {
		return err
	}
	if err := q.CreateApiLog(ctx, db.CreateApiLogParams{
		OrganizationID: l.OrganizationID.UUID(), ID: l.ID, ApiKeyID: l.APIKeyID.UUID(), Client: l.Client,
		HttpMethod: l.HTTPMethod, HttpStatus: int32(l.HTTPStatus), //nolint:gosec // an HTTP status code
		RequestOrigin: l.RequestOrigin, RequestPath: l.RequestPath, RequestBody: body, RequestResponse: resp,
		ApiVersion: l.APIVersion, LoggedAt: utc(l.LoggedAt), CreatedAt: utc(l.CreatedAt),
	}); err != nil {
		return fmt.Errorf("create api log: %w", mapErr(err))
	}
	return nil
}

func (r apiLogRepo) Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.APILog, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetApiLog(ctx, db.GetApiLogParams{OrganizationID: org.UUID(), ID: id})
	if err != nil {
		return nil, fmt.Errorf("get api log: %w", mapErr(err))
	}
	return apiLogOf(row)
}

func (r apiLogRepo) List(ctx context.Context, org core.OrganizationID, f auditlog.APILogFilter, page core.PageRequest) (core.Page[*auditlog.APILog], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*auditlog.APILog]{}, err
	}
	afterAt, after, lim, err := logPageArgs(page)
	if err != nil {
		return core.Page[*auditlog.APILog]{}, err
	}
	statuses := make([]int32, len(f.HTTPStatuses))
	for i, s := range f.HTTPStatuses {
		statuses[i] = int32(s) //nolint:gosec // an HTTP status code
	}
	paths := make([]string, len(f.RequestPaths))
	for i, p := range f.RequestPaths {
		paths[i] = globLike(p)
	}
	rows, err := q.ListApiLogs(ctx, db.ListApiLogsParams{
		OrganizationID: org.UUID(), FromAt: optUTC(f.LoggedAt.From), ToAt: optUTC(f.LoggedAt.To),
		Ids: nonNil(f.IDs), ApiKeyIds: uuids(f.APIKeyIDs), HttpMethods: nonNil(f.HTTPMethods),
		Succeeded: f.Succeeded, Failed: f.Failed, HttpStatuses: statuses, ApiVersion: optString(f.APIVersion),
		RequestPaths: paths, Clients: nonNil(f.Clients), After: after, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*auditlog.APILog]{}, fmt.Errorf("list api logs: %w", mapErr(err))
	}
	return pageOf(rows, page, apiLogOf, func(l *auditlog.APILog) string { return logToken(l.LoggedAt, l.ID) })
}

func apiLogOf(row db.ApiLog) (*auditlog.APILog, error) {
	body, err := decodeMap(row.RequestBody)
	if err != nil {
		return nil, err
	}
	resp, err := decodeMap(row.RequestResponse)
	if err != nil {
		return nil, err
	}
	return &auditlog.APILog{
		ID: row.ID, OrganizationID: idOf[core.OrganizationID](row.OrganizationID), APIKeyID: idOf[core.APIKeyID](row.ApiKeyID),
		Client: row.Client, HTTPMethod: row.HttpMethod, HTTPStatus: int(row.HttpStatus), RequestOrigin: row.RequestOrigin,
		RequestPath: row.RequestPath, RequestBody: body, Response: resp, APIVersion: row.ApiVersion,
		LoggedAt: utc(row.LoggedAt), CreatedAt: utc(row.CreatedAt),
	}, nil
}

// activityLogRepo implements auditlog.ActivityLogRepository.
type activityLogRepo struct{ t *txState }

var _ auditlog.ActivityLogRepository = activityLogRepo{}

func (r activityLogRepo) Create(ctx context.Context, l *auditlog.ActivityLog) error {
	q, err := r.t.queries(ctx, l.OrganizationID)
	if err != nil {
		return err
	}
	obj, err := encodeMap(l.Object)
	if err != nil {
		return err
	}
	changes, err := encodeMap(l.ObjectChanges)
	if err != nil {
		return err
	}
	if err := q.CreateActivityLog(ctx, db.CreateActivityLogParams{
		OrganizationID: l.OrganizationID.UUID(), ID: l.ID, UserID: optUUID(l.UserID), ApiKeyID: optUUID(l.APIKeyID),
		ActivityType: l.Type, ActivitySource: string(l.Source), ActivityObject: obj, ActivityObjectChanges: changes,
		ResourceID: l.ResourceID, ResourceType: l.ResourceType,
		ExternalCustomerID: optString(l.ExternalCustomerID), ExternalSubscriptionID: optString(l.ExternalSubscriptionID),
		LoggedAt: utc(l.LoggedAt), CreatedAt: utc(l.CreatedAt),
	}); err != nil {
		return fmt.Errorf("create activity log: %w", mapErr(err))
	}
	return nil
}

func (r activityLogRepo) Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.ActivityLog, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetActivityLog(ctx, db.GetActivityLogParams{OrganizationID: org.UUID(), ID: id})
	if err != nil {
		return nil, fmt.Errorf("get activity log: %w", mapErr(err))
	}
	return activityLogOf(row)
}

func (r activityLogRepo) List(ctx context.Context, org core.OrganizationID, f auditlog.ActivityLogFilter, page core.PageRequest) (core.Page[*auditlog.ActivityLog], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*auditlog.ActivityLog]{}, err
	}
	afterAt, after, lim, err := logPageArgs(page)
	if err != nil {
		return core.Page[*auditlog.ActivityLog]{}, err
	}
	sources := make([]string, len(f.Sources))
	for i, s := range f.Sources {
		sources[i] = string(s)
	}
	rows, err := q.ListActivityLogs(ctx, db.ListActivityLogsParams{
		OrganizationID: org.UUID(), FromAt: optUTC(f.LoggedAt.From), ToAt: optUTC(f.LoggedAt.To),
		Ids: nonNil(f.IDs), ActivityTypes: nonNil(f.Types), ActivitySources: sources,
		UserIds: uuids(f.UserIDs), ApiKeyIds: uuids(f.APIKeyIDs), ResourceIds: nonNil(f.ResourceIDs), ResourceTypes: nonNil(f.ResourceTypes),
		ExternalCustomerID: optString(f.ExternalCustomerID), ExternalSubscriptionID: optString(f.ExternalSubscriptionID),
		After: after, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*auditlog.ActivityLog]{}, fmt.Errorf("list activity logs: %w", mapErr(err))
	}
	return pageOf(rows, page, activityLogOf, func(l *auditlog.ActivityLog) string { return logToken(l.LoggedAt, l.ID) })
}

func activityLogOf(row db.ActivityLog) (*auditlog.ActivityLog, error) {
	obj, err := decodeMap(row.ActivityObject)
	if err != nil {
		return nil, err
	}
	changes, err := decodeMap(row.ActivityObjectChanges)
	if err != nil {
		return nil, err
	}
	return &auditlog.ActivityLog{
		ID: row.ID, OrganizationID: idOf[core.OrganizationID](row.OrganizationID),
		UserID: optIDOf[core.UserID](row.UserID), APIKeyID: optIDOf[core.APIKeyID](row.ApiKeyID),
		Type: row.ActivityType, Source: auditlog.ActivitySource(row.ActivitySource), Object: obj, ObjectChanges: changes,
		ResourceID: row.ResourceID, ResourceType: row.ResourceType,
		ExternalCustomerID: deref(row.ExternalCustomerID), ExternalSubscriptionID: deref(row.ExternalSubscriptionID),
		LoggedAt: utc(row.LoggedAt), CreatedAt: utc(row.CreatedAt),
	}, nil
}
