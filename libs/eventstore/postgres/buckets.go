package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/calc"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var (
	_ metering.BucketReader    = (*Store)(nil)
	_ eventstore.BucketDefiner = (*Store)(nil)
)

// The classes of the advisory locks of the usage buckets: shared by the
// writers and exclusive for the definers per (organization, code), and
// exclusive per (organization, subscription, code, bucket) for the writers
// of a bucket. The keys hash the schema with the key, so that the event
// stores of two schemas of a database do not contend.
const (
	lockDefinitions = 0x7476_0001
	lockBucket      = 0x7476_0002
)

// The bucket of a timestamp expression, and the lock key of its bucket.
const (
	bucketOf      = `date_bin('15 minutes', %s, TIMESTAMPTZ '2001-01-01 00:00:00+00')`
	bucketLockKey = `hashtext('__schema__.' || %[1]s || chr(31) || %[2]s || chr(31) || %[3]s || chr(31) || extract(epoch FROM ` + bucketOf + `)::bigint::text)`
)

// lockDefinitionsSQL takes the shared definitions lock of the (organization,
// code) pairs of the batch, in order.
var lockDefinitionsSQL = fmt.Sprintf(`SELECT pg_advisory_xact_lock_shared(%d, k) FROM (
	SELECT DISTINCT hashtext('__schema__.' || o || chr(31) || c) AS k FROM unnest($1::text[], $2::text[]) AS u(o, c) ORDER BY 1
) s`, lockDefinitions)

// lockBucketsSQL takes the locks of the buckets of the batch whose code has
// definitions, in order, and returns one row per lock: none when no
// definition applies to the batch.
var lockBucketsSQL = fmt.Sprintf(`SELECT pg_advisory_xact_lock(%d, k) FROM (
	SELECT DISTINCT `+fmt.Sprintf(bucketLockKey, "u.o", "u.s", "u.c", "u.ts")+` AS k
	FROM unnest($1::text[], $2::text[], $3::text[], $4::timestamptz[]) AS u(o, s, c, ts)
	WHERE EXISTS (SELECT 1 FROM __schema__.usage_bucket_definitions d WHERE d.organization_id = u.o AND d.code = u.c)
	ORDER BY 1
) s`, lockBucket)

// bucketRows returns the SELECT aggregating the events of src, a relation of
// (organization_id, subscription_id, code, ts, sequence, properties, amount,
// ingested_at), into the rows of the buckets of their definitions.
func bucketRows(src, where string) string {
	return `SELECT s.organization_id, s.subscription_id, s.code, d.definition_id,
		` + fmt.Sprintf(bucketOf, "s.ts") + ` AS bucket,
		__schema__.usage_bucket_dimensions(s.properties, d.dimensions, d.fold) AS dimensions,
		sha256(convert_to(__schema__.usage_bucket_dimensions(s.properties, d.dimensions, d.fold)::text, 'UTF8')),
		count(*), coalesce(sum(s.amount), 0), max(s.ts), max(s.ingested_at),
		count(x.value), coalesce(sum(x.value), 0), max(x.value),
		(array_agg(x.value ORDER BY s.ts DESC, s.sequence DESC) FILTER (WHERE x.value IS NOT NULL))[1],
		max(s.ts) FILTER (WHERE x.value IS NOT NULL),
		(array_agg(s.sequence ORDER BY s.ts DESC, s.sequence DESC) FILTER (WHERE x.value IS NOT NULL))[1],
		coalesce(sum(s.amount) FILTER (WHERE x.value IS NOT NULL), 0)
	FROM ` + src + ` s
	JOIN __schema__.usage_bucket_definitions d ON d.organization_id = s.organization_id AND d.code = s.code
	CROSS JOIN LATERAL (
		SELECT CASE WHEN d.property <> '' AND s.properties->>d.property ~ ` + numericPattern + `
			THEN (s.properties->>d.property)::numeric END AS value
	) x
	` + where + `
	GROUP BY 1, 2, 3, 4, 5, 6, 7`
}

