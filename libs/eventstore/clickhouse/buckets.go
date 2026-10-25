package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/calc"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var (
	_ metering.BucketReader    = (*Store)(nil)
	_ eventstore.BucketDefiner = (*Store)(nil)
)

// consistent returns ctx with the settings of the bucket maintenance on
// replicated tables (ReplicatedMergeTree, SharedMergeTree): inserts return
// once a quorum of replicas holds them, and reads see every insert a quorum
// acknowledged. A writer then reads every row and event another writer
// wrote before, which the versions and the conflict checks of the bucket
// maintenance rely on (see [Store.conflicts]). Tables that are not
// replicated ignore both.
func consistent(ctx context.Context) context.Context {
	return ch.Context(ctx, ch.WithSettings(ch.Settings{
		"insert_quorum":                 "auto",
		"select_sequential_consistency": 1,
	}))
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

// definition is a stored definition.
type definition struct {
	metering.BucketDefinition
	ready      bool
	generation uint64
	version    uint64
}

// rows returns the definition_id of the bucket rows of the definition.
func (d definition) rows() string { return instanceKey(d.ID(), d.generation) }

// definitions returns the definitions of a code (all of them, or the one
// of the ID).
func (s *Store) definitions(ctx context.Context, org, code, id string) ([]definition, error) {
	a := args{org, code}
	where := "organization_id = $1 AND code = $2"
	if id != "" {
		where += " AND definition_id = " + a.add(id)
	}
	var (
		out                 []definition
		property            string
		dims                []string
		fold                map[string][]string
		ready               uint8
		generation, version uint64
	)
	err := s.each(ctx, `SELECT property, dimensions, fold, ready, generation, version FROM usage_bucket_definitions FINAL WHERE `+where+` ORDER BY definition_id`,
		a, []any{&property, &dims, &fold, &ready, &generation, &version}, func() error {
			d := metering.BucketDefinition{OrganizationID: org, Code: code, Property: property, Dimensions: slices.Clone(dims)}
			if len(fold) > 0 {
				d.Values = maps.Clone(fold)
			}
			out = append(out, definition{BucketDefinition: d.Normalize(), ready: ready == 1, generation: generation, version: version})
			return nil
		})
	return out, err
}

func b2u(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// writeDefinition writes a version of the definition d of a generation.
func (s *Store) writeDefinition(ctx context.Context, d metering.BucketDefinition, ready, deleted bool, generation, version uint64) error {
	fold := d.Values
	if fold == nil {
		fold = map[string][]string{}
	}
	return s.conn.Exec(consistent(ctx), `INSERT INTO usage_bucket_definitions (organization_id, code, definition_id, property, dimensions, fold, ready, generation, is_deleted, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		d.OrganizationID, d.Code, d.ID(), d.Property, d.Dimensions, fold, b2u(ready), generation, b2u(deleted), version)
}

// observeVersions raises the store's sequences above every version written
// of the definition d and of its block, tombstones included, so that a
// write that must win over them gets a higher one.
func (s *Store) observeVersions(ctx context.Context, d metering.BucketDefinition) error {
	var high uint64
	err := s.each(ctx, `SELECT max(version) FROM (
		SELECT version FROM usage_bucket_definitions WHERE organization_id = $1 AND code = $2 AND definition_id = $3
		UNION ALL
		SELECT version FROM usage_bucket_blocks WHERE organization_id = $1 AND code = $2 AND definition_id = $3
	)`, args{d.OrganizationID, d.Code, d.ID()}, []any{&high}, func() error { return nil })
	if err != nil {
		return err
	}
	s.observe(high)
	return nil
}

// blocked reports whether d is blocked.
func (s *Store) blocked(ctx context.Context, d metering.BucketDefinition) (bool, error) {
	var n uint64
	err := s.each(ctx, `SELECT count() FROM usage_bucket_blocks FINAL WHERE organization_id = $1 AND code = $2 AND definition_id = $3`,
		args{d.OrganizationID, d.Code, d.ID()}, []any{&n}, func() error { return nil })
	return n > 0, err
}

// DefineBuckets implements [eventstore.BucketDefiner]. The definition is
// written not ready, as a new generation whose version is the generation,
// so that writers maintain it, then its buckets are backfilled from every
// stored event of the code, a page of buckets at a time, then it is
// written ready as version generation + 1. A writer that missed the
// definition inserted its events before the definition was written, hence
// before the backfill read them; a writer that saw it merges into its rows
// or recomputes them, as for any definition.
//
// A drop or a block of the definition while it backfills is written above
// both versions ([Store.DropBuckets] reads the generation first), so that
// the ready version loses to it and the definition stays dropped; the
// backfill of a generation dropped meanwhile writes nothing, as its pages
// recompute the generation in place only. Defining an unfinished
// definition resumes its generation; defining a blocked one fails with
// [metering.ErrBucketsBlocked], checked again before the ready version.
func (s *Store) DefineBuckets(ctx context.Context, d metering.BucketDefinition) error {
	if err := checkDefinition(d); err != nil {
		return wrap("define buckets", err)
	}
	d = d.Normalize()
	if blocked, err := s.blocked(ctx, d); err != nil || blocked {
		if err == nil {
			err = metering.ErrBucketsBlocked
		}
		return wrap("define buckets", err)
	}
	existing, err := s.definitions(ctx, d.OrganizationID, d.Code, d.ID())
	if err != nil {
		return wrap("define buckets", err)
	}
	var generation uint64
	switch {
	case len(existing) > 0 && existing[0].ready:
		return nil
	case len(existing) > 0 && existing[0].generation != 0:
		generation = existing[0].generation
	default:
		if err := s.observeVersions(ctx, d); err != nil {
			return wrap("define buckets", err)
		}
		generation = s.sequences(2)
		if err := s.writeDefinition(ctx, d, false, false, generation, generation); err != nil {
			return wrap("define buckets", err)
		}
	}
	if err := s.backfill(consistent(ctx), d, generation); err != nil {
		return wrap("define buckets: backfill", err)
	}
	if blocked, err := s.blocked(ctx, d); err != nil || blocked {
		if err == nil {
			// A block written while the generation backfilled may have
			// read the definition before its write: drop it again.
			if err = s.drop(ctx, d, generation); err == nil {
				err = metering.ErrBucketsBlocked
			}
		}
		return wrap("define buckets", err)
	}
	return wrap("define buckets", s.writeDefinition(ctx, d, true, false, generation, generation+1))
}

// backfill recomputes the rows of generation of the definition in every
// bucket holding events of its code, by pages of buckets in (subscription,
// bucket) order.
func (s *Store) backfill(ctx context.Context, d metering.BucketDefinition, generation uint64) error {
	var (
		after bucketKey
		first = true
	)
	for {
		page := buckets{org: d.OrganizationID}
		a := args{d.OrganizationID, d.Code}
		where := ""
		if !first {
			where = " AND (subscription_id, " + bucketUs + ") > (" + a.add(after.sub) + ", " + a.add(after.us) + ")"
		}
		k := bucketKey{code: d.Code}
		err := s.each(ctx, `SELECT DISTINCT subscription_id, `+bucketUs+` AS us FROM events_enriched
			WHERE organization_id = $1 AND code = $2`+where+` ORDER BY 1, 2 LIMIT `+fmt.Sprint(repairChunk),
			a, []any{&k.sub, &k.us}, func() error {
				page.keys = append(page.keys, k)
				return nil
			})
		if err != nil {
			return err
		}
		if len(page.keys) == 0 {
			return nil
		}
		if err := s.recompute(ctx, page, instanceKey(d.ID(), generation)); err != nil {
			return err
		}
		after, first = page.keys[len(page.keys)-1], false
	}
}

// drop tombstones the definition d above every version written of it,
// then deletes the bucket rows of its generations up to generation (the
// last one when 0): a read that started before the tombstone reads the
// definition again after its rows and goes back to the events, and the
// rows of a later generation, defined after the drop, are kept. Rows a
// writer of a dropped generation inserts after the delete are never read,
// and the recomputes of their buckets tombstone them.
func (s *Store) drop(ctx context.Context, d metering.BucketDefinition, generation uint64) error {
	existing, err := s.definitions(ctx, d.OrganizationID, d.Code, d.ID())
	if err != nil {
		return err
	}
	if err := s.observeVersions(ctx, d); err != nil {
		return err
	}
	if len(existing) > 0 {
		generation = max(generation, existing[0].generation)
	}
	version := s.sequences(1)
	if generation == 0 {
		generation = version
	}
	if err := s.writeDefinition(ctx, d, false, true, generation, version); err != nil {
		return err
	}
	return s.conn.Exec(consistent(ctx), `DELETE FROM usage_buckets_15m
		WHERE organization_id = $1 AND code = $2 AND (definition_id = $3
			OR (startsWith(definition_id, concat($3, '.')) AND toUInt64OrZero(substringIndex(definition_id, '.', -1)) <= $4))`,
		d.OrganizationID, d.Code, d.ID(), generation)
}

// DropBuckets implements [metering.BucketLifecycle]: the definition is
// tombstoned and its rows deleted (see [Store.drop]), and its block lifted.
func (s *Store) DropBuckets(ctx context.Context, d metering.BucketDefinition) error {
	if err := checkDefinition(d); err != nil {
		return wrap("drop buckets", err)
	}
	d = d.Normalize()
	defer s.reads.Forget(d)
	if err := s.drop(ctx, d, 0); err != nil {
		return wrap("drop buckets", err)
	}
	blocked, err := s.blocked(ctx, d)
	if err != nil || !blocked {
		return wrap("drop buckets", err)
	}
	return wrap("drop buckets", s.writeBlock(ctx, d, true))
}

// writeBlock writes the block of d, or its tombstone, above every version
// written of d.
func (s *Store) writeBlock(ctx context.Context, d metering.BucketDefinition, deleted bool) error {
	if err := s.observeVersions(ctx, d); err != nil {
		return err
	}
	fold := d.Values
	if fold == nil {
		fold = map[string][]string{}
	}
	return s.conn.Exec(consistent(ctx), `INSERT INTO usage_bucket_blocks (organization_id, code, definition_id, property, dimensions, fold, blocked_at, is_deleted, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		d.OrganizationID, d.Code, d.ID(), d.Property, d.Dimensions, fold, s.now().UTC(), b2u(deleted), s.sequences(1))
}

// BlockBuckets implements [metering.BucketLifecycle]: the block is written
// first, so that a definer checking it after its backfill sees it, then
// the definition is dropped (see [Store.drop]).
func (s *Store) BlockBuckets(ctx context.Context, d metering.BucketDefinition) error {
	if err := checkDefinition(d); err != nil {
		return wrap("block buckets", err)
	}
	d = d.Normalize()
	defer s.reads.Forget(d)
	if err := s.writeBlock(ctx, d, false); err != nil {
		return wrap("block buckets", err)
	}
	return wrap("block buckets", s.drop(ctx, d, 0))
}

// BucketDefinitionStates implements [metering.BucketLifecycle]. A
// generation is the store's sequence when it was written, in nanoseconds:
// its instant is the definition's.
func (s *Store) BucketDefinitionStates(ctx context.Context, organizationID string) ([]metering.BucketDefinitionState, error) {
	var (
		out                 []metering.BucketDefinitionState
		org, code, property string
		dims                []string
		fold                map[string][]string
		ready, blocked      uint8
		generation          uint64
		blockedAt, readAt   time.Time
		readOK              bool
	)
	err := s.each(ctx, `SELECT d.organization_id, d.code, d.definition_id, d.property, d.dimensions, d.fold, d.ready, toUInt8(0),
			d.generation, toDateTime64(0, 6, 'UTC'), r.read_at, r.n > 0
		FROM (SELECT * FROM usage_bucket_definitions FINAL WHERE organization_id = $1 OR $1 = '') d
		LEFT JOIN (
			SELECT organization_id, code, definition_id, generation, max(read_at) AS read_at, count() AS n
			FROM usage_bucket_reads WHERE organization_id = $1 OR $1 = '' GROUP BY 1, 2, 3, 4
		) r ON r.organization_id = d.organization_id AND r.code = d.code AND r.definition_id = d.definition_id AND r.generation = d.generation
		UNION ALL
		SELECT organization_id, code, definition_id, property, dimensions, fold, toUInt8(0), toUInt8(1),
			toUInt64(0), blocked_at, toDateTime64(0, 6, 'UTC'), false
		FROM usage_bucket_blocks FINAL WHERE organization_id = $1 OR $1 = ''
		ORDER BY 1, 2, 3`, args{organizationID},
		[]any{&org, &code, new(string), &property, &dims, &fold, &ready, &blocked, &generation, &blockedAt, &readAt, &readOK}, func() error {
			d := metering.BucketDefinition{OrganizationID: org, Code: code, Property: property, Dimensions: slices.Clone(dims)}
			if len(fold) > 0 {
				d.Values = maps.Clone(fold)
			}
			st := metering.BucketDefinitionState{Definition: d.Normalize(), Ready: ready == 1, Blocked: blocked == 1}
			switch {
			case st.Blocked:
				st.DefinedAt = blockedAt.UTC()
			case generation != 0:
				st.DefinedAt = time.Unix(0, int64(generation)).UTC() //nolint:gosec // sequences are nanoseconds since the epoch
			}
			if readOK {
				st.ReadAt = readAt.UTC()
			}
			out = append(out, st)
			return nil
		})
	return out, wrap("bucket definition states", err)
}

// BucketDefinitions implements [eventstore.BucketDefiner].
func (s *Store) BucketDefinitions(ctx context.Context, organizationID, code string) ([]metering.BucketDefinition, error) {
	if organizationID == "" {
		return nil, wrap("bucket definitions", eventstore.ErrMissingOrganization)
	}
	defs, err := s.definitions(ctx, organizationID, code, "")
	if err != nil {
		return nil, wrap("bucket definitions", err)
	}
	out := make([]metering.BucketDefinition, len(defs))
	for i, d := range defs {
		out[i] = d.BucketDefinition
	}
	return out, nil
}

// AllBucketDefinitions implements [eventstore.BucketDefiner].
func (s *Store) AllBucketDefinitions(ctx context.Context) ([]metering.BucketDefinition, error) {
	var (
		out                 []metering.BucketDefinition
		org, code, property string
		dims                []string
		fold                map[string][]string
	)
	err := s.each(ctx, `SELECT organization_id, code, property, dimensions, fold FROM usage_bucket_definitions FINAL
		ORDER BY organization_id, code, definition_id`,
		nil, []any{&org, &code, &property, &dims, &fold}, func() error {
			d := metering.BucketDefinition{OrganizationID: org, Code: code, Property: property, Dimensions: slices.Clone(dims)}
			if len(fold) > 0 {
				d.Values = maps.Clone(fold)
			}
			out = append(out, d.Normalize())
			return nil
		})
	return out, wrap("all bucket definitions", err)
}

// ReadBuckets implements [metering.BucketReader]. It reads the ready
// definitions only.
func (s *Store) ReadBuckets(ctx context.Context, r metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	q := r.Query
	if q.OrganizationID == "" {
		return nil, false, wrap("read buckets", eventstore.ErrMissingOrganization)
	}
	stored, err := s.definitions(ctx, q.OrganizationID, q.Code, "")
	if err != nil {
		return nil, false, wrap("read buckets", err)
	}
	var (
		defs  []metering.BucketDefinition
		ready = map[string]definition{}
	)
	for _, d := range stored {
		if d.ready {
			defs = append(defs, d.BucketDefinition)
			ready[d.ID()] = d
		}
	}
	covering, ok := metering.CoveringDefinition(defs, r)
	if !ok {
		return nil, false, nil
	}
	d := ready[covering.ID()]
	var a args
	bounds := []string{
		"organization_id = " + a.add(q.OrganizationID),
		"code = " + a.add(q.Code),
		"subscription_id = " + a.add(q.SubscriptionID),
		"definition_id = " + a.add(d.rows()),
		"bucket >= " + a.time(r.From.UTC()),
		"bucket < " + a.time(r.To.UTC()),
	}
	count, amount, last := "events_count", "precise_total_amount_cents", "last_event_at"
	if q.Numeric {
		count, amount, last = "value_count", "value_precise_total_amount_cents", "latest_at"
	}
	where := append([]string{"is_deleted = 0"}, filterConditions(&a, "dimensions", q)...)

	var cols, keys []string
	for i, k := range r.By {
		alias := fmt.Sprintf("g%d", i)
		cols = append(cols, "nullIf(dimensions["+a.add(k)+"], '') AS "+alias)
		keys = append(keys, alias)
	}
	if r.PerBucket {
		cols = append(cols, "bucket")
		keys = append(keys, "bucket")
	}
	const latestOf = ", (latest_at, latest_sequence), latest_at IS NOT NULL)"
	cols = append(cols,
		"toInt64(sum("+count+"))", "toInt64(sum(events_count))", "toString(sum("+amount+"))", "toString(sum(units))",
		"toString(maxOrNull(max_value))", "toString(argMaxIfOrNull(latest_value"+latestOf+")",
		"maxOrNull("+last+")", "max(last_ingested_at)")
	for _, k := range r.Columns {
		cols = append(cols, "argMaxIfOrNull(nullIf(dimensions["+a.add(k)+"], '')"+latestOf)
	}
	sql := `SELECT ` + strings.Join(cols, ", ") + `
FROM usage_buckets_15m FINAL
WHERE ` + strings.Join(bounds, " AND ") + `
	AND ` + strings.Join(where, " AND ") + `
` + groupBy(keys) + ` HAVING count() > 0`
	if r.PerBucket {
		sql += " ORDER BY bucket"
	}

	groupVals, dest := groupDest(len(r.By))
	var (
		bucket         time.Time
		n, events      int64
		amt, sum       = newDecimal(), newDecimal()
		maxVal, latest *string
		lastAt         *time.Time
		ingested       time.Time
	)
	if r.PerBucket {
		dest = append(dest, &bucket)
	}
	dest = append(dest, &n, &events, amt.s, sum.s, &maxVal, &latest, &lastAt, &ingested)
	colVals, colDest := groupDest(len(r.Columns))
	dest = append(dest, colDest...)
	var out []metering.BucketTotal
	err = s.each(ctx, sql, a, dest, func() error {
		t := metering.BucketTotal{
			Groups: calc.Groups(r.By, groupVals), Count: n, EventsCount: events, Sum: money.Zero,
			LastIngestedAt: ingested.UTC(),
		}
		if lastAt != nil {
			t.LastEventAt = lastAt.UTC()
		}
		if r.PerBucket {
			t.Bucket = bucket.UTC()
		}
		var err error
		if t.PreciseTotalAmountCents, err = amt.get(); err != nil {
			return err
		}
		if q.Numeric {
			if t.Sum, err = sum.get(); err != nil {
				return err
			}
			if t.Max, err = parseDecimal(maxVal); err != nil {
				return err
			}
			if t.Latest, err = parseDecimal(latest); err != nil {
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
	// A definition dropped while the rows were read may have lost some of
	// them: the definition read again after the rows tells, its generation
	// too, as a definition dropped and defined again has new rows.
	after, err := s.definitions(ctx, q.OrganizationID, q.Code, d.ID())
	if err != nil {
		return nil, false, wrap("read buckets", err)
	}
	if len(after) == 0 || !after[0].ready || after[0].generation != d.generation {
		return nil, false, nil
	}
	s.recordRead(ctx, d)
	return out, true, nil
}

// recordRead records a read of the generation of d in usage_bucket_reads,
// at most once per [metering.BucketReadResolution] and process. A failure
// is logged only: the read is served, and a later one records it.
func (s *Store) recordRead(ctx context.Context, d definition) {
	now := s.now().UTC()
	if !s.reads.Due(d.BucketDefinition, now) {
		return
	}
	err := s.conn.Exec(ctx, `INSERT INTO usage_bucket_reads (organization_id, code, definition_id, generation, read_at) VALUES ($1, $2, $3, $4, $5)`,
		d.OrganizationID, d.Code, d.ID(), d.generation, now)
	if err != nil {
		s.reads.Forget(d.BucketDefinition)
		slog.WarnContext(ctx, "record usage bucket read", "organization_id", d.OrganizationID, "code", d.Code, "definition_id", d.ID(), "error", err)
	}
}

// BucketStats implements [eventstore.BucketDefiner].
func (s *Store) BucketStats(ctx context.Context, d metering.BucketDefinition) (eventstore.BucketStats, error) {
	if err := checkDefinition(d); err != nil {
		return eventstore.BucketStats{}, wrap("bucket stats", err)
	}
	d = d.Normalize()
	var st eventstore.BucketStats
	stored, err := s.definitions(ctx, d.OrganizationID, d.Code, d.ID())
	if err != nil || len(stored) == 0 {
		return st, wrap("bucket stats", err)
	}
	err = s.each(ctx, `SELECT toInt64(sum(n)), toInt64(max(n)) FROM (
		SELECT count() AS n FROM usage_buckets_15m FINAL
		WHERE organization_id = $1 AND code = $2 AND definition_id = $3 AND is_deleted = 0
		GROUP BY subscription_id, bucket
	)`, args{d.OrganizationID, d.Code, stored[0].rows()}, []any{&st.Rows, &st.MaxRowsPerBucket}, func() error { return nil })
	return st, wrap("bucket stats", err)
}

func parseDecimal(s *string) (*money.Decimal, error) {
	if s == nil {
		return nil, nil
	}
	d, err := money.Parse(*s)
	if err != nil {
		return nil, fmt.Errorf("decimal %q: %w", *s, err)
	}
	return &d, nil
}
