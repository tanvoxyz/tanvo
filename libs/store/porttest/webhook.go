package porttest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// webhookFixtureSecret is a valid Standard Webhooks secret (24 key bytes).
const webhookFixtureSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

// webhookFixtureEndpoint returns an endpoint of org, not stored.
func webhookFixtureEndpoint(e *env, org core.OrganizationID, url string, types []webhook.EventType) *webhook.Endpoint {
	return &webhook.Endpoint{
		ID: core.NewID[core.WebhookEndpointID](e.ids), OrganizationID: org, URL: url, EventTypes: types,
		Secret: webhookFixtureSecret, CreatedAt: now, UpdatedAt: now,
	}
}

// webhookFixtureEvent returns a domain event of org about object, not
// stored.
func webhookFixtureEvent(e *env, org core.OrganizationID, t webhook.EventType, object uuid.UUID, occurred time.Time) *webhook.Event {
	return &webhook.Event{
		ID: core.NewID[core.AuditEventID](e.ids), OrganizationID: org, Type: t, ObjectType: "invoice", ObjectID: object,
		// Key order and spacing are kept verbatim.
		Payload:    json.RawMessage(`{"webhook_type": "invoice.created", "object_type":"invoice","invoice":{"lago_id":"x","total":100}}`),
		OccurredAt: occurred,
	}
}

func webhookEndpointID(e *webhook.Endpoint) core.WebhookEndpointID { return e.ID }

func webhookDeliveryID(d *webhook.Delivery) core.WebhookID { return d.ID }

func domainEventID(e *webhook.Event) core.AuditEventID { return e.ID }

