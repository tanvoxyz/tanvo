package billing_test

// Ported from lago spec/scenarios: current_usage/**, usage_monitoring,
// lifetime_usages. Each lago scenario that runs over both the postgres and
// clickhouse event stores is ported once: the memory event store stands in
// for both (the store conformance suites cover the two engines).

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// usgStd creates a scenario at (2024-03-05), a customer, a monthly plan of
// 0 with a standard charge of amount on m, and a subscription; the clock is
// moved to 2024-03-06.
func usgStd(t *testing.T, agg catalog.AggregationType, field, amount string, mopts ...func(*catalog.BillableMetric)) (*scenario, *subscription.Subscription, *catalog.BillableMetric) {
	t.Helper()
	s := newScenario(t, usgDT(2024, 3, 5))
	c := s.Customer()
	m := s.Metric("bm", agg, field, mopts...)
	p := s.usgMonthly(0)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"`+amount+`"}`)
	sub := s.usgSub(c, p)
	s.travelTo(usgDT(2024, 3, 6))
	return s, sub, m
}

func TestUsageScenarioCountAgg(t *testing.T) {
	// source: spec/scenarios/current_usage/by_aggregation_type/count_agg_spec.rb:21
	t.Run("standard charge", func(t *testing.T) {
		s, sub, m := usgStd(t, catalog.AggregationTypeCount, "", "5")
		for range 3 {
			s.usgEvent(sub, m.Code)
		}
		u := s.usgCurrent(sub)
		usgEq(t, "total_amount_cents", u.TotalAmount, 1500)
		usgEq(t, "charges_usage.count", len(u.charges), 1)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "3")
		usgEq(t, "events_count", cu.events, 3)
		usgEq(t, "amount_cents", cu.amount, 1500)
	})
	// source: spec/scenarios/current_usage/by_aggregation_type/count_agg_spec.rb:62
	t.Run("incremental events", func(t *testing.T) {
		s, sub, m := usgStd(t, catalog.AggregationTypeCount, "", "10")
		s.usgEvent(sub, m.Code)
		cu := s.usgCurrent(sub).first(t)
		usgUnits(t, "charge", cu.units, "1")
		usgEq(t, "amount_cents", cu.amount, 1000)
		s.usgEvent(sub, m.Code)
		s.usgEvent(sub, m.Code)
		cu = s.usgCurrent(sub).first(t)
		usgUnits(t, "charge", cu.units, "3")
		usgEq(t, "amount_cents", cu.amount, 3000)
	})
}

func TestUsageScenarioMaxAgg(t *testing.T) {
	// source: spec/scenarios/current_usage/by_aggregation_type/max_agg_spec.rb:21
	t.Run("max value", func(t *testing.T) {
		s, sub, m := usgStd(t, catalog.AggregationTypeMax, "item_id", "10")
		steps := []struct {
			value, units string
			amount       int64
		}{{"5", "5", 5000}, {"20", "20", 20_000}, {"3", "20", 20_000}}
		var cu usgCharge
		for _, st := range steps {
			s.usgEvent(sub, m.Code, "item_id", st.value)
			cu = s.usgCurrent(sub).first(t)
			usgUnits(t, "charge", cu.units, st.units)
			usgEq(t, "amount_cents", cu.amount, st.amount)
		}
		usgEq(t, "events_count", cu.events, 3)
	})
	// source: spec/scenarios/current_usage/by_aggregation_type/max_agg_spec.rb:82
	t.Run("zero usage", func(t *testing.T) {
		s, sub, _ := usgStd(t, catalog.AggregationTypeMax, "item_id", "10")
		cu := s.usgCurrent(sub).first(t)
		usgUnits(t, "charge", cu.units, "0")
		usgEq(t, "events_count", cu.events, 0)
		usgEq(t, "amount_cents", cu.amount, 0)
	})
}

func TestUsageScenarioLatestAgg(t *testing.T) {
	newLatest := func(t *testing.T) (*scenario, *subscription.Subscription, *catalog.BillableMetric) {
		s := newScenario(t, usgDT(2024, 3, 1))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeLatest, "item_id")
		p := s.usgMonthly(0)
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"5"}`)
		return s, s.usgSub(c, p), m
	}
	// source: spec/scenarios/current_usage/by_aggregation_type/latest_agg_spec.rb:21
	t.Run("latest value", func(t *testing.T) {
		s, sub, m := newLatest(t)
		steps := []struct {
			hour         int
			value, units string
			amount       int64
		}{{1, "10", "10", 5000}, {2, "3", "3", 1500}, {3, "50", "50", 25_000}}
		var cu usgCharge
		for _, st := range steps {
			s.travelTo(usgDT(2024, 3, 5, st.hour))
			s.usgEvent(sub, m.Code, "item_id", st.value)
			cu = s.usgCurrent(sub).first(t)
			usgUnits(t, "charge", cu.units, st.units)
			usgEq(t, "amount_cents", cu.amount, st.amount)
		}
		usgEq(t, "events_count", cu.events, 3)
	})
	// source: spec/scenarios/current_usage/by_aggregation_type/latest_agg_spec.rb:86
	t.Run("zero usage", func(t *testing.T) {
		s, sub, _ := newLatest(t)
		s.travelTo(usgDT(2024, 3, 5))
		cu := s.usgCurrent(sub).first(t)
		usgUnits(t, "charge", cu.units, "0")
		usgEq(t, "events_count", cu.events, 0)
		usgEq(t, "amount_cents", cu.amount, 0)
	})
}

// source: spec/scenarios/current_usage/by_aggregation_type/weighted_sum_agg_spec.rb:20
func TestUsageScenarioWeightedSumAgg(t *testing.T) {
	s := newScenario(t, usgDT(2023, 3, 5))
	c := s.Customer()
	m := s.Metric("bm", catalog.AggregationTypeWeightedSum, "value", usgRecurring, func(m *catalog.BillableMetric) {
		m.WeightedInterval = catalog.WeightedIntervalSeconds
	})
	p := s.usgMonthly(0)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := s.usgSub(c, p)

	s.usgEvent(sub, m.Code, "value", "2500")
	u := s.usgCurrent(sub)
	usgEq(t, "total_amount_cents", u.TotalAmount, 217_742)
	usgUnits(t, "charge", u.first(t).units.Round(16, 0), "2177.4193548387096774")

	s.travelTo(usgDT(2023, 4, 1))
	s.performBilling()
	invs := s.usgSubscriptionInvoices(c, sub)
	usgEq(t, "invoices", len(invs), 1)
	fs := s.usgChargeFees(invs[0].ID)
	if len(fs) != 1 {
		t.Fatalf("charge fees = %d, want = 1", len(fs))
	}
	usgEq(t, "fee amount_cents", fs[0].Amount, 217_742)
	usgUnits(t, "fee", fs[0].Units.Round(0, 0), "2177")
	usgUnits(t, "fee total_aggregated", fs[0].TotalAggregatedUnits, "2500")

	// The second period starts from the cached aggregation of the first.
	s.travelTo(usgDT(2023, 4, 4))
	s.usgEvent(sub, m.Code, "value", "-2000")
	s.travelTo(usgDT(2023, 4, 6))
	s.usgEvent(sub, m.Code, "value", "-200")
	u = s.usgCurrent(sub)
	usgEq(t, "total_amount_cents", u.TotalAmount, 53_333)
	usgUnits(t, "charge", u.first(t).units.Round(16, 0), "533.3333333333333333")

	s.travelTo(usgDT(2023, 5, 1))
	s.performBilling()
	invs = s.usgSubscriptionInvoices(c, sub)
	usgEq(t, "invoices", len(invs), 2)
	fs = s.usgChargeFees(invs[1].ID)
	if len(fs) != 1 {
		t.Fatalf("charge fees = %d, want = 1", len(fs))
	}
	usgEq(t, "fee amount_cents", fs[0].Amount, 53_333)
	usgUnits(t, "fee", fs[0].Units.Round(5, 0), "533.33333")
	usgUnits(t, "fee total_aggregated", fs[0].TotalAggregatedUnits, "300")
}

func TestUsageScenarioSumAgg(t *testing.T) {
	groups := []string{"key_1", "2024", "key_2", "Feb", "key_3", "06"}
	props := func(v string) []string { return append([]string{"item_id", v}, groups...) }
	charge := `{"amount":"29","pricing_group_keys":["key_1","key_2","key_3"]}`
	// source: spec/scenarios/current_usage/by_aggregation_type/sum_agg_spec.rb:34
	t.Run("in advance charge and groups", func(t *testing.T) {
		s := newScenario(t, usgDT(2024, 2, 1))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeSum, "item_id", usgRecurring)
		p := s.usgMonthly(0)
		s.Charge(p, m, catalog.ChargeModelStandard, charge, usgInAdvance, usgProrated)
		sub := s.usgSub(c, p)
		for i, st := range []struct{ value, units string }{{"10", "10"}, {"-5", "5"}, {"2", "7"}} {
			s.travelTo(usgDT(2024, 2, 6, i+1))
			s.usgEvent(sub, m.Code, props(st.value)...)
			usgEq(t, "Fee.count", len(s.usgFees(sub)), i+1)
			u := s.usgCurrent(sub)
			usgEq(t, "total_amount_cents", u.TotalAmount, 24_000)
			usgUnits(t, "charge", u.first(t).units, st.units)
		}
	})
	// source: spec/scenarios/current_usage/by_aggregation_type/sum_agg_spec.rb:127
	t.Run("multiple subscriptions of one plan", func(t *testing.T) {
		s := newScenario(t, usgDT(2024, 2, 1))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeSum, "item_id", usgRecurring)
		p := s.usgMonthly(0)
		s.Charge(p, m, catalog.ChargeModelStandard, charge, usgInAdvance, usgProrated)
		sub1 := s.usgSub(c, p)
		s.travelTo(usgDT(2024, 2, 1, 1))
		sub2 := s.usgSub(c, p)
		for j, sub := range []*subscription.Subscription{sub1, sub2} {
			for i, st := range []struct{ value, units string }{{"10", "10"}, {"-5", "5"}, {"2", "7"}} {
				s.travelTo(usgDT(2024, 2, 6, j, i+1))
				s.usgEvent(sub, m.Code, props(st.value)...)
				if j == 0 {
					usgEq(t, "Fee.count", len(s.usgFees(sub1, sub2)), i+1)
				}
				u := s.usgCurrent(sub)
				usgEq(t, "total_amount_cents", u.TotalAmount, 24_000)
				usgUnits(t, "charge", u.first(t).units, st.units)
			}
		}
	})
}

