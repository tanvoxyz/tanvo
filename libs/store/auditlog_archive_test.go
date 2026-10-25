package store_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
)

// memArchive is an in-memory log archive keeping one copy of each log; the
// archived logs are read through a memstore.
type memArchive struct {
	mu   sync.Mutex
	mem  *memstore.Store
	seen map[uuid.UUID]bool
	fail error
}

func newMemArchive() *memArchive {
	return &memArchive{mem: memstore.New(), seen: map[uuid.UUID]bool{}}
}

func (a *memArchive) Write(ctx context.Context, b auditlog.Batch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != nil {
		return a.fail
	}
	return a.mem.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		for _, l := range b.Security {
			if !a.seen[l.ID] {
				a.seen[l.ID] = true
				if err := r.SecurityLogs.Create(ctx, l); err != nil {
					return err
				}
			}
		}
		for _, l := range b.API {
			if !a.seen[l.ID] {
				a.seen[l.ID] = true
				if err := r.APILogs.Create(ctx, l); err != nil {
					return err
				}
			}
		}
		for _, l := range b.Activity {
			if !a.seen[l.ID] {
				a.seen[l.ID] = true
				if err := r.ActivityLogs.Create(ctx, l); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (a *memArchive) repos(ctx context.Context) port.Repos {
	var out port.Repos
	_ = a.mem.InTx(ctx, func(_ context.Context, r port.Repos) error { out = r; return nil })
	return out
}

func (a *memArchive) SecurityLogs() auditlog.SecurityLogReader {
	return a.repos(context.Background()).SecurityLogs
}

func (a *memArchive) APILogs() auditlog.APILogReader { return a.repos(context.Background()).APILogs }

func (a *memArchive) ActivityLogs() auditlog.ActivityLogReader {
	return a.repos(context.Background()).ActivityLogs
}

// TestArchiveLogs checks that Store.ArchiveLogs moves the audit logs of
// every organization to the archive in batches, under row-level security,
// that a failed archive write keeps them, and that a store with the archive
// lists the pending and archived logs together.
func TestArchiveLogs(t *testing.T) {
	ctx := t.Context()
	archive := newMemArchive()
	db := pgtest.New(t, template)
	s, err := store.Open(ctx, db.AppDSN, store.WithRowLevelSecurity(), store.WithLogArchive(archive))
	if err != nil {
		t.Fatalf("Open() error got = %v, want = nil", err)
	}
	t.Cleanup(s.Pool().Close)

	g := core.UUIDv7Generator{}
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	orgs := []*organization.Organization{
		organization.New(core.NewID[core.OrganizationID](g), "Acme", now),
		organization.New(core.NewID[core.OrganizationID](g), "Hooli", now),
	}
	var activities []uuid.UUID
	if err := s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		for i, o := range orgs {
			if err := r.Organizations.Create(ctx, o); err != nil {
				return err
			}
			for j := range 3 {
				at := now.Add(time.Duration(i*10+j) * time.Minute)
				l := &auditlog.ActivityLog{
					ID: g.NewUUID(), OrganizationID: o.ID, Type: "customer.created", Source: auditlog.ActivitySourceAPI,
					ResourceID: g.NewUUID(), ResourceType: "customer", Object: map[string]any{"n": float64(j)}, LoggedAt: at, CreatedAt: at,
				}
				activities = append(activities, l.ID)
				if err := r.ActivityLogs.Create(ctx, l); err != nil {
					return err
				}
			}
			at := now.Add(time.Duration(i) * time.Minute)
			if err := r.SecurityLogs.Create(ctx, &auditlog.SecurityLog{
				ID: g.NewUUID(), OrganizationID: o.ID, Type: "user", Event: "user.signed_up", LoggedAt: at, CreatedAt: at,
			}); err != nil {
				return err
			}
			if err := r.APILogs.Create(ctx, &auditlog.APILog{
				ID: g.NewUUID(), OrganizationID: o.ID, APIKeyID: core.NewID[core.APIKeyID](g), Client: "sdk", HTTPMethod: "post",
				HTTPStatus: 200, RequestOrigin: "https://api.tanvo.test", RequestPath: "/v1/customers", APIVersion: "v1", LoggedAt: at, CreatedAt: at,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("create logs: %v", err)
	}

	listActivities := func(org core.OrganizationID) []uuid.UUID {
		t.Helper()
		var out []uuid.UUID
		page := core.PageRequest{Size: 2}
		for {
			var p core.Page[*auditlog.ActivityLog]
			if err := s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
				var err error
				p, err = r.ActivityLogs.List(ctx, org, auditlog.ActivityLogFilter{}, page)
				return err
			}); err != nil {
				t.Fatalf("List() error = %v", err)
			}
			for _, l := range p.Items {
				out = append(out, l.ID)
			}
			if p.NextToken == "" {
				return out
			}
			page.Token = p.NextToken
		}
	}
	wantFirst := slices.Clone(activities[:3])
	slices.Reverse(wantFirst)

	archive.fail = errors.New("archive down")
	if _, err := s.ArchiveLogs(ctx, archive, 10); !errors.Is(err, archive.fail) {
		t.Fatalf("ArchiveLogs() error got = %v, want = %v", err, archive.fail)
	}
	archive.fail = nil
	if got := listActivities(orgs[0].ID); !slices.Equal(got, wantFirst) {
		t.Errorf("List() before archiving got = %v, want = %v", got, wantFirst)
	}

	moved := 0
	for range 10 {
		n, err := s.ArchiveLogs(ctx, archive, 2)
		if err != nil {
			t.Fatalf("ArchiveLogs() error = %v", err)
		}
		if n == 0 {
			break
		}
		if n > 6 {
			t.Errorf("ArchiveLogs(2) moved %d logs, want at most 2 of each kind", n)
		}
		moved += n
		// Partly moved: the list merges both stores.
		if got := listActivities(orgs[0].ID); !slices.Equal(got, wantFirst) {
			t.Errorf("List() while archiving got = %v, want = %v", got, wantFirst)
		}
	}
	if moved != 10 {
		t.Errorf("ArchiveLogs() moved %d logs in all, want = 10", moved)
	}
	if len(archive.seen) != 10 {
		t.Errorf("archive holds %d logs, want = 10", len(archive.seen))
	}
	var left int
	if err := s.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM activity_logs) + (SELECT count(*) FROM api_logs) + (SELECT count(*) FROM security_logs)`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("audit log tables hold %d logs after archiving, want = 0", left)
	}
	if got := listActivities(orgs[0].ID); !slices.Equal(got, wantFirst) {
		t.Errorf("List() after archiving got = %v, want = %v", got, wantFirst)
	}
	if err := s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		l, err := r.ActivityLogs.Get(ctx, orgs[1].ID, activities[3])
		if err != nil {
			return err
		}
		if l.Object["n"] != 0.0 {
			t.Errorf("Get() object got = %v, want = map[n:0]", l.Object)
		}
		return nil
	}); err != nil {
		t.Errorf("Get() of an archived log error = %v", err)
	}
	if _, err := s.ArchiveLogs(ctx, archive, 0); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("ArchiveLogs(0) error got = %v, want = %v", err, core.ErrInvalidValue)
	}
}
