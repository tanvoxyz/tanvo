package plans_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func reloadCharge(h *billingtest.Harness, id core.ChargeID) *catalog.Charge {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*catalog.Charge, error) {
		return r.Charges.Get(ctx, h.OrgID(), id)
	})
}

func planCharges(h *billingtest.Harness, id core.PlanID) []*catalog.Charge {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*catalog.Charge, error) {
		return r.Charges.ListByPlan(ctx, h.OrgID(), id)
	})
}

// chargeJobs returns the pending charge cascade jobs.
func chargeJobs(h *billingtest.Harness) []port.Job {
	kinds := []string{kernel.JobCreateChargeChildren, kernel.JobUpdateChargeChildren, kernel.JobDestroyChargeChildren, kernel.JobCascadeChargeFilter}
	return slices.DeleteFunc(h.Store.Jobs(), func(j port.Job) bool { return !slices.Contains(kinds, j.Kind) })
}

// runChargeJobs runs the pending jobs the plans service handles, and the
// jobs they enqueue (the cascades fan out in batches), until none is left.
func runChargeJobs(t *testing.T, h *billingtest.Harness, e *billing.Engine) {
	t.Helper()
	for range 10 {
		jobs := h.Store.TakeJobs()
		if len(jobs) == 0 {
			return
		}
		for _, j := range jobs {
			if _, err := e.Plans.HandleJob(h.Ctx, j); err != nil {
				t.Fatalf("HandleJob(%s) error = %v", j.Kind, err)
			}
		}
	}
	t.Fatalf("jobs still enqueued after 10 rounds: %v", h.JobKinds())
}

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error = %v, want = %s_not_found", err, resource)
	}
}

func wantInvalid(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) || !ve.Has(field, code) {
		t.Fatalf("error = %v, want = %s %s", err, field, code)
	}
}

func props(raw string) core.Optional[catalog.Properties] {
	return core.Some(catalog.MustProperties(raw))
}

func standardInput(m *catalog.BillableMetric, code string) plans.ChargeInput {
	return plans.ChargeInput{
		Code:           core.Some(code),
		BillableMetric: refs.ByID(m.ID),
		ChargeModel:    core.Some(catalog.ChargeModelStandard),
		Properties:     props(`{"amount":"100"}`),
	}
}

func TestCreateCharge(t *testing.T) {
	// source: spec/services/charges/create_service_spec.rb:14
	t.Run("plan not found", func(t *testing.T) {
		h, e := setup(t)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		_, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByCode("nope"), standardInput(m, "api"), false)
		wantNotFound(t, err, "plan")
	})

	// source: spec/services/charges/create_service_spec.rb:24
	t.Run("billable metric not found", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		in := standardInput(&catalog.BillableMetric{}, "api")
		in.BillableMetric = refs.ByCode("nope")
		_, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
		wantNotFound(t, err, "billable_metric")
	})

	// source: spec/services/charges/create_service_spec.rb:33
	t.Run("code already on the plan", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.Code = "api" })
		_, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), standardInput(m, "api"), false)
		wantInvalid(t, err, "code", core.CodeAlreadyExists)
	})

	// source: spec/services/charges/create_service_spec.rb:59
	t.Run("invalid properties", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		in := standardInput(m, "api")
		in.Properties = props(`{"amount":"abc"}`)
		if _, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false); !errors.Is(err, core.ErrInvalidValue) {
			t.Fatalf("error = %v, want = invalid value", err)
		}
		if got := planCharges(h, p.ID); len(got) != 0 {
			t.Errorf("charges = %d, want = 0", len(got))
		}
	})

	// source: spec/services/charges/create_service_spec.rb:95
	t.Run("valid params", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		m.Filters = []catalog.MetricFilter{{Key: "region", Values: []string{"eu", "us"}}}
		h.Do(func(ctx context.Context, r port.Repos) error { return r.Metrics.Update(ctx, m) })
		tax := h.Tax("vat", "20")
		in := standardInput(m, "api")
		in.MinAmount = core.Some(money.MustParse("12.34"))
		in.InvoiceDisplayName = core.Some(new("API calls"))
		in.Taxes = core.Some([]refs.Ref{refs.ByCode(tax.Code)})
		in.Filters = core.Some([]plans.ChargeFilterInput{{Values: map[string][]string{"region": {"eu"}}, Properties: props(`{"amount":"5"}`)}})
		c, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByCode(p.Code), in, false)
		if err != nil {
			t.Fatalf("CreateCharge() error = %v", err)
		}
		got := reloadCharge(h, c.ID)
		if got.Code != "api" || got.MinAmount != 1234 || !got.Invoiceable || got.InvoiceDisplayName == nil || *got.InvoiceDisplayName != "API calls" {
			t.Errorf("charge = %+v, want = code api, min 1234, invoiceable, named API calls", got)
		}
		if !slices.Equal(got.TaxIDs, []core.TaxID{tax.ID}) {
			t.Errorf("taxes = %v, want = %v", got.TaxIDs, []core.TaxID{tax.ID})
		}
		if len(got.Filters) != 1 || got.Filters[0].Code == "" {
			t.Errorf("filters = %+v, want = one coded filter", got.Filters)
		}
	})

	// source: spec/services/charges/create_service_spec.rb:168
	t.Run("filters the properties of the model", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		in := standardInput(m, "api")
		in.Properties = props(`{"amount":"100","package_size":10,"pricing_group_keys":["region"]}`)
		c, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
		if err != nil {
			t.Fatalf("CreateCharge() error = %v", err)
		}
		if want := catalog.MustProperties(`{"amount":"100","pricing_group_keys":["region"]}`); !c.Properties.Equal(want) {
			t.Errorf("properties = %s, want = %s", c.Properties.Raw(), want.Raw())
		}
	})

	// source: spec/services/charges/create_service_spec.rb:210
	t.Run("applied pricing unit", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		pu := &catalog.PricingUnit{ID: core.NewID[core.PricingUnitID](h.IDs), OrganizationID: h.OrgID(), Code: "credits", Name: "Credits", ShortName: "CR", CreatedAt: now, UpdatedAt: now}
		h.Do(func(ctx context.Context, r port.Repos) error { return r.PricingUnits.Create(ctx, pu) })
		tests := []struct {
			name    string
			unit    refs.Ref
			rate    string
			wantErr bool
		}{
			{name: "valid", unit: refs.ByCode("credits"), rate: "0.5"},
			{name: "unknown unit", unit: refs.ByCode("nope"), rate: "0.5", wantErr: true},
			{name: "zero rate", unit: refs.ByCode("credits"), rate: "0", wantErr: true},
		}
		for i, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				in := standardInput(m, "api_"+string(rune('a'+i)))
				in.AppliedPricingUnit = core.Some(&plans.PricingUnitInput{PricingUnit: tt.unit, ConversionRate: money.MustParse(tt.rate)})
				c, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
				if (err != nil) != tt.wantErr {
					t.Fatalf("CreateCharge() error = %v, want error = %t", err, tt.wantErr)
				}
				if err == nil && (c.AppliedPricingUnit == nil || c.AppliedPricingUnit.PricingUnitID != pu.ID) {
					t.Errorf("applied pricing unit = %+v, want = %s", c.AppliedPricingUnit, pu.ID)
				}
			})
		}
	})

	// source: spec/services/charges/create_children_service_spec.rb:39
	t.Run("cascades to override plans", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		live := h.Plan(period.Monthly, 0, overrideOf(p))
		idle := h.Plan(period.Monthly, 0, overrideOf(p))
		h.Subscription(h.Customer(), live)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		c, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), standardInput(m, "api"), true)
		if err != nil {
			t.Fatalf("CreateCharge() error = %v", err)
		}
		runChargeJobs(t, h, e)
		got := planCharges(h, live.ID)
		if len(got) != 1 || got[0].ParentID != c.ID || got[0].Code != "api" || !got[0].Properties.Equal(c.Properties) {
			t.Fatalf("live override charges = %+v, want = one copy of %s", got, c.ID)
		}
		if got := planCharges(h, idle.ID); len(got) != 0 {
			t.Errorf("idle override charges = %d, want = 0", len(got))
		}
	})
}

