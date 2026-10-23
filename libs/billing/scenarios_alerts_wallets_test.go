package billing_test

// Ported from lago spec/scenarios/wallets/alerting/*_alert_spec.rb: the
// wallet alerts are evaluated by the job every balance and ongoing balance
// change enqueues (lago UsageMonitoring::ProcessWalletAlertsJob).

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// walAlertValue is the value a wallet alert of type typ reads from w (lago
// find_value of the wallet alerts).
func walAlertValue(typ alert.Type, w *wallet.Wallet) money.Decimal {
	switch typ {
	case alert.TypeWalletBalanceAmount:
		return money.NewFromInt(w.BalanceAmount)
	case alert.TypeWalletCreditsBalance:
		return w.CreditsBalance
	case alert.TypeWalletOngoingBalanceAmount:
		return money.NewFromInt(w.OngoingBalanceAmount)
	default:
		return w.CreditsOngoingBalance
	}
}

// walAlert creates an alert of type typ on wallet w as lago
// create_wallet_alert does: decreasing, with the wallet's current value as
// its baseline (app/services/usage_monitoring/create_alert_service.rb:97),
// then schedules its evaluation (Engine.AlertSaved) and drains the jobs.
func (s *scenario) walAlert(w *wallet.Wallet, typ alert.Type, ths ...alert.Threshold) *alert.Alert {
	s.T.Helper()
	now := s.Now()
	a := &alert.Alert{
		ID: core.NewID[core.AlertID](s.IDs), OrganizationID: s.OrgID(), Code: "alert", Type: typ,
		WalletID: w.ID, Direction: typ.DefaultDirection(), CreatedAt: now, UpdatedAt: now,
	}
	if a.Direction == alert.DirectionDecreasing {
		a.PreviousValue = walAlertValue(typ, s.ReloadWallet(w.ID))
	}
	for _, t := range ths {
		t.ID = core.NewID[core.AlertThresholdID](s.IDs)
		a.Thresholds = append(a.Thresholds, t)
	}
	s.must(a.Validate())
	s.tx(func(ctx context.Context, r port.Repos) error { return r.Alerts.Create(ctx, a) })
	s.must(s.E.AlertSaved(s.Ctx, s.OrgID(), a))
	s.drain()
	return a
}

// reloadAlert reloads alert a.
func (s *scenario) reloadAlert(a *alert.Alert) *alert.Alert {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*alert.Alert, error) {
		return r.Alerts.Get(ctx, s.OrgID(), a.ID)
	})
}

// alertTriggered returns the triggered alerts of a, oldest first.
func (s *scenario) alertTriggered(a *alert.Alert) []*alert.TriggeredAlert {
	return (&monScenario{scenario: s}).triggered(a)
}

// walCross is a crossed threshold, in the alert's unit.
type walCross struct {
	code      string
	value     int64
	recurring bool
}

// walOnce and walRec are one-time and recurring crossings.
func walOnce(code string, v int64) walCross { return walCross{code, v, false} }
func walRec(code string, v int64) walCross  { return walCross{code, v, true} }

// walAlertStep is one event of a wallet alert scenario: the event's
// amount, the wallet value and alert previous value after it (in the
// alert's unit) and the thresholds the alert crosses (none when empty).
type walAlertStep struct {
	amount  string
	value   int64
	crossed []walCross
}

// walAlertCase is one wallet alert scenario: a plan of one standard charge
// of 1 per unit (in advance or in arrears), a wallet with granted credits
// (topped up before the alert with topUp), an alert and events.
type walAlertCase struct {
	name, source  string
	typ           alert.Type
	inAdvance     bool
	granted, rate string
	topUp         string
	ths           []alert.Threshold
	steps         []walAlertStep
}

