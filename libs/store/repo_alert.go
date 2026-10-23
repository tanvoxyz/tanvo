package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindAlert binds the repositories of the alert aggregates.
func (t *txState) bindAlert(r *port.Repos) {
	r.Alerts = alertRepo{t}
	r.TriggeredAlerts = triggeredAlertRepo{t}
}

// alertRepo implements alert.Repository.
type alertRepo struct{ t *txState }

var _ alert.Repository = alertRepo{}

func alertParams(a *alert.Alert) db.CreateAlertParams {
	p := db.CreateAlertParams{
		OrganizationID:         a.OrganizationID.UUID(),
		ID:                     a.ID.UUID(),
		Code:                   a.Code,
		Name:                   a.Name,
		AlertType:              string(a.Type),
		SubscriptionExternalID: optString(a.SubscriptionExternalID),
		WalletID:               optUUID(a.WalletID),
		BillableMetricID:       optUUID(a.BillableMetricID),
		Direction:              string(a.Direction),
		PreviousValue:          a.PreviousValue,
		LastProcessedAt:        optUTC(a.LastProcessedAt),
		CreatedAt:              utc(a.CreatedAt),
		UpdatedAt:              utc(a.UpdatedAt),
	}
	return p
}

func alertOf(r db.Alert, ths []alert.Threshold) (*alert.Alert, error) {
	return &alert.Alert{
		ID:                     idOf[core.AlertID](r.ID),
		OrganizationID:         idOf[core.OrganizationID](r.OrganizationID),
		Code:                   r.Code,
		Name:                   r.Name,
		Type:                   alert.Type(r.AlertType),
		SubscriptionExternalID: deref(r.SubscriptionExternalID),
		WalletID:               optIDOf[core.WalletID](r.WalletID),
		BillableMetricID:       optIDOf[core.BillableMetricID](r.BillableMetricID),
		Direction:              alert.Direction(r.Direction),
		Thresholds:             ths,
		PreviousValue:          r.PreviousValue,
		LastProcessedAt:        optUTC(r.LastProcessedAt),
		CreatedAt:              utc(r.CreatedAt),
		UpdatedAt:              utc(r.UpdatedAt),
		DeletedAt:              optUTC(r.DeletedAt),
	}, nil
}

