package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// testAuditLog checks the security, API and activity logs: round trips,
// isolation, the filters and the most-recent-first order.
func testAuditLog(t *testing.T, h Harness) {
	e := newEnv(t, h)
	a, b := e.twoOrgs()
	org, ob := a.OrganizationID, b.OrganizationID
	g := core.UUIDv7Generator{}
	user, key := core.NewID[core.UserID](g), core.NewID[core.APIKeyID](g)

	t.Run("security", func(t *testing.T) {
		sec := func(o core.OrganizationID, typ, event string, d time.Duration) *auditlog.SecurityLog {
			return &auditlog.SecurityLog{ID: g.NewUUID(), OrganizationID: o, Type: typ, Event: event, LoggedAt: at(d), CreatedAt: at(d)}
		}
		s1 := sec(org, "user", "user.signed_up", 0)
		s1.UserID = user
		s1.Resources = map[string]any{"email": "a@hooli.com", "n": 1.0}
		s1.DeviceInfo = map[string]any{"browser": "Firefox"}
		s2 := sec(org, "api_key", "api_key.created", time.Hour)
		s2.APIKeyID = key
		s3 := sec(org, "role", "role.created", time.Hour) // same instant as s2
		s4 := sec(ob, "user", "user.signed_up", 2*time.Hour)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, l := range []*auditlog.SecurityLog{s1, s2, s3, s4} {
				noErr(t, "Create()", r.SecurityLogs.Create(ctx, l))
			}
			got, err := r.SecurityLogs.Get(ctx, org, s1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, s1)
			_, err = r.SecurityLogs.Get(ctx, org, s4.ID)
			wantErr(t, "Get() of another organization", err, core.ErrNotFound)

			list := func(f auditlog.SecurityLogFilter) []uuid.UUID {
				return ids(listAll(t, 1, func(p core.PageRequest) (core.Page[*auditlog.SecurityLog], error) {
					return r.SecurityLogs.List(ctx, org, f, p)
				}), func(l *auditlog.SecurityLog) uuid.UUID { return l.ID })
			}
			tie := []uuid.UUID{s2.ID, s3.ID}
			if s3.ID.String() < s2.ID.String() {
				tie = []uuid.UUID{s3.ID, s2.ID}
			}
			equal(t, "List() order", list(auditlog.SecurityLogFilter{}), append(tie, s1.ID))
			equal(t, "List() by user", list(auditlog.SecurityLogFilter{UserIDs: []core.UserID{user}}), []uuid.UUID{s1.ID})
			equal(t, "List() by api key", list(auditlog.SecurityLogFilter{APIKeyIDs: []core.APIKeyID{key}}), []uuid.UUID{s2.ID})
			equal(t, "List() by types", list(auditlog.SecurityLogFilter{Types: []string{"user", "role"}}), []uuid.UUID{s3.ID, s1.ID})
			equal(t, "List() by event", list(auditlog.SecurityLogFilter{Events: []string{"api_key.created"}}), []uuid.UUID{s2.ID})
			equal(t, "List() up to", list(auditlog.SecurityLogFilter{LoggedAt: auditlog.Range{To: ptr(at(30 * time.Minute))}}), []uuid.UUID{s1.ID})
			equal(t, "List() from", list(auditlog.SecurityLogFilter{LoggedAt: auditlog.Range{From: ptr(at(time.Hour))}}), tie)
			return nil
		})
	})

	t.Run("api", func(t *testing.T) {
		api := func(o core.OrganizationID, method string, status int, path, client string, d time.Duration) *auditlog.APILog {
			return &auditlog.APILog{
				ID: g.NewUUID(), OrganizationID: o, APIKeyID: key, Client: client, HTTPMethod: method, HTTPStatus: status,
				RequestOrigin: "https://api.tanvo.test", RequestPath: path, APIVersion: "v1", LoggedAt: at(d), CreatedAt: at(d),
			}
		}
		a1 := api(org, "post", 200, "/v1/customers", "curl/8", 0)
		a1.RequestBody = map[string]any{"external_id": "c1"}
		a1.Response = map[string]any{"customer": map[string]any{"external_id": "c1"}}
		a2 := api(org, "delete", 404, "/v1/customers/c1", "sdk", time.Minute)
		a3 := api(org, "put", 422, "/v1/plans/p_1", "sdk", 2*time.Minute)
		a3.APIKeyID = core.NewID[core.APIKeyID](g)
		a4 := api(ob, "post", 200, "/v1/customers", "sdk", 3*time.Minute)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, l := range []*auditlog.APILog{a1, a2, a3, a4} {
				noErr(t, "Create()", r.APILogs.Create(ctx, l))
			}
			dup := *a1
			dup.HTTPStatus = 500
			noErr(t, "Create() of a stored request id", r.APILogs.Create(ctx, &dup))
			got, err := r.APILogs.Get(ctx, org, a1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, a1)
			_, err = r.APILogs.Get(ctx, ob, a1.ID)
			wantErr(t, "Get() of another organization", err, core.ErrNotFound)
			list := func(f auditlog.APILogFilter) []uuid.UUID {
				return ids(listAll(t, 2, func(p core.PageRequest) (core.Page[*auditlog.APILog], error) {
					return r.APILogs.List(ctx, org, f, p)
				}), func(l *auditlog.APILog) uuid.UUID { return l.ID })
			}
			equal(t, "List()", list(auditlog.APILogFilter{}), []uuid.UUID{a3.ID, a2.ID, a1.ID})
			equal(t, "List() by id", list(auditlog.APILogFilter{IDs: []uuid.UUID{a2.ID}}), []uuid.UUID{a2.ID})
			equal(t, "List() by key", list(auditlog.APILogFilter{APIKeyIDs: []core.APIKeyID{key}}), []uuid.UUID{a2.ID, a1.ID})
			equal(t, "List() by methods", list(auditlog.APILogFilter{HTTPMethods: []string{"post", "put"}}), []uuid.UUID{a3.ID, a1.ID})
			equal(t, "List() by status", list(auditlog.APILogFilter{HTTPStatuses: []int{404}}), []uuid.UUID{a2.ID})
			equal(t, "List() succeeded", list(auditlog.APILogFilter{Succeeded: true, HTTPStatuses: []int{404}}), []uuid.UUID{a1.ID})
			equal(t, "List() failed", list(auditlog.APILogFilter{Failed: true}), []uuid.UUID{a3.ID, a2.ID})
			equal(t, "List() by path", list(auditlog.APILogFilter{RequestPaths: []string{"/v1/customers/*"}}), []uuid.UUID{a2.ID})
			equal(t, "List() by exact path", list(auditlog.APILogFilter{RequestPaths: []string{"/v1/plans/p_1"}}), []uuid.UUID{a3.ID})
			equal(t, "List() underscore is literal", list(auditlog.APILogFilter{RequestPaths: []string{"/v1/plans/p_"}}), []uuid.UUID{})
			equal(t, "List() by client", list(auditlog.APILogFilter{Clients: []string{"curl/8"}}), []uuid.UUID{a1.ID})
			equal(t, "List() by version", list(auditlog.APILogFilter{APIVersion: "v2"}), []uuid.UUID{})
			return nil
		})
	})

	t.Run("activity", func(t *testing.T) {
		act := func(o core.OrganizationID, typ string, src auditlog.ActivitySource, d time.Duration) *auditlog.ActivityLog {
			return &auditlog.ActivityLog{
				ID: g.NewUUID(), OrganizationID: o, Type: typ, Source: src, ResourceID: g.NewUUID(), ResourceType: "customer",
				LoggedAt: at(d), CreatedAt: at(d),
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
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, l := range []*auditlog.ActivityLog{c1, c2, c3, c4} {
				noErr(t, "Create()", r.ActivityLogs.Create(ctx, l))
			}
			got, err := r.ActivityLogs.Get(ctx, org, c1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, c1)
			_, err = r.ActivityLogs.Get(ctx, org, c4.ID)
			wantErr(t, "Get() of another organization", err, core.ErrNotFound)
			list := func(f auditlog.ActivityLogFilter) []uuid.UUID {
				return ids(listAll(t, 2, func(p core.PageRequest) (core.Page[*auditlog.ActivityLog], error) {
					return r.ActivityLogs.List(ctx, org, f, p)
				}), func(l *auditlog.ActivityLog) uuid.UUID { return l.ID })
			}
			equal(t, "List()", list(auditlog.ActivityLogFilter{}), []uuid.UUID{c3.ID, c2.ID, c1.ID})
			equal(t, "List() by id", list(auditlog.ActivityLogFilter{IDs: []uuid.UUID{c2.ID}}), []uuid.UUID{c2.ID})
			equal(t, "List() by types", list(auditlog.ActivityLogFilter{Types: []string{"customer.created", "invoice.created"}}), []uuid.UUID{c3.ID, c1.ID})
			equal(t, "List() by source", list(auditlog.ActivityLogFilter{Sources: []auditlog.ActivitySource{auditlog.ActivitySourceFront}}), []uuid.UUID{c2.ID})
			equal(t, "List() by user", list(auditlog.ActivityLogFilter{UserIDs: []core.UserID{user}}), []uuid.UUID{c2.ID})
			equal(t, "List() by api key", list(auditlog.ActivityLogFilter{APIKeyIDs: []core.APIKeyID{key}}), []uuid.UUID{c1.ID})
			equal(t, "List() by resource", list(auditlog.ActivityLogFilter{ResourceIDs: []uuid.UUID{c3.ResourceID}}), []uuid.UUID{c3.ID})
			equal(t, "List() by resource type", list(auditlog.ActivityLogFilter{ResourceTypes: []string{"invoice"}}), []uuid.UUID{c3.ID})
			equal(t, "List() by customer", list(auditlog.ActivityLogFilter{ExternalCustomerID: "c1"}), []uuid.UUID{c1.ID})
			equal(t, "List() by subscription", list(auditlog.ActivityLogFilter{ExternalSubscriptionID: "s1"}), []uuid.UUID{c2.ID})
			equal(t, "List() range", list(auditlog.ActivityLogFilter{LoggedAt: auditlog.Range{From: ptr(at(time.Minute)), To: ptr(at(time.Minute))}}), []uuid.UUID{c2.ID})
			return nil
		})
	})
}
