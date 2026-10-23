package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// All collects every item of a paginated listing.
func All[T any](fetch func(page core.PageRequest) (core.Page[T], error)) ([]T, error) {
	var out []T
	req := core.PageRequest{}
	for {
		p, err := fetch(req)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		if p.NextToken == "" {
			return out, nil
		}
		req.Token = p.NextToken
	}
}

// Emit records a domain event of type t on the object (objectType, id)
// with payload marshaled as JSON (domain §2.3).
func Emit(ctx context.Context, d *Deps, org core.OrganizationID, t webhook.EventType, objectType string, id uuid.UUID, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", t, err)
	}
	e := webhook.Event{
		ID:             core.NewID[core.AuditEventID](d.IDs),
		OrganizationID: org,
		Type:           t,
		ObjectType:     objectType,
		ObjectID:       id,
		Payload:        raw,
		OccurredAt:     d.Now(),
	}
	if err := d.Webhooks.Emit(ctx, e); err != nil {
		return fmt.Errorf("emit %s: %w", t, err)
	}
	return nil
}

// LockInvoice takes the lock of invoice id: operations that read, change
// and write back one invoice row (finalize, refresh, void, retry)
// serialize on it.
func LockInvoice(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) error {
	if err := r.Locks.Lock(ctx, org, port.LockKey{Scope: "invoice", ID: id.UUID()}); err != nil {
		return fmt.Errorf("lock invoice %s: %w", id, err)
	}
	return nil
}

// LockCustomer takes the customer lock of scope s (domain §6).
func LockCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, s customer.LockScope) error {
	if err := r.Locks.Lock(ctx, org, port.CustomerLock(s, c)); err != nil {
		return fmt.Errorf("lock customer %s %s: %w", c, s, err)
	}
	return nil
}

// LockSubscription takes the lock of a subscription.
func LockSubscription(ctx context.Context, r port.Repos, org core.OrganizationID, s core.SubscriptionID) error {
	if err := r.Locks.Lock(ctx, org, port.SubscriptionLock(s)); err != nil {
		return fmt.Errorf("lock subscription %s: %w", s, err)
	}
	return nil
}

// EventLock is the lock serializing the in-advance billing of the event
// with transaction id tx on subscription s; its ID is derived from both,
// since a transaction id is not a UUID.
func EventLock(s core.SubscriptionID, tx string) port.LockKey {
	return port.LockKey{Scope: "event", ID: uuid.NewSHA1(s.UUID(), []byte(tx))}
}

// LockEvent takes the [EventLock] of transaction tx on subscription s
// until the transaction ends.
func LockEvent(ctx context.Context, r port.Repos, org core.OrganizationID, s core.SubscriptionID, tx string) error {
	if err := r.Locks.Lock(ctx, org, EventLock(s, tx)); err != nil {
		return fmt.Errorf("lock event %s: %w", tx, err)
	}
	return nil
}

// RoundCents rounds a minor-unit amount half up to a whole number of
// minor units. Amounts beyond int64 saturate.
func RoundCents(d money.Decimal) int64 {
	n, ok := d.Round(0, money.HalfUp).Int64()
	if !ok {
		if d.IsNegative() {
			return -1 << 63
		}
		return 1<<63 - 1
	}
	return n
}

// Cents returns n minor units as a decimal.
func Cents(n int64) money.Decimal { return money.NewFromInt(n) }

// MeteringTo turns the exclusive end of a half-open period into the
// inclusive bound libs/metering reads (the last microsecond of the
// period, like Rails' end_of_day stored in PostgreSQL).
func MeteringTo(to time.Time) time.Time { return to.Add(-time.Microsecond) }

// MeteringMetric converts a billable metric to the metering view.
func MeteringMetric(m *catalog.BillableMetric) metering.Metric {
	out := metering.Metric{
		Code:             m.Code,
		Aggregation:      metering.AggregationType(m.AggregationType),
		Recurring:        m.Recurring,
		RoundingFunction: metering.RoundingFunction(m.RoundingFunction),
	}
	if m.FieldName != nil {
		out.FieldName = *m.FieldName
	}
	if m.RoundingPrecision != nil {
		out.RoundingPrecision = int32(*m.RoundingPrecision) //nolint:gosec // precision is validated small
	}
	return out
}

