package invoicing_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// previewRequest previews req for the fixture's customer.
func (f *fixture) previewRequest(req invoicing.PreviewRequest) (*invoicing.InvoiceView, error) {
	f.T.Helper()
	req.OrganizationID = f.OrgID()
	if req.CustomerID.IsZero() && req.CustomerExternalID == "" {
		req.CustomerID = f.cust.ID
	}
	return f.svc.PreviewRequest(f.Ctx, req)
}

// subRefs returns the references of subs by external ID.
func subRefs(subs ...*subscription.Subscription) []refs.SubscriptionRef {
	out := make([]refs.SubscriptionRef, len(subs))
	for i, s := range subs {
		out[i] = refs.SubscriptionRef{ExternalID: s.ExternalID}
	}
	return out
}

// reload returns the stored subscription s.
func (f *fixture) reload(s *subscription.Subscription) *subscription.Subscription {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*subscription.Subscription, error) {
		return r.Subscriptions.Get(ctx, f.OrgID(), s.ID)
	})
}

// checkNothingStored fails when the preview left a record behind.
func (f *fixture) checkNothingStored(t *testing.T, subs int) {
	t.Helper()
	if got := len(f.Invoices(f.cust.ID)); got != 0 {
		t.Errorf("stored invoices = %d, want = 0", got)
	}
	all := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.Subscription, error) {
		return r.Subscriptions.ListByCustomer(ctx, f.OrgID(), f.cust.ID)
	})
	if got := len(all); got != subs {
		t.Errorf("stored subscriptions = %d, want = %d", got, subs)
	}
	acs := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*coupon.AppliedCoupon, error) {
		return r.AppliedCoupons.ListActiveByCustomer(ctx, f.OrgID(), f.cust.ID)
	})
	if got := len(acs); got != 0 {
		t.Errorf("stored applied coupons = %d, want = 0", got)
	}
}

// started subscribes the fixture's customer to its plan on 1 March 2024,
// before previewDay.
func (f *fixture) started() *subscription.Subscription {
	f.T.Helper()
	return f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2024, time.March, 1)))
}

// reasons returns the invoicing reasons of the view's subscriptions.
func reasons(v *invoicing.InvoiceView) []invoice.InvoicingReason {
	out := make([]invoice.InvoicingReason, len(v.Subscriptions))
	for i, s := range v.Subscriptions {
		out[i] = s.Subscription.Reason
	}
	return out
}

