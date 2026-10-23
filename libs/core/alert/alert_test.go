package alert

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var testNow = time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func decimals(t *testing.T, ss []string) []money.Decimal {
	t.Helper()
	out := []money.Decimal{}
	for _, s := range ss {
		d, err := money.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func strs(ds []money.Decimal) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.Reduce().String())
	}
	return out
}

func thresholdsOf(t *testing.T, oneTime []string, recurring ...string) []Threshold {
	var out []Threshold
	for _, v := range decimals(t, oneTime) {
		out = append(out, Threshold{Code: ptr("warn" + v.String()), Value: v})
	}
	for _, v := range decimals(t, recurring) {
		out = append(out, Threshold{Code: ptr("rec"), Value: v, Recurring: true})
	}
	return out
}

func TestCrossed(t *testing.T) {
	type tc struct {
		Name       string   `yaml:"name"`
		Source     string   `yaml:"source"`
		Thresholds []string `yaml:"thresholds"`
		Recurring  *string  `yaml:"recurring"`
		Direction  string   `yaml:"direction"`
		Previous   string   `yaml:"previous"`
		Current    string   `yaml:"current"`
		Want       []string `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "crossed.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var rec []string
			if c.Recurring != nil {
				rec = []string{*c.Recurring}
			}
			a := &Alert{Direction: Direction(c.Direction), Thresholds: thresholdsOf(t, c.Thresholds, rec...)}
			got := strs(Crossed(a, money.MustParse(c.Previous), money.MustParse(c.Current)))
			if !slices.Equal(got, c.Want) {
				t.Errorf("Crossed() = %v, want = %v", got, c.Want)
			}
		})
	}
}

func TestCrossedCapsRecurring(t *testing.T) {
	a := &Alert{Direction: DirectionIncreasing, Thresholds: thresholdsOf(t, nil, "0.001")}
	got := Crossed(a, money.Decimal{}, money.NewFromInt(1_000_000))
	if len(got) != MaxRecurringCrossings || !got[len(got)-1].Equal(money.NewFromInt(1_000_000)) {
		t.Errorf("len = %d, last = %v, want = %d values ending at 1000000", len(got), got[len(got)-1], MaxRecurringCrossings)
	}
}

func TestFormat(t *testing.T) {
	type tc struct {
		Name       string   `yaml:"name"`
		Source     string   `yaml:"source"`
		Thresholds []string `yaml:"thresholds"`
		Recurring  string   `yaml:"recurring"`
		Values     []string `yaml:"values"`
		Want       []string `yaml:"want"`
		OneTime    []string `yaml:"one_time"`
	}
	for _, c := range spectest.Load[tc](t, "format.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			a := &Alert{Thresholds: thresholdsOf(t, c.Thresholds, c.Recurring)}
			got := []string{}
			for _, x := range a.Format(decimals(t, c.Values)) {
				s := *x.Code + ":" + x.Value.String()
				if x.Recurring {
					s += ":recurring"
				}
				got = append(got, s)
			}
			if !slices.Equal(got, c.Want) {
				t.Errorf("Format() = %v, want = %v", got, c.Want)
			}
			if ot := strs(a.OneTimeValues()); !slices.Equal(ot, c.OneTime) {
				t.Errorf("OneTimeValues() = %v, want = %v", ot, c.OneTime)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	type tc struct {
		Name           string   `yaml:"name"`
		Source         string   `yaml:"source"`
		Type           *string  `yaml:"type"`
		Code           *string  `yaml:"code"`
		Subscription   *string  `yaml:"subscription"`
		Wallet         bool     `yaml:"wallet"`
		Metric         bool     `yaml:"metric"`
		Direction      string   `yaml:"direction"`
		Thresholds     []string `yaml:"thresholds"`
		ThresholdCount int      `yaml:"threshold_count"`
		Recurring      []string `yaml:"recurring"`
		Errors         []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "alert.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			a := &Alert{
				Code:                   "my-code",
				Type:                   TypeCurrentUsageAmount,
				SubscriptionExternalID: "sub-1",
				Direction:              DirectionIncreasing,
			}
			oneTime := []string{"10", "20"}
			if c.Thresholds != nil {
				oneTime = c.Thresholds
			}
			if c.ThresholdCount > 0 {
				oneTime = nil
				for i := range c.ThresholdCount {
					oneTime = append(oneTime, fmt.Sprint(i+1))
				}
			}
			a.Thresholds = thresholdsOf(t, oneTime, c.Recurring...)
			if c.Type != nil {
				a.Type = Type(*c.Type)
			}
			if c.Code != nil {
				a.Code = *c.Code
			}
			if c.Subscription != nil {
				a.SubscriptionExternalID = *c.Subscription
			}
			if c.Wallet {
				a.WalletID = core.MustParseID[core.WalletID]("0190a0a0-0000-7000-8000-000000000001")
			}
			if c.Metric {
				a.BillableMetricID = core.MustParseID[core.BillableMetricID]("0190a0a0-0000-7000-8000-000000000002")
			}
			if c.Direction != "" {
				a.Direction = Direction(c.Direction)
			}
			got := []string{}
			for _, fe := range core.FieldErrors(a.Validate()) {
				got = append(got, fe.String())
			}
			if !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() = %v, want = %v", got, c.Errors)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	a := &Alert{
		ID:         core.MustParseID[core.AlertID]("0190a0a0-0000-7000-8000-000000000003"),
		Type:       TypeCurrentUsageAmount,
		Direction:  DirectionIncreasing,
		Thresholds: thresholdsOf(t, []string{"10", "30"}),
	}
	id := core.MustParseID[core.TriggeredAlertID]("0190a0a0-0000-7000-8000-000000000004")
	if got := a.Evaluate(id, core.SubscriptionID{}, money.NewFromInt(5), testNow); got != nil {
		t.Errorf("Evaluate(5) = %v, want = nil", got)
	}
	got := a.Evaluate(id, core.SubscriptionID{}, money.NewFromInt(35), testNow)
	if got == nil || len(got.CrossedThresholds) != 2 || !got.PreviousValue.Equal(money.NewFromInt(5)) {
		t.Fatalf("Evaluate(35) = %+v, want = 2 thresholds from 5", got)
	}
	if !a.PreviousValue.Equal(money.NewFromInt(35)) || a.LastProcessedAt == nil {
		t.Errorf("PreviousValue = %v, want = 35", a.PreviousValue)
	}
	a.ResetForPeriod(testNow)
	if !a.PreviousValue.IsZero() {
		t.Errorf("PreviousValue after reset = %v, want = 0", a.PreviousValue)
	}
}

func TestTypePredicates(t *testing.T) {
	for _, ty := range TypeValues() {
		wallet := strings.HasPrefix(string(ty), "wallet_")
		if got := ty.IsWalletType(); got != wallet {
			t.Errorf("%s.IsWalletType() = %v, want = %v", ty, got, wallet)
		}
		if got, want := ty.UsesBillableMetric(), strings.HasPrefix(string(ty), "billable_metric_"); got != want {
			t.Errorf("%s.UsesBillableMetric() = %v, want = %v", ty, got, want)
		}
		if got, want := ty.DefaultDirection() == DirectionDecreasing, wallet; got != want {
			t.Errorf("%s.DefaultDirection() = %v", ty, ty.DefaultDirection())
		}
		if !wallet && ty.IsCurrentUsage() == ty.IsLifetimeUsage() {
			t.Errorf("%s: current = %v, lifetime = %v, want = exactly one", ty, ty.IsCurrentUsage(), ty.IsLifetimeUsage())
		}
	}
}
