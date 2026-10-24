package deduct_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
)

func TestActivityLogs(t *testing.T) {
	t.Run("coupon and applied coupon lifecycle", func(t *testing.T) {
		t.Log("source: spec/services/coupons/create_service_spec.rb:38")
		t.Log("source: spec/services/coupons/update_service_spec.rb:48")
		t.Log("source: spec/services/coupons/destroy_service_spec.rb:35")
		t.Log("source: spec/services/applied_coupons/create_service_spec.rb:49")
		t.Log("source: spec/services/applied_coupons/terminate_service_spec.rb:24")
		f := newFixture(t)
		h := f.h
		h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
		c := newCoupon(h, "free-beer")
		mustOK(t, f.s.CreateCoupon(h.Ctx, c))
		_, err := f.s.UpdateCoupon(h.Ctx, h.OrgID(), c.ID, func(c *coupon.Coupon) { c.Name = "Free wine" })
		mustOK(t, err)
		a, err := f.s.ApplyCoupon(h.Ctx, deduct.ApplyCouponInput{OrganizationID: h.OrgID(), CouponID: c.ID, CustomerID: f.cust.ID})
		mustOK(t, err)
		_, err = f.s.TerminateAppliedCoupon(h.Ctx, h.OrgID(), a.ID)
		mustOK(t, err)
		_, err = f.s.DestroyCoupon(h.Ctx, h.OrgID(), c.ID)
		mustOK(t, err)
		id, ext := c.ID.UUID(), f.cust.ExternalID
		h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityCouponCreated, ResourceType: auditlog.ResourceCoupon, ResourceID: id},
			billingtest.ActivityWant{Type: auditlog.ActivityCouponUpdated, ResourceType: auditlog.ResourceCoupon, ResourceID: id, Changes: []string{"name"}},
			billingtest.ActivityWant{Type: auditlog.ActivityAppliedCouponCreated, ResourceType: auditlog.ResourceCoupon, ResourceID: id, ExternalCustomerID: ext},
			billingtest.ActivityWant{Type: auditlog.ActivityAppliedCouponDeleted, ResourceType: auditlog.ResourceCoupon, ResourceID: id, ExternalCustomerID: ext},
			billingtest.ActivityWant{Type: auditlog.ActivityCouponDeleted, ResourceType: auditlog.ResourceCoupon, ResourceID: id},
		)
	})

	t.Run("destroying a coupon terminates its applied coupons", func(t *testing.T) {
		t.Log("source: app/services/coupons/destroy_service.rb:24-25 (AppliedCoupons::TerminateService per active applied coupon)")
		f := newFixture(t)
		h := f.h
		h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
		c := newCoupon(h, "free-beer")
		mustOK(t, f.s.CreateCoupon(h.Ctx, c))
		_, err := f.s.ApplyCoupon(h.Ctx, deduct.ApplyCouponInput{OrganizationID: h.OrgID(), CouponID: c.ID, CustomerID: f.cust.ID})
		mustOK(t, err)
		_, err = f.s.DestroyCoupon(h.Ctx, h.OrgID(), c.ID)
		mustOK(t, err)
		id := c.ID.UUID()
		h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityAppliedCouponDeleted, ResourceType: auditlog.ResourceCoupon, ResourceID: id, ExternalCustomerID: f.cust.ExternalID},
			billingtest.ActivityWant{Type: auditlog.ActivityCouponDeleted, ResourceType: auditlog.ResourceCoupon, ResourceID: id},
		)
	})

	t.Run("a failed change logs nothing", func(t *testing.T) {
		f := newFixture(t)
		f.h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
		c := newCoupon(f.h, "")
		if err := f.s.CreateCoupon(f.h.Ctx, c); err == nil {
			t.Fatal("CreateCoupon: error = nil, want an error")
		}
		f.h.WantActivities()
	})
}

func TestPrepaidCreditActivityLogs(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		t.Log("source: spec/services/credits/applied_prepaid_credits_service_spec.rb:123")
		sc := newPrepaidScene(t, prepaidCases()[0])
		h := sc.f.h
		h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceSystem})
		_, err := sc.apply(deduct.Options{})
		mustOK(t, err)
		// A transaction's log is on its wallet.
		h.WantActivities(billingtest.ActivityWant{
			Type: auditlog.ActivityWalletTransactionCreated, ResourceType: auditlog.ResourceWallet,
			ResourceID: sc.wallets["priority"].ID.UUID(), ExternalCustomerID: sc.f.cust.ExternalID,
		})
	})

	t.Run("a preview logs nothing", func(t *testing.T) {
		sc := newPrepaidScene(t, prepaidCases()[0])
		sc.f.h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceSystem})
		_, err := sc.apply(deduct.Options{Preview: true})
		mustOK(t, err)
		sc.f.h.WantActivities()
	})
}
