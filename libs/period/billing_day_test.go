package period

import "testing"

// billingDayCase is a subscription and the instants it is or is not billed on.
type billingDayCase struct {
	Name                    string `yaml:"name"`
	Source                  string `yaml:"source"`
	Interval                string `yaml:"interval"`
	BillingTime             string `yaml:"billing_time"`
	BillChargesMonthly      bool   `yaml:"bill_charges_monthly"`
	BillFixedChargesMonthly bool   `yaml:"bill_fixed_charges_monthly"`
	SubscriptionAt          ts     `yaml:"subscription_at"`
	Timezone                string `yaml:"timezone"`
	BilledOn                []ts   `yaml:"billed_on"`
	NotBilledOn             []ts   `yaml:"not_billed_on"`
}

func TestIsBillingDay(t *testing.T) {
	for _, c := range loadCases[billingDayCase](t, "billing_day.yaml", nil) {
		t.Run(c.Name, func(t *testing.T) {
			t.Log("source:", c.Source)
			interval, err := ParseInterval(c.Interval)
			if err != nil {
				t.Fatal(err)
			}
			billingTime, err := ParseBillingTime(c.BillingTime)
			if err != nil {
				t.Fatal(err)
			}
			s := Subscription{
				Plan: Plan{
					Interval:                interval,
					BillChargesMonthly:      c.BillChargesMonthly,
					BillFixedChargesMonthly: c.BillFixedChargesMonthly,
				},
				BillingTime:    billingTime,
				SubscriptionAt: c.SubscriptionAt.Time,
				StartedAt:      c.SubscriptionAt.Time,
				Location:       loadLocation(t, c.Timezone),
			}
			for _, at := range c.BilledOn {
				if got := s.IsBillingDay(at.Time); !got {
					t.Errorf("IsBillingDay(%s): got = %v, want = true", at.Time, got)
				}
			}
			for _, at := range c.NotBilledOn {
				if got := s.IsBillingDay(at.Time); got {
					t.Errorf("IsBillingDay(%s): got = %v, want = false", at.Time, got)
				}
			}
		})
	}
}