func TestCreateChargeChildren(t *testing.T) {
	// source: spec/services/charges/create_children_service_spec.rb:93
	t.Run("the child plan already has a copy", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		child := h.Plan(period.Monthly, 0, overrideOf(p))
		h.Subscription(h.Customer(), child)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		c, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), standardInput(m, "api"), true)
		if err != nil {
			t.Fatalf("CreateCharge() error = %v", err)
		}
		jobs := h.Store.Jobs()
		runChargeJobs(t, h, e)
		for _, j := range jobs {
			if _, err := e.Plans.HandleJob(h.Ctx, j); err != nil {
				t.Fatalf("HandleJob() error = %v", err)
			}
		}
		runChargeJobs(t, h, e)
		if got := planCharges(h, child.ID); len(got) != 1 || got[0].ParentID != c.ID {
			t.Errorf("child charges = %+v, want = one copy", got)
		}
	})
}

func TestGetAndListCharges(t *testing.T) {
	// source: spec/requests/api/v1/plans/charges_controller_spec.rb
	h, e := setup(t)
	p := h.Plan(period.Monthly, 0)
	m := h.Metric("api", catalog.AggregationTypeCount, "")
	c1 := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	c2 := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"2"}`)
	child := h.Plan(period.Monthly, 0, overrideOf(p))
	h.Charge(child, m, catalog.ChargeModelStandard, `{"amount":"3"}`, func(c *catalog.Charge) { c.ParentID = c1.ID })

	got, err := e.Plans.GetCharge(h.Ctx, h.OrgID(), refs.ByCode(p.Code), refs.ByCode(c2.Code))
	if err != nil || got.ID != c2.ID {
		t.Fatalf("GetCharge() = %v, %v, want = %s", got, err, c2.ID)
	}
	_, err = e.Plans.GetCharge(h.Ctx, h.OrgID(), refs.ByID(child.ID), refs.ByID(c1.ID))
	wantNotFound(t, err, "plan")

	page, err := e.Plans.ListCharges(h.Ctx, h.OrgID(), refs.ByID(p.ID), core.PageRequest{Size: 1})
	if err != nil {
		t.Fatalf("ListCharges() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != c2.ID || page.NextToken == "" {
		t.Fatalf("first page = %+v, want = [%s] and a token", page, c2.ID)
	}
	page, err = e.Plans.ListCharges(h.Ctx, h.OrgID(), refs.ByID(p.ID), core.PageRequest{Size: 1, Token: page.NextToken})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != c1.ID || page.NextToken != "" {
		t.Fatalf("second page = %+v, %v, want = [%s]", page, err, c1.ID)
	}
}

func TestUpdateCharge(t *testing.T) {
	// source: spec/services/charges/update_service_spec.rb:20
	t.Run("charge not found", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		_, err := e.Plans.UpdateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByCode("nope"), plans.ChargeInput{}, false)
		wantNotFound(t, err, "charge")
	})

	// source: spec/services/charges/update_service_spec.rb:30
	t.Run("code already on the plan", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.Code = "taken" })
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		_, err := e.Plans.UpdateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), plans.ChargeInput{Code: core.Some("taken")}, false)
		wantInvalid(t, err, "code", core.CodeAlreadyExists)
	})

	// source: spec/services/charges/update_service_spec.rb:57, 196, 210
	t.Run("updates the charge", func(t *testing.T) {
		tests := []struct {
			name     string
			attached bool
			wantCode string
			wantMin  int64
		}{
			{name: "plan without subscriptions", wantCode: "renamed", wantMin: 500},
			{name: "plan attached to subscriptions", attached: true, wantCode: "orig", wantMin: 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				p := h.Plan(period.Monthly, 0)
				m := h.Metric("api", catalog.AggregationTypeCount, "")
				c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.Code = "orig" })
				if tt.attached {
					h.Subscription(h.Customer(), p)
				}
				in := plans.ChargeInput{
					Code:               core.Some("renamed"),
					Properties:         props(`{"amount":"7"}`),
					MinAmount:          core.Some(money.MustParse("5")),
					InvoiceDisplayName: core.Some(new("API calls")),
				}
				if _, err := e.Plans.UpdateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), in, false); err != nil {
					t.Fatalf("UpdateCharge() error = %v", err)
				}
				got := reloadCharge(h, c.ID)
				if got.Code != tt.wantCode || got.MinAmount != tt.wantMin {
					t.Errorf("code, min = %s, %d, want = %s, %d", got.Code, got.MinAmount, tt.wantCode, tt.wantMin)
				}
				if !got.Properties.Equal(catalog.MustProperties(`{"amount":"7"}`)) || got.InvoiceDisplayName == nil || *got.InvoiceDisplayName != "API calls" {
					t.Errorf("properties, name = %s, %v, want = amount 7, API calls", got.Properties.Raw(), got.InvoiceDisplayName)
				}
			})
		}
	})

	// A model change without properties takes the new model's defaults.
	// source: app/services/charges/update_service.rb:43-50
	t.Run("model change takes the defaults", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		got, err := e.Plans.UpdateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), plans.ChargeInput{ChargeModel: core.Some(catalog.ChargeModelPackage)}, false)
		if err != nil {
			t.Fatalf("UpdateCharge() error = %v", err)
		}
		if want := catalog.MustProperties(`{"amount":"0","free_units":0,"package_size":1}`); got.ChargeModel != catalog.ChargeModelPackage || !got.Properties.Equal(want) {
			t.Errorf("model, properties = %s, %s, want = package, %s", got.ChargeModel, got.Properties.Raw(), want.Raw())
		}
	})
}

func TestUpdateChargeChildren(t *testing.T) {
	tests := []struct {
		name      string
		childProp string
		wantProp  string
	}{
		// source: spec/services/charges/update_children_service_spec.rb:82
		{name: "child not modified", childProp: `{"amount":"1"}`, wantProp: `{"amount":"9"}`},
		// source: spec/services/charges/update_children_service_spec.rb:108
		{name: "child modified", childProp: `{"amount":"4"}`, wantProp: `{"amount":"4"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, e := setup(t)
			p := h.Plan(period.Monthly, 0)
			m := h.Metric("api", catalog.AggregationTypeCount, "")
			c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
			childPlan := h.Plan(period.Monthly, 0, overrideOf(p))
			h.Subscription(h.Customer(), childPlan)
			child := h.Charge(childPlan, m, catalog.ChargeModelStandard, tt.childProp, func(x *catalog.Charge) { x.ParentID, x.Code = c.ID, c.Code })
			if _, err := e.Plans.UpdateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), plans.ChargeInput{Properties: props(`{"amount":"9"}`)}, true); err != nil {
				t.Fatalf("UpdateCharge() error = %v", err)
			}
			if got := len(chargeJobs(h)); got != 1 {
				t.Fatalf("cascade jobs = %d, want = 1", got)
			}
			runChargeJobs(t, h, e)
			if got := reloadCharge(h, child.ID).Properties; !got.Equal(catalog.MustProperties(tt.wantProp)) {
				t.Errorf("child properties = %s, want = %s", got.Raw(), tt.wantProp)
			}
		})
	}

	// source: spec/services/charges/update_children_service_spec.rb:128
	t.Run("no children", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		if _, err := e.Plans.UpdateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), plans.ChargeInput{Properties: props(`{"amount":"9"}`)}, true); err != nil {
			t.Fatalf("UpdateCharge() error = %v", err)
		}
		if got := len(chargeJobs(h)); got != 0 {
			t.Errorf("cascade jobs = %d, want = 0", got)
		}
	})
}

