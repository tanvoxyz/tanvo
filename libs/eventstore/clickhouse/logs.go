package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
)

// LogStore is the ClickHouse log archive ([auditlog.Archive]): the
// security_logs, api_logs and activity_logs tables of lago's
// db/clickhouse_migrate, read as lago's SecurityLogsQuery, ApiLogsQuery
// and ActivityLogsQuery (app/queries/), in the order and with the page
// tokens of the PostgreSQL log tables, so that a list merges both. It is
// safe for concurrent use.
type LogStore struct{ conn driver.Conn }

var _ auditlog.Archive = (*LogStore)(nil)

// NewLogStore returns the log store over conn, whose schema [Migrate]
// applied.
func NewLogStore(conn driver.Conn) *LogStore { return &LogStore{conn: settingsConn{Conn: conn}} }

const (
	securityLogColumns = `organization_id, log_id, user_id, api_key_id, log_type, log_event, device_info, resources, logged_at, created_at`
	apiLogColumns      = `organization_id, request_id, api_key_id, api_version, client, request_body, request_response, request_path, request_origin, http_method, http_status, logged_at, created_at`
	activityLogColumns = `organization_id, activity_id, user_id, api_key_id, external_customer_id, external_subscription_id, activity_type, activity_source, activity_object, activity_object_changes, resource_id, resource_type, logged_at, created_at`
)

// Write implements [auditlog.Archive]: it inserts the logs of b, one
// statement per table. Writing a log again stores no second copy.
func (s *LogStore) Write(ctx context.Context, b auditlog.Batch) error {
	ctx = consistent(ctx)
	if err := insertRows(ctx, s.conn, "security_logs", securityLogColumns, b.Security, func(l *auditlog.SecurityLog) ([]any, error) {
		dev, err := encodeLogMap(l.DeviceInfo)
		if err != nil {
			return nil, err
		}
		res, err := encodeLogMap(l.Resources)
		if err != nil {
			return nil, err
		}
		return []any{
			l.OrganizationID.String(), l.ID.String(), optID(l.UserID), optID(l.APIKeyID), l.Type, l.Event, dev, res,
			l.LoggedAt.UTC(), l.CreatedAt.UTC(),
		}, nil
	}); err != nil {
		return err
	}
	if err := insertRows(ctx, s.conn, "api_logs", apiLogColumns, b.API, func(l *auditlog.APILog) ([]any, error) {
		body, err := encodeLogMap(l.RequestBody)
		if err != nil {
			return nil, err
		}
		resp, err := encodeLogMap(l.Response)
		if err != nil {
			return nil, err
		}
		return []any{
			l.OrganizationID.String(), l.ID.String(), l.APIKeyID.String(), l.APIVersion, l.Client, body, resp,
			l.RequestPath, l.RequestOrigin, l.HTTPMethod, int32(l.HTTPStatus), //nolint:gosec // an HTTP status code
			l.LoggedAt.UTC(), l.CreatedAt.UTC(),
		}, nil
	}); err != nil {
		return err
	}
	return insertRows(ctx, s.conn, "activity_logs", activityLogColumns, b.Activity, func(l *auditlog.ActivityLog) ([]any, error) {
		obj, err := encodeLogMap(l.Object)
		if err != nil {
			return nil, err
		}
		changes, err := encodeLogMap(l.ObjectChanges)
		if err != nil {
			return nil, err
		}
		return []any{
			l.OrganizationID.String(), l.ID.String(), optID(l.UserID), optID(l.APIKeyID),
			optText(l.ExternalCustomerID), optText(l.ExternalSubscriptionID), l.Type, string(l.Source), obj, changes,
			l.ResourceID.String(), l.ResourceType, l.LoggedAt.UTC(), l.CreatedAt.UTC(),
		}, nil
	})
}

func insertRows[T any](ctx context.Context, conn driver.Conn, table, columns string, logs []T, row func(T) ([]any, error)) error {
	if len(logs) == 0 {
		return nil
	}
	b, err := conn.PrepareBatch(ctx, "INSERT INTO "+table+" ("+columns+")")
	if err != nil {
		return fmt.Errorf("eventstore: clickhouse: insert %s: %w", table, err)
	}
	for _, l := range logs {
		vals, err := row(l)
		if err == nil {
			err = b.Append(vals...)
		}
		if err != nil {
			_ = b.Abort()
			return fmt.Errorf("eventstore: clickhouse: insert %s: %w", table, err)
		}
	}
	if err := b.Send(); err != nil {
		return fmt.Errorf("eventstore: clickhouse: insert %s: %w", table, err)
	}
	return nil
}