const bucketColumns = `organization_id, subscription_id, code, definition_id, bucket, dimensions, dimensions_hash,
	events_count, precise_total_amount_cents, last_event_at, last_ingested_at,
	value_count, units, max_value, latest_value, latest_at, latest_sequence, value_precise_total_amount_cents`

// writeBucketsSQL is writeSQL adding the accepted events to their buckets.
var writeBucketsSQL = `WITH ` + inputCTE + `,
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
	RETURNING organization_id, subscription_id, code, "timestamp" AS ts, sequence, properties,
		precise_total_amount_cents AS amount, ingested_at
),
bucketed AS (
	INSERT INTO __schema__.usage_buckets AS b (` + bucketColumns + `)
	` + bucketRows("stored", "") + `
	ON CONFLICT (organization_id, subscription_id, code, definition_id, bucket, dimensions_hash) DO UPDATE SET
		events_count = b.events_count + excluded.events_count,
		precise_total_amount_cents = b.precise_total_amount_cents + excluded.precise_total_amount_cents,
		last_event_at = greatest(b.last_event_at, excluded.last_event_at),
		last_ingested_at = greatest(b.last_ingested_at, excluded.last_ingested_at),
		value_count = b.value_count + excluded.value_count,
		units = b.units + excluded.units,
		max_value = greatest(b.max_value, excluded.max_value),
		latest_value = CASE WHEN ` + newerLatest + ` THEN excluded.latest_value ELSE b.latest_value END,
		latest_at = CASE WHEN ` + newerLatest + ` THEN excluded.latest_at ELSE b.latest_at END,
		latest_sequence = CASE WHEN ` + newerLatest + ` THEN excluded.latest_sequence ELSE b.latest_sequence END,
		value_precise_total_amount_cents = b.value_precise_total_amount_cents + excluded.value_precise_total_amount_cents
)
SELECT organization_id, subscription_id, transaction_id FROM accepted`

// newerLatest reports whether the latest value of the added events is more
// recent than the latest value of the bucket.
const newerLatest = `(excluded.latest_at IS NOT NULL AND (b.latest_at IS NULL OR
	(excluded.latest_at, excluded.latest_sequence) > (b.latest_at, b.latest_sequence)))`

// touchedCTE is the buckets of a batch of events, from the arrays of
// lockBucketsSQL.
const touchedCTE = `touched AS (
	SELECT DISTINCT u.o AS organization_id, u.s AS subscription_id, u.c AS code, ` + `date_bin('15 minutes', u.ts, TIMESTAMPTZ '2001-01-01 00:00:00+00')` + ` AS bucket
	FROM unnest($1::text[], $2::text[], $3::text[], $4::timestamptz[]) AS u(o, s, c, ts)
)`

// The recomputation of the buckets a replacement touched: their rows are
// deleted, then aggregated again from the events, in two statements so
// that the second reads the events the replacement stored.
const (
	deleteTouchedSQL = `WITH ` + touchedCTE + `
DELETE FROM __schema__.usage_buckets b USING touched t
WHERE b.organization_id = t.organization_id AND b.subscription_id = t.subscription_id AND b.code = t.code AND b.bucket = t.bucket`

	recomputeEvents = `(
	SELECT e.organization_id, e.subscription_id, e.code, e."timestamp" AS ts, e.sequence, e.properties,
		e.precise_total_amount_cents AS amount, e.ingested_at
	FROM touched t JOIN __schema__.events e ON e.organization_id = t.organization_id AND e.subscription_id = t.subscription_id
		AND e.code = t.code AND e."timestamp" >= t.bucket AND e."timestamp" < t.bucket + interval '15 minutes'
)`
)

var recomputeTouchedSQL = `WITH ` + touchedCTE + `
INSERT INTO __schema__.usage_buckets (` + bucketColumns + `)
` + bucketRows(recomputeEvents, "")