func TestUsageScenarioUniqueCountAgg(t *testing.T) {
	newUnique := func(t *testing.T, at string, charge string, opts ...func(*catalog.Charge)) (*scenario, *subscription.Subscription, *catalog.BillableMetric) {
		s := newScenario(t, usgParse(t, at))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeUniqueCount, "item_id", usgRecurring)
		p := s.usgMonthly(0)
		s.Charge(p, m, catalog.ChargeModelStandard, charge, opts...)
		return s, s.usgSub(c, p), m
	}
	// source: spec/scenarios/current_usage/by_aggregation_type/unique_count_agg_spec.rb:22
	t.Run("groups keep units", func(t *testing.T) {
		s, sub, m := newUnique(t, "2024-02-06T00:00:00", `{"amount":"1","pricing_group_keys":["key_1","key_2","key_3"]}`)
		s.travelTo(usgDT(2024, 2, 7))
		s.usgEventAt(sub, m.Code, usgDT(2024, 2, 7, 0, 0, 0), "item_id", "001", "key_1", "2024", "key_2", "Feb", "key_3", "08")
		s.usgEventAt(sub, m.Code, usgDT(2024, 2, 7, 0, 0, 2), "item_id", "001", "key_1", "2024", "key_2", "Feb", "key_3", "06")
		s.usgEventAt(sub, m.Code, usgDT(2024, 2, 7, 0, 0, 3), "item_id", "002", "key_1", "2024", "key_2", "Feb", "key_3", "06")
		usgEq(t, "total_amount_cents", s.usgCurrent(sub).TotalAmount, 300)
	})
	// source: spec/scenarios/current_usage/by_aggregation_type/unique_count_agg_spec.rb:92
	t.Run("in advance charge and group by", func(t *testing.T) {
		s, sub, m := newUnique(t, "2024-02-01T00:00:00", `{"amount":"29","pricing_group_keys":["key_1","key_2","key_3"]}`, usgInAdvance, usgProrated)
		g := []string{"key_1", "2024", "key_2", "Feb", "key_3", "06"}
		steps := []struct {
			now, ts, op string
			total       int64
		}{
			{"2024-02-01T00:00:00", "2024-02-01T01:00:00", "remove", 0},
			{"2024-02-01T00:00:00", "2024-02-01T01:00:10", "add", 2900},
			{"2024-02-02T00:00:00", "2024-02-02T01:00:00", "add", 2900},
			{"2024-02-03T00:00:00", "2024-02-03T01:00:00", "remove", 2900},
		}
		for i, st := range steps {
			s.travelTo(usgParse(t, st.now))
			s.usgEventAt(sub, m.Code, usgParse(t, st.ts), append([]string{"item_id", "001", "operation_type", st.op}, g...)...)
			usgEq(t, "Fee.count", len(s.usgFees(sub)), i+1)
			usgEq(t, "total_amount_cents", s.usgCurrent(sub).TotalAmount, st.total)
		}
	})
	// source: spec/scenarios/current_usage/by_aggregation_type/unique_count_agg_spec.rb:205
	t.Run("prorated in advance charge", func(t *testing.T) {
		s, sub, m := newUnique(t, "2024-02-23T01:00:00", `{"amount":"1"}`, usgInAdvance, usgProrated)
		for i, st := range []struct {
			ts, op, units string
		}{
			{"2024-02-23T01:02:00", "", "1"},
			{"2024-02-29T01:01:00", "remove", "0"},
			{"2024-02-29T01:02:00", "remove", "0"},
		} {
			s.travelTo(usgParse(t, st.ts))
			p := []string{"item_id", "seat_1"}
			if st.op != "" {
				p = append(p, "operation_type", st.op)
			}
			s.usgEventAt(sub, m.Code, usgParse(t, st.ts), p...)
			usgEq(t, "Fee.count", len(s.usgFees(sub)), i+1)
			u := s.usgCurrent(sub)
			usgEq(t, "total_amount_cents", u.TotalAmount, 24)
			usgUnits(t, "charge", u.first(t).units, st.units)
			usgEq(t, "amount_cents", u.first(t).amount, 24)
		}
	})
	// source: spec/scenarios/current_usage/by_aggregation_type/unique_count_agg_spec.rb:288
	t.Run("in advance charge", func(t *testing.T) {
		s := newScenario(t, usgParse(t, "2024-02-23T01:00:00"))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeUniqueCount, "item_id", usgRecurring)
		p := s.usgMonthly(0)
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, usgInAdvance)
		sub := s.usgSub(c, p, billingtest.SubscribedAt(usgParse(t, "2024-02-01T01:00:00")))
		for i, st := range []struct {
			now, ts, item, op, units string
		}{
			{"2024-02-23T01:02:00", "2024-02-23T01:02:00", "seat_1", "", "1"},
			{"2024-02-26T01:01:00", "2024-02-26T01:01:00", "seat_1", "remove", "0"},
			{"2024-02-26T01:02:00", "2024-02-26T01:01:00", "seat_2", "", "1"},
		} {
			s.travelTo(usgParse(t, st.now))
			p := []string{"item_id", st.item}
			if st.op != "" {
				p = append(p, "operation_type", st.op)
			}
			s.usgEventAt(sub, m.Code, usgParse(t, st.ts), p...)
			usgEq(t, "Fee.count", len(s.usgFees(sub)), i+1)
			u := s.usgCurrent(sub)
			usgEq(t, "total_amount_cents", u.TotalAmount, 100)
			usgUnits(t, "charge", u.first(t).units, st.units)
			usgEq(t, "amount_cents", u.first(t).amount, 100)
		}
	})
}

// usgModel creates the usgStd scenario with a charge of model and
// properties on a count metric.
func usgModel(t *testing.T, agg catalog.AggregationType, field string, model catalog.ChargeModel, props string, opts ...func(*catalog.Charge)) (*scenario, *subscription.Subscription, *catalog.BillableMetric, *catalog.Charge) {
	t.Helper()
	s := newScenario(t, usgDT(2024, 3, 5))
	c := s.Customer()
	m := s.Metric("bm", agg, field)
	p := s.usgMonthly(0)
	ch := s.Charge(p, m, model, props, opts...)
	sub := s.usgSub(c, p)
	s.travelTo(usgDT(2024, 3, 6))
	return s, sub, m, ch
}

func TestUsageScenarioStandard(t *testing.T) {
	// source: spec/scenarios/current_usage/by_charge_model/standard_spec.rb:16
	t.Run("basic usage", func(t *testing.T) {
		s, sub, m, _ := usgModel(t, catalog.AggregationTypeCount, "", catalog.ChargeModelStandard, `{"amount":"12.5"}`)
		for range 4 {
			s.usgEvent(sub, m.Code)
		}
		cu := s.usgCurrent(sub).first(t)
		usgUnits(t, "charge", cu.units, "4")
		usgEq(t, "events_count", cu.events, 4)
		usgEq(t, "amount_cents", cu.amount, 5000)
	})
	// source: spec/scenarios/current_usage/by_charge_model/standard_spec.rb:53
	t.Run("grouped by", func(t *testing.T) {
		s, sub, m, _ := usgModel(t, catalog.AggregationTypeCount, "", catalog.ChargeModelStandard, `{"amount":"10","pricing_group_keys":["region"]}`)
		s.usgEvent(sub, m.Code, "region", "us")
		s.usgEvent(sub, m.Code, "region", "us")
		s.usgEvent(sub, m.Code, "region", "eu")
		cu := s.usgCurrent(sub).first(t)
		usgUnits(t, "charge", cu.units, "3")
		usgEq(t, "amount_cents", cu.amount, 3000)
		usgEq(t, "grouped_usage.count", len(cu.fees), 2)
		for _, g := range []struct {
			region, units string
			amount        int64
		}{{"us", "2", 2000}, {"eu", "1", 1000}} {
			gu, ok := cu.group("region", g.region)
			if !ok {
				t.Fatalf("no group %s", g.region)
			}
			usgUnits(t, g.region, gu.units, g.units)
			usgEq(t, g.region+" amount_cents", gu.amount, g.amount)
		}
	})
	// source: spec/scenarios/current_usage/by_charge_model/standard_spec.rb:116
	t.Run("filters", func(t *testing.T) {
		s := newScenario(t, usgDT(2024, 3, 5))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeSum, "hours", usgMetricFilter("cloud", "aws", "gcp"))
		p := s.usgMonthly(0)
		ch := s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"0.12"}`, usgFilters(
			billingtest.ChargeFilter(map[string][]string{"cloud": {"gcp"}}, `{"amount":"0.10"}`),
			billingtest.ChargeFilter(map[string][]string{"cloud": {"aws"}}, `{"amount":"0.08"}`),
		))
		sub := s.usgSub(c, p)
		s.travelTo(usgDT(2024, 3, 6))
		s.usgEvent(sub, m.Code, "cloud", "aws", "hours", "100")
		s.usgEvent(sub, m.Code, "cloud", "gcp", "hours", "200")
		cu := s.usgCurrent(sub).first(t)
		usgEq(t, "amount_cents", cu.amount, 2800)
		for _, f := range []struct {
			id          int
			name, units string
			amount      int64
		}{{1, "aws", "100", 800}, {0, "gcp", "200", 2000}} {
			fu, ok := cu.filter(ch.Filters[f.id].ID)
			if !ok {
				t.Fatalf("no filter %s", f.name)
			}
			usgUnits(t, f.name, fu.units, f.units)
			usgEq(t, f.name+" amount_cents", fu.amount, f.amount)
		}
	})
}

func TestUsageScenarioPackage(t *testing.T) {
	cases := []struct {
		source      string
		props       string
		events      int
		units       string
		amountCents int64
	}{
		// source: spec/scenarios/current_usage/by_charge_model/package_spec.rb:16
		{"package_spec.rb:16", `{"amount":"100","free_units":0,"package_size":10}`, 3, "3", 10_000},
		// source: spec/scenarios/current_usage/by_charge_model/package_spec.rb:47
		{"package_spec.rb:47", `{"amount":"100","free_units":0,"package_size":10}`, 15, "15", 20_000},
		// source: spec/scenarios/current_usage/by_charge_model/package_spec.rb:84
		{"package_spec.rb:84", `{"amount":"100","free_units":5,"package_size":10}`, 3, "3", 0},
		// source: spec/scenarios/current_usage/by_charge_model/package_spec.rb:115
		{"package_spec.rb:115", `{"amount":"100","free_units":5,"package_size":10}`, 12, "12", 10_000},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			s, sub, m, _ := usgModel(t, catalog.AggregationTypeCount, "", catalog.ChargeModelPackage, tc.props)
			for range tc.events {
				s.usgEvent(sub, m.Code)
			}
			cu := s.usgCurrent(sub).first(t)
			usgUnits(t, "charge", cu.units, tc.units)
			usgEq(t, "amount_cents", cu.amount, tc.amountCents)
		})
	}
}

const usgGraduated = `{"graduated_ranges":[{"from_value":0,"to_value":5,"per_unit_amount":"10","flat_amount":"100"},{"from_value":6,"to_value":null,"per_unit_amount":"5","flat_amount":"50"}]}`

func TestUsageScenarioGraduated(t *testing.T) {
	ranges := `{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"2","flat_amount":"100"},{"from_value":11,"to_value":null,"per_unit_amount":"1","flat_amount":"50"}]}`
	// source: spec/scenarios/current_usage/by_charge_model/graduated_spec.rb:18
	// source: spec/scenarios/current_usage/by_charge_model/graduated_spec.rb:60
	for _, prorated := range []bool{false, true} {
		t.Run("no events", func(t *testing.T) {
			s := newScenario(t, usgDT(2024, 3, 5))
			s.usgTaxBE("20")
			c := s.Customer()
			var mopts []func(*catalog.BillableMetric)
			var copts []func(*catalog.Charge)
			if prorated {
				mopts, copts = append(mopts, usgRecurring), append(copts, usgProrated)
			}
			m := s.Metric("bm", catalog.AggregationTypeSum, "amount", mopts...)
			p := s.usgMonthly(0)
			sub := s.usgSub(c, p)
			s.Charge(p, m, catalog.ChargeModelGraduated, ranges, copts...)
			s.travelTo(usgDT(2024, 3, 6))
			for range 2 {
				u := s.usgCurrent(sub)
				usgEq(t, "total_amount_cents", u.TotalAmount, 12_000)
				usgUnits(t, "charge", u.first(t).units, "0")
				usgEq(t, "amount_cents", u.first(t).amount, 10_000)
			}
		})
	}
	cases := []struct {
		source      string
		events      int
		amountCents int64
	}{
		// source: spec/scenarios/current_usage/by_charge_model/graduated_spec.rb:114
		{"graduated_spec.rb:114", 3, 13_000},
		// source: spec/scenarios/current_usage/by_charge_model/graduated_spec.rb:145
		{"graduated_spec.rb:145", 8, 21_500},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			s, sub, m, _ := usgModel(t, catalog.AggregationTypeCount, "", catalog.ChargeModelGraduated, usgGraduated)
			for range tc.events {
				s.usgEvent(sub, m.Code)
			}
			cu := s.usgCurrent(sub).first(t)
			usgEq(t, "amount_cents", cu.amount, tc.amountCents)
		})
	}
}

func TestUsageScenarioVolume(t *testing.T) {
	ranges := `{"volume_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"5","flat_amount":"100"},{"from_value":11,"to_value":null,"per_unit_amount":"2","flat_amount":"50"}]}`
	cases := []struct {
		source      string
		events      int
		amountCents int64
	}{
		// source: spec/scenarios/current_usage/by_charge_model/volume_spec.rb:26
		{"volume_spec.rb:26", 5, 12_500},
		// source: spec/scenarios/current_usage/by_charge_model/volume_spec.rb:57
		{"volume_spec.rb:57", 15, 8_000},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			s, sub, m, _ := usgModel(t, catalog.AggregationTypeCount, "", catalog.ChargeModelVolume, ranges)
			for range tc.events {
				s.usgEvent(sub, m.Code)
			}
			cu := s.usgCurrent(sub).first(t)
			usgEq(t, "events", cu.events, int64(tc.events))
			usgEq(t, "amount_cents", cu.amount, tc.amountCents)
		})
	}
}

