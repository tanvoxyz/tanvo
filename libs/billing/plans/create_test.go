package plans_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var eur = money.MustCurrency("EUR")

// hasFieldError reports whether err is a validation error with code on
// field.
func hasFieldError(err error, field, code string) bool {
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	return slices.ContainsFunc(ve.Errors, func(e core.FieldError) bool { return e.Field == field && e.Code == code })
}

func thresholds(h *billingtest.Harness, id core.PlanID) []*catalog.UsageThreshold {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*catalog.UsageThreshold, error) {
		return r.UsageThresholds.ListByPlan(ctx, h.OrgID(), id)
	})
}

// commitment returns the minimum commitment of the plan, nil for none.
func commitment(h *billingtest.Harness, id core.PlanID) *catalog.Commitment {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*catalog.Commitment, error) {
		c, err := r.Commitments.GetByPlan(ctx, h.OrgID(), id, catalog.CommitmentTypeMinimumCommitment)
		if errors.Is(err, core.ErrNotFound) {
			return nil, nil
		}
		return c, err
	})
}

func createInput() plans.CreateInput {
	return plans.CreateInput{
		Code: "new_plan", Name: "New Plan", InvoiceDisplayName: new("New Plan Invoice Name"),
		Interval: period.Monthly, Amount: 200, Currency: eur,
	}
}