// withThresholds converts alert rows, loading their thresholds in one
// query.
func (r alertRepo) withThresholds(ctx context.Context, q *db.Queries, org core.OrganizationID, rows []db.Alert) ([]*alert.Alert, error) {
	if len(rows) == 0 {
		return []*alert.Alert{}, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	trows, err := q.ListAlertThresholds(ctx, db.ListAlertThresholdsParams{OrganizationID: org.UUID(), AlertIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list alert thresholds: %w", mapErr(err))
	}
	by := make(map[uuid.UUID][]alert.Threshold, len(rows))
	for _, t := range trows {
		by[t.AlertID] = append(by[t.AlertID], alert.Threshold{
			ID: idOf[core.AlertThresholdID](t.ID), Code: t.Code, Value: t.Value, Recurring: t.Recurring,
		})
	}
	out := make([]*alert.Alert, len(rows))
	for i, row := range rows {
		a, err := alertOf(row, by[row.ID])
		if err != nil {
			return nil, err
		}
		out[i] = a
	}
	return out, nil
}

// one converts a single alert row.
func (r alertRepo) one(ctx context.Context, q *db.Queries, org core.OrganizationID, row db.Alert) (*alert.Alert, error) {
	out, err := r.withThresholds(ctx, q, org, []db.Alert{row})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// writeThresholds replaces the thresholds of a.
func (r alertRepo) writeThresholds(ctx context.Context, q *db.Queries, a *alert.Alert, replace bool) error {
	org, id := a.OrganizationID.UUID(), a.ID.UUID()
	if replace {
		if err := q.DeleteAlertThresholds(ctx, db.DeleteAlertThresholdsParams{OrganizationID: org, AlertID: id}); err != nil {
			return err
		}
	}
	if len(a.Thresholds) == 0 {
		return nil
	}
	p := db.InsertAlertThresholdsParams{
		OrganizationID:  org,
		AlertID:         id,
		Ids:             make([]uuid.UUID, len(a.Thresholds)),
		Codes:           make([]string, len(a.Thresholds)),
		ThresholdValues: make([]string, len(a.Thresholds)),
		Recurrings:      make([]bool, len(a.Thresholds)),
	}
	for i, t := range a.Thresholds {
		p.Ids[i] = t.ID.UUID()
		p.Codes[i] = deref(t.Code)
		p.ThresholdValues[i] = t.Value.String()
		p.Recurrings[i] = t.Recurring
	}
	return q.InsertAlertThresholds(ctx, p)
}

func (r alertRepo) Create(ctx context.Context, a *alert.Alert) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateAlert(ctx, alertParams(a))
	if err := insertErr("alert", n, err); err != nil {
		return err
	}
	if err := r.writeThresholds(ctx, q, a, false); err != nil {
		return fmt.Errorf("create alert %s: %w", a.ID, mapErr(err))
	}
	return nil
}

func (r alertRepo) Update(ctx context.Context, a *alert.Alert) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	// The code and the wallet's type are unique: guard the update.
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateAlert(ctx, db.UpdateAlertParams(alertParams(a)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeThresholds(ctx, q, a, true)
	})
	return updateErr("alert", a.ID, n, err)
}

func (r alertRepo) Get(ctx context.Context, org core.OrganizationID, id core.AlertID) (*alert.Alert, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetAlert(ctx, db.GetAlertParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("alert", id, err)
	}
	return r.one(ctx, q, org, row)
}

func (r alertRepo) GetBySubscriptionCode(ctx context.Context, org core.OrganizationID, subscriptionExternalID, code string) (*alert.Alert, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetAlertBySubscriptionCode(ctx, db.GetAlertBySubscriptionCodeParams{
		OrganizationID: org.UUID(), SubscriptionExternalID: &subscriptionExternalID, Code: code,
	})
	if err != nil {
		return nil, getErr("alert", code, err)
	}
	return r.one(ctx, q, org, row)
}

func (r alertRepo) GetByWalletCode(ctx context.Context, org core.OrganizationID, wallet core.WalletID, code string) (*alert.Alert, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetAlertByWalletCode(ctx, db.GetAlertByWalletCodeParams{OrganizationID: org.UUID(), WalletID: optUUID(wallet), Code: code})
	if err != nil {
		return nil, getErr("alert", code, err)
	}
	return r.one(ctx, q, org, row)
}

func (r alertRepo) ListBySubscription(ctx context.Context, org core.OrganizationID, subscriptionExternalID string) ([]*alert.Alert, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAlertsBySubscription(ctx, db.ListAlertsBySubscriptionParams{OrganizationID: org.UUID(), SubscriptionExternalID: &subscriptionExternalID})
	if err != nil {
		return nil, fmt.Errorf("list alerts of subscription %q: %w", subscriptionExternalID, mapErr(err))
	}
	return r.withThresholds(ctx, q, org, rows)
}

func (r alertRepo) ListByWallet(ctx context.Context, org core.OrganizationID, wallet core.WalletID) ([]*alert.Alert, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAlertsByWallet(ctx, db.ListAlertsByWalletParams{OrganizationID: org.UUID(), WalletID: optUUID(wallet)})
	if err != nil {
		return nil, fmt.Errorf("list alerts of wallet %s: %w", wallet, mapErr(err))
	}
	return r.withThresholds(ctx, q, org, rows)
}

func (r alertRepo) ListByBillableMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID) ([]*alert.Alert, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAlertsByBillableMetric(ctx, db.ListAlertsByBillableMetricParams{OrganizationID: org.UUID(), BillableMetricID: optUUID(metric)})
	if err != nil {
		return nil, fmt.Errorf("list alerts of billable metric %s: %w", metric, mapErr(err))
	}
	return r.withThresholds(ctx, q, org, rows)
}

func (r alertRepo) List(ctx context.Context, org core.OrganizationID, f alert.Filter, page core.PageRequest) (core.Page[*alert.Alert], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*alert.Alert]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*alert.Alert]{}, err
	}
	rows, err := q.ListAlerts(ctx, db.ListAlertsParams{
		OrganizationID:         org.UUID(),
		SubscriptionExternalID: optString(f.SubscriptionExternalID),
		WalletID:               optUUID(f.WalletID),
		AlertTypes:             enumStrings(f.Types),
		After:                  after,
		Lim:                    lim,
	})
	if err != nil {
		return core.Page[*alert.Alert]{}, fmt.Errorf("list alerts: %w", mapErr(err))
	}
	if n := page.Limit(); len(rows) > n {
		// Load the thresholds of the page only; pageOf still sees the
		// extra row and sets the next token.
		alerts, err := r.withThresholds(ctx, q, org, rows[:n])
		if err != nil {
			return core.Page[*alert.Alert]{}, err
		}
		return core.Page[*alert.Alert]{Items: alerts, NextToken: alerts[len(alerts)-1].ID.String()}, nil
	}
	alerts, err := r.withThresholds(ctx, q, org, rows)
	if err != nil {
		return core.Page[*alert.Alert]{}, err
	}
	return core.Page[*alert.Alert]{Items: alerts}, nil
}

func (r alertRepo) Discard(ctx context.Context, org core.OrganizationID, id core.AlertID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if _, err := q.GetAlert(ctx, db.GetAlertParams{OrganizationID: org.UUID(), ID: id.UUID()}); err != nil {
		return getErr("alert", id, err)
	}
	if err := q.DeleteAlertThresholds(ctx, db.DeleteAlertThresholdsParams{OrganizationID: org.UUID(), AlertID: id.UUID()}); err != nil {
		return err
	}
	n, err := q.DiscardAlert(ctx, db.DiscardAlertParams{OrganizationID: org.UUID(), ID: id.UUID(), DeletedAt: new(at.UTC())})
	return updateErr("alert", id, n, err)
}