func TestUsageScenarioGraduatedPercentage(t *testing.T) {
	ranges := `{"graduated_percentage_ranges":[{"from_value":0,"to_value":100,"rate":"5","flat_amount":"0"},{"from_value":101,"to_value":null,"rate":"2","flat_amount":"10"}]}`
	cases := []struct {
		source, amount string
		amountCents    int64
	}{
		// source: spec/scenarios/current_usage/by_charge_model/graduated_percentage_spec.rb:26
		{"graduated_percentage_spec.rb:26", "50", 250},
		// source: spec/scenarios/current_usage/by_charge_model/graduated_percentage_spec.rb:56
		{"graduated_percentage_spec.rb:56", "150", 1600},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			s, sub, m, _ := usgModel(t, catalog.AggregationTypeSum, "amount", catalog.ChargeModelGraduatedPercentage, ranges)
			s.usgEvent(sub, m.Code, "amount", tc.amount)
			cu := s.usgCurrent(sub).first(t)
			usgUnits(t, "charge", cu.units, tc.amount)
			usgEq(t, "amount_cents", cu.amount, tc.amountCents)
		})
	}
}

// usgStep is one event then the expected current usage.
type usgStep struct {
	at     string // clock and event timestamp; empty keeps the clock
	amount string
	// expected: Amount (-1 skips), TotalAmount, units of the first charge
	// (empty skips) and its amount (-1 skips).
	usage, total int64
	units        string
	charge       int64
}

// usgRun ingests each step's event on field and checks the usage.
func (s *scenario) usgRun(t *testing.T, sub *subscription.Subscription, m *catalog.BillableMetric, field string, steps []usgStep, extra ...string) {
	t.Helper()
	for i, st := range steps {
		if st.at != "" {
			s.travelTo(usgParse(t, st.at))
		}
		if st.amount != "" {
			s.usgEvent(sub, m.Code, append([]string{field, st.amount}, extra...)...)
		}
		u := s.usgCurrent(sub)
		if st.usage >= 0 {
			usgEq(t, "step amount_cents", u.Amount, st.usage)
		}
		usgEq(t, "step total_amount_cents", u.TotalAmount, st.total)
		if st.units != "" {
			usgUnits(t, "step charge", u.first(t).units, st.units)
		}
		if st.charge >= 0 {
			usgEq(t, "step charge amount_cents", u.first(t).amount, st.charge)
		}
		if t.Failed() {
			t.Fatalf("step %d failed", i)
		}
	}
}

func TestUsageScenarioPercentage(t *testing.T) {
	newPct := func(t *testing.T, props string) (*scenario, *subscription.Subscription, *catalog.BillableMetric) {
		s := newScenario(t, usgDT(2023, 3, 5))
		s.usgTaxBE("20")
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeSum, "amount")
		p := s.usgMonthly(1000)
		sub := s.usgSub(c, p)
		s.Charge(p, m, catalog.ChargeModelPercentage, props)
		s.travelTo(usgDT(2023, 3, 6))
		return s, sub, m
	}
	// source: spec/scenarios/current_usage/by_charge_model/percentage_spec.rb:20
	t.Run("free units per events and fixed amount", func(t *testing.T) {
		s, sub, m := newPct(t, `{"rate":"1","fixed_amount":"5","free_units_per_events":3}`)
		s.usgRun(t, sub, m, "amount", []usgStep{
			{amount: "10", usage: -1, total: 0, units: "10", charge: -1},
			{amount: "10", usage: -1, total: 0, units: "20", charge: -1},
			{amount: "10", usage: -1, total: 0, units: "30", charge: -1},
			{amount: "10", usage: 510, total: 612, units: "40", charge: -1},
		})
	})
	// source: spec/scenarios/current_usage/by_charge_model/percentage_spec.rb:93
	t.Run("free units per total aggregation and fixed amount", func(t *testing.T) {
		s, sub, m := newPct(t, `{"rate":"1","fixed_amount":"5","free_units_per_total_aggregation":"15.0"}`)
		s.usgRun(t, sub, m, "amount", []usgStep{
			{at: "2023-03-06T00:01:00", amount: "4", usage: -1, total: 0, units: "4", charge: -1},
			{at: "2023-03-06T00:02:00", amount: "4", usage: -1, total: 0, units: "8", charge: -1},
			{at: "2023-03-06T00:03:00", amount: "4", usage: -1, total: 0, units: "12", charge: -1},
			{at: "2023-03-06T00:04:00", amount: "4", usage: 501, total: 601, units: "16", charge: -1},
			{at: "2023-03-06T00:05:00", amount: "10", usage: 1011, total: 1213, units: "26", charge: -1},
		})
	})
	// source: spec/scenarios/current_usage/by_charge_model/percentage_spec.rb:187
	t.Run("events overage", func(t *testing.T) {
		s, sub, m := newPct(t, `{"rate":"1","fixed_amount":"5","free_units_per_events":3,"free_units_per_total_aggregation":"15.0"}`)
		s.usgRun(t, sub, m, "amount", []usgStep{
			{amount: "1", usage: -1, total: 0, units: "1", charge: -1},
			{amount: "1", usage: -1, total: 0, units: "2", charge: -1},
			{amount: "1", usage: -1, total: 0, units: "3", charge: -1},
			{amount: "1", usage: 501, total: 601, units: "4", charge: -1},
		})
	})
	// source: spec/scenarios/current_usage/by_charge_model/percentage_spec.rb:265
	t.Run("total aggregation overage", func(t *testing.T) {
		s, sub, m := newPct(t, `{"rate":"1","fixed_amount":"5","free_units_per_events":3,"free_units_per_total_aggregation":"15.0"}`)
		s.usgRun(t, sub, m, "amount", []usgStep{
			{amount: "10", usage: -1, total: 0, units: "10", charge: -1},
			{amount: "10", usage: 505, total: 606, units: "20", charge: -1},
			{amount: "10", usage: 1015, total: 1218, units: "30", charge: -1},
		})
	})
	minMax := `"rate":"1","fixed_amount":"1","per_transaction_max_amount":"12","per_transaction_min_amount":"1.75"`
	// source: spec/scenarios/current_usage/by_charge_model/percentage_spec.rb:332
	// source: spec/scenarios/current_usage/by_charge_model/percentage_spec.rb:402
	for _, grouped := range []bool{false, true} {
		t.Run("min and max per transaction amount", func(t *testing.T) {
			props, extra := `{`+minMax+`}`, []string(nil)
			if grouped {
				props, extra = `{`+minMax+`,"pricing_group_keys":["region","provider"]}`, []string{"region", "US", "provider", "AWS"}
			}
			s, sub, m := newPct(t, props)
			s.usgRun(t, sub, m, "amount", []usgStep{
				{amount: "100", usage: -1, total: 240, units: "100", charge: 200},
				{amount: "1000", usage: -1, total: 1560, units: "1100", charge: 1300},
				{amount: "10000", usage: -1, total: 3000, units: "11100", charge: 2500},
			}, extra...)
			if grouped {
				if _, ok := s.usgCurrent(sub).first(t).group("region", "US", "provider", "AWS"); !ok {
					t.Errorf("grouped_usage.first.grouped_by != {region: US, provider: AWS}")
				}
			}
		})
	}
	// source: spec/scenarios/current_usage/by_charge_model/percentage_spec.rb:474
	// source: spec/scenarios/current_usage/by_charge_model/percentage_spec.rb:517
	for _, grouped := range []bool{false, true} {
		t.Run("min and max per transaction amount and no events", func(t *testing.T) {
			props := `{` + minMax + `}`
			if grouped {
				props = `{` + minMax + `,"pricing_group_keys":["region","provider"]}`
			}
			s, sub, m := newPct(t, props)
			for range 2 {
				s.usgRun(t, sub, m, "amount", []usgStep{{usage: -1, total: 0, units: "0", charge: 0}})
			}
		})
	}
}

