package auditlog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// PageToken returns the page token of a log list after the entry logged at
// at with id. Every log store pages with it, so that a list can continue
// from one store to the other.
func PageToken(at time.Time, id uuid.UUID) string {
	return core.Cursor{Key: at.UTC().Format(time.RFC3339Nano), ID: id}.Encode()
}

// ParsePageToken decodes a token of [PageToken]; a malformed token fails
// with [core.ErrInvalidValue].
func ParsePageToken(token string) (time.Time, uuid.UUID, error) {
	c, err := core.DecodeCursor(token)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	at, err := time.Parse(time.RFC3339Nano, c.Key)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	return at.UTC(), c.ID, nil
}

// SecurityLogReader reads security logs, as [SecurityLogRepository].
type SecurityLogReader interface {
	// Get returns the entry id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*SecurityLog, error)
	// List returns the entries of org matching f.
	List(ctx context.Context, org core.OrganizationID, f SecurityLogFilter, page core.PageRequest) (core.Page[*SecurityLog], error)
}

// APILogReader reads API logs, as [APILogRepository].
type APILogReader interface {
	// Get returns the request id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*APILog, error)
	// List returns the requests of org matching f.
	List(ctx context.Context, org core.OrganizationID, f APILogFilter, page core.PageRequest) (core.Page[*APILog], error)
}

// ActivityLogReader reads activity logs, as [ActivityLogRepository].
type ActivityLogReader interface {
	// Get returns the activity id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*ActivityLog, error)
	// List returns the activities of org matching f.
	List(ctx context.Context, org core.OrganizationID, f ActivityLogFilter, page core.PageRequest) (core.Page[*ActivityLog], error)
}

// Batch is a set of logs moved together to an [Archive].
type Batch struct {
	// Security are the security logs.
	Security []*SecurityLog
	// API are the API logs.
	API []*APILog
	// Activity are the activity logs.
	Activity []*ActivityLog
}

// Len returns the number of logs of b.
func (b Batch) Len() int { return len(b.Security) + len(b.API) + len(b.Activity) }

// Archive is a log store outside the transactional store (ClickHouse,
// architecture §7.4). Logs are written in the transaction of their change
// to the transactional store, which holds them until they are moved to the
// archive in batches ([ArchivedSecurityLogs] reads both meanwhile).
//
// [Archive.Write] must be idempotent: a batch whose move failed after the
// write is written again, and the archive keeps one copy of each log.
type Archive interface {
	// Write stores the logs of b.
	Write(ctx context.Context, b Batch) error
	// SecurityLogs reads the archived security logs.
	SecurityLogs() SecurityLogReader
	// APILogs reads the archived API logs.
	APILogs() APILogReader
	// ActivityLogs reads the archived activity logs.
	ActivityLogs() ActivityLogReader
}

// ArchivedSecurityLogs returns the repository of the security logs held in
// pending until they are moved to archive: entries are created in pending
// and read from both, pending first.
func ArchivedSecurityLogs(pending SecurityLogRepository, archive SecurityLogReader) SecurityLogRepository {
	return archivedSecurity{pending: pending, archive: archive}
}

// ArchivedAPILogs is [ArchivedSecurityLogs] for API logs. A log whose
// request ID is archived is not created again, as [APILogRepository.Create]
// keeps the first log of a request ID.
func ArchivedAPILogs(pending APILogRepository, archive APILogReader) APILogRepository {
	return archivedAPI{pending: pending, archive: archive}
}

// ArchivedActivityLogs is [ArchivedSecurityLogs] for activity logs.
func ArchivedActivityLogs(pending ActivityLogRepository, archive ActivityLogReader) ActivityLogRepository {
	return archivedActivity{pending: pending, archive: archive}
}

type archivedSecurity struct {
	pending SecurityLogRepository
	archive SecurityLogReader
}

func (r archivedSecurity) Create(ctx context.Context, l *SecurityLog) error {
	return r.pending.Create(ctx, l)
}

func (r archivedSecurity) Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*SecurityLog, error) {
	return getEither(ctx, org, id, r.pending.Get, r.archive.Get)
}

func (r archivedSecurity) List(ctx context.Context, org core.OrganizationID, f SecurityLogFilter, page core.PageRequest) (core.Page[*SecurityLog], error) {
	return listBoth(ctx, page,
		func(ctx context.Context, p core.PageRequest) (core.Page[*SecurityLog], error) {
			return r.pending.List(ctx, org, f, p)
		},
		func(ctx context.Context, p core.PageRequest) (core.Page[*SecurityLog], error) {
			return r.archive.List(ctx, org, f, p)
		},
		func(l *SecurityLog) (time.Time, uuid.UUID) { return l.LoggedAt, l.ID })
}

