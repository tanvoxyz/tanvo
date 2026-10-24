package billing_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
)

// newLoggedScenario is newScenario whose engine records activity logs.
func newLoggedScenario(t *testing.T, now time.Time) *scenario {
	t.Helper()
	h := billingtest.New(t, now)
	h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceSystem})
	e, err := billing.New(*h.Deps)
	if err != nil {
		t.Fatalf("billing.New() error = %v", err)
	}
	return &scenario{Harness: h, E: e}
}

func TestActivityLogs(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	t.Run("invoice payment overdue", func(t *testing.T) {
		t.Log("source: spec/services/invoices/payments/mark_overdue_service_spec.rb:34")
		s := newLoggedScenario(t, now)
		c := s.Customer()
		inv := s.StoredInvoice(c, func(inv *invoice.Invoice) {
			inv.Status, inv.PaymentDueDate = invoice.StatusFinalized, core.DateOf(now, time.UTC).AddDays(-1)
		})
		_, err := s.E.MarkInvoiceOverdue(s.Ctx, s.OrgID(), inv.ID)
		s.must(err)
		s.WantActivities(billingtest.ActivityWant{
			Type: auditlog.ActivityInvoicePaymentOverdue, ResourceType: auditlog.ResourceInvoice, ResourceID: inv.ID.UUID(), ExternalCustomerID: c.ExternalID,
		})
	})

	t.Run("order form expired", func(t *testing.T) {
		t.Log("source: spec/services/order_forms/expire_service_spec.rb:150")
		t.Log("source: spec/services/order_forms/expire_service_spec.rb:156")
		s := newLoggedScenario(t, now)
		c := s.Customer()
		yesterday := now.AddDate(0, 0, -1)
		v, f := s.quoteFixture(c, &yesterday, quote.OrderFormStatusGenerated)
		_, err := s.E.ExpireOrderForm(s.Ctx, s.OrgID(), f.ID)
		s.must(err)
		s.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityOrderFormExpired, ResourceType: auditlog.ResourceOrderForm, ResourceID: f.ID.UUID(), ExternalCustomerID: c.ExternalID},
			// A version's log is on its quote.
			billingtest.ActivityWant{Type: auditlog.ActivityQuoteVoided, ResourceType: auditlog.ResourceQuote, ResourceID: v.QuoteID.UUID(), ExternalCustomerID: c.ExternalID},
		)
	})

	t.Run("an expired order form logs nothing again", func(t *testing.T) {
		t.Log("source: spec/services/order_forms/expire_service_spec.rb:90")
		s := newLoggedScenario(t, now)
		yesterday := now.AddDate(0, 0, -1)
		_, f := s.quoteFixture(s.Customer(), &yesterday, quote.OrderFormStatusExpired)
		_, err := s.E.ExpireOrderForm(s.Ctx, s.OrgID(), f.ID)
		s.must(err)
		s.WantActivities()
	})
}
