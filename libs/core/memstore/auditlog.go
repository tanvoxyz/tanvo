package memstore

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
)

// auditLogData holds the audit logs.
type auditLogData struct {
	security map[uuid.UUID]*auditlog.SecurityLog
	api      map[uuid.UUID]*auditlog.APILog
	activity map[uuid.UUID]*auditlog.ActivityLog
}

func newAuditLogData() *auditLogData {
	return &auditLogData{
		security: map[uuid.UUID]*auditlog.SecurityLog{},
		api:      map[uuid.UUID]*auditlog.APILog{},
		activity: map[uuid.UUID]*auditlog.ActivityLog{},
	}
}

// snapshot copies the maps; entries are immutable once stored.
func (d *auditLogData) snapshot() *auditLogData {
	return &auditLogData{security: maps.Clone(d.security), api: maps.Clone(d.api), activity: maps.Clone(d.activity)}
}

func cloneJSON(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

// securityLogRepo implements auditlog.SecurityLogRepository.
type securityLogRepo struct{ d *auditLogData }

var _ auditlog.SecurityLogRepository = securityLogRepo{}

func (r securityLogRepo) Create(_ context.Context, l *auditlog.SecurityLog) error {
	if _, ok := r.d.security[l.ID]; ok {
		return conflict("security_log", "id")
	}
	c := *l
	c.Resources, c.DeviceInfo = cloneJSON(l.Resources), cloneJSON(l.DeviceInfo)
	c.LoggedAt, c.CreatedAt = l.LoggedAt.UTC(), l.CreatedAt.UTC()
	r.d.security[l.ID] = &c
	return nil
}

func (r securityLogRepo) Get(_ context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.SecurityLog, error) {
	l, ok := r.d.security[id]
	if !ok || l.OrganizationID != org {
		return nil, notFound("security_log", id)
	}
	c := *l
	return &c, nil
}

func (r securityLogRepo) List(_ context.Context, org core.OrganizationID, f auditlog.SecurityLogFilter, page core.PageRequest) (core.Page[*auditlog.SecurityLog], error) {
	var rows []*auditlog.SecurityLog
	for _, l := range r.d.security {
		if l.OrganizationID != org || !f.LoggedAt.Contains(l.LoggedAt) ||
			!anyOf(f.UserIDs, l.UserID) || !anyOf(f.APIKeyIDs, l.APIKeyID) ||
			!anyOf(f.Types, l.Type) || !anyOf(f.Events, l.Event) {
			continue
		}
		c := *l
		rows = append(rows, &c)
	}
	return paginateRecent(rows, func(l *auditlog.SecurityLog) (time.Time, uuid.UUID) { return l.LoggedAt, l.ID }, page)
}

// apiLogRepo implements auditlog.APILogRepository.
type apiLogRepo struct{ d *auditLogData }

var _ auditlog.APILogRepository = apiLogRepo{}

func (r apiLogRepo) Create(_ context.Context, l *auditlog.APILog) error {
	if _, ok := r.d.api[l.ID]; ok {
		return nil
	}
	c := *l
	c.RequestBody, c.Response = cloneJSON(l.RequestBody), cloneJSON(l.Response)
	c.LoggedAt, c.CreatedAt = l.LoggedAt.UTC(), l.CreatedAt.UTC()
	r.d.api[l.ID] = &c
	return nil
}

func (r apiLogRepo) Get(_ context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.APILog, error) {
	l, ok := r.d.api[id]
	if !ok || l.OrganizationID != org {
		return nil, notFound("api_log", id)
	}
	c := *l
	return &c, nil
}

func (r apiLogRepo) List(_ context.Context, org core.OrganizationID, f auditlog.APILogFilter, page core.PageRequest) (core.Page[*auditlog.APILog], error) {
	var rows []*auditlog.APILog
	for _, l := range r.d.api {
		if l.OrganizationID != org || !f.LoggedAt.Contains(l.LoggedAt) ||
			!anyOf(f.IDs, l.ID) || !anyOf(f.APIKeyIDs, l.APIKeyID) ||
			!anyOf(f.HTTPMethods, l.HTTPMethod) || !anyOf(f.Clients, l.Client) ||
			(f.APIVersion != "" && l.APIVersion != f.APIVersion) ||
			!matchesStatus(f, l.HTTPStatus) || !matchesPaths(f.RequestPaths, l.RequestPath) {
			continue
		}
		c := *l
		rows = append(rows, &c)
	}
	return paginateRecent(rows, func(l *auditlog.APILog) (time.Time, uuid.UUID) { return l.LoggedAt, l.ID }, page)
}

// matchesStatus applies the status filter of lago ApiLogsQuery
// with_http_statuses: succeeded and failed take precedence over codes.
func matchesStatus(f auditlog.APILogFilter, status int) bool {
	if f.Succeeded || f.Failed {
		return (!f.Succeeded || status <= 399) && (!f.Failed || status > 399)
	}
	return anyOf(f.HTTPStatuses, status)
}

// matchesPaths reports whether path matches any pattern, "*" matching any
// run of characters.
func matchesPaths(patterns []string, path string) bool {
	if len(patterns) == 0 {
		return true
	}
	return slices.ContainsFunc(patterns, func(p string) bool { return globMatch(p, path) })
}

func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// activityLogRepo implements auditlog.ActivityLogRepository.
type activityLogRepo struct{ d *auditLogData }

var _ auditlog.ActivityLogRepository = activityLogRepo{}

func (r activityLogRepo) Create(_ context.Context, l *auditlog.ActivityLog) error {
	if _, ok := r.d.activity[l.ID]; ok {
		return conflict("activity_log", "id")
	}
	c := *l
	c.Object, c.ObjectChanges = cloneJSON(l.Object), cloneJSON(l.ObjectChanges)
	c.LoggedAt, c.CreatedAt = l.LoggedAt.UTC(), l.CreatedAt.UTC()
	r.d.activity[l.ID] = &c
	return nil
}

func (r activityLogRepo) Get(_ context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.ActivityLog, error) {
	l, ok := r.d.activity[id]
	if !ok || l.OrganizationID != org {
		return nil, notFound("activity_log", id)
	}
	c := *l
	return &c, nil
}

func (r activityLogRepo) List(_ context.Context, org core.OrganizationID, f auditlog.ActivityLogFilter, page core.PageRequest) (core.Page[*auditlog.ActivityLog], error) {
	var rows []*auditlog.ActivityLog
	for _, l := range r.d.activity {
		if l.OrganizationID != org || !f.LoggedAt.Contains(l.LoggedAt) ||
			!anyOf(f.IDs, l.ID) || !anyOf(f.Types, l.Type) || !anyOf(f.Sources, l.Source) ||
			!anyOf(f.UserIDs, l.UserID) || !anyOf(f.APIKeyIDs, l.APIKeyID) ||
			!anyOf(f.ResourceIDs, l.ResourceID) || !anyOf(f.ResourceTypes, l.ResourceType) ||
			(f.ExternalCustomerID != "" && l.ExternalCustomerID != f.ExternalCustomerID) ||
			(f.ExternalSubscriptionID != "" && l.ExternalSubscriptionID != f.ExternalSubscriptionID) {
			continue
		}
		c := *l
		rows = append(rows, &c)
	}
	return paginateRecent(rows, func(l *auditlog.ActivityLog) (time.Time, uuid.UUID) { return l.LoggedAt, l.ID }, page)
}

// anyOf reports whether vs is empty or holds v.
func anyOf[T comparable](vs []T, v T) bool { return len(vs) == 0 || slices.Contains(vs, v) }
