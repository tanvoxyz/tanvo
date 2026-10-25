package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	db     DB
	now    func() time.Time
	schema string

	// backfillChunk is the number of events a backfill transaction reads.
	backfillChunk int

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

// WithBackfillChunk sets the number of events each transaction of a
// bucket backfill reads (see [Store.DefineBuckets]); 10,000 by default.
func WithBackfillChunk(n int) Option {
	return func(s *Store) {
		if n > 0 {
			s.backfillChunk = n
		}
	}
}

// WithSchema sets the PostgreSQL schema holding the event store, the
// schema of [MigrateOptions]: every table, sequence and function the store
// uses is qualified with it, whatever the search_path of db. Without it,
// they are unqualified and resolved by the search_path. A name
// [ValidateSchema] rejects makes every method of the store fail.
func WithSchema(name string) Option { return func(s *Store) { s.schema = name } }

// New returns a store over db, whose schema [Migrate] applied.
func New(db DB, opts ...Option) *Store {
	s := &Store{now: time.Now, backfillChunk: 10_000, months: map[int]bool{}}
	for _, o := range opts {
		o(s)
	}
	q, err := newQualifier(s.schema)
	if err != nil {
		s.db = failedDB{err: err}
	} else {
		s.db = schemaDB{db: db, q: q}
	}
	return s
}

