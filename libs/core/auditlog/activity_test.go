package auditlog_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
)

// captureRepo keeps the logs created through it.
type captureRepo struct {
	auditlog.ActivityLogRepository
	logs []*auditlog.ActivityLog
}

func (r *captureRepo) Create(_ context.Context, l *auditlog.ActivityLog) error {
	r.logs = append(r.logs, l)
	return nil
}

func TestActivityTypes(t *testing.T) {
	t.Parallel()
	t.Log("source: app/models/clickhouse/activity_log.rb:50 (ACTIVITY_TYPES, 75 types) and :27 (RESOURCE_TYPES, 20 kinds)")
	types, resources := auditlog.ActivityTypes(), auditlog.ResourceTypes()
	if len(types) != 75 || len(resources) != 20 {
		t.Errorf("types got = %d, %d, want = 75, 20", len(types), len(resources))
	}
	seen := map[auditlog.ActivityType]bool{}
	for _, ty := range types {
		if seen[ty] {
			t.Errorf("duplicate type %q", ty)
		}
		seen[ty] = true
	}
	for _, tc := range []struct {
		t    auditlog.ActivityType
		want bool
	}{
		{auditlog.ActivityCustomerUpdated, true},
		{auditlog.ActivityInvoicePaymentStatusUpdated, true},
		{auditlog.ActivityCustomerCreated, false},
	} {
		if got := tc.t.IsUpdate(); got != tc.want {
			t.Errorf("%s IsUpdate() got = %v, want = %v", tc.t, got, tc.want)
		}
	}
}

func TestActivityProducer(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	org := core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	user := core.NewID[core.UserID](core.UUIDv7Generator{})
	key := core.NewID[core.APIKeyID](core.UUIDv7Generator{})
	resource := uuid.New()
	tests := []struct {
		name  string
		actor *auditlog.Actor
		a     auditlog.Activity
		want  auditlog.ActivityLog
	}{
		{
			// source: spec/services/utils/activity_log_spec.rb:195 (no source is system; changes only for updates)
			name: "job is system",
			a: auditlog.Activity{
				Type: auditlog.ActivityCustomerCreated, ResourceType: auditlog.ResourceCustomer,
				Object: map[string]any{"name": "a"}, Before: map[string]any{"name": "b"}, ExternalCustomerID: "c1",
			},
			want: auditlog.ActivityLog{
				Type: "customer.created", Source: auditlog.ActivitySourceSystem, ResourceType: "customer",
				Object: map[string]any{"name": "a"}, ObjectChanges: map[string]any{}, ExternalCustomerID: "c1",
			},
		},
		{
			// source: spec/services/utils/activity_log_spec.rb:80 (the changed attributes)
			name:  "api key",
			actor: &auditlog.Actor{Source: auditlog.ActivitySourceAPI, APIKeyID: key},
			a: auditlog.Activity{
				Type: auditlog.ActivityCustomerUpdated, ResourceType: auditlog.ResourceCustomer,
				Before: map[string]any{"name": "b", "updated_at": "1", "email": "e"},
				Object: map[string]any{"name": "a", "updated_at": "2", "email": "e", "city": "x"},
			},
			want: auditlog.ActivityLog{
				Type: "customer.updated", Source: auditlog.ActivitySourceAPI, APIKeyID: key, ResourceType: "customer",
				Object:        map[string]any{"name": "a", "updated_at": "2", "email": "e", "city": "x"},
				ObjectChanges: map[string]any{"name": []any{"b", "a"}, "city": []any{nil, "x"}},
			},
		},
		{
			name:  "user",
			actor: &auditlog.Actor{Source: auditlog.ActivitySourceFront, UserID: user},
			a:     auditlog.Activity{Type: auditlog.ActivityPlanDeleted, ResourceType: auditlog.ResourcePlan},
			want: auditlog.ActivityLog{
				Type: "plan.deleted", Source: auditlog.ActivitySourceFront, UserID: user, ResourceType: "plan",
				ObjectChanges: map[string]any{},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var actor func(context.Context) auditlog.Actor
			if tc.actor != nil {
				actor = func(context.Context) auditlog.Actor { return *tc.actor }
			}
			p := auditlog.NewActivityProducer(core.FixedClock(now), core.UUIDv7Generator{}, actor)
			repo := &captureRepo{}
			tc.a.OrganizationID, tc.a.ResourceID = org, resource
			if err := p.Produce(t.Context(), repo, tc.a); err != nil {
				t.Fatal(err)
			}
			if len(repo.logs) != 1 {
				t.Fatalf("logs got = %d, want = 1", len(repo.logs))
			}
			got := *repo.logs[0]
			if got.ID == uuid.Nil {
				t.Error("ID got = nil")
			}
			want := tc.want
			want.ID, want.OrganizationID, want.ResourceID, want.LoggedAt, want.CreatedAt = got.ID, org, resource, now, now
			if !reflect.DeepEqual(got, want) {
				t.Errorf("log got = %+v, want = %+v", got, want)
			}
		})
	}

	t.Run("unknown type", func(t *testing.T) {
		t.Parallel()
		p := auditlog.NewActivityProducer(core.FixedClock(now), core.UUIDv7Generator{}, nil)
		err := p.Produce(t.Context(), &captureRepo{}, auditlog.Activity{Type: "organization.updated", ResourceType: auditlog.ResourcePlan})
		if !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("Produce() got = %v, want = %v", err, core.ErrInvalidValue)
		}
	})
}

func TestObjectOf(t *testing.T) {
	t.Parallel()
	id := core.NewID[core.APIKeyID](core.UUIDv7Generator{})
	type nested struct{ URLPath string }
	v := struct {
		APIKeyID  core.APIKeyID
		CreatedAt time.Time
		Amount    json.Number
		Items     []nested
		HTMLBody2 string
	}{APIKeyID: id, CreatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Amount: "12.300", Items: []nested{{URLPath: "/x"}}, HTMLBody2: "b"}
	got, err := auditlog.ObjectOf(v)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"api_key_id": id.String(), "created_at": "2026-01-02T00:00:00Z", "amount": json.Number("12.300"),
		"items": []any{map[string]any{"url_path": "/x"}}, "html_body2": "b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ObjectOf() got = %v, want = %v", got, want)
	}
	if _, err := auditlog.ObjectOf([]int{1}); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("ObjectOf(slice) got = %v, want = %v", err, core.ErrInvalidValue)
	}
}
