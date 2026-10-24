package invoicing_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

// loggedFixture is a fixture whose services record activity logs.
func loggedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, billingDay)
	f.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceSystem})
	return f
}

// invoiceLog is the expected log t of invoice id of the fixture's customer.
func (f *fixture) invoiceLog(t auditlog.ActivityType, id core.InvoiceID) billingtest.ActivityWant {
	return billingtest.ActivityWant{Type: t, ResourceType: auditlog.ResourceInvoice, ResourceID: id.UUID(), ExternalCustomerID: f.cust.ExternalID}
}

func TestActivityLogs(t *testing.T) {
	t.Run("finalized subscription invoice", func(t *testing.T) {
		t.Log("source: spec/services/invoices/subscription_service_spec.rb:392")
		f := loggedFixture(t)
		inv := f.billOne(f.subscribe())
		f.WantActivities(f.invoiceLog(auditlog.ActivityInvoiceCreated, inv.ID))
	})

	t.Run("drafted subscription invoice, then finalized", func(t *testing.T) {
		t.Log("source: spec/services/invoices/subscription_service_spec.rb:583")
		t.Log("source: spec/services/invoices/subscription_service_spec.rb:595")
		t.Log("source: spec/services/invoices/refresh_draft_and_finalize_service_spec.rb:199")
		f := loggedFixture(t)
		f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
		inv := f.billOne(f.subscribe())
		if _, err := f.svc.FinalizeDraft(f.Ctx, f.OrgID(), inv.ID); err != nil {
			t.Fatalf("finalize: %v", err)
		}
		f.WantActivities(
			f.invoiceLog(auditlog.ActivityInvoiceDrafted, inv.ID),
			f.invoiceLog(auditlog.ActivityInvoiceReadyToFinalize, inv.ID),
			f.invoiceLog(auditlog.ActivityInvoiceCreated, inv.ID),
		)
	})

	t.Run("void and regenerate", func(t *testing.T) {
		t.Log("source: spec/services/invoices/void_service_spec.rb:97")
		t.Log("source: spec/services/invoices/regenerate_from_voided_service_spec.rb:362")
		f := loggedFixture(t)
		old := f.billOne(f.subscribe())
		if _, err := f.svc.Void(f.Ctx, f.OrgID(), old.ID, invoicing.VoidInput{}); err != nil {
			t.Fatalf("void: %v", err)
		}
		fee := f.Fees(old.ID)[0]
		inv, err := f.svc.Regenerate(f.Ctx, f.OrgID(), old.ID, invoicing.RegenerateInput{
			Fees: []invoicing.RegeneratedFee{{FeeID: fee.ID, Units: &fee.Units}},
		})
		if err != nil {
			t.Fatalf("regenerate: %v", err)
		}
		f.WantActivities(
			f.invoiceLog(auditlog.ActivityInvoiceCreated, old.ID),
			f.invoiceLog(auditlog.ActivityInvoiceVoided, old.ID),
			f.invoiceLog(auditlog.ActivityInvoiceCreated, inv.ID),
			f.invoiceLog(auditlog.ActivityInvoiceRegenerated, old.ID),
		)
	})

	t.Run("delete", func(t *testing.T) {
		t.Log("source: spec/services/invoices/delete_service_spec.rb:29")
		f := loggedFixture(t)
		inv := f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
		if err := f.svc.Delete(f.Ctx, f.OrgID(), inv.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		f.WantActivities(f.invoiceLog(auditlog.ActivityInvoiceDeleted, inv.ID))
	})

	t.Run("one-off invoice", func(t *testing.T) {
		t.Log("source: app/services/invoices/create_one_off_service.rb:24")
		f := loggedFixture(t)
		inv, err := f.svc.CreateOneOff(f.Ctx, invoicing.OneOffInput{
			OrganizationID: f.OrgID(), CustomerID: f.cust.ID, Fees: []fees.OneOff{{AddOnID: f.AddOn(1000).ID}},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		f.WantActivities(f.invoiceLog(auditlog.ActivityInvoiceOneOffCreated, inv.ID))
	})

	t.Run("a failed change logs nothing", func(t *testing.T) {
		f := loggedFixture(t)
		inv := f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
		if _, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{}); err == nil {
			t.Fatal("void a draft: error = nil, want an error")
		}
		f.WantActivities()
	})
}
