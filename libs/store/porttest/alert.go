package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// alertFixtureMetric creates a count billable metric.
func alertFixtureMetric(ctx context.Context, e *env, r port.Repos, org core.OrganizationID, code string) *catalog.BillableMetric {
	e.t.Helper()
	m := &catalog.BillableMetric{
		ID: core.NewID[core.BillableMetricID](e.ids), OrganizationID: org, Code: code, Name: "Metric " + code,
		AggregationType: catalog.AggregationTypeCount, CreatedAt: now, UpdatedAt: now,
	}
	noErr(e.t, "Metrics.Create() fixture", r.Metrics.Create(ctx, m))
	return m
}

// alertFixtureWallet creates a EUR wallet of p's customer.
func alertFixtureWallet(ctx context.Context, e *env, r port.Repos, p subscriptionParents) *wallet.Wallet {
	e.t.Helper()
	w := wallet.New(p.org, core.NewID[core.WalletID](e.ids), p.customer.ID, money.MustCurrency("EUR"), money.MustParse("1"), now)
	noErr(e.t, "Wallets.Create() fixture", r.Wallets.Create(ctx, w))
	return w
}

// alertFixture returns an unstored subscription alert.
func alertFixture(e *env, org core.OrganizationID, subscriptionExternalID, code string, typ alert.Type) *alert.Alert {
	return &alert.Alert{
		ID: core.NewID[core.AlertID](e.ids), OrganizationID: org, Code: code, Type: typ,
		SubscriptionExternalID: subscriptionExternalID, Direction: alert.DirectionIncreasing,
		Thresholds: []alert.Threshold{{ID: core.NewID[core.AlertThresholdID](e.ids), Value: money.MustParse("100")}},
		CreatedAt:  now, UpdatedAt: now,
	}
}

// alertFixtureWalletAlert returns an unstored wallet alert.
func alertFixtureWalletAlert(e *env, w *wallet.Wallet, code string, typ alert.Type) *alert.Alert {
	a := alertFixture(e, w.OrganizationID, "", code, typ)
	a.WalletID = w.ID
	a.Direction = alert.DirectionDecreasing
	return a
}

func alertID(a *alert.Alert) core.AlertID { return a.ID }