// The statements of a backfill chunk: the events of (organization $1,
// code $2) from the (subscription, timestamp) key ($3, $4) on, before the
// key ($6, $7) unless $5.
const backfillRange = `organization_id = $1 AND code = $2 AND (subscription_id, "timestamp") >= ($3, $4)
	AND ($5 OR (subscription_id, "timestamp") < ($6, $7))`

const (
	// backfillEndSQL returns the key of the event closing a chunk of $5
	// events from the key ($3, $4) on.
	backfillEndSQL = `SELECT subscription_id, "timestamp" FROM __schema__.events
	WHERE organization_id = $1 AND code = $2 AND (subscription_id, "timestamp") >= ($3, $4)
	ORDER BY subscription_id, "timestamp" OFFSET $5 LIMIT 1`
	// backfillBucketsSQL returns the buckets of the events of a chunk.
	backfillBucketsSQL = `SELECT coalesce(array_agg(s ORDER BY s, b), '{}'), coalesce(array_agg(b ORDER BY s, b), '{}') FROM (
	SELECT DISTINCT subscription_id AS s, ` + `date_bin('15 minutes', "timestamp", TIMESTAMPTZ '2001-01-01 00:00:00+00')` + ` AS b
	FROM __schema__.events WHERE ` + backfillRange + `
) c`
	// backfillDeleteSQL deletes the rows of definition $3 in the buckets
	// ($4, $5) of subscriptions and bucket starts.
	backfillDeleteSQL = `DELETE FROM __schema__.usage_buckets u USING unnest($4::text[], $5::timestamptz[]) AS t(sub, start)
	WHERE u.organization_id = $1 AND u.code = $2 AND u.definition_id = $3 AND u.subscription_id = t.sub AND u.bucket = t.start`
)

// backfillInsertSQL aggregates the rows of definition $3 in the buckets
// ($4, $5) again.
var backfillInsertSQL = `INSERT INTO __schema__.usage_buckets (` + bucketColumns + `)
` + bucketRows(`(
	SELECT e.organization_id, e.subscription_id, e.code, e."timestamp" AS ts, e.sequence, e.properties,
		e.precise_total_amount_cents AS amount, e.ingested_at
	FROM unnest($4::text[], $5::timestamptz[]) AS t(s, b)
	JOIN __schema__.events e ON e.organization_id = $1 AND e.code = $2 AND e.subscription_id = t.s
		AND e."timestamp" >= t.b AND e."timestamp" < t.b + interval '15 minutes'
)`, "WHERE d.definition_id = $3")

// lockSharedDefinitionSQL takes the definitions lock of ($1, $2) shared,
// as a writer does; definitionInPlaceSQL, run after it in a snapshot of
// its own, reports whether definition $3 is still in place in generation
// $4, neither dropped nor dropped and defined again.
var lockSharedDefinitionSQL = fmt.Sprintf(`SELECT pg_advisory_xact_lock_shared(%d, hashtext('__schema__.' || $1 || chr(31) || $2))`, lockDefinitions)

const definitionInPlaceSQL = `SELECT EXISTS (
	SELECT 1 FROM __schema__.usage_bucket_definitions WHERE organization_id = $1 AND code = $2 AND definition_id = $3 AND generation = $4
)`

// lockArgs returns the arrays of the lock and recomputation statements.
func (c columns) lockArgs() []any { return []any{c.org, c.sub, c.code, c.ts} }

