package fees_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// breakdownsString renders presentation breakdowns sorted, as
// "k=v,k=v:units;…" with units rounded to 5 places; a nil value is "<nil>".
func breakdownsString(bs []invoice.PresentationBreakdown) string {
	var out []string
	for _, b := range bs {
		var kv []string
		for _, k := range slices.Sorted(func(yield func(string) bool) {
			for k := range b.PresentationBy {
				if !yield(k) {
					return
				}
			}
		}) {
			v := "<nil>"
			if p := b.PresentationBy[k]; p != nil {
				v = *p
			}
			kv = append(kv, k+"="+v)
		}
		out = append(out, strings.Join(kv, ",")+":"+b.Units.Round(5, money.HalfUp).Reduce().String())
	}
	slices.Sort(out)
	return strings.Join(out, ";")
}

// groupedValuesString renders cached breakdowns like breakdownsString.
func groupedValuesString(vs []metering.GroupedValue) string {
	bs := make([]invoice.PresentationBreakdown, len(vs))
	for i, v := range vs {
		bs[i] = invoice.PresentationBreakdown{PresentationBy: v.Groups, Units: v.Value}
	}
	return breakdownsString(bs)
}

func checkBreakdowns(t *testing.T, name string, got []invoice.PresentationBreakdown, want string) {
	t.Helper()
	if s := breakdownsString(got); s != want {
		t.Errorf("%s breakdowns: got = %q, want = %q", name, s, want)
	}
}

// presentationFilters is the setup of the presentation cases with charge
// filters: europe and usa filters, events in europe, the usa and apac.
func presentationFilters(t *testing.T) (*chargeFixture, *catalog.Charge) {
	t.Helper()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeSum, "value", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{
			{ID: core.NewID[core.BillableMetricFilterID](f.IDs), Key: "region", Values: []string{"europe", "usa", "apac"}},
		}
	})
	props := `{"amount":"0","presentation_group_keys":[{"value":"region"}]}`
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, props, func(c *catalog.Charge) {
		c.Filters = []catalog.ChargeFilter{
			billingtest.ChargeFilter(map[string][]string{"region": {"europe"}}, props),
			billingtest.ChargeFilter(map[string][]string{"region": {"usa"}}, props),
		}
	})
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "region", "europe", "value", "10")
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "region", "usa", "value", "5")
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "region", "apac", "value", "3")
	return f, c
}

func TestChargeFeesPresentationBreakdownsFilters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts fees.Options
	}{
		// source: spec/services/fees/charge_service_spec.rb:315
		{"invoice", fees.Options{}},
		// source: spec/services/fees/charge_service_spec.rb:2950
		{"current usage", fees.Options{CurrentUsage: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, c := presentationFilters(t)
			var res *fees.ChargeResult
			if tc.opts.CurrentUsage {
				res = f.run(f.usageScope(f.sub, date(2022, time.March, 20)), c, tc.opts)
			} else {
				_, res = f.fees(c, tc.opts)
			}
			feeCount(t, res, 3)
			checkBreakdowns(t, "europe", byFilter(t, res, c.Filters[0].ID).PresentationBreakdowns, "region=europe:10")
			checkBreakdowns(t, "usa", byFilter(t, res, c.Filters[1].ID).PresentationBreakdowns, "region=usa:5")
			checkBreakdowns(t, "default", byFilter(t, res, core.ChargeFilterID{}).PresentationBreakdowns, "region=apac:3")
		})
	}
}

// groupedPresentation is the setup of the presentation cases with pricing
// groups: aws events in two regions, a gcp event in one.
func groupedPresentation(t *testing.T, agg catalog.AggregationType, recurring bool) (*chargeFixture, *catalog.Charge) {
	t.Helper()
	f := newChargeFixture(t)
	m := f.Metric("metric", agg, "value", func(m *catalog.BillableMetric) {
		if agg == catalog.AggregationTypeWeightedSum {
			m.WeightedInterval, m.Recurring = catalog.WeightedIntervalSeconds, recurring
		}
	})
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20","pricing_group_keys":["cloud"],"presentation_group_keys":[{"value":"region"}]}`)
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "cloud", "aws", "region", "us-east-1", "value", "10")
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "cloud", "aws", "region", "us-central-1", "value", "5")
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "cloud", "gcp", "region", "eu-west-1", "value", "3")
	return f, c
}