// source: spec/services/invoices/preview/build_subscription_service_spec.rb:18
// source: spec/services/invoices/preview/subscriptions_service_spec.rb:358
func TestPreviewRequestProposal(t *testing.T) {
	f := newFixture(t, previewDay, func(p *catalog.Plan) { p.Amount = 3100 })
	f.taxCustomer("50")
	v, err := f.previewRequest(invoicing.PreviewRequest{
		Subscription: &invoicing.PreviewSubscription{Plan: refs.ByCode(f.plan.Code)},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// Calendar billing by default: 30 and 31 March are billed.
	checkAmounts(t, v.Invoice, 200, 100, 300)
	if len(v.Subscriptions) != 1 || len(v.Fees) != 1 {
		t.Fatalf("subscriptions, fees = %d, %d, want = 1, 1", len(v.Subscriptions), len(v.Fees))
	}
	if got := v.Subscriptions[0].ExternalID; got != "" {
		t.Errorf("external ID = %q, want = none", got)
	}
	if got, want := v.Fees[0].CustomerID, f.cust.ID; got != want {
		t.Errorf("fee customer = %v, want = %v", got, want)
	}
	if got, want := v.Invoice.BillingEntityID, f.cust.BillingEntityID; got != want {
		t.Errorf("billing entity = %v, want = %v", got, want)
	}
	f.checkNothingStored(t, 0)
}

// source: spec/services/invoices/preview/subscriptions_service_spec.rb:6
// source: spec/services/invoices/preview/subscription_termination_service_spec.rb:6
// source: spec/services/invoices/preview/subscription_plan_change_service_spec.rb:6
// source: spec/services/billing_entities/resolve_service_spec.rb:6
func TestPreviewRequestRejections(t *testing.T) {
	type check func(t *testing.T, err error)
	notFound := func(resource string) check {
		return func(t *testing.T, err error) {
			t.Helper()
			var nf *core.NotFoundError
			if !errors.As(err, &nf) || nf.Resource != resource {
				t.Errorf("err = %v, want = %s not found", err, resource)
			}
		}
	}
	invalid := func(field, code string) check {
		return func(t *testing.T, err error) {
			t.Helper()
			var ve *core.ValidationError
			if !errors.As(err, &ve) || !ve.Has(field, code) {
				t.Errorf("err = %v, want = %s: %s", err, field, code)
			}
		}
	}
	future := previewDay.Add(48 * time.Hour)
	tests := []struct {
		name  string
		req   func(f *fixture) invoicing.PreviewRequest
		check check
	}{
		// source: spec/services/invoices/preview_context_service_spec.rb:107
		{"unknown customer", func(f *fixture) invoicing.PreviewRequest {
			return invoicing.PreviewRequest{CustomerExternalID: "unknown"}
		}, notFound("customer")},
		// source: spec/services/billing_entities/resolve_service_spec.rb:33
		{"unknown billing entity", func(f *fixture) invoicing.PreviewRequest {
			return invoicing.PreviewRequest{BillingEntity: refs.ByCode("unknown"), Subscription: &invoicing.PreviewSubscription{Plan: refs.ByCode(f.plan.Code)}}
		}, notFound("billing_entity")},
		// source: spec/services/invoices/preview/build_subscription_service_spec.rb:31
		{"unknown plan", func(f *fixture) invoicing.PreviewRequest {
			return invoicing.PreviewRequest{Subscription: &invoicing.PreviewSubscription{Plan: refs.ByCode("unknown")}}
		}, notFound("plan")},
		{"no plan", func(f *fixture) invoicing.PreviewRequest { return invoicing.PreviewRequest{} }, notFound("plan")},
		// source: spec/services/invoices/preview/find_subscriptions_service_spec.rb:12
		{"unknown subscription", func(f *fixture) invoicing.PreviewRequest {
			return invoicing.PreviewRequest{Subscriptions: []refs.SubscriptionRef{{ExternalID: "unknown"}}}
		}, notFound("subscription")},
		// source: spec/services/invoices/preview/subscriptions_service_spec.rb:278
		{"pending not in the future", func(f *fixture) invoicing.PreviewRequest {
			s := f.started()
			f.UpdateSubscription(s, func(s *subscription.Subscription) { s.Status = subscription.StatusPending })
			return invoicing.PreviewRequest{Subscriptions: subRefs(s)}
		}, notFound("subscription")},
		// source: spec/services/invoices/preview/subscriptions_service_spec.rb:149
		{"termination of two", func(f *fixture) invoicing.PreviewRequest {
			return invoicing.PreviewRequest{Subscriptions: subRefs(f.started(), f.started()), TerminatedAt: &future}
		}, invalid("subscription_ids", invoicing.CodeOnlyOneSubscriptionForTermination)},
		// source: spec/services/invoices/preview/subscriptions_service_spec.rb:63
		{"plan change of two", func(f *fixture) invoicing.PreviewRequest {
			return invoicing.PreviewRequest{Subscriptions: subRefs(f.started(), f.started()), TargetPlan: refs.ByCode(f.plan.Code)}
		}, invalid("subscription_ids", invoicing.CodeOnlyOneSubscriptionForPlanChange)},
		// source: spec/services/invoices/preview/subscription_termination_service_spec.rb:27
		{"termination in the past", func(f *fixture) invoicing.PreviewRequest {
			past := previewDay.Add(-time.Hour)
			return invoicing.PreviewRequest{Subscriptions: subRefs(f.started()), TerminatedAt: &past}
		}, invalid("terminated_at", invoicing.CodeCannotBeInPast)},
		// source: spec/services/invoices/preview/subscription_termination_service_spec.rb:40
		{"termination now", func(f *fixture) invoicing.PreviewRequest {
			now := previewDay
			return invoicing.PreviewRequest{Subscriptions: subRefs(f.started()), TerminatedAt: &now}
		}, invalid("terminated_at", invoicing.CodeCannotBeInPast)},
		// source: spec/services/invoices/preview/subscription_plan_change_service_spec.rb:21
		{"plan change to an unknown plan", func(f *fixture) invoicing.PreviewRequest {
			return invoicing.PreviewRequest{Subscriptions: subRefs(f.started()), TargetPlan: refs.ByCode("unknown")}
		}, notFound("plan")},
		// source: spec/services/invoices/preview/subscription_plan_change_service_spec.rb:37
		{"plan change to the same plan", func(f *fixture) invoicing.PreviewRequest {
			return invoicing.PreviewRequest{Subscriptions: subRefs(f.started()), TargetPlan: refs.ByCode(f.plan.Code)}
		}, invalid("plan_id", invoicing.CodeSamePlan)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, previewDay)
			_, err := f.previewRequest(tt.req(f))
			tt.check(t, err)
		})
	}
}

