package clickhouse

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Store is the ClickHouse event store. It is safe for concurrent use.
type Store struct {
	conn driver.Conn
	now  func() time.Time

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

// New returns a store over conn, whose schema [Migrate] applied.
func New(conn driver.Conn, opts ...Option) *Store {
	s := &Store{conn: conn, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// sequences reserves n ingestion sequence numbers and returns the first:
// the clock in nanoseconds, kept increasing within the process.
func (s *Store) sequences(n int) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := max(s.last+1, uint64(s.now().UnixNano()))
	s.last = first + uint64(n) - 1
	return first
}

const enrichedInsert = `INSERT INTO events_enriched (organization_id, code, subscription_id, timestamp, transaction_id, properties, precise_total_amount_cents, ingested_at, sequence)`

const rawInsert = `INSERT INTO events_raw (organization_id, transaction_id, subscription_id, code, timestamp, properties, precise_total_amount_cents, ingested_at, sequence)`

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

// insert writes the rows to events_enriched, then to events_raw.
func (s *Store) insert(ctx context.Context, rows []row) error {
	b, err := s.conn.PrepareBatch(ctx, enrichedInsert)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := b.Append(r.OrganizationID, r.Code, r.SubscriptionID, r.Timestamp, r.TransactionID, r.props, r.amount, r.IngestedAt, r.sequence); err != nil {
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
		if err := b.Append(r.OrganizationID, r.TransactionID, r.SubscriptionID, r.Code, r.Timestamp, r.props, r.amount, r.IngestedAt, r.sequence); err != nil {
			_ = b.Abort()
			return err
		}
	}
	return b.Send()
}

// known returns the transaction keys of the rows already in the ledger.
func (s *Store) known(ctx context.Context, rows []ingest.Row) (map[ingest.TransactionKey]bool, error) {
	byOrg := map[string][]any{}
	var orgs []string
	for _, r := range rows {
		if _, ok := byOrg[r.OrganizationID]; !ok {
			orgs = append(orgs, r.OrganizationID)
		}
		byOrg[r.OrganizationID] = append(byOrg[r.OrganizationID], r.TransactionID)
	}
	out := map[ingest.TransactionKey]bool{}
	for _, org := range orgs {
		// Bound the size of the statement, which binds the IDs as text.
		for ids := range slices.Chunk(byOrg[org], lookupChunk) {
			if err := s.lookup(ctx, org, ids, out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// lookupChunk is the number of transaction IDs looked up per statement.
const lookupChunk = 2000

func (s *Store) lookup(ctx context.Context, org string, ids []any, out map[ingest.TransactionKey]bool) error {
	r, err := s.conn.Query(consistent(ctx), `SELECT DISTINCT transaction_id FROM events_raw WHERE organization_id = $1 AND transaction_id IN $2`,
		org, ch.GroupSet{Value: ids})
	if err != nil {
		return err
	}
	defer r.Close()
	for r.Next() {
		var tx string
		if err := r.Scan(&tx); err != nil {
			return err
		}
		out[ingest.TransactionKey{OrganizationID: org, TransactionID: tx}] = true
	}
	return r.Err()
}

// Write implements [eventstore.Writer].
func (s *Store) Write(ctx context.Context, events []metering.Event) ([]int, error) {
	rows, err := ingest.Prepare(events, s.now())
	if err != nil {
		return nil, fmt.Errorf("eventstore: clickhouse: write: %w", err)
	}
	kept, duplicates := ingest.FirstPerTransaction(rows)
	if len(kept) > 0 {
		known, err := s.known(ctx, kept)
		if err != nil {
			return nil, fmt.Errorf("eventstore: clickhouse: write: %w", err)
		}
		var accepted, seen []ingest.Row
		for _, r := range kept {
			if known[ingest.TransactionKeyOf(r.Event)] {
				duplicates = append(duplicates, r.Position)
				seen = append(seen, r)
			} else {
				accepted = append(accepted, r)
			}
		}
		stored := s.rows(accepted)
		if len(stored) > 0 {
			if err := s.insert(consistent(ctx), stored); err != nil {
				return nil, fmt.Errorf("eventstore: clickhouse: write: %w", err)
			}
		}
		// The buckets of the events already in the ledger are maintained
		// too: a write that failed after its insert left them stale, and
		// its retry finds its events there. Recomputing a bucket is
		// idempotent.
		touched := stored
		for _, r := range seen {
			touched = append(touched, row{Row: r})
		}
		if len(touched) > 0 {
			if err := s.maintain(ctx, touched, false); err != nil {
				return nil, fmt.Errorf("eventstore: clickhouse: write: %w", err)
			}
		}
	}
	slices.Sort(duplicates)
	return duplicates, nil
}

// Replace implements [eventstore.Replacer].
func (s *Store) Replace(ctx context.Context, events []metering.Event) error {
	rows, err := ingest.Prepare(events, s.now())
	if err != nil {
		return fmt.Errorf("eventstore: clickhouse: replace: %w", err)
	}
	rows = ingest.LastPerKey(rows)
	if len(rows) == 0 {
		return nil
	}
	stored := s.rows(rows)
	if err := s.insert(consistent(ctx), stored); err != nil {
		return fmt.Errorf("eventstore: clickhouse: replace: %w", err)
	}
	// A replaced copy may move to other dimension values and take back a
	// maximum or a latest value: its buckets are recomputed, and their rows
	// it left tombstoned.
	if err := s.maintain(ctx, stored, true); err != nil {
		return fmt.Errorf("eventstore: clickhouse: replace: %w", err)
	}
	return nil
}