// DropPartitions drops the monthly partitions of the events ending at or
// before the given time, and their usage buckets (a retention policy). The
// ledger keeps the transaction IDs, so that ingestion still rejects them.
// It returns the number of months dropped.
func (s *Store) DropPartitions(ctx context.Context, before time.Time) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx, "SELECT __schema__.events_drop_partitions($1)", before.UTC()).Scan(&n); err != nil {
		return 0, fmt.Errorf("eventstore: postgres: drop partitions: %w", err)
	}
	s.forgetPartitions()
	return n, nil
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
		if _, err := s.db.Exec(ctx, "SELECT __schema__.events_create_partitions($1, $1)", t.UTC()); err != nil {
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
const insertValues = `SELECT organization_id, subscription_id, code, ts, transaction_id, properties::jsonb, amount::numeric, ingested_at, nextval('__schema__.events_sequence')`

const writeSQL = `WITH ` + inputCTE + `,
accepted AS (
	INSERT INTO __schema__.event_transactions (organization_id, subscription_id, transaction_id, code, "timestamp", ingested_at)
	SELECT organization_id, subscription_id, transaction_id, code, ts, ingested_at FROM input ORDER BY ord
	ON CONFLICT DO NOTHING
	RETURNING organization_id, subscription_id, transaction_id
),
stored AS (
	INSERT INTO __schema__.events (` + insertColumns + `)
	` + insertValues + `
	FROM (SELECT i.* FROM input i JOIN accepted a USING (organization_id, subscription_id, transaction_id) ORDER BY i.ord) s
	ON CONFLICT DO NOTHING
)
SELECT organization_id, subscription_id, transaction_id FROM accepted`

// replaceSQL stores the replacements: it deletes the events of the copies
// they move ($9 to $13: organization, subscription, code, timestamp,
// transaction ID), points the ledger at them, and upserts them.
const replaceSQL = `WITH ` + inputCTE + `,
moved AS (
	DELETE FROM __schema__.events e
	USING unnest($9::text[], $10::text[], $11::text[], $12::timestamptz[], $13::text[]) AS m(o, s, c, ts, t)
	WHERE e.organization_id = m.o AND e.subscription_id = m.s AND e.code = m.c AND e."timestamp" = m.ts AND e.transaction_id = m.t
),
ledger AS (
	INSERT INTO __schema__.event_transactions (organization_id, subscription_id, transaction_id, code, "timestamp", ingested_at)
	SELECT organization_id, subscription_id, transaction_id, code, ts, ingested_at FROM input
	ON CONFLICT (organization_id, subscription_id, transaction_id) DO UPDATE SET
		code = excluded.code,
		"timestamp" = excluded."timestamp"
)
INSERT INTO __schema__.events (` + insertColumns + `)
` + insertValues + `
FROM (SELECT * FROM input ORDER BY ord) s
ON CONFLICT (organization_id, subscription_id, code, "timestamp", transaction_id) DO UPDATE SET
	properties = excluded.properties,
	precise_total_amount_cents = excluded.precise_total_amount_cents,
	ingested_at = excluded.ingested_at,
	sequence = excluded.sequence,
	written_at = clock_timestamp()`

// currentSQL returns where the ledger has the events of the deduplication
// keys ($1 to $3: organization, subscription, transaction ID).
const currentSQL = `SELECT l.organization_id, l.subscription_id, l.transaction_id, l.code, l."timestamp"
FROM unnest($1::text[], $2::text[], $3::text[]) AS u(o, s, t)
JOIN __schema__.event_transactions l ON l.organization_id = u.o AND l.subscription_id = u.s AND l.transaction_id = u.t`

// copyAt is where the ledger has the event of a deduplication key.
type copyAt struct {
	code string
	ts   time.Time
}

// current returns where the ledger has the events of the rows' keys,
// locking the ledger rows with lock.
func current(ctx context.Context, tx pgx.Tx, c columns, lock bool) (map[ingest.TransactionKey]copyAt, error) {
	sql := currentSQL
	if lock {
		sql += " ORDER BY 1, 2, 3 FOR UPDATE OF l"
	}
	r, err := tx.Query(ctx, sql, c.org, c.sub, c.tx)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	out := map[ingest.TransactionKey]copyAt{}
	var (
		k  ingest.TransactionKey
		at copyAt
	)
	_, err = pgx.ForEachRow(r, []any{&k.OrganizationID, &k.SubscriptionID, &k.TransactionID, &at.code, &at.ts}, func() error {
		at.ts = at.ts.UTC()
		out[k] = at
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	return out, nil
}

// movedCopies returns the copies the rows move to another code or
// timestamp.
func movedCopies(rows []ingest.Row, at map[ingest.TransactionKey]copyAt) []ingest.Row {
	var out []ingest.Row
	for _, r := range rows {
		c, ok := at[ingest.TransactionKeyOf(r.Event)]
		if !ok || (c.code == r.Code && c.ts.Equal(r.Timestamp)) {
			continue
		}
		m := r
		m.Code, m.Timestamp = c.code, c.ts
		out = append(out, m)
	}
	return out
}

// maxReplaceAttempts bounds the attempts of a replacement racing other
// replacements of its events.
const maxReplaceAttempts = 5

// errLedgerMoved reports that a concurrent replacement moved an event
// between the read of the ledger and its lock.
var errLedgerMoved = errors.New("ledger moved under the replacement")

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
		return s.inTx(ctx, func(tx pgx.Tx) error {
			buckets, err := lockBuckets(ctx, tx, cols)
			if err != nil {
				return err
			}
			sql := writeSQL
			if buckets {
				sql = writeBucketsSQL
			}
			r, err := tx.Query(ctx, sql, cols.args()...)
			if err != nil {
				return err
			}
			var k ingest.TransactionKey
			_, err = pgx.ForEachRow(r, []any{&k.OrganizationID, &k.SubscriptionID, &k.TransactionID}, func() error {
				accepted[k] = true
				return nil
			})
			return err
		})
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
	replace := func(tx pgx.Tx) error {
		// The buckets are locked before the ledger rows, as writes do: the
		// ledger is read first to find the buckets of the moved copies,
		// then locked and read again.
		at, err := current(ctx, tx, cols, false)
		if err != nil {
			return err
		}
		moved := movedCopies(rows, at)
		touched, err := toColumns(append(slices.Clip(rows), moved...))
		if err != nil {
			return err
		}
		buckets, err := lockBuckets(ctx, tx, touched)
		if err != nil {
			return err
		}
		locked, err := current(ctx, tx, cols, true)
		if err != nil {
			return err
		}
		if !maps.Equal(at, locked) {
			return errLedgerMoved
		}
		m, err := toColumns(moved)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, replaceSQL, append(cols.args(), m.org, m.sub, m.code, m.ts, m.tx)...); err != nil {
			return err
		}
		if !buckets {
			return nil
		}
		// A replaced copy may leave its bucket row (another bucket, other
		// dimension values, a value no longer numeric) and take back a
		// maximum or a latest value: the touched buckets, of the new and
		// the moved copies, are aggregated again.
		if _, err := tx.Exec(ctx, deleteTouchedSQL, touched.lockArgs()...); err != nil {
			return fmt.Errorf("delete buckets: %w", err)
		}
		if _, err := tx.Exec(ctx, recomputeTouchedSQL, touched.lockArgs()...); err != nil {
			return fmt.Errorf("recompute buckets: %w", err)
		}
		return nil
	}
	err = s.withPartitions(ctx, rows, func() error {
		for attempt := 0; ; attempt++ {
			err := s.inTx(ctx, replace)
			if !errors.Is(err, errLedgerMoved) || attempt == maxReplaceAttempts-1 {
				return err
			}
		}
	})
	if err != nil {
		return fmt.Errorf("eventstore: postgres: replace: %w", err)
	}
	return nil
}