func testAlert(t *testing.T, h Harness) {
	t.Run("alerts", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			metric := alertFixtureMetric(ctx, e, r, org, "api_calls")
			full := alertFixture(e, org, "sub_1", "usage", alert.TypeBillableMetricCurrentUsageAmount)
			full.Name = ptr("Usage alert")
			full.BillableMetricID = metric.ID
			full.Thresholds = []alert.Threshold{
				{ID: core.NewID[core.AlertThresholdID](e.ids), Code: ptr("warn"), Value: money.MustParse("1000.5")},
				{ID: core.NewID[core.AlertThresholdID](e.ids), Value: money.MustParse("10")},
				{ID: core.NewID[core.AlertThresholdID](e.ids), Code: ptr("again"), Value: money.MustParse("250"), Recurring: true},
			}
			full.PreviousValue = money.MustParse("12.25")
			full.LastProcessedAt = ptr(at(-time.Hour))
			noErr(t, "Create()", r.Alerts.Create(ctx, full))
			wantErr(t, "Create() with the same ID", r.Alerts.Create(ctx, full), core.ErrConflict)
			got, err := r.Alerts.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.Alerts.GetBySubscriptionCode(ctx, org, "sub_1", "usage")
			noErr(t, "GetBySubscriptionCode()", err)
			equal(t, "GetBySubscriptionCode()", got, full)

			// Every optional field cleared and the thresholds replaced.
			full.Name, full.LastProcessedAt = nil, nil
			full.BillableMetricID = core.BillableMetricID{}
			full.Type = alert.TypeCurrentUsageAmount
			full.PreviousValue = money.Decimal{}
			full.Thresholds = []alert.Threshold{{ID: core.NewID[core.AlertThresholdID](e.ids), Value: money.MustParse("5")}}
			full.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Alerts.Update(ctx, full))
			got, err = r.Alerts.Get(ctx, org, full.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, full)
			full.Thresholds = nil
			noErr(t, "Update() without thresholds", r.Alerts.Update(ctx, full))
			got, err = r.Alerts.Get(ctx, org, full.ID)
			noErr(t, "Get() without thresholds", err)
			equal(t, "Get() without thresholds", len(got.Thresholds), 0)

			_, err = r.Alerts.Get(ctx, org, core.NewID[core.AlertID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.Alerts.Get(ctx, other.org, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Alerts.GetBySubscriptionCode(ctx, other.org, "sub_1", "usage")
			wantErr(t, "GetBySubscriptionCode() from another organization", err, core.ErrNotFound)
			_, err = r.Alerts.GetBySubscriptionCode(ctx, org, "sub_2", "usage")
			wantErr(t, "GetBySubscriptionCode() of another subscription", err, core.ErrNotFound)
			moved := *full
			moved.OrganizationID = other.org
			wantErr(t, "Update() through another organization", r.Alerts.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Update() unknown", r.Alerts.Update(ctx, alertFixture(e, org, "sub_1", "x", alert.TypeCurrentUsageAmount)), core.ErrNotFound)
			wantErr(t, "Discard() from another organization", r.Alerts.Discard(ctx, other.org, full.ID, now), core.ErrNotFound)

			noErr(t, "Discard()", r.Alerts.Discard(ctx, org, full.ID, at(time.Hour)))
			_, err = r.Alerts.Get(ctx, org, full.ID)
			wantErr(t, "Get() after Discard()", err, core.ErrNotFound)
			_, err = r.Alerts.GetBySubscriptionCode(ctx, org, "sub_1", "usage")
			wantErr(t, "GetBySubscriptionCode() after Discard()", err, core.ErrNotFound)
			wantErr(t, "Update() after Discard()", r.Alerts.Update(ctx, full), core.ErrNotFound)
			wantErr(t, "Discard() twice", r.Alerts.Discard(ctx, org, full.ID, now), core.ErrNotFound)
			sub1, err := r.Alerts.ListBySubscription(ctx, org, "sub_1")
			noErr(t, "ListBySubscription() after Discard()", err)
			equal(t, "ListBySubscription() after Discard()", len(sub1), 0)
			// A discarded alert frees its code.
			again := alertFixture(e, org, "sub_1", "usage", alert.TypeCurrentUsageAmount)
			noErr(t, "Create() with a discarded code", r.Alerts.Create(ctx, again))

			// Discarding a metric's alerts leaves the others.
			watching := alertFixture(e, org, "sub_3", "units", alert.TypeBillableMetricCurrentUsageUnits)
			watching.BillableMetricID = metric.ID
			noErr(t, "Create() watching the metric", r.Alerts.Create(ctx, watching))
			n, err := r.Alerts.DiscardByBillableMetric(ctx, org, metric.ID, at(time.Hour))
			noErr(t, "DiscardByBillableMetric()", err)
			equal(t, "DiscardByBillableMetric()", n, int64(1))
			byMetric, err := r.Alerts.ListByBillableMetric(ctx, org, metric.ID)
			noErr(t, "ListByBillableMetric() after DiscardByBillableMetric()", err)
			equal(t, "ListByBillableMetric() after DiscardByBillableMetric()", len(byMetric), 0)
			_, err = r.Alerts.Get(ctx, org, watching.ID)
			wantErr(t, "Get() after DiscardByBillableMetric()", err, core.ErrNotFound)
			_, err = r.Alerts.Get(ctx, org, again.ID)
			noErr(t, "Get() of an alert not watching the metric", err)
			n, err = r.Alerts.DiscardByBillableMetric(ctx, org, metric.ID, at(time.Hour))
			noErr(t, "DiscardByBillableMetric() twice", err)
			equal(t, "DiscardByBillableMetric() twice", n, int64(0))
			return nil
		})
	})

	t.Run("uniqueness", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			w1, w2 := alertFixtureWallet(ctx, e, r, p), alertFixtureWallet(ctx, e, r, p)
			a := alertFixture(e, org, "sub_1", "usage", alert.TypeCurrentUsageAmount)
			noErr(t, "Create()", r.Alerts.Create(ctx, a))
			wantErr(t, "Create() with a taken code", r.Alerts.Create(ctx, alertFixture(e, org, "sub_1", "usage", alert.TypeLifetimeUsageAmount)), core.ErrConflict)
			// Codes are per subscription, and a subscription may have two
			// alerts of one type.
			noErr(t, "Create() on another subscription", r.Alerts.Create(ctx, alertFixture(e, org, "sub_2", "usage", alert.TypeCurrentUsageAmount)))
			b := alertFixture(e, org, "sub_1", "usage2", alert.TypeCurrentUsageAmount)
			noErr(t, "Create() of a second alert of the type", r.Alerts.Create(ctx, b))
			o := alertFixture(e, other.org, "sub_1", "usage", alert.TypeCurrentUsageAmount)
			noErr(t, "Create() in another organization", r.Alerts.Create(ctx, o))

			wa := alertFixtureWalletAlert(e, w1, "low", alert.TypeWalletBalanceAmount)
			noErr(t, "Create() wallet alert", r.Alerts.Create(ctx, wa))
			// A wallet alert does not share codes with subscription alerts.
			noErr(t, "Create() wallet alert with a subscription alert's code",
				r.Alerts.Create(ctx, alertFixtureWalletAlert(e, w1, "usage", alert.TypeWalletCreditsBalance)))
			wantErr(t, "Create() wallet alert with a taken code",
				r.Alerts.Create(ctx, alertFixtureWalletAlert(e, w1, "low", alert.TypeWalletOngoingBalanceAmount)), core.ErrConflict)
			wantErr(t, "Create() second wallet alert of a type",
				r.Alerts.Create(ctx, alertFixtureWalletAlert(e, w1, "low2", alert.TypeWalletBalanceAmount)), core.ErrConflict)
			wb := alertFixtureWalletAlert(e, w2, "low", alert.TypeWalletBalanceAmount)
			noErr(t, "Create() on another wallet", r.Alerts.Create(ctx, wb))

			got, err := r.Alerts.GetByWalletCode(ctx, org, w2.ID, "low")
			noErr(t, "GetByWalletCode()", err)
			equal(t, "GetByWalletCode()", got, wb)
			_, err = r.Alerts.GetByWalletCode(ctx, other.org, w2.ID, "low")
			wantErr(t, "GetByWalletCode() from another organization", err, core.ErrNotFound)
			_, err = r.Alerts.GetByWalletCode(ctx, org, w2.ID, "usage")
			wantErr(t, "GetByWalletCode() unknown", err, core.ErrNotFound)

			// Updates into a taken code or type conflict and the
			// transaction goes on.
			b.Code = "usage"
			wantErr(t, "Update() into a taken code", r.Alerts.Update(ctx, b), core.ErrConflict)
			wb.Type = alert.TypeWalletCreditsBalance
			noErr(t, "Update() wallet alert type", r.Alerts.Update(ctx, wb))
			wb.WalletID = w1.ID
			wantErr(t, "Update() into a taken wallet type", r.Alerts.Update(ctx, wb), core.ErrConflict)
			b.Code = "usage3"
			noErr(t, "Update() after a conflict", r.Alerts.Update(ctx, b))
			got, err = r.Alerts.Get(ctx, org, b.ID)
			noErr(t, "Get() after a conflict", err)
			equal(t, "Get() after a conflict", got, b)
			return nil
		})
	})

	t.Run("lists", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			m1, m2 := alertFixtureMetric(ctx, e, r, org, "m1"), alertFixtureMetric(ctx, e, r, org, "m2")
			w := alertFixtureWallet(ctx, e, r, p)
			a1 := alertFixture(e, org, "sub_1", "a1", alert.TypeCurrentUsageAmount)
			a2 := alertFixture(e, org, "sub_1", "a2", alert.TypeBillableMetricCurrentUsageUnits)
			a2.BillableMetricID = m1.ID
			a3 := alertFixture(e, org, "sub_2", "a3", alert.TypeBillableMetricLifetimeUsageUnits)
			a3.BillableMetricID = m1.ID
			a4 := alertFixtureWalletAlert(e, w, "a4", alert.TypeWalletBalanceAmount)
			a5 := alertFixture(e, org, "sub_1", "a5", alert.TypeLifetimeUsageAmount)
			all := []*alert.Alert{a1, a2, a3, a4, a5}
			// a2 and a3 were created at the same instant; the others an
			// hour apart.
			for i, h := range []int{0, 1, 1, 2, 3} {
				all[i].CreatedAt = now.Add(time.Duration(h) * time.Hour)
			}
			for _, a := range all {
				noErr(t, "Create()", r.Alerts.Create(ctx, a))
			}
			// List is newest first, then by ID (alerts_query.rb:9-10).
			recent := []*alert.Alert{a5, a4, a2, a3}
			if a3.ID.String() < a2.ID.String() {
				recent[2], recent[3] = a3, a2
			}
			recent = append(recent, a1)
			noErr(t, "Create() in another organization", r.Alerts.Create(ctx, alertFixture(e, other.org, "sub_1", "a1", alert.TypeCurrentUsageAmount)))

			got, err := r.Alerts.ListBySubscription(ctx, org, "sub_1")
			noErr(t, "ListBySubscription()", err)
			equal(t, "ListBySubscription()", got, []*alert.Alert{a1, a2, a5})
			got, err = r.Alerts.ListBySubscription(ctx, org, "sub_404")
			noErr(t, "ListBySubscription() unknown", err)
			equal(t, "ListBySubscription() unknown", len(got), 0)
			got, err = r.Alerts.ListByWallet(ctx, org, w.ID)
			noErr(t, "ListByWallet()", err)
			equal(t, "ListByWallet()", got, []*alert.Alert{a4})
			got, err = r.Alerts.ListByWallet(ctx, other.org, w.ID)
			noErr(t, "ListByWallet() from another organization", err)
			equal(t, "ListByWallet() from another organization", len(got), 0)
			got, err = r.Alerts.ListByBillableMetric(ctx, org, m1.ID)
			noErr(t, "ListByBillableMetric()", err)
			equal(t, "ListByBillableMetric()", got, []*alert.Alert{a2, a3})
			got, err = r.Alerts.ListByBillableMetric(ctx, org, m2.ID)
			noErr(t, "ListByBillableMetric() unused", err)
			equal(t, "ListByBillableMetric() unused", len(got), 0)

			list := func(f alert.Filter) []*alert.Alert {
				return listAll(t, 1, func(pr core.PageRequest) (core.Page[*alert.Alert], error) {
					return r.Alerts.List(ctx, org, f, pr)
				})
			}
			equal(t, "List()", list(alert.Filter{}), recent)
			equal(t, "List() by subscription", ids(list(alert.Filter{SubscriptionExternalID: "sub_1"}), alertID), []core.AlertID{a5.ID, a2.ID, a1.ID})
			equal(t, "List() by wallet", ids(list(alert.Filter{WalletID: w.ID}), alertID), []core.AlertID{a4.ID})
			equal(t, "List() by types", ids(list(alert.Filter{Types: []alert.Type{alert.TypeCurrentUsageAmount, alert.TypeWalletBalanceAmount}}), alertID),
				[]core.AlertID{a4.ID, a1.ID})
			equal(t, "List() by subscription and type", ids(list(alert.Filter{SubscriptionExternalID: "sub_1", Types: []alert.Type{alert.TypeLifetimeUsageAmount}}), alertID),
				[]core.AlertID{a5.ID})
			op, err := r.Alerts.List(ctx, other.org, alert.Filter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(op.Items), 1)
			return nil
		})
	})

	t.Run("triggered alerts", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			s := p.newSub(e, "sub_1")
			noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, s))
			w := alertFixtureWallet(ctx, e, r, p)
			sa := alertFixture(e, org, "sub_1", "usage", alert.TypeCurrentUsageAmount)
			wa := alertFixtureWalletAlert(e, w, "low", alert.TypeWalletBalanceAmount)
			for _, a := range []*alert.Alert{sa, wa} {
				noErr(t, "Alerts.Create()", r.Alerts.Create(ctx, a))
			}
			t1 := &alert.TriggeredAlert{
				ID: core.NewID[core.TriggeredAlertID](e.ids), OrganizationID: org, AlertID: sa.ID, SubscriptionID: s.ID,
				CurrentValue: money.MustParse("1500.75"), PreviousValue: money.MustParse("90"),
				CrossedThresholds: []alert.CrossedThreshold{
					{Code: ptr("warn"), Value: money.MustParse("100")},
					{Value: money.MustParse("1000")},
					{Code: ptr("again"), Value: money.MustParse("1250"), Recurring: true},
				},
				TriggeredAt: at(-time.Minute), CreatedAt: now, UpdatedAt: now,
			}
			t2 := &alert.TriggeredAlert{
				ID: core.NewID[core.TriggeredAlertID](e.ids), OrganizationID: org, AlertID: wa.ID, WalletID: w.ID,
				CurrentValue: money.MustParse("5"), PreviousValue: money.MustParse("20"),
				TriggeredAt: now, CreatedAt: now, UpdatedAt: now,
			}
			t3 := &alert.TriggeredAlert{
				ID: core.NewID[core.TriggeredAlertID](e.ids), OrganizationID: org, AlertID: sa.ID, SubscriptionID: s.ID,
				CurrentValue: money.MustParse("2000"), PreviousValue: money.MustParse("1500.75"),
				CrossedThresholds: []alert.CrossedThreshold{{Value: money.MustParse("2000")}},
				TriggeredAt:       at(time.Minute), CreatedAt: at(time.Minute), UpdatedAt: at(time.Minute),
			}
			for _, x := range []*alert.TriggeredAlert{t1, t2, t3} {
				noErr(t, "Create()", r.TriggeredAlerts.Create(ctx, x))
			}
			wantErr(t, "Create() with the same ID", r.TriggeredAlerts.Create(ctx, t1), core.ErrConflict)
			got, err := r.TriggeredAlerts.Get(ctx, org, t1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, t1)
			got, err = r.TriggeredAlerts.Get(ctx, org, t2.ID)
			noErr(t, "Get() wallet record", err)
			equal(t, "Get() wallet record", got, t2)
			_, err = r.TriggeredAlerts.Get(ctx, other.org, t1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.TriggeredAlerts.Get(ctx, org, core.NewID[core.TriggeredAlertID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			list := func(f alert.TriggeredFilter) []core.TriggeredAlertID {
				return ids(listAll(t, 1, func(pr core.PageRequest) (core.Page[*alert.TriggeredAlert], error) {
					return r.TriggeredAlerts.List(ctx, org, f, pr)
				}), func(x *alert.TriggeredAlert) core.TriggeredAlertID { return x.ID })
			}
			all := listAll(t, 2, func(pr core.PageRequest) (core.Page[*alert.TriggeredAlert], error) {
				return r.TriggeredAlerts.List(ctx, org, alert.TriggeredFilter{}, pr)
			})
			equal(t, "List()", all, []*alert.TriggeredAlert{t1, t2, t3})
			equal(t, "List() by alert", list(alert.TriggeredFilter{AlertID: sa.ID}), []core.TriggeredAlertID{t1.ID, t3.ID})
			equal(t, "List() by subscription", list(alert.TriggeredFilter{SubscriptionID: s.ID}), []core.TriggeredAlertID{t1.ID, t3.ID})
			equal(t, "List() by wallet", list(alert.TriggeredFilter{WalletID: w.ID}), []core.TriggeredAlertID{t2.ID})
			op, err := r.TriggeredAlerts.List(ctx, other.org, alert.TriggeredFilter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(op.Items), 0)

			// Discarding an alert keeps its triggered alerts.
			noErr(t, "Alerts.Discard()", r.Alerts.Discard(ctx, org, sa.ID, now))
			equal(t, "List() by alert after Discard()", list(alert.TriggeredFilter{AlertID: sa.ID}), []core.TriggeredAlertID{t1.ID, t3.ID})
			return nil
		})
	})
}
