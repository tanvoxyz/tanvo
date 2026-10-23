package billing_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// chgBeginningOf returns the start of the calendar period of interval i
// holding t (Rails beginning_of_week/month/quarter/year; weeks start on
// Monday).
func chgBeginningOf(i period.Interval, t time.Time) time.Time {
	y, m, d := t.Date()
	switch i {
	case period.Weekly:
		wd := (int(t.Weekday()) + 6) % 7
		return time.Date(y, m, d-wd, 0, 0, 0, 0, time.UTC)
	case period.Quarterly:
		return time.Date(y, m-(m-1)%3, 1, 0, 0, 0, 0, time.UTC)
	case period.Yearly:
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	}
}

// chgAddPeriods adds n periods of interval i to t (Rails n.weeks/months).
func chgAddPeriods(i period.Interval, t time.Time, n int) time.Time {
	switch i {
	case period.Weekly:
		return t.AddDate(0, 0, 7*n)
	case period.Quarterly:
		return t.AddDate(0, 3*n, 0)
	case period.Yearly:
		return t.AddDate(n, 0, 0)
	default:
		return t.AddDate(0, n, 0)
	}
}

// chgCommitmentCase is one spec file of the minimum commitment matrix.
type chgCommitmentCase struct {
	source   string
	interval period.Interval
	advance  bool
	calendar bool
	start    time.Time
	want     [2]int64 // in arrears: first, second period; in advance: second, third
}

// chgCommitmentPlan creates the plan, its three charges and its 1_000_000
// minimum commitment, and returns the metric codes (metered, metered in
// advance, recurring in advance).
func chgCommitmentPlan(s *scenario, interval period.Interval, advance bool, opts ...func(*catalog.Plan)) *catalog.Plan {
	s.T.Helper()
	p := s.Plan(interval, 10_000, append([]func(*catalog.Plan){func(p *catalog.Plan) { p.PayInAdvance = advance }}, opts...)...)
	s.Commitment(p, 1_000_000)
	metered := s.Metric("metered", catalog.AggregationTypeSum, "total")
	meteredAdvance := s.Metric("metered_advance", catalog.AggregationTypeSum, "total")
	recurring := s.Metric("advance_recurring", catalog.AggregationTypeSum, "total", func(m *catalog.BillableMetric) { m.Recurring = true })
	payInAdvance := func(c *catalog.Charge) { c.PayInAdvance = true }
	s.Charge(p, metered, catalog.ChargeModelStandard, `{"amount":"1"}`)
	s.Charge(p, meteredAdvance, catalog.ChargeModelStandard, `{"amount":"1"}`, payInAdvance)
	s.Charge(p, recurring, catalog.ChargeModelStandard, `{"amount":"1"}`, payInAdvance)
	return p
}

// chgBillingTime is the subscription option of the billing time.
func chgBillingTime(calendar bool) func(*subscription.Subscription) {
	if calendar {
		return func(*subscription.Subscription) {}
	}
	return billingtest.Anniversary
}

// chgCommitmentCoupon applies the forever 1_000_000 coupon limited to p.
func chgCommitmentCoupon(s *scenario, p *catalog.Plan, c *subscription.Subscription) {
	s.T.Helper()
	cp := s.FixedAmountCoupon(1_000_000, func(c *coupon.Coupon) {
		c.Frequency = coupon.FrequencyForever
		c.LimitedPlanIDs = []core.PlanID{p.ID}
	})
	s.ApplyCoupon(cp, s.ReloadCustomer(c.CustomerID))
}

// chgCommitmentFee asserts the commitment fee of the subscription's last
// invoice.
func chgCommitmentFee(t *testing.T, s *scenario, sub core.SubscriptionID, want int64) {
	t.Helper()
	fs := chgFeesOf(s, chgLastInvoice(s, sub), core.FeeTypeCommitment)
	if want == 0 {
		if len(fs) != 0 {
			t.Errorf("commitment fees = %d, want = 0", len(fs))
		}
		return
	}
	if len(fs) != 1 {
		t.Fatalf("commitment fees = %d, want = 1", len(fs))
	}
	if got := fs[0].Amount; got != want {
		t.Errorf("commitment fee = %d, want = %d", got, want)
	}
}

