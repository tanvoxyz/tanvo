package subscriptions

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func usageBreakdownFee(grouped map[string]string, filtered bool, bs ...string) usageFee {
	f := &invoice.Fee{GroupedBy: grouped}
	if filtered {
		f.ChargeFilterID = core.MustParseID[core.ChargeFilterID]("0190a0a0-0000-7000-8000-0000000000f1")
	}
	for i := 0; i < len(bs); i += 3 {
		v := bs[i+1]
		f.PresentationBreakdowns = append(f.PresentationBreakdowns, invoice.PresentationBreakdown{
			PresentationBy: map[string]*string{bs[i]: &v}, Units: money.MustParse(bs[i+2]),
		})
	}
	return usageFee{fee: f}
}

func renderUsageBreakdowns(bs []invoice.PresentationBreakdown) string {
	var out []string
	for _, b := range bs {
		for k, v := range b.PresentationBy {
			out = append(out, k+"="+*v+":"+b.Units.String())
		}
	}
	return strings.Join(out, ";")
}

func TestBreakdownsOf(t *testing.T) {
	t.Parallel()
	ungrouped := usageBreakdownFee(nil, false, "region", "us", "1")
	grouped := usageBreakdownFee(map[string]string{"region": "eu"}, false, "region", "eu", "2")
	filtered := usageBreakdownFee(nil, true, "region", "apac", "3")
	cases := []struct {
		name, source string
		fees         []usageFee
		sel          feeSelection
		want         string
	}{
		{
			name: "one entry per breakdown", source: "spec/serializers/v1/customers/presentation_breakdown_builder_spec.rb:24",
			fees: []usageFee{usageBreakdownFee(nil, false, "cloud", "aws", "1.2"), usageBreakdownFee(nil, false, "cloud", "aws", "0.3", "cloud", "gcp", "3")},
			sel:  ungroupedFees, want: "cloud=aws:1.2;cloud=aws:0.3;cloud=gcp:3",
		},
		{name: "no breakdowns", source: "spec/serializers/v1/customers/presentation_breakdown_builder_spec.rb:35", fees: []usageFee{usageBreakdownFee(nil, false)}, sel: ungroupedFees},
		{name: "ungrouped", source: "spec/serializers/v1/customers/presentation_breakdown_builder_spec.rb:56", fees: []usageFee{ungrouped, grouped, filtered}, sel: ungroupedFees, want: "region=us:1"},
		{name: "grouped", source: "spec/serializers/v1/customers/presentation_breakdown_builder_spec.rb:66", fees: []usageFee{ungrouped, grouped, filtered}, sel: groupedFees, want: "region=eu:2"},
		{name: "all", source: "spec/serializers/v1/customers/presentation_breakdown_builder_spec.rb:76", fees: []usageFee{ungrouped, grouped, filtered}, sel: allFees, want: "region=us:1;region=eu:2;region=apac:3"},
		{
			name: "all breakdowns whatever the displayed keys", source: "spec/serializers/v1/customers/presentation_breakdown_builder_spec.rb:115",
			fees: []usageFee{usageBreakdownFee(nil, false, "cloud", "aws", "1", "region", "us", "2")}, sel: allFees, want: "cloud=aws:1;region=us:2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := renderUsageBreakdowns(breakdownsOf(tc.fees, tc.sel)); got != tc.want {
				t.Errorf("%s: got = %q, want = %q", tc.source, got, tc.want)
			}
		})
	}
}