func TestDeleteCharge(t *testing.T) {
	// source: spec/services/charges/destroy_service_spec.rb:26
	t.Run("soft deletes the charge", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := h.Metric("api", catalog.AggregationTypeCount, "")
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		got, err := e.Plans.DeleteCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), false)
		if err != nil || got.DeletedAt == nil {
			t.Fatalf("DeleteCharge() = %v, %v, want = a discarded charge", got, err)
		}
		if reloadCharge(h, c.ID).DeletedAt == nil {
			t.Errorf("stored charge kept, want = discarded")
		}
	})

	// source: spec/services/charges/destroy_service_spec.rb:45
	t.Run("charge not found", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		_, err := e.Plans.DeleteCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByCode("nope"), false)
		wantNotFound(t, err, "charge")
	})

	tests := []struct {
		name     string
		cascade  bool
		children bool
		wantJobs int
	}{
		// source: spec/services/charges/destroy_service_spec.rb:54
		{name: "cascade with children", cascade: true, children: true, wantJobs: 1},
		// source: spec/services/charges/destroy_service_spec.rb:70
		{name: "cascade without children", cascade: true},
		// source: spec/services/charges/destroy_service_spec.rb:81
		{name: "no cascade with children", children: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, e := setup(t)
			p := h.Plan(period.Monthly, 0)
			m := h.Metric("api", catalog.AggregationTypeCount, "")
			c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
			var child *catalog.Charge
			if tt.children {
				cp := h.Plan(period.Monthly, 0, overrideOf(p))
				h.Subscription(h.Customer(), cp)
				child = h.Charge(cp, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(x *catalog.Charge) { x.ParentID = c.ID })
			}
			if _, err := e.Plans.DeleteCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), tt.cascade); err != nil {
				t.Fatalf("DeleteCharge() error = %v", err)
			}
			if got := len(chargeJobs(h)); got != tt.wantJobs {
				t.Fatalf("cascade jobs = %d, want = %d", got, tt.wantJobs)
			}
			runChargeJobs(t, h, e)
			// source: spec/services/charges/destroy_children_service_spec.rb:33
			if child != nil {
				if got, want := reloadCharge(h, child.ID).DeletedAt != nil, tt.cascade; got != want {
					t.Errorf("child discarded = %t, want = %t", got, want)
				}
			}
		})
	}
}

func filterMetric(h *billingtest.Harness) *catalog.BillableMetric {
	return h.Metric("api", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{
			{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "region", Values: []string{"eu", "us", "ap"}},
			{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "tier", Values: []string{"gold", "silver"}},
		}
	})
}