func (r alertRepo) DiscardByBillableMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID, at time.Time) (int64, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	return q.DiscardAlertsByBillableMetric(ctx, db.DiscardAlertsByBillableMetricParams{
		OrganizationID: org.UUID(), BillableMetricID: new(metric.UUID()), DeletedAt: new(at.UTC()),
	})
}

// triggeredAlertRepo implements alert.TriggeredAlertRepository.
type triggeredAlertRepo struct{ t *txState }

var _ alert.TriggeredAlertRepository = triggeredAlertRepo{}

// alertCrossedJSON is the stored form of one alert.CrossedThreshold.
type alertCrossedJSON struct {
	Code      *string       `json:"code,omitempty"`
	Value     money.Decimal `json:"value"`
	Recurring bool          `json:"recurring,omitempty"`
}

func triggeredAlertOf(r db.TriggeredAlert) (*alert.TriggeredAlert, error) {
	var crossed []alertCrossedJSON
	if err := decodeJSON(r.CrossedThresholds, &crossed); err != nil {
		return nil, fmt.Errorf("triggered alert %s: crossed thresholds: %w", r.ID, err)
	}
	ths := make([]alert.CrossedThreshold, len(crossed))
	for i, c := range crossed {
		ths[i] = alert.CrossedThreshold(c)
	}
	return &alert.TriggeredAlert{
		ID:                idOf[core.TriggeredAlertID](r.ID),
		OrganizationID:    idOf[core.OrganizationID](r.OrganizationID),
		AlertID:           idOf[core.AlertID](r.AlertID),
		SubscriptionID:    optIDOf[core.SubscriptionID](r.SubscriptionID),
		WalletID:          optIDOf[core.WalletID](r.WalletID),
		CurrentValue:      r.CurrentValue,
		PreviousValue:     r.PreviousValue,
		CrossedThresholds: ths,
		TriggeredAt:       utc(r.TriggeredAt),
		CreatedAt:         utc(r.CreatedAt),
		UpdatedAt:         utc(r.UpdatedAt),
	}, nil
}

func (r triggeredAlertRepo) Create(ctx context.Context, t *alert.TriggeredAlert) error {
	q, err := r.t.queries(ctx, t.OrganizationID)
	if err != nil {
		return err
	}
	crossed := make([]alertCrossedJSON, len(t.CrossedThresholds))
	for i, c := range t.CrossedThresholds {
		crossed[i] = alertCrossedJSON(c)
	}
	doc, err := encodeJSON(crossed)
	if err != nil {
		return fmt.Errorf("create triggered_alert %s: %w", t.ID, err)
	}
	p := db.CreateTriggeredAlertParams{
		OrganizationID:    t.OrganizationID.UUID(),
		ID:                t.ID.UUID(),
		AlertID:           t.AlertID.UUID(),
		SubscriptionID:    optUUID(t.SubscriptionID),
		WalletID:          optUUID(t.WalletID),
		CurrentValue:      t.CurrentValue,
		PreviousValue:     t.PreviousValue,
		CrossedThresholds: doc,
		TriggeredAt:       utc(t.TriggeredAt),
		CreatedAt:         utc(t.CreatedAt),
		UpdatedAt:         utc(t.UpdatedAt),
	}
	n, err := q.CreateTriggeredAlert(ctx, p)
	return insertErr("triggered_alert", n, err)
}

func (r triggeredAlertRepo) Get(ctx context.Context, org core.OrganizationID, id core.TriggeredAlertID) (*alert.TriggeredAlert, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetTriggeredAlert(ctx, db.GetTriggeredAlertParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("triggered_alert", id, err)
	}
	return triggeredAlertOf(row)
}

func (r triggeredAlertRepo) List(ctx context.Context, org core.OrganizationID, f alert.TriggeredFilter, page core.PageRequest) (core.Page[*alert.TriggeredAlert], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*alert.TriggeredAlert]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*alert.TriggeredAlert]{}, err
	}
	rows, err := q.ListTriggeredAlerts(ctx, db.ListTriggeredAlertsParams{
		OrganizationID: org.UUID(),
		AlertID:        optUUID(f.AlertID),
		SubscriptionID: optUUID(f.SubscriptionID),
		WalletID:       optUUID(f.WalletID),
		After:          after,
		Lim:            lim,
	})
	if err != nil {
		return core.Page[*alert.TriggeredAlert]{}, fmt.Errorf("list triggered alerts: %w", mapErr(err))
	}
	return pageOf(rows, page, triggeredAlertOf, func(t *alert.TriggeredAlert) string { return t.ID.String() })
}
