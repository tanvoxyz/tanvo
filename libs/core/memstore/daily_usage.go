package memstore

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// cloneDailyUsage deep-copies a snapshot through its JSON form.
func cloneDailyUsage(d subscription.DailyUsage) subscription.DailyUsage {
	d.Usage = cloneUsageSnapshot(d.Usage)
	if d.UsageDiff != nil {
		c := cloneUsageSnapshot(*d.UsageDiff)
		d.UsageDiff = &c
	}
	return d
}

func cloneUsageSnapshot(u subscription.UsageSnapshot) subscription.UsageSnapshot {
	b, err := json.Marshal(u)
	if err != nil {
		panic(err) // a snapshot always encodes
	}
	var c subscription.UsageSnapshot
	if err := json.Unmarshal(b, &c); err != nil {
		panic(err)
	}
	return c
}

// dailyUsageRepo implements subscription.DailyUsageRepository.
type dailyUsageRepo struct{ d *subscriptionData }

var _ subscription.DailyUsageRepository = dailyUsageRepo{}

func (r dailyUsageRepo) Create(_ context.Context, d *subscription.DailyUsage) error {
	if r.d.dailyUsages.has(d.ID) {
		return conflict("daily_usage", "id")
	}
	r.d.dailyUsages.put(d.ID, *d)
	return nil
}

func (r dailyUsageRepo) GetByDate(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, day core.Date) (*subscription.DailyUsage, error) {
	d, ok := r.d.dailyUsages.first(func(x *subscription.DailyUsage) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub && x.UsageDate == day
	})
	if !ok {
		return nil, notFound("daily_usage", stringer(sub.String()+"@"+day.String()))
	}
	return d, nil
}

func (r dailyUsageRepo) GetByPeriodAndDate(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, from, to time.Time, day core.Date) (*subscription.DailyUsage, error) {
	d, ok := r.d.dailyUsages.first(func(x *subscription.DailyUsage) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub && x.UsageDate == day && x.From.Equal(from) && x.To.Equal(to)
	})
	if !ok {
		return nil, notFound("daily_usage", stringer(sub.String()+"@"+day.String()))
	}
	return d, nil
}

func (r dailyUsageRepo) Previous(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, from, to time.Time, day core.Date) (*subscription.DailyUsage, error) {
	rows := r.d.dailyUsages.find(func(x *subscription.DailyUsage) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub && x.From.Equal(from) && x.To.Equal(to) && x.UsageDate.Before(day)
	})
	if len(rows) == 0 {
		return nil, notFound("daily_usage", stringer(sub.String()+"<"+day.String()))
	}
	// Latest day first; among equal days, the first created (rows are in
	// creation order and the sort is stable).
	slices.SortStableFunc(rows, func(a, b *subscription.DailyUsage) int { return b.UsageDate.Compare(a.UsageDate) })
	return rows[0], nil
}

func (r dailyUsageRepo) ListDaysBetween(_ context.Context, org core.OrganizationID, from, to core.Date) ([]subscription.DailyUsageDay, error) {
	var out []subscription.DailyUsageDay
	r.d.dailyUsages.find(func(x *subscription.DailyUsage) bool {
		if x.OrganizationID == org && !x.UsageDate.Before(from) && !x.UsageDate.After(to) {
			out = append(out, subscription.DailyUsageDay{SubscriptionID: x.SubscriptionID, UsageDate: x.UsageDate})
		}
		return false
	})
	slices.SortFunc(out, func(a, b subscription.DailyUsageDay) int {
		return cmp.Or(a.SubscriptionID.Compare(b.SubscriptionID), a.UsageDate.Compare(b.UsageDate))
	})
	return slices.Compact(out), nil
}

func (r dailyUsageRepo) ListBySubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, page core.PageRequest) (core.Page[*subscription.DailyUsage], error) {
	rows := r.d.dailyUsages.find(func(x *subscription.DailyUsage) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub
	})
	return paginate(rows, func(x *subscription.DailyUsage) core.DailyUsageID { return x.ID }, page)
}

// RecordEventReceived implements subscription.Repository.
func (r subscriptionRepo) RecordEventReceived(_ context.Context, org core.OrganizationID, id core.SubscriptionID, day core.Date) error {
	s, ok := r.d.subscriptions.get(id)
	if !ok || s.OrganizationID != org || s.Status != subscription.StatusActive ||
		s.LastReceivedEventOn != nil && *s.LastReceivedEventOn == day {
		return nil
	}
	s.LastReceivedEventOn = &day
	r.d.subscriptions.put(id, *s)
	return nil
}

func (r dailyUsageRepo) DeleteFrom(_ context.Context, org core.OrganizationID, subs []core.SubscriptionID, from core.Date, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("delete daily usages: limit %d: %w", limit, core.ErrInvalidValue)
	}
	n := 0
	for _, d := range r.d.dailyUsages.find(func(x *subscription.DailyUsage) bool {
		return x.OrganizationID == org && slices.Contains(subs, x.SubscriptionID) && !x.UsageDate.Before(from)
	}) {
		if n == limit {
			break
		}
		r.d.dailyUsages.del(d.ID)
		n++
	}
	return n, nil
}