func TestCreate(t *testing.T) {
	// source: spec/services/plans/create_service_spec.rb:132
	t.Run("creates a plan with its taxes, thresholds and commitment", func(t *testing.T) {
		h, e := setup(t)
		tax := h.Tax("tax_code", "20")
		in := createInput()
		in.Taxes = []refs.Ref{refs.ByCode(tax.Code)}
		in.UsageThresholds = []plans.ThresholdInput{
			{Amount: new(int64(1000)), ThresholdDisplayName: new("Threshold 1")},
			{Amount: new(int64(10000))},
			{Amount: new(int64(100)), Recurring: true},
		}
		in.MinimumCommitment = &plans.CommitmentInput{
			Amount: core.Some(int64(100)), InvoiceDisplayName: core.Some(new("Minimum spending")),
			Taxes: core.Some([]refs.Ref{refs.ByCode(tax.Code)}),
		}
		p, err := e.Plans.Create(h.Ctx, h.OrgID(), in)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		got := reloadPlan(h, p.ID)
		if got.Code != "new_plan" || got.Amount != 200 || got.Currency.Code() != "EUR" || *got.InvoiceDisplayName != "New Plan Invoice Name" {
			t.Errorf("plan = %+v, want = new_plan 200 EUR", got)
		}
		if !slices.Equal(got.TaxIDs, []core.TaxID{tax.ID}) {
			t.Errorf("taxes = %v, want = [%s]", got.TaxIDs, tax.ID)
		}
		// source: spec/services/plans/create_service_spec.rb:189
		ts := thresholds(h, p.ID)
		if len(ts) != 3 || ts[2].Amount != 100 || !ts[2].Recurring {
			t.Fatalf("thresholds = %d, want = 3 with the recurring one last", len(ts))
		}
		c := commitment(h, p.ID)
		if c == nil || c.Amount != 100 || *c.InvoiceDisplayName != "Minimum spending" || !slices.Equal(c.TaxIDs, []core.TaxID{tax.ID}) {
			t.Errorf("commitment = %+v, want = 100 Minimum spending taxed", c)
		}
		if got, want := h.Webhooks(), []webhook.EventType{webhook.EventTypePlanCreated}; !slices.Equal(got, want) {
			t.Errorf("webhooks = %v, want = %v", got, want)
		}
	})

	// source: spec/services/plans/create_service_spec.rb:151
	t.Run("creates no minimum commitment without one", func(t *testing.T) {
		h, e := setup(t)
		p, err := e.Plans.Create(h.Ctx, h.OrgID(), createInput())
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if c := commitment(h, p.ID); c != nil {
			t.Errorf("commitment = %+v, want = none", c)
		}
		if ts := thresholds(h, p.ID); len(ts) != 0 {
			t.Errorf("thresholds = %d, want = 0", len(ts))
		}
	})

	// source: spec/services/plans/create_service_spec.rb:330-440
	t.Run("bill monthly settings", func(t *testing.T) {
		tests := []struct {
			name     string
			interval period.Interval
			in       *bool
			want     *bool
		}{
			{"yearly persists", period.Yearly, new(true), new(true)},
			{"yearly defaults to false", period.Yearly, nil, new(false)},
			{"semiannual persists", period.Semiannual, new(true), new(true)},
			{"semiannual defaults to false", period.Semiannual, nil, new(false)},
			{"monthly ignores the flag", period.Monthly, new(true), nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				in := createInput()
				in.Interval, in.BillChargesMonthly, in.BillFixedChargesMonthly = tt.interval, tt.in, tt.in
				p, err := e.Plans.Create(h.Ctx, h.OrgID(), in)
				if err != nil {
					t.Fatalf("Create() error = %v", err)
				}
				got := reloadPlan(h, p.ID)
				for name, v := range map[string]*bool{"bill_charges_monthly": got.BillChargesMonthly, "bill_fixed_charges_monthly": got.BillFixedChargesMonthly} {
					if !equalPtr(v, tt.want) {
						t.Errorf("%s = %v, want = %v", name, deref(v), deref(tt.want))
					}
				}
			})
		}
	})

	// source: spec/services/plans/create_service_spec.rb:516
	t.Run("reuses the code of a deleted plan", func(t *testing.T) {
		h, e := setup(t)
		h.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "new_plan"; p.Status = catalog.PlanStatusDeleted })
		if _, err := e.Plans.Create(h.Ctx, h.OrgID(), createInput()); err != nil {
			t.Fatalf("Create() error = %v, want = nil", err)
		}
	})

	t.Run("refuses a code in use", func(t *testing.T) {
		h, e := setup(t)
		h.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "new_plan" })
		_, err := e.Plans.Create(h.Ctx, h.OrgID(), createInput())
		if !hasFieldError(err, "code", core.CodeAlreadyExists) {
			t.Errorf("Create() error = %v, want = code value_already_exist", err)
		}
	})

	// source: spec/services/plans/create_service_spec.rb:528
	t.Run("validation errors", func(t *testing.T) {
		tests := []struct {
			name        string
			edit        func(*plans.CreateInput)
			field, code string
		}{
			{"missing name", func(in *plans.CreateInput) { in.Name = "" }, "name", core.CodeRequired},
			// source: spec/services/plans/create_service_spec.rb:596
			{"missing interval", func(in *plans.CreateInput) { in.Interval = 0 }, "interval", core.CodeRequired},
			{"missing threshold amount", func(in *plans.CreateInput) {
				in.UsageThresholds = []plans.ThresholdInput{{}}
			}, "usage_thresholds", plans.CodeMissingAmount},
			{"duplicated thresholds", func(in *plans.CreateInput) {
				in.UsageThresholds = []plans.ThresholdInput{{Amount: new(int64(10))}, {Amount: new(int64(10))}}
			}, "usage_thresholds", plans.CodeDuplicatedValues},
			{"two recurring thresholds", func(in *plans.CreateInput) {
				in.UsageThresholds = []plans.ThresholdInput{{Amount: new(int64(10)), Recurring: true}, {Amount: new(int64(20)), Recurring: true}}
			}, "usage_thresholds", catalog.CodeMultipleRecurringThresholds},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				in := createInput()
				tt.edit(&in)
				_, err := e.Plans.Create(h.Ctx, h.OrgID(), in)
				if !hasFieldError(err, tt.field, tt.code) {
					t.Fatalf("Create() error = %v, want = %s %s", err, tt.field, tt.code)
				}
				if len(h.Webhooks()) != 0 {
					t.Errorf("webhooks = %v, want = none", h.Webhooks())
				}
			})
		}
	})

	t.Run("refuses a tax of another organization", func(t *testing.T) {
		h, e := setup(t)
		in := createInput()
		in.Taxes = []refs.Ref{refs.ByCode("unknown")}
		if _, err := e.Plans.Create(h.Ctx, h.OrgID(), in); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Create() error = %v, want = not found", err)
		}
	})

	// source: spec/services/plans/create_service_spec.rb:607-647
	t.Run("refuses chargeables of another organization", func(t *testing.T) {
		tests := []struct {
			name string
			edit func(in *plans.CreateInput, m *catalog.BillableMetric, a *catalog.AddOn)
			want string
		}{
			// source: spec/services/plans/create_service_spec.rb:610
			{"metric", func(in *plans.CreateInput, m *catalog.BillableMetric, _ *catalog.AddOn) {
				in.Charges = []plans.ChargeInput{{BillableMetric: refs.ByID(m.ID)}}
			}, "billable_metrics_not_found"},
			// source: spec/services/plans/create_service_spec.rb:642
			{"add-on", func(in *plans.CreateInput, _ *catalog.BillableMetric, a *catalog.AddOn) {
				in.FixedCharges = []plans.FixedChargeInput{{AddOn: refs.ByID(a.ID)}}
			}, "add_ons_not_found"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				other := core.NewID[core.OrganizationID](h.IDs)
				m := h.Metric("other_calls", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) { m.OrganizationID = other })
				a := h.AddOn(100, func(a *catalog.AddOn) { a.OrganizationID = other })
				in := createInput()
				tt.edit(&in, m, a)
				_, err := e.Plans.Create(h.Ctx, h.OrgID(), in)
				var nf *core.NotFoundError
				if !errors.As(err, &nf) || nf.Error() != tt.want {
					t.Fatalf("Create() error = %v, want = %s", err, tt.want)
				}
				if _, err := e.Plans.GetByCode(h.Ctx, h.OrgID(), "new_plan"); !errors.Is(err, core.ErrNotFound) {
					t.Errorf("GetByCode() error = %v, want = not found (no plan created)", err)
				}
			})
		}
	})

	// source: spec/services/plans/create_service_spec.rb:893-960
	t.Run("metadata", func(t *testing.T) {
		tests := []struct {
			name string
			md   core.Metadata
		}{
			{"with metadata", core.Metadata{{Key: "foo", Value: "bar"}}},
			{"without metadata", nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				in := createInput()
				in.Metadata = tt.md
				p, err := e.Plans.Create(h.Ctx, h.OrgID(), in)
				if err != nil {
					t.Fatalf("Create() error = %v", err)
				}
				if got := reloadPlan(h, p.ID).Metadata; !slices.Equal(got, tt.md) {
					t.Errorf("metadata = %v, want = %v", got, tt.md)
				}
			})
		}
	})
}

