package creditnotes_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
)

func TestActivityLogs(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		t.Log("source: spec/services/credit_notes/create_service_spec.rb:356")
		f := newFixture(t)
		f.h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
		cn, err := f.svc.Create(f.h.Ctx, f.input(12, 0, item(f.fees[0], "10")))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		f.h.WantActivities(billingtest.ActivityWant{
			Type: auditlog.ActivityCreditNoteCreated, ResourceType: auditlog.ResourceCreditNote, ResourceID: cn.ID.UUID(), ExternalCustomerID: f.cust.ExternalID,
		})
	})

	t.Run("a rejected credit note logs nothing", func(t *testing.T) {
		f := newFixture(t)
		f.h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
		if _, err := f.svc.Create(f.h.Ctx, f.input(1000, 0, item(f.fees[0], "1000"))); err == nil {
			t.Fatal("create: error = nil, want an error")
		}
		f.h.WantActivities()
	})

	t.Run("refund failure", func(t *testing.T) {
		t.Log("source: spec/services/credit_notes/refunds/stripe_service_spec.rb:408")
		f := newRefundFixture(t, false)
		f.h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceSystem})
		if _, err := f.svc.Refund(f.h.Ctx, f.h.OrgID(), f.cn.ID); err != nil {
			t.Fatalf("refund: %v", err)
		}
		if _, err := f.svc.UpdateRefundStatus(f.h.Ctx, f.h.OrgID(), f.integration, "re_fake", core.RefundStatusFailed); err == nil {
			t.Fatal("update: error = nil, want the refund failure")
		}
		f.h.WantActivities(billingtest.ActivityWant{
			Type: auditlog.ActivityCreditNoteRefundFailure, ResourceType: auditlog.ResourceCreditNote, ResourceID: f.cn.ID.UUID(), ExternalCustomerID: f.cust.ExternalID,
		})
	})
}
