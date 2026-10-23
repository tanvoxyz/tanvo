package dunning

import (
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var testNow = time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)

func currency(code string) money.Currency {
	if code == "" {
		return money.Currency{}
	}
	return money.MustCurrency(code)
}

func TestCampaignValidate(t *testing.T) {
	type threshold struct {
		Currency string `yaml:"currency"`
		Amount   int64  `yaml:"amount"`
	}
	type tc struct {
		Name         string       `yaml:"name"`
		Source       string       `yaml:"source"`
		CampaignName *string      `yaml:"campaign_name"`
		Code         *string      `yaml:"code"`
		Days         *int         `yaml:"days"`
		Max          *int         `yaml:"max"`
		BCC          []string     `yaml:"bcc"`
		Thresholds   *[]threshold `yaml:"thresholds"`
		Errors       []string     `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "campaign.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			k := New(core.OrganizationID{}, core.DunningCampaignID{}, "dun", "Dunning", testNow)
			k.Thresholds = []Threshold{{Currency: currency("EUR"), Amount: 1000}}
			if c.CampaignName != nil {
				k.Name = *c.CampaignName
			}
			if c.Code != nil {
				k.Code = *c.Code
			}
			if c.Days != nil {
				k.DaysBetweenAttempts = *c.Days
			}
			if c.Max != nil {
				k.MaxAttempts = *c.Max
			}
			k.BCCEmails = c.BCC
			if c.Thresholds != nil {
				k.Thresholds = nil
				for _, th := range *c.Thresholds {
					k.Thresholds = append(k.Thresholds, Threshold{Currency: currency(th.Currency), Amount: th.Amount})
				}
			}
			got := []string{}
			for _, fe := range core.FieldErrors(k.Validate()) {
				got = append(got, fe.String())
			}
			want := c.Errors
			if want == nil {
				want = []string{}
			}
			if !slices.Equal(got, want) {
				t.Errorf("errors: got = %v, want = %v", got, want)
			}
		})
	}
}

func TestPlan(t *testing.T) {
	type tc struct {
		Name       string           `yaml:"name"`
		Source     string           `yaml:"source"`
		Thresholds map[string]int64 `yaml:"thresholds"`
		Days       int              `yaml:"days"`
		Max        int              `yaml:"max"`
		Excluded   bool             `yaml:"excluded"`
		Prior      map[string]int   `yaml:"prior"`
		Last       *float64         `yaml:"last"`
		Overdue    map[string]int64 `yaml:"overdue"`
		Want       []string         `yaml:"want"`
		Attempts   map[string]int   `yaml:"attempts"`
		Finished   bool             `yaml:"finished"`
	}
	for _, c := range spectest.Load[tc](t, "plan.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			k := New(core.OrganizationID{}, core.DunningCampaignID{}, "dun", "Dunning", testNow)
			k.DaysBetweenAttempts, k.MaxAttempts = c.Days, c.Max
			for _, cur := range slices.Sorted(maps.Keys(c.Thresholds)) {
				k.Thresholds = append(k.Thresholds, Threshold{Currency: currency(cur), Amount: c.Thresholds[cur]})
			}
			s := CustomerState{Excluded: c.Excluded, Attempts: c.Prior}
			if c.Last != nil {
				last := testNow.Add(-time.Duration(*c.Last * float64(24*time.Hour)))
				s.LastAttemptAt = &last
			}
			a := k.Plan(s, c.Overdue, testNow)
			got := []string{}
			for _, th := range a.Thresholds {
				got = append(got, th.Currency.Code())
			}
			if !slices.Equal(got, c.Want) {
				t.Errorf("thresholds: got = %v, want = %v", got, c.Want)
			}
			if a.Due() != (len(c.Want) > 0) {
				t.Errorf("Due: got = %t, want = %t", a.Due(), len(c.Want) > 0)
			}
			if !maps.Equal(a.Attempts, c.Attempts) {
				t.Errorf("attempts: got = %v, want = %v", a.Attempts, c.Attempts)
			}
			if a.Finished != c.Finished {
				t.Errorf("finished: got = %t, want = %t", a.Finished, c.Finished)
			}
		})
	}
}

func TestPlanDoesNotMutateState(t *testing.T) {
	// source: tanvo
	k := New(core.OrganizationID{}, core.DunningCampaignID{}, "dun", "Dunning", testNow)
	k.MaxAttempts = 3
	k.Thresholds = []Threshold{{Currency: currency("EUR"), Amount: 1}}
	prior := map[string]int{"EUR": 1}
	a := k.Plan(CustomerState{Attempts: prior}, map[string]int64{"EUR": 10}, testNow)
	if prior["EUR"] != 1 || a.Attempts["EUR"] != 2 {
		t.Errorf("attempts: got = prior %v, new %v, want = prior 1, new 2", prior, a.Attempts)
	}
}

func TestApplicable(t *testing.T) {
	own := core.NewID[core.DunningCampaignID](core.UUIDv7Generator{})
	entity := core.NewID[core.DunningCampaignID](core.UUIDv7Generator{})
	tests := []struct {
		name             string
		source           string
		excluded         bool
		customer, entity core.DunningCampaignID
		want             core.DunningCampaignID
	}{
		{"billing entity default", "spec/services/dunning_campaigns/bulk_process_service_spec.rb:111", false, core.DunningCampaignID{}, entity, entity},
		{"customer override", "spec/services/dunning_campaigns/bulk_process_service_spec.rb:263", false, own, entity, own},
		{"excluded", "spec/services/dunning_campaigns/process_attempt_service_spec.rb:111", true, own, entity, core.DunningCampaignID{}},
		{"none", "spec/services/dunning_campaigns/bulk_process_service_spec.rb:498", false, core.DunningCampaignID{}, core.DunningCampaignID{}, core.DunningCampaignID{}},
	}
	for _, tt := range tests {
		if got := Applicable(tt.excluded, tt.customer, tt.entity); got != tt.want {
			t.Errorf("%s: got = %s, want = %s", tt.name, got, tt.want)
		}
	}
}

func TestDelete(t *testing.T) {
	// source: spec/models/dunning_campaign_spec.rb:60
	k := New(core.OrganizationID{}, core.DunningCampaignID{}, "dun", "Dunning", testNow)
	if err := k.Delete(testNow); err != nil || !k.Deleted() {
		t.Errorf("Delete: got = %v, want = deleted", err)
	}
	if err := k.Delete(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Delete twice: got = %v, want = ErrInvalidTransition", err)
	}
	if _, ok := (&Campaign{Thresholds: []Threshold{{Currency: currency("USD")}}}).Threshold("EUR"); ok {
		t.Errorf("Threshold(EUR): got = found, want = none")
	}
}