func testWebhook(t *testing.T, h Harness) {
	t.Run("webhook endpoints", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		all := webhookFixtureEndpoint(e, org, "https://a.test/hooks", nil)
		all.Name = ptr("All events")
		invoices := webhookFixtureEndpoint(e, org, "https://b.test/hooks", []webhook.EventType{webhook.EventTypeInvoiceCreated, webhook.EventTypeInvoiceVoided})
		none := webhookFixtureEndpoint(e, org, "https://c.test/hooks", []webhook.EventType{})
		otherAll := webhookFixtureEndpoint(e, other, "https://a.test/hooks", nil)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, ep := range []*webhook.Endpoint{all, invoices, none, otherAll} {
				noErr(t, "Create()", r.WebhookEndpoints.Create(ctx, ep))
			}
			wantErr(t, "Create() with the same ID", r.WebhookEndpoints.Create(ctx, all), core.ErrConflict)
			for _, ep := range []*webhook.Endpoint{all, invoices, none} {
				got, err := r.WebhookEndpoints.Get(ctx, org, ep.ID)
				noErr(t, "Get()", err)
				equal(t, "Get()", got, ep)
			}
			got, err := r.WebhookEndpoints.Get(ctx, org, all.ID)
			noErr(t, "Get() of every type", err)
			if got.EventTypes != nil {
				t.Errorf("Get() event types got = %v, want = nil", got.EventTypes)
			}
			got, err = r.WebhookEndpoints.Get(ctx, org, none.ID)
			noErr(t, "Get() of no type", err)
			if got.EventTypes == nil || len(got.EventTypes) != 0 {
				t.Errorf("Get() event types got = %#v, want = []", got.EventTypes)
			}
			_, err = r.WebhookEndpoints.Get(ctx, org, core.NewID[core.WebhookEndpointID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			subscribed, err := r.WebhookEndpoints.ListSubscribed(ctx, org, webhook.EventTypeInvoiceCreated)
			noErr(t, "ListSubscribed()", err)
			equal(t, "ListSubscribed()", ids(subscribed, webhookEndpointID), []core.WebhookEndpointID{all.ID, invoices.ID})
			subscribed, err = r.WebhookEndpoints.ListSubscribed(ctx, org, webhook.EventTypeCustomerCreated)
			noErr(t, "ListSubscribed() of an unlisted type", err)
			equal(t, "ListSubscribed() of an unlisted type", subscribed, []*webhook.Endpoint{all})
			n, err := r.WebhookEndpoints.Count(ctx, org)
			noErr(t, "Count()", err)
			if n != 3 {
				t.Errorf("Count() got = %d, want = 3", n)
			}
			list := listAll(t, 2, func(p core.PageRequest) (core.Page[*webhook.Endpoint], error) {
				return r.WebhookEndpoints.List(ctx, org, p)
			})
			equal(t, "List()", list, []*webhook.Endpoint{all, invoices, none})

			// Isolation.
			_, err = r.WebhookEndpoints.Get(ctx, other, all.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			list = listAll(t, 2, func(p core.PageRequest) (core.Page[*webhook.Endpoint], error) {
				return r.WebhookEndpoints.List(ctx, other, p)
			})
			equal(t, "List() of another organization", list, []*webhook.Endpoint{otherAll})
			n, err = r.WebhookEndpoints.Count(ctx, other)
			noErr(t, "Count() of another organization", err)
			if n != 1 {
				t.Errorf("Count() of another organization got = %d, want = 1", n)
			}
			subscribed, err = r.WebhookEndpoints.ListSubscribed(ctx, other, webhook.EventTypeInvoiceVoided)
			noErr(t, "ListSubscribed() of another organization", err)
			equal(t, "ListSubscribed() of another organization", subscribed, []*webhook.Endpoint{otherAll})
			moved := *all
			moved.OrganizationID = other
			moved.URL = "https://moved.test"
			wantErr(t, "Update() through another organization", r.WebhookEndpoints.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Delete() from another organization", r.WebhookEndpoints.Delete(ctx, other, all.ID), core.ErrNotFound)

			// The URL is unique per organization.
			wantErr(t, "Create() with a taken URL", r.WebhookEndpoints.Create(ctx, webhookFixtureEndpoint(e, org, "https://a.test/hooks", nil)), core.ErrConflict)
			invoices.URL = "https://a.test/hooks"
			wantErr(t, "Update() to a taken URL", r.WebhookEndpoints.Update(ctx, invoices), core.ErrConflict)
			invoices.URL = "https://b2.test/hooks"
			invoices.Name = ptr("Invoices")
			invoices.EventTypes = nil
			invoices.Secret = "whsec_" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			invoices.UpdatedAt = at(time.Minute)
			noErr(t, "Update() after conflicts", r.WebhookEndpoints.Update(ctx, invoices))
			got, err = r.WebhookEndpoints.Get(ctx, org, invoices.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, invoices)
			subscribed, err = r.WebhookEndpoints.ListSubscribed(ctx, org, webhook.EventTypeCustomerCreated)
			noErr(t, "ListSubscribed() after Update()", err)
			equal(t, "ListSubscribed() after Update()", ids(subscribed, webhookEndpointID), []core.WebhookEndpointID{all.ID, invoices.ID})
			all.Name = nil
			all.EventTypes = []webhook.EventType{}
			noErr(t, "Update() to no type", r.WebhookEndpoints.Update(ctx, all))
			subscribed, err = r.WebhookEndpoints.ListSubscribed(ctx, org, webhook.EventTypeCustomerCreated)
			noErr(t, "ListSubscribed() after Update() to no type", err)
			equal(t, "ListSubscribed() after Update() to no type", ids(subscribed, webhookEndpointID), []core.WebhookEndpointID{invoices.ID})
			wantErr(t, "Update() unknown", r.WebhookEndpoints.Update(ctx, webhookFixtureEndpoint(e, org, "https://d.test", nil)), core.ErrNotFound)

			noErr(t, "Delete()", r.WebhookEndpoints.Delete(ctx, org, none.ID))
			wantErr(t, "Delete() again", r.WebhookEndpoints.Delete(ctx, org, none.ID), core.ErrNotFound)
			n, err = r.WebhookEndpoints.Count(ctx, org)
			noErr(t, "Count() after Delete()", err)
			if n != 2 {
				t.Errorf("Count() after Delete() got = %d, want = 2", n)
			}
			return nil
		})
	})

	t.Run("webhook deliveries", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		ep1 := webhookFixtureEndpoint(e, org, "https://a.test/hooks", nil)
		ep2 := webhookFixtureEndpoint(e, org, "https://b.test/hooks", nil)
		epOther := webhookFixtureEndpoint(e, other, "https://a.test/hooks", nil)
		object := uuid.New()
		ev := webhookFixtureEvent(e, org, webhook.EventTypeInvoiceCreated, object, now)
		evVoided := webhookFixtureEvent(e, org, webhook.EventTypeInvoiceVoided, object, now)
		evOther := webhookFixtureEvent(e, other, webhook.EventTypeInvoiceCreated, object, now)
		d1 := webhook.NewDelivery(core.NewID[core.WebhookID](e.ids), ep1.ID, ev, now)
		d2 := webhook.NewDelivery(core.NewID[core.WebhookID](e.ids), ep1.ID, evVoided, at(time.Second))
		d3 := webhook.NewDelivery(core.NewID[core.WebhookID](e.ids), ep1.ID, ev, at(2*time.Second))
		d4 := webhook.NewDelivery(core.NewID[core.WebhookID](e.ids), ep2.ID, ev, at(-time.Second))
		dOther := webhook.NewDelivery(core.NewID[core.WebhookID](e.ids), epOther.ID, evOther, at(-2*time.Second))
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, ep := range []*webhook.Endpoint{ep1, ep2, epOther} {
				noErr(t, "WebhookEndpoints.Create()", r.WebhookEndpoints.Create(ctx, ep))
			}
			for _, d := range []*webhook.Delivery{d1, d2, d3, d4, dOther} {
				noErr(t, "Create()", r.WebhookDeliveries.Create(ctx, d))
			}
			wantErr(t, "Create() with the same ID", r.WebhookDeliveries.Create(ctx, d1), core.ErrConflict)
			got, err := r.WebhookDeliveries.Get(ctx, org, d1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, d1)
			if string(got.Payload) != string(ev.Payload) {
				t.Errorf("Get() payload got = %s, want = %s (verbatim)", got.Payload, ev.Payload)
			}
			_, err = r.WebhookDeliveries.Get(ctx, org, core.NewID[core.WebhookID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.WebhookDeliveries.Get(ctx, other, d1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)

			// A failed attempt sets every optional field.
			noErr(t, "Fail()", d2.Fail(ptr(503), "upstream unavailable", 0.5, at(time.Minute)))
			noErr(t, "Update()", r.WebhookDeliveries.Update(ctx, d2))
			got, err = r.WebhookDeliveries.Get(ctx, org, d2.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, d2)
			noErr(t, "Succeed()", d3.Succeed(200, []byte("ok"), at(time.Minute)))
			noErr(t, "Update() succeeded", r.WebhookDeliveries.Update(ctx, d3))
			got, err = r.WebhookDeliveries.Get(ctx, org, d3.ID)
			noErr(t, "Get() succeeded", err)
			equal(t, "Get() succeeded", got, d3)
			moved := *d1
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.WebhookDeliveries.Update(ctx, &moved), core.ErrNotFound)
			missing := webhook.NewDelivery(core.NewID[core.WebhookID](e.ids), ep1.ID, ev, now)
			wantErr(t, "Update() unknown", r.WebhookDeliveries.Update(ctx, missing), core.ErrNotFound)

			list := listAll(t, 1, func(p core.PageRequest) (core.Page[*webhook.Delivery], error) {
				return r.WebhookDeliveries.ListByEndpoint(ctx, org, ep1.ID, webhook.DeliveryFilter{}, p)
			})
			equal(t, "ListByEndpoint()", list, []*webhook.Delivery{d1, d2, d3})
			for _, tc := range []struct {
				name string
				f    webhook.DeliveryFilter
				want []core.WebhookID
			}{
				{"statuses", webhook.DeliveryFilter{Statuses: []webhook.Status{webhook.StatusPending, webhook.StatusSucceeded}}, []core.WebhookID{d1.ID, d3.ID}},
				{"event types", webhook.DeliveryFilter{EventTypes: []webhook.EventType{webhook.EventTypeInvoiceVoided}}, []core.WebhookID{d2.ID}},
				{"both", webhook.DeliveryFilter{Statuses: []webhook.Status{webhook.StatusSucceeded}, EventTypes: []webhook.EventType{webhook.EventTypeInvoiceCreated}}, []core.WebhookID{d3.ID}},
				{"no match", webhook.DeliveryFilter{Statuses: []webhook.Status{webhook.StatusFailed}}, []core.WebhookID{}},
			} {
				got := listAll(t, 2, func(p core.PageRequest) (core.Page[*webhook.Delivery], error) {
					return r.WebhookDeliveries.ListByEndpoint(ctx, org, ep1.ID, tc.f, p)
				})
				equal(t, "ListByEndpoint() by "+tc.name, ids(got, webhookDeliveryID), tc.want)
			}
			p, err := r.WebhookDeliveries.ListByEndpoint(ctx, other, ep1.ID, webhook.DeliveryFilter{}, core.PageRequest{})
			noErr(t, "ListByEndpoint() from another organization", err)
			equal(t, "ListByEndpoint() from another organization", p.Items, []*webhook.Delivery{})
			return nil
		})

		// ListDue and PurgeBefore work across organizations. Other tests'
		// deliveries share the database, so the assertions keep this
		// test's deliveries only.
		mine := map[core.WebhookID]bool{d1.ID: true, d2.ID: true, d3.ID: true, d4.ID: true, dOther.ID: true}
		keep := func(ds []*webhook.Delivery) []core.WebhookID {
			out := []core.WebhookID{}
			for _, d := range ds {
				if mine[d.ID] {
					out = append(out, d.ID)
				}
			}
			return out
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			// Due at now: dOther (now−2s), d4 (now−1s), d1 (now); d2 is
			// retrying, due later; d3 succeeded.
			due, err := r.WebhookDeliveries.ListDue(ctx, now, 0)
			noErr(t, "ListDue()", err)
			equal(t, "ListDue()", keep(due), []core.WebhookID{dOther.ID, d4.ID, d1.ID})
			for _, d := range due {
				if !d.Due(now) {
					t.Errorf("ListDue() got delivery %v not due at %v", d.ID, now)
				}
			}
			due, err = r.WebhookDeliveries.ListDue(ctx, at(time.Hour), 0)
			noErr(t, "ListDue() later", err)
			equal(t, "ListDue() later", keep(due), []core.WebhookID{dOther.ID, d4.ID, d1.ID, d2.ID})
			due, err = r.WebhookDeliveries.ListDue(ctx, at(-time.Second-time.Millisecond), 0)
			noErr(t, "ListDue() earlier", err)
			equal(t, "ListDue() earlier", keep(due), []core.WebhookID{dOther.ID})
			due, err = r.WebhookDeliveries.ListDue(ctx, now, 1)
			noErr(t, "ListDue() with a limit", err)
			if len(due) != 1 {
				t.Errorf("ListDue() with a limit got %d deliveries, want = 1", len(due))
			}
			return nil
		})

		e.tx(func(ctx context.Context, r port.Repos) error {
			// Deliveries created before the cutoff go, in every organization.
			_, err := r.WebhookDeliveries.PurgeBefore(ctx, now)
			noErr(t, "PurgeBefore()", err)
			for _, x := range []struct {
				org  core.OrganizationID
				id   core.WebhookID
				gone bool
			}{{org, d4.ID, true}, {other, dOther.ID, true}, {org, d1.ID, false}, {org, d2.ID, false}, {org, d3.ID, false}} {
				_, err := r.WebhookDeliveries.Get(ctx, x.org, x.id)
				if x.gone {
					wantErr(t, "Get() purged", err, core.ErrNotFound)
				} else {
					noErr(t, "Get() kept", err)
				}
			}

			// Deleting an endpoint deletes its deliveries.
			noErr(t, "WebhookEndpoints.Delete()", r.WebhookEndpoints.Delete(ctx, org, ep1.ID))
			_, err = r.WebhookDeliveries.Get(ctx, org, d1.ID)
			wantErr(t, "Get() of a deleted endpoint", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("domain events", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		object, another := uuid.New(), uuid.New()
		e1 := webhookFixtureEvent(e, org, webhook.EventTypeInvoiceCreated, object, now)
		e2 := webhookFixtureEvent(e, org, webhook.EventTypeInvoiceGenerated, object, at(time.Second))
		e3 := webhookFixtureEvent(e, org, webhook.EventTypeInvoiceCreated, another, at(2*time.Second))
		e4 := webhookFixtureEvent(e, org, webhook.EventTypeInvoiceVoided, object, at(3*time.Second))
		e5 := webhookFixtureEvent(e, org, webhook.EventTypeCustomerCreated, object, at(4*time.Second))
		e5.ObjectType = "customer"
		eOther := webhookFixtureEvent(e, other, webhook.EventTypeInvoiceCreated, object, now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, ev := range []*webhook.Event{e1, e2, e3, e4, e5, eOther} {
				noErr(t, "Record()", r.DomainEvents.Record(ctx, ev))
			}
			wantErr(t, "Record() of a recorded id", r.DomainEvents.Record(ctx, e1), core.ErrConflict)
			got, err := r.DomainEvents.Get(ctx, org, e1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, e1)
			if string(got.Payload) != string(e1.Payload) {
				t.Errorf("Get() payload got = %s, want = %s (verbatim)", got.Payload, e1.Payload)
			}
			_, err = r.DomainEvents.Get(ctx, org, core.NewID[core.AuditEventID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.DomainEvents.Get(ctx, other, e1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)

			list := listAll(t, 2, func(p core.PageRequest) (core.Page[*webhook.Event], error) {
				return r.DomainEvents.ListByObject(ctx, org, "invoice", object, p)
			})
			equal(t, "ListByObject()", list, []*webhook.Event{e1, e2, e4})
			list = listAll(t, 2, func(p core.PageRequest) (core.Page[*webhook.Event], error) {
				return r.DomainEvents.ListByObject(ctx, org, "customer", object, p)
			})
			equal(t, "ListByObject() of another object type", ids(list, domainEventID), []core.AuditEventID{e5.ID})
			list = listAll(t, 2, func(p core.PageRequest) (core.Page[*webhook.Event], error) {
				return r.DomainEvents.ListByObject(ctx, other, "invoice", object, p)
			})
			equal(t, "ListByObject() of another organization", ids(list, domainEventID), []core.AuditEventID{eOther.ID})
			list = listAll(t, 2, func(p core.PageRequest) (core.Page[*webhook.Event], error) {
				return r.DomainEvents.ListByObject(ctx, other, "invoice", another, p)
			})
			equal(t, "ListByObject() of another organization's object", list, []*webhook.Event{})
			return nil
		})
	})
}