func TestUsageScenarioDynamic(t *testing.T) {
	newDyn := func(t *testing.T, props string) (*scenario, *subscription.Subscription, *catalog.BillableMetric) {
		s := newScenario(t, usgDT(2023, 3, 5))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeSum, "item_id")
		p := s.usgMonthly(1000)
		s.Charge(p, m, catalog.ChargeModelDynamic, props)
		sub := s.usgSub(c, p)
		s.travelTo(usgDT(2023, 3, 6))
		return s, sub, m
	}
	// source: spec/scenarios/current_usage/by_charge_model/dynamic_spec.rb:30
	t.Run("ungrouped", func(t *testing.T) {
		s, sub, m := newDyn(t, `{}`)
		s.usgDynamicEvent(sub, m.Code, "10.1", "item_id", "10")
		u := s.usgCurrent(sub)
		usgEq(t, "total_amount_cents", u.TotalAmount, 10)
		usgUnits(t, "charge", u.first(t).units, "10")
		s.usgDynamicEvent(sub, m.Code, "901.9", "item_id", "10")
		u = s.usgCurrent(sub)
		usgEq(t, "total_amount_cents", u.TotalAmount, 912)
		usgUnits(t, "charge", u.first(t).units, "20")
	})
	// source: spec/scenarios/current_usage/by_charge_model/dynamic_spec.rb:75
	t.Run("grouped", func(t *testing.T) {
		s, sub, m := newDyn(t, `{"pricing_group_keys":["group_key"]}`)
		s.usgDynamicEvent(sub, m.Code, "10.1", "item_id", "10", "group_key", "value 1")
		u := s.usgCurrent(sub)
		usgEq(t, "total_amount_cents", u.TotalAmount, 10)
		usgUnits(t, "charge", u.first(t).units, "10")
		if _, ok := u.first(t).group("group_key", "value 1"); !ok {
			t.Errorf("no group value 1")
		}
		s.usgDynamicEvent(sub, m.Code, "901.9", "item_id", "10", "group_key", "value 2")
		u = s.usgCurrent(sub)
		usgEq(t, "total_amount_cents", u.TotalAmount, 912)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "20")
		usgEq(t, "grouped_usage.size", len(cu.fees), 2)
		for _, g := range []struct {
			value  string
			amount int64
		}{{"value 2", 902}, {"value 1", 10}} {
			gu, ok := cu.group("group_key", g.value)
			if !ok {
				t.Fatalf("no group %s", g.value)
			}
			usgEq(t, g.value+" amount_cents", gu.amount, g.amount)
			usgEq(t, g.value+" events_count", gu.events, 1)
			usgUnits(t, g.value, gu.units, "10")
			usgUnits(t, g.value+" total_aggregated", gu.fees[0].TotalAggregatedUnits, "10")
		}
	})
	// source: spec/scenarios/current_usage/by_charge_model/dynamic_spec.rb:151
	for _, grouped := range []bool{false, true} {
		for _, tc := range []struct {
			precise string
			want    int64
		}{{"4312", 4312}, {"4312.5", 4313}} {
			t.Run("precise cents in usage preview and billing", func(t *testing.T) {
				props := `{}`
				if grouped {
					props = `{"pricing_group_keys":["database_name"]}`
				}
				s := newScenario(t, usgDT(2023, 3, 1))
				c := s.Customer()
				m := s.Metric("bm", catalog.AggregationTypeSum, "item_id")
				p := s.usgMonthly(0)
				s.Charge(p, m, catalog.ChargeModelDynamic, props)
				sub := s.usgSub(c, p)
				s.travelTo(usgDT(2023, 3, 6))
				s.usgDynamicEvent(sub, m.Code, tc.precise, "item_id", "4", "database_name", "qa-db")
				u := s.usgCurrent(sub)
				usgEq(t, "total_amount_cents", u.TotalAmount, tc.want)
				if grouped {
					usgEq(t, "grouped_usage[0].amount_cents", u.first(t).fees[0].Amount, tc.want)
				}
				pv, err := s.E.Invoices.Preview(s.Ctx, invoicing.PreviewInput{OrganizationID: s.OrgID(), CustomerID: c.ID, SubscriptionIDs: []core.SubscriptionID{sub.ID}})
				s.must(err)
				usgEq(t, "preview fees_amount_cents", pv.Invoice.FeesAmount, tc.want)
				s.travelTo(usgDT(2023, 4, 1))
				s.performBilling()
				invs := s.usgSubscriptionInvoices(c, sub)
				if len(invs) == 0 {
					t.Fatalf("no invoice")
				}
				usgEq(t, "invoice total_amount_cents", invs[0].TotalAmount, tc.want)
			})
		}
	}
}

// usgThreeRanges are the three graduated ranges of the prorated sum
// scenarios.
const usgThreeRanges = `{"graduated_ranges":[{"from_value":0,"to_value":5,"per_unit_amount":"10","flat_amount":"100"},{"from_value":6,"to_value":15,"per_unit_amount":"5","flat_amount":"50"},{"from_value":16,"to_value":null,"per_unit_amount":"2","flat_amount":"0"}]}`

// usgProratedGraduated creates a scenario at start, a customer, a monthly
// plan of 0 with a prorated graduated charge of ranges on a recurring
// metric of agg on field, and a subscription.
func usgProratedGraduated(t *testing.T, start string, agg catalog.AggregationType, field, ranges string, subOpts ...func(*subscription.Subscription)) (*scenario, *subscription.Subscription, *catalog.BillableMetric, *catalog.Plan) {
	t.Helper()
	s := newScenario(t, usgParse(t, start))
	s.Tax("zero", "0")
	c := s.Customer()
	m := s.Metric("bm", agg, field, usgRecurring)
	p := s.usgMonthly(0)
	s.Charge(p, m, catalog.ChargeModelGraduated, ranges, usgProrated)
	sub := s.usgSub(c, p, subOpts...)
	return s, sub, m, p
}

// usgBill runs the periodic billing at at and returns the invoices of sub.
func (s *scenario) usgBill(t *testing.T, sub *subscription.Subscription, at string) []*invoice.Invoice {
	t.Helper()
	s.travelTo(usgParse(t, at))
	s.performBilling()
	c := s.ReloadCustomer(sub.CustomerID)
	return s.usgSubscriptionInvoices(c, sub)
}

// usgTerminate terminates sub now and returns its reloaded state.
func (s *scenario) usgTerminate(t *testing.T, sub *subscription.Subscription) *subscription.Subscription {
	t.Helper()
	out, err := s.E.Lifecycle.Terminate(s.Ctx, lifecycle.TerminateInput{OrganizationID: s.OrgID(), SubscriptionID: sub.ID})
	s.must(err)
	s.drain()
	return out
}

func TestUsageScenarioProratedGraduatedSum(t *testing.T) {
	period1 := []usgStep{
		{at: "2023-09-10T00:00:00", amount: "2", usage: 11_400, total: 11_400, units: "2", charge: -1},
		{at: "2023-09-16T00:00:00", amount: "5", usage: 18_400, total: 18_400, units: "7", charge: -1},
		{at: "2023-09-20T00:00:00", amount: "-6", usage: 16_567, total: 16_567, units: "1", charge: -1},
		{at: "2023-09-25T00:00:00", amount: "10", usage: 17_967, total: 17_967, units: "11", charge: -1},
		{at: "2023-09-26T00:00:00", amount: "4", usage: 18_300, total: 18_300, units: "15", charge: -1},
		{at: "2023-09-30T00:00:00", amount: "60", usage: 18_700, total: 18_700, units: "75", charge: -1},
	}
	period2 := []usgStep{
		{at: "2023-10-05T00:00:00", usage: 37_000, total: 37_000, units: "75", charge: -1},
		{at: "2023-10-17T00:00:00", amount: "20", usage: 38_935, total: 38_935, units: "95", charge: -1},
	}
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:20
	t.Run("three ranges and one overflow case", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2023-09-01T00:00:00", catalog.AggregationTypeSum, "amount", usgThreeRanges)
		s.usgRun(t, sub, m, "amount", []usgStep{{usage: 10_000, total: 10_000, units: "0", charge: -1}})
		s.usgRun(t, sub, m, "amount", period1)
		invs := s.usgBill(t, sub, "2023-10-01T00:00:00")
		usgEq(t, "invoices", len(invs), 1)
		usgEq(t, "invoice total_amount_cents", invs[0].TotalAmount, 18_700)
		s.usgRun(t, sub, m, "amount", period2)
	})
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:199
	t.Run("old events before first invoice", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2023-12-01T00:00:00", catalog.AggregationTypeSum, "amount",
			`{"graduated_ranges":[{"from_value":0,"to_value":5,"per_unit_amount":"0","flat_amount":"0"},{"from_value":6,"to_value":null,"per_unit_amount":"12","flat_amount":"0"}]}`)
		s.travelTo(usgDT(2023, 12, 2))
		old := usgDT(2023, 11, 7, 5, 54, 53) // 1_699_336_493
		s.usgEventAt(sub, m.Code, old, "amount", "5")
		s.usgEventAt(sub, m.Code, old, "amount", "5")
		s.usgRun(t, sub, m, "amount", []usgStep{{usage: 6_000, total: 6_000, units: "10", charge: -1}})
		invs := s.usgBill(t, sub, "2024-01-01T00:00:00")
		usgEq(t, "invoices", len(invs), 1)
		usgEq(t, "invoice total_amount_cents", invs[0].TotalAmount, 6_000)
		s.usgRun(t, sub, m, "amount", []usgStep{
			{at: "2024-01-05T00:00:00", usage: 6_000, total: 6_000, units: "10", charge: -1},
			{at: "2024-01-06T00:00:00", amount: "2", usage: 8_013, total: 8_013, units: "12", charge: -1},
		})
	})
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:301
	t.Run("old events before first invoice and termination", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2023-10-01T00:00:00", catalog.AggregationTypeSum, "amount",
			`{"graduated_ranges":[{"from_value":0,"to_value":5,"per_unit_amount":"0","flat_amount":"0"},{"from_value":6,"to_value":null,"per_unit_amount":"10","flat_amount":"0"}]}`)
		s.travelTo(usgDT(2023, 10, 5))
		s.usgEvent(sub, m.Code, "amount", "4")
		s.usgEvent(sub, m.Code, "amount", "3")
		s.travelTo(usgDT(2023, 11, 5))
		s.usgEvent(sub, m.Code, "amount", "-1")
		s.usgRun(t, sub, m, "amount", []usgStep{
			{at: "2023-12-07T00:00:00", usage: 1_000, total: 1_000, units: "6", charge: -1},
			{amount: "1", usage: 1_806, total: 1_806, units: "7", charge: -1},
		})
		// lago calls TerminateService without billing the elapsed periods.
		got := s.usgTerminate(t, sub)
		usgEq(t, "status", got.Status, subscription.StatusTerminated)
		invs := s.usgSubscriptionInvoices(s.ReloadCustomer(sub.CustomerID), sub)
		usgEq(t, "invoices", len(invs), 1)
		// Deviation: lago bills 258. Both bill the 6 persisted units for the
		// 7 days to the termination at 2023-12-07 00:00, 1 over the free
		// tier: 1 × $10 × 7/31 = 226. lago also counts the 1-unit event sent
		// at the termination instant, for 1 day (1 × $10 × 1/31 = 32); in
		// tanvo an event at terminated_at belongs to no period (domain
		// §10.2, half-open periods).
		usgEq(t, "invoice total_amount_cents", invs[len(invs)-1].TotalAmount, 226)
		usgEq(t, "issuing_date", invs[len(invs)-1].IssuingDate.String(), "2023-12-07")
	})
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:406
	t.Run("upgrade", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2023-09-01T00:00:00", catalog.AggregationTypeSum, "amount", usgThreeRanges)
		for _, st := range period1 {
			s.travelTo(usgParse(t, st.at))
			s.usgEvent(sub, m.Code, "amount", st.amount)
		}
		s.usgRun(t, sub, m, "amount", []usgStep{{usage: 18_700, total: 18_700, units: "75", charge: -1}})
		invs := s.usgBill(t, sub, "2023-10-01T00:00:00")
		usgEq(t, "invoices", len(invs), 1)
		usgEq(t, "invoice total_amount_cents", invs[0].TotalAmount, 18_700)
		s.usgRun(t, sub, m, "amount", period2)

		s.travelTo(usgDT(2023, 10, 18, 5, 20))
		pNew := s.usgMonthly(100)
		s.Charge(pNew, m, catalog.ChargeModelGraduated, usgThreeRanges, usgProrated)
		next, err := s.E.Lifecycle.ChangePlan(s.Ctx, lifecycle.ChangePlanInput{OrganizationID: s.OrgID(), SubscriptionID: sub.ID, PlanID: pNew.ID})
		s.must(err)
		s.drain()
		c := s.ReloadCustomer(sub.CustomerID)
		old := billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*subscription.Subscription, error) {
			return r.Subscriptions.Get(ctx, s.OrgID(), sub.ID)
		})
		usgEq(t, "old status", old.Status, subscription.StatusTerminated)
		invs = s.usgSubscriptionInvoices(c, sub)
		usgEq(t, "old invoices", len(invs), 2)
		usgEq(t, "termination charge fees", len(s.usgChargeFees(invs[1].ID)), 1)
		usgEq(t, "termination total_amount_cents", invs[1].TotalAmount, 27_323)

		invs = s.usgBill(t, next, "2023-11-01T00:00:00")
		usgEq(t, "new invoices", len(invs), 1)
		usgEq(t, "new total_amount_cents", invs[0].TotalAmount, 26_742+45)
		s.usgRun(t, next, m, "amount", []usgStep{{at: "2023-11-05T00:00:00", usage: 41_000, total: 41_000, units: "95", charge: -1}})
	})
}