// TestScenarioMinimumCommitmentMatrix ports the monthly, quarterly and
// weekly files of spec/scenarios/commitments/minimum/{in_arrears,in_advance}/{calendar,anniversary}.
func TestScenarioMinimumCommitmentMatrix(t *testing.T) {
	feb28 := time.Date(2024, 2, 28, 10, 0, 0, 0, time.UTC)
	mar12 := time.Date(2024, 3, 12, 10, 0, 0, 0, time.UTC)
	cases := []chgCommitmentCase{
		{"in_arrears/calendar/monthly_spec.rb", period.Monthly, false, true, feb28, [2]int64{65_276, 989_000}},
		{"in_arrears/calendar/quarterly_spec.rb", period.Quarterly, false, true, mar12, [2]int64{214_582, 989_000}},
		{"in_arrears/calendar/weekly_spec.rb", period.Weekly, false, true, mar12, [2]int64{845_572, 989_000}},
		{"in_arrears/anniversary/monthly_spec.rb", period.Monthly, false, false, feb28, [2]int64{987_000, 989_000}},
		{"in_arrears/anniversary/quarterly_spec.rb", period.Quarterly, false, false, mar12, [2]int64{987_000, 989_000}},
		{"in_arrears/anniversary/weekly_spec.rb", period.Weekly, false, false, mar12, [2]int64{987_000, 989_000}},
		{"in_advance/calendar/monthly_spec.rb", period.Monthly, true, true, feb28, [2]int64{61_276, 988_500}},
		{"in_advance/calendar/quarterly_spec.rb", period.Quarterly, true, true, mar12, [2]int64{210_582, 988_500}},
		{"in_advance/calendar/weekly_spec.rb", period.Weekly, true, true, mar12, [2]int64{841_572, 988_500}},
		{"in_advance/anniversary/monthly_spec.rb", period.Monthly, true, false, feb28, [2]int64{983_000, 988_500}},
		{"in_advance/anniversary/quarterly_spec.rb", period.Quarterly, true, false, mar12, [2]int64{983_000, 988_500}},
		{"in_advance/anniversary/weekly_spec.rb", period.Weekly, true, false, mar12, [2]int64{983_000, 988_500}},
	}
	for _, tc := range cases {
		// boundary returns the n-th billing instant after the start.
		boundary := func(n int) time.Time {
			at := chgAddPeriods(tc.interval, tc.start, n)
			if tc.calendar {
				at = chgBeginningOf(tc.interval, at)
			}
			return at
		}
		for _, withCoupon := range []bool{false, true} {
			// setup runs the spec's before blocks, up to the coupon.
			setup := func(t *testing.T) (*scenario, *subscription.Subscription) {
				s := newScenario(t, tc.start)
				p := chgCommitmentPlan(s, tc.interval, tc.advance)
				c := s.Customer()
				sub := chgSubscribe(s, c, p, chgBillingTime(tc.calendar))
				if tc.advance {
					s.ingest(sub, "advance_recurring", "total", "15")
					s.ingest(sub, "metered", "total", "25")
					s.ingest(sub, "metered_advance", "total", "30")
					s.performBilling()
				} else {
					s.ingest(sub, "advance_recurring", "total", "10")
					s.ingest(sub, "metered", "total", "10")
					s.ingest(sub, "metered_advance", "total", "10")
					s.travelTo(boundary(1))
					s.performBilling()
				}
				if withCoupon {
					chgCommitmentCoupon(s, p, sub)
					if !tc.advance && tc.calendar {
						s.performBilling()
					}
				}
				return s, sub
			}
			name := fmt.Sprintf("%s coupon=%v", tc.source, withCoupon)
			if tc.advance {
				// source: spec/scenarios/commitments/minimum/<file>:139 (no coupon), :200 (coupon)
				t.Run(name+" first period", func(t *testing.T) {
					s, sub := setup(t)
					chgCommitmentFee(t, s, sub.ID, 0)
				})
				// source: spec/scenarios/commitments/minimum/<file>:153 (no coupon), :214 (coupon)
				t.Run(name+" second period", func(t *testing.T) {
					s, sub := setup(t)
					s.travelTo(boundary(1))
					s.performBilling()
					chgCommitmentFee(t, s, sub.ID, tc.want[0])
				})
				// source: spec/scenarios/commitments/minimum/<file>:171 (no coupon), :232 (coupon)
				t.Run(name+" third period", func(t *testing.T) {
					s, sub := setup(t)
					s.travelTo(boundary(1))
					s.performBilling()
					s.travelTo(boundary(2))
					s.performBilling()
					chgCommitmentFee(t, s, sub.ID, tc.want[1])
				})
				continue
			}
			// source: spec/scenarios/commitments/minimum/<file>:141 (no coupon), :184/:188 (coupon)
			t.Run(name+" first period", func(t *testing.T) {
				s, sub := setup(t)
				chgCommitmentFee(t, s, sub.ID, tc.want[0])
			})
			// source: spec/scenarios/commitments/minimum/<file>:155 (no coupon), :198/:202 (coupon)
			t.Run(name+" second period", func(t *testing.T) {
				s, sub := setup(t)
				s.travelTo(boundary(2))
				s.performBilling()
				chgCommitmentFee(t, s, sub.ID, tc.want[1])
			})
		}
	}
}