func TestGetAndList(t *testing.T) {
	h, e := setup(t)
	p := h.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Name = "Gold" })
	h.Plan(period.Monthly, 90, overrideOf(p))
	pending := h.Plan(period.Monthly, 100, pendingDeletion, func(p *catalog.Plan) { p.Name = "Silver" })
	h.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Status = catalog.PlanStatusDeleted })

	got, err := e.Plans.GetByCode(h.Ctx, h.OrgID(), p.Code)
	if err != nil || got.ID != p.ID {
		t.Errorf("GetByCode() = %v, %v, want = %s", got, err, p.ID)
	}
	page, err := e.Plans.List(h.Ctx, h.OrgID(), "", core.PageRequest{Size: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	ids := make([]core.PlanID, len(page.Items))
	for i, x := range page.Items {
		ids[i] = x.ID
	}
	slices.SortFunc(ids, func(a, b core.PlanID) int { return a.Compare(b) })
	want := []core.PlanID{p.ID, pending.ID}
	slices.SortFunc(want, func(a, b core.PlanID) int { return a.Compare(b) })
	if !slices.Equal(ids, want) {
		t.Errorf("List() = %v, want = %v (parents, pending deletion included)", ids, want)
	}
	page, err = e.Plans.List(h.Ctx, h.OrgID(), "silv", core.PageRequest{Size: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != pending.ID {
		t.Errorf("List(silv) = %v, %v, want = [%s]", page.Items, err, pending.ID)
	}

	c := h.Customer()
	h.Subscription(c, p)
	v, err := e.Plans.View(h.Ctx, p)
	if err != nil {
		t.Fatalf("View() error = %v", err)
	}
	if v.ActiveSubscriptions != 1 {
		t.Errorf("active subscriptions = %d, want = 1", v.ActiveSubscriptions)
	}
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
