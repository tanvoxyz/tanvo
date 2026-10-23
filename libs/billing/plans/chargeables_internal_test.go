package plans

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestValidateChargeables(t *testing.T) {
	type fixture struct {
		metric, otherMetric *catalog.BillableMetric
		addOn, otherAddOn   *catalog.AddOn
	}
	missing := refs.Ref{ID: [16]byte{0xff}}
	tests := []struct {
		name    string
		charges func(f fixture) []ChargeInput
		fixed   func(f fixture) []FixedChargeInput
		want    string
	}{
		// source: spec/services/plans/chargeables_validation_service_spec.rb:14
		{name: "no charges nor fixed charges"},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:27
		{name: "existing metric", charges: func(f fixture) []ChargeInput {
			return []ChargeInput{{BillableMetric: refs.ByID(f.metric.ID)}, {BillableMetric: refs.ByID(f.metric.ID)}}
		}},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:38
		{name: "missing metric", charges: func(fixture) []ChargeInput {
			return []ChargeInput{{BillableMetric: missing}}
		}, want: "billable_metrics_not_found"},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:54
		{name: "some metrics missing", charges: func(f fixture) []ChargeInput {
			return []ChargeInput{{BillableMetric: refs.ByID(f.metric.ID)}, {BillableMetric: missing}}
		}, want: "billable_metrics_not_found"},
		{name: "metric of another organization", charges: func(f fixture) []ChargeInput {
			return []ChargeInput{{BillableMetric: refs.ByID(f.otherMetric.ID)}}
		}, want: "billable_metrics_not_found"},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:70
		{name: "existing add-on by id", fixed: func(f fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: refs.ByID(f.addOn.ID)}}
		}},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:81
		{name: "missing add-on id", fixed: func(fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: missing}}
		}, want: "add_ons_not_found"},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:95
		{name: "existing add-on by code", fixed: func(f fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: refs.ByCode(f.addOn.Code)}}
		}},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:106
		{name: "missing add-on code", fixed: func(fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: refs.ByCode("unknown")}}
		}, want: "add_ons_not_found"},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:123
		{name: "add-on by id and by code", fixed: func(f fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: refs.ByID(f.addOn.ID)}, {AddOn: refs.ByCode(f.addOn.Code)}}
		}},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:135, spec/services/plans/chargeables_validation_service_spec.rb:147
		{name: "fixed charge without an add-on", fixed: func(f fixture) []FixedChargeInput {
			return []FixedChargeInput{{}, {AddOn: refs.ByCode(f.addOn.Code)}}
		}},
		{name: "add-on of another organization", fixed: func(f fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: refs.ByID(f.otherAddOn.ID)}}
		}, want: "add_ons_not_found"},
		{name: "discarded add-on", fixed: func(f fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: refs.ByCode("gone")}}
		}, want: "add_ons_not_found"},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:167
		{name: "both exist", charges: func(f fixture) []ChargeInput {
			return []ChargeInput{{BillableMetric: refs.ByID(f.metric.ID)}}
		}, fixed: func(f fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: refs.ByID(f.addOn.ID)}}
		}},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:178
		{name: "both with a missing metric", charges: func(fixture) []ChargeInput {
			return []ChargeInput{{BillableMetric: missing}}
		}, fixed: func(f fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: refs.ByID(f.addOn.ID)}}
		}, want: "billable_metrics_not_found"},
		// source: spec/services/plans/chargeables_validation_service_spec.rb:192
		{name: "both with a missing add-on", charges: func(f fixture) []ChargeInput {
			return []ChargeInput{{BillableMetric: refs.ByID(f.metric.ID)}}
		}, fixed: func(fixture) []FixedChargeInput {
			return []FixedChargeInput{{AddOn: missing}}
		}, want: "add_ons_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)
			h := billingtest.New(t, now)
			otherOrg := core.NewID[core.OrganizationID](h.IDs)
			f := fixture{
				metric:      h.Metric("api_calls", catalog.AggregationTypeCount, ""),
				otherMetric: h.Metric("other_calls", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) { m.OrganizationID = otherOrg }),
				addOn:       h.AddOn(100),
				otherAddOn:  h.AddOn(100, func(a *catalog.AddOn) { a.OrganizationID = otherOrg }),
			}
			h.AddOn(100, func(a *catalog.AddOn) { a.Code = "gone"; a.DeletedAt = new(now) })
			var charges []ChargeInput
			if tt.charges != nil {
				charges = tt.charges(f)
			}
			var fixed []FixedChargeInput
			if tt.fixed != nil {
				fixed = tt.fixed(f)
			}
			err := billingtest.Read(h, func(ctx context.Context, r port.Repos) (error, error) {
				return validateChargeables(ctx, r, h.OrgID(), charges, fixed), nil
			})
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Fatalf("validateChargeables() = %q, want = %q", got, tt.want)
			}
			if tt.want != "" && !errors.Is(err, core.ErrNotFound) {
				t.Errorf("error %v is not core.ErrNotFound", err)
			}
		})
	}
}