// walOngoingCases are the cases of
// spec/scenarios/wallets/alerting/wallet_ongoing_balance_amount_alert_spec.rb,
// in cents; wallet_credits_ongoing_balance_alert_spec.rb repeats them in
// credits at rate 1 ([walInCredits]).
func walOngoingCases() []walAlertCase {
	const src = "spec/scenarios/wallets/alerting/wallet_ongoing_balance_amount_alert_spec.rb:"
	def := []alert.Threshold{th(7500, "warn"), th(5000, "critical"), th(2500, "emergency")}
	return []walAlertCase{
		{name: "ongoing goes down", source: src + "89", granted: "100", ths: []alert.Threshold{th(5000, "alert")}, steps: []walAlertStep{
			{amount: "25", value: 7500},
			{amount: "30", value: 4500, crossed: []walCross{walOnce("alert", 5000)}},
			{amount: "20", value: 2500},
		}},
		{name: "changes before the first alert", source: src + "139", granted: "50", topUp: "50", ths: def, steps: []walAlertStep{
			{amount: "30", value: 7000, crossed: []walCross{walOnce("warn", 7500)}},
		}},
		{name: "no change before the first alert", source: src + "168", granted: "100", ths: def, steps: []walAlertStep{
			{amount: "30", value: 7000, crossed: []walCross{walOnce("warn", 7500)}},
		}},
		{name: "one threshold crossed", source: src + "192", granted: "100", ths: def, steps: []walAlertStep{
			{amount: "30", value: 7000, crossed: []walCross{walOnce("warn", 7500)}},
		}},
		{name: "multiple thresholds at once", source: src + "213", granted: "100", ths: def, steps: []walAlertStep{
			{amount: "70", value: 3000, crossed: []walCross{walOnce("warn", 7500), walOnce("critical", 5000)}},
		}},
		{name: "multiple thresholds over time", source: src + "237", granted: "100", ths: def, steps: []walAlertStep{
			{amount: "30", value: 7000, crossed: []walCross{walOnce("warn", 7500)}},
			{amount: "30", value: 4000, crossed: []walCross{walOnce("critical", 5000)}},
			{amount: "30", value: 1000, crossed: []walCross{walOnce("emergency", 2500)}},
		}},
		{name: "no threshold crossed", source: src + "293", granted: "100", ths: def, steps: []walAlertStep{
			{amount: "10", value: 9000},
		}},
		{name: "only one-time thresholds", source: src + "312", granted: "100", ths: def, steps: []walAlertStep{
			{amount: "30", value: 7000, crossed: []walCross{walOnce("warn", 7500)}},
		}},
		{name: "recurring threshold", source: src + "333", granted: "100", ths: []alert.Threshold{th(8000, "one_time"), rth(1000, "recurring")}, steps: []walAlertStep{
			{amount: "25", value: 7500, crossed: []walCross{walOnce("one_time", 8000)}},
			{amount: "10", value: 6500, crossed: []walCross{walRec("recurring", 7000)}},
			{amount: "20", value: 4500, crossed: []walCross{walRec("recurring", 5000), walRec("recurring", 6000)}},
		}},
		{name: "negative thresholds", source: src + "393", granted: "100", ths: []alert.Threshold{th(5000, "positive"), th(-5000, "negative")}, steps: []walAlertStep{
			{amount: "55", value: 4500, crossed: []walCross{walOnce("positive", 5000)}},
			{amount: "100", value: -5500, crossed: []walCross{walOnce("negative", -5000)}},
		}},
		{name: "recurring below zero", source: src + "436", granted: "20", ths: []alert.Threshold{th(1500, "one_time"), rth(1000, "recurring")}, steps: []walAlertStep{
			{amount: "10", value: 1000, crossed: []walCross{walOnce("one_time", 1500)}},
			{amount: "10", value: 0, crossed: []walCross{walRec("recurring", 500)}},
			{amount: "10", value: -1000, crossed: []walCross{walRec("recurring", -500)}},
		}},
	}
}

// walInCredits returns the ongoing case c as a credits ongoing balance case
// at rate 1: every amount in cents becomes credits.
func walInCredits(c walAlertCase) walAlertCase {
	c.typ = alert.TypeWalletCreditsOngoingBalance
	c.source = strings.Replace(c.source, "wallet_ongoing_balance_amount", "wallet_credits_ongoing_balance", 1)
	ths := make([]alert.Threshold, len(c.ths))
	for i, t := range c.ths {
		t.Value = t.Value.Div(money.NewFromInt(100))
		ths[i] = t
	}
	c.ths = ths
	steps := make([]walAlertStep, len(c.steps))
	for i, st := range c.steps {
		st.value /= 100
		cs := make([]walCross, len(st.crossed))
		for j, x := range st.crossed {
			x.value /= 100
			cs[j] = x
		}
		st.crossed = cs
		steps[i] = st
	}
	c.steps = steps
	return c
}