func TestChargeFiltersBatch(t *testing.T) {
	update := func(t *testing.T, h *billingtest.Harness, e *billing.Engine, p *catalog.Plan, c *catalog.Charge, in []plans.ChargeFilterInput) (*catalog.Charge, error) {
		t.Helper()
		return e.Plans.UpdateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), plans.ChargeInput{Filters: core.Some(in)}, false)
	}

	// source: spec/services/charge_filters/create_or_update_batch_service_spec.rb:38, 62
	t.Run("empty values", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := filterMetric(h)
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		_, err := update(t, h, e, p, c, []plans.ChargeFilterInput{
			{Values: map[string][]string{"region": {"eu"}}, Properties: props(`{"amount":"2"}`)},
			{Values: map[string][]string{}, Properties: props(`{"amount":"3"}`)},
		})
		wantInvalid(t, err, "values", core.CodeRequired)
		if got := reloadCharge(h, c.ID).Filters; len(got) != 0 {
			t.Errorf("filters = %d, want = 0", len(got))
		}
	})

	// source: spec/services/charge_filters/create_or_update_batch_service_spec.rb:94
	t.Run("empty list discards the filters", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := filterMetric(h)
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
			c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"2"}`)}
		})
		if _, err := update(t, h, e, p, c, nil); err != nil {
			t.Fatalf("UpdateCharge() error = %v", err)
		}
		if got := reloadCharge(h, c.ID).Filters; len(got) != 0 {
			t.Errorf("filters = %d, want = 0", len(got))
		}
	})

	// source: spec/services/charge_filters/create_or_update_batch_service_spec.rb:115, 158, 197, 207
	t.Run("new filters", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := filterMetric(h)
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		got, err := update(t, h, e, p, c, []plans.ChargeFilterInput{
			{Values: map[string][]string{"region": {"us"}}, Properties: props(`{"amount":"3","rate":"9","presentation_group_keys":[{"value":"x"}]}`)},
			{Values: map[string][]string{"region": {"eu"}, "tier": {"gold"}}, Properties: props(`{"amount":"2"}`)},
		})
		if err != nil {
			t.Fatalf("UpdateCharge() error = %v", err)
		}
		if len(got.Filters) != 2 || got.Filters[0].Values["region"][0] != "us" || got.Filters[1].Values["tier"][0] != "gold" {
			t.Fatalf("filters = %+v, want = us then eu/gold", got.Filters)
		}
		if want := catalog.MustProperties(`{"amount":"3"}`); !got.Filters[0].Properties.Equal(want) {
			t.Errorf("filter properties = %s, want = %s", got.Filters[0].Properties.Raw(), want.Raw())
		}
		if got.Filters[0].Code == "" || got.Filters[0].Code == got.Filters[1].Code {
			t.Errorf("filter codes = %q, %q, want = distinct codes", got.Filters[0].Code, got.Filters[1].Code)
		}
	})

	// source: spec/services/charge_filters/create_or_update_batch_service_spec.rb:227, 329, 388
	t.Run("kept, new and removed filters", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := filterMetric(h)
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
			c.Filters = []catalog.ChargeFilter{
				billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"2"}`),
				billingtest.ChargeFilter(map[string][]string{"region": {"us"}}, `{"amount":"3"}`),
			}
			c.Filters[0].Code = "eu"
		})
		kept := c.Filters[0].ID
		got, err := update(t, h, e, p, c, []plans.ChargeFilterInput{
			{Values: map[string][]string{"region": {"ap"}}, Properties: props(`{"amount":"4"}`)},
			{Values: map[string][]string{"region": {"eu"}}, Properties: props(`{"amount":"5"}`)},
		})
		if err != nil {
			t.Fatalf("UpdateCharge() error = %v", err)
		}
		if len(got.Filters) != 2 {
			t.Fatalf("filters = %d, want = 2", len(got.Filters))
		}
		if f := got.Filters[1]; f.ID != kept || f.Code != "eu" || !f.Properties.Equal(catalog.MustProperties(`{"amount":"5"}`)) {
			t.Errorf("kept filter = %+v, want = %s with code eu and amount 5", f, kept)
		}
		if got.Filters[0].Values["region"][0] != "ap" {
			t.Errorf("first filter = %v, want = ap", got.Filters[0].Values)
		}
	})
}

