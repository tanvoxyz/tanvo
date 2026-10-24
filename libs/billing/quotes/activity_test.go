package quotes_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/quotes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
)

// newLoggedFix is newFix whose services record activity logs.
func newLoggedFix(t *testing.T) *fix {
	t.Helper()
	h := billingtest.New(t, testNow)
	h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceFront})
	e, err := billing.New(*h.Deps)
	if err != nil {
		t.Fatalf("billing.New() error = %v", err)
	}
	return &fix{h: h, svc: e.Quotes, cust: h.Customer()}
}

// onQuote is the expected log t on quote q.
func (f *fix) onQuote(t auditlog.ActivityType, q core.QuoteID, changes ...string) billingtest.ActivityWant {
	return billingtest.ActivityWant{Type: t, ResourceType: auditlog.ResourceQuote, ResourceID: q.UUID(), ExternalCustomerID: f.cust.ExternalID, Changes: changes}
}

func TestActivityLogs(t *testing.T) {
	t.Parallel()
	t.Run("quote and its versions", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quotes/create_service_spec.rb:59")
		t.Log("source: spec/services/quotes/create_service_spec.rb:65")
		t.Log("source: spec/services/quotes/update_service_spec.rb:32")
		t.Log("source: spec/services/quotes/update_service_spec.rb:41")
		t.Log("source: spec/services/quote_versions/update_service_spec.rb:40")
		t.Log("source: spec/services/quote_versions/clone_service_spec.rb:46")
		t.Log("source: spec/services/quote_versions/clone_service_spec.rb:96")
		t.Log("source: spec/services/quote_versions/void_service_spec.rb:35")
		f := newLoggedFix(t)
		q := f.quote(t, quote.OrderTypeOneOff, nil)
		id := q.Quote.ID
		if _, err := f.svc.Update(f.h.Ctx, f.h.OrgID(), id, quotes.UpdateInput{}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if _, err := f.svc.Update(f.h.Ctx, f.h.OrgID(), id, quotes.UpdateInput{Owners: core.Some([]core.MembershipID{})}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if _, err := f.svc.UpdateVersion(f.h.Ctx, f.h.OrgID(), id, q.Current.Version.ID, quotes.UpdateVersionInput{Content: core.Some(new("Terms"))}); err != nil {
			t.Fatalf("UpdateVersion() error = %v", err)
		}
		if _, err := f.svc.CloneVersion(f.h.Ctx, f.h.OrgID(), id, q.Current.Version.ID); err != nil {
			t.Fatalf("CloneVersion() error = %v", err)
		}
		f.h.WantActivities(
			f.onQuote(auditlog.ActivityQuoteCreated, id),
			f.onQuote(auditlog.ActivityQuoteUpdated, id),
			f.onQuote(auditlog.ActivityQuoteUpdated, id, "content"),
			f.onQuote(auditlog.ActivityQuoteVoided, id),
			f.onQuote(auditlog.ActivityQuoteVersionCreated, id),
		)
	})

	t.Run("approval, signature, execution", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:58")
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:64")
		t.Log("source: spec/services/order_forms/mark_as_signed_service_spec.rb:145")
		t.Log("source: spec/services/order_forms/mark_as_signed_service_spec.rb:151")
		t.Log("source: spec/services/order_forms/mark_as_signed_service_spec.rb:157")
		t.Log("source: spec/services/orders/one_off/execute_service_spec.rb:77")
		f := newLoggedFix(t)
		q, a := f.approved(t)
		sig, err := f.svc.SignOrderForm(f.h.Ctx, f.h.OrgID(), a.OrderForm.OrderForm.ID, quotes.SignInput{})
		if err != nil {
			t.Fatalf("SignOrderForm() error = %v", err)
		}
		o := sig.Order.Order
		if _, err := f.svc.ExecuteOrder(f.h.Ctx, f.h.OrgID(), o.ID, quote.ExecutionModeExecute); err != nil {
			t.Fatalf("ExecuteOrder() error = %v", err)
		}
		ext := f.cust.ExternalID
		form := a.OrderForm.OrderForm.ID.UUID()
		f.h.WantActivities(
			f.onQuote(auditlog.ActivityQuoteCreated, q.Quote.ID),
			f.onQuote(auditlog.ActivityQuoteApproved, q.Quote.ID),
			billingtest.ActivityWant{Type: auditlog.ActivityOrderFormCreated, ResourceType: auditlog.ResourceOrderForm, ResourceID: form, ExternalCustomerID: ext},
			billingtest.ActivityWant{Type: auditlog.ActivityOrderFormSigned, ResourceType: auditlog.ResourceOrderForm, ResourceID: form, ExternalCustomerID: ext},
			billingtest.ActivityWant{Type: auditlog.ActivityOrderCreated, ResourceType: auditlog.ResourceOrder, ResourceID: o.ID.UUID(), ExternalCustomerID: ext},
			billingtest.ActivityWant{Type: auditlog.ActivityOrderExecuted, ResourceType: auditlog.ResourceOrder, ResourceID: o.ID.UUID(), ExternalCustomerID: ext},
		)
		if got := f.h.Activities(auditlog.ActivityOrderFormFileUploaded); len(got) != 0 {
			t.Errorf("order_form.file_uploaded logs = %d, want = 0 without a signed document", len(got))
		}
	})

	t.Run("void order form", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/order_forms/void_service_spec.rb:103")
		t.Log("source: spec/services/order_forms/void_service_spec.rb:109")
		f := newLoggedFix(t)
		q, a := f.approved(t)
		id := a.OrderForm.OrderForm.ID
		if _, err := f.svc.VoidOrderForm(f.h.Ctx, f.h.OrgID(), id); err != nil {
			t.Fatalf("VoidOrderForm() error = %v", err)
		}
		f.h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityOrderFormVoided, ResourceType: auditlog.ResourceOrderForm, ResourceID: id.UUID(), ExternalCustomerID: f.cust.ExternalID},
			f.onQuote(auditlog.ActivityQuoteVoided, q.Quote.ID),
		)
	})

	t.Run("a failed change logs nothing", func(t *testing.T) {
		t.Parallel()
		f := newLoggedFix(t)
		q, _ := f.approved(t)
		if _, err := f.svc.VoidVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID); err == nil {
			t.Fatal("VoidVersion() error = nil, want not voidable")
		}
		if got := f.h.Activities(auditlog.ActivityQuoteVoided); len(got) != 0 {
			t.Errorf("quote.voided logs = %d, want = 0", len(got))
		}
	})
}
