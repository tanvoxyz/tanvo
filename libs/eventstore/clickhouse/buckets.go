package clickhouse

import (
	"context"
	"errors"
	"fmt"
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

// bucketMicros is the width of a bucket in microseconds.
const bucketMicros = int64(metering.BucketWidth / time.Microsecond)

// bucketUs is the expression of the bucket, in microseconds since the
// epoch, of the timestamp of an event.
var bucketUs = fmt.Sprintf("intDiv(toUnixTimestamp64Micro(timestamp), %[1]d) * %[1]d", bucketMicros)

const bucketColumns = `organization_id, code, subscription_id, definition_id, bucket, dimensions, dimensions_key,
	events_count, precise_total_amount_cents, last_event_at, last_ingested_at,
	value_count, units, max_value, latest_value, latest_at, latest_sequence, value_precise_total_amount_cents, version`

// recomputeSQL returns the statement aggregating the events selected by
// scope (conditions on the sorting key of events_enriched) into the rows
// of the buckets of the definitions selected by defs, versioned by the sum
// of the sequences of each bucket's events.
func recomputeSQL(scope, defs string) string {
	zero := fmt.Sprintf("toDecimal256(0, %d)", scale)
	return `INSERT INTO usage_buckets_15m (` + bucketColumns + `)
WITH
ev AS (
	SELECT organization_id, code, subscription_id, timestamp, sequence, properties,
		precise_total_amount_cents AS amount, ingested_at, ` + bucketUs + ` AS bucket_us
	FROM events_enriched FINAL
	WHERE ` + scope + `
),
versions AS (
	SELECT code, subscription_id, bucket_us, sum(toUInt256(sequence)) AS version FROM ev GROUP BY code, subscription_id, bucket_us
),
defs AS (
	SELECT organization_id, code, definition_id, property, dimensions, fold FROM usage_bucket_definitions FINAL WHERE ` + defs + `
),
valued AS (
	SELECT ev.*, d.definition_id AS definition_id,
		mapSort(mapApply((k, v) -> (k, if(v != '' AND mapContains(d.fold, k) AND NOT has(d.fold[k], v), ` + "'" + metering.BucketOtherValue + "'" + `, v)),
			mapFilter((k, v) -> has(d.dimensions, k), ev.properties))) AS dims,
		d.property != '' AND match(ev.properties[d.property], ` + numericPattern + `) AS isnum,
		toDecimal256OrZero(ev.properties[d.property], ` + fmt.Sprint(scale) + `) AS value
	FROM ev INNER JOIN defs d ON ev.organization_id = d.organization_id AND ev.code = d.code
),
rows AS (
	SELECT organization_id, code, subscription_id, definition_id, bucket_us, dims,
		count() AS n, sum(ifNull(amount, ` + zero + `)) AS amt, max(timestamp) AS last_at, max(ingested_at) AS ingested,
		countIf(isnum) AS vn, sumIf(value, isnum) AS units, maxIfOrNull(value, isnum) AS max_value,
		argMaxIfOrNull(value, (timestamp, sequence), isnum) AS latest_value,
		maxIfOrNull(timestamp, isnum) AS latest_at,
		argMaxIf(sequence, (timestamp, sequence), isnum) AS latest_sequence,
		sumIf(ifNull(amount, ` + zero + `), isnum) AS vamt
	FROM valued
	GROUP BY organization_id, code, subscription_id, definition_id, bucket_us, dims
)
SELECT r.organization_id, r.code, r.subscription_id, r.definition_id, fromUnixTimestamp64Micro(r.bucket_us, 'UTC'),
	r.dims, toString(r.dims), r.n, r.amt, r.last_at, r.ingested, r.vn, r.units, r.max_value, r.latest_value,
	r.latest_at, r.latest_sequence, r.vamt, v.version
FROM rows r INNER JOIN versions v ON r.code = v.code AND r.subscription_id = v.subscription_id AND r.bucket_us = v.bucket_us`
}

// tombstoneSQL tombstones the rows of the touched buckets that the highest
// version of their bucket no longer holds: rows of dimension values the
// events of the bucket left.
const tombstoneSQL = `INSERT INTO usage_buckets_15m (organization_id, code, subscription_id, definition_id, bucket, dimensions, dimensions_key, version, is_deleted)
SELECT organization_id, code, subscription_id, definition_id, bucket, dimensions, dimensions_key, top, 1
FROM (
	SELECT organization_id, code, subscription_id, definition_id, bucket, dimensions, dimensions_key, version,
		max(version) OVER (PARTITION BY code, subscription_id, definition_id, bucket) AS top
	FROM usage_buckets_15m FINAL
	WHERE organization_id = $1 AND code IN $2 AND bucket >= fromUnixTimestamp64Micro($3, 'UTC') AND bucket <= fromUnixTimestamp64Micro($4, 'UTC')
		AND has(arrayZip(CAST($5 AS Array(String)), CAST($6 AS Array(String)), CAST($7 AS Array(Int64))), (code, subscription_id, toUnixTimestamp64Micro(bucket)))
)
WHERE version < top`

// touched is the buckets an organization's write touched, of the codes
// with definitions.
type touched struct {
	org                  string
	codes, keyCodes      []string
	keySubs              []string
	keyBuckets           []int64
	minBucket, maxBucket int64
}

// touchedBuckets returns, per organization, the buckets of the rows whose
// code has definitions (written ready or not).
func (s *Store) touchedBuckets(ctx context.Context, rows []row) ([]touched, error) {
	var orgs []string
	codes := map[string][]any{}
	for _, r := range rows {
		if _, ok := codes[r.OrganizationID]; !ok {
			orgs = append(orgs, r.OrganizationID)
		}
		if !slices.Contains(codes[r.OrganizationID], any(r.Code)) {
			codes[r.OrganizationID] = append(codes[r.OrganizationID], r.Code)
		}
	}
	var out []touched
	for _, org := range orgs {
		defined := map[string]bool{}
		var code string
		err := s.each(ctx, `SELECT DISTINCT code FROM usage_bucket_definitions FINAL WHERE organization_id = $1 AND code IN $2`,
			args{org, ch.GroupSet{Value: codes[org]}}, []any{&code}, func() error {
				defined[code] = true
				return nil
			})
		if err != nil {
			return nil, err
		}
		if len(defined) == 0 {
			continue
		}
		t := touched{org: org}
		seen := map[string]bool{}
		for _, r := range rows {
			if r.OrganizationID != org || !defined[r.Code] {
				continue
			}
			b := metering.BucketFloor(r.Timestamp).UnixMicro()
			k := r.Code + "\x00" + r.SubscriptionID + "\x00" + fmt.Sprint(b)
			if seen[k] {
				continue
			}
			if len(seen) == 0 || b < t.minBucket {
				t.minBucket = b
			}
			if len(seen) == 0 || b > t.maxBucket {
				t.maxBucket = b
			}
			seen[k] = true
			if !slices.Contains(t.codes, r.Code) {
				t.codes = append(t.codes, r.Code)
			}
			t.keyCodes = append(t.keyCodes, r.Code)
			t.keySubs = append(t.keySubs, r.SubscriptionID)
			t.keyBuckets = append(t.keyBuckets, b)
		}
		out = append(out, t)
	}
	return out, nil
}

// codesSet returns the codes as a set parameter.
func codesSet(codes []string) ch.GroupSet {
	v := make([]any, len(codes))
	for i, c := range codes {
		v[i] = c
	}
	return ch.GroupSet{Value: v}
}

// maintain recomputes the buckets the rows touched, once inserted, and with
// tombstones tombstones the rows they left. A writer reads the definitions
// after its insert, so that a definition it does not see is backfilled
// from events that include its rows.
func (s *Store) maintain(ctx context.Context, rows []row, tombstones bool) error {
	ctx = consistent(ctx)
	all, err := s.touchedBuckets(ctx, rows)
	if err != nil {
		return fmt.Errorf("buckets: %w", err)
	}
	for _, t := range all {
		var a args
		scope := fmt.Sprintf(`organization_id = %s AND code IN %s
		AND timestamp >= fromUnixTimestamp64Micro(%s, 'UTC') AND timestamp < fromUnixTimestamp64Micro(%s, 'UTC')
		AND has(arrayZip(CAST(%s AS Array(String)), CAST(%s AS Array(String)), CAST(%s AS Array(Int64))), (code, subscription_id, %s))`,
			a.add(t.org), a.add(codesSet(t.codes)), a.add(t.minBucket), a.add(t.maxBucket+bucketMicros),
			a.add(t.keyCodes), a.add(t.keySubs), a.add(t.keyBuckets), bucketUs)
		defs := "organization_id = " + a.add(t.org) + " AND code IN " + a.add(codesSet(t.codes))
		if err := s.conn.Exec(ctx, recomputeSQL(scope, defs), a...); err != nil {
			return fmt.Errorf("recompute buckets: %w", err)
		}
		if !tombstones {
			continue
		}
		if err := s.conn.Exec(ctx, tombstoneSQL, t.org, codesSet(t.codes), t.minBucket, t.maxBucket, t.keyCodes, t.keySubs, t.keyBuckets); err != nil {
			return fmt.Errorf("tombstone buckets: %w", err)
		}
	}
	return nil
}

// consistent returns ctx with the settings of the bucket maintenance on
// replicated tables (ReplicatedMergeTree, SharedMergeTree): inserts return
// once a quorum of replicas holds them, and reads see every insert a quorum
// acknowledged. Two writers recomputing a bucket then read nested sets of
// its events, and the highest version holds them all. Tables that are not
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
	ready bool
}