func TestChargeFilterServices(t *testing.T) {
	// source: spec/services/charge_filters/create_service_spec.rb:40, 75, 194, 240
	t.Run("create", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := filterMetric(h)
		c := h.Charge(p, m, catalog.ChargeModelGraduated, `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"1","flat_amount":"0"}]}`)
		_, err := e.Plans.CreateChargeFilter(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), plans.ChargeFilterInput{}, false)
		wantInvalid(t, err, "values", core.CodeRequired)
		f, err := e.Plans.CreateChargeFilter(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), plans.ChargeFilterInput{
			Values:     map[string][]string{"region": {"eu"}},
			Properties: props(`{"amount":"9","graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"2","flat_amount":"0"}],"presentation_group_keys":[{"value":"x"}]}`),
		}, false)
		if err != nil {
			t.Fatalf("CreateChargeFilter() error = %v", err)
		}
		want := catalog.MustProperties(`{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"2","flat_amount":"0"}]}`)
		if !f.Properties.Equal(want) {
			t.Errorf("properties = %s, want = %s", f.Properties.Raw(), want.Raw())
		}
		if got := reloadCharge(h, c.ID).Filters; len(got) != 1 || got[0].ID != f.ID {
			t.Errorf("stored filters = %+v, want = [%s]", got, f.ID)
		}
	})

	// source: spec/services/charge_filters/update_service_spec.rb:24, 34; spec/services/charge_filters/destroy_service_spec.rb:21, 31
	t.Run("update and destroy", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := filterMetric(h)
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
			c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"2"}`)}
		})
		id := c.Filters[0].ID
		_, err := e.Plans.UpdateChargeFilter(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), core.NewID[core.ChargeFilterID](h.IDs), plans.ChargeFilterInput{}, false)
		wantNotFound(t, err, "charge_filter")
		f, err := e.Plans.UpdateChargeFilter(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), id, plans.ChargeFilterInput{
			Properties: props(`{"amount":"7"}`), InvoiceDisplayName: core.Some(new("Europe")),
		}, false)
		if err != nil {
			t.Fatalf("UpdateChargeFilter() error = %v", err)
		}
		if !f.Properties.Equal(catalog.MustProperties(`{"amount":"7"}`)) || f.InvoiceDisplayName == nil || *f.InvoiceDisplayName != "Europe" {
			t.Errorf("filter = %+v, want = amount 7, Europe", f)
		}
		page, err := e.Plans.ListChargeFilters(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), core.PageRequest{})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("ListChargeFilters() = %+v, %v, want = one filter", page, err)
		}
		if _, err := e.Plans.DeleteChargeFilter(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), id, false); err != nil {
			t.Fatalf("DeleteChargeFilter() error = %v", err)
		}
		if got := reloadCharge(h, c.ID).Filters; len(got) != 0 {
			t.Errorf("filters = %d, want = 0", len(got))
		}
	})

	// source: spec/services/charge_filters/create_service_spec.rb:111, 150, 177; spec/services/charge_filters/update_service_spec.rb:91, 119; spec/services/charge_filters/destroy_service_spec.rb:54, 82
	t.Run("cascade jobs", func(t *testing.T) {
		tests := []struct {
			name     string
			cascade  bool
			children bool
			wantJobs int
		}{
			{name: "cascade with children", cascade: true, children: true, wantJobs: 3},
			{name: "no cascade with children", children: true},
			{name: "cascade without children", cascade: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				p := h.Plan(period.Monthly, 0)
				m := filterMetric(h)
				c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
				if tt.children {
					cp := h.Plan(period.Monthly, 0, overrideOf(p))
					h.Subscription(h.Customer(), cp)
					h.Charge(cp, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(x *catalog.Charge) { x.ParentID = c.ID })
				}
				f, err := e.Plans.CreateChargeFilter(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), plans.ChargeFilterInput{Values: map[string][]string{"region": {"eu"}}, Properties: props(`{"amount":"2"}`)}, tt.cascade)
				if err != nil {
					t.Fatalf("CreateChargeFilter() error = %v", err)
				}
				if _, err := e.Plans.UpdateChargeFilter(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), f.ID, plans.ChargeFilterInput{Properties: props(`{"amount":"3"}`)}, tt.cascade); err != nil {
					t.Fatalf("UpdateChargeFilter() error = %v", err)
				}
				if _, err := e.Plans.DeleteChargeFilter(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), f.ID, tt.cascade); err != nil {
					t.Fatalf("DeleteChargeFilter() error = %v", err)
				}
				if got := len(chargeJobs(h)); got != tt.wantJobs {
					t.Errorf("cascade jobs = %d, want = %d", got, tt.wantJobs)
				}
			})
		}
	})
}

func TestCascadeChargeFilter(t *testing.T) {
	type fixture struct {
		h     *billingtest.Harness
		e     *billing.Engine
		p     *catalog.Plan
		c     *catalog.Charge
		child *catalog.Charge
	}
	build := func(t *testing.T, childFilter string) fixture {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := filterMetric(h)
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
			c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"2"}`)}
			c.Filters[0].Code = "eu"
		})
		cp := h.Plan(period.Monthly, 0, overrideOf(p))
		h.Subscription(h.Customer(), cp)
		child := h.Charge(cp, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(x *catalog.Charge) {
			x.ParentID = c.ID
			if childFilter != "" {
				x.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, childFilter)}
				x.Filters[0].Code = "eu"
			}
		})
		return fixture{h, e, p, c, child}
	}

	// source: spec/services/charge_filters/cascade_service_spec.rb:34, 86, 99
	t.Run("update", func(t *testing.T) {
		tests := []struct {
			name        string
			childFilter string
			newProps    string
			want        string
		}{
			{name: "not customized", childFilter: `{"amount":"2"}`, newProps: `{"amount":"5"}`, want: `{"amount":"5"}`},
			{name: "customized", childFilter: `{"amount":"8"}`, newProps: `{"amount":"5"}`, want: `{"amount":"8"}`},
			{name: "customized gets pricing_group_keys", childFilter: `{"amount":"8"}`, newProps: `{"amount":"5","pricing_group_keys":["tier"]}`, want: `{"amount":"8","pricing_group_keys":["tier"]}`},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				f := build(t, tt.childFilter)
				if _, err := f.e.Plans.UpdateChargeFilter(f.h.Ctx, f.h.OrgID(), refs.ByID(f.p.ID), refs.ByID(f.c.ID), f.c.Filters[0].ID, plans.ChargeFilterInput{Properties: props(tt.newProps)}, true); err != nil {
					t.Fatalf("UpdateChargeFilter() error = %v", err)
				}
				runChargeJobs(t, f.h, f.e)
				got := reloadCharge(f.h, f.child.ID).Filters
				if len(got) != 1 || !got[0].Properties.Equal(catalog.MustProperties(tt.want)) {
					t.Errorf("child filters = %+v, want = %s", got, tt.want)
				}
			})
		}
	})

	// source: spec/services/charge_filters/cascade_service_spec.rb:229, 250, 272
	t.Run("create", func(t *testing.T) {
		for _, existing := range []bool{false, true} {
			f := build(t, "")
			if existing {
				f.child.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"us"}}, `{"amount":"6"}`)}
				f.child.Filters[0].ID = core.NewID[core.ChargeFilterID](f.h.IDs)
				f.child.Filters[0].ChargeID = f.child.ID
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Charges.Update(ctx, f.child) })
			}
			pf, err := f.e.Plans.CreateChargeFilter(f.h.Ctx, f.h.OrgID(), refs.ByID(f.p.ID), refs.ByID(f.c.ID), plans.ChargeFilterInput{Values: map[string][]string{"region": {"us"}}, Properties: props(`{"amount":"4"}`)}, true)
			if err != nil {
				t.Fatalf("CreateChargeFilter() error = %v", err)
			}
			runChargeJobs(t, f.h, f.e)
			got := reloadCharge(f.h, f.child.ID).Filters
			if len(got) != 1 || got[0].Code != pf.Code {
				t.Fatalf("existing %t: child filters = %+v, want = one filter with code %s", existing, got, pf.Code)
			}
			want := `{"amount":"4"}`
			if existing {
				want = `{"amount":"6"}`
			}
			if !got[0].Properties.Equal(catalog.MustProperties(want)) {
				t.Errorf("existing %t: child filter properties = %s, want = %s", existing, got[0].Properties.Raw(), want)
			}
		}
	})

	// source: app/services/charge_filters/cascade_service.rb:45-52
	t.Run("destroy", func(t *testing.T) {
		f := build(t, `{"amount":"2"}`)
		if _, err := f.e.Plans.DeleteChargeFilter(f.h.Ctx, f.h.OrgID(), refs.ByID(f.p.ID), refs.ByID(f.c.ID), f.c.Filters[0].ID, true); err != nil {
			t.Fatalf("DeleteChargeFilter() error = %v", err)
		}
		runChargeJobs(t, f.h, f.e)
		if got := reloadCharge(f.h, f.child.ID).Filters; len(got) != 0 {
			t.Errorf("child filters = %d, want = 0", len(got))
		}
	})
}

