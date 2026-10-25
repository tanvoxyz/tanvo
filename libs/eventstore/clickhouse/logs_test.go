package clickhouse_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
)

var logEpoch = time.Date(2026, 3, 1, 12, 0, 0, 123456000, time.UTC)

func logAt(d time.Duration) time.Time { return logEpoch.Add(d) }

// listLogs pages through a list with pages of size, checking that every
// page but the last is full and carries a token.
func listLogs[T any](t *testing.T, size int, list func(core.PageRequest) (core.Page[T], error), id func(T) uuid.UUID) []uuid.UUID {
	t.Helper()
	out := []uuid.UUID{}
	page := core.PageRequest{Size: size}
	for range 100 {
		p, err := list(page)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		for _, it := range p.Items {
			out = append(out, id(it))
		}
		if p.NextToken == "" {
			return out
		}
		if len(p.Items) != size {
			t.Fatalf("List() page with a token has %d items, want = %d", len(p.Items), size)
		}
		page.Token = p.NextToken
	}
	t.Fatal("List() does not end")
	return nil
}

func wantIDs(t *testing.T, name string, got, want []uuid.UUID) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s got = %v, want = %v", name, got, want)
	}
}

func wantLog(t *testing.T, name string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s got = %+v, want = %+v", name, got, want)
	}
}

