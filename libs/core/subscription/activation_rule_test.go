package subscription

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

func testRule(st ActivationRuleStatus) *ActivationRule {
	r := NewActivationRule(core.NewID[core.OrganizationID](testIDs), core.NewID[core.ActivationRuleID](testIDs),
		core.NewID[core.SubscriptionID](testIDs), ActivationRuleTypePayment, 0, testNow)
	r.Status = st
	return r
}

func TestActivationRule(t *testing.T) {
	type tc struct {
		Name         string   `yaml:"name"`
		Source       string   `yaml:"source"`
		Status       string   `yaml:"status"`
		RuleType     *string  `yaml:"rule_type"`
		TimeoutHours int      `yaml:"timeout_hours"`
		ExpiresAt    *float64 `yaml:"expires_at"`
		Fulfilled    bool     `yaml:"fulfilled"`
		Rejected     bool     `yaml:"rejected"`
		Expirable    bool     `yaml:"expirable"`
		Errors       []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "activation_rule.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			r := testRule(ActivationRuleStatus(c.Status))
			if c.RuleType != nil {
				r.RuleType = ActivationRuleType(*c.RuleType)
			}
			r.TimeoutHours = c.TimeoutHours
			r.ExpiresAt = atp(c.ExpiresAt)
			if got := r.Fulfilled(); got != c.Fulfilled {
				t.Errorf("Fulfilled: got = %v, want = %v", got, c.Fulfilled)
			}
			if got := r.Rejected(); got != c.Rejected {
				t.Errorf("Rejected: got = %v, want = %v", got, c.Rejected)
			}
			if got := r.Expirable(testNow); got != c.Expirable {
				t.Errorf("Expirable: got = %v, want = %v", got, c.Expirable)
			}
			want := c.Errors
			if want == nil {
				want = []string{}
			}
			if got := fieldErrors(r.Validate()); !slices.Equal(got, want) {
				t.Errorf("errors: got = %v, want = %v", got, want)
			}
		})
	}
}

func TestCanTransitionRule(t *testing.T) {
	allowed := map[ActivationRuleStatus][]ActivationRuleStatus{
		ActivationRuleStatusInactive: {ActivationRuleStatusPending, ActivationRuleStatusNotApplicable},
		ActivationRuleStatusPending: {
			ActivationRuleStatusSatisfied, ActivationRuleStatusFailed,
			ActivationRuleStatusExpired, ActivationRuleStatusDeclined,
		},
	}
	methods := map[ActivationRuleStatus]func(*ActivationRule, time.Time) error{
		ActivationRuleStatusPending:       (*ActivationRule).Start,
		ActivationRuleStatusNotApplicable: (*ActivationRule).MarkNotApplicable,
		ActivationRuleStatusSatisfied:     (*ActivationRule).Satisfy,
		ActivationRuleStatusFailed:        (*ActivationRule).Fail,
		ActivationRuleStatusExpired:       (*ActivationRule).Expire,
		ActivationRuleStatusDeclined:      (*ActivationRule).Decline,
	}
	for _, from := range ActivationRuleStatusValues() {
		for _, to := range ActivationRuleStatusValues() {
			want := slices.Contains(allowed[from], to)
			if got := CanTransitionRule(from, to); got != want {
				t.Errorf("CanTransitionRule(%s, %s): got = %v, want = %v", from, to, got, want)
			}
			m, ok := methods[to]
			if !ok {
				continue
			}
			r := testRule(from)
			err := m(r, testNow)
			if want {
				if err != nil || r.Status != to {
					t.Errorf("%s → %s: got = %v %s, want = nil %s", from, to, err, r.Status, to)
				}
			} else if !errors.Is(err, core.ErrInvalidTransition) || r.Status != from {
				t.Errorf("%s → %s: got = %v %s, want = core.ErrInvalidTransition %s", from, to, err, r.Status, from)
			}
		}
	}
}