type archivedAPI struct {
	pending APILogRepository
	archive APILogReader
}

func (r archivedAPI) Create(ctx context.Context, l *APILog) error {
	_, err := r.archive.Get(ctx, l.OrganizationID, l.ID)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, core.ErrNotFound):
		return fmt.Errorf("get archived api log: %w", err)
	}
	return r.pending.Create(ctx, l)
}

func (r archivedAPI) Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*APILog, error) {
	return getEither(ctx, org, id, r.pending.Get, r.archive.Get)
}

func (r archivedAPI) List(ctx context.Context, org core.OrganizationID, f APILogFilter, page core.PageRequest) (core.Page[*APILog], error) {
	return listBoth(ctx, page,
		func(ctx context.Context, p core.PageRequest) (core.Page[*APILog], error) {
			return r.pending.List(ctx, org, f, p)
		},
		func(ctx context.Context, p core.PageRequest) (core.Page[*APILog], error) {
			return r.archive.List(ctx, org, f, p)
		},
		func(l *APILog) (time.Time, uuid.UUID) { return l.LoggedAt, l.ID })
}

type archivedActivity struct {
	pending ActivityLogRepository
	archive ActivityLogReader
}

func (r archivedActivity) Create(ctx context.Context, l *ActivityLog) error {
	return r.pending.Create(ctx, l)
}

func (r archivedActivity) Get(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*ActivityLog, error) {
	return getEither(ctx, org, id, r.pending.Get, r.archive.Get)
}

func (r archivedActivity) List(ctx context.Context, org core.OrganizationID, f ActivityLogFilter, page core.PageRequest) (core.Page[*ActivityLog], error) {
	return listBoth(ctx, page,
		func(ctx context.Context, p core.PageRequest) (core.Page[*ActivityLog], error) {
			return r.pending.List(ctx, org, f, p)
		},
		func(ctx context.Context, p core.PageRequest) (core.Page[*ActivityLog], error) {
			return r.archive.List(ctx, org, f, p)
		},
		func(l *ActivityLog) (time.Time, uuid.UUID) { return l.LoggedAt, l.ID })
}

// getEither returns the entry of the pending store, else of the archive.
func getEither[T any](ctx context.Context, org core.OrganizationID, id uuid.UUID, pending, archive func(context.Context, core.OrganizationID, uuid.UUID) (T, error)) (T, error) {
	l, err := pending(ctx, org, id)
	if err == nil || !errors.Is(err, core.ErrNotFound) {
		return l, err
	}
	return archive(ctx, org, id)
}

// listBoth merges a page of the pending store and one of the archive, both
// ordered by LoggedAt descending then ID ascending from the same cursor,
// into one page in that order. A log in both, moved while listed, is
// returned once, from the pending store.
func listBoth[T any](ctx context.Context, page core.PageRequest, pending, archive func(context.Context, core.PageRequest) (core.Page[T], error), key func(T) (time.Time, uuid.UUID)) (core.Page[T], error) {
	p, err := pending(ctx, page)
	if err != nil {
		return core.Page[T]{}, err
	}
	a, err := archive(ctx, page)
	if err != nil {
		return core.Page[T]{}, err
	}
	n := page.Limit()
	seen := make(map[uuid.UUID]bool, len(p.Items))
	for _, l := range p.Items {
		_, id := key(l)
		seen[id] = true
	}
	out := make([]T, 0, min(n, len(p.Items)+len(a.Items)))
	more := p.NextToken != "" || a.NextToken != ""
	i, j := 0, 0
	for i < len(p.Items) || j < len(a.Items) {
		if j < len(a.Items) {
			if _, id := key(a.Items[j]); seen[id] {
				j++
				continue
			}
		}
		var next T
		if j >= len(a.Items) || (i < len(p.Items) && before(key(p.Items[i]))(key(a.Items[j]))) {
			next = p.Items[i]
			i++
		} else {
			next = a.Items[j]
			j++
		}
		if len(out) == n {
			more = true
			break
		}
		out = append(out, next)
	}
	res := core.Page[T]{Items: out}
	if more && len(out) > 0 {
		res.NextToken = PageToken(key(out[len(out)-1]))
	}
	return res, nil
}

// before reports whether the entry (at, id) is listed before (at2, id2):
// the most recent first, then by ID.
func before(at time.Time, id uuid.UUID) func(time.Time, uuid.UUID) bool {
	return func(at2 time.Time, id2 uuid.UUID) bool {
		if !at.Equal(at2) {
			return at.After(at2)
		}
		return id.String() < id2.String()
	}
}