func TestOverrideCharge(t *testing.T) {
	// source: spec/services/charges/override_service_spec.rb:42, 87, 208
	h, e := setup(t)
	p := h.Plan(period.Monthly, 0)
	m := filterMetric(h)
	tax := h.Tax("vat", "20")
	c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
		c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"2"}`)}
		c.Filters[0].Code = "eu"
		c.TaxIDs = []core.TaxID{tax.ID}
	})
	s := h.Subscription(h.Customer(), p)
	got, err := e.Plans.UpdateSubscriptionCharge(h.Ctx, h.OrgID(), refs.SubscriptionRef{ExternalID: s.ExternalID}, refs.ByID(c.ID), plans.ChargeInput{})
	if err != nil {
		t.Fatalf("UpdateSubscriptionCharge() error = %v", err)
	}
	if got.ParentID != c.ID || got.Code != c.Code || !got.Properties.Equal(c.Properties) {
		t.Errorf("override = %+v, want = a copy of %s", got, c.ID)
	}
	if len(got.Filters) != 1 || got.Filters[0].Code != "eu" || got.Filters[0].ID == c.Filters[0].ID {
		t.Errorf("override filters = %+v, want = a copy with code eu", got.Filters)
	}
	if after := reloadCharge(h, c.ID); !after.Properties.Equal(c.Properties) || len(after.Filters) != 1 {
		t.Errorf("parent charge changed: %+v", after)
	}
	// Without tax codes the override has no taxes of its own.
	if len(got.TaxIDs) != 0 {
		t.Errorf("override taxes got = %v, want = none", got.TaxIDs)
	}

	// source: spec/services/charges/override_service_spec.rb:65
	other := h.Tax("gst", "10")
	s2 := h.Subscription(h.Customer(), p)
	got, err = e.Plans.UpdateSubscriptionCharge(h.Ctx, h.OrgID(), refs.SubscriptionRef{ExternalID: s2.ExternalID}, refs.ByID(c.ID),
		plans.ChargeInput{Taxes: core.Some([]refs.Ref{refs.ByCode(other.Code)})})
	if err != nil {
		t.Fatalf("UpdateSubscriptionCharge() with taxes error = %v", err)
	}
	if !slices.Equal(got.TaxIDs, []core.TaxID{other.ID}) {
		t.Errorf("override taxes got = %v, want = [%s]", got.TaxIDs, other.ID)
	}
}

func TestUpdateSubscriptionCharge(t *testing.T) {
	type fixture struct {
		h *billingtest.Harness
		e *billing.Engine
		p *catalog.Plan
		m *catalog.BillableMetric
		c *catalog.Charge
	}
	build := func(t *testing.T) fixture {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := filterMetric(h)
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		return fixture{h, e, p, m, c}
	}
	in := plans.ChargeInput{
		InvoiceDisplayName: core.Some(new("Overridden Charge")),
		MinAmount:          core.Some(money.MustParse("5")),
		Properties:         props(`{"amount":"150"}`),
	}
	check := func(t *testing.T, f fixture, got *catalog.Charge, plan core.PlanID) {
		t.Helper()
		if got.ParentID != f.c.ID || got.PlanID != plan {
			t.Errorf("override parent, plan = %s, %s, want = %s, %s", got.ParentID, got.PlanID, f.c.ID, plan)
		}
		if got.InvoiceDisplayName == nil || *got.InvoiceDisplayName != "Overridden Charge" || got.MinAmount != 500 || !got.Properties.Equal(catalog.MustProperties(`{"amount":"150"}`)) {
			t.Errorf("override = %+v, want = the override params", got)
		}
	}

	// source: spec/services/subscriptions/update_or_override_charge_service_spec.rb:32
	t.Run("creates the plan and charge overrides", func(t *testing.T) {
		f := build(t)
		s := f.h.Subscription(f.h.Customer(), f.p)
		got, err := f.e.Plans.UpdateSubscriptionCharge(f.h.Ctx, f.h.OrgID(), refs.SubscriptionRef{ID: s.ID}, refs.ByCode(f.c.Code), in)
		if err != nil {
			t.Fatalf("UpdateSubscriptionCharge() error = %v", err)
		}
		plan := reloadSubscription(f.h, s.ID).PlanID
		if np := reloadPlan(f.h, plan); np.ParentID != f.p.ID {
			t.Fatalf("subscription plan parent = %s, want = %s", np.ParentID, f.p.ID)
		}
		check(t, f, got, plan)
		if n := len(planCharges(f.h, plan)); n != 1 {
			t.Errorf("override plan charges = %d, want = 1", n)
		}
	})

	// source: spec/services/subscriptions/update_or_override_charge_service_spec.rb:77, 93, 111
	t.Run("existing overrides", func(t *testing.T) {
		for _, byOverride := range []bool{false, true} {
			f := build(t)
			cp := f.h.Plan(period.Monthly, 0, overrideOf(f.p))
			oc := f.h.Charge(cp, f.m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(x *catalog.Charge) { x.ParentID, x.Code = f.c.ID, f.c.Code })
			s := f.h.Subscription(f.h.Customer(), cp)
			// The subscription's plan holds the override under the
			// parent's code (subscriptions/charges_controller.rb:91-94).
			ref := refs.ByCode(f.c.Code)
			if byOverride {
				ref = refs.ByID(oc.ID)
			}
			got, err := f.e.Plans.UpdateSubscriptionCharge(f.h.Ctx, f.h.OrgID(), refs.SubscriptionRef{ID: s.ID}, ref, in)
			if err != nil {
				t.Fatalf("UpdateSubscriptionCharge() error = %v", err)
			}
			if got.ID != oc.ID {
				t.Errorf("by override %t: updated = %s, want = %s", byOverride, got.ID, oc.ID)
			}
			check(t, f, got, cp.ID)
			if n := len(planCharges(f.h, cp.ID)); n != 1 {
				t.Errorf("override plan charges = %d, want = 1", n)
			}
		}
	})

	// source: spec/services/subscriptions/update_or_override_charge_service_spec.rb:129
	t.Run("top-level charge on the override plan", func(t *testing.T) {
		f := build(t)
		cp := f.h.Plan(period.Monthly, 0, overrideOf(f.p))
		top := f.h.Charge(cp, f.m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		s := f.h.Subscription(f.h.Customer(), cp)
		got, err := f.e.Plans.UpdateSubscriptionCharge(f.h.Ctx, f.h.OrgID(), refs.SubscriptionRef{ID: s.ID}, refs.ByID(top.ID), in)
		if err != nil {
			t.Fatalf("UpdateSubscriptionCharge() error = %v", err)
		}
		if got.ID != top.ID || got.MinAmount != 500 {
			t.Errorf("updated = %+v, want = %s in place", got, top.ID)
		}
	})

	// source: spec/services/subscriptions/update_or_override_charge_service_spec.rb:160, 176
	t.Run("taxes and filters", func(t *testing.T) {
		f := build(t)
		tax := f.h.Tax("vat", "20")
		s := f.h.Subscription(f.h.Customer(), f.p)
		got, err := f.e.Plans.UpdateSubscriptionCharge(f.h.Ctx, f.h.OrgID(), refs.SubscriptionRef{ID: s.ID}, refs.ByID(f.c.ID), plans.ChargeInput{
			Taxes:   core.Some([]refs.Ref{refs.ByCode("vat")}),
			Filters: core.Some([]plans.ChargeFilterInput{{Values: map[string][]string{"region": {"eu"}}, Properties: props(`{"amount":"3"}`)}}),
		})
		if err != nil {
			t.Fatalf("UpdateSubscriptionCharge() error = %v", err)
		}
		if !slices.Equal(got.TaxIDs, []core.TaxID{tax.ID}) || len(got.Filters) != 1 {
			t.Errorf("override taxes, filters = %v, %+v, want = [vat], one filter", got.TaxIDs, got.Filters)
		}
	})

	// source: spec/requests/api/v1/subscriptions/charges_controller_spec.rb
	t.Run("not found", func(t *testing.T) {
		f := build(t)
		s := f.h.Subscription(f.h.Customer(), f.p)
		_, err := f.e.Plans.UpdateSubscriptionCharge(f.h.Ctx, f.h.OrgID(), refs.SubscriptionRef{ExternalID: "nope"}, refs.ByID(f.c.ID), in)
		wantNotFound(t, err, "subscription")
		_, err = f.e.Plans.GetSubscriptionCharge(f.h.Ctx, f.h.OrgID(), refs.SubscriptionRef{ID: s.ID}, refs.ByCode("nope"))
		wantNotFound(t, err, "charge")
	})
}

func TestSubscriptionChargeFilters(t *testing.T) {
	type fixture struct {
		h *billingtest.Harness
		e *billing.Engine
		p *catalog.Plan
		c *catalog.Charge
		s refs.SubscriptionRef
	}
	build := func(t *testing.T) fixture {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 0)
		m := filterMetric(h)
		c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
			c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"2"}`)}
			c.Filters[0].Code = "eu"
		})
		s := h.Subscription(h.Customer(), p)
		return fixture{h, e, p, c, refs.SubscriptionRef{ExternalID: s.ExternalID}}
	}

	// source: spec/services/subscriptions/charge_filters/create_service_spec.rb:33, 116, 131, 149
	t.Run("create", func(t *testing.T) {
		f := build(t)
		_, err := f.e.Plans.CreateSubscriptionChargeFilter(f.h.Ctx, f.h.OrgID(), f.s, refs.ByID(f.c.ID), plans.ChargeFilterInput{})
		wantInvalid(t, err, "values", core.CodeRequired)
		_, err = f.e.Plans.CreateSubscriptionChargeFilter(f.h.Ctx, f.h.OrgID(), refs.SubscriptionRef{ExternalID: "nope"}, refs.ByID(f.c.ID), plans.ChargeFilterInput{Values: map[string][]string{"region": {"us"}}})
		wantNotFound(t, err, "subscription")
		got, err := f.e.Plans.CreateSubscriptionChargeFilter(f.h.Ctx, f.h.OrgID(), f.s, refs.ByID(f.c.ID), plans.ChargeFilterInput{Values: map[string][]string{"region": {"us"}}, Properties: props(`{"amount":"4"}`)})
		if err != nil {
			t.Fatalf("CreateSubscriptionChargeFilter() error = %v", err)
		}
		oc := reloadCharge(f.h, got.ChargeID)
		if oc.ParentID != f.c.ID || len(oc.Filters) != 2 {
			t.Errorf("override = %+v, want = a copy of %s with two filters", oc, f.c.ID)
		}
		if n := len(reloadCharge(f.h, f.c.ID).Filters); n != 1 {
			t.Errorf("parent filters = %d, want = 1", n)
		}
		_, err = f.e.Plans.CreateSubscriptionChargeFilter(f.h.Ctx, f.h.OrgID(), f.s, refs.ByCode(f.c.Code), plans.ChargeFilterInput{Values: map[string][]string{"region": {"eu"}}})
		wantInvalid(t, err, "values", "value_already_exists")
	})

	// source: spec/services/subscriptions/charge_filters/update_or_override_service_spec.rb:37, 98, 121, 156
	t.Run("update", func(t *testing.T) {
		f := build(t)
		_, err := f.e.Plans.UpdateSubscriptionChargeFilter(f.h.Ctx, f.h.OrgID(), f.s, refs.ByID(f.c.ID), core.NewID[core.ChargeFilterID](f.h.IDs), plans.ChargeFilterInput{})
		wantNotFound(t, err, "charge_filter")
		got, err := f.e.Plans.UpdateSubscriptionChargeFilter(f.h.Ctx, f.h.OrgID(), f.s, refs.ByID(f.c.ID), f.c.Filters[0].ID, plans.ChargeFilterInput{Properties: props(`{"amount":"9"}`)})
		if err != nil {
			t.Fatalf("UpdateSubscriptionChargeFilter() error = %v", err)
		}
		if got.ChargeID == f.c.ID || !got.Properties.Equal(catalog.MustProperties(`{"amount":"9"}`)) || got.Values["region"][0] != "eu" {
			t.Errorf("filter = %+v, want = the override's eu filter at amount 9", got)
		}
		again, err := f.e.Plans.UpdateSubscriptionChargeFilter(f.h.Ctx, f.h.OrgID(), f.s, refs.ByCode(f.c.Code), got.ID, plans.ChargeFilterInput{InvoiceDisplayName: core.Some(new("EU"))})
		if err != nil {
			t.Fatalf("UpdateSubscriptionChargeFilter() error = %v", err)
		}
		if again.ID != got.ID || !again.Properties.Equal(got.Properties) || again.InvoiceDisplayName == nil || *again.InvoiceDisplayName != "EU" {
			t.Errorf("filter = %+v, want = %s renamed EU", again, got.ID)
		}
		if p := reloadCharge(f.h, f.c.ID).Filters[0].Properties; !p.Equal(catalog.MustProperties(`{"amount":"2"}`)) {
			t.Errorf("parent filter properties = %s, want = amount 2", p.Raw())
		}
	})

	// source: spec/services/subscriptions/charge_filters/destroy_service_spec.rb:31, 64, 130
	t.Run("destroy", func(t *testing.T) {
		f := build(t)
		_, err := f.e.Plans.DeleteSubscriptionChargeFilter(f.h.Ctx, f.h.OrgID(), f.s, refs.ByID(f.c.ID), core.NewID[core.ChargeFilterID](f.h.IDs))
		wantNotFound(t, err, "charge_filter")
		got, err := f.e.Plans.DeleteSubscriptionChargeFilter(f.h.Ctx, f.h.OrgID(), f.s, refs.ByID(f.c.ID), f.c.Filters[0].ID)
		if err != nil {
			t.Fatalf("DeleteSubscriptionChargeFilter() error = %v", err)
		}
		oc := reloadCharge(f.h, got.ChargeID)
		if oc.ParentID != f.c.ID || len(oc.Filters) != 0 {
			t.Errorf("override = %+v, want = a copy of %s without filters", oc, f.c.ID)
		}
		page, err := f.e.Plans.ListSubscriptionChargeFilters(f.h.Ctx, f.h.OrgID(), f.s, refs.ByCode(f.c.Code), core.PageRequest{})
		if err != nil || len(page.Items) != 0 {
			t.Errorf("ListSubscriptionChargeFilters() = %+v, %v, want = none", page, err)
		}
	})
}

