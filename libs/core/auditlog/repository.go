package auditlog

import (
	"context"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// SecurityLogRepository stores security logs. Lists are most recent first:
// by LoggedAt descending, then ID ascending, with a [core.Cursor] page
// token over (LoggedAt in RFC 3339, ID).
type SecurityLogRepository interface {
	// Create stores l.
	Create(ctx context.Context, l *SecurityLog) error
	// Get returns the entry id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*SecurityLog, error)
	// List returns the entries of org matching f.
	List(ctx context.Context, org core.OrganizationID, f SecurityLogFilter, page core.PageRequest) (core.Page[*SecurityLog], error)
}

// APILogRepository stores API logs, listed most recent first.
type APILogRepository interface {
	// Create stores l; when a log of l's request id exists, it keeps that
	// one and returns nil, so that a reused client request id cannot fail
	// a batch of logs.
	Create(ctx context.Context, l *APILog) error
	// Get returns the request id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*APILog, error)
	// List returns the requests of org matching f.
	List(ctx context.Context, org core.OrganizationID, f APILogFilter, page core.PageRequest) (core.Page[*APILog], error)
}

// ActivityLogRepository stores activity logs, listed most recent first.
type ActivityLogRepository interface {
	// Create stores l.
	Create(ctx context.Context, l *ActivityLog) error
	// Get returns the activity id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*ActivityLog, error)
	// List returns the activities of org matching f.
	List(ctx context.Context, org core.OrganizationID, f ActivityLogFilter, page core.PageRequest) (core.Page[*ActivityLog], error)
}
