package period

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// trialCase is a subscription with a free trial, the instant it is looked
// at and the expected trial state.
type trialCase struct {
	Name             string `yaml:"name"`
	Source           string `yaml:"source"`
	Now              ts     `yaml:"now"`
	TrialDays        int    `yaml:"trial_days"`
	StartedAt        ts     `yaml:"started_at"`
	InitialStartedAt ts     `yaml:"initial_started_at"`
	TrialEndedAt     ts     `yaml:"trial_ended_at"`
	Timezone         string `yaml:"timezone"`
	// Want holds trial_end (an instant, or null for none), in_trial and
	// trial_end_due.
	Want map[string]any `yaml:"want"`
}

func TestTrial(t *testing.T) {
	for _, c := range loadCases[trialCase](t, "trial.yaml", nil) {
		t.Run(c.Name, func(t *testing.T) {
			t.Log("source:", c.Source)
			s := Subscription{
				Plan:             Plan{Interval: Monthly, TrialDays: c.TrialDays},
				BillingTime:      Calendar,
				SubscriptionAt:   c.StartedAt.Time,
				StartedAt:        c.StartedAt.Time,
				InitialStartedAt: c.InitialStartedAt.Time,
				TrialEndedAt:     c.TrialEndedAt.Time,
				Location:         loadLocation(t, c.Timezone),
			}
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
			now := c.Now.Time
			for name, want := range c.Want {
				switch name {
				case "trial_end":
					end, ok := s.TrialEnd()
					if want == nil {
						if ok {
							t.Errorf("trial_end: got = %s, want = none", end)
						}
						continue
					}
					w, err := wantTime(want)
					if err != nil {
						t.Fatal(err)
					}
					if !ok || !end.Equal(w) {
						t.Errorf("trial_end: got = %s (%v), want = %s", end, ok, w)
					}
				case "in_trial":
					if got := s.InTrial(now); got != want {
						t.Errorf("in_trial: got = %v, want = %v", got, want)
					}
				case "trial_end_due":
					if got := s.TrialEndDue(now); got != want {
						t.Errorf("trial_end_due: got = %v, want = %v", got, want)
					}
				default:
					t.Fatalf("unknown result %q", name)
				}
			}
		})
	}
}

// changeCase is a plan change and how it is classified.
type changeCase struct {
	Name   string     `yaml:"name"`
	Source string     `yaml:"source"`
	From   planAmount `yaml:"from"`
	To     planAmount `yaml:"to"`
	Want   string     `yaml:"want"`
}

type planAmount struct {
	Interval string `yaml:"interval"`
	Amount   string `yaml:"amount"`
}

func (p planAmount) parse(t *testing.T) (Interval, money.Decimal) {
	t.Helper()
	interval, err := ParseInterval(p.Interval)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := money.Parse(p.Amount)
	if err != nil {
		t.Fatal(err)
	}
	return interval, amount
}

func TestCompare(t *testing.T) {
	for _, c := range loadCases[changeCase](t, "change.yaml", nil) {
		t.Run(c.Name, func(t *testing.T) {
			t.Log("source:", c.Source)
			fromInterval, fromAmount := c.From.parse(t)
			toInterval, toAmount := c.To.parse(t)
			if got := Compare(fromInterval, fromAmount, toInterval, toAmount).String(); got != c.Want {
				t.Errorf("Compare: got = %s, want = %s", got, c.Want)
			}
		})
	}
}

func TestYearlyAmount(t *testing.T) {
	tests := []struct {
		interval Interval
		want     string
	}{
		{Weekly, "520"},
		{Monthly, "120"},
		{Quarterly, "40"},
		{Semiannual, "20"},
		{Yearly, "10"},
	}
	for _, tt := range tests {
		if got := YearlyAmount(tt.interval, money.MustParse("10")); !got.Equal(money.MustParse(tt.want)) {
			t.Errorf("YearlyAmount(%s, 10): got = %s, want = %s", tt.interval, got, tt.want)
		}
	}
}
