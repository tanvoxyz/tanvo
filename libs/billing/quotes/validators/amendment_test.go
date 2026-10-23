package validators

import (
	"strconv"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// amendFix is the world of the subscription_amendment specs: an active
// subscription to a 100.00 plan amended to a 200.00 plan, in EUR.
type amendFix struct {
	h           *billingtest.Harness
	q           *quote.Quote
	v           *quote.Version
	plan        *catalog.Plan
	planItem    obj
	planPayload obj
	items       obj
}

// target is the state of the amended subscription.
type target int

const (
	targetActive target = iota
	targetTerminated
	targetPending
	targetMissing
)

func newAmendFix(t *testing.T, tg target, planOpts ...func(*catalog.Plan)) *amendFix {
	h := billingtest.New(t, testNow)
	cust := h.Customer()
	f := &amendFix{h: h}
	targetPlan := h.Plan(period.Monthly, 10_000, eurPlan)
	f.plan = h.Plan(period.Monthly, 20_000, append([]func(*catalog.Plan){eurPlan}, planOpts...)...)
	f.q, f.v = newQuote(h, cust, quote.OrderTypeSubscriptionAmendment, eur)
	switch tg {
	case targetActive:
		f.q.SubscriptionID = h.Subscription(cust, targetPlan).ID
	case targetTerminated:
		s := h.Subscription(cust, targetPlan)
		h.UpdateSubscription(s, func(s *subscription.Subscription) {
			if err := s.Terminate(h.Now()); err != nil {
				t.Fatal(err)
			}
		})
		f.q.SubscriptionID = s.ID
	case targetPending:
		f.q.SubscriptionID = h.Subscription(cust, targetPlan, billingtest.SubscribedAt(h.Now().AddDate(0, 1, 0))).ID
	case targetMissing:
	}
	f.planPayload = obj{"code": f.plan.Code}
	f.planItem = obj{"id": f.plan.ID.String(), "localId": "3d08b2df-4e4c-4d58-b415-a525c1663735", "type": "plan", "payload": f.planPayload}
	f.items = obj{"plans": arr{f.planItem}}
	return f
}

type amendCase struct {
	line     int
	name     string
	scope    Scope
	target   target
	planOpts []func(*catalog.Plan)
	edit     func(f *amendFix)
	want     []string
}

func runAmendCases(t *testing.T, source string, cases []amendCase, run func(t *testing.T, f *amendFix, scope Scope) []string) {
	t.Helper()
	for _, c := range cases {
		t.Run(strconv.Itoa(c.line)+"/"+c.name, func(t *testing.T) {
			t.Logf("source: %s:%d", source, c.line)
			f := newAmendFix(t, c.target, c.planOpts...)
			if c.edit != nil {
				c.edit(f)
			}
			checkErrors(t, run(t, f, c.scope), c.want)
		})
	}
}

func TestSubscriptionAmendmentBusiness(t *testing.T) {
	const source = "spec/services/quote_versions/validators/subscription_amendment/business_validator_spec.rb"
	twoPlans := func(f *amendFix) { f.items["plans"] = arr{f.planItem, f.planItem} }
	cases := []amendCase{
		{line: 36, name: "valid", scope: ScopeApprove},
		{line: 44, name: "second plan", scope: ScopeApprove, edit: twoPlans, want: []string{"billing_items.plans: single_plan_expected"}},
		{line: 54, name: "second plan at update", scope: ScopeUpdate, edit: twoPlans, want: []string{"billing_items.plans: single_plan_expected"}},
		{line: 64, name: "ending date", scope: ScopeApprove, edit: func(f *amendFix) { f.planPayload["endDate"] = iso(testNow.AddDate(1, 0, 0)) }},
		{
			line: 72, name: "ending date in the past", scope: ScopeApprove,
			edit: func(f *amendFix) { f.planPayload["endDate"] = iso(testNow.AddDate(0, 0, -1)) },
			want: []string{"billing_items.plans.0.payload.endDate: invalid_date"},
		},
		{line: 83, name: "start date not a date", scope: ScopeApprove, edit: func(f *amendFix) { f.planPayload["startDate"] = "whenever" }},
		{
			line: 93, name: "ending date before start date", scope: ScopeApprove,
			edit: func(f *amendFix) {
				f.planPayload["startDate"], f.planPayload["endDate"] = iso(testNow.AddDate(2, 0, 0)), iso(testNow.AddDate(1, 0, 0))
			},
		},
		{line: 101, name: "terminated target", scope: ScopeApprove, target: targetTerminated, want: []string{"subscription_id: subscription_not_active"}},
		{line: 110, name: "pending target", scope: ScopeApprove, target: targetPending, want: []string{"subscription_id: subscription_not_active"}},
		{
			line: 124, name: "billing entity", scope: ScopeApprove,
			edit: func(f *amendFix) { f.v.BillingEntityID = billingEntity(f.h, f.h.OrgID()).ID },
			want: []string{"billing_entity_id: not_supported_for_order_type"},
		},
		{line: 136, name: "no target subscription", scope: ScopeApprove, target: targetMissing, want: []string{"subscription_id: value_is_mandatory"}},
		{line: 147, name: "lower amount", scope: ScopeApprove, planOpts: []func(*catalog.Plan){func(p *catalog.Plan) { p.Amount = 5_000 }}},
		{line: 155, name: "override below the target plan", scope: ScopeApprove, edit: func(f *amendFix) { f.planItem["overrides"] = obj{"amountCents": 5_000} }},
		{
			line: 163, name: "unknown plan", scope: ScopeApprove,
			edit: func(f *amendFix) { f.planItem["id"] = unknownID },
			want: []string{"billing_items.plans.0.id: plan_not_found"},
		},
		{
			line: 172, name: "plan currency does not match", scope: ScopeApprove,
			planOpts: []func(*catalog.Plan){func(p *catalog.Plan) { p.Currency = billingtest.USD }},
			want:     []string{"billing_items.plans.0.id: currencies_does_not_match"},
		},
	}
	runAmendCases(t, source, cases, func(t *testing.T, f *amendFix, scope Scope) []string {
		return runBusiness(t, f.h, f.q, f.v, scope, f.items)
	})
}

func TestSubscriptionAmendmentValidator(t *testing.T) {
	const source = "spec/services/quote_versions/validators/subscription_amendment_validator_spec.rb"
	cases := []amendCase{
		{line: 40, name: "complete valid version", scope: ScopeApprove},
		{
			line: 50, name: "structural errors first", scope: ScopeApprove, target: targetTerminated,
			edit: func(f *amendFix) { f.planItem["overrides"] = obj{"taxCodes": arr{"vat_20"}} },
			want: []string{"billing_items.plans.0.overrides.taxCodes: unsupported_key"},
		},
		{line: 60, name: "inactive target", scope: ScopeApprove, target: targetTerminated, want: []string{"subscription_id: subscription_not_active"}},
		{line: 70, name: "nil billing items at update", scope: ScopeUpdate, edit: func(f *amendFix) { f.items = nil }},
	}
	runAmendCases(t, source, cases, func(t *testing.T, f *amendFix, scope Scope) []string {
		return runValidate(t, f.h, f.q, f.v, scope, f.items)
	})
}

// TestValidatorFor checks that each order type gets its own validator: the
// one-off schema refuses plans, the subscription ones refuse add-ons, and
// only the amendment one limits the plans to one.
func TestValidatorFor(t *testing.T) {
	const source = "spec/services/quote_versions/validators_spec.rb"
	cases := []struct {
		line  int
		order quote.OrderType
		want  []string
	}{
		{line: 16, order: quote.OrderTypeOneOff, want: []string{"billing_items.plans: unsupported_key"}},
		{line: 24, order: quote.OrderTypeSubscriptionCreation, want: []string{"billing_items.addOns: unsupported_key"}},
		{line: 32, order: quote.OrderTypeSubscriptionAmendment, want: []string{"billing_items.addOns: unsupported_key"}},
	}
	for _, c := range cases {
		t.Run(string(c.order), func(t *testing.T) {
			t.Logf("source: %s:%d", source, c.line)
			h := billingtest.New(t, testNow)
			q, v := newQuote(h, h.Customer(), c.order, eur)
			items := obj{"addOns": arr{}, "plans": arr{}}
			if c.order == quote.OrderTypeOneOff {
				delete(items, "addOns")
			} else {
				delete(items, "plans")
			}
			checkErrors(t, runValidate(t, h, q, v, ScopeUpdate, items), c.want)
		})
	}
	t.Run("amendment single plan", func(t *testing.T) {
		t.Logf("source: %s:32", source)
		f := newAmendFix(t, targetActive)
		f.items["plans"] = arr{f.planItem, f.planItem}
		checkErrors(t, runValidate(t, f.h, f.q, f.v, ScopeUpdate, f.items), []string{"billing_items.plans: single_plan_expected"})
	})
	t.Run("no validator", func(t *testing.T) {
		h := billingtest.New(t, testNow)
		q, v := newQuote(h, h.Customer(), quote.OrderType("bogus"), eur)
		checkErrors(t, runValidate(t, h, q, v, ScopeApprove, obj{"anything": 1}), nil)
	})
	t.Run("bad arguments", func(t *testing.T) {
		h := billingtest.New(t, testNow)
		q, v := newQuote(h, h.Customer(), quote.OrderTypeOneOff, eur)
		if err := Validate(h.Ctx, port.Repos{}, nil, v, ScopeUpdate, h.Now()); err == nil {
			t.Error("nil quote: got = nil, want = an error")
		}
		if err := Validate(h.Ctx, port.Repos{}, q, v, Scope(0), h.Now()); err == nil {
			t.Error("zero scope: got = nil, want = an error")
		}
	})
}