func TestUsageScenarioProratedGraduatedUniqueCount(t *testing.T) {
	two := `{"graduated_ranges":[{"from_value":0,"to_value":1,"per_unit_amount":"10","flat_amount":"100"},{"from_value":2,"to_value":null,"per_unit_amount":"5","flat_amount":"50"}]}`
	uc := catalog.AggregationTypeUniqueCount
	// steps of unique-count events: the amount property is the item.
	run := func(t *testing.T, s *scenario, sub *subscription.Subscription, m *catalog.BillableMetric, steps []usgStep, ops []string) {
		t.Helper()
		for i, st := range steps {
			op := "add"
			if i < len(ops) && ops[i] != "" {
				op = ops[i]
			}
			if st.amount != "" {
				if st.at != "" {
					s.travelTo(usgParse(t, st.at))
				}
				s.usgEvent(sub, m.Code, "amount", st.amount, "operation_type", op)
				st.at, st.amount = "", ""
			}
			s.usgRun(t, sub, m, "amount", []usgStep{st})
		}
	}
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:630
	t.Run("two ranges", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2023-09-01T00:00:00", uc, "amount", two)
		run(t, s, sub, m, []usgStep{
			{at: "2023-09-10T00:00:00", amount: "1111", usage: 10_700, total: 10_700, units: "1", charge: -1},
			{at: "2023-09-12T00:00:00", amount: "1111", usage: 10_100, total: 10_100, units: "0", charge: -1},
			{at: "2023-09-14T00:00:00", amount: "1111", usage: 10_667, total: 10_667, units: "1", charge: -1},
			{at: "2023-09-15T00:00:00", amount: "2222", usage: 15_933, total: 15_933, units: "2", charge: -1},
			{at: "2023-09-16T00:00:00", amount: "2222", usage: 15_933, total: 15_933, units: "2", charge: -1},
			{at: "2023-09-20T00:00:00", amount: "3333", usage: 16_117, total: 16_117, units: "3", charge: -1},
		}, []string{"add", "remove"})
		invs := s.usgBill(t, sub, "2023-10-01T00:00:00")
		usgEq(t, "invoices", len(invs), 1)
		usgEq(t, "invoice total_amount_cents", invs[0].TotalAmount, 16_117)
		run(t, s, sub, m, []usgStep{
			{at: "2023-10-05T00:00:00", usage: 17_000, total: 17_000, units: "3", charge: -1},
			{at: "2023-10-17T00:00:00", amount: "4444", usage: 17_242, total: 17_242, units: "4", charge: -1},
		}, nil)
		// The 2023-11-01 billing is not run in lago either: the clock
		// only moves.
		run(t, s, sub, m, []usgStep{{at: "2023-11-05T00:00:00", usage: 17_500, total: 17_500, units: "4", charge: -1}}, nil)
	})
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:806
	t.Run("removal in another interval", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2023-09-01T00:00:00", uc, "amount", two)
		run(t, s, sub, m, []usgStep{{at: "2023-09-10T00:00:00", amount: "1111", usage: 10_700, total: 10_700, units: "1", charge: -1}}, nil)
		invs := s.usgBill(t, sub, "2023-10-01T00:00:00")
		usgEq(t, "invoices", len(invs), 1)
		usgEq(t, "invoice total_amount_cents", invs[0].TotalAmount, 10_700)
		run(t, s, sub, m, []usgStep{
			{at: "2023-10-05T00:00:00", usage: 11_000, total: 11_000, units: "1", charge: -1},
			{at: "2023-10-17T00:00:00", amount: "1111", usage: 10_548, total: 10_548, units: "0", charge: -1},
			{at: "2023-10-19T00:00:00", amount: "2222", usage: 10_968, total: 10_968, units: "1", charge: -1},
		}, []string{"", "remove", "add"})
		invs = s.usgBill(t, sub, "2023-11-01T00:00:00")
		usgEq(t, "invoices", len(invs), 2)
		usgEq(t, "invoice total_amount_cents", invs[1].TotalAmount, 10_968)
		run(t, s, sub, m, []usgStep{{at: "2023-11-05T00:00:00", usage: 11_000, total: 11_000, units: "1", charge: -1}}, nil)
	})
	five := `{"graduated_ranges":[{"from_value":0,"to_value":5,"per_unit_amount":"10","flat_amount":"100"},{"from_value":6,"to_value":null,"per_unit_amount":"5","flat_amount":"50"}]}`
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:928
	t.Run("multiple events on the same day", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2023-09-01T00:00:00", uc, "amount", five)
		run(t, s, sub, m, []usgStep{
			{at: "2023-10-10T00:00:00", amount: "1111", usage: 10_710, total: 10_710, units: "1", charge: -1},
			{at: "2023-10-20T00:00:00", amount: "2222", usage: 11_097, total: 11_097, units: "2", charge: -1},
			{at: "2023-10-20T00:00:00", amount: "3333", usage: 11_484, total: 11_484, units: "3", charge: -1},
			{at: "2023-10-20T00:00:00", amount: "4444", usage: 11_871, total: 11_871, units: "4", charge: -1},
			{at: "2023-10-20T00:00:00", amount: "5555", usage: 12_258, total: 12_258, units: "5", charge: -1},
			{at: "2023-10-25T00:00:00", amount: "6666", usage: 17_371, total: 17_371, units: "6", charge: -1},
		}, nil)
		// lago's clock never ran the 2023-10-01 billing: the first invoice
		// is the one of October.
		invs := s.usgBill(t, sub, "2023-11-01T00:00:00")
		usgEq(t, "invoices", len(invs), 1)
		usgEq(t, "invoice total_amount_cents", invs[0].TotalAmount, 17_371)
		run(t, s, sub, m, []usgStep{{at: "2023-11-05T00:00:00", usage: 20_500, total: 20_500, units: "6", charge: -1}}, nil)
	})
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:1080
	t.Run("old events before first invoice and termination", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2023-10-01T00:00:00", uc, "amount",
			`{"graduated_ranges":[{"from_value":0,"to_value":1,"per_unit_amount":"5","flat_amount":"10"},{"from_value":2,"to_value":null,"per_unit_amount":"15","flat_amount":"30"}]}`)
		s.travelTo(usgDT(2023, 10, 5))
		s.usgEvent(sub, m.Code, "amount", "1111", "operation_type", "add")
		run(t, s, sub, m, []usgStep{
			{at: "2023-12-07T00:00:00", usage: 1_500, total: 1_500, units: "1", charge: -1},
			{amount: "2222", usage: 5_710, total: 5_710, units: "2", charge: -1},
		}, nil)
		got := s.usgTerminate(t, sub)
		usgEq(t, "status", got.Status, subscription.StatusTerminated)
		invs := s.usgSubscriptionInvoices(s.ReloadCustomer(sub.CustomerID), sub)
		usgEq(t, "invoices", len(invs), 1)
		// Deviation: lago bills 4_161. Both bill item 1111 for the 7 days to
		// the termination at 2023-12-07 00:00: $10 + $5 × 7/31 = 1_113. lago
		// also counts item 2222, added at the termination instant, for 1
		// day in the second tier ($30 + $15 × 1/31 = 3_048); in tanvo an
		// event at terminated_at belongs to no period (domain §10.2,
		// half-open periods).
		usgEq(t, "invoice total_amount_cents", invs[len(invs)-1].TotalAmount, 1_113)
		usgEq(t, "issuing_date", invs[len(invs)-1].IssuingDate.String(), "2023-12-07")
	})
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:1197
	t.Run("item removed before billing period", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2024-10-01T00:00:00", uc, "user_id",
			`{"graduated_ranges":[{"from_value":0,"to_value":1,"per_unit_amount":"0","flat_amount":"0"},{"from_value":2,"to_value":null,"per_unit_amount":"10","flat_amount":"0"}]}`)
		for _, e := range []struct{ at, user, op string }{
			{"2024-10-05T00:00:00", "73283", "add"},
			{"2024-10-06T00:00:00", "73290", "add"},
			{"2024-10-07T00:00:00", "78924", "add"},
			{"2024-10-25T00:00:00", "73290", "remove"},
		} {
			s.travelTo(usgParse(t, e.at))
			s.usgEvent(sub, m.Code, "user_id", e.user, "operation_type", e.op)
		}
		invs := s.usgBill(t, sub, "2024-11-01T00:00:00")
		usgEq(t, "invoices", len(invs), 1)
		s.travelTo(usgDT(2024, 11, 15))
		u := s.usgCurrent(sub)
		usgUnits(t, "charge", u.first(t).units, "2")
		usgEq(t, "amount_cents", u.Amount, 1000)
		invs = s.usgBill(t, sub, "2024-12-01T00:00:00")
		usgEq(t, "invoices", len(invs), 2)
		usgEq(t, "invoice total_amount_cents", invs[len(invs)-1].TotalAmount, 1000)
	})
	// source: spec/scenarios/current_usage/by_charge_model/prorated_graduated_spec.rb:1312
	t.Run("combinations of add and remove", func(t *testing.T) {
		s, sub, m, _ := usgProratedGraduated(t, "2025-05-21T04:31:39", uc, "user_id",
			`{"graduated_ranges":[{"from_value":0,"to_value":3,"per_unit_amount":"0","flat_amount":"0"},{"from_value":4,"to_value":null,"per_unit_amount":"10","flat_amount":"0"}]}`,
			billingtest.Anniversary)
		for _, e := range []struct{ at, user, op string }{
			{"2025-05-21T00:00:00", "49303", "add"},
			{"2025-05-31T00:00:00", "49067", "add"},
			{"2025-07-23T00:00:00", "62741", "add"},
			{"2025-07-24T00:00:00", "49067", "remove"},
			{"2025-07-24T00:00:00", "63011", "add"},
		} {
			s.travelTo(usgParse(t, e.at))
			s.usgEvent(sub, m.Code, "user_id", e.user, "operation_type", e.op)
		}
		s.travelTo(usgParse(t, "2025-08-19T18:00:00"))
		usgEq(t, "amount_cents", s.usgCurrent(sub).Amount, 0)
	})
}

