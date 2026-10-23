package auth

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestAuditLogsActivity(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	u := e.user("alice@example.com", "")
	now := e.clock.Now()
	mk := func(age time.Duration, user core.UserID) uuid.UUID {
		l := &auditlog.ActivityLog{
			ID: e.deps.IDs.NewUUID(), OrganizationID: o.ID, UserID: user, Type: "customer.created",
			Source: auditlog.ActivitySourceFront, ResourceID: e.deps.IDs.NewUUID(), ResourceType: "customer",
			LoggedAt: now.Add(-age), CreatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error { return r.ActivityLogs.Create(ctx, l) })
		return l.ID
	}
	recent := mk(time.Hour, u.ID)
	other := mk(2*time.Hour, core.UserID{})
	old := mk(40*24*time.Hour, u.ID)
	s := NewAuditLogs(e.deps)
	ids := func(p core.Page[ActivityLogView]) []uuid.UUID {
		out := make([]uuid.UUID, len(p.Items))
		for i, v := range p.Items {
			out[i] = v.ID
		}
		return out
	}
	cases := []struct {
		name   string
		source string
		q      ActivityLogQuery
		want   []uuid.UUID
	}{
		{"retention", "spec/queries/activity_logs_query_spec.rb:40", ActivityLogQuery{}, []uuid.UUID{recent, other}},
		{"from before retention", "spec/queries/activity_logs_query_spec.rb:40", ActivityLogQuery{ActivityLogFilter: auditlog.ActivityLogFilter{LoggedAt: auditlog.Range{From: new(now.AddDate(-1, 0, 0))}}}, []uuid.UUID{recent, other}},
		{"user emails", "spec/queries/activity_logs_query_spec.rb:124", ActivityLogQuery{UserEmails: []string{"alice@example.com"}}, []uuid.UUID{recent}},
		{"unknown email", "spec/queries/activity_logs_query_spec.rb:124", ActivityLogQuery{UserEmails: []string{"bob@example.com"}}, []uuid.UUID{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := s.ListActivityLogs(e.ctx, o.ID, c.q, core.PageRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(p); !slices.Equal(got, c.want) {
				t.Errorf("ids got = %v, want = %v (%s)", got, c.want, c.source)
			}
		})
	}
	t.Run("emails", func(t *testing.T) {
		v, err := s.GetActivityLog(e.ctx, o.ID, recent)
		if err != nil || v.UserEmail != "alice@example.com" {
			t.Errorf("get got = %q, %v, want = alice@example.com", v.UserEmail, err)
		}
		// lago show finds old logs too (activity_logs_controller.rb:35).
		if _, err := s.GetActivityLog(e.ctx, o.ID, old); err != nil {
			t.Errorf("get old got = %v, want = nil", err)
		}
		if _, err := s.GetActivityLog(e.ctx, o.ID, e.deps.IDs.NewUUID()); code(err) != "activity_log_not_found" {
			t.Errorf("get unknown got = %v, want = activity_log_not_found", err)
		}
	})
}

func TestAuditLogsAPIRetention(t *testing.T) {
	// source: spec/queries/api_logs_query_spec.rb:41
	e := newEnv(t)
	o := e.org()
	now := e.clock.Now()
	var recent uuid.UUID
	for _, age := range []time.Duration{time.Hour, 31 * 24 * time.Hour} {
		l := &auditlog.APILog{ID: e.deps.IDs.NewUUID(), OrganizationID: o.ID, HTTPMethod: "post", HTTPStatus: 200, RequestPath: "/api/v1/customers", LoggedAt: now.Add(-age), CreatedAt: now}
		if age == time.Hour {
			recent = l.ID
		}
		e.tx(func(ctx context.Context, r port.Repos) error { return r.APILogs.Create(ctx, l) })
	}
	p, err := NewAuditLogs(e.deps).ListAPILogs(e.ctx, o.ID, auditlog.APILogFilter{}, core.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].ID != recent {
		t.Errorf("logs got = %d, want = the recent one", len(p.Items))
	}
}

func TestAuditLogsSecurity(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	now := e.clock.Now()
	mk := func(age time.Duration) uuid.UUID {
		l := &auditlog.SecurityLog{ID: e.deps.IDs.NewUUID(), OrganizationID: o.ID, Type: "user", Event: "user.signed_up", LoggedAt: now.Add(-age), CreatedAt: now}
		e.tx(func(ctx context.Context, r port.Repos) error { return r.SecurityLogs.Create(ctx, l) })
		return l.ID
	}
	recent := mk(time.Hour)
	old := mk(91 * 24 * time.Hour)
	s := NewAuditLogs(e.deps)

	// source: spec/queries/security_logs_query_spec.rb:45
	_, err := s.ListSecurityLogs(e.ctx, o.ID, auditlog.SecurityLogFilter{}, core.PageRequest{})
	if fe := core.FieldErrors(err); len(fe) != 1 || fe[0].Field != "to_date" || fe[0].Code != core.CodeRequired {
		t.Fatalf("missing to got = %v, want = to_date value_is_mandatory", err)
	}

	// source: spec/queries/security_logs_query_spec.rb:61
	p, err := s.ListSecurityLogs(e.ctx, o.ID, auditlog.SecurityLogFilter{LoggedAt: auditlog.Range{To: &now}}, core.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].ID != recent {
		t.Errorf("logs got = %d, want = the recent one", len(p.Items))
	}
	if _, err := s.GetSecurityLog(e.ctx, o.ID, old); code(err) != "security_log_not_found" {
		t.Errorf("get old got = %v, want = security_log_not_found", err)
	}
}