func TestWalletAlertScenarios(t *testing.T) {
	const (
		bal = "spec/scenarios/wallets/alerting/wallet_balance_amount_alert_spec.rb:"
		cr  = "spec/scenarios/wallets/alerting/wallet_credits_balance_alert_spec.rb:"
	)
	tests := []walAlertCase{
		{
			name: "balance drops below threshold", source: bal + "40", typ: alert.TypeWalletBalanceAmount, inAdvance: true, granted: "100",
			ths: []alert.Threshold{th(8000, "warn"), th(5000, "critical"), th(2000, "emergency")}, steps: []walAlertStep{
				{amount: "10", value: 9000},
				{amount: "20", value: 7000, crossed: []walCross{walOnce("warn", 8000)}},
			},
		},
		{
			name: "balance crosses all at once", source: bal + "110", typ: alert.TypeWalletBalanceAmount, inAdvance: true, granted: "100",
			ths: []alert.Threshold{th(9000, "notice"), th(7000, "warn"), th(5000, "critical")}, steps: []walAlertStep{
				{amount: "5", value: 9500},
				{amount: "55", value: 4000, crossed: []walCross{walOnce("notice", 9000), walOnce("warn", 7000), walOnce("critical", 5000)}},
			},
		},
		{
			name: "balance stays above threshold", source: bal + "153", typ: alert.TypeWalletBalanceAmount, inAdvance: true, granted: "100",
			ths: []alert.Threshold{th(5000, "warn")}, steps: []walAlertStep{
				{amount: "10", value: 9000},
				{amount: "10", value: 8000},
			},
		},
		{
			name: "balance recurring threshold", source: bal + "190", typ: alert.TypeWalletBalanceAmount, inAdvance: true, granted: "100",
			ths: []alert.Threshold{th(8000, "initial"), rth(2000, "low")}, steps: []walAlertStep{
				{amount: "5", value: 9500},
				{amount: "25", value: 7000, crossed: []walCross{walOnce("initial", 8000)}},
				{amount: "20", value: 5000, crossed: []walCross{walRec("low", 6000)}},
			},
		},
		{
			name: "balance progressive consumption", source: bal + "248", typ: alert.TypeWalletBalanceAmount, inAdvance: true, granted: "100",
			ths: []alert.Threshold{th(7000, "notice"), th(4000, "warn"), th(1000, "critical")}, steps: []walAlertStep{
				{amount: "10", value: 9000},
				{amount: "10", value: 8000},
				{amount: "20", value: 6000, crossed: []walCross{walOnce("notice", 7000)}},
				{amount: "30", value: 3000, crossed: []walCross{walOnce("warn", 4000)}},
			},
		},
		{
			name: "credits balance at rate 1", source: cr + "28", typ: alert.TypeWalletCreditsBalance, inAdvance: true, granted: "100",
			ths: []alert.Threshold{th(80, "notice"), th(50, "warn")}, steps: []walAlertStep{
				{amount: "10", value: 90},
				{amount: "20", value: 70, crossed: []walCross{walOnce("notice", 80)}},
			},
		},
		{
			name: "credits balance at rate 2", source: cr + "95", typ: alert.TypeWalletCreditsBalance, inAdvance: true, granted: "50", rate: "2",
			ths: []alert.Threshold{th(40, "notice"), th(30, "warn")}, steps: []walAlertStep{
				{amount: "10", value: 45},
				{amount: "30", value: 30, crossed: []walCross{walOnce("notice", 40), walOnce("warn", 30)}},
			},
		},
		{
			name: "credits balance recurring threshold", source: cr + "150", typ: alert.TypeWalletCreditsBalance, inAdvance: true, granted: "100",
			ths: []alert.Threshold{th(90, "initial"), rth(10, "low")}, steps: []walAlertStep{
				{amount: "5", value: 95},
				{amount: "25", value: 70, crossed: []walCross{walOnce("initial", 90), walRec("low", 70), walRec("low", 80)}},
				{amount: "20", value: 50, crossed: []walCross{walRec("low", 50), walRec("low", 60), walRec("low", 70)}},
			},
		},
	}
	for _, c := range walOngoingCases() {
		c.name, c.typ = "ongoing balance amount: "+c.name, alert.TypeWalletOngoingBalanceAmount
		tests = append(tests, c)
		credits := walInCredits(c)
		credits.name = strings.Replace(c.name, "ongoing balance amount", "credits ongoing balance", 1)
		tests = append(tests, credits)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := walScenario(t, billingtest.Date(2024, 3, 1), 5)
			m := s.Metric("amount_metric", catalog.AggregationTypeSum, "amount")
			p := s.Plan(period.Monthly, 0)
			var opts []func(*catalog.Charge)
			if tt.inAdvance {
				opts = append(opts, walInAdvance)
			}
			s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, opts...)
			c := s.Customer()
			in := wallets.CreateInput{Name: new("Wallet"), GrantedCredits: walDec(tt.granted)}
			if tt.rate != "" {
				in.RateAmount = money.MustParse(tt.rate)
			}
			w := s.walCreate(c, in)
			if tt.topUp != "" {
				s.walTopUp(wallets.TransactionInput{WalletID: w.ID, GrantedCredits: walDec(tt.topUp)})
			}
			s.travelTo(billingtest.Date(2024, 3, 2))
			sub := s.walSubscribe(c, p)
			a := s.walAlert(w, tt.typ, tt.ths...)
			if got, want := a.Direction, alert.DirectionDecreasing; got != want {
				t.Errorf("direction: got = %s, want = %s", got, want)
			}
			s.travelTo(billingtest.Date(2024, 3, 5))
			prev := a.PreviousValue
			n := 0
			for i, st := range tt.steps {
				s.ingest(sub, m.Code, "amount", st.amount)
				s.must(s.E.TrackUsage(s.Ctx, s.OrgID(), c.ID, []core.SubscriptionID{sub.ID}))
				s.walRefresh(c)
				want := money.NewFromInt(st.value)
				if got := walAlertValue(tt.typ, s.ReloadWallet(w.ID)); !got.Equal(want) {
					t.Errorf("step %d: wallet value: got = %s, want = %s", i, got, want)
				}
				if got := s.reloadAlert(a).PreviousValue; !got.Equal(want) {
					t.Errorf("step %d: previous_value: got = %s, want = %s", i, got, want)
				}
				if len(st.crossed) > 0 {
					n++
				}
				ts := s.alertTriggered(a)
				if len(ts) != n {
					t.Fatalf("step %d: triggered alerts: got = %d, want = %d", i, len(ts), n)
				}
				if len(st.crossed) > 0 {
					ta := ts[n-1]
					if !ta.CurrentValue.Equal(want) || !ta.PreviousValue.Equal(prev) {
						t.Errorf("step %d: triggered values: got = %s/%s, want = %s/%s", i, ta.CurrentValue, ta.PreviousValue, want, prev)
					}
					if ta.WalletID != w.ID || !ta.SubscriptionID.IsZero() {
						t.Errorf("step %d: alertable: got = wallet %s, subscription %s, want = wallet %s, none", i, ta.WalletID, ta.SubscriptionID, w.ID)
					}
					var wantCrossed []string
					for _, x := range st.crossed {
						r := "once"
						if x.recurring {
							r = "recurring"
						}
						wantCrossed = append(wantCrossed, x.code+":"+strconv.FormatInt(x.value, 10)+":"+r)
					}
					monStrings(t, "crossed_thresholds", crossed(ta), wantCrossed)
				}
				prev = want
			}
			if got := monCount(s.Webhooks(), webhook.EventTypeAlertTriggered); got != n {
				t.Errorf("alert.triggered webhooks: got = %d, want = %d", got, n)
			}
		})
	}
}
