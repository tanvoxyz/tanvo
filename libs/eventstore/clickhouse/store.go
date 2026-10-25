package clickhouse

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Store is the ClickHouse event store. It is safe for concurrent use.
type Store struct {
	conn driver.Conn
	now  func() time.Time

	heal  time.Duration
	locks keyLocks
	// hook, when set (by tests), is called at named stages of a write.
	hook func(stage string)

	mu   sync.Mutex
	last uint64 // last ingestion sequence assigned
}

var (
	_ metering.Store      = (*Store)(nil)
	_ eventstore.Writer   = (*Store)(nil)
	_ eventstore.Replacer = (*Store)(nil)
)

// Option configures a [Store].
type Option func(*Store)

// WithClock sets the clock giving the IngestedAt of events written without
// one, and seeding the ingestion sequence.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithHealWindow sets how recent the stored copy of a duplicate must be for
// [Store.Write] to recompute its bucket (default one hour): a retry of a
// write that failed after its insert heals the buckets that write left
// stale. Older duplicates are left to [Store.RepairBuckets]; zero turns
// the healing off.
func WithHealWindow(d time.Duration) Option { return func(s *Store) { s.heal = d } }

// New returns a store over conn, whose schema [Migrate] applied.
func New(conn driver.Conn, opts ...Option) *Store {
	s := &Store{conn: conn, now: time.Now, heal: time.Hour}
	for _, o := range opts {
		o(s)
	}
	return s
}

// sequences reserves n ingestion sequence numbers and returns the first:
// the clock in nanoseconds, kept increasing within the process and above
// every sequence the store observed.
func (s *Store) sequences(n int) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := max(s.last+1, uint64(s.now().UnixNano()))
	s.last = first + uint64(n) - 1
	return first
}

// observe raises the sequences the store assigns above seq, a sequence
// another writer, whose clock may run ahead, gave a stored copy: a copy
// that must win over it (a replacement, a tombstone) gets a higher one.
func (s *Store) observe(seq uint64) {
	s.mu.Lock()
	s.last = max(s.last, seq)
	s.mu.Unlock()
}

const enrichedInsert = `INSERT INTO events_enriched (organization_id, code, subscription_id, timestamp, transaction_id, properties, precise_total_amount_cents, ingested_at, sequence, value, attribution_labels)`

const rawInsert = `INSERT INTO events_raw (organization_id, subscription_id, transaction_id, code, timestamp, properties, precise_total_amount_cents, ingested_at, sequence, replaced, user_agent, ip_address)`

type row struct {
	ingest.Row
	props    map[string]string
	amount   *string
	sequence uint64
}

func (s *Store) rows(in []ingest.Row) []row {
	first := s.sequences(len(in))
	out := make([]row, len(in))
	for i, r := range in {
		out[i] = row{Row: r, props: map[string]string(r.Properties), sequence: first + uint64(i)}
		if out[i].props == nil {
			out[i].props = map[string]string{}
		}
		if r.PreciseTotalAmountCents != nil {
			a := r.PreciseTotalAmountCents.String()
			out[i].amount = &a
		}
	}
	return out
}

// labelsOf returns the attribution labels to store: an empty map for none.
func labelsOf(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// insert writes the rows to events_enriched, then to events_raw, marked
// replaced or not.
func (s *Store) insert(ctx context.Context, rows []row, replaced bool) error {
	var rep uint8
	if replaced {
		rep = 1
	}
	b, err := s.conn.PrepareBatch(ctx, enrichedInsert)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := b.Append(r.OrganizationID, r.Code, r.SubscriptionID, r.Timestamp, r.TransactionID, r.props, r.amount, r.IngestedAt, r.sequence, r.Value, labelsOf(r.AttributionLabels)); err != nil {
			_ = b.Abort()
			return err
		}
	}
	if err := b.Send(); err != nil {
		return err
	}
	b, err = s.conn.PrepareBatch(ctx, rawInsert)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := b.Append(r.OrganizationID, r.SubscriptionID, r.TransactionID, r.Code, r.Timestamp, r.props, r.amount, r.IngestedAt, r.sequence, rep, r.Metadata.UserAgent, r.Metadata.IPAddress); err != nil {
			_ = b.Abort()
			return err
		}
	}
	return b.Send()
}

// stored is the current copy of a deduplication key: where it is (code,
// timestamp), its sequence, and the highest sequence of every copy written
// under the key.
type stored struct {
	row
	highest uint64
}

