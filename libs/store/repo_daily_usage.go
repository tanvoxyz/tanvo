package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// dailyUsageRepo implements subscription.DailyUsageRepository.
type dailyUsageRepo struct{ t *txState }

var _ subscription.DailyUsageRepository = dailyUsageRepo{}

// dateCol converts a date to a date column value.
func dateCol(d core.Date) time.Time { return d.Midnight(time.UTC) }

// dateOfCol converts a date column value.
func dateOfCol(t time.Time) core.Date {
	y, m, d := t.Date()
	return core.NewDate(y, m, d)
}

// optDateOf converts a nullable date column.
func optDateOf(t *time.Time) *core.Date {
	if t == nil {
		return nil
	}
	d := dateOfCol(*t)
	return &d
}

// emptyObject is the stored form of a nil usage diff.
var emptyObject = []byte("{}")

func dailyUsageOf(r db.DailyUsage) (*subscription.DailyUsage, error) {
	d := &subscription.DailyUsage{
		ID:                     idOf[core.DailyUsageID](r.ID),
		OrganizationID:         idOf[core.OrganizationID](r.OrganizationID),
		CustomerID:             idOf[core.CustomerID](r.CustomerID),
		SubscriptionID:         idOf[core.SubscriptionID](r.SubscriptionID),
		ExternalSubscriptionID: r.ExternalSubscriptionID,
		From:                   utc(r.FromDatetime),
		To:                     utc(r.ToDatetime),
		RefreshedAt:            utc(r.RefreshedAt),
		UsageDate:              dateOfCol(r.UsageDate),
		CreatedAt:              utc(r.CreatedAt),
		UpdatedAt:              utc(r.UpdatedAt),
	}
	if err := json.Unmarshal(r.Usage, &d.Usage); err != nil {
		return nil, fmt.Errorf("daily usage %s: decode usage: %w", r.ID, err)
	}
	if string(r.UsageDiff) != string(emptyObject) {
		d.UsageDiff = &subscription.UsageSnapshot{}
		if err := json.Unmarshal(r.UsageDiff, d.UsageDiff); err != nil {
			return nil, fmt.Errorf("daily usage %s: decode usage diff: %w", r.ID, err)
		}
	}
	return d, nil
}

func (r dailyUsageRepo) Create(ctx context.Context, d *subscription.DailyUsage) error {
	q, err := r.t.queries(ctx, d.OrganizationID)
	if err != nil {
		return err
	}
	usage, err := json.Marshal(d.Usage)
	if err != nil {
		return fmt.Errorf("create daily usage %s: encode usage: %w", d.ID, err)
	}
	diff := emptyObject
	if d.UsageDiff != nil {
		if diff, err = json.Marshal(d.UsageDiff); err != nil {
			return fmt.Errorf("create daily usage %s: encode usage diff: %w", d.ID, err)
		}
	}
	err = q.CreateDailyUsage(ctx, db.CreateDailyUsageParams{
		OrganizationID:         d.OrganizationID.UUID(),
		ID:                     d.ID.UUID(),
		CustomerID:             d.CustomerID.UUID(),
		SubscriptionID:         d.SubscriptionID.UUID(),
		ExternalSubscriptionID: d.ExternalSubscriptionID,
		Usage:                  usage,
		UsageDiff:              diff,
		FromDatetime:           utc(d.From),
		ToDatetime:             utc(d.To),
		RefreshedAt:            utc(d.RefreshedAt),
		UsageDate:              dateCol(d.UsageDate),
		CreatedAt:              utc(d.CreatedAt),
		UpdatedAt:              utc(d.UpdatedAt),
	})
	if err != nil {
		return fmt.Errorf("create daily usage %s: %w", d.ID, mapErr(err))
	}
	return nil
}

func (r dailyUsageRepo) GetByDate(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, day core.Date) (*subscription.DailyUsage, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDailyUsageByDate(ctx, db.GetDailyUsageByDateParams{
		OrganizationID: org.UUID(), SubscriptionID: sub.UUID(), UsageDate: dateCol(day),
	})
	if err != nil {
		return nil, fmt.Errorf("get daily usage of subscription %s on %s: %w", sub, day, mapErr(err))
	}
	return dailyUsageOf(row)
}

