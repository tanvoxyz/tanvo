package validators

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

const (
	graduatedCharge = `{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"0","flat_amount":"200"},` +
		`{"from_value":11,"to_value":null,"per_unit_amount":"0","flat_amount":"300"}]}`
	graduatedFixedCharge = `{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"5","flat_amount":"200"},` +
		`{"from_value":11,"to_value":null,"per_unit_amount":"1","flat_amount":"300"}]}`
)

// subFix is the world of the subscription_creation specs: a EUR plan with
// a standard charge on api_calls and a standard fixed charge, a EUR fixed
// amount coupon, and a EUR quote quoting them with a wallet credit.
type subFix struct {
	h      *billingtest.Harness
	cust   *customer.Customer
	q      *quote.Quote
	v      *quote.Version
	plan   *catalog.Plan
	metric *catalog.BillableMetric
	charge *catalog.Charge
	fixed  *catalog.FixedCharge
	addOn  *catalog.AddOn
	coupon *coupon.Coupon

	planItem, planPayload, chargeSnapshot, fixedSnapshot obj
	planOverrides, chargeOverride, fixedOverride         obj
	couponItem, couponPayload                            obj
	walletItem, walletPayload, rule                      obj
	items                                                obj
}

func eurPlan(p *catalog.Plan) { p.Currency = eur }

func eurCoupon(c *coupon.Coupon) { c.Currency = eur }

func newSubFix(t *testing.T) *subFix {
	h := billingtest.New(t, testNow)
	f := &subFix{h: h, cust: h.Customer()}
	f.q, f.v = newQuote(h, f.cust, quote.OrderTypeSubscriptionCreation, eur)
	f.plan = h.Plan(period.Monthly, 100, eurPlan)
	f.metric = h.Metric("api_calls", catalog.AggregationTypeCount, "")
	f.charge = h.Charge(f.plan, f.metric, catalog.ChargeModelStandard, `{"amount":"250"}`)
	f.addOn = h.AddOn(100, func(a *catalog.AddOn) { a.Currency = eur })
	f.fixed = h.FixedCharge(f.plan, f.addOn, catalog.FixedChargeModelStandard, `{"amount":"300"}`, "1")
	f.coupon = h.FixedAmountCoupon(200, eurCoupon)
	return f
}

// build assembles the billing items from the entities as they now are.
func (f *subFix) build() {
	f.chargeSnapshot = obj{"id": f.charge.ID.String(), "billableMetric": obj{"code": f.metric.Code}, "chargeModel": string(f.charge.ChargeModel)}
	f.fixedSnapshot = obj{"id": f.fixed.ID.String(), "addOn": obj{"code": f.addOn.Code}, "chargeModel": string(f.fixed.ChargeModel)}
	f.planPayload = obj{
		"code": f.plan.Code, "startDate": isoDay(f.h.Now()),
		"charges": arr{f.chargeSnapshot}, "fixedCharges": arr{f.fixedSnapshot},
	}
	f.chargeOverride = obj{"billableMetricCode": f.metric.Code, "chargeModel": string(f.charge.ChargeModel), "properties": obj{"amount": "100"}}
	f.fixedOverride = obj{"addOnCode": f.addOn.Code, "units": "3"}
	f.planOverrides = obj{"amountCents": 500_000, "charges": arr{f.chargeOverride}, "fixedCharges": arr{f.fixedOverride}}
	f.planItem = obj{"id": f.plan.ID.String(), "type": "plan", "payload": f.planPayload, "overrides": f.planOverrides}
	f.couponPayload = obj{"code": f.coupon.Code, "type": "fixed_amount", "amountCents": 20_000}
	f.couponItem = obj{"id": f.coupon.ID.String(), "localId": "ba54903c-7bd5-4d40-8d51-45a5157005ff", "payload": f.couponPayload}
	f.rule = obj{"trigger": "interval", "interval": "monthly", "method": "fixed"}
	f.walletPayload = obj{
		"paidCredits": "100.0", "grantedCredits": "10.0", "rateAmount": "1.0", "currency": "EUR",
		"recurringTransactionRules": arr{f.rule},
	}
	f.walletItem = obj{"localId": "d9169d94-b322-4d70-a2b1-9e6a58e3f74a", "payload": f.walletPayload}
	f.items = obj{"plans": arr{f.planItem}, "coupons": arr{f.couponItem}, "walletCredits": arr{f.walletItem}}
}

// replace sets the members of m to those of with, keeping the identity of
// m (Ruby let redefinition).
func replace(m, with obj) {
	for k := range m {
		delete(m, k)
	}
	for k, v := range with {
		m[k] = v
	}
}

func (f *subFix) updatePlan(fn func(*catalog.Plan)) {
	fn(f.plan)
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Plans.Update(ctx, f.plan) })
}

func (f *subFix) updateCharge(fn func(*catalog.Charge)) {
	fn(f.charge)
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Charges.Update(ctx, f.charge) })
}

func (f *subFix) updateFixed(fn func(*catalog.FixedCharge)) {
	fn(f.fixed)
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.FixedCharges.Update(ctx, f.fixed) })
}

func (f *subFix) updateCoupon(fn func(*coupon.Coupon)) {
	fn(f.coupon)
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Coupons.Update(ctx, f.coupon) })
}

func (f *subFix) paymentMethod(c *customer.Customer) *payment.Method {
	m := &payment.Method{
		ID: core.NewID[core.PaymentMethodID](f.h.IDs), OrganizationID: f.h.OrgID(), CustomerID: c.ID,
		ProviderMethodID: "pm_" + c.ID.String()[30:], CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
	}
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Create(ctx, m) })
	return m
}

func percentageCoupon(c *coupon.Coupon) {
	rate := money.MustParse("10")
	c.Type, c.PercentageRate, c.Amount = coupon.TypePercentage, &rate, nil
}