// SecurityLogs implements [auditlog.Archive].
func (s *LogStore) SecurityLogs() auditlog.SecurityLogReader { return securityLogs{s} }

// APILogs implements [auditlog.Archive].
func (s *LogStore) APILogs() auditlog.APILogReader { return apiLogs{s} }

// ActivityLogs implements [auditlog.Archive].
func (s *LogStore) ActivityLogs() auditlog.ActivityLogReader { return activityLogs{s} }

// logQuery builds the statement of a log list or lookup.
type logQuery struct {
	table, id string
	args      args
	where     []string
}

func newLogQuery(table, id string, org core.OrganizationID) *logQuery {
	q := &logQuery{table: table, id: id}
	q.where = append(q.where, "organization_id = "+q.args.add(org.String()))
	return q
}

func (q *logQuery) cond(c string) { q.where = append(q.where, c) }

// in keeps the rows whose column is one of vals; no values do not filter.
func (q *logQuery) in(column string, vals []string) {
	if len(vals) > 0 {
		q.cond(column + " IN " + q.args.add(vals))
	}
}

func (q *logQuery) eq(column, v string) {
	if v != "" {
		q.cond(column + " = " + q.args.add(v))
	}
}

func (q *logQuery) logged(r auditlog.Range) {
	if r.From != nil {
		q.cond("logged_at >= " + q.args.time(*r.From))
	}
	if r.To != nil {
		q.cond("logged_at <= " + q.args.time(*r.To))
	}
}

// page bounds the list after the page token and returns the row limit.
func (q *logQuery) page(p core.PageRequest) (int, error) {
	if p.Token != "" {
		at, id, err := auditlog.ParsePageToken(p.Token)
		if err != nil {
			return 0, err
		}
		t := q.args.time(at)
		q.cond("(logged_at < " + t + " OR (logged_at = " + t + " AND " + q.id + " > " + q.args.add(id.String()) + "))")
	}
	return p.Limit() + 1, nil
}

func (q *logQuery) sql(columns string, limit int) string {
	return "SELECT " + columns + " FROM " + q.table + " FINAL WHERE " + strings.Join(q.where, " AND ") +
		" ORDER BY logged_at DESC, " + q.id + " LIMIT " + strconv.Itoa(limit)
}

// list runs q and returns a page of its rows.
func list[T any](ctx context.Context, s *LogStore, q *logQuery, columns string, page core.PageRequest, scan func(driver.Rows) (T, error), key func(T) (time.Time, uuid.UUID)) (core.Page[T], error) {
	lim, err := q.page(page)
	if err != nil {
		return core.Page[T]{}, err
	}
	items, err := query(ctx, s, q.sql(columns, lim), q.args, scan)
	if err != nil {
		return core.Page[T]{}, err
	}
	out := core.Page[T]{Items: items}
	if n := page.Limit(); len(items) > n {
		out.Items = items[:n]
		out.NextToken = auditlog.PageToken(key(items[n-1]))
	}
	return out, nil
}

// get returns the row of id, or [core.ErrNotFound].
func get[T any](ctx context.Context, s *LogStore, q *logQuery, columns string, id uuid.UUID, scan func(driver.Rows) (T, error)) (T, error) {
	q.cond(q.id + " = " + q.args.add(id.String()))
	items, err := query(ctx, s, q.sql(columns, 1), q.args, scan)
	if err != nil {
		var zero T
		return zero, err
	}
	if len(items) == 0 {
		var zero T
		return zero, fmt.Errorf("%s %s: %w", strings.TrimSuffix(q.table, "s"), id, core.ErrNotFound)
	}
	return items[0], nil
}

func query[T any](ctx context.Context, s *LogStore, sql string, a args, scan func(driver.Rows) (T, error)) ([]T, error) {
	rows, err := s.conn.Query(consistent(ctx), sql, a...)
	if err != nil {
		return nil, fmt.Errorf("eventstore: clickhouse: query logs: %w", err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("eventstore: clickhouse: read logs: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventstore: clickhouse: read logs: %w", err)
	}
	return out, nil
}

type securityLogs struct{ s *LogStore }

func (r securityLogs) Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.SecurityLog, error) {
	return get(ctx, r.s, newLogQuery("security_logs", "log_id", org), securityLogColumns, id, scanSecurityLog)
}

