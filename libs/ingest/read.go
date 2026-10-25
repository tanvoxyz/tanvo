package ingest

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ReaderConfig configures a [Reader].
type ReaderConfig struct {
	// Transactor reads the raw event log of [ModePostgres] and the
	// subscriptions.
	Transactor port.Transactor
	// Mode is the default mode; ModePostgres when empty.
	Mode Mode
	// ModeFor overrides Mode per organization when set.
	ModeFor func(core.OrganizationID) Mode
	// Raw reads the ingestion ledger of [ModeKafka]; nil when no
	// organization ingests through Kafka.
	Raw metering.RawReader
	// Enriched reads the enriched events of [ModeKafka]; nil when no
	// organization ingests through Kafka.
	Enriched metering.EnrichedReader
}

// ErrEnrichedUnavailable reports a read of enriched events by an
// organization ingesting in [ModePostgres], whose events are not stored
// enriched (lago endpoint_not_available).
var ErrEnrichedUnavailable = errors.New("ingest: enriched events are stored in kafka mode only")

// Reader reads ingested events as the event API returns them, from the
// store of each organization's mode: the raw event log in [ModePostgres],
// the ClickHouse ingestion ledger in [ModeKafka] (lago EventsQuery and
// EventsController#show).
type Reader struct {
	cfg ReaderConfig
}

// NewReader returns a reader.
func NewReader(cfg ReaderConfig) (*Reader, error) {
	if cfg.Transactor == nil {
		return nil, fmt.Errorf("ingest: reader: missing transactor")
	}
	if cfg.Mode == "" {
		cfg.Mode = ModePostgres
	}
	return &Reader{cfg: cfg}, nil
}

func (r *Reader) mode(org core.OrganizationID) Mode {
	if r.cfg.ModeFor != nil {
		if m := r.cfg.ModeFor(org); m != "" {
			return m
		}
	}
	return r.cfg.Mode
}

// raw returns the ledger reader of org, or nil in [ModePostgres].
func (r *Reader) raw(org core.OrganizationID) (metering.RawReader, error) {
	if r.mode(org) != ModeKafka {
		return nil, nil
	}
	if r.cfg.Raw == nil {
		return nil, fmt.Errorf("ingest: reader: no event ledger for kafka mode")
	}
	return r.cfg.Raw, nil
}

// Get returns the event with the transaction ID, of the external
// subscription when externalSubscriptionID is set, else the first
// ingested; or core.ErrNotFound.
func (r *Reader) Get(ctx context.Context, org core.OrganizationID, externalSubscriptionID, transactionID string) (*event.Event, error) {
	raw, err := r.raw(org)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		var e *event.Event
		err := r.cfg.Transactor.InTx(ctx, func(ctx context.Context, rs port.Repos) error {
			var err error
			e, err = rs.Events.Get(ctx, org, externalSubscriptionID, transactionID)
			return err
		})
		return e, err
	}
	m, ok, err := raw.RawEvent(ctx, org.String(), externalSubscriptionID, transactionID)
	if err != nil {
		return nil, fmt.Errorf("ingest: event %q: %w", transactionID, err)
	}
	if !ok {
		return nil, fmt.Errorf("event %q: %w", transactionID, core.ErrNotFound)
	}
	return fromMetering(org, m), nil
}

// ListInput selects the events of [Reader.List].
type ListInput struct {
	event.ListFilter
	// FromStartedAt replaces TimestampFrom with the start of the
	// subscription with ExternalSubscriptionID, the latest one (not
	// terminated first, then by started_at descending), when there is
	// one (lago timestamp_from_started_at).
	FromStartedAt bool
}

// List returns the events of org matching in: newest first by timestamp
// in [ModePostgres], by ingestion in [ModeKafka].
func (r *Reader) List(ctx context.Context, org core.OrganizationID, in ListInput, page core.PageRequest) (core.Page[*event.Event], error) {
	f := in.ListFilter
	if in.FromStartedAt && f.ExternalSubscriptionID != "" {
		sub, err := r.latestSubscription(ctx, org, f.ExternalSubscriptionID)
		if err != nil {
			return core.Page[*event.Event]{}, err
		}
		if sub != nil {
			f.TimestampFrom = sub.StartedAt
		}
	}
	raw, err := r.raw(org)
	if err != nil {
		return core.Page[*event.Event]{}, err
	}
	if raw == nil {
		var p core.Page[*event.Event]
		err := r.cfg.Transactor.InTx(ctx, func(ctx context.Context, rs port.Repos) error {
			var err error
			p, err = rs.Events.List(ctx, org, f, page)
			return err
		})
		return p, err
	}
	after, err := decodeRawToken(page.Token)
	if err != nil {
		return core.Page[*event.Event]{}, err
	}
	limit := page.Limit()
	rows, err := raw.RawEvents(ctx, metering.RawFilter{
		OrganizationID: org.String(),
		Code:           f.Code,
		SubscriptionID: f.ExternalSubscriptionID,
		TimestampFrom:  f.TimestampFrom,
		TimestampTo:    f.TimestampTo,
	}, after, limit+1)
	if err != nil {
		return core.Page[*event.Event]{}, fmt.Errorf("ingest: events: %w", err)
	}
	var out core.Page[*event.Event]
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		out.NextToken = encodeRawToken(metering.RawPosition{IngestedAt: last.IngestedAt, SubscriptionID: last.SubscriptionID, TransactionID: last.TransactionID})
	}
	for _, m := range rows {
		out.Items = append(out.Items, fromMetering(org, m))
	}
	return out, nil
}