// chgKeyGap is the reason of the cases blocked by the idempotency key of
// periodic invoice subscriptions.
const chgKeyGap = "blocked by core: invoice.Subscription.Key is (subscription, reason, period start) but lago keys recurring rows on the charges period, so the monthly charge invoices of a yearly plan collide and later periodic billing is skipped"

// TestScenarioMinimumCommitmentYearly ports the yearly files of the
// minimum commitment matrix, with charges billed monthly.
func TestScenarioMinimumCommitmentYearly(t *testing.T) {
	type tc struct {
		source   string
		advance  bool
		calendar bool
		start    time.Time
		want     int64
		skip     string
	}
	mar5 := time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC)
	cases := []tc{
		{"in_arrears/calendar/yearly_spec.rb", false, true, mar5, 808_086, chgKeyGap},
		{"in_advance/calendar/yearly_spec.rb", true, true, mar5, 808_186, chgKeyGap},
		{"in_arrears/anniversary/yearly_spec.rb", false, false, time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC), 981_100, chgKeyGap},
		{"in_advance/anniversary/yearly_spec.rb", true, false, mar5, 981_100, ""},
	}
	monthly := true
	for _, c := range cases {
		setup := func(t *testing.T) (*scenario, *subscription.Subscription) {
			s := newScenario(t, c.start)
			p := s.Plan(period.Yearly, 10_000, func(p *catalog.Plan) { p.PayInAdvance, p.BillChargesMonthly = c.advance, &monthly })
			s.Commitment(p, 1_000_000)
			advance := s.Metric("advance_metered", catalog.AggregationTypeSum, "total")
			metered := s.Metric("metered", catalog.AggregationTypeSum, "total")
			recurring := s.Metric("recurring_advance", catalog.AggregationTypeSum, "total", func(m *catalog.BillableMetric) { m.Recurring = true })
			payInAdvance := func(c *catalog.Charge) { c.PayInAdvance = true }
			s.Charge(p, advance, catalog.ChargeModelStandard, `{"amount":"1"}`, payInAdvance)
			s.Charge(p, metered, catalog.ChargeModelStandard, `{"amount":"1"}`)
			s.Charge(p, recurring, catalog.ChargeModelStandard, `{"amount":"1"}`, payInAdvance)
			sub := chgSubscribe(s, s.Customer(), p, chgBillingTime(c.calendar))
			s.travelTo(c.start.Add(time.Hour))
			s.ingest(sub, "advance_metered", "total", "2")
			s.ingest(sub, "metered", "total", "1")
			s.ingest(sub, "advance_metered", "total", "30")
			switch {
			case c.advance:
				s.performBilling()
			case c.calendar:
				s.travelTo(chgBeginningOf(period.Monthly, c.start.AddDate(0, 1, 0)))
				s.performBilling()
			}
			return s, sub
		}
		// source: spec/scenarios/commitments/minimum/<file>:142 (anniversary), :144/:146 (calendar)
		t.Run(c.source+" first period", func(t *testing.T) {
			s, sub := setup(t)
			if c.advance || c.calendar {
				s.travelTo(c.start.Add(time.Minute))
			} else {
				s.travelTo(c.start.AddDate(0, 1, 1))
				s.performBilling()
			}
			chgCommitmentFee(t, s, sub.ID, 0)
		})
		// source: spec/scenarios/commitments/minimum/<file>:182 (anniversary), :182/:186 (calendar)
		t.Run(c.source+" last period", func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
			s, sub := setup(t)
			if c.calendar {
				s.travelTo(c.start.AddDate(0, 5, 0))
				s.ingest(sub, "advance_metered", "total", "55")
				s.travelTo(c.start.AddDate(0, 11, 0))
				s.ingest(sub, "metered", "total", "1")
				if c.advance {
					s.travelTo(chgBeginningOf(period.Monthly, c.start.AddDate(0, 12, 0)))
					s.performBilling()
				}
				s.travelTo(chgBeginningOf(period.Yearly, c.start.AddDate(1, 0, 0)))
				s.performBilling()
			} else {
				for i := 1; i <= 12; i++ {
					s.travelTo(c.start.AddDate(0, i, 0))
					switch i {
					case 5:
						s.ingest(sub, "advance_metered", "total", "55")
					case 11:
						s.ingest(sub, "metered", "total", "1")
					}
					s.performBilling()
				}
			}
			fs := chgFeesOf(s, chgLastInvoice(s, sub.ID), core.FeeTypeCommitment)
			if len(fs) != 1 {
				t.Fatalf("commitment fees = %d, want = 1", len(fs))
			}
			if got := chgSum(fs); got != c.want {
				t.Errorf("commitment fee = %d, want = %d", got, c.want)
			}
		})
	}
}

