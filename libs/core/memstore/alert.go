package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
)

// alertData holds the tables of package alert.
type alertData struct {
	alerts    *table[core.AlertID, alert.Alert]
	triggered *table[core.TriggeredAlertID, alert.TriggeredAlert]
}

func newAlertData() *alertData {
	return &alertData{
		alerts: newTable[core.AlertID](func(a alert.Alert) alert.Alert {
			a.Thresholds = slices.Clone(a.Thresholds)
			return a
		}),
		triggered: newTable[core.TriggeredAlertID](func(t alert.TriggeredAlert) alert.TriggeredAlert {
			t.CrossedThresholds = slices.Clone(t.CrossedThresholds)
			return t
		}),
	}
}

func (d *alertData) snapshot() *alertData {
	return &alertData{alerts: d.alerts.snapshot(), triggered: d.triggered.snapshot()}
}

// alertRepo implements alert.Repository.
type alertRepo struct{ d *alertData }

// alertSameTarget reports whether a and b watch the same subscription or
// wallet.
func alertSameTarget(a, b *alert.Alert) bool {
	if a.OrganizationID != b.OrganizationID {
		return false
	}
	if !a.WalletID.IsZero() || !b.WalletID.IsZero() {
		return a.WalletID == b.WalletID
	}
	return a.SubscriptionExternalID == b.SubscriptionExternalID
}

func (r alertRepo) check(a *alert.Alert) error {
	if r.d.alerts.any(func(x *alert.Alert) bool {
		return x.DeletedAt == nil && x.ID != a.ID && alertSameTarget(x, a) && x.Code == a.Code
	}) {
		return conflict("alert", "code")
	}
	if !a.WalletID.IsZero() && r.d.alerts.any(func(x *alert.Alert) bool {
		return x.DeletedAt == nil && x.ID != a.ID && alertSameTarget(x, a) && x.Type == a.Type
	}) {
		return conflict("alert", "alert_type")
	}
	return nil
}

func (r alertRepo) Create(_ context.Context, a *alert.Alert) error {
	if r.d.alerts.has(a.ID) {
		return conflict("alert", "id")
	}
	if err := r.check(a); err != nil {
		return err
	}
	r.d.alerts.put(a.ID, *a)
	return nil
}

func (r alertRepo) Update(_ context.Context, a *alert.Alert) error {
	cur, ok := r.d.alerts.get(a.ID)
	if !ok || cur.OrganizationID != a.OrganizationID || cur.DeletedAt != nil {
		return notFound("alert", a.ID)
	}
	if err := r.check(a); err != nil {
		return err
	}
	r.d.alerts.put(a.ID, *a)
	return nil
}

func (r alertRepo) Get(_ context.Context, org core.OrganizationID, id core.AlertID) (*alert.Alert, error) {
	a, ok := r.d.alerts.get(id)
	if !ok || a.OrganizationID != org || a.DeletedAt != nil {
		return nil, notFound("alert", id)
	}
	return a, nil
}

func (r alertRepo) GetBySubscriptionCode(_ context.Context, org core.OrganizationID, subscriptionExternalID, code string) (*alert.Alert, error) {
	a, ok := r.d.alerts.first(func(x *alert.Alert) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.WalletID.IsZero() && x.SubscriptionExternalID == subscriptionExternalID && x.Code == code
	})
	if !ok {
		return nil, notFound("alert", stringer(code))
	}
	return a, nil
}

func (r alertRepo) GetByWalletCode(_ context.Context, org core.OrganizationID, wallet core.WalletID, code string) (*alert.Alert, error) {
	a, ok := r.d.alerts.first(func(x *alert.Alert) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.WalletID == wallet && x.Code == code
	})
	if !ok {
		return nil, notFound("alert", stringer(code))
	}
	return a, nil
}

func (r alertRepo) ListBySubscription(_ context.Context, org core.OrganizationID, subscriptionExternalID string) ([]*alert.Alert, error) {
	return r.d.alerts.find(func(x *alert.Alert) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.WalletID.IsZero() && x.SubscriptionExternalID == subscriptionExternalID
	}), nil
}

func (r alertRepo) ListByWallet(_ context.Context, org core.OrganizationID, wallet core.WalletID) ([]*alert.Alert, error) {
	return r.d.alerts.find(func(x *alert.Alert) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.WalletID == wallet
	}), nil
}

func (r alertRepo) ListByBillableMetric(_ context.Context, org core.OrganizationID, metric core.BillableMetricID) ([]*alert.Alert, error) {
	return r.d.alerts.find(func(x *alert.Alert) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.BillableMetricID == metric
	}), nil
}

func (r alertRepo) List(_ context.Context, org core.OrganizationID, f alert.Filter, page core.PageRequest) (core.Page[*alert.Alert], error) {
	rows := r.d.alerts.find(func(x *alert.Alert) bool {
		return x.OrganizationID == org && x.DeletedAt == nil &&
			(f.SubscriptionExternalID == "" || x.SubscriptionExternalID == f.SubscriptionExternalID) &&
			(f.WalletID.IsZero() || x.WalletID == f.WalletID) &&
			(len(f.Types) == 0 || slices.Contains(f.Types, x.Type))
	})
	return paginate(rows, func(x *alert.Alert) core.AlertID { return x.ID }, page)
}

func (r alertRepo) Discard(ctx context.Context, org core.OrganizationID, id core.AlertID, at time.Time) error {
	a, err := r.Get(ctx, org, id)
	if err != nil {
		return err
	}
	at = at.UTC()
	a.Thresholds = nil
	a.DeletedAt, a.UpdatedAt = &at, at
	r.d.alerts.put(id, *a)
	return nil
}

func (r alertRepo) DiscardByBillableMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID, at time.Time) (int64, error) {
	alerts, err := r.ListByBillableMetric(ctx, org, metric)
	if err != nil {
		return 0, err
	}
	at = at.UTC()
	for _, a := range alerts {
		a.DeletedAt, a.UpdatedAt = &at, at
		r.d.alerts.put(a.ID, *a)
	}
	return int64(len(alerts)), nil
}

// triggeredAlertRepo implements alert.TriggeredAlertRepository.
type triggeredAlertRepo struct{ d *alertData }

func (r triggeredAlertRepo) Create(_ context.Context, t *alert.TriggeredAlert) error {
	if r.d.triggered.has(t.ID) {
		return conflict("triggered_alert", "id")
	}
	r.d.triggered.put(t.ID, *t)
	return nil
}

func (r triggeredAlertRepo) Get(_ context.Context, org core.OrganizationID, id core.TriggeredAlertID) (*alert.TriggeredAlert, error) {
	t, ok := r.d.triggered.get(id)
	if !ok || t.OrganizationID != org {
		return nil, notFound("triggered_alert", id)
	}
	return t, nil
}

func (r triggeredAlertRepo) List(_ context.Context, org core.OrganizationID, f alert.TriggeredFilter, page core.PageRequest) (core.Page[*alert.TriggeredAlert], error) {
	rows := r.d.triggered.find(func(x *alert.TriggeredAlert) bool {
		return x.OrganizationID == org &&
			(f.AlertID.IsZero() || x.AlertID == f.AlertID) &&
			(f.SubscriptionID.IsZero() || x.SubscriptionID == f.SubscriptionID) &&
			(f.WalletID.IsZero() || x.WalletID == f.WalletID)
	})
	return paginate(rows, func(x *alert.TriggeredAlert) core.TriggeredAlertID { return x.ID }, page)
}

var (
	_ alert.Repository               = alertRepo{}
	_ alert.TriggeredAlertRepository = triggeredAlertRepo{}
)
