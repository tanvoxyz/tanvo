package invoicing_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// fcAt is the instant the fixed charges tests bill at: the start of a
// month, as the specs' timestamp.
var fcAt = billingtest.Date(2026, time.March, 1)

// fcFixture is a fixture whose service resolves the gated subscriptions
// with the lifecycle service, and a pay-in-advance fixed charge of 10.00
// per unit on its plan.
type fcFixture struct {
	*fixture
	fc *catalog.FixedCharge
}

func newFCFixture(t *testing.T, opts ...func(*catalog.FixedCharge)) *fcFixture {
	t.Helper()
	f := newFixture(t, fcAt)
	f.svc.WithStatusResolver(lifecycle.New(f.Deps).WithBiller(f.svc))
	a := f.AddOn(1000)
	fc := f.FixedCharge(f.plan, a, catalog.FixedChargeModelStandard, `{"amount":"10"}`, "10",
		append([]func(*catalog.FixedCharge){func(fc *catalog.FixedCharge) { fc.PayInAdvance = true }}, opts...)...)
	return &fcFixture{fixture: f, fc: fc}
}

// event records the units of fc on s, applied immediately at ts.
func (f *fcFixture) event(s *subscription.Subscription, fc *catalog.FixedCharge, units string, ts time.Time) {
	f.T.Helper()
	ev := &subscription.FixedChargeEvent{
		ID: core.NewID[core.FixedChargeEventID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: s.ID,
		FixedChargeID: fc.ID, Units: money.MustParse(units), Timestamp: ts, ApplyUnitsImmediately: true,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.FixedChargeEvents.Record(ctx, ev, time.UTC) })
}

// gated makes s incomplete with a pending payment rule.
func (f *fcFixture) gated(s *subscription.Subscription) *subscription.ActivationRule {
	f.T.Helper()
	f.UpdateSubscription(s, func(s *subscription.Subscription) { s.Status = subscription.StatusIncomplete })
	rule := subscription.NewActivationRule(f.OrgID(), core.NewID[core.ActivationRuleID](f.IDs), s.ID, subscription.ActivationRuleTypePayment, 48, f.Now())
	rule.Status = subscription.ActivationRuleStatusPending
	f.Do(func(ctx context.Context, r port.Repos) error { return r.ActivationRules.Create(ctx, rule) })
	return rule
}

func (f *fcFixture) run(s *subscription.Subscription) *invoice.Invoice {
	f.T.Helper()
	inv, err := f.svc.InAdvanceFixedCharges(f.Ctx, f.OrgID(), kernel.InAdvanceFixedChargesArgs{SubscriptionID: s.ID, Timestamp: fcAt})
	if err != nil {
		f.T.Fatalf("bill in-advance fixed charges: %v", err)
	}
	return inv
}

func (f *fcFixture) ruleStatus(id core.ActivationRuleID, s *subscription.Subscription) subscription.ActivationRuleStatus {
	f.T.Helper()
	rules := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.ActivationRule, error) {
		return r.ActivationRules.ListBySubscription(ctx, f.OrgID(), s.ID)
	})
	i := slices.IndexFunc(rules, func(r *subscription.ActivationRule) bool { return r.ID == id })
	return rules[i].Status
}

func (f *fcFixture) subscriptionStatus(s *subscription.Subscription) subscription.Status {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*subscription.Subscription, error) {
		return r.Subscriptions.Get(ctx, f.OrgID(), s.ID)
	}).Status
}