func TestUsageScenarioPricingUnit(t *testing.T) {
	// newPriced creates the lago scenario at 2024-03-05: a monthly plan of
	// 0 with one charge of m priced in "credits" (CRD) at 0.5, and a
	// subscription.
	newPriced := func(t *testing.T, agg catalog.AggregationType, field, props string) (*scenario, *subscription.Subscription, *catalog.BillableMetric) {
		s := newScenario(t, usgDT(2024, 3, 5))
		c := s.Customer()
		pu := s.usgPricingUnit("credits", "CRD")
		m := s.Metric("bm", agg, field)
		p := s.usgMonthly(0)
		s.Charge(p, m, catalog.ChargeModelStandard, props, usgPriced(pu, "0.5"))
		return s, s.usgSub(c, p), m
	}
	checkPU := func(t *testing.T, what string, cu usgCharge, amount string) {
		t.Helper()
		short, rate, got, ok := cu.pricingUnits()
		if !ok {
			t.Fatalf("%s pricing_unit_details = nil, want = present", what)
		}
		usgEq(t, what+" short_name", short, "CRD")
		usgUnits(t, what+" conversion_rate", rate, "0.5")
		usgUnits(t, what+" pricing unit amount_cents", got, amount)
	}
	// source: spec/scenarios/current_usage/pricing_unit_spec.rb:37
	t.Run("standard charge and events", func(t *testing.T) {
		s, sub, m := newPriced(t, catalog.AggregationTypeSum, "credits", `{"amount":"1.0"}`)
		for range 3 {
			s.usgEvent(sub, m.Code, "credits", "100")
		}
		u := s.usgCurrent(sub)
		usgEq(t, "charges_usage.count", len(u.charges), 1)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "300")
		usgEq(t, "events_count", cu.events, 3)
		usgEq(t, "amount_cents", cu.amount, 15_000)
		checkPU(t, "charge", cu, "30000")
	})
	// source: spec/scenarios/current_usage/pricing_unit_spec.rb:83
	t.Run("zero usage", func(t *testing.T) {
		s, sub, _ := newPriced(t, catalog.AggregationTypeCount, "", `{"amount":"5.0"}`)
		u := s.usgCurrent(sub)
		usgEq(t, "charges_usage.count", len(u.charges), 1)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "0")
		usgEq(t, "events_count", cu.events, 0)
		usgEq(t, "amount_cents", cu.amount, 0)
		checkPU(t, "charge", cu, "0")
	})
	// source: spec/scenarios/current_usage/pricing_unit_spec.rb:116
	t.Run("grouped by", func(t *testing.T) {
		s, sub, m := newPriced(t, catalog.AggregationTypeSum, "credits", `{"amount":"1.0","pricing_group_keys":["region"]}`)
		s.usgEvent(sub, m.Code, "credits", "100", "region", "us")
		s.usgEvent(sub, m.Code, "credits", "200", "region", "eu")
		u := s.usgCurrent(sub)
		usgEq(t, "charges_usage.count", len(u.charges), 1)
		cu := u.first(t)
		usgEq(t, "amount_cents", cu.amount, 15_000)
		checkPU(t, "charge", cu, "30000")
		usgEq(t, "grouped_usage.count", len(cu.fees), 2)
		for _, g := range []struct {
			region, units, pu string
			amount            int64
		}{{"us", "100", "10000", 5000}, {"eu", "200", "20000", 10_000}} {
			gu, ok := cu.group("region", g.region)
			if !ok {
				t.Fatalf("no group %s", g.region)
			}
			usgUnits(t, g.region, gu.units, g.units)
			usgEq(t, g.region+" amount_cents", gu.amount, g.amount)
			checkPU(t, g.region, gu, g.pu)
		}
	})
	// source: spec/scenarios/current_usage/pricing_unit_spec.rb:174
	t.Run("without applied pricing unit", func(t *testing.T) {
		s := newScenario(t, usgDT(2024, 3, 5))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeCount, "")
		p := s.usgMonthly(0)
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`)
		sub := s.usgSub(c, p)
		s.usgEvent(sub, m.Code)
		u := s.usgCurrent(sub)
		usgEq(t, "charges_usage.count", len(u.charges), 1)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "1")
		usgEq(t, "amount_cents", cu.amount, 1000)
		if _, _, _, ok := cu.pricingUnits(); ok {
			t.Errorf("pricing_unit_details = present, want = nil")
		}
	})
}

func TestUsageScenarioAnniversaryPeriodBoundaries(t *testing.T) {
	// subscribeOn subscribes on date to an anniversary plan of interval
	// with a standard count charge.
	subscribeOn := func(t *testing.T, date time.Time, interval period.Interval) (*scenario, *subscription.Subscription, *catalog.BillableMetric) {
		s := newScenario(t, date)
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeCount, "")
		p := s.Plan(interval, 0)
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		return s, s.usgSub(c, p, billingtest.Anniversary), m
	}
	// periodOn returns the charges period of the usage on date; lago's
	// inclusive to_datetime (…23:59:59) is To minus one second.
	periodOn := func(s *scenario, sub *subscription.Subscription, date time.Time) (time.Time, time.Time) {
		s.travelTo(date)
		u := s.usgCurrent(sub)
		return u.From, u.To.Add(-time.Second)
	}
	eqTime := func(t *testing.T, what string, got, want time.Time) {
		t.Helper()
		if !got.Equal(want) {
			t.Errorf("%s = %s, want = %s", what, got, want)
		}
	}
	// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:47
	t.Run("yearly leap day hands the clamped day to one period", func(t *testing.T) {
		s, sub, _ := subscribeOn(t, usgDT(2024, 2, 29), period.Yearly)
		from, to := periodOn(s, sub, usgDT(2025, 2, 20))
		eqTime(t, "from", from, usgDT(2024, 2, 29))
		eqTime(t, "to", to, usgDT(2025, 2, 27, 23, 59, 59))
		from, to = periodOn(s, sub, usgDT(2025, 3, 10))
		eqTime(t, "from", from, usgDT(2025, 2, 28))
		eqTime(t, "to", to, usgDT(2026, 2, 27, 23, 59, 59))
	})
	// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:55
	t.Run("yearly leap day counts an event on the clamped day once", func(t *testing.T) {
		s, sub, m := subscribeOn(t, usgDT(2024, 2, 29), period.Yearly)
		s.travelTo(usgDT(2025, 2, 28, 12))
		s.usgEvent(sub, m.Code)
		s.travelTo(usgDT(2025, 3, 10))
		usgEq(t, "opening events_count", s.usgCurrent(sub).first(t).events, 1)
		s.travelTo(usgDT(2025, 2, 20))
		usgEq(t, "closing events_count", s.usgCurrent(sub).first(t).events, 0)
	})
	for _, tc := range []struct {
		source   string
		name     string
		start    time.Time
		interval period.Interval
		on, from time.Time
	}{
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:77
		{"anniversary_period_boundaries_spec.rb:77", "quarterly month end holds the quarter", usgDT(2022, 1, 31), period.Quarterly, usgDT(2022, 5, 31), usgDT(2022, 4, 30)},
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:82
		{"anniversary_period_boundaries_spec.rb:82", "quarterly month end keeps the day before an anniversary", usgDT(2022, 1, 31), period.Quarterly, usgDT(2022, 7, 30), usgDT(2022, 4, 30)},
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:88
		{"anniversary_period_boundaries_spec.rb:88", "quarterly month end is not dragged by a clamped month", usgDT(2022, 1, 31), period.Quarterly, usgDT(2022, 8, 15), usgDT(2022, 7, 31)},
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:106
		{"anniversary_period_boundaries_spec.rb:106", "quarterly on the 29th resolves from the subscription day", usgDT(2022, 1, 29), period.Quarterly, usgDT(2022, 2, 28), usgDT(2022, 1, 29)},
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:116
		{"anniversary_period_boundaries_spec.rb:116", "semiannual month end ignores other month ends", usgDT(2021, 1, 31), period.Semiannual, usgDT(2021, 4, 30), usgDT(2021, 1, 31)},
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:126
		{"anniversary_period_boundaries_spec.rb:126", "quarterly 30-day month end ignores other month ends", usgDT(2021, 4, 30), period.Quarterly, usgDT(2021, 5, 31), usgDT(2021, 4, 30)},
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:132
		{"anniversary_period_boundaries_spec.rb:132", "quarterly 30-day month end keeps the day in a longer month", usgDT(2021, 4, 30), period.Quarterly, usgDT(2021, 8, 15), usgDT(2021, 7, 30)},
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:149
		{"anniversary_period_boundaries_spec.rb:149", "semiannual 30-day month end ignores other month ends", usgDT(2021, 4, 30), period.Semiannual, usgDT(2021, 6, 30), usgDT(2021, 4, 30)},
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:154
		{"anniversary_period_boundaries_spec.rb:154", "semiannual 30-day month end keeps the day in a longer month", usgDT(2021, 4, 30), period.Semiannual, usgDT(2021, 11, 15), usgDT(2021, 10, 30)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sub, _ := subscribeOn(t, tc.start, tc.interval)
			from, _ := periodOn(s, sub, tc.on)
			eqTime(t, tc.source+" from", from, tc.from)
		})
	}
	for _, tc := range []struct {
		name                   string
		start                  time.Time
		closingOn, openingOn   time.Time
		closingTo, openingFrom time.Time
	}{
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:92
		{"quarterly month end periods meet", usgDT(2022, 1, 31), usgDT(2022, 4, 15), usgDT(2022, 5, 15), usgDT(2022, 4, 29, 23, 59, 59), usgDT(2022, 4, 30)},
		// source: spec/scenarios/current_usage/anniversary_period_boundaries_spec.rb:136
		{"quarterly 30-day month end periods meet", usgDT(2021, 4, 30), usgDT(2021, 7, 29), usgDT(2021, 7, 30), usgDT(2021, 7, 29, 23, 59, 59), usgDT(2021, 7, 30)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sub, _ := subscribeOn(t, tc.start, period.Quarterly)
			_, to := periodOn(s, sub, tc.closingOn)
			from, _ := periodOn(s, sub, tc.openingOn)
			eqTime(t, "closing to", to, tc.closingTo)
			eqTime(t, "opening from", from, tc.openingFrom)
		})
	}
}

func TestUsageScenarioIdenticalChargeFilters(t *testing.T) {
	// newFiltered creates the lago scenario: a sum metric on value with a
	// cloud filter (aws, gcp), a standard charge of 10 with the filters, a
	// subscription on 2024-03-05; the clock is moved to 2024-03-06.
	newFiltered := func(t *testing.T, fs ...catalog.ChargeFilter) (*scenario, *subscription.Subscription, *catalog.BillableMetric, *catalog.Charge) {
		s := newScenario(t, usgDT(2024, 3, 5))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeSum, "value", usgMetricFilter("cloud", "aws", "gcp"))
		p := s.usgMonthly(0)
		ch := s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`, usgFilters(fs...))
		sub := s.usgSub(c, p)
		s.travelTo(usgDT(2024, 3, 6))
		return s, sub, m, ch
	}
	named := func(name string, values map[string][]string, props string, created time.Time) catalog.ChargeFilter {
		f := billingtest.ChargeFilter(values, props)
		f.InvoiceDisplayName = &name
		f.CreatedAt = created
		return f
	}
	type bucket struct {
		units  string
		events int64
		amount int64
	}
	check := func(t *testing.T, cu usgCharge, what string, id core.ChargeFilterID, want bucket) {
		t.Helper()
		fu, ok := cu.filter(id)
		if !ok {
			t.Fatalf("no bucket for %s", what)
		}
		usgUnits(t, what, fu.units, want.units)
		usgEq(t, what+" events_count", fu.events, want.events)
		usgEq(t, what+" amount_cents", fu.amount, want.amount)
	}
	// source: spec/scenarios/current_usage/with_identical_charge_filters_spec.rb:38
	t.Run("filters without values", func(t *testing.T) {
		t.Skip("invalid by design: catalog.Charge.Validate rejects a charge filter without values (filters[i].values required), so tanvo never stores one")
	})
	// source: spec/scenarios/current_usage/with_identical_charge_filters_spec.rb:85
	t.Run("subset filter counts in its most specific bucket", func(t *testing.T) {
		now := usgDT(2024, 3, 5)
		s, sub, m, ch := newFiltered(t,
			named("All clouds", map[string][]string{"cloud": {"aws", "gcp"}}, `{"amount":"5"}`, now),
			named("AWS only", map[string][]string{"cloud": {"aws"}}, `{"amount":"3"}`, now),
		)
		s.usgEvent(sub, m.Code, "cloud", "aws", "value", "10")
		s.usgEvent(sub, m.Code, "cloud", "gcp", "value", "7")
		cu := s.usgCurrent(sub).first(t)
		usgEq(t, "filters.count", len(cu.fees), 3)
		check(t, cu, "All clouds", ch.Filters[0].ID, bucket{"7", 1, 3500})
		check(t, cu, "AWS only", ch.Filters[1].ID, bucket{"10", 1, 3000})
	})
	// source: spec/scenarios/current_usage/with_identical_charge_filters_spec.rb:149
	t.Run("identical filters count in the oldest duplicate", func(t *testing.T) {
		t.Skip("invalid by design: catalog.Charge.Validate rejects two filters with equal values (filters[i].values duplicated), so tanvo never stores duplicates")
	})
}