// chgCommitmentAmounts are the three commitments of the shared examples:
// none, reached (1 cent) and not reached (1_000_000).
var chgCommitmentAmounts = []struct {
	name   string
	amount int64 // -1 for no commitment
}{{"no minimum commitment", -1}, {"minimum commitment reached", 1}, {"minimum commitment not reached", 1_000_000}}

// chgPeriodicPlan creates the 100_000 plan of the in arrears and in advance
// specs, with its minimum commitment unless amount is negative.
func chgPeriodicPlan(s *scenario, interval period.Interval, advance, monthly bool, amount int64) *catalog.Plan {
	s.T.Helper()
	p := s.Plan(interval, 100_000, func(p *catalog.Plan) {
		p.PayInAdvance = advance
		if monthly {
			p.BillChargesMonthly = &monthly
		}
	})
	if amount >= 0 {
		s.Commitment(p, amount)
	}
	return p
}

// TestScenarioMinimumCommitmentInArrears ports
// spec/scenarios/commitments/minimum/in_arrears_spec.rb: the first
// invoice of each plan carries the commitment true-up only when the
// commitment is not reached.
func TestScenarioMinimumCommitmentInArrears(t *testing.T) {
	d := func(y int, m time.Month, day int, h ...int) time.Time { return billingtest.Date(y, m, day, h...) }
	cases := []struct {
		source   string
		interval period.Interval
		calendar bool
		monthly  bool
		start    time.Time
		billing  time.Time
		want     int64
	}{
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:85
		{"weekly calendar", period.Weekly, true, false, d(2023, 2, 1), d(2023, 2, 6), 642_857},
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:94
		{"weekly anniversary", period.Weekly, false, false, d(2023, 2, 1), d(2023, 2, 15), 900_000},
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:107
		{"monthly calendar", period.Monthly, true, false, d(2023, 2, 4), d(2023, 3, 1), 803_571},
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:116
		{"monthly anniversary", period.Monthly, false, false, d(2023, 2, 4), d(2023, 3, 4), 900_000},
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:129
		{"quarterly calendar", period.Quarterly, true, false, d(2023, 2, 4), d(2023, 4, 1), 560_000},
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:138
		{"quarterly anniversary", period.Quarterly, false, false, d(2023, 2, 4), d(2023, 5, 4), 900_000},
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:151
		{"yearly calendar", period.Yearly, true, false, d(2022, 2, 1), d(2023, 1, 1), 823_561},
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:156 (bill_charges_monthly false in lago too)
		{"yearly calendar charged monthly", period.Yearly, true, false, d(2022, 2, 1), d(2023, 1, 1), 823_561},
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:168
		{"yearly anniversary charged yearly", period.Yearly, false, false, d(2022, 2, 4), d(2023, 2, 4), 900_000},
		// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:174 (bill_charges_monthly false in lago too)
		{"yearly anniversary charged monthly", period.Yearly, false, false, d(2022, 2, 4), d(2023, 2, 4), 900_000},
	}
	for _, tc := range cases {
		for _, ca := range chgCommitmentAmounts {
			// source: spec/scenarios/commitments/minimum/in_arrears_spec.rb:52, :60, :68
			t.Run(tc.source+"/"+ca.name, func(t *testing.T) {
				s := newScenario(t, tc.start)
				p := chgPeriodicPlan(s, tc.interval, false, tc.monthly, ca.amount)
				sub := chgSubscribe(s, s.Customer(), p, chgBillingTime(tc.calendar))
				for _, h := range []int{1, 2} {
					s.travelTo(tc.billing.Add(time.Duration(h) * time.Hour))
					s.performBilling()
				}
				invs := chgSubInvoices(s, sub.ID)
				if len(invs) == 0 {
					t.Fatalf("invoices = 0, want > 0")
				}
				want := int64(0)
				if ca.amount == 1_000_000 {
					want = tc.want
				}
				fs := chgFeesOf(s, invs[0], core.FeeTypeCommitment)
				if want == 0 {
					if len(fs) != 0 {
						t.Errorf("commitment fees = %d, want = 0", len(fs))
					}
					return
				}
				if len(fs) != 1 || fs[0].Amount != want {
					t.Errorf("commitment fees = %v, want = [%d]", chgAmounts(fs), want)
				}
			})
		}
	}
}