func TestActivationRuleStart(t *testing.T) {
	r := testRule(ActivationRuleStatusInactive)
	r.TimeoutHours = 48
	if err := r.Start(testNow); err != nil {
		t.Fatal(err)
	}
	if want := at(48); r.ExpiresAt == nil || !r.ExpiresAt.Equal(want) {
		t.Errorf("expires_at: got = %v, want = %v", r.ExpiresAt, want)
	}
	r = testRule(ActivationRuleStatusInactive)
	if err := r.Start(testNow); err != nil || r.ExpiresAt != nil {
		t.Errorf("without timeout: got = %v %v, want = nil nil", err, r.ExpiresAt)
	}
}

func TestPaymentRuleApplicable(t *testing.T) {
	type tc struct {
		Name                  string `yaml:"name"`
		Source                string `yaml:"source"`
		PayInAdvance          bool   `yaml:"pay_in_advance"`
		InTrial               bool   `yaml:"in_trial"`
		InAdvanceFixedCharges bool   `yaml:"in_advance_fixed_charges"`
		Applicable            bool   `yaml:"applicable"`
	}
	for _, c := range spectest.Load[tc](t, "payment_rule.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			f := ActivationFacts{PlanPayInAdvance: c.PayInAdvance, InTrial: c.InTrial, HasInAdvanceFixedCharges: c.InAdvanceFixedCharges}
			if got := PaymentRuleApplicable(f); got != c.Applicable {
				t.Errorf("PaymentRuleApplicable: got = %v, want = %v", got, c.Applicable)
			}
		})
	}
}

func TestGating(t *testing.T) {
	type tc struct {
		Name         string   `yaml:"name"`
		Source       string   `yaml:"source"`
		Status       string   `yaml:"status"`
		Rules        []string `yaml:"rules"`
		PendingRules bool     `yaml:"pending_rules"`
		Gated        bool     `yaml:"gated"`
		PaymentGated bool     `yaml:"payment_gated"`
	}
	for _, c := range spectest.Load[tc](t, "gating.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			s := &Subscription{Status: Status(c.Status)}
			var rules []*ActivationRule
			for _, st := range c.Rules {
				rules = append(rules, testRule(ActivationRuleStatus(st)))
			}
			if got := HasPendingRules(rules); got != c.PendingRules {
				t.Errorf("HasPendingRules: got = %v, want = %v", got, c.PendingRules)
			}
			if got := Gated(s, rules); got != c.Gated {
				t.Errorf("Gated: got = %v, want = %v", got, c.Gated)
			}
			if got := PaymentGated(s, rules); got != c.PaymentGated {
				t.Errorf("PaymentGated: got = %v, want = %v", got, c.PaymentGated)
			}
		})
	}
}

func TestActivationRuleChecks(t *testing.T) {
	for _, st := range StatusValues() {
		err := CheckActivationRulesEditable(&Subscription{Status: st})
		if got, want := err == nil, st == StatusPending; got != want {
			t.Errorf("CheckActivationRulesEditable(%s): got = %v, want editable = %v", st, err, want)
		}
	}
	method := core.NewID[core.PaymentMethodID](testIDs)
	cases := []struct {
		name        string
		typ         core.PaymentMethodType
		id          core.PaymentMethodID
		customerHas bool
		wantAllowed bool
	}{
		{"manual", core.PaymentMethodTypeManual, core.PaymentMethodID{}, true, false},
		{"provider with method", core.PaymentMethodTypeProvider, method, false, true},
		{"provider with customer default", core.PaymentMethodTypeProvider, core.PaymentMethodID{}, true, true},
		{"provider without any method", core.PaymentMethodTypeProvider, core.PaymentMethodID{}, false, false},
	}
	for _, c := range cases {
		err := CheckPaymentRule(&Subscription{PaymentMethodType: c.typ, PaymentMethodID: c.id}, c.customerHas)
		if got := err == nil; got != c.wantAllowed {
			t.Errorf("CheckPaymentRule(%s): got = %v, want allowed = %v", c.name, err, c.wantAllowed)
		}
		var re *core.RuleError
		if err != nil && (!errors.As(err, &re) || re.Code != CodePaymentMethodRequired) {
			t.Errorf("CheckPaymentRule(%s): got = %v, want = rule error %s", c.name, err, CodePaymentMethodRequired)
		}
	}
}