func limitedToPlan(p *catalog.Plan) func(*coupon.Coupon) {
	return func(c *coupon.Coupon) { c.LimitedPlanIDs = []core.PlanID{p.ID} }
}

type subCase struct {
	line  int
	name  string
	scope Scope
	// setup changes the entities before the billing items are built.
	setup func(f *subFix)
	// edit changes the billing items.
	edit func(f *subFix)
	want []string
}

func runSubCases(t *testing.T, source string, cases []subCase, run func(t *testing.T, f *subFix, scope Scope) []string) {
	t.Helper()
	for _, c := range cases {
		t.Run(strconv.Itoa(c.line)+"/"+c.name, func(t *testing.T) {
			t.Logf("source: %s:%d", source, c.line)
			f := newSubFix(t)
			if c.setup != nil {
				c.setup(f)
			}
			f.build()
			if c.edit != nil {
				c.edit(f)
			}
			checkErrors(t, run(t, f, c.scope), c.want)
		})
	}
}

func TestSubscriptionCreationBusiness(t *testing.T) {
	const source = "spec/services/quote_versions/validators/subscription_creation/business_validator_spec.rb"
	const (
		chargeCode  = "billing_items.plans.0.overrides.charges.0.billableMetricCode"
		fixedCode   = "billing_items.plans.0.overrides.fixedCharges.0.addOnCode"
		fixedProps  = "billing_items.plans.0.overrides.fixedCharges.0.properties"
		ruleField0  = "billing_items.walletCredits.0.payload.recurringTransactionRules.0."
		walletField = "billing_items.walletCredits.0.payload."
	)
	noCurrency := func(f *subFix) { f.v.Currency = money.Currency{} }
	usdPlan := func(f *subFix) { f.updatePlan(func(p *catalog.Plan) { p.Currency = billingtest.USD }) }
	usdCoupon := func(f *subFix) { f.updateCoupon(func(c *coupon.Coupon) { c.Currency = billingtest.USD }) }
	graduated := func(f *subFix) {
		f.updateCharge(func(c *catalog.Charge) {
			c.ChargeModel, c.Properties = catalog.ChargeModelGraduated, catalog.MustProperties(graduatedCharge)
		})
	}
	inAdvance := func(f *subFix) { f.updateCharge(func(c *catalog.Charge) { c.PayInAdvance = true }) }
	commitment := func(f *subFix) { f.h.Commitment(f.plan, 1_000) }
	noAmountCommitment := func(f *subFix) { f.planOverrides["minimumCommitment"] = obj{"invoiceDisplayName": "Min commitment"} }
	secondCoupon := func(f *subFix, opts ...func(*coupon.Coupon)) {
		other := f.h.FixedAmountCoupon(200, append([]func(*coupon.Coupon){eurCoupon}, opts...)...)
		f.items["coupons"] = arr{f.couponItem, obj{
			"id": other.ID.String(), "localId": "4c0e5e21-9f38-4a2b-8c7d-6e5f4a3b2c1d",
			"payload": obj{"code": other.Code, "type": "fixed_amount", "amountCents": 10_000},
		}}
	}
	recurring := func(f *subFix) { f.updateCoupon(func(c *coupon.Coupon) { c.Frequency = coupon.FrequencyRecurring }) }
	months := func(n int) string { return iso(testNow.AddDate(0, n, 0)) }
	sixMonths := testNow.AddDate(0, 6, 0)
	dayStart := billingtest.Date(sixMonths.Year(), sixMonths.Month(), sixMonths.Day())

	cases := []subCase{
		{line: 110, name: "valid", scope: ScopeUpdate},
		{line: 117, name: "valid at approve", scope: ScopeApprove},
		{line: 128, name: "missing currency at update", scope: ScopeUpdate, setup: noCurrency},
		{line: 135, name: "missing currency at approve", scope: ScopeApprove, setup: noCurrency, want: []string{"currency: value_is_mandatory"}},
		{
			line: 147, name: "currency not ISO 4217", scope: ScopeUpdate,
			setup: func(f *subFix) { f.v.Currency = doubloon() },
			want:  []string{"currency: invalid_currency"},
		},
		{line: 158, name: "no start date at update", scope: ScopeUpdate, edit: func(f *subFix) { delete(f.planPayload, "startDate") }},
		{line: 165, name: "no start date at approve", scope: ScopeApprove, edit: func(f *subFix) { delete(f.planPayload, "startDate") }},
		{
			line: 174, name: "unknown plan", scope: ScopeUpdate,
			edit: func(f *subFix) { f.planItem["id"] = unknownID },
			want: []string{"billing_items.plans.0.id: plan_not_found"},
		},
		{
			line: 185, name: "plan of another organization", scope: ScopeUpdate,
			setup: func(f *subFix) {
				org := otherOrg(f.h)
				f.plan = f.h.Plan(period.Monthly, 100, eurPlan, func(p *catalog.Plan) { p.OrganizationID = org })
			},
			want: []string{"billing_items.plans.0.id: plan_not_found"},
		},
		{
			line: 194, name: "discarded plan", scope: ScopeUpdate,
			setup: func(f *subFix) {
				f.updatePlan(func(p *catalog.Plan) {
					if err := p.MarkDeleted(f.h.Now()); err != nil {
						t.Fatal(err)
					}
				})
			},
		},
		{line: 202, name: "plan currency differs", scope: ScopeUpdate, setup: usdPlan, want: []string{"billing_items.plans.0.id: currencies_does_not_match"}},
		{
			line: 212, name: "override reprices the plan in the version currency", scope: ScopeUpdate, setup: usdPlan,
			edit: func(f *subFix) { f.planOverrides["amountCurrency"] = "EUR" },
		},
		{
			line: 220, name: "override reprices the plan in a third currency", scope: ScopeUpdate, setup: usdPlan,
			edit: func(f *subFix) { f.planOverrides["amountCurrency"] = "GBP" },
			want: []string{"billing_items.plans.0.id: currencies_does_not_match"},
		},
		{
			line: 230, name: "override metric with no charge", scope: ScopeUpdate,
			edit: func(f *subFix) { f.chargeOverride["billableMetricCode"] = "unknown_metric" },
			want: []string{chargeCode + ": charge_not_found"},
		},
		{
			line: 244, name: "override of another plan's charge", scope: ScopeUpdate,
			setup: func(f *subFix) {
				other := f.h.Metric("other_metric", catalog.AggregationTypeCount, "")
				f.h.Charge(f.h.Plan(period.Monthly, 100, eurPlan), other, catalog.ChargeModelStandard, `{"amount":"1"}`)
			},
			edit: func(f *subFix) { f.chargeOverride["billableMetricCode"] = "other_metric" },
			want: []string{chargeCode + ": charge_not_found"},
		},
		{
			line: 255, name: "discarded charge", scope: ScopeUpdate,
			setup: func(f *subFix) {
				f.h.Do(func(ctx context.Context, r port.Repos) error {
					return r.Charges.Discard(ctx, f.h.OrgID(), f.charge.ID, f.h.Now())
				})
			},
			want: []string{chargeCode + ": charge_not_found"},
		},
		{
			line: 268, name: "two charges on the overridden metric", scope: ScopeUpdate,
			edit: func(f *subFix) {
				second := f.h.Charge(f.plan, f.metric, catalog.ChargeModelStandard, `{"amount":"1"}`)
				dup := obj{"id": second.ID.String(), "billableMetric": f.chargeSnapshot["billableMetric"], "chargeModel": f.chargeSnapshot["chargeModel"]}
				f.planPayload["charges"] = arr{f.chargeSnapshot, dup}
			},
			want: []string{chargeCode + ": ambiguous_charge_override"},
		},
		{
			line: 279, name: "snapshot without charges", scope: ScopeUpdate,
			edit: func(f *subFix) { delete(f.planPayload, "charges") },
			want: []string{chargeCode + ": charge_not_found"},
		},
		{
			line: 291, name: "snapshot charge of another plan", scope: ScopeUpdate,
			edit: func(f *subFix) {
				other := f.h.Charge(f.h.Plan(period.Monthly, 100, eurPlan), f.metric, catalog.ChargeModelStandard, `{"amount":"1"}`)
				f.chargeSnapshot["id"] = other.ID.String()
			},
			want: []string{chargeCode + ": charge_not_found"},
		},
		{
			line: 302, name: "override charge model differs", scope: ScopeUpdate,
			edit: func(f *subFix) { f.chargeOverride["chargeModel"] = "graduated" },
			want: []string{"billing_items.plans.0.overrides.charges.0.chargeModel: cannot_override_charge_model"},
		},
		{
			line: 313, name: "snapshot charge model differs", scope: ScopeUpdate,
			edit: func(f *subFix) { f.chargeSnapshot["chargeModel"] = "graduated" },
			want: []string{"billing_items.plans.0.payload.charges.0.chargeModel: charge_model_changed"},
		},
		{line: 324, name: "snapshot charge without model", scope: ScopeUpdate, edit: func(f *subFix) { delete(f.chargeSnapshot, "chargeModel") }},
		{
			line: 332, name: "properties invalid for the model", scope: ScopeUpdate,
			edit: func(f *subFix) { f.chargeOverride["properties"] = obj{"amount": "not a number"} },
			want: []string{"billing_items.plans.0.overrides.charges.0.properties.amount: invalid_amount"},
		},
		{
			line: 343, name: "empty properties", scope: ScopeUpdate,
			edit: func(f *subFix) { f.chargeOverride["properties"] = obj{} },
			want: []string{"billing_items.plans.0.overrides.charges.0.properties.amount: invalid_amount"},
		},
		{
			line: 355, name: "properties drafted for another model", scope: ScopeUpdate, setup: graduated,
			edit: func(f *subFix) {
				f.chargeOverride["chargeModel"], f.chargeOverride["properties"] = nil, obj{"amount": "100"}
			},
			want: []string{"billing_items.plans.0.overrides.charges.0.properties.graduated_ranges: missing_graduated_ranges"},
		},
		{
			line: 369, name: "properties shaped for no model", scope: ScopeUpdate, setup: graduated,
			edit: func(f *subFix) {
				f.chargeOverride["chargeModel"], f.chargeOverride["properties"] = nil, obj{"graduated_ranges": "one to ten"}
			},
			want: []string{"billing_items.plans.0.overrides.charges.0.properties: invalid_value"},
		},
		{
			line: 391, name: "camelCase tiers", scope: ScopeUpdate, setup: graduated,
			edit: func(f *subFix) {
				f.chargeOverride["chargeModel"] = nil
				f.chargeOverride["properties"] = obj{"graduatedRanges": arr{
					obj{"fromValue": 0, "toValue": 1000, "perUnitAmount": "0.005", "flatAmount": "0"},
					obj{"fromValue": 1001, "toValue": nil, "perUnitAmount": "0.002", "flatAmount": "0"},
				}}
			},
		},
		{
			line: 409, name: "snake_case tiers", scope: ScopeUpdate, setup: graduated,
			edit: func(f *subFix) {
				f.chargeOverride["chargeModel"] = nil
				f.chargeOverride["properties"] = obj{"graduated_ranges": arr{
					obj{"from_value": 0, "to_value": nil, "per_unit_amount": "0.005", "flat_amount": "0"},
				}}
			},
		},
		{line: 417, name: "override without properties", scope: ScopeUpdate, edit: func(f *subFix) { delete(f.chargeOverride, "properties") }},
		{
			line: 426, name: "model moved, properties no longer fit", scope: ScopeUpdate,
			edit: func(f *subFix) {
				f.chargeSnapshot["chargeModel"] = "graduated"
				f.chargeOverride["chargeModel"], f.chargeOverride["properties"] = nil, obj{"amount": "100"}
			},
			want: []string{"billing_items.plans.0.payload.charges.0.chargeModel: charge_model_changed"},
		},
		{
			line: 438, name: "minimum on a charge billed in advance", scope: ScopeUpdate, setup: inAdvance,
			edit: func(f *subFix) { f.chargeOverride["minAmountCents"] = 1_000 },
			want: []string{"billing_items.plans.0.overrides.charges.0.minAmountCents: not_compatible_with_pay_in_advance"},
		},
		{
			line: 450, name: "zero minimum on a charge billed in advance", scope: ScopeUpdate, setup: inAdvance,
			edit: func(f *subFix) { f.chargeOverride["minAmountCents"] = 0 },
		},
		{line: 458, name: "minimum on a charge billed in arrears", scope: ScopeUpdate, edit: func(f *subFix) { f.chargeOverride["minAmountCents"] = 1_000 }},
		{
			line: 469, name: "discarded metric", scope: ScopeUpdate,
			setup: func(f *subFix) {
				f.h.Do(func(ctx context.Context, r port.Repos) error {
					return r.Metrics.Discard(ctx, f.h.OrgID(), f.metric.ID, f.h.Now())
				})
			},
		},
		{
			line: 477, name: "fixed override add-on with no fixed charge", scope: ScopeUpdate,
			edit: func(f *subFix) { f.fixedOverride["addOnCode"] = "unknown_add_on" },
			want: []string{fixedCode + ": fixed_charge_not_found"},
		},
		{
			line: 488, name: "discarded fixed charge", scope: ScopeUpdate,
			setup: func(f *subFix) {
				f.h.Do(func(ctx context.Context, r port.Repos) error {
					return r.FixedCharges.Discard(ctx, f.h.OrgID(), f.fixed.ID, f.h.Now())
				})
			},
			want: []string{fixedCode + ": fixed_charge_not_found"},
		},
		{
			line: 503, name: "two fixed charges on the overridden add-on", scope: ScopeUpdate,
			edit: func(f *subFix) {
				second := f.h.FixedCharge(f.plan, f.addOn, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1")
				dup := obj{"id": second.ID.String(), "addOn": f.fixedSnapshot["addOn"], "chargeModel": f.fixedSnapshot["chargeModel"]}
				f.planPayload["fixedCharges"] = arr{f.fixedSnapshot, dup}
			},
			want: []string{fixedCode + ": ambiguous_fixed_charge_override"},
		},
		{
			line: 514, name: "snapshot fixed charge model differs", scope: ScopeUpdate,
			edit: func(f *subFix) { f.fixedSnapshot["chargeModel"] = "graduated" },
			want: []string{"billing_items.plans.0.payload.fixedCharges.0.chargeModel: fixed_charge_model_changed"},
		},
		{line: 525, name: "snapshot fixed charge without model", scope: ScopeUpdate, edit: func(f *subFix) { delete(f.fixedSnapshot, "chargeModel") }},
		{
			line: 533, name: "fixed properties invalid for the model", scope: ScopeUpdate,
			edit: func(f *subFix) { f.fixedOverride["properties"] = obj{"amount": "not a number"} },
			want: []string{fixedProps + ".amount: invalid_amount"},
		},
		{
			line: 547, name: "fixed properties drafted for another model", scope: ScopeUpdate,
			edit: func(f *subFix) {
				f.fixedOverride["properties"] = obj{"graduated_ranges": arr{obj{"from_value": 0, "per_unit_amount": "1"}}}
			},
			want: []string{fixedProps + ": invalid_value"},
		},
		{
			line: 558, name: "empty fixed properties", scope: ScopeUpdate,
			edit: func(f *subFix) { f.fixedOverride["properties"] = obj{} },
			want: []string{fixedProps + ": invalid_value"},
		},
		{line: 569, name: "valid fixed properties", scope: ScopeUpdate, edit: func(f *subFix) { f.fixedOverride["properties"] = obj{"amount": "42"} }},
		{
			line: 587, name: "camelCase fixed tiers", scope: ScopeUpdate,
			setup: func(f *subFix) {
				f.updateFixed(func(fc *catalog.FixedCharge) {
					fc.ChargeModel, fc.Properties = catalog.FixedChargeModelGraduated, catalog.MustProperties(graduatedFixedCharge)
				})
			},
			edit: func(f *subFix) {
				f.fixedOverride["properties"] = obj{"graduatedRanges": arr{
					obj{"fromValue": 0, "toValue": 10, "perUnitAmount": "5", "flatAmount": "200"},
					obj{"fromValue": 11, "toValue": nil, "perUnitAmount": "1", "flatAmount": "300"},
				}}
			},
		},
		{
			line: 595, name: "negative units", scope: ScopeUpdate,
			edit: func(f *subFix) { f.fixedOverride["units"] = "-1" },
			want: []string{"billing_items.plans.0.overrides.fixedCharges.0.units: invalid_value"},
		},
		{
			line: 606, name: "units not a decimal", scope: ScopeUpdate,
			edit: func(f *subFix) { f.fixedOverride["units"] = "abc" },
			want: []string{"billing_items.plans.0.overrides.fixedCharges.0.units: invalid_value"},
		},
		{line: 617, name: "zero units", scope: ScopeUpdate, edit: func(f *subFix) { f.fixedOverride["units"] = "0" }},
		{line: 625, name: "no units", scope: ScopeUpdate, edit: func(f *subFix) { delete(f.fixedOverride, "units") }},
		{line: 633, name: "null plan overrides", scope: ScopeUpdate, edit: func(f *subFix) { f.planItem["overrides"] = nil }},
		{line: 643, name: "commitment without amount at update", scope: ScopeUpdate, edit: noAmountCommitment},
		{
			line: 650, name: "commitment without amount at approve", scope: ScopeApprove, edit: noAmountCommitment,
			want: []string{"billing_items.plans.0.overrides.minimumCommitment.amountCents: value_is_mandatory"},
		},
		{line: 660, name: "commitment without amount, plan commitment", scope: ScopeApprove, setup: commitment, edit: noAmountCommitment},
		{
			line: 675, name: "commitment with null amount, plan commitment", scope: ScopeApprove, setup: commitment,
			edit: func(f *subFix) {
				f.planOverrides["minimumCommitment"] = obj{"amountCents": nil, "invoiceDisplayName": "Min commitment"}
			},
		},
		{
			line: 683, name: "dates not ISO 8601", scope: ScopeUpdate,
			edit: func(f *subFix) { f.planPayload["startDate"] = "not-a-date" },
			want: []string{"billing_items.plans.0.payload.startDate: invalid_date"},
		},
		{
			line: 694, name: "endDate before startDate", scope: ScopeUpdate,
			edit: func(f *subFix) { f.planPayload["startDate"], f.planPayload["endDate"] = months(6), months(3) },
			want: []string{"billing_items.plans.0.payload.endDate: invalid_date_range"},
		},
		{
			line: 705, name: "date-only dates", scope: ScopeUpdate,
			edit: func(f *subFix) {
				f.planPayload["startDate"], f.planPayload["endDate"] = isoDay(testNow), isoDay(testNow.AddDate(1, 0, 0))
			},
		},
		{
			line: 718, name: "dates on the same day", scope: ScopeUpdate,
			edit: func(f *subFix) {
				f.planPayload["startDate"] = iso(dayStart)
				f.planPayload["endDate"] = iso(dayStart.AddDate(0, 0, 1).Add(-1e9))
			},
			want: []string{"billing_items.plans.0.payload.endDate: invalid_date_range"},
		},
		{
			line: 729, name: "endDate today at update", scope: ScopeUpdate,
			edit: func(f *subFix) {
				f.planPayload["startDate"], f.planPayload["endDate"] = isoDay(testNow.AddDate(0, -1, 0)), iso(testNow)
			},
		},
		{
			line: 736, name: "endDate today at approve", scope: ScopeApprove,
			edit: func(f *subFix) {
				f.planPayload["startDate"], f.planPayload["endDate"] = isoDay(testNow.AddDate(0, -1, 0)), iso(testNow)
			},
			want: []string{"billing_items.plans.0.payload.endDate: invalid_date"},
		},
		{
			line: 747, name: "blank start date", scope: ScopeApprove,
			edit: func(f *subFix) { f.planPayload["startDate"] = "" },
			want: []string{"billing_items.plans.0.payload.startDate: invalid_date"},
		},
		{
			line: 761, name: "only one plan carries a start date", scope: ScopeApprove,
			edit: func(f *subFix) {
				other := f.h.Plan(period.Monthly, 100, eurPlan)
				f.items = obj{"plans": arr{f.planItem, obj{"id": other.ID.String(), "type": "plan", "payload": obj{"code": other.Code}}}}
			},
		},
		{
			line: 770, name: "payment method of the customer", scope: ScopeUpdate,
			edit: func(f *subFix) { f.planPayload["paymentMethodId"] = f.paymentMethod(f.cust).ID.String() },
		},
		{
			line: 779, name: "payment method of another customer", scope: ScopeUpdate,
			edit: func(f *subFix) { f.planPayload["paymentMethodId"] = f.paymentMethod(f.h.Customer()).ID.String() },
			want: []string{"billing_items.plans.0.payload.paymentMethodId: payment_method_not_found"},
		},
		{
			line: 790, name: "unknown coupon", scope: ScopeUpdate,
			edit: func(f *subFix) { f.couponItem["id"] = unknownID },
			want: []string{"billing_items.coupons.0.id: coupon_not_found"},
		},
		{
			line: 799, name: "coupon of another organization", scope: ScopeUpdate,
			setup: func(f *subFix) {
				org := otherOrg(f.h)
				f.coupon = f.h.FixedAmountCoupon(200, eurCoupon, func(c *coupon.Coupon) { c.OrganizationID = org })
			},
			want: []string{"billing_items.coupons.0.id: coupon_not_found"},
		},
		{
			line: 808, name: "discarded coupon", scope: ScopeUpdate,
			setup: func(f *subFix) { f.updateCoupon(func(c *coupon.Coupon) { c.Delete(f.h.Now()) }) },
		},
		{
			line: 817, name: "non-reusable coupon applied twice", scope: ScopeUpdate,
			setup: func(f *subFix) { f.updateCoupon(func(c *coupon.Coupon) { c.Reusable = false }) },
			edit:  func(f *subFix) { f.items["coupons"] = arr{f.couponItem, f.couponItem} },
			want:  []string{"billing_items.coupons.1.id: coupon_is_not_reusable"},
		},
		{line: 826, name: "reusable coupon applied twice", scope: ScopeUpdate, edit: func(f *subFix) { f.items["coupons"] = arr{f.couponItem, f.couponItem} }},
		{line: 842, name: "second coupon", scope: ScopeUpdate, edit: func(f *subFix) { secondCoupon(f) }},
		{
			line: 855, name: "both limited to the same plan", scope: ScopeUpdate,
			setup: func(f *subFix) { f.updateCoupon(limitedToPlan(f.plan)) },
			edit:  func(f *subFix) { secondCoupon(f, limitedToPlan(f.plan)) },
			want:  []string{"billing_items.coupons.1.id: plan_overlapping"},
		},
		{
			line: 870, name: "limited to different plans", scope: ScopeUpdate,
			setup: func(f *subFix) { f.updateCoupon(limitedToPlan(f.plan)) },
			edit:  func(f *subFix) { secondCoupon(f, limitedToPlan(f.h.Plan(period.Monthly, 100, eurPlan))) },
		},
		{
			line: 884, name: "limited to a plan and to a metric it charges", scope: ScopeUpdate,
			setup: func(f *subFix) { f.updateCoupon(limitedToPlan(f.plan)) },
			edit: func(f *subFix) {
				secondCoupon(f, func(c *coupon.Coupon) { c.LimitedBillableMetricIDs = []core.BillableMetricID{f.metric.ID} })
			},
			want: []string{"billing_items.coupons.1.id: plan_overlapping"},
		},
		{line: 910, name: "fixed coupon currency differs", scope: ScopeUpdate, setup: usdCoupon, want: []string{"billing_items.coupons.0.id: currencies_does_not_match"}},
		{
			line: 920, name: "coupon override in the version currency", scope: ScopeUpdate, setup: usdCoupon,
			edit: func(f *subFix) { f.couponItem["overrides"] = obj{"amountCurrency": "EUR"} },
		},
		{
			line: 928, name: "coupon override in a third currency", scope: ScopeUpdate, setup: usdCoupon,
			edit: func(f *subFix) { f.couponItem["overrides"] = obj{"amountCurrency": "GBP"} },
			want: []string{"billing_items.coupons.0.id: currencies_does_not_match"},
		},
		{
			line: 947, name: "percentage coupon in another currency", scope: ScopeUpdate,
			setup: func(f *subFix) {
				f.updateCoupon(func(c *coupon.Coupon) { percentageCoupon(c); c.Currency = billingtest.USD })
			},
			edit: func(f *subFix) {
				replace(f.couponPayload, obj{"code": f.coupon.Code, "type": "percentage", "percentageRate": 10.0})
			},
		},
		{
			line: 956, name: "blank version currency skips the coupon currency", scope: ScopeUpdate,
			setup: func(f *subFix) { noCurrency(f); usdCoupon(f) },
		},
		{
			line: 967, name: "fixed coupon without amountCents at approve", scope: ScopeApprove,
			edit: func(f *subFix) {
				replace(f.couponPayload, obj{"code": f.coupon.Code, "type": "fixed_amount", "amountCents": nil})
			},
			want: []string{"billing_items.coupons.0.payload.amountCents: value_is_mandatory"},
		},
		{
			line: 977, name: "fixed coupon amountCents in the overrides", scope: ScopeApprove,
			edit: func(f *subFix) {
				replace(f.couponPayload, obj{"code": f.coupon.Code, "type": "fixed_amount"})
				f.couponItem["overrides"] = obj{"amountCents": 15_000}
			},
		},
		{
			line: 986, name: "percentage coupon without percentageRate", scope: ScopeApprove,
			setup: func(f *subFix) { f.updateCoupon(percentageCoupon) },
			edit: func(f *subFix) {
				replace(f.couponPayload, obj{"code": f.coupon.Code, "type": "percentage", "percentageRate": nil})
			},
			want: []string{"billing_items.coupons.0.payload.percentageRate: value_is_mandatory"},
		},
		{
			line: 999, name: "percentage coupon percentageRate in the overrides", scope: ScopeApprove,
			setup: func(f *subFix) { f.updateCoupon(percentageCoupon) },
			edit: func(f *subFix) {
				replace(f.couponPayload, obj{"code": f.coupon.Code, "type": "percentage"})
				f.couponItem["overrides"] = obj{"percentageRate": 15.0}
			},
		},
		{
			line: 1007, name: "coupon retyped since the snapshot", scope: ScopeApprove,
			setup: func(f *subFix) { f.updateCoupon(percentageCoupon) },
			want:  []string{"billing_items.coupons.0.payload.type: coupon_type_does_not_match"},
		},
		{
			line: 1018, name: "overridden to recurring without duration", scope: ScopeApprove,
			edit: func(f *subFix) { f.couponItem["overrides"] = obj{"frequency": "recurring"} },
			want: []string{"billing_items.coupons.0.overrides.frequencyDuration: value_is_mandatory"},
		},
		{
			line: 1031, name: "overridden to recurring with duration", scope: ScopeApprove,
			edit: func(f *subFix) { f.couponItem["overrides"] = obj{"frequency": "recurring", "frequencyDuration": 3} },
		},
		{
			line: 1039, name: "payload recurring without duration", scope: ScopeApprove,
			edit: func(f *subFix) { f.couponPayload["frequency"] = "recurring" },
			want: []string{"billing_items.coupons.0.payload.frequencyDuration: value_is_mandatory"},
		},
		{
			line: 1053, name: "recurring coupon overridden to recurring without duration", scope: ScopeApprove,
			setup: func(f *subFix) {
				recurring(f)
				f.updateCoupon(func(c *coupon.Coupon) { d := 3; c.FrequencyDuration = &d })
			},
			edit: func(f *subFix) { f.couponItem["overrides"] = obj{"frequency": "recurring"} },
		},
		{line: 1061, name: "overrides only the duration", scope: ScopeApprove, edit: func(f *subFix) { f.couponItem["overrides"] = obj{"frequencyDuration": 3} }},
		{line: 1070, name: "payload recurring without duration at update", scope: ScopeUpdate, edit: func(f *subFix) { f.couponPayload["frequency"] = "recurring" }},
		{
			line: 1078, name: "fixed coupon without amountCents at update", scope: ScopeUpdate,
			edit: func(f *subFix) { replace(f.couponPayload, obj{"code": f.coupon.Code, "type": "fixed_amount"}) },
		},
		{
			line: 1086, name: "paidCredits not a decimal", scope: ScopeUpdate,
			edit: func(f *subFix) { f.walletPayload["paidCredits"] = "abc" },
			want: []string{walletField + "paidCredits: invalid_value"},
		},
		{
			line: 1095, name: "negative grantedCredits", scope: ScopeUpdate,
			edit: func(f *subFix) { f.walletPayload["grantedCredits"] = "-1" },
			want: []string{walletField + "grantedCredits: invalid_value"},
		},
		{
			line: 1104, name: "zero rateAmount", scope: ScopeUpdate,
			edit: func(f *subFix) { f.walletPayload["rateAmount"] = "0" },
			want: []string{walletField + "rateAmount: invalid_value"},
		},
		{
			line: 1113, name: "interval trigger without interval", scope: ScopeUpdate,
			edit: func(f *subFix) { replace(f.rule, obj{"trigger": "interval", "interval": nil, "method": "fixed"}) },
			want: []string{ruleField0 + "interval: value_is_mandatory"},
		},
		{
			line: 1124, name: "threshold trigger without thresholdCredits", scope: ScopeUpdate,
			edit: func(f *subFix) {
				replace(f.rule, obj{"trigger": "threshold", "thresholdCredits": nil, "method": "fixed"})
			},
			want: []string{ruleField0 + "thresholdCredits: value_is_mandatory"},
		},
		{
			line: 1135, name: "thresholdCredits not a decimal", scope: ScopeUpdate,
			edit: func(f *subFix) {
				replace(f.rule, obj{"trigger": "threshold", "method": "fixed", "thresholdCredits": "abc"})
			},
			want: []string{ruleField0 + "thresholdCredits: invalid_value"},
		},
		{
			line: 1148, name: "target method without targetOngoingBalance", scope: ScopeUpdate,
			edit: func(f *subFix) {
				replace(f.rule, obj{"trigger": "interval", "interval": "monthly", "method": "target", "targetOngoingBalance": nil})
			},
			want: []string{ruleField0 + "targetOngoingBalance: value_is_mandatory"},
		},
		{
			line: 1161, name: "targetOngoingBalance not a decimal", scope: ScopeUpdate,
			edit: func(f *subFix) {
				replace(f.rule, obj{"trigger": "interval", "interval": "monthly", "method": "target", "targetOngoingBalance": "abc"})
			},
			want: []string{ruleField0 + "targetOngoingBalance: invalid_value"},
		},
		{
			line: 1174, name: "target below the threshold", scope: ScopeUpdate,
			edit: func(f *subFix) {
				replace(f.rule, obj{"trigger": "threshold", "method": "target", "thresholdCredits": "100", "targetOngoingBalance": "50"})
			},
			want: []string{ruleField0 + "targetOngoingBalance: invalid_value"},
		},
		{
			line: 1187, name: "target equals the threshold", scope: ScopeUpdate,
			edit: func(f *subFix) {
				replace(f.rule, obj{"trigger": "threshold", "method": "target", "thresholdCredits": "100", "targetOngoingBalance": "100"})
			},
		},
		{
			line: 1195, name: "rule paidCredits not a decimal", scope: ScopeUpdate,
			edit: func(f *subFix) { f.rule["paidCredits"] = "abc" },
			want: []string{ruleField0 + "paidCredits: invalid_value"},
		},
		{
			line: 1206, name: "wallet currency differs", scope: ScopeUpdate,
			edit: func(f *subFix) { f.walletPayload["currency"] = "USD" },
			want: []string{walletField + "currency: currencies_does_not_match"},
		},
		{
			line: 1221, name: "wallet limited to a known metric", scope: ScopeUpdate,
			edit: func(f *subFix) {
				f.walletPayload["appliesTo"] = obj{"feeTypes": arr{"charge"}, "billableMetricCodes": arr{f.metric.Code}}
			},
		},
		{
			line: 1231, name: "wallet limited to an unknown metric", scope: ScopeUpdate,
			edit: func(f *subFix) { f.walletPayload["appliesTo"] = obj{"billableMetricCodes": arr{"unknown_metric"}} },
			want: []string{walletField + "appliesTo.billableMetricCodes: billable_metric_not_found"},
		},
		{line: 1242, name: "wallet without currency", scope: ScopeUpdate, edit: func(f *subFix) { delete(f.walletPayload, "currency") }},
		{
			line: 1252, name: "target top-up without the target method", scope: ScopeUpdate,
			edit: func(f *subFix) {
				replace(f.rule, obj{"trigger": "interval", "interval": "monthly", "method": "fixed", "grantsTargetTopUp": true})
			},
			want: []string{ruleField0 + "grantsTargetTopUp: invalid_value"},
		},
		{
			line: 1271, name: "target top-up with the target method", scope: ScopeUpdate,
			edit: func(f *subFix) {
				replace(f.rule, obj{
					"trigger": "interval", "interval": "monthly", "method": "target",
					"targetOngoingBalance": "500", "grantsTargetTopUp": true,
				})
			},
		},
		{
			line: 1279, name: "rule expirationAt in the past at update", scope: ScopeUpdate,
			edit: func(f *subFix) { f.rule["expirationAt"] = iso(testNow.AddDate(0, 0, -1)) },
		},
		{
			line: 1286, name: "rule expirationAt in the past at approve", scope: ScopeApprove,
			edit: func(f *subFix) { f.rule["expirationAt"] = iso(testNow.AddDate(0, 0, -1)) },
			want: []string{ruleField0 + "expirationAt: invalid_date"},
		},
		{
			line: 1299, name: "wallet expirationAt in the past", scope: ScopeApprove,
			edit: func(f *subFix) { f.walletPayload["expirationAt"] = iso(testNow.AddDate(0, 0, -1)) },
			want: []string{walletField + "expirationAt: invalid_date"},
		},
		{
			line: 1311, name: "wallet expirationAt in the future", scope: ScopeApprove,
			edit: func(f *subFix) { f.walletPayload["expirationAt"] = iso(testNow.AddDate(1, 0, 0)) },
		},
		{
			line: 1326, name: "errors on several coupons", scope: ScopeUpdate,
			edit: func(f *subFix) {
				second := obj{"id": unknownID, "localId": f.couponItem["localId"], "payload": f.couponPayload}
				f.items["coupons"] = arr{f.couponItem, second}
			},
			want: []string{"billing_items.coupons.1.id: coupon_not_found"},
		},
	}
	runSubCases(t, source, cases, func(t *testing.T, f *subFix, scope Scope) []string {
		return runBusiness(t, f.h, f.q, f.v, scope, f.items)
	})
}

// TestCouponOverlapMatchesApplication is the spec that runs the overlapping
// pair through coupon application too, so that the quote's verdict and the
// application's stay in step.
func TestCouponOverlapMatchesApplication(t *testing.T) {
	t.Log("source: spec/services/quote_versions/validators/subscription_creation/business_validator_spec.rb:895")
	f := newSubFix(t)
	f.updateCoupon(limitedToPlan(f.plan))
	other := f.h.FixedAmountCoupon(200, eurCoupon, func(c *coupon.Coupon) {
		c.LimitedBillableMetricIDs = []core.BillableMetricID{f.metric.ID}
	})
	f.h.ApplyCoupon(f.coupon, f.cust)
	var err error
	f.h.Do(func(ctx context.Context, r port.Repos) error {
		b := &business{ctx: ctx, r: r, q: f.q}
		pm, perr := b.planMetrics([]*coupon.Coupon{f.coupon, other})
		if perr != nil {
			return perr
		}
		err = coupon.CheckApplication(other, coupon.ApplicationFacts{Active: []*coupon.Coupon{f.coupon}, PlanMetrics: pm})
		return nil
	})
	var re *core.RuleError
	if !errors.As(err, &re) || re.Code != coupon.CodePlanOverlapping {
		t.Errorf("application: got = %v, want = %s", err, coupon.CodePlanOverlapping)
	}
}

func TestSubscriptionCreationValidator(t *testing.T) {
	const source = "spec/services/quote_versions/validators/subscription_creation_validator_spec.rb"
	// The world of this spec: a plan with dates, a coupon and a wallet
	// credit, all typed.
	shape := func(f *subFix) {
		replace(f.planItem, obj{
			"id": f.plan.ID.String(), "localId": "3d08b2df-4e4c-4d58-b415-a525c1663735", "type": "plan",
			"payload": obj{"code": f.plan.Code, "startDate": "2026-01-01", "endDate": "2026-12-31"},
		})
		f.couponItem["type"] = "coupon"
		f.walletItem["type"] = "wallet_credit"
		replace(f.walletPayload, obj{"paidCredits": "100.0", "grantedCredits": "10.0", "rateAmount": "1.0"})
	}
	cases := []subCase{
		{line: 58, name: "complete valid version", scope: ScopeApprove, edit: shape},
		{
			line: 76, name: "structural errors first", scope: ScopeApprove,
			edit: func(f *subFix) {
				shape(f)
				f.planItem["overrides"] = obj{"amountCents": -1}
				f.v.Currency = doubloon()
			},
			want: []string{"billing_items.plans.0.overrides.amountCents: invalid_value"},
		},
		{
			line: 86, name: "business error", scope: ScopeApprove,
			edit: func(f *subFix) { shape(f); f.planItem["id"] = unknownID },
			want: []string{"billing_items.plans.0.id: plan_not_found"},
		},
		{
			line: 97, name: "structural errors skip the business pass", scope: ScopeApprove,
			edit: func(f *subFix) { shape(f); f.planItem["entitlementsOverrides"], f.planItem["id"] = arr{}, unknownID },
			want: []string{"billing_items.plans.0.entitlementsOverrides: unsupported_key"},
		},
		{line: 128, name: "nil billing items at update", scope: ScopeUpdate, edit: func(f *subFix) { f.items = nil }},
	}
	runSubCases(t, source, cases, func(t *testing.T, f *subFix, scope Scope) []string {
		var items any = f.items
		if f.items == nil {
			items = nil
		}
		return runValidate(t, f.h, f.q, f.v, scope, items)
	})
}

// TestCouponOverlapIgnoresDeletedPlans checks that a deleted plan charges
// no metric when coupons are compared, as lago's join on the kept plan
// has it (subscription_creation/business_validator.rb:400).
func TestCouponOverlapIgnoresDeletedPlans(t *testing.T) {
	f := newSubFix(t)
	f.updateCoupon(limitedToPlan(f.plan))
	f.updatePlan(func(p *catalog.Plan) {
		if err := p.MarkDeleted(f.h.Now()); err != nil {
			t.Fatal(err)
		}
	})
	f.build()
	other := f.h.FixedAmountCoupon(200, eurCoupon, func(c *coupon.Coupon) {
		c.LimitedBillableMetricIDs = []core.BillableMetricID{f.metric.ID}
	})
	f.items = obj{"coupons": arr{f.couponItem, obj{
		"id": other.ID.String(), "localId": "4c0e5e21-9f38-4a2b-8c7d-6e5f4a3b2c1d",
		"payload": obj{"code": other.Code, "type": "fixed_amount", "amountCents": 10_000},
	}}}
	checkErrors(t, runBusiness(t, f.h, f.q, f.v, ScopeUpdate, f.items), nil)
}