func TestUsageScenarioZeroFees(t *testing.T) {
	// zeroCharge asserts lago expect_zero_charge_usage: no units, events or
	// amount, no pricing unit and no grouped usage.
	zeroCharge := func(t *testing.T, what string, cu usgCharge) {
		t.Helper()
		usgUnits(t, what, cu.units, "0")
		usgEq(t, what+" events_count", cu.events, 0)
		usgEq(t, what+" amount_cents", cu.amount, 0)
		if _, _, _, ok := cu.pricingUnits(); ok {
			t.Errorf("%s pricing_unit_details = present, want = nil", what)
		}
		for _, f := range cu.fees {
			if len(f.GroupedBy) > 0 {
				t.Errorf("%s grouped_usage = %v, want = []", what, f.GroupedBy)
			}
		}
	}
	// zeroUsage asserts lago expect_zero_customer_usage.
	zeroUsage := func(t *testing.T, u *usgUsage) {
		t.Helper()
		usgEq(t, "amount_cents", u.Amount, 0)
		usgEq(t, "total_amount_cents", u.TotalAmount, 0)
		usgEq(t, "taxes_amount_cents", u.TaxesAmount, 0)
		if u.From.IsZero() || u.To.IsZero() || u.IssuingDate.IsZero() {
			t.Errorf("from, to, issuing date = %s, %s, %s, want = present", u.From, u.To, u.IssuingDate)
		}
	}
	// noFilters asserts lago filters: [].
	noFilters := func(t *testing.T, cu usgCharge) {
		t.Helper()
		for _, f := range cu.fees {
			if !f.ChargeFilterID.IsZero() {
				t.Errorf("filters = [%s], want = []", f.ChargeFilterID)
			}
		}
	}
	for _, tc := range []struct {
		source       string
		model        catalog.ChargeModel
		agg          catalog.AggregationType
		field, props string
	}{
		// source: spec/scenarios/current_usage/zero_fees_spec.rb:63
		{"zero_fees_spec.rb:63", catalog.ChargeModelStandard, catalog.AggregationTypeCount, "", `{"amount":"20"}`},
		// source: spec/scenarios/current_usage/zero_fees_spec.rb:91
		{"zero_fees_spec.rb:91", catalog.ChargeModelGraduated, catalog.AggregationTypeCount, "", `{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"2","flat_amount":"0"},{"from_value":11,"to_value":null,"per_unit_amount":"1","flat_amount":"50"}]}`},
		// source: spec/scenarios/current_usage/zero_fees_spec.rb:114
		{"zero_fees_spec.rb:114", catalog.ChargeModelPackage, catalog.AggregationTypeCount, "", `{"amount":"100","free_units":10,"package_size":10}`},
		// source: spec/scenarios/current_usage/zero_fees_spec.rb:139
		{"zero_fees_spec.rb:139", catalog.ChargeModelPercentage, catalog.AggregationTypeSum, "value", `{"rate":"0.05","fixed_amount":"2"}`},
		// source: spec/scenarios/current_usage/zero_fees_spec.rb:167
		{"zero_fees_spec.rb:167", catalog.ChargeModelVolume, catalog.AggregationTypeCount, "", `{"volume_ranges":[{"from_value":0,"to_value":100,"per_unit_amount":"2","flat_amount":"0"},{"from_value":101,"to_value":null,"per_unit_amount":"1","flat_amount":"0"}]}`},
		// source: spec/scenarios/current_usage/zero_fees_spec.rb:195
		{"zero_fees_spec.rb:195", catalog.ChargeModelGraduatedPercentage, catalog.AggregationTypeCount, "", `{"graduated_percentage_ranges":[{"from_value":0,"to_value":10,"rate":"1","flat_amount":"0"},{"from_value":11,"to_value":null,"rate":"0.5","flat_amount":"50"}]}`},
	} {
		t.Run(string(tc.model), func(t *testing.T) {
			s, sub, _, ch := usgModel(t, tc.agg, tc.field, tc.model, tc.props)
			u := s.usgCurrent(sub)
			zeroUsage(t, u)
			usgEq(t, tc.source+" charges_usage.count", len(u.charges), 1)
			cu := u.first(t)
			zeroCharge(t, tc.source, cu)
			usgEq(t, tc.source+" charge", cu.charge, ch.ID)
			noFilters(t, cu)
		})
	}
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:226
	t.Run("multiple charge models", func(t *testing.T) {
		s := newScenario(t, usgDT(2024, 3, 5))
		c := s.Customer()
		count := s.Metric("bm", catalog.AggregationTypeCount, "")
		sum := s.Metric("sum", catalog.AggregationTypeSum, "value")
		p := s.usgMonthly(0)
		std := s.Charge(p, count, catalog.ChargeModelStandard, `{"amount":"20"}`)
		grad := s.Charge(p, sum, catalog.ChargeModelGraduated, `{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"2","flat_amount":"0"},{"from_value":11,"to_value":null,"per_unit_amount":"1","flat_amount":"50"}]}`)
		sub := s.usgSub(c, p)
		u := s.usgCurrent(sub)
		zeroUsage(t, u)
		usgEq(t, "charges_usage.count", len(u.charges), 2)
		for _, id := range []core.ChargeID{std.ID, grad.ID} {
			cu := u.charge(t, id)
			zeroCharge(t, "charge", cu)
			noFilters(t, cu)
		}
	})
	grouped := func(t *testing.T) (*scenario, *subscription.Subscription, *catalog.BillableMetric) {
		s, sub, m, _ := usgModel(t, catalog.AggregationTypeCount, "", catalog.ChargeModelStandard, `{"amount":"20","pricing_group_keys":["region"]}`)
		return s, sub, m
	}
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:258
	t.Run("pricing group keys without usage", func(t *testing.T) {
		s, sub, _ := grouped(t)
		u := s.usgCurrent(sub)
		zeroUsage(t, u)
		usgEq(t, "charges_usage.count", len(u.charges), 1)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "0")
		usgEq(t, "events_count", cu.events, 0)
		usgEq(t, "amount_cents", cu.amount, 0)
		noFilters(t, cu)
		// lago returns one zero group {region: nil}; tanvo's zero fee has
		// no grouping values.
		usgEq(t, "grouped_usage.count", len(cu.fees), 1)
		usgEq(t, "grouped_by region", cu.fees[0].GroupedBy["region"], "")
		usgEq(t, "group events_count", cu.fees[0].EventsCount, 0)
		usgEq(t, "group amount_cents", cu.fees[0].Amount, 0)
	})
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:287
	t.Run("pricing group keys with one group", func(t *testing.T) {
		s, sub, m := grouped(t)
		s.usgEvent(sub, m.Code, "region", "europe")
		u := s.usgCurrent(sub)
		usgEq(t, "amount_cents", u.Amount, 2000)
		usgEq(t, "charges_usage.count", len(u.charges), 1)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "1")
		usgEq(t, "amount_cents", cu.amount, 2000)
		usgEq(t, "grouped_usage.count", len(cu.fees), 1)
		gu, ok := cu.group("region", "europe")
		if !ok {
			t.Fatalf("no group europe")
		}
		usgUnits(t, "europe", gu.units, "1")
		usgEq(t, "europe events_count", gu.events, 1)
		usgEq(t, "europe amount_cents", gu.amount, 2000)
		noFilters(t, gu)
	})
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:321
	t.Run("pricing group keys with two groups", func(t *testing.T) {
		s, sub, m := grouped(t)
		s.usgEvent(sub, m.Code, "region", "europe")
		s.usgEvent(sub, m.Code, "region", "usa")
		u := s.usgCurrent(sub)
		usgEq(t, "amount_cents", u.Amount, 4000)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "2")
		usgEq(t, "amount_cents", cu.amount, 4000)
		usgEq(t, "grouped_usage.count", len(cu.fees), 2)
		for _, r := range []string{"europe", "usa"} {
			gu, ok := cu.group("region", r)
			if !ok {
				t.Fatalf("no group %s", r)
			}
			usgUnits(t, r, gu.units, "1")
			usgEq(t, r+" events_count", gu.events, 1)
			usgEq(t, r+" amount_cents", gu.amount, 2000)
		}
	})
	// regions creates the lago "with charge filters" scenario: region
	// (europe, usa) filters of 20 and 50 on a standard charge of 10.
	regions := func(t *testing.T, agg catalog.AggregationType, field string) (*scenario, *subscription.Subscription, *catalog.BillableMetric, *catalog.Charge) {
		s := newScenario(t, usgDT(2024, 3, 5))
		c := s.Customer()
		mopts := []func(*catalog.BillableMetric){usgMetricFilter("region", "europe", "usa")}
		if agg == catalog.AggregationTypeUniqueCount {
			mopts = append(mopts, usgRecurring)
		}
		m := s.Metric("bm", agg, field, mopts...)
		p := s.usgMonthly(0)
		ch := s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`, usgFilters(
			billingtest.ChargeFilter(map[string][]string{"region": {"europe"}}, `{"amount":"20"}`),
			billingtest.ChargeFilter(map[string][]string{"region": {"usa"}}, `{"amount":"50"}`),
		))
		return s, s.usgSub(c, p), m, ch
	}
	// bucket is the expected usage of a filter; events -1 is not checked
	// (lago does not assert it).
	type bucket struct {
		units  string
		events int64
		amount int64
	}
	buckets := func(t *testing.T, cu usgCharge, ch *catalog.Charge, europe, usa, catchAll bucket) {
		t.Helper()
		usgEq(t, "filters.count", len(cu.fees), 3)
		for _, b := range []struct {
			name string
			id   core.ChargeFilterID
			want bucket
		}{{"europe", ch.Filters[0].ID, europe}, {"usa", ch.Filters[1].ID, usa}, {"catch-all", core.ChargeFilterID{}, catchAll}} {
			fu, ok := cu.filter(b.id)
			if !ok {
				t.Errorf("no %s bucket", b.name)
				continue
			}
			usgUnits(t, b.name, fu.units, b.want.units)
			if b.want.events >= 0 {
				usgEq(t, b.name+" events_count", fu.events, b.want.events)
			}
			usgEq(t, b.name+" amount_cents", fu.amount, b.want.amount)
		}
	}
	zero := bucket{"0", 0, 0}
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:377
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:487
	t.Run("charge filters without usage", func(t *testing.T) {
		s, sub, _, ch := regions(t, catalog.AggregationTypeCount, "")
		u := s.usgCurrent(sub)
		zeroUsage(t, u)
		usgEq(t, "charges_usage.count", len(u.charges), 1)
		cu := u.first(t)
		zeroCharge(t, "charge", cu)
		buckets(t, cu, ch, zero, zero, zero)
		for _, f := range cu.fees {
			if f.ChargeFilterID.IsZero() && f.InvoiceDisplayName != nil {
				t.Errorf("catch-all invoice_display_name = %q, want = nil", *f.InvoiceDisplayName)
			}
		}
	})
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:406
	t.Run("charge filters with one filter used", func(t *testing.T) {
		s, sub, m, ch := regions(t, catalog.AggregationTypeCount, "")
		s.usgEvent(sub, m.Code, "region", "europe")
		u := s.usgCurrent(sub)
		usgEq(t, "amount_cents", u.Amount, 2000)
		usgEq(t, "total_amount_cents", u.TotalAmount, 2000)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "1")
		usgEq(t, "events_count", cu.events, 1)
		usgEq(t, "amount_cents", cu.amount, 2000)
		buckets(t, cu, ch, bucket{"1", 1, 2000}, zero, zero)
	})
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:449
	t.Run("charge filters fetched twice", func(t *testing.T) {
		s, sub, _, _ := regions(t, catalog.AggregationTypeCount, "")
		first, second := s.usgCurrent(sub), s.usgCurrent(sub)
		usgEq(t, "amount_cents", second.Amount, first.Amount)
		usgEq(t, "total_amount_cents", second.TotalAmount, first.TotalAmount)
		usgEq(t, "charges_usage.count", len(second.charges), len(first.charges))
		a, b := first.first(t).fees, second.first(t).fees
		usgEq(t, "filters.count", len(b), len(a))
		for i := range min(len(a), len(b)) {
			usgUnits(t, "filter", b[i].Units, a[i].Units.String())
			usgEq(t, "filter events_count", b[i].EventsCount, a[i].EventsCount)
			usgEq(t, "filter amount_cents", b[i].Amount, a[i].Amount)
			usgEq(t, "filter values", b[i].ChargeFilterID, a[i].ChargeFilterID)
		}
	})
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:507
	t.Run("charge filters pre-filtering with some usage", func(t *testing.T) {
		s, sub, m, ch := regions(t, catalog.AggregationTypeCount, "")
		s.usgEvent(sub, m.Code, "region", "europe")
		s.usgEvent(sub, m.Code, "region", "unknown")
		u := s.usgCurrent(sub)
		usgEq(t, "amount_cents", u.Amount, 3000)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "2")
		usgEq(t, "events_count", cu.events, 2)
		usgEq(t, "amount_cents", cu.amount, 3000)
		buckets(t, cu, ch, bucket{"1", 1, 2000}, zero, bucket{"1", 1, 1000})
	})
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:556
	t.Run("charge filters on a recurring metric", func(t *testing.T) {
		s, sub, m, ch := regions(t, catalog.AggregationTypeUniqueCount, "item_id")
		s.usgEvent(sub, m.Code, "region", "europe", "item_id", "item_1")
		s.travelTo(usgDT(2024, 4, 10))
		u := s.usgCurrent(sub)
		usgEq(t, "amount_cents", u.Amount, 2000)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "1")
		usgEq(t, "amount_cents", cu.amount, 2000)
		buckets(t, cu, ch, bucket{"1", -1, 2000}, zero, zero)
	})
	for _, tc := range []struct {
		source       string
		name         string
		model        catalog.ChargeModel
		agg          catalog.AggregationType
		field, props string
		value        string
		units        string
	}{
		// source: spec/scenarios/current_usage/zero_fees_spec.rb:598
		{"zero_fees_spec.rb:598", "standard amount 0", catalog.ChargeModelStandard, catalog.AggregationTypeCount, "", `{"amount":"0"}`, "", "3"},
		// source: spec/scenarios/current_usage/zero_fees_spec.rb:635
		{"zero_fees_spec.rb:635", "package within free units", catalog.ChargeModelPackage, catalog.AggregationTypeCount, "", `{"amount":"100","free_units":50,"package_size":10}`, "", "3"},
		// source: spec/scenarios/current_usage/zero_fees_spec.rb:669
		{"zero_fees_spec.rb:669", "sum of zero values", catalog.ChargeModelStandard, catalog.AggregationTypeSum, "value", `{"amount":"10"}`, "0", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sub, m, ch := usgModel(t, tc.agg, tc.field, tc.model, tc.props)
			for range 3 {
				if tc.field != "" {
					s.usgEvent(sub, m.Code, tc.field, tc.value)
				} else {
					s.usgEvent(sub, m.Code)
				}
			}
			u := s.usgCurrent(sub)
			usgEq(t, tc.source+" amount_cents", u.Amount, 0)
			usgEq(t, tc.source+" charges_usage.count", len(u.charges), 1)
			cu := u.first(t)
			usgUnits(t, tc.source, cu.units, tc.units)
			usgEq(t, tc.source+" events_count", cu.events, 3)
			usgEq(t, tc.source+" charge amount_cents", cu.amount, 0)
			usgEq(t, tc.source+" charge", cu.charge, ch.ID)
			noFilters(t, cu)
		})
	}
	// source: spec/scenarios/current_usage/zero_fees_spec.rb:711
	t.Run("filters with a zero amount", func(t *testing.T) {
		s := newScenario(t, usgDT(2024, 3, 5))
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeSum, "hours", usgMetricFilter("cloud", "aws", "gcp"))
		p := s.usgMonthly(0)
		ch := s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"0.12"}`, usgFilters(
			billingtest.ChargeFilter(map[string][]string{"cloud": {"aws"}}, `{"amount":"0"}`),
			billingtest.ChargeFilter(map[string][]string{"cloud": {"gcp"}}, `{"amount":"0.10"}`),
		))
		sub := s.usgSub(c, p)
		s.usgEvent(sub, m.Code, "cloud", "aws", "hours", "100")
		s.usgEvent(sub, m.Code, "cloud", "gcp", "hours", "200")
		u := s.usgCurrent(sub)
		usgEq(t, "amount_cents", u.Amount, 2000)
		cu := u.first(t)
		usgUnits(t, "charge", cu.units, "300")
		usgEq(t, "events_count", cu.events, 2)
		usgEq(t, "amount_cents", cu.amount, 2000)
		// europe, usa and catch-all read aws, gcp and catch-all here.
		buckets(t, cu, ch, bucket{"100", 1, 0}, bucket{"200", 1, 2000}, zero)
	})
}

