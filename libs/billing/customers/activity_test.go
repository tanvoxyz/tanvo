package customers_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
)

func TestActivityLogs(t *testing.T) {
	t.Run("create, update and delete", func(t *testing.T) {
		t.Log("source: spec/services/customers/create_service_spec.rb:74")
		t.Log("source: spec/services/customers/update_service_spec.rb:67")
		t.Log("source: spec/services/customers/destroy_service_spec.rb:34")
		h, svc := setup(t)
		h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
		c, err := svc.Create(h.Ctx, h.OrgID(), "cus_1", customers.Input{Name: core.Some(new("Foo"))})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{Name: core.Some(new("Bar"))}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		// The portal update logs nothing (customer_portal/customer_update_service.rb is not activity_loggable).
		if _, err := svc.UpdateFromPortal(h.Ctx, h.OrgID(), c.ID, customers.Input{Name: core.Some(new("Baz"))}); err != nil {
			t.Fatalf("UpdateFromPortal: %v", err)
		}
		if _, err := svc.Delete(h.Ctx, h.OrgID(), c.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		id := c.ID.UUID()
		h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityCustomerCreated, ResourceType: auditlog.ResourceCustomer, ResourceID: id, ExternalCustomerID: "cus_1"},
			billingtest.ActivityWant{Type: auditlog.ActivityCustomerUpdated, ResourceType: auditlog.ResourceCustomer, ResourceID: id, ExternalCustomerID: "cus_1", Changes: []string{"name"}},
			billingtest.ActivityWant{Type: auditlog.ActivityCustomerDeleted, ResourceType: auditlog.ResourceCustomer, ResourceID: id, ExternalCustomerID: "cus_1"},
		)
	})

	t.Run("upsert", func(t *testing.T) {
		t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:107")
		t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:632")
		h, svc := setup(t)
		h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
		c, _, err := svc.Upsert(h.Ctx, h.OrgID(), "cus_1", customers.Input{Name: core.Some(new("Foo"))})
		if err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if _, _, err := svc.Upsert(h.Ctx, h.OrgID(), "cus_1", customers.Input{Name: core.Some(new("Bar"))}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		// The upsert produces customer.updated without a block, so without
		// changes (upsert_from_api_service.rb:161).
		id := c.ID.UUID()
		h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityCustomerCreated, ResourceType: auditlog.ResourceCustomer, ResourceID: id, ExternalCustomerID: "cus_1"},
			billingtest.ActivityWant{Type: auditlog.ActivityCustomerUpdated, ResourceType: auditlog.ResourceCustomer, ResourceID: id, ExternalCustomerID: "cus_1"},
		)
	})

	t.Run("a failed change logs nothing", func(t *testing.T) {
		h, svc := setup(t)
		h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
		if _, err := svc.Create(h.Ctx, h.OrgID(), "cus_1", customers.Input{BillingAddress: customers.Address{Country: core.Some("ZZ")}}); err == nil {
			t.Fatal("Create: error = nil, want an error")
		}
		h.WantActivities()
	})
}