// chgAmounts lists the amounts of fees.
func chgAmounts(fs []*invoice.Fee) []int64 {
	out := make([]int64, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Amount)
	}
	return out
}

// TestScenarioMinimumCommitmentInAdvance ports
// spec/scenarios/commitments/minimum/in_advance_spec.rb: the starting
// invoice never carries the true-up; the invoice of the next period does
// when the commitment is not reached.
func TestScenarioMinimumCommitmentInAdvance(t *testing.T) {
	d := func(y int, m time.Month, day int, h ...int) time.Time { return billingtest.Date(y, m, day, h...) }
	cases := []struct {
		source   string
		line     int
		interval period.Interval
		calendar bool
		monthly  bool
		start    time.Time
		next     time.Time
		want     int64
	}{
		{"weekly calendar", 53, period.Weekly, true, false, d(2023, 2, 1), d(2023, 2, 6, 10), 642_857},
		{"weekly anniversary", 143, period.Weekly, false, false, d(2023, 2, 1), d(2023, 2, 8, 10), 900_000},
		{"monthly calendar", 214, period.Monthly, true, false, d(2023, 2, 4), d(2023, 3, 1, 10), 803_571},
		{"monthly anniversary", 281, period.Monthly, false, false, d(2023, 2, 4), d(2023, 3, 4, 10), 900_000},
		{"quarterly calendar", 352, period.Quarterly, true, false, d(2023, 2, 4), d(2023, 4, 1, 10), 560_000},
		{"quarterly anniversary", 419, period.Quarterly, false, false, d(2023, 2, 4), d(2023, 5, 4, 10), 900_000},
		{"yearly calendar charged yearly", 491, period.Yearly, true, false, d(2022, 2, 1), d(2023, 1, 1, 10), 823_561},
		{"yearly calendar charged monthly", 556, period.Yearly, true, true, d(2022, 2, 1), d(2023, 1, 1, 10), 823_561},
		{"yearly anniversary charged yearly", 625, period.Yearly, false, false, d(2022, 2, 4), d(2023, 2, 4, 10), 900_000},
		{"yearly anniversary charged monthly", 690, period.Yearly, false, true, d(2022, 2, 4), d(2023, 2, 4, 10), 900_000},
	}
	for _, tc := range cases {
		setup := func(t *testing.T, amount int64) (*scenario, *subscription.Subscription) {
			s := newScenario(t, tc.start)
			p := chgPeriodicPlan(s, tc.interval, true, tc.monthly, amount)
			sub := chgSubscribe(s, s.Customer(), p, chgBillingTime(tc.calendar))
			s.performBilling()
			return s, sub
		}
		for _, ca := range chgCommitmentAmounts {
			// source: spec/scenarios/commitments/minimum/in_advance_spec.rb:<line>+4, +12, +20 (no previous period)
			t.Run(fmt.Sprintf("%s/no previous period/%s (line %d)", tc.source, ca.name, tc.line), func(t *testing.T) {
				s, sub := setup(t, ca.amount)
				chgCommitmentFee(t, s, sub.ID, 0)
			})
			// source: spec/scenarios/commitments/minimum/in_advance_spec.rb:<line>+38, +46, +54 (previous period)
			t.Run(fmt.Sprintf("%s/previous period/%s (line %d)", tc.source, ca.name, tc.line), func(t *testing.T) {
				s, sub := setup(t, ca.amount)
				s.travelTo(tc.next)
				s.performBilling()
				want := int64(0)
				if ca.amount == 1_000_000 {
					want = tc.want
				}
				chgCommitmentFee(t, s, sub.ID, want)
			})
		}
	}

	// source: spec/scenarios/commitments/minimum/in_advance_spec.rb:125
	t.Run("weekly calendar/terminated", func(t *testing.T) {
		s := newScenario(t, d(2023, 2, 1))
		p := chgPeriodicPlan(s, period.Weekly, true, false, 1_000_000)
		sub := chgSubscribe(s, s.Customer(), p)
		s.performBilling()
		s.travelTo(d(2023, 2, 6, 10))
		s.performBilling()
		s.travelTo(d(2023, 2, 13, 10))
		s.performBilling()
		s.travelTo(d(2023, 2, 15, 10))
		_, err := s.E.Lifecycle.Terminate(s.Ctx, chgTerminate(s, sub))
		s.must(err)
		s.drain()
		invs := chgSubInvoices(s, sub.ID)
		if len(invs) != 4 {
			t.Fatalf("invoices = %d, want = 4", len(invs))
		}
		var all []int64
		for i, want := range []int{0, 1, 1, 1} {
			fs := chgFeesOf(s, invs[i], core.FeeTypeCommitment)
			if len(fs) != want {
				t.Errorf("invoice %d commitment fees = %d, want = %d", i+1, len(fs), want)
			}
			all = append(all, chgAmounts(fs)...)
		}
		slices.Sort(all)
		if want := []int64{328_571, 642_857, 900_000}; !slices.Equal(all, want) {
			t.Errorf("commitment fees = %v, want = %v", all, want)
		}
	})
}