// source: spec/services/fees/charge_service_spec.rb:902
func TestChargeFeesPresentationBreakdownsGrouped(t *testing.T) {
	t.Parallel()
	f, c := groupedPresentation(t, catalog.AggregationTypeSum, false)
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 2)
	checkBreakdowns(t, "aws", byGroup(t, res, "cloud", "aws").PresentationBreakdowns, "region=us-central-1:5;region=us-east-1:10")
	checkBreakdowns(t, "gcp", byGroup(t, res, "cloud", "gcp").PresentationBreakdowns, "region=eu-west-1:3")
}

func TestChargeFeesPresentationBreakdownsCached(t *testing.T) {
	t.Parallel()
	// source: spec/services/fees/charge_service_spec.rb:922
	t.Run("grouped", func(t *testing.T) {
		t.Parallel()
		f, c := groupedPresentation(t, catalog.AggregationTypeWeightedSum, true)
		_, res := f.fees(c, fees.Options{})
		if len(res.Aggregations) != 2 {
			t.Fatalf("got %d cached aggregations, want = 2", len(res.Aggregations))
		}
		want := map[string]string{"aws": "region=us-central-1:2.58065;region=us-east-1:5.16129", "gcp": "region=eu-west-1:1.54839"}
		for _, a := range res.Aggregations {
			cloud, _ := a.Aggregation.Groups.Value("cloud")
			if got := groupedValuesString(a.Aggregation.PresentationBreakdowns); got != want[cloud] {
				t.Errorf("%s cached breakdowns: got = %q, want = %q", cloud, got, want[cloud])
			}
		}
	})
	// source: spec/services/fees/charge_service_spec.rb:2829
	t.Run("ungrouped", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		m := f.Metric("metric", catalog.AggregationTypeWeightedSum, "value", func(m *catalog.BillableMetric) {
			m.WeightedInterval, m.Recurring = catalog.WeightedIntervalSeconds, true
		})
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20","presentation_group_keys":[{"value":"region"}]}`)
		f.Event(f.sub, m.Code, date(2022, time.March, 16), "region", "us-east-1", "value", "10")
		f.Event(f.sub, m.Code, date(2022, time.March, 16), "region", "eu-west-1", "value", "5")
		_, res := f.fees(c, fees.Options{Recurring: true})
		if len(res.Aggregations) != 1 {
			t.Fatalf("got %d cached aggregations, want = 1", len(res.Aggregations))
		}
		want := "region=eu-west-1:2.58065;region=us-east-1:5.16129"
		if got := groupedValuesString(res.Aggregations[0].Aggregation.PresentationBreakdowns); got != want {
			t.Errorf("cached breakdowns: got = %q, want = %q", got, want)
		}
	})
}

func TestChargeFeesPresentationBreakdownsAdjusted(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, a *invoice.AdjustedFee) *fees.ChargeResult {
		f := newChargeFixture(t)
		m := f.Metric("metric", catalog.AggregationTypeSum, "value")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20","pricing_group_keys":["cloud"],"presentation_group_keys":[{"value":"region"}]}`)
		f.Event(f.sub, m.Code, date(2022, time.March, 16), "cloud", "aws", "value", "10", "region", "eu")
		f.Event(f.sub, m.Code, date(2022, time.March, 16), "cloud", "aws", "value", "5", "region", "us")
		sc := f.scope(f.sub, f.b, date(2022, time.April, 1), draft)
		a.InvoiceID, a.SubscriptionID, a.ChargeID, a.FeeType = sc.Invoice.ID, f.sub.ID, c.ID, core.FeeTypeCharge
		a.GroupedBy = map[string]string{"cloud": "aws"}
		a.ChargesFrom, a.ChargesTo = sc.Boundaries.Charges.From, sc.Boundaries.Charges.To
		f.adjust(a)
		return f.run(sc, c, fees.Options{})
	}
	for _, tc := range []struct {
		name  string
		adj   invoice.AdjustedFee
		units string
		want  string
	}{
		// source: spec/services/fees/charge_service_spec.rb:4674
		{"units changed", invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("3")}, "3", ""},
		// source: spec/services/fees/charge_service_spec.rb:4705
		{"units kept", invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnitsAndUnitPrice, Units: dec("15"), UnitPreciseAmount: dec("1"), InvoiceDisplayName: ptr("renamed")}, "15", "region=eu:10;region=us:5"},
		// source: spec/services/fees/charge_service_spec.rb:4736
		{"display name only", invoice.AdjustedFee{Mode: invoice.AdjustmentModeDisplayName, InvoiceDisplayName: ptr("renamed")}, "15", "region=eu:10;region=us:5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := setup(t, &tc.adj)
			aws := byGroup(t, res, "cloud", "aws")
			if !eqDec(aws.Units, dec(tc.units)) {
				t.Errorf("got units = %s, want = %s", aws.Units, tc.units)
			}
			checkBreakdowns(t, "aws", aws.PresentationBreakdowns, tc.want)
			if tc.adj.Mode == invoice.AdjustmentModeDisplayName && (aws.InvoiceDisplayName == nil || *aws.InvoiceDisplayName != "renamed") {
				t.Errorf("got invoice display name = %v, want = renamed", aws.InvoiceDisplayName)
			}
		})
	}
}