// TestLogStore checks the archived security, API and activity logs: round
// trips, organization isolation, idempotent writes, the filters of lago's
// log queries and the most-recent-first order, as the PostgreSQL log
// tables (libs/store porttest testAuditLog).
func TestLogStore(t *testing.T) {
	conn := database(t)
	ctx := context.Background()
	s := clickhouse.NewLogStore(conn)
	g := core.UUIDv7Generator{}
	org, ob := core.NewID[core.OrganizationID](g), core.NewID[core.OrganizationID](g)
	user, key := core.NewID[core.UserID](g), core.NewID[core.APIKeyID](g)

	t.Run("security", func(t *testing.T) {
		sec := func(o core.OrganizationID, typ, event string, d time.Duration) *auditlog.SecurityLog {
			return &auditlog.SecurityLog{ID: g.NewUUID(), OrganizationID: o, Type: typ, Event: event, LoggedAt: logAt(d), CreatedAt: logAt(d)}
		}
		s1 := sec(org, "user", "user.signed_up", 0)
		s1.UserID = user
		// Nested values round trip (source: spec/models/clickhouse/security_log_spec.rb:31, 50).
		s1.Resources = map[string]any{"email": "a@hooli.com", "n": 1.0, "roles": []any{"admin", "finance"}, "name": map[string]any{"deleted": "A", "added": "B"}}
		s1.DeviceInfo = map[string]any{"browser": "Chrome", "resolution": map[string]any{"width": 1920.0, "height": 1080.0}}
		s2 := sec(org, "api_key", "api_key.created", time.Hour)
		s2.APIKeyID = key
		s3 := sec(org, "role", "role.created", time.Hour) // same instant as s2
		s4 := sec(ob, "user", "user.signed_up", 2*time.Hour)
		b := auditlog.Batch{Security: []*auditlog.SecurityLog{s1, s2, s3, s4}}
		for range 2 { // a move retried after its insert
			if err := s.Write(ctx, b); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
		}
		r := s.SecurityLogs()
		got, err := r.Get(ctx, org, s1.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		wantLog(t, "Get()", got, s1)
		if _, err := r.Get(ctx, org, s4.ID); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get() of another organization error got = %v, want = %v", err, core.ErrNotFound)
		}
		list := func(f auditlog.SecurityLogFilter) []uuid.UUID {
			return listLogs(t, 1, func(p core.PageRequest) (core.Page[*auditlog.SecurityLog], error) { return r.List(ctx, org, f, p) },
				func(l *auditlog.SecurityLog) uuid.UUID { return l.ID })
		}
		tie := []uuid.UUID{s2.ID, s3.ID}
		if s3.ID.String() < s2.ID.String() {
			tie = []uuid.UUID{s3.ID, s2.ID}
		}
		const src = "spec/queries/security_logs_query_spec.rb:"
		_ = src                                                                                                                                      // the spec file of the source lines below
		wantIDs(t, "List() order, by pages of one", list(auditlog.SecurityLogFilter{}), append(slices.Clone(tie), s1.ID))                            // source: src + "37, 70"
		wantIDs(t, "List() by user", list(auditlog.SecurityLogFilter{UserIDs: []core.UserID{user}}), []uuid.UUID{s1.ID})                             // source: src + "105"
		wantIDs(t, "List() by api key", list(auditlog.SecurityLogFilter{APIKeyIDs: []core.APIKeyID{key}}), []uuid.UUID{s2.ID})                       // source: src + "95"
		wantIDs(t, "List() by types", list(auditlog.SecurityLogFilter{Types: []string{"user", "role"}}), []uuid.UUID{s3.ID, s1.ID})                  // source: src + "115"
		wantIDs(t, "List() by event", list(auditlog.SecurityLogFilter{Events: []string{"api_key.created"}}), []uuid.UUID{s2.ID})                     // source: src + "125"
		wantIDs(t, "List() up to", list(auditlog.SecurityLogFilter{LoggedAt: auditlog.Range{To: new(logAt(30 * time.Minute))}}), []uuid.UUID{s1.ID}) // source: src + "82"
		wantIDs(t, "List() from", list(auditlog.SecurityLogFilter{LoggedAt: auditlog.Range{From: new(logAt(time.Hour))}}), tie)
	})

	t.Run("api", func(t *testing.T) {
		api := func(o core.OrganizationID, method string, status int, path, client string, d time.Duration) *auditlog.APILog {
			return &auditlog.APILog{
				ID: g.NewUUID(), OrganizationID: o, APIKeyID: key, Client: client, HTTPMethod: method, HTTPStatus: status,
				RequestOrigin: "https://api.tanvo.test", RequestPath: path, APIVersion: "v1", LoggedAt: logAt(d), CreatedAt: logAt(d),
			}
		}
		a1 := api(org, "post", 200, "/v1/customers", "curl/8", 0)
		a1.RequestBody = map[string]any{"external_id": "c1"}
		a1.Response = map[string]any{"customer": map[string]any{"external_id": "c1"}}
		a2 := api(org, "delete", 404, "/v1/customers/c1", "sdk", time.Minute)
		a3 := api(org, "put", 422, "/v1/plans/p_1", "sdk", 2*time.Minute)
		a3.APIKeyID = core.NewID[core.APIKeyID](g)
		a4 := api(ob, "post", 200, "/v1/customers", "sdk", 3*time.Minute)
		a5 := api(org, "patch", 200, "/v1/wallets/w1", "sdk", 4*time.Minute)
		b := auditlog.Batch{API: []*auditlog.APILog{a1, a2, a3, a4, a5}}
		for range 2 {
			if err := s.Write(ctx, b); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
		}
		r := s.APILogs()
		got, err := r.Get(ctx, org, a1.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		wantLog(t, "Get()", got, a1)
		if _, err := r.Get(ctx, ob, a1.ID); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get() of another organization error got = %v, want = %v", err, core.ErrNotFound)
		}
		list := func(f auditlog.APILogFilter) []uuid.UUID {
			return listLogs(t, 2, func(p core.PageRequest) (core.Page[*auditlog.APILog], error) { return r.List(ctx, org, f, p) },
				func(l *auditlog.APILog) uuid.UUID { return l.ID })
		}
		const src = "spec/queries/api_logs_query_spec.rb:"
		_ = src                                                                                                                                                                      // the spec file of the source lines below
		wantIDs(t, "List(), by pages of two", list(auditlog.APILogFilter{}), []uuid.UUID{a5.ID, a3.ID, a2.ID, a1.ID})                                                                // source: src + "20, 60"
		wantIDs(t, "List() by id", list(auditlog.APILogFilter{IDs: []uuid.UUID{a2.ID}}), []uuid.UUID{a2.ID})                                                                         // source: src + "171"
		wantIDs(t, "List() by key", list(auditlog.APILogFilter{APIKeyIDs: []core.APIKeyID{key}}), []uuid.UUID{a5.ID, a2.ID, a1.ID})                                                  // source: src + "85"
		wantIDs(t, "List() by methods", list(auditlog.APILogFilter{HTTPMethods: []string{"post", "put"}}), []uuid.UUID{a3.ID, a1.ID})                                                // source: src + "95"
		wantIDs(t, "List() by status", list(auditlog.APILogFilter{HTTPStatuses: []int{404}}), []uuid.UUID{a2.ID})                                                                    // source: src + "105"
		wantIDs(t, "List() succeeded", list(auditlog.APILogFilter{Succeeded: true, HTTPStatuses: []int{404}}), []uuid.UUID{a5.ID, a1.ID})                                            // source: src + "114"
		wantIDs(t, "List() failed", list(auditlog.APILogFilter{Failed: true}), []uuid.UUID{a3.ID, a2.ID})                                                                            // source: src + "125"
		wantIDs(t, "List() up to", list(auditlog.APILogFilter{LoggedAt: auditlog.Range{From: new(logAt(time.Minute)), To: new(logAt(2 * time.Minute))}}), []uuid.UUID{a3.ID, a2.ID}) // source: src + "72"
		wantIDs(t, "List() succeeded and failed", list(auditlog.APILogFilter{Succeeded: true, Failed: true}), []uuid.UUID{})
		// source: src + "149"
		wantIDs(t, "List() by path", list(auditlog.APILogFilter{RequestPaths: []string{"/v1/customers/*"}}), []uuid.UUID{a2.ID})
		wantIDs(t, "List() by paths", list(auditlog.APILogFilter{RequestPaths: []string{"/v1/customers", "/v1/wallets/*"}}), []uuid.UUID{a5.ID, a1.ID})
		wantIDs(t, "List() by exact path", list(auditlog.APILogFilter{RequestPaths: []string{"/v1/plans/p_1"}}), []uuid.UUID{a3.ID})
		wantIDs(t, "List() underscore is literal", list(auditlog.APILogFilter{RequestPaths: []string{"/v1/plans/p_"}}), []uuid.UUID{})
		wantIDs(t, "List() by client", list(auditlog.APILogFilter{Clients: []string{"curl/8"}}), []uuid.UUID{a1.ID}) // source: src + "181"
		wantIDs(t, "List() by version", list(auditlog.APILogFilter{APIVersion: "v2"}), []uuid.UUID{})                // source: src + "140"
		wantIDs(t, "List() by current version", list(auditlog.APILogFilter{APIVersion: "v1", Clients: []string{"sdk"}}), []uuid.UUID{a5.ID, a3.ID, a2.ID})
	})

	t.Run("activity", func(t *testing.T) {
		act := func(o core.OrganizationID, typ string, src auditlog.ActivitySource, d time.Duration) *auditlog.ActivityLog {
			return &auditlog.ActivityLog{
				ID: g.NewUUID(), OrganizationID: o, Type: typ, Source: src, ResourceID: g.NewUUID(), ResourceType: "customer",
				LoggedAt: logAt(d), CreatedAt: logAt(d),
			}
		}
		c1 := act(org, "customer.created", auditlog.ActivitySourceAPI, 0)
		c1.APIKeyID = key
		c1.ExternalCustomerID = "c1"
		c1.Object = map[string]any{"external_id": "c1"}
		c1.ObjectChanges = map[string]any{"name": []any{nil, "Acme"}}
		c2 := act(org, "customer.updated", auditlog.ActivitySourceFront, time.Minute)
		c2.UserID = user
		c2.ExternalSubscriptionID = "s1"
		c3 := act(org, "invoice.created", auditlog.ActivitySourceSystem, 2*time.Minute)
		c3.ResourceType = "invoice"
		c4 := act(ob, "customer.created", auditlog.ActivitySourceAPI, 0)
		b := auditlog.Batch{Activity: []*auditlog.ActivityLog{c1, c2, c3, c4}}
		for range 2 {
			if err := s.Write(ctx, b); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
		}
		r := s.ActivityLogs()
		got, err := r.Get(ctx, org, c1.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		wantLog(t, "Get()", got, c1)
		if _, err := r.Get(ctx, org, c4.ID); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get() of another organization error got = %v, want = %v", err, core.ErrNotFound)
		}
		list := func(f auditlog.ActivityLogFilter) []uuid.UUID {
			return listLogs(t, 2, func(p core.PageRequest) (core.Page[*auditlog.ActivityLog], error) { return r.List(ctx, org, f, p) },
				func(l *auditlog.ActivityLog) uuid.UUID { return l.ID })
		}
		const src = "spec/queries/activity_logs_query_spec.rb:"
		_ = src                                                                                                                                                // the spec file of the source lines below
		wantIDs(t, "List(), by pages of two", list(auditlog.ActivityLogFilter{}), []uuid.UUID{c3.ID, c2.ID, c1.ID})                                            // source: src + "20, 59"
		wantIDs(t, "List() by id", list(auditlog.ActivityLogFilter{IDs: []uuid.UUID{c2.ID}}), []uuid.UUID{c2.ID})                                              // source: src + "94"
		wantIDs(t, "List() by types", list(auditlog.ActivityLogFilter{Types: []string{"customer.created", "invoice.created"}}), []uuid.UUID{c3.ID, c1.ID})     // source: src + "104"
		wantIDs(t, "List() by source", list(auditlog.ActivityLogFilter{Sources: []auditlog.ActivitySource{auditlog.ActivitySourceFront}}), []uuid.UUID{c2.ID}) // source: src + "114"
		wantIDs(t, "List() by user", list(auditlog.ActivityLogFilter{UserIDs: []core.UserID{user}}), []uuid.UUID{c2.ID})
		wantIDs(t, "List() by api key", list(auditlog.ActivityLogFilter{APIKeyIDs: []core.APIKeyID{key}}), []uuid.UUID{c1.ID})                                                 // source: src + "84"
		wantIDs(t, "List() by resource", list(auditlog.ActivityLogFilter{ResourceIDs: []uuid.UUID{c3.ResourceID}}), []uuid.UUID{c3.ID})                                        // source: src + "154"
		wantIDs(t, "List() by resource type", list(auditlog.ActivityLogFilter{ResourceTypes: []string{"invoice"}}), []uuid.UUID{c3.ID})                                        // source: src + "164"
		wantIDs(t, "List() by customer", list(auditlog.ActivityLogFilter{ExternalCustomerID: "c1"}), []uuid.UUID{c1.ID})                                                       // source: src + "134"
		wantIDs(t, "List() by subscription", list(auditlog.ActivityLogFilter{ExternalSubscriptionID: "s1"}), []uuid.UUID{c2.ID})                                               // source: src + "144"
		wantIDs(t, "List() range", list(auditlog.ActivityLogFilter{LoggedAt: auditlog.Range{From: new(logAt(time.Minute)), To: new(logAt(time.Minute))}}), []uuid.UUID{c2.ID}) // source: src + "71"
	})

	t.Run("bad token", func(t *testing.T) {
		_, err := s.ActivityLogs().List(ctx, org, auditlog.ActivityLogFilter{}, core.PageRequest{Token: "x"})
		if !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("List() with a bad token error got = %v, want = %v", err, core.ErrInvalidValue)
		}
	})
}
