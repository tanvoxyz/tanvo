package plans_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func TestOverride(t *testing.T) {
	// source: spec/services/plans/override_service_spec.rb:126
	t.Run("creates an override plan", func(t *testing.T) {
		h, e := setup(t)
		parentTax := h.Tax("parent_tax", "10")
		tax := h.Tax("override_tax", "20")
		p := h.Plan(period.Monthly, 100, func(p *catalog.Plan) {
			p.TaxIDs = []core.TaxID{parentTax.ID}
			p.Metadata = core.Metadata{{Key: "k", Value: "v"}}
			p.TrialPeriodDays = 3
		})
		h.UsageThreshold(p, 999, false)
		h.Commitment(p, 999)
		in := plans.OverrideInput{
			Amount: core.Some(int64(300)), Name: core.Some("overridden name"), Description: core.Some(new("desc")),
			InvoiceDisplayName: core.Some(new("invoice name")), TrialPeriodDays: core.Some(20),
			Taxes: []refs.Ref{refs.ByCode(tax.Code)},
		}
		var np *catalog.Plan
		h.Do(func(ctx context.Context, r port.Repos) error {
			var err error
			np, err = e.Plans.Override(ctx, r, p, in, nil)
			return err
		})
		got := reloadPlan(h, np.ID)
		if got.ParentID != p.ID || got.Code != p.Code || got.Interval != p.Interval || got.Currency.Code() != p.Currency.Code() {
			t.Errorf("override = %+v, want = a child of %s", got, p.ID)
		}
		if got.Amount != 300 || got.Name != "overridden name" || *got.Description != "desc" || *got.InvoiceDisplayName != "invoice name" || got.TrialPeriodDays != 20 {
			t.Errorf("override = %+v, want = the overridden values", got)
		}
		if !slices.Equal(got.TaxIDs, []core.TaxID{tax.ID}) {
			t.Errorf("taxes = %v, want = [%s]", got.TaxIDs, tax.ID)
		}
		if len(got.Metadata) != 0 {
			t.Errorf("metadata = %v, want = none", got.Metadata)
		}
		// The parent's thresholds and commitment are not copied.
		if ts := thresholds(h, np.ID); len(ts) != 0 {
			t.Errorf("thresholds = %d, want = 0", len(ts))
		}
		if c := commitment(h, np.ID); c != nil {
			t.Errorf("commitment = %+v, want = none", c)
		}
		if len(h.Webhooks()) != 0 {
			t.Errorf("webhooks = %v, want = none", h.Webhooks())
		}
	})

	// source: spec/services/plans/override_service_spec.rb:262, spec/services/usage_thresholds/override_service_spec.rb:27
	t.Run("creates the usage thresholds", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 100)
		in := plans.OverrideInput{UsageThresholds: []plans.ThresholdInput{
			{Amount: new(int64(1000)), ThresholdDisplayName: new("Threshold")},
			{Amount: new(int64(10)), Recurring: true},
		}}
		var np *catalog.Plan
		h.Do(func(ctx context.Context, r port.Repos) error {
			var err error
			np, err = e.Plans.Override(ctx, r, p, in, nil)
			return err
		})
		ts := thresholds(h, np.ID)
		if len(ts) != 2 || ts[0].Amount != 1000 || *ts[0].ThresholdDisplayName != "Threshold" || !ts[1].Recurring {
			t.Errorf("thresholds = %v, want = 1000 then the recurring one", ts)
		}
	})

	// source: spec/services/usage_thresholds/override_service_spec.rb:41
	t.Run("refuses duplicated thresholds", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 100)
		in := plans.OverrideInput{UsageThresholds: []plans.ThresholdInput{{Amount: new(int64(10))}, {Amount: new(int64(10))}}}
		err := h.Deps.Tx.InTx(h.Ctx, func(ctx context.Context, r port.Repos) error {
			_, err := e.Plans.Override(ctx, r, p, in, nil)
			return err
		})
		// source: spec/services/usage_thresholds/override_service_spec.rb:60
		if !hasFieldError(err, "usage_thresholds.amount", core.CodeAlreadyExists) {
			t.Errorf("Override() error = %v, want = usage_thresholds.amount value_already_exist", err)
		}
	})

	// source: spec/services/plans/override_service_spec.rb:278, spec/services/commitments/override_service_spec.rb:32
	t.Run("creates the minimum commitment", func(t *testing.T) {
		h, e := setup(t)
		tax := h.Tax("tax", "20")
		p := h.Plan(period.Monthly, 100)
		in := plans.OverrideInput{MinimumCommitment: &plans.CommitmentInput{
			Amount: core.Some(int64(1000)), InvoiceDisplayName: core.Some(new("Commitment")),
			Taxes: core.Some([]refs.Ref{refs.ByCode(tax.Code)}),
		}}
		var np *catalog.Plan
		h.Do(func(ctx context.Context, r port.Repos) error {
			var err error
			np, err = e.Plans.Override(ctx, r, p, in, nil)
			return err
		})
		c := commitment(h, np.ID)
		if c == nil || c.Amount != 1000 || *c.InvoiceDisplayName != "Commitment" || !slices.Equal(c.TaxIDs, []core.TaxID{tax.ID}) {
			t.Errorf("commitment = %+v, want = 1000 Commitment taxed", c)
		}
	})
}