// The true-up fee of a minimum amount carries no breakdown, as Fee#dup
// copies no association.
// source: app/services/fees/create_true_up_service.rb:28
func TestChargeFeesPresentationBreakdownsTrueUp(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeSum, "value")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1","presentation_group_keys":[{"value":"region"}]}`, func(c *catalog.Charge) { c.MinAmount = 100000 })
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "region", "eu", "value", "2")
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 2)
	checkBreakdowns(t, "fee", res.Fees[0].PresentationBreakdowns, "region=eu:2")
	if res.Fees[1].TrueUpParentFeeID.IsZero() || len(res.Fees[1].PresentationBreakdowns) != 0 {
		t.Errorf("got true-up fee = %+v, want one without breakdowns", res.Fees[1])
	}
}

func TestInAdvanceChargeFeesPresentationBreakdowns(t *testing.T) {
	t.Parallel()
	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:770
	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:778
	t.Run("stripped of pricing group keys", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		m := f.Metric("metric", catalog.AggregationTypeSum, "value")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"10","pricing_group_keys":["cloud"],"presentation_group_keys":[{"value":"region"}]}`, advance)
		res := f.inAdvance(c, f.Event(f.sub, m.Code, date(2022, time.March, 20), "cloud", "aws", "region", "us-east-1", "value", "9"), false)
		feeCount(t, res, 1)
		checkBreakdowns(t, "fee", res.Fees[0].PresentationBreakdowns, "region=us-east-1:9")
		if len(res.Aggregations) != 1 {
			t.Fatalf("got %d cached aggregations, want = 1", len(res.Aggregations))
		}
		if got := groupedValuesString(res.Aggregations[0].Aggregation.PresentationBreakdowns); got != "region=us-east-1:9" {
			t.Errorf("cached breakdowns: got = %q, want = region=us-east-1:9", got)
		}
	})
	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:735
	t.Run("without presentation keys", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		m := f.Metric("metric", catalog.AggregationTypeSum, "value")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"10"}`, advance)
		res := f.inAdvance(c, f.Event(f.sub, m.Code, date(2022, time.March, 20), "region", "eu", "value", "9"), false)
		if len(res.Fees[0].PresentationBreakdowns) != 0 || len(res.Aggregations) != 1 || len(res.Aggregations[0].Aggregation.PresentationBreakdowns) != 0 {
			t.Errorf("got breakdowns on the fee or its cached aggregation, want none")
		}
	})
	// The fee breaks down the event; the cached aggregation the usage up
	// to it.
	t.Run("event after event", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		m := f.Metric("metric", catalog.AggregationTypeSum, "units")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1","presentation_group_keys":[{"value":"department"},{"value":"units"}]}`, advance)
		var cached []string
		for i, tc := range []struct {
			units string
		}{
			// source: spec/services/fees/create_pay_in_advance_service_spec.rb:898
			{"3"},
			// source: spec/services/fees/create_pay_in_advance_service_spec.rb:933
			{"10"},
			// source: spec/services/fees/create_pay_in_advance_service_spec.rb:983
			{"7"},
		} {
			e := f.Event(f.sub, m.Code, date(2022, time.March, 20, 1, 0, i), "units", tc.units, "department", "engineering")
			res := f.inAdvance(c, e, false)
			feeCount(t, res, 1)
			want := fmt.Sprintf("department=engineering,units=%s:%s", tc.units, tc.units)
			checkBreakdowns(t, "fee", res.Fees[0].PresentationBreakdowns, want)
			cached = append(cached, want)
			slices.Sort(cached)
			if len(res.Aggregations) != 1 {
				t.Fatalf("got %d cached aggregations, want = 1", len(res.Aggregations))
			}
			if got, want := groupedValuesString(res.Aggregations[0].Aggregation.PresentationBreakdowns), strings.Join(cached, ";"); got != want {
				t.Errorf("event %d cached breakdowns: got = %q, want = %q", i, got, want)
			}
		}
	})
}
