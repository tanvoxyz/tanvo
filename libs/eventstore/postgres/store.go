package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// DB is what the store needs from a pgx pool, connection or transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store is the PostgreSQL event store. It is safe for concurrent use.
type Store struct {
	db  DB
	now func() time.Time

	mu     sync.Mutex
	months map[int]bool // months (year*12 + month) known to have partitions
}

var (
	_ metering.Store      = (*Store)(nil)
	_ eventstore.Writer   = (*Store)(nil)
	_ eventstore.Replacer = (*Store)(nil)
)

// Option configures a [Store].
type Option func(*Store)

// WithClock sets the clock giving the IngestedAt of events written without
// one.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// New returns a store over db, whose schema [Migrate] applied.
func New(db DB, opts ...Option) *Store {
	s := &Store{db: db, now: time.Now, months: map[int]bool{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

func monthOf(t time.Time) int {
	t = t.UTC()
	return t.Year()*12 + int(t.Month()) - 1
}

// EnsurePartitions creates the monthly partitions of the events of the
// times, if missing.
func (s *Store) EnsurePartitions(ctx context.Context, times []time.Time) error {
	var missing []time.Time
	s.mu.Lock()
	for _, t := range times {
		m := monthOf(t)
		if !s.months[m] {
			s.months[m] = true
			missing = append(missing, t)
		}
	}
	s.mu.Unlock()
	for _, t := range missing {
		if _, err := s.db.Exec(ctx, "SELECT events_create_partitions($1, $1)", t.UTC()); err != nil {
			s.forgetPartitions()
			return fmt.Errorf("eventstore: postgres: partitions: %w", err)
		}
	}
	return nil
}

// forgetPartitions clears the partitions known to exist, after a write
// failed for want of one (dropped by retention, for example).
func (s *Store) forgetPartitions() {
	s.mu.Lock()
	clear(s.months)
	s.mu.Unlock()
}

// columns are the inputs of a batch insert, as arrays.
type columns struct {
	org, sub, code, tx []string
	ts, ingested       []time.Time
	props              []string
	amount             []*string
}

func toColumns(rows []ingest.Row) (columns, error) {
	c := columns{
		org: make([]string, len(rows)), sub: make([]string, len(rows)), code: make([]string, len(rows)), tx: make([]string, len(rows)),
		ts: make([]time.Time, len(rows)), ingested: make([]time.Time, len(rows)),
		props: make([]string, len(rows)), amount: make([]*string, len(rows)),
	}
	for i, r := range rows {
		c.org[i], c.sub[i], c.code[i], c.tx[i] = r.OrganizationID, r.SubscriptionID, r.Code, r.TransactionID
		c.ts[i], c.ingested[i] = r.Timestamp, r.IngestedAt
		props := r.Properties
		if props == nil {
			props = metering.Properties{}
		}
		b, err := json.Marshal(map[string]string(props))
		if err != nil {
			return columns{}, fmt.Errorf("eventstore: postgres: properties: %w", err)
		}
		c.props[i] = string(b)
		if r.PreciseTotalAmountCents != nil {
			a := r.PreciseTotalAmountCents.String()
			c.amount[i] = &a
		}
	}
	return c, nil
}

func (c columns) args() []any {
	return []any{c.org, c.sub, c.code, c.tx, c.ts, c.props, c.amount, c.ingested}
}

const inputCTE = `input AS (
	SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::timestamptz[], $6::text[], $7::text[], $8::timestamptz[])
		WITH ORDINALITY AS u(organization_id, subscription_id, code, transaction_id, ts, properties, amount, ingested_at, ord)
)`

const insertColumns = `organization_id, subscription_id, code, "timestamp", transaction_id, properties, precise_total_amount_cents, ingested_at, sequence`

// The sequence is drawn in ingestion order: the subquery is ordered by
// position in the batch, and nextval runs over its rows in that order.
const insertValues = `SELECT organization_id, subscription_id, code, ts, transaction_id, properties::jsonb, amount::numeric, ingested_at, nextval('events_sequence')`

const writeSQL = `WITH ` + inputCTE + `,
accepted AS (
	INSERT INTO event_transactions (organization_id, transaction_id, "timestamp", ingested_at)
	SELECT organization_id, transaction_id, ts, ingested_at FROM input ORDER BY ord
	ON CONFLICT DO NOTHING
	RETURNING organization_id, transaction_id
),
stored AS (
	INSERT INTO events (` + insertColumns + `)
	` + insertValues + `
	FROM (SELECT i.* FROM input i JOIN accepted a USING (organization_id, transaction_id) ORDER BY i.ord) s
	ON CONFLICT DO NOTHING
)
SELECT organization_id, transaction_id FROM accepted`

const replaceSQL = `WITH ` + inputCTE + `,
ledger AS (
	INSERT INTO event_transactions (organization_id, transaction_id, "timestamp", ingested_at)
	SELECT DISTINCT ON (organization_id, transaction_id) organization_id, transaction_id, ts, ingested_at
	FROM input ORDER BY organization_id, transaction_id, ord
	ON CONFLICT DO NOTHING
)
INSERT INTO events (` + insertColumns + `)
` + insertValues + `
FROM (SELECT * FROM input ORDER BY ord) s
ON CONFLICT (organization_id, subscription_id, code, "timestamp", transaction_id) DO UPDATE SET
	properties = excluded.properties,
	precise_total_amount_cents = excluded.precise_total_amount_cents,
	ingested_at = excluded.ingested_at,
	sequence = excluded.sequence`

// isMissingPartition reports whether err is PostgreSQL's "no partition of
// relation found for row".
func isMissingPartition(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func (s *Store) withPartitions(ctx context.Context, rows []ingest.Row, run func() error) error {
	times := make([]time.Time, len(rows))
	for i, r := range rows {
		times[i] = r.Timestamp
	}
	for attempt := 0; ; attempt++ {
		if err := s.EnsurePartitions(ctx, times); err != nil {
			return err
		}
		err := run()
		if err == nil || attempt > 0 || !isMissingPartition(err) {
			return err
		}
		s.forgetPartitions()
	}
}

// Write implements [eventstore.Writer].
func (s *Store) Write(ctx context.Context, events []metering.Event) ([]int, error) {
	rows, err := ingest.Prepare(events, s.now())
	if err != nil {
		return nil, fmt.Errorf("eventstore: postgres: write: %w", err)
	}
	kept, duplicates := ingest.FirstPerTransaction(rows)
	if len(kept) == 0 {
		slices.Sort(duplicates)
		return duplicates, nil
	}
	cols, err := toColumns(kept)
	if err != nil {
		return nil, err
	}
	accepted := make(map[ingest.TransactionKey]bool, len(kept))
	err = s.withPartitions(ctx, kept, func() error {
		clear(accepted)
		r, err := s.db.Query(ctx, writeSQL, cols.args()...)
		if err != nil {
			return err
		}
		var k ingest.TransactionKey
		_, err = pgx.ForEachRow(r, []any{&k.OrganizationID, &k.TransactionID}, func() error {
			accepted[k] = true
			return nil
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("eventstore: postgres: write: %w", err)
	}
	for _, r := range kept {
		if !accepted[ingest.TransactionKeyOf(r.Event)] {
			duplicates = append(duplicates, r.Position)
		}
	}
	slices.Sort(duplicates)
	return duplicates, nil
}

// Replace implements [eventstore.Replacer].
func (s *Store) Replace(ctx context.Context, events []metering.Event) error {
	rows, err := ingest.Prepare(events, s.now())
	if err != nil {
		return fmt.Errorf("eventstore: postgres: replace: %w", err)
	}
	rows = ingest.LastPerKey(rows)
	if len(rows) == 0 {
		return nil
	}
	cols, err := toColumns(rows)
	if err != nil {
		return err
	}
	err = s.withPartitions(ctx, rows, func() error {
		_, err := s.db.Exec(ctx, replaceSQL, cols.args()...)
		return err
	})
	if err != nil {
		return fmt.Errorf("eventstore: postgres: replace: %w", err)
	}
	return nil
}