func TestUsageScenarioLifetimeWithoutProgressiveBilling(t *testing.T) {
	// start creates the lago scenario at time0 2022-12-01: a monthly plan
	// of 31_00 with a standard charge of 0.01 on a sum of total, and a
	// subscription.
	start := func(t *testing.T) (*scenario, *subscription.Subscription, *catalog.BillableMetric, *catalog.Plan, *usgLifetimeRef) {
		time0 := usgDT(2022, 12, 1)
		s := newScenario(t, time0)
		c := s.Customer()
		m := s.Metric("bm", catalog.AggregationTypeSum, "total")
		p := s.usgMonthly(31_00)
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"0.01"}`)
		return s, s.usgSub(c, p), m, p, &usgLifetimeRef{id: c.ID, time0: time0}
	}
	// firstPeriod runs the part both lago examples share: two events of
	// 1_000_000 on days 4 and 5 and the first invoice.
	firstPeriod := func(t *testing.T, s *scenario, sub *subscription.Subscription, m *catalog.BillableMetric, c *usgLifetimeRef) {
		t.Helper()
		s.usgPassTime(c.time0, &c.passed, 4)
		s.usgEvent(sub, m.Code, "total", "1000000")
		usgEq(t, "current_usage_amount_cents", s.usgLifetime(sub).CurrentUsageAmount, 1_000_000)
		usgEq(t, "Invoice.count", len(s.Invoices(c.id)), 0)
		s.usgPassTime(c.time0, &c.passed, 1)
		s.usgEvent(sub, m.Code, "total", "1000000")
		usgEq(t, "current_usage_amount_cents", s.usgLifetime(sub).CurrentUsageAmount, 2_000_000)
		usgEq(t, "Invoice.count", len(s.Invoices(c.id)), 0)
		s.usgPassTime(c.time0, &c.passed, 29)
		invs := s.Invoices(c.id)
		usgEq(t, "Invoice.count", len(invs), 1)
		if len(invs) == 1 {
			usgEq(t, "total_amount_cents", invs[0].TotalAmount, 31_00+2_000_000)
			usgEq(t, "invoice_type", invs[0].Type, invoice.TypeSubscription)
		}
	}
	// source: spec/scenarios/lifetime_usages/without_progressive_billing_spec.rb:18
	t.Run("lifetime usage without invoices", func(t *testing.T) {
		s, sub, m, _, c := start(t)
		firstPeriod(t, s, sub, m, c)
	})
	// source: spec/scenarios/lifetime_usages/without_progressive_billing_spec.rb:54
	t.Run("threshold added after the usage was invoiced", func(t *testing.T) {
		s, sub, m, p, c := start(t)
		firstPeriod(t, s, sub, m, c)
		s.usgPassTime(c.time0, &c.passed, 1)
		s.UsageThreshold(p, 1000, false)
		s.usgPassTime(c.time0, &c.passed, 2)
		usgEq(t, "Invoice.count", len(s.Invoices(c.id)), 1)
		s.usgEvent(sub, m.Code, "total", "1000000")
		lu := s.usgLifetime(sub)
		usgEq(t, "current_usage_amount_cents", lu.CurrentUsageAmount, 1_000_000)
		usgEq(t, "invoiced_usage_amount_cents", lu.InvoicedUsageAmount, 2_000_000)
		usgEq(t, "Invoice.count", len(s.Invoices(c.id)), 1)
	})
}

// usgLifetimeRef carries the customer and the time travel state of the
// lifetime usage scenarios.
type usgLifetimeRef struct {
	id     core.CustomerID
	time0  time.Time
	passed int
}