// known returns the current copy of the deduplication keys of the rows
// already in the ledger.
func (s *Store) known(ctx context.Context, rows []ingest.Row) (map[ingest.TransactionKey]stored, error) {
	type scope struct{ org, sub string }
	byScope := map[scope][]string{}
	var scopes []scope
	for _, r := range rows {
		k := scope{r.OrganizationID, r.SubscriptionID}
		if _, ok := byScope[k]; !ok {
			scopes = append(scopes, k)
		}
		byScope[k] = append(byScope[k], r.TransactionID)
	}
	byOrg := map[string][]scope{}
	var orgs []string
	for _, k := range scopes {
		if _, ok := byOrg[k.org]; !ok {
			orgs = append(orgs, k.org)
		}
		byOrg[k.org] = append(byOrg[k.org], k)
	}
	out := map[ingest.TransactionKey]stored{}
	for _, org := range orgs {
		var subs, ids []string
		for _, k := range byOrg[org] {
			for _, id := range byScope[k] {
				subs, ids = append(subs, k.sub), append(ids, id)
			}
		}
		// Bound the size of the statement, which binds the keys as text.
		for i := 0; i < len(ids); i += lookupChunk {
			j := min(i+lookupChunk, len(ids))
			if err := s.lookup(ctx, org, subs[i:j], ids[i:j], out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// lookupChunk is the number of keys looked up per statement.
const lookupChunk = 2000

func (s *Store) lookup(ctx context.Context, org string, subs, ids []string, out map[ingest.TransactionKey]stored) error {
	var c stored
	return s.each(consistent(ctx), `SELECT subscription_id, transaction_id, argMax(code, version), argMax(timestamp, version),
		argMax(sequence, version), max(sequence)
		FROM events_raw WHERE organization_id = $1 AND subscription_id IN $2 AND transaction_id IN $3
			AND has(arrayZip(CAST($4 AS Array(String)), CAST($5 AS Array(String))), (subscription_id, transaction_id))
		GROUP BY 1, 2`,
		args{org, set(subs), set(ids), subs, ids}, []any{&c.SubscriptionID, &c.TransactionID, &c.Code, &c.Timestamp, &c.sequence, &c.highest}, func() error {
			c.OrganizationID = org
			c.Timestamp = c.Timestamp.UTC()
			out[ingest.TransactionKeyOf(c.Event)] = c
			return nil
		})
}

// Write implements [eventstore.Writer].
//
// A write reads the ledger for the deduplication keys of the batch, drops
// the known ones and the repeats within the batch, inserts events_enriched
// then events_raw, then maintains the buckets: it merges its new events
// into them, and recomputes those of the duplicates whose stored copy is
// within the heal window and above every event their bucket holds (the
// copies of a write that failed before its maintenance).
func (s *Store) Write(ctx context.Context, events []metering.Event) ([]int, error) {
	rows, err := ingest.Prepare(events, s.now())
	if err != nil {
		return nil, fmt.Errorf("eventstore: clickhouse: write: %w", err)
	}
	kept, duplicates := ingest.FirstPerTransaction(rows)
	if len(kept) == 0 {
		slices.Sort(duplicates)
		return duplicates, nil
	}
	known, err := s.known(ctx, kept)
	if err != nil {
		return nil, fmt.Errorf("eventstore: clickhouse: write: %w", err)
	}
	var (
		accepted []ingest.Row
		heal     []row
		healFrom = uint64(max(s.now().Add(-s.heal).UnixNano(), 0))
	)
	for _, r := range kept {
		c, ok := known[ingest.TransactionKeyOf(r.Event)]
		if !ok {
			accepted = append(accepted, r)
			continue
		}
		duplicates = append(duplicates, r.Position)
		if s.heal > 0 && c.sequence >= healFrom {
			heal = append(heal, c.row)
		}
	}
	var stored []row
	if len(accepted) > 0 {
		stored = s.rows(accepted)
		if err := s.insert(consistent(ctx), stored, false); err != nil {
			return nil, fmt.Errorf("eventstore: clickhouse: write: %w", err)
		}
	}
	if err := s.maintain(ctx, stored, nil, heal); err != nil {
		return nil, fmt.Errorf("eventstore: clickhouse: write: %w", err)
	}
	slices.Sort(duplicates)
	return duplicates, nil
}

// Replace implements [eventstore.Replacer]. The replacement takes the
// place of the current copy of its deduplication key: a copy moving to
// another code or timestamp tombstones the one it replaces in
// events_enriched. Replaced copies may move to other dimension values and
// take back a maximum or a latest value: the buckets of the new and the
// replaced copies are recomputed, and their rows the copies left
// tombstoned.
func (s *Store) Replace(ctx context.Context, events []metering.Event) error {
	rows, err := ingest.Prepare(events, s.now())
	if err != nil {
		return fmt.Errorf("eventstore: clickhouse: replace: %w", err)
	}
	rows = ingest.LastPerKey(rows)
	if len(rows) == 0 {
		return nil
	}
	known, err := s.known(ctx, rows)
	if err != nil {
		return fmt.Errorf("eventstore: clickhouse: replace: %w", err)
	}
	var moved []row
	for _, r := range rows {
		c, ok := known[ingest.TransactionKeyOf(r.Event)]
		if !ok {
			continue
		}
		// The replacement and the tombstone win over every copy of the
		// key, whatever the clocks of their writers.
		s.observe(c.highest)
		if c.Code != r.Code || !c.Timestamp.Equal(r.Timestamp) {
			moved = append(moved, c.row)
		}
	}
	stored := s.rows(rows)
	if len(moved) > 0 {
		if err := s.tombstone(consistent(ctx), moved); err != nil {
			return fmt.Errorf("eventstore: clickhouse: replace: %w", err)
		}
	}
	if err := s.insert(consistent(ctx), stored, true); err != nil {
		return fmt.Errorf("eventstore: clickhouse: replace: %w", err)
	}
	if err := s.maintain(ctx, nil, append(stored, moved...), nil); err != nil {
		return fmt.Errorf("eventstore: clickhouse: replace: %w", err)
	}
	return nil
}

// tombstone deletes the copies from events_enriched, with sequences above
// theirs.
func (s *Store) tombstone(ctx context.Context, copies []row) error {
	first := s.sequences(len(copies))
	b, err := s.conn.PrepareBatch(ctx, `INSERT INTO events_enriched (organization_id, code, subscription_id, timestamp, transaction_id, ingested_at, sequence, is_deleted)`)
	if err != nil {
		return err
	}
	for i, r := range copies {
		if err := b.Append(r.OrganizationID, r.Code, r.SubscriptionID, r.Timestamp, r.TransactionID, r.Timestamp, first+uint64(i), uint8(1)); err != nil {
			_ = b.Abort()
			return err
		}
	}
	return b.Send()
}
