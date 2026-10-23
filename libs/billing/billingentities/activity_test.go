package billingentities_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingentities"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// activities returns the activity logs of h's organization, most recent
// first.
func activities(t *testing.T, h *billingtest.Harness) []*auditlog.ActivityLog {
	t.Helper()
	var out []*auditlog.ActivityLog
	err := h.Deps.Tx.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		p, err := r.ActivityLogs.List(ctx, h.Org.ID, auditlog.ActivityLogFilter{}, core.PageRequest{Size: 100})
		out = p.Items
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestActivityLogs(t *testing.T) {
	t.Parallel()
	h := billingtest.New(t, now)
	user := core.NewID[core.UserID](h.IDs)
	h.Deps.ActivityLogs = auditlog.NewActivityProducer(h.Clock, h.IDs, func(context.Context) auditlog.Actor {
		return auditlog.Actor{Source: auditlog.ActivitySourceFront, UserID: user}
	})
	s, err := billingentities.New(h.Deps, &blobs{files: map[core.BlobRef][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	t.Log("source: spec/services/billing_entities/create_service_spec.rb:18")
	be, err := s.Create(ctx, h.Org.ID, "second", billingentities.Input{Name: core.Some("Second")})
	if err != nil {
		t.Fatal(err)
	}
	logs := activities(t, h)
	if len(logs) != 1 {
		t.Fatalf("logs got = %d, want = 1", len(logs))
	}
	if l := logs[0]; l.Type != "billing_entities.created" || l.ResourceType != "billing_entity" || l.ResourceID != be.ID.UUID() ||
		l.Source != auditlog.ActivitySourceFront || l.UserID != user || l.Object["code"] != "second" || len(l.ObjectChanges) != 0 {
		t.Errorf("create log got = %+v", l)
	}

	t.Log("source: spec/services/billing_entities/update_service_spec.rb:105")
	if _, err := s.Update(ctx, h.Org.ID, be.ID, billingentities.Input{Name: core.Some("Renamed")}); err != nil {
		t.Fatal(err)
	}
	logs = activities(t, h)
	if len(logs) != 2 {
		t.Fatalf("logs got = %d, want = 2", len(logs))
	}
	var upd *auditlog.ActivityLog
	for _, l := range logs {
		if l.Type == "billing_entities.updated" {
			upd = l
		}
	}
	if upd == nil {
		t.Fatalf("logs got = %+v, want an update", logs)
	}
	name, ok := upd.ObjectChanges["name"].([]any)
	if !ok || len(name) != 2 || name[0] != "Second" || name[1] != "Renamed" {
		t.Errorf("update changes got = %v, want = name [Second Renamed]", upd.ObjectChanges)
	}
	if _, ok := upd.ObjectChanges["updated_at"]; ok {
		t.Errorf("update changes got = %v, want no updated_at", upd.ObjectChanges)
	}

	// A failed change logs nothing: the log is in the change's transaction.
	if _, err := s.Create(ctx, h.Org.ID, "second", billingentities.Input{Name: core.Some("Again")}); err == nil {
		t.Fatal("Create() of a taken code got = nil, want an error")
	}
	if got := len(activities(t, h)); got != 2 {
		t.Errorf("logs after a failure got = %d, want = 2", got)
	}
}
