package coupon

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var testNow = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

var (
	testOrg      = core.MustParseID[core.OrganizationID]("0190a0a0-0000-7000-8000-000000000001")
	testCoupon   = core.MustParseID[core.CouponID]("0190a0a0-0000-7000-8000-000000000002")
	testCustomer = core.MustParseID[core.CustomerID]("0190a0a0-0000-7000-8000-000000000003")
	testApplied  = core.MustParseID[core.AppliedCouponID]("0190a0a0-0000-7000-8000-000000000004")
	usd          = money.MustCurrency("USD")
	eur          = money.MustCurrency("EUR")
)

func ptr[T any](v T) *T { return &v }

func planID(n int) core.PlanID {
	return core.MustParseID[core.PlanID](fmt.Sprintf("0190a0a0-0000-7000-8000-0000000001%02d", n))
}

func metricID(n int) core.BillableMetricID {
	return core.MustParseID[core.BillableMetricID](fmt.Sprintf("0190a0a0-0000-7000-8000-0000000002%02d", n))
}

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func newFixedCoupon() *Coupon {
	c := New(testOrg, testCoupon, "promo", "Promo", TypeFixedAmount, testNow)
	c.Amount = ptr[int64](1000)
	c.Currency = usd
	return c
}

func TestCouponValidate(t *testing.T) {
	type tc struct {
		Name              string            `yaml:"name"`
		Source            string            `yaml:"source"`
		Code              *string           `yaml:"code"`
		CouponName        *string           `yaml:"coupon_name"`
		CouponType        string            `yaml:"coupon_type"`
		Amount            *int64            `yaml:"amount"`
		NoAmount          bool              `yaml:"no_amount"`
		NoCurrency        bool              `yaml:"no_currency"`
		PercentageRate    *spectest.Decimal `yaml:"percentage_rate"`
		Frequency         string            `yaml:"frequency"`
		FrequencyDuration *int              `yaml:"frequency_duration"`
		Expiration        string            `yaml:"expiration"`
		ExpirationAt      *spectest.Time    `yaml:"expiration_at"`
		Plans             int               `yaml:"plans"`
		Metrics           int               `yaml:"metrics"`
		Errors            []string          `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "coupon.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			cp := newFixedCoupon()
			if c.Code != nil {
				cp.Code = *c.Code
			}
			if c.CouponName != nil {
				cp.Name = *c.CouponName
			}
			if c.CouponType != "" {
				cp.Type = Type(c.CouponType)
			}
			if c.Amount != nil {
				cp.Amount = c.Amount
			}
			if c.NoAmount {
				cp.Amount = nil
			}
			if c.NoCurrency {
				cp.Currency = money.Currency{}
			}
			if c.PercentageRate != nil {
				cp.PercentageRate = &c.PercentageRate.Decimal
			}
			if c.Frequency != "" {
				cp.Frequency = Frequency(c.Frequency)
			}
			cp.FrequencyDuration = c.FrequencyDuration
			if c.Expiration != "" {
				cp.Expiration = Expiration(c.Expiration)
			}
			if c.ExpirationAt != nil {
				cp.ExpirationAt = &c.ExpirationAt.Time
			}
			for i := range c.Plans {
				cp.LimitedPlanIDs = append(cp.LimitedPlanIDs, planID(i))
			}
			for i := range c.Metrics {
				cp.LimitedBillableMetricIDs = append(cp.LimitedBillableMetricIDs, metricID(i))
			}
			if got := fieldErrors(cp.Validate(testNow)); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, c.Errors, c.Source)
			}
		})
	}
}

func TestCanTransition(t *testing.T) {
	for _, from := range StatusValues() {
		for _, to := range StatusValues() {
			want := from == StatusActive && to == StatusTerminated
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) got = %v, want = %v", from, to, got, want)
			}
		}
	}
}

func TestTerminate(t *testing.T) {
	// spec/models/coupon_spec.rb:108 and spec/models/applied_coupon_spec.rb:90.
	c := newFixedCoupon()
	if err := c.Terminate(testNow); err != nil || c.Status != StatusTerminated || c.TerminatedAt == nil {
		t.Fatalf("Terminate() got = (%v, %s, %v), want = (nil, terminated, set)", err, c.Status, c.TerminatedAt)
	}
	if err := c.Terminate(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("second Terminate() got = %v, want = %v", err, core.ErrInvalidTransition)
	}
	a := Apply(newFixedCoupon(), testApplied, testCustomer, Overrides{}, testNow)
	if err := a.Terminate(testNow); err != nil || a.Status != StatusTerminated || a.TerminatedAt == nil {
		t.Fatalf("AppliedCoupon.Terminate() got = (%v, %s, %v), want = (nil, terminated, set)", err, a.Status, a.TerminatedAt)
	}
	if err := a.Terminate(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("second AppliedCoupon.Terminate() got = %v, want = %v", err, core.ErrInvalidTransition)
	}
}

func TestExpired(t *testing.T) {
	c := newFixedCoupon()
	if c.Expired(testNow) {
		t.Errorf("Expired() without expiration got = true, want = false")
	}
	c.Expiration, c.ExpirationAt = ExpirationTimeLimit, ptr(testNow)
	if !c.Expired(testNow) || c.Expired(testNow.Add(-time.Second)) {
		t.Errorf("Expired() at and before expiration got = (%v, %v), want = (true, false)", c.Expired(testNow), c.Expired(testNow.Add(-time.Second)))
	}
}

// source: spec/services/coupons/update_service_spec.rb:32
func TestKeepLocked(t *testing.T) {
	old, next := newFixedCoupon(), newFixedCoupon()
	next.Name, next.Description = "New", ptr("text")
	next.Expiration, next.ExpirationAt = ExpirationTimeLimit, ptr(testNow)
	next.Code, next.Amount, next.Currency, next.Reusable = "other", ptr[int64](5), eur, false
	next.Frequency, next.FrequencyDuration = FrequencyRecurring, ptr(2)
	next.LimitedPlanIDs = []core.PlanID{planID(1)}
	KeepLocked(old, next)
	want := newFixedCoupon()
	want.Name, want.Description = "New", ptr("text")
	want.Expiration, want.ExpirationAt = ExpirationTimeLimit, ptr(testNow)
	if next.Code != want.Code || *next.Amount != *want.Amount || next.Currency != want.Currency || next.Reusable != want.Reusable ||
		next.Frequency != want.Frequency || next.FrequencyDuration != nil || len(next.LimitedPlanIDs) != 0 ||
		next.Name != "New" || *next.Description != "text" || next.Expiration != ExpirationTimeLimit {
		t.Errorf("KeepLocked() got = %+v, want = %+v", next, want)
	}
}

func TestApply(t *testing.T) {
	// spec/services/applied_coupons/create_service_spec.rb: the applied
	// coupon captures the coupon's terms, overridden by the parameters.
	c := newFixedCoupon()
	c.Frequency, c.FrequencyDuration = FrequencyRecurring, ptr(3)
	a := Apply(c, testApplied, testCustomer, Overrides{}, testNow)
	if *a.Amount != 1000 || a.Currency != usd || *a.FrequencyDuration != 3 || *a.FrequencyDurationRemaining != 3 || a.RemainingAmount != nil || a.PercentageRate != nil {
		t.Errorf("Apply() got = %+v, want = coupon terms with 3 periods remaining", a)
	}
	a = Apply(c, testApplied, testCustomer, Overrides{Amount: ptr[int64](50), Currency: eur, Frequency: FrequencyOnce}, testNow)
	if *a.Amount != 50 || a.Currency != eur || a.FrequencyDuration != nil || a.FrequencyDurationRemaining != nil || *a.RemainingAmount != 50 {
		t.Errorf("Apply(overrides) got = %+v, want = 50 EUR once with 50 remaining", a)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("Validate() got = %v, want = nil", err)
	}
	*c.Amount = 1
	if *a.Amount != 50 {
		t.Errorf("Apply() shares the coupon's amount: got = %d, want = 50", *a.Amount)
	}
	p := New(testOrg, testCoupon, "pct", "Pct", TypePercentage, testNow)
	p.PercentageRate = ptr(money.MustParse("10"))
	a = Apply(p, testApplied, testCustomer, Overrides{PercentageRate: ptr(money.MustParse("20"))}, testNow)
	if !a.Percentage() || !a.PercentageRate.Equal(money.MustParse("20")) || a.Amount != nil || a.RemainingAmount != nil {
		t.Errorf("Apply(percentage) got = %+v, want = 20%% without amount", a)
	}
}

func TestCheckApplication(t *testing.T) {
	limitedTo := func(plans []int, metrics []int) *Coupon {
		c := newFixedCoupon()
		for _, p := range plans {
			c.LimitedPlanIDs = append(c.LimitedPlanIDs, planID(p))
		}
		for _, m := range metrics {
			c.LimitedBillableMetricIDs = append(c.LimitedBillableMetricIDs, metricID(m))
		}
		return c
	}
	planMetrics := map[core.PlanID][]core.BillableMetricID{planID(1): {metricID(1)}, planID(2): {metricID(2)}}
	tests := []struct {
		name, source string
		coupon       *Coupon
		facts        ApplicationFacts
		want         string
	}{
		{"applicable", "spec/services/applied_coupons/create_service_spec.rb", newFixedCoupon(), ApplicationFacts{}, ""},
		{"customer without currency", "spec/services/applied_coupons/create_service_spec.rb", newFixedCoupon(), ApplicationFacts{}, ""},
		{"terminated coupon", "spec/services/applied_coupons/create_service_spec.rb:211", func() *Coupon { c := newFixedCoupon(); c.Status = StatusTerminated; return c }(), ApplicationFacts{}, "coupon_not_found"},
		{"deleted coupon", "app/models/coupon.rb:67", func() *Coupon { c := newFixedCoupon(); c.Delete(testNow); return c }(), ApplicationFacts{}, "coupon_not_found"},
		{"reusable already applied", "spec/services/applied_coupons/create_service_spec.rb", newFixedCoupon(), ApplicationFacts{AppliedCount: 2}, ""},
		{
			"not reusable already applied", "spec/services/applied_coupons/create_service_spec.rb", func() *Coupon { c := newFixedCoupon(); c.Reusable = false; return c }(),
			ApplicationFacts{AppliedCount: 1},
			"coupon: " + CodeNotReusable,
		},
		{
			"overlap before reuse", "app/services/applied_coupons/create_service.rb:68-72",
			func() *Coupon { c := limitedTo([]int{1}, nil); c.Reusable = false; return c }(),
			ApplicationFacts{AppliedCount: 1, Active: []*Coupon{limitedTo([]int{1}, nil)}, PlanMetrics: planMetrics},
			CodePlanOverlapping,
		},
		{
			"same plan", "spec/services/applied_coupons/create_service_spec.rb", limitedTo([]int{1}, nil),
			ApplicationFacts{Active: []*Coupon{limitedTo([]int{1, 3}, nil)}, PlanMetrics: planMetrics},
			CodePlanOverlapping,
		},
		{
			"other plan", "spec/services/applied_coupons/create_service_spec.rb", limitedTo([]int{1}, nil),
			ApplicationFacts{Active: []*Coupon{limitedTo([]int{2}, nil)}, PlanMetrics: planMetrics},
			"",
		},
		{
			"same metric", "spec/services/applied_coupons/create_service_spec.rb", limitedTo(nil, []int{1}),
			ApplicationFacts{Active: []*Coupon{limitedTo(nil, []int{1})}, PlanMetrics: planMetrics},
			CodePlanOverlapping,
		},
		{
			"metric charged by an applied plan", "spec/services/applied_coupons/create_service_spec.rb", limitedTo(nil, []int{1}),
			ApplicationFacts{Active: []*Coupon{limitedTo([]int{1}, nil)}, PlanMetrics: planMetrics},
			CodePlanOverlapping,
		},
		{
			"plan charging an applied metric", "spec/services/applied_coupons/create_service_spec.rb", limitedTo([]int{2}, nil),
			ApplicationFacts{Active: []*Coupon{limitedTo(nil, []int{2})}, PlanMetrics: planMetrics},
			CodePlanOverlapping,
		},
		{
			"unlimited never overlaps", "spec/services/applied_coupons/create_service_spec.rb", newFixedCoupon(),
			ApplicationFacts{Active: []*Coupon{limitedTo([]int{1}, nil), newFixedCoupon()}, PlanMetrics: planMetrics},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckApplication(tt.coupon, tt.facts)
			got := ""
			if re, ok := errors.AsType[*core.RuleError](err); ok {
				got = re.Code
				if !errors.Is(err, core.ErrNotAllowed) {
					t.Errorf("CheckApplication() got = %v, want = matching %v", err, core.ErrNotAllowed)
				}
			} else if errors.Is(err, core.ErrNotFound) {
				got = err.Error()
			} else if fe := fieldErrors(err); len(fe) > 0 {
				got = fe[0]
			} else if err != nil {
				t.Fatalf("CheckApplication() got = %v, want = a rule, not found or validation error", err)
			}
			if got != tt.want {
				t.Errorf("CheckApplication() got = %q, want = %q (%s)", got, tt.want, tt.source)
			}
		})
	}
}

func TestAppliedCoupon(t *testing.T) {
	type tc struct {
		Name                  string            `yaml:"name"`
		Source                string            `yaml:"source"`
		Op                    string            `yaml:"op"`
		CouponType            string            `yaml:"coupon_type"`
		Amount                int64             `yaml:"amount"`
		PercentageRate        *spectest.Decimal `yaml:"percentage_rate"`
		Frequency             string            `yaml:"frequency"`
		FrequencyDuration     *int              `yaml:"frequency_duration"`
		DurationRemaining     *int              `yaml:"duration_remaining"`
		Remaining             *int64            `yaml:"remaining"`
		Status                string            `yaml:"status"`
		Base                  int64             `yaml:"base"`
		Credit                int64             `yaml:"credit"`
		CouponTerminated      bool              `yaml:"coupon_terminated"`
		Errors                []string          `yaml:"errors"`
		WantDiscount          int64             `yaml:"want_discount"`
		WantRemaining         *int64            `yaml:"want_remaining"`
		WantDurationRemaining *int              `yaml:"want_duration_remaining"`
		WantStatus            string            `yaml:"want_status"`
	}
	for _, c := range spectest.Load[tc](t, "applied_coupon.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			cp := New(testOrg, testCoupon, "promo", "Promo", Type(c.CouponType), testNow)
			cp.Frequency = Frequency(c.Frequency)
			cp.FrequencyDuration = c.FrequencyDuration
			if cp.Type == TypePercentage {
				cp.PercentageRate = &c.PercentageRate.Decimal
			} else {
				cp.Amount, cp.Currency = ptr(c.Amount), usd
			}
			a := Apply(cp, testApplied, testCustomer, Overrides{}, testNow)
			if c.DurationRemaining != nil {
				a.FrequencyDurationRemaining = c.DurationRemaining
			}
			if c.Remaining != nil {
				a.RemainingAmount = c.Remaining
			}
			if c.Status != "" {
				a.Status = Status(c.Status)
				if a.Status == StatusTerminated {
					a.TerminatedAt = ptr(testNow)
				}
			}
			switch c.Op {
			case "validate":
				if got := fieldErrors(a.Validate()); !slices.Equal(got, c.Errors) {
					t.Errorf("Validate() errors got = %v, want = %v (%s)", got, c.Errors, c.Source)
				}
				return
			case "discount":
				if got := a.Discount(c.Base); got != c.WantDiscount {
					t.Errorf("Discount(%d) got = %d, want = %d (%s)", c.Base, got, c.WantDiscount, c.Source)
				}
				return
			case "consume":
				if err := a.Consume(c.Credit, testNow); err != nil {
					t.Fatalf("Consume() got = %v, want = nil", err)
				}
			case "restore":
				a.Restore(c.Credit, c.CouponTerminated, testNow)
			case "consume_restore":
				if err := a.Consume(c.Credit, testNow); err != nil {
					t.Fatalf("Consume() got = %v, want = nil", err)
				}
				a.Restore(c.Credit, c.CouponTerminated, testNow)
			default:
				t.Fatalf("unknown op %q", c.Op)
			}
			if c.WantRemaining != nil && (a.RemainingAmount == nil || *a.RemainingAmount != *c.WantRemaining) {
				t.Errorf("RemainingAmount got = %v, want = %d (%s)", deref(a.RemainingAmount), *c.WantRemaining, c.Source)
			}
			if c.WantDurationRemaining != nil && (a.FrequencyDurationRemaining == nil || *a.FrequencyDurationRemaining != *c.WantDurationRemaining) {
				t.Errorf("FrequencyDurationRemaining got = %v, want = %d (%s)", deref(a.FrequencyDurationRemaining), *c.WantDurationRemaining, c.Source)
			}
			if string(a.Status) != c.WantStatus {
				t.Errorf("Status got = %s, want = %s (%s)", a.Status, c.WantStatus, c.Source)
			}
			if (a.Status == StatusTerminated) != (a.TerminatedAt != nil) {
				t.Errorf("TerminatedAt got = %v, want = set iff terminated (%s)", a.TerminatedAt, c.Source)
			}
		})
	}
}

func TestConsumeTerminated(t *testing.T) {
	a := Apply(newFixedCoupon(), testApplied, testCustomer, Overrides{}, testNow)
	_ = a.Terminate(testNow)
	if err := a.Consume(1, testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Consume() on terminated got = %v, want = %v", err, core.ErrInvalidTransition)
	}
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