// source: spec/services/invoices/preview/subscription_termination_service_spec.rb:53
// source: spec/services/invoices/preview/subscriptions_service_spec.rb:160
func TestPreviewRequestTermination(t *testing.T) {
	f := newFixture(t, previewDay)
	s := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2024, time.March, 1)))
	at := previewDay.Add(36 * time.Hour)
	v, err := f.previewRequest(invoicing.PreviewRequest{Subscriptions: subRefs(s), TerminatedAt: &at})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got := reasons(v); len(got) != 1 || got[0] != invoice.InvoicingReasonSubscriptionTerminating {
		t.Errorf("reasons = %v, want = [subscription_terminating]", got)
	}
	if got := v.Subscriptions[0].ExternalID; got != s.ExternalID {
		t.Errorf("external ID = %q, want = %q", got, s.ExternalID)
	}
	// The plan is paid in arrears: 1 to 31 March is billed in full.
	if got, want := v.Invoice.FeesAmount, int64(1000); got != want {
		t.Errorf("fees amount = %d, want = %d", got, want)
	}
	if got := f.reload(s); got.Status != subscription.StatusActive || got.TerminatedAt != nil {
		t.Errorf("stored subscription = %s %v, want = active", got.Status, got.TerminatedAt)
	}
	f.checkNothingStored(t, 1)
}

// source: spec/services/invoices/preview/subscriptions_service_spec.rb:178
func TestPreviewRequestEnding(t *testing.T) {
	tests := []struct {
		name   string
		ending *time.Time
		want   invoice.InvoicingReason
	}{
		// source: spec/services/invoices/preview/subscriptions_service_spec.rb:196
		{"in the current period", new(billingtest.Date(2024, time.April, 1).Add(12 * time.Hour)), invoice.InvoicingReasonSubscriptionTerminating},
		// source: spec/services/invoices/preview/subscriptions_service_spec.rb:210
		{"in the future", new(billingtest.Date(2024, time.May, 15)), invoice.InvoicingReasonSubscriptionPeriodic},
		// source: spec/services/invoices/preview/subscriptions_service_spec.rb:226
		{"never", nil, invoice.InvoicingReasonSubscriptionPeriodic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, previewDay)
			s := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2024, time.March, 1)),
				func(s *subscription.Subscription) { s.EndingAt = tt.ending })
			v, err := f.previewRequest(invoicing.PreviewRequest{Subscriptions: subRefs(s)})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if got := reasons(v); len(got) != 1 || got[0] != tt.want {
				t.Errorf("reasons = %v, want = [%s]", got, tt.want)
			}
		})
	}
}