// lockBuckets takes, in tx, the locks a write of the events needs and
// reports whether definitions apply to them.
func lockBuckets(ctx context.Context, tx pgx.Tx, c columns) (bool, error) {
	if _, err := tx.Exec(ctx, lockDefinitionsSQL, c.org, c.code); err != nil {
		return false, fmt.Errorf("lock definitions: %w", err)
	}
	tag, err := tx.Exec(ctx, lockBucketsSQL, c.lockArgs()...)
	if err != nil {
		return false, fmt.Errorf("lock buckets: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// inTx runs fn in a transaction of the store (a savepoint when the store
// runs in a transaction).
func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func definitionLockKey(d metering.BucketDefinition) (string, []any) {
	return fmt.Sprintf("SELECT pg_advisory_xact_lock(%d, hashtext('__schema__.' || $1 || chr(31) || $2))", lockDefinitions),
		[]any{d.OrganizationID, d.Code}
}

func checkDefinition(d metering.BucketDefinition) error {
	if d.OrganizationID == "" {
		return eventstore.ErrMissingOrganization
	}
	if d.Code == "" {
		return errors.New("missing code")
	}
	return nil
}

// DefineBuckets implements [eventstore.BucketDefiner]. It inserts the
// definition not ready, under the definitions lock of the code taken
// exclusively: writers that ran before it have committed, and every later
// writer maintains the new buckets. It then backfills the buckets in chunks
// of the stored events, each in its own transaction under the locks of its
// buckets, alongside the writers, and marks the definition ready. Defining
// a ready definition again is a no-op; defining one whose backfill was
// interrupted resumes it; defining a blocked one fails with
// [metering.ErrBucketsBlocked].
//
// Each insert of a definition takes a new generation. The backfill stops,
// and the definition is not marked ready, once its generation is no longer
// in place: the definition was dropped, and possibly defined again, which
// that definer backfills from the start.
func (s *Store) DefineBuckets(ctx context.Context, d metering.BucketDefinition) error {
	if err := checkDefinition(d); err != nil {
		return wrap("define buckets", err)
	}
	d = d.Normalize()
	fold, err := foldOf(d)
	if err != nil {
		return wrap("define buckets", err)
	}
	var (
		ready      bool
		generation int64
	)
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		lock, args := definitionLockKey(d)
		if _, err := tx.Exec(ctx, lock, args...); err != nil {
			return err
		}
		var blocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM __schema__.usage_bucket_blocks
			WHERE organization_id = $1 AND code = $2 AND definition_id = $3)`, d.OrganizationID, d.Code, d.ID()).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return metering.ErrBucketsBlocked
		}
		if _, err := tx.Exec(ctx, `INSERT INTO __schema__.usage_bucket_definitions (organization_id, code, definition_id, property, dimensions, fold)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, d.OrganizationID, d.Code, d.ID(), d.Property, d.Dimensions, fold); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT ready, generation FROM __schema__.usage_bucket_definitions WHERE organization_id = $1 AND code = $2 AND definition_id = $3`,
			d.OrganizationID, d.Code, d.ID()).Scan(&ready, &generation)
	})
	if err != nil || ready {
		return wrap("define buckets", err)
	}
	if err := s.backfill(ctx, d, generation); err != nil {
		if errors.Is(err, errDropped) {
			return nil
		}
		return wrap("define buckets", err)
	}
	_, err = s.db.Exec(ctx, `UPDATE __schema__.usage_bucket_definitions SET ready = true
		WHERE organization_id = $1 AND code = $2 AND definition_id = $3 AND generation = $4`,
		d.OrganizationID, d.Code, d.ID(), generation)
	return wrap("define buckets", err)
}

// foldOf returns the fold column of a normalized definition.
func foldOf(d metering.BucketDefinition) ([]byte, error) {
	values := d.Values
	if values == nil {
		values = map[string][]string{}
	}
	return json.Marshal(values)
}

// errDropped stops the backfill of a definition dropped meanwhile.
var errDropped = errors.New("definition dropped")

// backfill aggregates the buckets of generation of d again from the stored
// events, chunk by chunk in (subscription, timestamp) order. A chunk ends
// on a bucket boundary, so that it rebuilds whole buckets. It fails with
// errDropped once the generation is no longer in place.
func (s *Store) backfill(ctx context.Context, d metering.BucketDefinition, generation int64) error {
	var (
		fromSub string
		fromTS  = time.Time{}
	)
	for {
		var (
			toSub string
			toTS  time.Time
			last  bool
		)
		err := s.db.QueryRow(ctx, backfillEndSQL, d.OrganizationID, d.Code, fromSub, fromTS, s.backfillChunk).Scan(&toSub, &toTS)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			last = true
		case err != nil:
			return fmt.Errorf("backfill: %w", err)
		default:
			toTS = metering.BucketFloor(toTS)
			if toSub == fromSub && !toTS.After(fromTS) {
				toTS = toTS.Add(metering.BucketWidth)
			}
		}
		err = s.inTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, lockSharedDefinitionSQL, d.OrganizationID, d.Code); err != nil {
				return fmt.Errorf("lock definitions: %w", err)
			}
			var present bool
			if err := tx.QueryRow(ctx, definitionInPlaceSQL, d.OrganizationID, d.Code, d.ID(), generation).Scan(&present); err != nil {
				return err
			}
			if !present {
				return errDropped
			}
			var (
				subs    []string
				buckets []time.Time
			)
			if err := tx.QueryRow(ctx, backfillBucketsSQL, d.OrganizationID, d.Code, fromSub, fromTS, last, toSub, toTS).
				Scan(&subs, &buckets); err != nil {
				return err
			}
			if len(subs) == 0 {
				return nil
			}
			orgs, codes := make([]string, len(subs)), make([]string, len(subs))
			for i := range subs {
				orgs[i], codes[i] = d.OrganizationID, d.Code
			}
			if _, err := tx.Exec(ctx, lockBucketsSQL, orgs, subs, codes, buckets); err != nil {
				return fmt.Errorf("lock buckets: %w", err)
			}
			if _, err := tx.Exec(ctx, backfillDeleteSQL, d.OrganizationID, d.Code, d.ID(), subs, buckets); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, backfillInsertSQL, d.OrganizationID, d.Code, d.ID(), subs, buckets)
			return err
		})
		if errors.Is(err, errDropped) {
			return err
		}
		if err != nil {
			return fmt.Errorf("backfill: %w", err)
		}
		if last {
			return nil
		}
		fromSub, fromTS = toSub, toTS
	}
}

// drop deletes, in tx under the exclusive definitions lock of the code,
// the definition and its buckets: writers and backfills of the code wait
// for it, and a read checks the definition in the snapshot of its rows.
func drop(ctx context.Context, tx pgx.Tx, d metering.BucketDefinition) error {
	lock, args := definitionLockKey(d)
	if _, err := tx.Exec(ctx, lock, args...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM __schema__.usage_bucket_definitions WHERE organization_id = $1 AND code = $2 AND definition_id = $3`,
		d.OrganizationID, d.Code, d.ID()); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM __schema__.usage_buckets WHERE organization_id = $1 AND code = $2 AND definition_id = $3`,
		d.OrganizationID, d.Code, d.ID())
	return err
}

// DropBuckets implements [metering.BucketLifecycle]: the definition, its
// buckets and its block are deleted in one transaction.
func (s *Store) DropBuckets(ctx context.Context, d metering.BucketDefinition) error {
	if err := checkDefinition(d); err != nil {
		return wrap("drop buckets", err)
	}
	d = d.Normalize()
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := drop(ctx, tx, d); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM __schema__.usage_bucket_blocks WHERE organization_id = $1 AND code = $2 AND definition_id = $3`,
			d.OrganizationID, d.Code, d.ID())
		return err
	})
	s.reads.Forget(d)
	return wrap("drop buckets", err)
}