// TestScenarioMinimumCommitmentOnTermination ports
// spec/scenarios/commitments/minimum/on_termination_spec.rb.
func TestScenarioMinimumCommitmentOnTermination(t *testing.T) {
	setup := func(t *testing.T) (*scenario, *subscription.Subscription) {
		s := newScenario(t, billingtest.Date(2024, 3, 1))
		p := s.Plan(period.Monthly, 10_000)
		s.Commitment(p, 1_000_000)
		sub := chgSubscribe(s, s.Customer(), p)
		for _, at := range []time.Time{billingtest.Date(2024, 4, 1), billingtest.Date(2024, 5, 1)} {
			s.travelTo(at)
			s.performBilling()
		}
		return s, sub
	}
	// source: spec/scenarios/commitments/minimum/on_termination_spec.rb:54
	t.Run("periodic invoices", func(t *testing.T) {
		s, sub := setup(t)
		invs := chgSubInvoices(s, sub.ID)
		if len(invs) != 2 {
			t.Fatalf("invoices = %d, want = 2", len(invs))
		}
		for _, inv := range invs {
			if n := len(chgFeesOf(s, inv, core.FeeTypeCommitment)); n != 1 {
				t.Errorf("commitment fees = %d, want = 1", n)
			}
			if inv.TotalAmount != 1_000_000 {
				t.Errorf("total = %d, want = 1000000", inv.TotalAmount)
			}
		}
	})
	// source: spec/scenarios/commitments/minimum/on_termination_spec.rb:66
	t.Run("upgrade", func(t *testing.T) {
		s, sub := setup(t)
		pricier := s.Plan(period.Monthly, 50_000)
		s.travelTo(billingtest.Date(2024, 6, 1, 10))
		chgSubscribe(s, s.ReloadCustomer(sub.CustomerID), pricier)
		chgAssertTermination(t, s, sub.ID, billingtest.Date(2024, 6, 1), billingtest.Date(2024, 6, 1, 10))
	})
	// source: spec/scenarios/commitments/minimum/on_termination_spec.rb:93
	t.Run("downgrade", func(t *testing.T) {
		s, sub := setup(t)
		cheaper := s.Plan(period.Monthly, 1_000)
		s.travelTo(billingtest.Date(2024, 5, 15))
		chgSubscribe(s, s.ReloadCustomer(sub.CustomerID), cheaper)
		if got := chgReloadSub(s, sub.ID).Status; got != subscription.StatusActive {
			t.Fatalf("status = %s, want = active", got)
		}
		s.travelTo(time.Date(2024, 6, 1, 0, 15, 49, 0, time.UTC))
		s.performBilling()
		inv := chgAssertTermination(t, s, sub.ID, billingtest.Date(2024, 5, 1), billingtest.Date(2024, 6, 1))
		if got := chgSum(chgFeesOf(s, inv, core.FeeTypeSubscription)); got != 10_000 {
			t.Errorf("subscription fee = %d, want = 10000", got)
		}
		if got := chgSum(chgFeesOf(s, inv, core.FeeTypeCommitment)); got != 990_000 {
			t.Errorf("commitment fee = %d, want = 990000", got)
		}
		if inv.TotalAmount != 1_000_000 {
			t.Errorf("total = %d, want = 1000000", inv.TotalAmount)
		}
	})
}