func (r securityLogs) List(ctx context.Context, org core.OrganizationID, f auditlog.SecurityLogFilter, page core.PageRequest) (core.Page[*auditlog.SecurityLog], error) {
	q := newLogQuery("security_logs", "log_id", org)
	q.logged(f.LoggedAt)
	q.in("user_id", ids(f.UserIDs))
	q.in("api_key_id", ids(f.APIKeyIDs))
	q.in("log_type", f.Types)
	q.in("log_event", f.Events)
	return list(ctx, r.s, q, securityLogColumns, page, scanSecurityLog, func(l *auditlog.SecurityLog) (time.Time, uuid.UUID) { return l.LoggedAt, l.ID })
}

func scanSecurityLog(rows driver.Rows) (*auditlog.SecurityLog, error) {
	var (
		org, id, typ, event string
		user, key           *string
		dev, res            map[string]string
		l                   auditlog.SecurityLog
	)
	if err := rows.Scan(&org, &id, &user, &key, &typ, &event, &dev, &res, &l.LoggedAt, &l.CreatedAt); err != nil {
		return nil, err
	}
	var err error
	l.Type, l.Event = typ, event
	l.LoggedAt, l.CreatedAt = l.LoggedAt.UTC(), l.CreatedAt.UTC()
	if l.OrganizationID, err = core.ParseID[core.OrganizationID](org); err != nil {
		return nil, err
	}
	if l.ID, err = uuid.Parse(id); err != nil {
		return nil, err
	}
	if l.UserID, err = parseOptID[core.UserID](user); err != nil {
		return nil, err
	}
	if l.APIKeyID, err = parseOptID[core.APIKeyID](key); err != nil {
		return nil, err
	}
	if l.DeviceInfo, err = decodeLogMap(dev); err != nil {
		return nil, err
	}
	if l.Resources, err = decodeLogMap(res); err != nil {
		return nil, err
	}
	return &l, nil
}

type apiLogs struct{ s *LogStore }

func (r apiLogs) Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.APILog, error) {
	return get(ctx, r.s, newLogQuery("api_logs", "request_id", org), apiLogColumns, id, scanAPILog)
}

// List keeps the statuses up to 399 for Succeeded and from 400 for Failed,
// which take precedence over HTTPStatuses, and request paths matching a
// pattern where "*" matches any run of characters (lago
// app/queries/api_logs_query.rb with_http_statuses, with_request_paths).
func (r apiLogs) List(ctx context.Context, org core.OrganizationID, f auditlog.APILogFilter, page core.PageRequest) (core.Page[*auditlog.APILog], error) {
	q := newLogQuery("api_logs", "request_id", org)
	q.logged(f.LoggedAt)
	q.in("request_id", uuidStrings(f.IDs))
	q.in("api_key_id", ids(f.APIKeyIDs))
	q.in("toString(http_method)", f.HTTPMethods)
	switch {
	case f.Succeeded || f.Failed:
		if f.Succeeded {
			q.cond("http_status <= 399")
		}
		if f.Failed {
			q.cond("http_status > 399")
		}
	case len(f.HTTPStatuses) > 0:
		st := make([]int32, len(f.HTTPStatuses))
		for i, v := range f.HTTPStatuses {
			st[i] = int32(v) //nolint:gosec // an HTTP status code
		}
		q.cond("http_status IN " + q.args.add(st))
	}
	q.eq("api_version", f.APIVersion)
	if len(f.RequestPaths) > 0 {
		likes := make([]string, len(f.RequestPaths))
		for i, p := range f.RequestPaths {
			likes[i] = "request_path LIKE " + q.args.add(globLike(p))
		}
		q.cond("(" + strings.Join(likes, " OR ") + ")")
	}
	q.in("client", f.Clients)
	return list(ctx, r.s, q, apiLogColumns, page, scanAPILog, func(l *auditlog.APILog) (time.Time, uuid.UUID) { return l.LoggedAt, l.ID })
}

func scanAPILog(rows driver.Rows) (*auditlog.APILog, error) {
	var (
		org, id, key string
		body, resp   map[string]string
		status       int32
		l            auditlog.APILog
	)
	if err := rows.Scan(&org, &id, &key, &l.APIVersion, &l.Client, &body, &resp, &l.RequestPath, &l.RequestOrigin, &l.HTTPMethod, &status, &l.LoggedAt, &l.CreatedAt); err != nil {
		return nil, err
	}
	var err error
	l.HTTPStatus = int(status)
	l.LoggedAt, l.CreatedAt = l.LoggedAt.UTC(), l.CreatedAt.UTC()
	if l.OrganizationID, err = core.ParseID[core.OrganizationID](org); err != nil {
		return nil, err
	}
	if l.ID, err = uuid.Parse(id); err != nil {
		return nil, err
	}
	if l.APIKeyID, err = core.ParseID[core.APIKeyID](key); err != nil {
		return nil, err
	}
	if l.RequestBody, err = decodeLogMap(body); err != nil {
		return nil, err
	}
	if l.Response, err = decodeLogMap(resp); err != nil {
		return nil, err
	}
	return &l, nil
}

