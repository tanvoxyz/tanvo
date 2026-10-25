package clickhouse

import (
	"context"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var _ metering.EnrichedReader = (*Store)(nil)

// EnrichedEvents implements [metering.EnrichedReader]: the current copies
// of events_enriched, which FINAL keeps and whose tombstones it hides.
func (s *Store) EnrichedEvents(ctx context.Context, f metering.RawFilter, after *metering.EnrichedPosition, limit int) ([]metering.EnrichedEvent, error) {
	if f.OrganizationID == "" {
		return nil, wrap("enriched events", eventstore.ErrMissingOrganization)
	}
	var a args
	where := []string{"organization_id = " + a.add(f.OrganizationID)}
	if f.SubscriptionID != "" {
		where = append(where, "subscription_id = "+a.add(f.SubscriptionID))
	}
	if f.Code != "" {
		where = append(where, "code = "+a.add(f.Code))
	}
	if f.TimestampFrom != nil {
		where = append(where, "timestamp >= "+a.time(*f.TimestampFrom))
	}
	if f.TimestampTo != nil {
		where = append(where, "timestamp <= "+a.time(*f.TimestampTo))
	}
	if after != nil {
		at := a.time(after.EnrichedAt)
		sub, tx := a.add(after.SubscriptionID), a.add(after.TransactionID)
		where = append(where, "(enriched_at < "+at+" OR (enriched_at = "+at+" AND (subscription_id > "+sub+" OR (subscription_id = "+sub+" AND transaction_id > "+tx+"))))")
	}
	sql := `SELECT subscription_id, transaction_id, code, timestamp, properties,
		if(isNull(precise_total_amount_cents), NULL, toString(precise_total_amount_cents)), ingested_at,
		value, if(isNull(decimal_value), NULL, toString(decimal_value)), enriched_at
		FROM events_enriched FINAL WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY enriched_at DESC, subscription_id, transaction_id LIMIT ` + a.add(limit)
	var (
		e              metering.EnrichedEvent
		props          map[string]string
		amount, number *string
		out            []metering.EnrichedEvent
	)
	dest := []any{&e.SubscriptionID, &e.TransactionID, &e.Code, &e.Timestamp, &props, &amount, &e.IngestedAt, &e.Value, &number, &e.EnrichedAt}
	err := s.each(ctx, sql, a, dest, func() error {
		ev := e
		ev.OrganizationID = f.OrganizationID
		ev.Timestamp, ev.IngestedAt, ev.EnrichedAt = ev.Timestamp.UTC(), ev.IngestedAt.UTC(), ev.EnrichedAt.UTC()
		ev.Properties = metering.Properties(props)
		ev.PreciseTotalAmountCents, ev.DecimalValue = nil, nil
		for _, p := range []struct {
			s *string
			d **money.Decimal
		}{{amount, &ev.PreciseTotalAmountCents}, {number, &ev.DecimalValue}} {
			if p.s == nil {
				continue
			}
			d, err := money.Parse(*p.s)
			if err != nil {
				return err
			}
			*p.d = &d
		}
		if ev.Value != nil {
			v := *ev.Value
			ev.Value = &v
		}
		out = append(out, ev)
		return nil
	})
	return out, wrap("enriched events", err)
}
