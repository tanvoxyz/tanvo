package x402pay_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// The route check runs first in a gate check: a stranger's check on a
// funded connection answers the route's verdict.
//
// source: spec/services/x402/gate_checks/route_check_service_spec.rb
func TestGateCheckRoute(t *testing.T) {
	charge := func(g *gateEnv, opts ...func(*catalog.Charge)) *catalog.Charge {
		return g.h.Charge(g.plan, g.metric, catalog.ChargeModelStandard, `{"amount":"1"}`, opts...)
	}
	passes := func(t *testing.T, g *gateEnv) {
		t.Helper()
		if _, err := g.check(); err != nil {
			t.Errorf("GateCheck: got = %v, want = success", err)
		}
	}
	tests := []struct {
		name, source string
		setup        func(*gateEnv)
		want         string
	}{
		{"with a usage-only USD plan whose route charges target the wallet", "17-21", func(*gateEnv) {}, ""},
		{"with an override of the plan carrying a base fee in EUR", "23-29", func(g *gateEnv) {
			g.h.Plan(period.Monthly, 500, func(p *catalog.Plan) {
				p.ParentID, p.Code, p.Currency = g.plan.ID, g.plan.Code, money.MustCurrency("EUR")
			})
		}, ""},
		{"with an unknown plan code", "31-37", func(g *gateEnv) { g.in.PlanCode = "unknown" }, "plan_not_found"},
		{"with a metric the plan has no charge for", "39-45", func(g *gateEnv) {
			g.in.BillableMetricCode = g.h.Metric("other", catalog.AggregationTypeCount, "").Code
		}, "charge_not_found"},
		{"with a discarded route charge", "47-53", func(g *gateEnv) {
			g.h.Do(func(ctx context.Context, r port.Repos) error {
				_, err := r.Charges.DiscardByPlan(ctx, g.h.OrgID(), g.plan.ID, g.h.Now())
				return err
			})
		}, "charge_not_found"},
		{"with a EUR plan", "55-61", func(g *gateEnv) {
			g.plan = g.h.Plan(period.Monthly, 0, func(p *catalog.Plan) { p.Currency = money.MustCurrency("EUR") })
			charge(g, targeted)
			g.in.PlanCode = g.plan.Code
		}, "base: plan_currency_not_supported"},
		{"with a base fee", "63-69", func(g *gateEnv) {
			g.plan = g.h.Plan(period.Monthly, 1_000)
			charge(g, targeted)
			g.in.PlanCode = g.plan.Code
		}, "base: plan_not_usage_only"},
		{"with a fixed charge", "71-77", func(g *gateEnv) {
			g.h.FixedCharge(g.plan, g.h.AddOn(100), catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1")
		}, "base: plan_not_usage_only"},
		{"with a discarded fixed charge", "79-85", func(g *gateEnv) {
			f := g.h.FixedCharge(g.plan, g.h.AddOn(100), catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1")
			g.h.Do(func(ctx context.Context, r port.Repos) error {
				return r.FixedCharges.Discard(ctx, g.h.OrgID(), f.ID, g.h.Now())
			})
		}, ""},
		{"with a minimum commitment", "87-93", func(g *gateEnv) { g.h.Commitment(g.plan, 1_000) }, "base: plan_not_usage_only"},
		{"with a charge minimum on another metric of the plan", "95-101", func(g *gateEnv) {
			g.h.Charge(g.plan, g.h.Metric("other", catalog.AggregationTypeCount, ""), catalog.ChargeModelStandard, `{"amount":"1"}`,
				func(c *catalog.Charge) { c.MinAmount = 100 })
		}, "base: plan_not_usage_only"},
		{"with a second route charge that does not accept a target wallet", "103-109", func(g *gateEnv) { charge(g) }, "base: charge_not_targeted"},
		{"with a pay-in-advance route charge that is not invoiceable", "119-125", func(g *gateEnv) {
			g.h.Do(func(ctx context.Context, r port.Repos) error {
				_, err := r.Charges.DiscardByPlan(ctx, g.h.OrgID(), g.plan.ID, g.h.Now())
				return err
			})
			charge(g, targeted, regroupPaidFees)
		}, "base: charge_not_invoiceable"},
		{"with a second route charge that is pay-in-advance and not invoiceable", "127-133", func(g *gateEnv) {
			charge(g, targeted, regroupPaidFees)
		}, "base: charge_not_invoiceable"},
		{"with an invoiceable pay-in-advance route charge", "135-141", func(g *gateEnv) {
			charge(g, targeted, func(c *catalog.Charge) { c.PayInAdvance = true })
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/gate_checks/route_check_service_spec.rb:{tt.source}
			g := newGate(t)
			tt.setup(g)
			if tt.want == "" {
				passes(t, g)
				return
			}
			g.refused(t, tt.want)
		})
	}
	// The refusal without the events targeting integration
	// (route_check_service_spec.rb:111-117) is not ported: Tanvo has no
	// premium integrations.
}

// regroupPaidFees is a pay-in-advance charge whose paid fees are not
// invoiced.
func regroupPaidFees(c *catalog.Charge) {
	c.PayInAdvance, c.Invoiceable, c.RegroupPaidFees = true, false, catalog.RegroupPaidFeesInvoice
}