type activityLogs struct{ s *LogStore }

func (r activityLogs) Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.ActivityLog, error) {
	return get(ctx, r.s, newLogQuery("activity_logs", "activity_id", org), activityLogColumns, id, scanActivityLog)
}

func (r activityLogs) List(ctx context.Context, org core.OrganizationID, f auditlog.ActivityLogFilter, page core.PageRequest) (core.Page[*auditlog.ActivityLog], error) {
	q := newLogQuery("activity_logs", "activity_id", org)
	q.logged(f.LoggedAt)
	q.in("activity_id", uuidStrings(f.IDs))
	q.in("activity_type", f.Types)
	sources := make([]string, len(f.Sources))
	for i, v := range f.Sources {
		sources[i] = string(v)
	}
	q.in("toString(activity_source)", sources)
	q.in("user_id", ids(f.UserIDs))
	q.in("api_key_id", ids(f.APIKeyIDs))
	q.in("resource_id", uuidStrings(f.ResourceIDs))
	q.in("resource_type", f.ResourceTypes)
	q.eq("external_customer_id", f.ExternalCustomerID)
	q.eq("external_subscription_id", f.ExternalSubscriptionID)
	return list(ctx, r.s, q, activityLogColumns, page, scanActivityLog, func(l *auditlog.ActivityLog) (time.Time, uuid.UUID) { return l.LoggedAt, l.ID })
}

func scanActivityLog(rows driver.Rows) (*auditlog.ActivityLog, error) {
	var (
		org, id, source, resource string
		user, key, cust, sub      *string
		obj, changes              map[string]string
		l                         auditlog.ActivityLog
	)
	if err := rows.Scan(&org, &id, &user, &key, &cust, &sub, &l.Type, &source, &obj, &changes, &resource, &l.ResourceType, &l.LoggedAt, &l.CreatedAt); err != nil {
		return nil, err
	}
	var err error
	l.Source = auditlog.ActivitySource(source)
	l.LoggedAt, l.CreatedAt = l.LoggedAt.UTC(), l.CreatedAt.UTC()
	if cust != nil {
		l.ExternalCustomerID = *cust
	}
	if sub != nil {
		l.ExternalSubscriptionID = *sub
	}
	if l.OrganizationID, err = core.ParseID[core.OrganizationID](org); err != nil {
		return nil, err
	}
	if l.ID, err = uuid.Parse(id); err != nil {
		return nil, err
	}
	if l.ResourceID, err = uuid.Parse(resource); err != nil {
		return nil, err
	}
	if l.UserID, err = parseOptID[core.UserID](user); err != nil {
		return nil, err
	}
	if l.APIKeyID, err = parseOptID[core.APIKeyID](key); err != nil {
		return nil, err
	}
	if l.Object, err = decodeLogMap(obj); err != nil {
		return nil, err
	}
	if l.ObjectChanges, err = decodeLogMap(changes); err != nil {
		return nil, err
	}
	return &l, nil
}

// encodeLogMap encodes m as a map column: each top-level key with its
// value in JSON. A nil map is an empty one.
func encodeLogMap(m map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for k, v := range m {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("eventstore: clickhouse: encode log %s: %w", k, err)
		}
		out[k] = string(b)
	}
	return out, nil
}

// decodeLogMap decodes a map column of [encodeLogMap]; an empty map is
// nil.
func decodeLogMap(m map[string]string) (map[string]any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		var val any
		if err := json.Unmarshal([]byte(v), &val); err != nil {
			return nil, fmt.Errorf("decode log %s: %w", k, err)
		}
		out[k] = val
	}
	return out, nil
}

// globLike returns the LIKE pattern of a path pattern where "*" matches
// any run of characters, the other characters literally.
func globLike(p string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`, `*`, `%`).Replace(p)
}

type idValue interface {
	IsZero() bool
	String() string
}

func optID[T idValue](id T) *string {
	if id.IsZero() {
		return nil
	}
	s := id.String()
	return &s
}

func optText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ids[T idValue](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = v.String()
	}
	return out
}

func uuidStrings(in []uuid.UUID) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = v.String()
	}
	return out
}

func parseOptID[T core.Identifier[T]](s *string) (T, error) {
	if s == nil || *s == "" {
		var zero T
		return zero, nil
	}
	return core.ParseID[T](*s)
}