func (r dailyUsageRepo) GetByPeriodAndDate(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, from, to time.Time, day core.Date) (*subscription.DailyUsage, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDailyUsageByPeriodAndDate(ctx, db.GetDailyUsageByPeriodAndDateParams{
		OrganizationID: org.UUID(), SubscriptionID: sub.UUID(), UsageDate: dateCol(day),
		FromDatetime: utc(from), ToDatetime: utc(to),
	})
	if err != nil {
		return nil, fmt.Errorf("get daily usage of subscription %s on %s: %w", sub, day, mapErr(err))
	}
	return dailyUsageOf(row)
}

func (r dailyUsageRepo) Previous(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, from, to time.Time, day core.Date) (*subscription.DailyUsage, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPreviousDailyUsage(ctx, db.GetPreviousDailyUsageParams{
		OrganizationID: org.UUID(), SubscriptionID: sub.UUID(), UsageDate: dateCol(day),
		FromDatetime: utc(from), ToDatetime: utc(to),
	})
	if err != nil {
		return nil, fmt.Errorf("get daily usage of subscription %s before %s: %w", sub, day, mapErr(err))
	}
	return dailyUsageOf(row)
}

func (r dailyUsageRepo) ListDaysBetween(ctx context.Context, org core.OrganizationID, from, to core.Date) ([]subscription.DailyUsageDay, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListDailyUsageDaysBetween(ctx, db.ListDailyUsageDaysBetweenParams{
		OrganizationID: org.UUID(), Since: dateCol(from), Until: dateCol(to),
	})
	if err != nil {
		return nil, fmt.Errorf("list daily usage days: %w", mapErr(err))
	}
	out := make([]subscription.DailyUsageDay, len(rows))
	for i, row := range rows {
		out[i] = subscription.DailyUsageDay{SubscriptionID: idOf[core.SubscriptionID](row.SubscriptionID), UsageDate: dateOfCol(row.UsageDate)}
	}
	return out, nil
}

func (r dailyUsageRepo) ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, page core.PageRequest) (core.Page[*subscription.DailyUsage], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*subscription.DailyUsage]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*subscription.DailyUsage]{}, err
	}
	rows, err := q.ListDailyUsagesBySubscription(ctx, db.ListDailyUsagesBySubscriptionParams{
		OrganizationID: org.UUID(), SubscriptionID: sub.UUID(), After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*subscription.DailyUsage]{}, fmt.Errorf("list daily usages of subscription %s: %w", sub, mapErr(err))
	}
	return pageOf(rows, page, dailyUsageOf, func(d *subscription.DailyUsage) string { return d.ID.String() })
}

// RecordEventReceived implements subscription.Repository.
func (r subscriptionRepo) RecordEventReceived(ctx context.Context, org core.OrganizationID, id core.SubscriptionID, day core.Date) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.RecordSubscriptionEventReceived(ctx, db.RecordSubscriptionEventReceivedParams{
		OrganizationID: org.UUID(), ID: id.UUID(), Day: dateCol(day),
	}); err != nil {
		return fmt.Errorf("record event received by subscription %s: %w", id, mapErr(err))
	}
	return nil
}

func (r dailyUsageRepo) DeleteFrom(ctx context.Context, org core.OrganizationID, subs []core.SubscriptionID, from core.Date, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("delete daily usages: limit %d: %w", limit, core.ErrInvalidValue)
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.DeleteDailyUsagesFrom(ctx, db.DeleteDailyUsagesFromParams{
		OrganizationID: org.UUID(), SubscriptionIds: uuids(subs), Since: dateCol(from), Lim: int32(min(limit, math.MaxInt32)), // bounded
	})
	if err != nil {
		return 0, fmt.Errorf("delete daily usages: %w", mapErr(err))
	}
	return int(n), nil
}