func TestInAdvanceFixedCharges(t *testing.T) {
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:41
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:74
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:98
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:124
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:132
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:138
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:150
	t.Run("bills the fees", func(t *testing.T) {
		f := newFCFixture(t)
		f.taxCustomer("20")
		po := "PO-SUB-123"
		s := f.subscribe(func(s *subscription.Subscription) { s.PurchaseOrderNumber = &po })
		f.event(s, f.fc, "10", fcAt)
		inv := f.run(s)
		if inv == nil {
			t.Fatal("invoice = nil, want = one")
		}
		inv = f.Invoice(inv.ID)
		if inv.Type != invoice.TypeSubscription || inv.Status != invoice.StatusFinalized || inv.PaymentStatus != core.PaymentStatusPending {
			t.Errorf("type, status, payment = %s, %s, %s, want = subscription, finalized, pending", inv.Type, inv.Status, inv.PaymentStatus)
		}
		if inv.IssuingDate != core.DateOf(fcAt, time.UTC) {
			t.Errorf("issuing date = %v, want = %v", inv.IssuingDate, fcAt)
		}
		if inv.PurchaseOrderNumber == nil || *inv.PurchaseOrderNumber != po {
			t.Errorf("purchase order number = %v, want = %s", inv.PurchaseOrderNumber, po)
		}
		checkAmounts(t, inv, 10000, 2000, 12000)
		fees := f.Fees(inv.ID)
		if len(fees) != 1 {
			t.Fatalf("fees = %d, want = 1", len(fees))
		}
		fe := fees[0]
		if fe.Type != core.FeeTypeFixedCharge || fe.FixedChargeID != f.fc.ID || !fe.PayInAdvance ||
			!fe.Units.Equal(money.NewFromInt(10)) || fe.UnitAmount != 1000 || fe.Amount != 10000 {
			t.Errorf("fee = %s %s in advance %t, %s units at %d = %d, want = fixed_charge %s in advance, 10 units at 1000 = 10000",
				fe.Type, fe.FixedChargeID, fe.PayInAdvance, fe.Units, fe.UnitAmount, fe.Amount, f.fc.ID)
		}
		iss := f.invoiceSubscriptions(inv.ID)
		if len(iss) != 1 || iss[0].Reason != invoice.InvoicingReasonInAdvanceCharge {
			t.Errorf("invoice subscriptions = %v, want = one in_advance_charge", iss)
		}
		hooks := f.Webhooks()
		if !slices.Contains(hooks, webhook.EventTypeInvoiceCreated) || !slices.Contains(hooks, webhook.EventTypeFeeCreated) {
			t.Errorf("webhooks = %v, want = invoice.created and fee.created", hooks)
		}
		if f.enqueued(kernel.JobCreateInvoicePayment) != 1 || f.enqueued(kernel.JobGenerateInvoiceDocument) != 1 {
			t.Errorf("jobs = %v, want = a payment and a document", f.JobKinds())
		}
	})

	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:189
	t.Run("multiple events", func(t *testing.T) {
		f := newFCFixture(t)
		fc2 := f.FixedCharge(f.plan, f.AddOn(2000), catalog.FixedChargeModelStandard, `{"amount":"20"}`, "5",
			func(fc *catalog.FixedCharge) { fc.PayInAdvance = true })
		s := f.subscribe()
		f.event(s, f.fc, "10", fcAt)
		f.event(s, fc2, "5", fcAt)
		inv := f.run(s)
		if inv == nil {
			t.Fatal("invoice = nil, want = one")
		}
		if got := len(f.Fees(inv.ID)); got != 2 {
			t.Errorf("fees = %d, want = 2", got)
		}
		checkAmounts(t, f.Invoice(inv.ID), 20000, 0, 20000)
	})

	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:212
	t.Run("zero total", func(t *testing.T) {
		f := newFCFixture(t)
		f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.FinalizeZeroAmountInvoice = customer.ZeroAmountFinalizationSkip })
		s := f.subscribe()
		f.event(s, f.fc, "0", fcAt)
		inv := f.run(s)
		if inv == nil {
			t.Fatal("invoice = nil, want = one")
		}
		inv = f.Invoice(inv.ID)
		if inv.Status != invoice.StatusClosed || inv.PaymentStatus != core.PaymentStatusSucceeded || inv.TotalAmount != 0 {
			t.Errorf("status, payment, total = %s, %s, %d, want = closed, succeeded, 0", inv.Status, inv.PaymentStatus, inv.TotalAmount)
		}
		if got := len(f.Fees(inv.ID)); got != 1 {
			t.Errorf("fees = %d, want = 1", got)
		}
		if f.enqueued(kernel.JobCreateInvoicePayment) != 0 {
			t.Errorf("jobs = %v, want = no payment", f.JobKinds())
		}
		if hooks := f.Webhooks(); slices.Contains(hooks, webhook.EventTypeInvoiceCreated) {
			t.Errorf("webhooks = %v, want = no invoice.created", hooks)
		}
	})

	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:156
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:167
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:178
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:680
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:699
	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:710
	for _, tt := range []struct {
		name   string
		status subscription.Status
		fcOpts []func(*catalog.FixedCharge)
		at     time.Time
		none   bool
	}{
		{name: "pending subscription", status: subscription.StatusPending, at: fcAt},
		{name: "no events", status: subscription.StatusActive, none: true},
		{name: "not pay in advance", status: subscription.StatusActive, at: fcAt, fcOpts: []func(*catalog.FixedCharge){func(fc *catalog.FixedCharge) { fc.PayInAdvance = false }}},
		{name: "timestamp mismatch", status: subscription.StatusActive, at: fcAt.AddDate(0, 0, 1)},
		{name: "terminated subscription", status: subscription.StatusTerminated, at: fcAt},
		{name: "canceled subscription", status: subscription.StatusCanceled, at: fcAt},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFCFixture(t, tt.fcOpts...)
			s := f.subscribe(func(s *subscription.Subscription) {
				s.Status = tt.status
				if tt.status == subscription.StatusPending {
					// The harness activates a subscription whose start came.
					s.SubscriptionAt = fcAt.AddDate(0, 1, 0)
				}
			})
			if !tt.none {
				f.event(s, f.fc, "10", tt.at)
			}
			if inv := f.run(s); inv != nil {
				t.Errorf("invoice = %s, want = nil", inv.ID)
			}
			if got := f.Invoices(f.cust.ID); len(got) != 0 {
				t.Errorf("invoices = %d, want = 0", len(got))
			}
		})
	}

	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:721
	t.Run("gated subscription", func(t *testing.T) {
		f := newFCFixture(t)
		s := f.subscribe()
		rule := f.gated(s)
		f.event(s, f.fc, "10", fcAt)
		inv := f.run(s)
		if inv == nil {
			t.Fatal("invoice = nil, want = one")
		}
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:728
		if got := f.Invoice(inv.ID).Status; got != invoice.StatusOpen {
			t.Errorf("status = %s, want = open", got)
		}
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:735
		if hooks := f.Webhooks(); slices.Contains(hooks, webhook.EventTypeInvoiceCreated) {
			t.Errorf("webhooks = %v, want = no invoice.created", hooks)
		}
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:741
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:747
		if f.enqueued(kernel.JobGenerateInvoiceDocument) != 0 || f.enqueued(kernel.JobCreateInvoicePayment) != 1 {
			t.Errorf("jobs = %v, want = a payment and no document", f.JobKinds())
		}
		if got := f.ruleStatus(rule.ID, s); got != subscription.ActivationRuleStatusPending {
			t.Errorf("rule = %s, want = pending", got)
		}
	})

	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:755
	t.Run("gated subscription with zero total", func(t *testing.T) {
		f := newFCFixture(t)
		s := f.subscribe()
		rule := f.gated(s)
		f.event(s, f.fc, "0", fcAt)
		inv := f.run(s)
		if inv == nil {
			t.Fatal("invoice = nil, want = one")
		}
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:762
		if got := f.ruleStatus(rule.ID, s); got != subscription.ActivationRuleStatusSatisfied {
			t.Errorf("rule = %s, want = satisfied", got)
		}
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:768
		if got := f.subscriptionStatus(s); got != subscription.StatusActive {
			t.Errorf("subscription = %s, want = active", got)
		}
		if got := f.Invoice(inv.ID).Status; got == invoice.StatusOpen {
			t.Errorf("status = %s, want = not open", got)
		}
	})

	// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:775
	t.Run("gated subscription with pending taxes", func(t *testing.T) {
		f := newFCFixture(t)
		f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
		f.Taxes.Compute = pendingTaxes
		s := f.subscribe()
		rule := f.gated(s)
		f.event(s, f.fc, "10", fcAt)
		inv := f.run(s)
		if inv == nil {
			t.Fatal("invoice = nil, want = one")
		}
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:782
		if got := f.ruleStatus(rule.ID, s); got != subscription.ActivationRuleStatusPending {
			t.Errorf("rule = %s, want = pending", got)
		}
		if got := f.subscriptionStatus(s); got != subscription.StatusIncomplete {
			t.Errorf("subscription = %s, want = incomplete", got)
		}
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:789
		if got := f.Invoice(inv.ID).Status; got != invoice.StatusOpen {
			t.Errorf("status = %s, want = open", got)
		}
	})
}
