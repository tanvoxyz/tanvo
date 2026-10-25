package auditlog_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
)

// sliceLogs is an activity log store over a slice, ordered as the log
// stores are.
type sliceLogs struct {
	logs []*auditlog.ActivityLog
	gets int
}

func (s *sliceLogs) Create(_ context.Context, l *auditlog.ActivityLog) error {
	s.logs = append(s.logs, l)
	return nil
}

func (s *sliceLogs) Get(_ context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.ActivityLog, error) {
	s.gets++
	for _, l := range s.logs {
		if l.OrganizationID == org && l.ID == id {
			return l, nil
		}
	}
	return nil, core.ErrNotFound
}

func (s *sliceLogs) List(_ context.Context, org core.OrganizationID, _ auditlog.ActivityLogFilter, page core.PageRequest) (core.Page[*auditlog.ActivityLog], error) {
	sorted := slices.Clone(s.logs)
	slices.SortFunc(sorted, func(a, b *auditlog.ActivityLog) int {
		if c := b.LoggedAt.Compare(a.LoggedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	var after *auditlog.ActivityLog
	if page.Token != "" {
		at, id, err := auditlog.ParsePageToken(page.Token)
		if err != nil {
			return core.Page[*auditlog.ActivityLog]{}, err
		}
		after = &auditlog.ActivityLog{LoggedAt: at, ID: id}
	}
	var out core.Page[*auditlog.ActivityLog]
	for _, l := range sorted {
		if l.OrganizationID != org {
			continue
		}
		if after != nil && (l.LoggedAt.After(after.LoggedAt) || (l.LoggedAt.Equal(after.LoggedAt) && l.ID.String() <= after.ID.String())) {
			continue
		}
		if len(out.Items) == page.Limit() {
			last := out.Items[len(out.Items)-1]
			out.NextToken = auditlog.PageToken(last.LoggedAt, last.ID)
			break
		}
		out.Items = append(out.Items, l)
	}
	return out, nil
}

func TestPageToken(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.FixedZone("x", 3600))
	id := uuid.New()
	gotAt, gotID, err := auditlog.ParsePageToken(auditlog.PageToken(at, id))
	if err != nil || !gotAt.Equal(at) || gotAt.Location() != time.UTC || gotID != id {
		t.Errorf("ParsePageToken(PageToken()) got = %v, %v, %v, want = %v, %v, nil", gotAt, gotID, err, at.UTC(), id)
	}
	for _, tok := range []string{"x", core.Cursor{Key: "yesterday", ID: id}.Encode()} {
		if _, _, err := auditlog.ParsePageToken(tok); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("ParsePageToken(%q) error got = %v, want = %v", tok, err, core.ErrInvalidValue)
		}
	}
}

// TestArchivedLogs checks the logs read from both the pending store and the
// archive: created in the pending store, read from it first, and listed
// merged in order, without duplicates, at every page size.
func TestArchivedLogs(t *testing.T) {
	ctx := context.Background()
	g := core.UUIDv7Generator{}
	org, other := core.NewID[core.OrganizationID](g), core.NewID[core.OrganizationID](g)
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mk := func(o core.OrganizationID, minute int) *auditlog.ActivityLog {
		return &auditlog.ActivityLog{ID: g.NewUUID(), OrganizationID: o, Type: "customer.created", LoggedAt: base.Add(time.Duration(minute) * time.Minute)}
	}
	// Archived: minutes 0, 2, 4, 4 and 6; pending: 1, 4, 5 and 7; l6 is in
	// both (moved while listed).
	a := []*auditlog.ActivityLog{mk(org, 0), mk(org, 2), mk(org, 4), mk(org, 4), mk(org, 6), mk(other, 3)}
	p := []*auditlog.ActivityLog{mk(org, 1), mk(org, 4), mk(org, 5), mk(org, 7), a[4]}
	pending, archive := &sliceLogs{logs: p}, &sliceLogs{logs: a}
	r := auditlog.ArchivedActivityLogs(pending, archive)

	all := append(slices.Clone(p[:4]), a[:5]...)
	slices.SortFunc(all, func(x, y *auditlog.ActivityLog) int {
		if c := y.LoggedAt.Compare(x.LoggedAt); c != 0 {
			return c
		}
		return strings.Compare(x.ID.String(), y.ID.String())
	})
	var want []uuid.UUID
	for _, l := range all {
		want = append(want, l.ID)
	}
	for size := 1; size <= 10; size++ {
		var got []uuid.UUID
		page := core.PageRequest{Size: size}
		for range 20 {
			res, err := r.List(ctx, org, auditlog.ActivityLogFilter{}, page)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			for _, l := range res.Items {
				got = append(got, l.ID)
			}
			if len(res.Items) > size {
				t.Fatalf("List(size %d) returned %d items", size, len(res.Items))
			}
			if res.NextToken == "" {
				break
			}
			page.Token = res.NextToken
		}
		if !slices.Equal(got, want) {
			t.Errorf("List(size %d) got = %v, want = %v", size, got, want)
		}
	}

	if l, err := r.Get(ctx, org, a[1].ID); err != nil || l != a[1] {
		t.Errorf("Get() of an archived log got = %v, %v, want = %v", l, err, a[1])
	}
	if l, err := r.Get(ctx, org, p[0].ID); err != nil || l != p[0] {
		t.Errorf("Get() of a pending log got = %v, %v, want = %v", l, err, p[0])
	}
	if _, err := r.Get(ctx, org, a[5].ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get() of another organization error got = %v, want = %v", err, core.ErrNotFound)
	}
	n := mk(org, 9)
	if err := r.Create(ctx, n); err != nil || pending.logs[len(pending.logs)-1] != n || len(archive.logs) != len(a) {
		t.Errorf("Create() error = %v, want the log in the pending store only", err)
	}
}

// apiLogs is an API log store over a slice.
type apiLogs struct{ logs []*auditlog.APILog }

func (s *apiLogs) Create(_ context.Context, l *auditlog.APILog) error {
	s.logs = append(s.logs, l)
	return nil
}

func (s *apiLogs) Get(_ context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.APILog, error) {
	for _, l := range s.logs {
		if l.OrganizationID == org && l.ID == id {
			return l, nil
		}
	}
	return nil, core.ErrNotFound
}

func (s *apiLogs) List(context.Context, core.OrganizationID, auditlog.APILogFilter, core.PageRequest) (core.Page[*auditlog.APILog], error) {
	return core.Page[*auditlog.APILog]{Items: s.logs}, nil
}

// TestArchivedAPILogsCreate checks that an API log whose request ID is
// archived is not created again: the first log of a request ID is kept.
func TestArchivedAPILogsCreate(t *testing.T) {
	ctx := context.Background()
	g := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](g)
	first := &auditlog.APILog{ID: g.NewUUID(), OrganizationID: org, HTTPStatus: 200}
	pending, archive := &apiLogs{}, &apiLogs{logs: []*auditlog.APILog{first}}
	r := auditlog.ArchivedAPILogs(pending, archive)
	again := *first
	again.HTTPStatus = 500
	if err := r.Create(ctx, &again); err != nil || len(pending.logs) != 0 {
		t.Errorf("Create() of an archived request ID got = %v, %d pending, want = nil, 0", err, len(pending.logs))
	}
	fresh := &auditlog.APILog{ID: g.NewUUID(), OrganizationID: org}
	if err := r.Create(ctx, fresh); err != nil || len(pending.logs) != 1 {
		t.Errorf("Create() got = %v, %d pending, want = nil, 1", err, len(pending.logs))
	}
}