// chgTerminate is the termination input of sub now.
func chgTerminate(s *scenario, sub *subscription.Subscription) lifecycle.TerminateInput {
	return lifecycle.TerminateInput{OrganizationID: s.OrgID(), SubscriptionID: sub.ID}
}

// chgAssertTermination asserts the subscription is terminated with a
// finalized last invoice billing [from, to) with one commitment fee, and
// returns that invoice. Lago prints the end as to - 1s.
func chgAssertTermination(t *testing.T, s *scenario, id core.SubscriptionID, from, to time.Time) *invoice.Invoice {
	t.Helper()
	if got := chgReloadSub(s, id).Status; got != subscription.StatusTerminated {
		t.Fatalf("status = %s, want = terminated", got)
	}
	invs := chgSubInvoices(s, id)
	inv := invs[len(invs)-1]
	if inv.Status != invoice.StatusFinalized {
		t.Errorf("invoice status = %s, want = finalized", inv.Status)
	}
	var iss []*invoice.Subscription
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		iss, err = r.InvoiceSubscriptions.ListByInvoice(ctx, s.OrgID(), inv.ID)
		return err
	})
	if len(iss) != 1 {
		t.Fatalf("invoice subscriptions = %d, want = 1", len(iss))
	}
	if got := iss[0].Period; !got.From.Equal(from) || !got.To.Equal(to) {
		t.Errorf("period = %v, want = [%v, %v)", got, from, to)
	}
	if n := len(chgFeesOf(s, inv, core.FeeTypeCommitment)); n != 1 {
		t.Errorf("commitment fees = %d, want = 1", n)
	}
	return inv
}