// ListEnriched returns the enriched events of org matching in, latest
// enriched first, then by external subscription ID and transaction ID
// (lago EventsQuery with enriched, ordered by enriched_at). An
// organization in [ModePostgres] gets [ErrEnrichedUnavailable].
//
// source: app/queries/events_query.rb:14-37
func (r *Reader) ListEnriched(ctx context.Context, org core.OrganizationID, in ListInput, page core.PageRequest) (core.Page[*metering.EnrichedEvent], error) {
	if r.mode(org) != ModeKafka {
		return core.Page[*metering.EnrichedEvent]{}, ErrEnrichedUnavailable
	}
	if r.cfg.Enriched == nil {
		return core.Page[*metering.EnrichedEvent]{}, fmt.Errorf("ingest: reader: no enriched events for kafka mode")
	}
	f, err := r.filter(ctx, org, in)
	if err != nil {
		return core.Page[*metering.EnrichedEvent]{}, err
	}
	pos, err := decodeRawToken(page.Token)
	if err != nil {
		return core.Page[*metering.EnrichedEvent]{}, err
	}
	var after *metering.EnrichedPosition
	if pos != nil {
		after = &metering.EnrichedPosition{EnrichedAt: pos.IngestedAt, SubscriptionID: pos.SubscriptionID, TransactionID: pos.TransactionID}
	}
	limit := page.Limit()
	rows, err := r.cfg.Enriched.EnrichedEvents(ctx, f, after, limit+1)
	if err != nil {
		return core.Page[*metering.EnrichedEvent]{}, fmt.Errorf("ingest: enriched events: %w", err)
	}
	var out core.Page[*metering.EnrichedEvent]
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		out.NextToken = encodeRawToken(metering.RawPosition{IngestedAt: last.EnrichedAt, SubscriptionID: last.SubscriptionID, TransactionID: last.TransactionID})
	}
	for i := range rows {
		out.Items = append(out.Items, &rows[i])
	}
	return out, nil
}

// filter returns the ledger filter of in for org, the subscription's
// start resolved for FromStartedAt.
func (r *Reader) filter(ctx context.Context, org core.OrganizationID, in ListInput) (metering.RawFilter, error) {
	f := in.ListFilter
	if in.FromStartedAt && f.ExternalSubscriptionID != "" {
		sub, err := r.latestSubscription(ctx, org, f.ExternalSubscriptionID)
		if err != nil {
			return metering.RawFilter{}, err
		}
		if sub != nil {
			f.TimestampFrom = sub.StartedAt
		}
	}
	return metering.RawFilter{
		OrganizationID: org.String(),
		Code:           f.Code,
		SubscriptionID: f.ExternalSubscriptionID,
		TimestampFrom:  f.TimestampFrom,
		TimestampTo:    f.TimestampTo,
	}, nil
}

// latestSubscription returns the subscription of org with the external ID
// that lago's EventsQuery picks: not terminated first, then the latest
// terminated, then by started_at descending (unstarted first); or nil.
func (r *Reader) latestSubscription(ctx context.Context, org core.OrganizationID, externalID string) (*subscription.Subscription, error) {
	subs, err := RepoCatalog{Transactor: r.cfg.Transactor}.Subscriptions(ctx, org, externalID)
	if err != nil || len(subs) == 0 {
		return nil, err
	}
	desc := func(a, b *time.Time) int {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		case b == nil:
			return 1
		}
		return b.Compare(*a)
	}
	return slices.MinFunc(subs, func(a, b *subscription.Subscription) int {
		return cmp.Or(desc(a.TerminatedAt, b.TerminatedAt), desc(a.StartedAt, b.StartedAt))
	}), nil
}

// fromMetering returns the event API's view of a ledger event; the ledger
// keeps no event ID.
func fromMetering(org core.OrganizationID, m metering.Event) *event.Event {
	var amount *money.Decimal
	if m.PreciseTotalAmountCents != nil {
		a := *m.PreciseTotalAmountCents
		amount = &a
	}
	return &event.Event{
		OrganizationID:         org,
		TransactionID:          m.TransactionID,
		Code:                   m.Code,
		ExternalSubscriptionID: m.SubscriptionID,
		Timestamp:              m.Timestamp,
		Properties:             m.Properties,
		PreciseTotalAmount:     amount,
		Metadata:               event.Metadata{UserAgent: m.Metadata.UserAgent, IPAddress: m.Metadata.IPAddress},
		IngestedAt:             m.IngestedAt,
		CreatedAt:              m.IngestedAt,
		UpdatedAt:              m.IngestedAt,
	}
}

// rawToken is the page token of a ledger list.
type rawToken struct {
	At  time.Time `json:"a"`
	Sub string    `json:"s"`
	Tx  string    `json:"t"`
}

func encodeRawToken(p metering.RawPosition) string {
	b, _ := json.Marshal(rawToken{At: p.IngestedAt, Sub: p.SubscriptionID, Tx: p.TransactionID})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeRawToken(s string) (*metering.RawPosition, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	var t rawToken
	if err == nil {
		err = json.Unmarshal(b, &t)
	}
	if err != nil || t.At.IsZero() {
		return nil, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	return &metering.RawPosition{IngestedAt: t.At, SubscriptionID: t.Sub, TransactionID: t.Tx}, nil
}