// definitions returns the definitions of a code (all of them, or the one
// of the ID).
func (s *Store) definitions(ctx context.Context, org, code, id string) ([]definition, error) {
	a := args{org, code}
	where := "organization_id = $1 AND code = $2"
	if id != "" {
		where += " AND definition_id = " + a.add(id)
	}
	var (
		out      []definition
		property string
		dims     []string
		fold     map[string][]string
		ready    uint8
	)
	err := s.each(ctx, `SELECT property, dimensions, fold, ready FROM usage_bucket_definitions FINAL WHERE `+where+` ORDER BY definition_id`,
		a, []any{&property, &dims, &fold, &ready}, func() error {
			d := metering.BucketDefinition{OrganizationID: org, Code: code, Property: property, Dimensions: slices.Clone(dims)}
			if len(fold) > 0 {
				d.Values = maps.Clone(fold)
			}
			out = append(out, definition{BucketDefinition: d.Normalize(), ready: ready == 1})
			return nil
		})
	return out, err
}

func (s *Store) writeDefinition(ctx context.Context, d metering.BucketDefinition, ready, deleted bool) error {
	b2u := func(b bool) uint8 {
		if b {
			return 1
		}
		return 0
	}
	fold := d.Values
	if fold == nil {
		fold = map[string][]string{}
	}
	return s.conn.Exec(consistent(ctx), `INSERT INTO usage_bucket_definitions (organization_id, code, definition_id, property, dimensions, fold, ready, is_deleted, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		d.OrganizationID, d.Code, d.ID(), d.Property, d.Dimensions, fold, b2u(ready), b2u(deleted), s.sequences(1))
}

// DefineBuckets implements [eventstore.BucketDefiner]. The definition is
// written not ready, so that writers maintain it, then its buckets are
// backfilled from every stored event of the code, then it is written
// ready. A writer that missed the definition inserted its events before the
// definition was written, hence before the backfill read them.
func (s *Store) DefineBuckets(ctx context.Context, d metering.BucketDefinition) error {
	if err := checkDefinition(d); err != nil {
		return wrap("define buckets", err)
	}
	d = d.Normalize()
	existing, err := s.definitions(ctx, d.OrganizationID, d.Code, d.ID())
	if err != nil {
		return wrap("define buckets", err)
	}
	if len(existing) > 0 && existing[0].ready {
		return nil
	}
	if err := s.writeDefinition(ctx, d, false, false); err != nil {
		return wrap("define buckets", err)
	}
	var a args
	scope := "organization_id = " + a.add(d.OrganizationID) + " AND code = " + a.add(d.Code)
	defs := scope + " AND definition_id = " + a.add(d.ID())
	if err := s.conn.Exec(consistent(ctx), recomputeSQL(scope, defs), a...); err != nil {
		return wrap("define buckets: backfill", err)
	}
	return wrap("define buckets", s.writeDefinition(ctx, d, true, false))
}

// DropBuckets implements [eventstore.BucketDefiner].
func (s *Store) DropBuckets(ctx context.Context, d metering.BucketDefinition) error {
	if err := checkDefinition(d); err != nil {
		return wrap("drop buckets", err)
	}
	d = d.Normalize()
	if err := s.writeDefinition(ctx, d, false, true); err != nil {
		return wrap("drop buckets", err)
	}
	err := s.conn.Exec(ctx, `DELETE FROM usage_buckets_15m WHERE organization_id = $1 AND code = $2 AND definition_id = $3`,
		d.OrganizationID, d.Code, d.ID())
	return wrap("drop buckets", err)
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
	var defs []metering.BucketDefinition
	for _, d := range stored {
		if d.ready {
			defs = append(defs, d.BucketDefinition)
		}
	}
	d, ok := metering.CoveringDefinition(defs, r)
	if !ok {
		return nil, false, nil
	}
	var a args
	bounds := []string{
		"organization_id = " + a.add(q.OrganizationID),
		"code = " + a.add(q.Code),
		"subscription_id = " + a.add(q.SubscriptionID),
		"definition_id = " + a.add(d.ID()),
		"bucket >= " + a.time(r.From.UTC()),
		"bucket < " + a.time(r.To.UTC()),
	}
	count, amount, last := "events_count", "precise_total_amount_cents", "last_event_at"
	if q.Numeric {
		count, amount, last = "value_count", "value_precise_total_amount_cents", "latest_at"
	}
	where := append([]string{"version = top", "is_deleted = 0"}, filterConditions(&a, "dimensions", q)...)

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
FROM (
	SELECT *, max(version) OVER (PARTITION BY bucket) AS top
	FROM usage_buckets_15m FINAL
	WHERE ` + strings.Join(bounds, " AND ") + `
)
WHERE ` + strings.Join(where, " AND ") + `
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
	// them: the definition read again after the rows tells.
	after, err := s.definitions(ctx, q.OrganizationID, q.Code, d.ID())
	if err != nil {
		return nil, false, wrap("read buckets", err)
	}
	if len(after) == 0 || !after[0].ready {
		return nil, false, nil
	}
	return out, true, nil
}

// repairChunk is the number of buckets a repair recomputes per statement.
const repairChunk = 1000

// RepairBuckets recomputes the buckets of the events stored with a
// sequence at or after since (the store's sequences are the writers'
// clocks in nanoseconds), and tombstones the rows they left. It heals the
// buckets a write left stale: a write failing between its insert and its
// maintenance whose retry never came, or, on replicated tables, recomputes
// that ran against replicas missing a concurrent insert. A periodic job
// calls it with a watermark trailing the previous run by more than the
// longest write and the clock skew between writers.
func (s *Store) RepairBuckets(ctx context.Context, since time.Time) error {
	ctx = consistent(ctx)
	var (
		r       row
		buckets []row
	)
	err := s.each(ctx, `SELECT DISTINCT e.organization_id, e.code, e.subscription_id, fromUnixTimestamp64Micro(`+bucketUs+`, 'UTC')
		FROM events_enriched e
		WHERE e.sequence >= $1 AND (e.organization_id, e.code) IN (
			SELECT organization_id, code FROM usage_bucket_definitions FINAL
		)
		ORDER BY 1, 2, 3, 4`, args{uint64(max(since.UnixNano(), 0))},
		[]any{&r.OrganizationID, &r.Code, &r.SubscriptionID, &r.Timestamp}, func() error {
			buckets = append(buckets, r)
			return nil
		})
	if err != nil {
		return wrap("repair buckets", err)
	}
	for chunk := range slices.Chunk(buckets, repairChunk) {
		if err := s.maintain(ctx, chunk, true); err != nil {
			return wrap("repair buckets", err)
		}
	}
	return nil
}

// BucketStats implements [eventstore.BucketDefiner].
func (s *Store) BucketStats(ctx context.Context, d metering.BucketDefinition) (eventstore.BucketStats, error) {
	if err := checkDefinition(d); err != nil {
		return eventstore.BucketStats{}, wrap("bucket stats", err)
	}
	var st eventstore.BucketStats
	err := s.each(ctx, `SELECT toInt64(sum(n)), toInt64(max(n)) FROM (
		SELECT count() AS n FROM (
			SELECT subscription_id, bucket, is_deleted, version = max(version) OVER (PARTITION BY subscription_id, bucket) AS top
			FROM usage_buckets_15m FINAL WHERE organization_id = $1 AND code = $2 AND definition_id = $3
		) WHERE top AND is_deleted = 0 GROUP BY subscription_id, bucket
	)`, args{d.OrganizationID, d.Code, d.Normalize().ID()}, []any{&st.Rows, &st.MaxRowsPerBucket}, func() error { return nil })
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