// BlockBuckets implements [metering.BucketLifecycle]: the definition and
// its buckets are deleted and its block inserted in one transaction, under
// the lock [Store.DefineBuckets] checks the blocks under.
func (s *Store) BlockBuckets(ctx context.Context, d metering.BucketDefinition) error {
	if err := checkDefinition(d); err != nil {
		return wrap("block buckets", err)
	}
	d = d.Normalize()
	fold, err := foldOf(d)
	if err != nil {
		return wrap("block buckets", err)
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if err := drop(ctx, tx, d); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO __schema__.usage_bucket_blocks (organization_id, code, definition_id, property, dimensions, fold, blocked_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`, d.OrganizationID, d.Code, d.ID(), d.Property, d.Dimensions, fold, s.now().UTC())
		return err
	})
	s.reads.Forget(d)
	return wrap("block buckets", err)
}

// BucketDefinitionStates implements [metering.BucketLifecycle].
func (s *Store) BucketDefinitionStates(ctx context.Context, organizationID string) ([]metering.BucketDefinitionState, error) {
	var (
		out                 []metering.BucketDefinitionState
		org, code, property string
		dims                []string
		fold                []byte
		ready, blocked      bool
		definedAt           time.Time
		readAt              *time.Time
	)
	err := s.each(ctx, `SELECT organization_id, code, definition_id, property, dimensions, fold, ready, false, created_at, read_at
		FROM __schema__.usage_bucket_definitions WHERE organization_id = $1 OR $1 = ''
		UNION ALL
		SELECT organization_id, code, definition_id, property, dimensions, fold, false, true, blocked_at, NULL
		FROM __schema__.usage_bucket_blocks WHERE organization_id = $1 OR $1 = ''
		ORDER BY 1, 2, 3`, args{organizationID},
		[]any{&org, &code, new(string), &property, &dims, &fold, &ready, &blocked, &definedAt, &readAt}, func() error {
			d := metering.BucketDefinition{OrganizationID: org, Code: code, Property: property, Dimensions: dims}
			if err := json.Unmarshal(fold, &d.Values); err != nil {
				return fmt.Errorf("fold of definition: %w", err)
			}
			st := metering.BucketDefinitionState{Definition: d.Normalize(), Ready: ready, Blocked: blocked, DefinedAt: definedAt.UTC()}
			if readAt != nil {
				st.ReadAt = readAt.UTC()
			}
			out = append(out, st)
			return nil
		})
	return out, wrap("bucket definition states", err)
}

// BucketDefinitions implements [eventstore.BucketDefiner].
func (s *Store) BucketDefinitions(ctx context.Context, organizationID, code string) ([]metering.BucketDefinition, error) {
	defs, err := s.definitions(ctx, organizationID, code, false)
	if err != nil {
		return nil, wrap("bucket definitions", err)
	}
	out := make([]metering.BucketDefinition, len(defs))
	for i, d := range defs {
		out[i] = d.BucketDefinition
	}
	return out, nil
}

// definition is a stored definition and its generation.
type definition struct {
	metering.BucketDefinition
	generation int64
}

// definitions returns the definitions of a code, the ready ones only when
// ready is set.
func (s *Store) definitions(ctx context.Context, organizationID, code string, ready bool) ([]definition, error) {
	if organizationID == "" {
		return nil, eventstore.ErrMissingOrganization
	}
	var (
		out        []definition
		property   string
		dims       []string
		fold       []byte
		generation int64
	)
	err := s.each(ctx, `SELECT property, dimensions, fold, generation FROM __schema__.usage_bucket_definitions
		WHERE organization_id = $1 AND code = $2 AND (ready OR NOT $3) ORDER BY definition_id`, args{organizationID, code, ready},
		[]any{&property, &dims, &fold, &generation}, func() error {
			d := metering.BucketDefinition{OrganizationID: organizationID, Code: code, Property: property, Dimensions: dims}
			if err := json.Unmarshal(fold, &d.Values); err != nil {
				return fmt.Errorf("fold of definition: %w", err)
			}
			out = append(out, definition{BucketDefinition: d.Normalize(), generation: generation})
			return nil
		})
	return out, err
}

// AllBucketDefinitions implements [eventstore.BucketDefiner].
func (s *Store) AllBucketDefinitions(ctx context.Context) ([]metering.BucketDefinition, error) {
	var (
		out                 []metering.BucketDefinition
		org, code, property string
		dims                []string
		fold                []byte
	)
	err := s.each(ctx, `SELECT organization_id, code, property, dimensions, fold FROM __schema__.usage_bucket_definitions
		ORDER BY organization_id, code, definition_id`, nil,
		[]any{&org, &code, &property, &dims, &fold}, func() error {
			d := metering.BucketDefinition{OrganizationID: org, Code: code, Property: property, Dimensions: dims}
			if err := json.Unmarshal(fold, &d.Values); err != nil {
				return fmt.Errorf("fold of definition: %w", err)
			}
			out = append(out, d.Normalize())
			return nil
		})
	return out, wrap("all bucket definitions", err)
}

// readyDefinitionSQL reports whether a definition is in place and ready in
// generation $4.
const readyDefinitionSQL = `SELECT EXISTS (SELECT 1 FROM __schema__.usage_bucket_definitions
	WHERE organization_id = $1 AND code = $2 AND definition_id = $3 AND generation = $4 AND ready)`

// ReadBuckets implements [metering.BucketReader]. It reads the ready
// definitions only. The statement reading the rows checks that the
// definition is still in place, in the generation the read chose, in the
// same snapshot; when it returns no row, a second statement tells an empty
// read from a dropped definition. A generation once dropped never comes
// back, so the two statements cannot disagree on a definition dropped
// and defined again between them.
func (s *Store) ReadBuckets(ctx context.Context, r metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	q := r.Query
	stored, err := s.definitions(ctx, q.OrganizationID, q.Code, true)
	if err != nil {
		return nil, false, wrap("read buckets", err)
	}
	defs := make([]metering.BucketDefinition, len(stored))
	for i, d := range stored {
		defs[i] = d.BucketDefinition
	}
	d, ok := metering.CoveringDefinition(defs, r)
	if !ok {
		return nil, false, nil
	}
	var generation int64
	for _, sd := range stored {
		if sd.ID() == d.ID() {
			generation = sd.generation
		}
	}
	var a args
	org, code, id, gen := a.add(q.OrganizationID), a.add(q.Code), a.add(d.ID()), a.add(generation)
	where := []string{
		"b.organization_id = " + org,
		"b.subscription_id = " + a.add(q.SubscriptionID),
		"b.code = " + code,
		"b.definition_id = " + id,
		"b.bucket >= " + a.add(r.From.UTC()) + "::timestamptz",
		"b.bucket < " + a.add(r.To.UTC()) + "::timestamptz",
		`EXISTS (SELECT 1 FROM __schema__.usage_bucket_definitions d
			WHERE d.organization_id = ` + org + ` AND d.code = ` + code + ` AND d.definition_id = ` + id + `
				AND d.generation = ` + gen + ` AND d.ready)`,
	}
	count, amount, last := "b.events_count", "b.precise_total_amount_cents", "b.last_event_at"
	if q.Numeric {
		count, amount, last = "b.value_count", "b.value_precise_total_amount_cents", "b.latest_at"
	}
	where = append(where, filterConditions(&a, "b.dimensions", q)...)

	var cols []string
	for _, k := range r.By {
		cols = append(cols, "NULLIF(b.dimensions->>"+a.text(k)+", '')")
	}
	n := len(cols)
	if r.PerBucket {
		cols = append(cols, "b.bucket")
		n++
	}
	const latestOrder = "ORDER BY b.latest_at DESC, b.latest_sequence DESC) FILTER (WHERE b.latest_at IS NOT NULL))[1]"
	cols = append(cols,
		"sum("+count+")::bigint", "sum(b.events_count)::bigint", "sum("+amount+")::text", "sum(b.units)::text", "max(b.max_value)::text",
		"((array_agg(b.latest_value "+latestOrder+")::text",
		"max("+last+")", "max(b.last_ingested_at)")
	for _, k := range r.Columns {
		cols = append(cols, "(array_agg(NULLIF(b.dimensions->>"+a.text(k)+", '') "+latestOrder)
	}
	sql := "SELECT " + strings.Join(cols, ", ") + " FROM __schema__.usage_buckets b WHERE " + strings.Join(where, " AND ") + " " + groupBy(n) + " HAVING count(*) > 0"
	if r.PerBucket {
		sql += fmt.Sprintf(" ORDER BY %d", n)
	}

	groupVals, dest := groupDest(len(r.By))
	var (
		bucket            time.Time
		total             metering.BucketTotal
		sum, amt          money.Decimal
		maxVal, latestVal *string
		lastAt            *time.Time
		ingested          time.Time
	)
	if r.PerBucket {
		dest = append(dest, &bucket)
	}
	dest = append(dest, &total.Count, &total.EventsCount, &amt, &sum, &maxVal, &latestVal, &lastAt, &ingested)
	colVals, colDest := groupDest(len(r.Columns))
	dest = append(dest, colDest...)
	var out []metering.BucketTotal
	err = s.each(ctx, sql, a, dest, func() error {
		t := metering.BucketTotal{
			Groups: calc.Groups(r.By, groupVals), Count: total.Count, EventsCount: total.EventsCount,
			Sum: sum, PreciseTotalAmountCents: amt, LastIngestedAt: ingested.UTC(),
		}
		if lastAt != nil {
			t.LastEventAt = lastAt.UTC()
		}
		if r.PerBucket {
			t.Bucket = bucket.UTC()
		}
		if !q.Numeric {
			t.Sum = money.Zero
		}
		var err error
		if q.Numeric {
			if t.Max, err = parseDecimal(maxVal); err != nil {
				return err
			}
			if t.Latest, err = parseDecimal(latestVal); err != nil {
				return err
			}
			if len(r.Columns) > 0 {
				t.LatestColumns = calc.Groups(r.Columns, colVals)
			}
		}
		out = append(out, t)
		return nil
	})
	if err != nil {
		return nil, false, wrap("read buckets", err)
	}
	if len(out) == 0 {
		if err := s.db.QueryRow(ctx, readyDefinitionSQL, q.OrganizationID, q.Code, d.ID(), generation).Scan(&ok); err != nil {
			return nil, false, wrap("read buckets", err)
		}
	}
	if ok {
		s.recordRead(ctx, d, generation)
	}
	return out, ok, nil
}

// recordRead records the read of generation of d, at most once per
// [metering.BucketReadResolution] and process. A failure only logs: the
// read is served all the same, and the next one records it.
func (s *Store) recordRead(ctx context.Context, d metering.BucketDefinition, generation int64) {
	now := s.now().UTC()
	if !s.reads.Due(d, now) {
		return
	}
	if _, err := s.db.Exec(ctx, `UPDATE __schema__.usage_bucket_definitions SET read_at = $5
		WHERE organization_id = $1 AND code = $2 AND definition_id = $3 AND generation = $4 AND (read_at IS NULL OR read_at < $5)`,
		d.OrganizationID, d.Code, d.ID(), generation, now); err != nil {
		s.reads.Forget(d)
		slog.WarnContext(ctx, "eventstore: postgres: record usage bucket read", "organization_id", d.OrganizationID, "code", d.Code, "error", err)
	}
}

// BucketStats implements [metering.BucketLifecycle].
func (s *Store) BucketStats(ctx context.Context, d metering.BucketDefinition) (eventstore.BucketStats, error) {
	if err := checkDefinition(d); err != nil {
		return eventstore.BucketStats{}, wrap("bucket stats", err)
	}
	var st eventstore.BucketStats
	err := s.db.QueryRow(ctx, `SELECT coalesce(sum(n), 0)::bigint, coalesce(max(n), 0)::bigint FROM (
		SELECT count(*) AS n FROM __schema__.usage_buckets WHERE organization_id = $1 AND code = $2 AND definition_id = $3
		GROUP BY subscription_id, bucket
	) b`, d.OrganizationID, d.Code, d.Normalize().ID()).Scan(&st.Rows, &st.MaxRowsPerBucket)
	return st, wrap("bucket stats", err)
}

func parseDecimal(s *string) (*money.Decimal, error) {
	if s == nil {
		return nil, nil
	}
	d, err := money.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}