// source: spec/services/invoices/preview/subscriptions_service_spec.rb:261
// source: spec/services/invoices/preview/find_subscriptions_service_spec.rb:60
func TestPreviewRequestPendingSubscription(t *testing.T) {
	f := newFixture(t, previewDay, func(p *catalog.Plan) { p.PayInAdvance = true })
	start := billingtest.Date(2024, time.April, 10)
	s := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(start))
	if s.Status != subscription.StatusPending {
		t.Fatalf("status = %s, want = pending", s.Status)
	}
	v, err := f.previewRequest(invoicing.PreviewRequest{Subscriptions: subRefs(s)})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got := reasons(v); len(got) != 1 || got[0] != invoice.InvoicingReasonSubscriptionStarting {
		t.Errorf("reasons = %v, want = [subscription_starting]", got)
	}
	if got := v.Subscriptions[0].ExternalID; got != s.ExternalID {
		t.Errorf("external ID = %q, want = %q", got, s.ExternalID)
	}
	// Paid in advance from 10 April: 21 of 30 days.
	if got, want := v.Invoice.FeesAmount, int64(700); got != want {
		t.Errorf("fees amount = %d, want = %d", got, want)
	}
	if got := f.reload(s); got.Status != subscription.StatusPending {
		t.Errorf("stored status = %s, want = pending", got.Status)
	}
	f.checkNothingStored(t, 1)
}

// source: spec/services/invoices/preview/subscription_plan_change_service_spec.rb:56
func TestPreviewRequestPlanChange(t *testing.T) {
	tests := []struct {
		name         string
		amount       int64
		payInAdvance bool
		want         []invoice.InvoicingReason
		terminatedAt time.Time
		startedAt    time.Time
	}{
		// source: spec/services/invoices/preview/subscription_plan_change_service_spec.rb:68
		{"upgrade in advance", 1000, true, []invoice.InvoicingReason{invoice.InvoicingReasonSubscriptionTerminating, invoice.InvoicingReasonSubscriptionStarting}, previewDay, previewDay},
		// A downgrade ends at the end of the first day of the next period,
		// when the new subscription has started.
		// source: spec/services/invoices/preview/subscription_plan_change_service_spec.rb:95
		{"downgrade in advance", 999, true, []invoice.InvoicingReason{invoice.InvoicingReasonSubscriptionTerminating, invoice.InvoicingReasonSubscriptionStarting}, billingtest.Date(2024, time.April, 2).Add(-time.Microsecond), billingtest.Date(2024, time.April, 1)},
		// source: spec/services/invoices/preview/subscription_plan_change_service_spec.rb:127
		{"upgrade in arrears", 2000, false, []invoice.InvoicingReason{invoice.InvoicingReasonSubscriptionTerminating}, previewDay, time.Time{}},
		// source: spec/services/invoices/preview/subscription_plan_change_service_spec.rb:150
		{"downgrade in arrears", 500, false, []invoice.InvoicingReason{invoice.InvoicingReasonSubscriptionTerminating}, billingtest.Date(2024, time.April, 2).Add(-time.Microsecond), time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, previewDay)
			s := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2024, time.March, 1)))
			target := f.Plan(period.Monthly, tt.amount, func(p *catalog.Plan) { p.PayInAdvance = tt.payInAdvance })
			v, err := f.previewRequest(invoicing.PreviewRequest{Subscriptions: subRefs(s), TargetPlan: refs.ByCode(target.Code)})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if got := reasons(v); !slices.Equal(got, tt.want) {
				t.Errorf("reasons = %v, want = %v", got, tt.want)
			}
			if got := v.Invoice.CreatedAt; got.IsZero() {
				t.Errorf("created at is zero")
			}
			if got := v.Subscriptions[0].Subscription.Period.To; got.After(tt.terminatedAt) {
				t.Errorf("period end = %v, want <= %v", got, tt.terminatedAt)
			}
			if !tt.startedAt.IsZero() {
				if got := v.Subscriptions[1].Subscription.Period.From; !got.Equal(tt.startedAt) {
					t.Errorf("new subscription period start = %v, want = %v", got, tt.startedAt)
				}
			}
			if got := f.reload(s); got.Status != subscription.StatusActive {
				t.Errorf("stored status = %s, want = active", got.Status)
			}
			f.checkNothingStored(t, 1)
		})
	}
}

