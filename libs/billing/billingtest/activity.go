package billingtest

import (
	"bytes"
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// LogActivities makes h's services record activity logs, as performed by
// actor. Call it before building the services under test.
func (h *Harness) LogActivities(actor auditlog.Actor) {
	h.T.Helper()
	h.Deps.ActivityLogs = auditlog.NewActivityProducer(h.Clock, h.IDs, func(context.Context) auditlog.Actor { return actor })
}

// Activities returns the activity logs of h's organization, oldest first,
// optionally only those of the types.
func (h *Harness) Activities(types ...auditlog.ActivityType) []*auditlog.ActivityLog {
	h.T.Helper()
	var all []*auditlog.ActivityLog
	h.Do(func(ctx context.Context, r port.Repos) error {
		req := core.PageRequest{Size: core.MaxPageSize}
		for {
			p, err := r.ActivityLogs.List(ctx, h.OrgID(), auditlog.ActivityLogFilter{}, req)
			if err != nil {
				return err
			}
			all = append(all, p.Items...)
			if p.NextToken == "" {
				return nil
			}
			req.Token = p.NextToken
		}
	})
	// The IDs grow with time, and break the ties of a frozen clock.
	slices.SortFunc(all, func(a, b *auditlog.ActivityLog) int {
		if c := a.LoggedAt.Compare(b.LoggedAt); c != 0 {
			return c
		}
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	if len(types) == 0 {
		return all
	}
	return slices.DeleteFunc(all, func(l *auditlog.ActivityLog) bool { return !slices.Contains(types, auditlog.ActivityType(l.Type)) })
}

// ActivityWant is an expected activity log.
type ActivityWant struct {
	// Type is the activity type.
	Type auditlog.ActivityType
	// ResourceType and ResourceID are the logged resource.
	ResourceType auditlog.ResourceType
	ResourceID   uuid.UUID
	// ExternalCustomerID and ExternalSubscriptionID are the external IDs
	// recorded.
	ExternalCustomerID, ExternalSubscriptionID string
	// Changes are the keys of the changes recorded, sorted; none means no
	// changes.
	Changes []string
}

// WantActivities fails unless the activity logs of h's organization are
// want, in order. Only the logs of the types in want are compared, so a
// test checks the sites it is about.
func (h *Harness) WantActivities(want ...ActivityWant) {
	h.T.Helper()
	var types []auditlog.ActivityType
	for _, w := range want {
		if !slices.Contains(types, w.Type) {
			types = append(types, w.Type)
		}
	}
	got := h.Activities(types...)
	if len(want) == 0 {
		got = h.Activities()
	}
	if len(got) != len(want) {
		var gt []string
		for _, l := range got {
			gt = append(gt, l.Type)
		}
		h.T.Fatalf("activity logs = %v, want %d logs", gt, len(want))
	}
	for i, w := range want {
		l := got[i]
		g := ActivityWant{
			Type: auditlog.ActivityType(l.Type), ResourceType: auditlog.ResourceType(l.ResourceType), ResourceID: l.ResourceID,
			ExternalCustomerID: l.ExternalCustomerID, ExternalSubscriptionID: l.ExternalSubscriptionID,
			Changes: slices.Sorted(maps.Keys(l.ObjectChanges)),
		}
		if len(w.Changes) == 0 {
			w.Changes = nil
		}
		if len(g.Changes) == 0 {
			g.Changes = nil
		}
		if g.Type != w.Type || g.ResourceType != w.ResourceType || g.ResourceID != w.ResourceID ||
			g.ExternalCustomerID != w.ExternalCustomerID || g.ExternalSubscriptionID != w.ExternalSubscriptionID ||
			!slices.Equal(g.Changes, w.Changes) {
			h.T.Errorf("activity log %d = %+v, want %+v", i, g, w)
		}
		if len(l.Object) == 0 {
			h.T.Errorf("activity log %d (%s) object is empty", i, l.Type)
		}
	}
}