// MeteringMetricFilters converts the filters of a billable metric.
func MeteringMetricFilters(m *catalog.BillableMetric) []metering.MetricFilter {
	out := make([]metering.MetricFilter, 0, len(m.Filters))
	for _, f := range m.Filters {
		out = append(out, metering.MetricFilter{Key: f.Key, Values: f.Values})
	}
	return out
}

// MeteringChargeFilters converts the filters of a charge.
func MeteringChargeFilters(c *catalog.Charge) []metering.ChargeFilter {
	out := make([]metering.ChargeFilter, 0, len(c.Filters))
	for _, f := range c.Filters {
		out = append(out, metering.ChargeFilter{ID: f.ID.String(), Values: f.Values, CreatedAt: f.CreatedAt})
	}
	return out
}

// MeteringRequest returns the base aggregation request of a charge's
// metric over the half-open charges period p of a subscription.
//
// An event at the termination instant belongs to no period: the event's
// subscription satisfies started_at <= timestamp < terminated_at (domain
// §10.2). lago reads timestamp <= terminated_at instead
// (app/services/subscriptions/dates_service.rb:125, app/models/event.rb:19),
// which counts an event at that instant on both sides of a plan change.
func MeteringRequest(env *SubscriptionEnv, m *catalog.BillableMetric, p period.Period, periodDays int) metering.Request {
	return metering.Request{
		Metric:                  MeteringMetric(m),
		OrganizationID:          env.OrganizationID().String(),
		SubscriptionID:          env.Subscription.ExternalID,
		From:                    p.From,
		To:                      MeteringTo(p.To),
		DaysTo:                  meteringDaysTo(env, p),
		PeriodDays:              periodDays,
		Location:                env.Location(),
		HasPreviousSubscription: env.Previous != nil,
	}
}

// meteringDaysTo returns the end the billed days of p are counted to: the
// termination instant when the subscription's termination ends p, as lago's
// to_datetime (app/services/subscriptions/terminated_dates_service.rb), so
// that a termination at local midnight bills the day it ends on
// (Utils::Datetime.date_diff_with_timezone adds a second to an end at the
// beginning of a day); otherwise the metering bound of p, like lago's
// end_of_day.
//
// lago replaces the end only when terminated_at <= the end_of_day of the
// natural period (dates_service.rb:125), so a termination at the very end
// of the period (a downgrade) keeps it.
func meteringDaysTo(env *SubscriptionEnv, p period.Period) time.Time {
	if t := env.Subscription.TerminatedAt; t != nil && p.To.Equal(*t) && cutShort(env, p) {
		return *t
	}
	return MeteringTo(p.To)
}

// cutShort reports whether the termination ending charges period p cut it
// before its natural end.
func cutShort(env *SubscriptionEnv, p period.Period) bool {
	ps := env.Period()
	ps.TerminatedAt = time.Time{}
	d, err := ps.Dates(MeteringTo(p.To), period.CurrentUsage)
	if err != nil {
		return false
	}
	natural, ok := d.ChargesPeriod()
	return ok && natural.To.After(p.To)
}

// Enqueue encodes args and enqueues one job of kind on its queue (see
// [QueueOf]); uniqueKey and runAt are optional.
func Enqueue(ctx context.Context, r port.Repos, org core.OrganizationID, kind string, args any, uniqueKey string, runAt time.Time) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("encode %s args: %w", kind, err)
	}
	job := port.Job{Kind: kind, Queue: QueueOf(kind), OrganizationID: org, Args: raw, UniqueKey: uniqueKey, RunAt: runAt}
	if err := r.Jobs.Enqueue(ctx, job); err != nil {
		return fmt.Errorf("enqueue %s: %w", kind, err)
	}
	return nil
}

// ScopeUUID returns a deterministic identifier for a pair of objects, used
// as the scope of sequences counted per pair.
func ScopeUUID(a, b uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(a, b[:])
}
