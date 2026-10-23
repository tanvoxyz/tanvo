package clickhouse

import (
	"context"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var _ metering.RawReader = (*Store)(nil)

// rawCurrent selects the current copy of each deduplication key of the
// ledger rows matching where: the copy with the highest version, which
// FINAL would keep. The tuples keep NULL amounts, which argMax skips.
func rawCurrent(where string) string {
	return `SELECT subscription_id AS sub, transaction_id AS tx,
		argMax(code, version) AS c, argMax(timestamp, version) AS ts,
		argMax(properties, version) AS props,
		argMax(tuple(precise_total_amount_cents), version).1 AS amount,
		argMax(ingested_at, version) AS ing, min(sequence) AS first,
		argMax(user_agent, version) AS ua, argMax(ip_address, version) AS ip
		FROM events_raw WHERE ` + where + ` GROUP BY sub, tx`
}

const rawColumns = `sub, tx, c, ts, props, if(isNull(amount), NULL, toString(amount)), ing, ua, ip`

// scanRaw reads the events of a statement selecting [rawColumns].
func (s *Store) scanRaw(ctx context.Context, org, sql string, a args) ([]metering.Event, error) {
	var (
		e      metering.Event
		props  map[string]string
		amount *string
		out    []metering.Event
	)
	dest := []any{&e.SubscriptionID, &e.TransactionID, &e.Code, &e.Timestamp, &props, &amount, &e.IngestedAt, &e.Metadata.UserAgent, &e.Metadata.IPAddress}
	err := s.each(ctx, sql, a, dest, func() error {
		ev := e
		ev.OrganizationID = org
		ev.Timestamp, ev.IngestedAt = ev.Timestamp.UTC(), ev.IngestedAt.UTC()
		ev.Properties = metering.Properties(props)
		if amount != nil {
			d, err := money.Parse(*amount)
			if err != nil {
				return err
			}
			ev.PreciseTotalAmountCents = &d
		}
		out = append(out, ev)
		return nil
	})
	return out, err
}

// RawEvent implements [metering.RawReader].
func (s *Store) RawEvent(ctx context.Context, org, sub, tx string) (metering.Event, bool, error) {
	if org == "" {
		return metering.Event{}, false, wrap("raw event", eventstore.ErrMissingOrganization)
	}
	var a args
	where := "organization_id = " + a.add(org) + " AND transaction_id = " + a.add(tx)
	if sub != "" {
		where += " AND subscription_id = " + a.add(sub)
	}
	events, err := s.scanRaw(ctx, org, "SELECT "+rawColumns+" FROM ("+rawCurrent(where)+") ORDER BY first, sub LIMIT 1", a)
	if err != nil || len(events) == 0 {
		return metering.Event{}, false, wrap("raw event", err)
	}
	return events[0], true, nil
}

// RawEvents implements [metering.RawReader].
func (s *Store) RawEvents(ctx context.Context, f metering.RawFilter, after *metering.RawPosition, limit int) ([]metering.Event, error) {
	if f.OrganizationID == "" {
		return nil, wrap("raw events", eventstore.ErrMissingOrganization)
	}
	var a args
	inner := []string{"organization_id = " + a.add(f.OrganizationID)}
	if f.SubscriptionID != "" {
		inner = append(inner, "subscription_id = "+a.add(f.SubscriptionID))
	}
	outer := []string{"1"}
	if f.Code != "" {
		outer = append(outer, "c = "+a.add(f.Code))
	}
	if f.TimestampFrom != nil {
		outer = append(outer, "ts >= "+a.time(*f.TimestampFrom))
	}
	if f.TimestampTo != nil {
		outer = append(outer, "ts <= "+a.time(*f.TimestampTo))
	}
	if after != nil {
		at := a.time(after.IngestedAt)
		sub, tx := a.add(after.SubscriptionID), a.add(after.TransactionID)
		outer = append(outer, "(ing < "+at+" OR (ing = "+at+" AND (sub > "+sub+" OR (sub = "+sub+" AND tx > "+tx+"))))")
	}
	sql := "SELECT " + rawColumns + " FROM (" + rawCurrent(strings.Join(inner, " AND ")) + ") WHERE " +
		strings.Join(outer, " AND ") + " ORDER BY ing DESC, sub, tx LIMIT " + a.add(limit)
	events, err := s.scanRaw(ctx, f.OrganizationID, sql, a)
	return events, wrap("raw events", err)
}