func TestChargeChildrenBatches(t *testing.T) {
	// source: app/jobs/charges/create_children_job.rb:11, app/jobs/charges/update_children_job.rb:11
	h, e := setup(t)
	p := h.Plan(period.Monthly, 0)
	m := h.Metric("api", catalog.AggregationTypeCount, "")
	children := make([]*catalog.Plan, 21)
	for i := range children {
		children[i] = h.Plan(period.Monthly, 0, overrideOf(p))
		h.Subscription(h.Customer(), children[i])
	}
	// An override plan without active or pending subscriptions is skipped.
	idle := h.Plan(period.Monthly, 0, overrideOf(p))
	c, err := e.Plans.CreateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), standardInput(m, "api"), true)
	if err != nil {
		t.Fatalf("CreateCharge() error = %v", err)
	}
	batches := func(kind string) []int {
		t.Helper()
		var sizes []int
		for _, j := range h.Store.TakeJobs() {
			if _, err := e.Plans.HandleJob(h.Ctx, j); err != nil {
				t.Fatalf("HandleJob(%s) error = %v", j.Kind, err)
			}
		}
		for _, j := range h.Store.Jobs() {
			if j.Kind != kind {
				continue
			}
			var a struct {
				PlanIDs  []core.PlanID   `json:"plan_ids"`
				ChildIDs []core.ChargeID `json:"child_ids"`
			}
			if err := json.Unmarshal(j.Args, &a); err != nil {
				t.Fatal(err)
			}
			sizes = append(sizes, len(a.PlanIDs)+len(a.ChildIDs))
		}
		runChargeJobs(t, h, e)
		return sizes
	}
	if got, want := batches(kernel.JobCreateChargeChildren), []int{20, 1}; !slices.Equal(got, want) {
		t.Errorf("create batches got = %v, want = %v", got, want)
	}
	for _, cp := range children {
		if got := planCharges(h, cp.ID); len(got) != 1 || got[0].ParentID != c.ID {
			t.Fatalf("charges of override plan %s got = %d, want = one copy", cp.ID, len(got))
		}
	}
	if got := planCharges(h, idle.ID); len(got) != 0 {
		t.Errorf("charges of the idle override plan got = %d, want = 0", len(got))
	}
	if _, err := e.Plans.UpdateCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(c.ID), plans.ChargeInput{Properties: props(`{"amount":"9"}`)}, true); err != nil {
		t.Fatalf("UpdateCharge() error = %v", err)
	}
	if got, want := batches(kernel.JobUpdateChargeChildren), []int{20, 1}; !slices.Equal(got, want) {
		t.Errorf("update batches got = %v, want = %v", got, want)
	}
	for _, cp := range children {
		if got := planCharges(h, cp.ID)[0].Properties; !got.Equal(catalog.MustProperties(`{"amount":"9"}`)) {
			t.Fatalf("override properties got = %s, want = {\"amount\":\"9\"}", got.Raw())
		}
	}
}