// source: spec/services/invoices/preview/credits_service_spec.rb:32
func TestPreviewRequestTerminationCredit(t *testing.T) {
	feb1 := billingtest.Date(2025, time.February, 1)
	tests := []struct {
		name string
		req  func(f *fixture, s *subscription.Subscription) invoicing.PreviewRequest
		want int64
	}{
		// source: spec/services/invoices/preview/credits_service_spec.rb:65
		{"termination", func(f *fixture, s *subscription.Subscription) invoicing.PreviewRequest {
			at := billingtest.Date(2025, time.February, 15)
			return invoicing.PreviewRequest{Subscriptions: subRefs(s), TerminatedAt: &at}
		}, 4643},
		// source: spec/services/invoices/preview/credits_service_spec.rb:92
		{"upgrade", func(f *fixture, s *subscription.Subscription) invoicing.PreviewRequest {
			f.Clock.Set(billingtest.Date(2025, time.February, 15))
			up := f.Plan(period.Monthly, 20_000, func(p *catalog.Plan) { p.PayInAdvance = true })
			return invoicing.PreviewRequest{Subscriptions: subRefs(s), TargetPlan: refs.ByCode(up.Code)}
		}, 5000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, feb1, func(p *catalog.Plan) { p.Amount, p.PayInAdvance = 10_000, true })
			m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
			f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"60"}`)
			s := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(feb1))
			f.bill(feb1, invoice.InvoicingReasonSubscriptionStarting, s)
			f.Event(s, "api_calls", feb1.Add(72*time.Hour))
			f.Clock.Set(billingtest.Date(2025, time.February, 10))
			v, err := f.previewRequest(tt.req(f, s))
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			var credited int64
			for _, c := range v.Credits {
				if !c.Credit.CreditNoteID.IsZero() {
					credited += c.Credit.Amount
				}
			}
			if credited != tt.want {
				t.Errorf("credit note credits = %d, want = %d", credited, tt.want)
			}
			if got, want := v.Invoice.CreditNotesAmount, tt.want; got != want {
				t.Errorf("credit notes amount = %d, want = %d", got, want)
			}
			inv := v.Invoice
			if got, want := inv.TotalAmount, inv.SubTotalIncludingTaxesAmount-inv.CreditNotesAmount-inv.PrepaidCreditAmount; got != want {
				t.Errorf("total = %d, want = %d", got, want)
			}
			if got := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (int, error) {
				ns, err := r.CreditNotes.ListAvailable(ctx, f.OrgID(), f.cust.ID, f.plan.Currency)
				return len(ns), err
			}); got != 0 {
				t.Errorf("stored credit notes = %d, want = 0", got)
			}
		})
	}
}

// source: spec/services/invoices/preview_context_service_spec.rb:270
func TestPreviewRequestCoupons(t *testing.T) {
	tests := []struct {
		name    string
		applied bool
		want    int64
	}{
		// source: spec/services/invoices/preview_context_service_spec.rb:314
		{"customer without coupons", false, 50},
		// source: spec/services/invoices/preview_context_service_spec.rb:278
		{"customer with coupons", true, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, previewDay, func(p *catalog.Plan) { p.Amount = 3100 })
			given := f.FixedAmountCoupon(50)
			if tt.applied {
				f.ApplyCoupon(f.FixedAmountCoupon(20), f.cust)
			}
			v, err := f.previewRequest(invoicing.PreviewRequest{
				Subscription: &invoicing.PreviewSubscription{Plan: refs.ByID(f.plan.ID)},
				Coupons:      []refs.Ref{refs.ByCode(given.Code), refs.ByCode("unknown")},
			})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if got := v.Invoice.CouponsAmount; got != tt.want {
				t.Errorf("coupons amount = %d, want = %d", got, tt.want)
			}
			if len(v.Credits) != 1 || v.Credits[0].Label == "" {
				t.Fatalf("credits = %+v, want = one labeled coupon credit", v.Credits)
			}
			if !tt.applied {
				if got, want := v.Credits[0].Label, given.Code; got != want {
					t.Errorf("label = %q, want = %q", got, want)
				}
				f.checkNothingStored(t, 0)
			}
		})
	}
}
