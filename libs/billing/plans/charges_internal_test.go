package plans

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func TestChargeGenerateCode(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		other    string
		child    string
		want     string
	}{
		// source: spec/services/charges/generate_code_service_spec.rb:13
		{name: "no charges", want: "api_calls"},
		// source: spec/services/charges/generate_code_service_spec.rb:19
		{name: "base code taken", existing: []string{"api_calls"}, want: "api_calls_2"},
		// source: spec/services/charges/generate_code_service_spec.rb:29
		{name: "numeric suffixes", existing: []string{"api_calls", "api_calls_2"}, want: "api_calls_3"},
		// source: spec/services/charges/generate_code_service_spec.rb:40
		{name: "gaps in suffixes", existing: []string{"api_calls", "api_calls_3", "api_calls_5"}, want: "api_calls_6"},
		// source: spec/services/charges/generate_code_service_spec.rb:52
		{name: "similar prefix", other: "api_calls_premium", want: "api_calls"},
		// source: spec/services/charges/generate_code_service_spec.rb:63
		{name: "non-numeric suffix", existing: []string{"api_calls_custom"}, want: "api_calls"},
		// source: spec/services/charges/generate_code_service_spec.rb:73
		{name: "child charge with the base code", child: "api_calls", want: "api_calls"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := billingtest.New(t, time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC))
			p := h.Plan(period.Monthly, 0)
			m := h.Metric("api_calls", catalog.AggregationTypeCount, "")
			for _, code := range tt.existing {
				h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.Code = code })
			}
			if tt.other != "" {
				om := h.Metric(tt.other, catalog.AggregationTypeCount, "")
				h.Charge(p, om, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.Code = tt.other })
			}
			if tt.child != "" {
				parent := h.Charge(h.Plan(period.Monthly, 0), m, catalog.ChargeModelStandard, `{"amount":"1"}`)
				h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.Code, c.ParentID = tt.child, parent.ID })
			}
			got := billingtest.Read(h, func(ctx context.Context, r port.Repos) (string, error) {
				return chargeGenerateCode(ctx, r, p, m)
			})
			if got != tt.want {
				t.Errorf("code = %q, want = %q", got, tt.want)
			}
		})
	}
}
