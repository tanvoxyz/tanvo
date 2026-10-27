package clickhouse

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/ext"

	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// bucketMicros is the width of a bucket in microseconds.
const bucketMicros = int64(metering.BucketWidth / time.Microsecond)

// bucketUs is the expression of the bucket, in microseconds since the
// epoch, of the timestamp of an event.
var bucketUs = fmt.Sprintf("intDiv(toUnixTimestamp64Micro(timestamp), %[1]d) * %[1]d", bucketMicros)

// bucketRowUs is the expression of the start of a bucket row, in
// microseconds since the epoch.
const bucketRowUs = "toUnixTimestamp64Micro(bucket)"

const bucketColumns = `organization_id, code, subscription_id, definition_id, bucket, dimensions, dimensions_key,
	events_count, precise_total_amount_cents, last_event_at, last_ingested_at,
	value_count, units, max_value, latest_value, latest_at, latest_sequence, value_precise_total_amount_cents,
	max_sequence, version, writer, is_deleted`

// bucketKey is a bucket of a code and an external subscription.
type bucketKey struct {
	code, sub string
	us        int64 // start, in microseconds since the epoch
}

func keyOf(r row) bucketKey {
	return bucketKey{code: r.Code, sub: r.SubscriptionID, us: metering.BucketFloor(r.Timestamp).UnixMicro()}
}

func compareKeys(a, b bucketKey) int {
	return cmp.Or(cmp.Compare(a.code, b.code), cmp.Compare(a.sub, b.sub), cmp.Compare(a.us, b.us))
}

// buckets is a set of buckets of one organization.
type buckets struct {
	org  string
	keys []bucketKey // sorted, distinct
}

// with returns the buckets of keys, of the same organization.
func (b buckets) with(keys []bucketKey) buckets { return buckets{org: b.org, keys: keys} }

func (b buckets) arrays() (codes, subs []string, starts []int64) {
	for _, k := range b.keys {
		codes, subs, starts = append(codes, k.code), append(subs, k.sub), append(starts, k.us)
	}
	return codes, subs, starts
}

// set returns the distinct values as a set parameter.
func set[T comparable](values []T) ch.GroupSet {
	var v []any
	seen := map[T]bool{}
	for _, x := range values {
		if !seen[x] {
			seen[x] = true
			v = append(v, x)
		}
	}
	return ch.GroupSet{Value: v}
}

// scope returns the conditions selecting the rows of the buckets in a
// table: on the sorting key (organization, code, subscription, the time
// column at), then exactly by (code, subscription, us), us being the
// expression of a row's bucket start in microseconds.
func (b buckets) scope(a *args, us, at string) string {
	codes, subs, starts := b.arrays()
	lo, hi := slices.Min(starts), slices.Max(starts)+bucketMicros
	return fmt.Sprintf(`organization_id = %s AND code IN %s AND subscription_id IN %s
		AND %s >= fromUnixTimestamp64Micro(%s, 'UTC') AND %s < fromUnixTimestamp64Micro(%s, 'UTC')
		AND has(arrayZip(CAST(%s AS Array(String)), CAST(%s AS Array(String)), CAST(%s AS Array(Int64))), (code, subscription_id, %s))`,
		a.add(b.org), a.add(set(codes)), a.add(set(subs)), at, a.add(lo), at, a.add(hi), a.add(codes), a.add(subs), a.add(starts), us)
}

// counters is the version a write gives the rows of each bucket.
type counters map[bucketKey]uint64

// sql returns the CTE "ctr" (code, subscription_id, bucket_us, counter) of
// the versions of the buckets.
func (c counters) sql(a *args, b buckets) string {
	return "ctr AS (\n\t" + c.query(a, b) + "\n)"
}

// query returns the query of the values of the buckets, as rows (code,
// subscription_id, bucket_us, counter).
func (c counters) query(a *args, b buckets) string {
	codes, subs, starts := b.arrays()
	values := make([]uint64, len(b.keys))
	for i, k := range b.keys {
		values[i] = c[k]
	}
	return fmt.Sprintf(`SELECT t.1 AS code, t.2 AS subscription_id, t.3 AS bucket_us, t.4 AS counter
	FROM (SELECT arrayJoin(arrayZip(CAST(%s AS Array(String)), CAST(%s AS Array(String)), CAST(%s AS Array(Int64)), CAST(%s AS Array(UInt64)))) AS t)`,
		a.add(codes), a.add(subs), a.add(starts), a.add(values))
}

// defined returns, per organization, the buckets of the rows whose code
// has definitions (ready or not).
func (s *Store) defined(ctx context.Context, rows []row) ([]buckets, error) {
	keys := map[string]map[bucketKey]bool{}
	var orgs []string
	for _, r := range rows {
		if keys[r.OrganizationID] == nil {
			keys[r.OrganizationID] = map[bucketKey]bool{}
			orgs = append(orgs, r.OrganizationID)
		}
		keys[r.OrganizationID][keyOf(r)] = true
	}
	var out []buckets
	for _, org := range orgs {
		var codes []string
		for k := range keys[org] {
			codes = append(codes, k.code)
		}
		has := map[string]bool{}
		var code string
		err := s.each(ctx, `SELECT DISTINCT code FROM usage_bucket_definitions FINAL WHERE organization_id = $1 AND code IN $2`,
			args{org, set(codes)}, []any{&code}, func() error {
				has[code] = true
				return nil
			})
		if err != nil {
			return nil, err
		}
		b := buckets{org: org}
		for k := range keys[org] {
			if has[k.code] {
				b.keys = append(b.keys, k)
			}
		}
		if len(b.keys) > 0 {
			slices.SortFunc(b.keys, compareKeys)
			out = append(out, b)
		}
	}
	return out, nil
}

// state is what a write reads of a bucket before writing it: the highest
// version of its rows and the highest sequence of the events they hold.
type state struct {
	version, sequence uint64
}

// states reads the state of the buckets, over every version of their rows,
// tombstones included, and which of their codes have definitions (ready
// or not), in one statement.
func (s *Store) states(ctx context.Context, b buckets) (map[bucketKey]state, map[string]bool, error) {
	var a args
	sql := `SELECT 0, code, subscription_id, ` + bucketRowUs + ` AS us, max(version), max(max_sequence)
		FROM usage_buckets_15m WHERE ` + b.scope(&a, bucketRowUs, "bucket") + ` GROUP BY 2, 3, 4
		UNION ALL
		SELECT DISTINCT 1, code, '', toInt64(0), toUInt64(0), toUInt64(0) FROM usage_bucket_definitions FINAL
		WHERE organization_id = ` + a.add(b.org) + ` AND code IN ` + a.add(set(keyCodes(b.keys)))
	out, defined := map[bucketKey]state{}, map[string]bool{}
	var (
		kind uint8
		k    bucketKey
		st   state
	)
	err := s.each(ctx, sql, a, []any{&kind, &k.code, &k.sub, &k.us, &st.version, &st.sequence}, func() error {
		if kind == 1 {
			defined[k.code] = true
		} else {
			out[k] = st
		}
		return nil
	})
	return out, defined, err
}

// conflicts returns the buckets holding rows that another write, of the
// kind that conflicts with this one, wrote with the version this one did.
// A writer checks after its insert: of two writers giving a bucket the
// same version, the one checking last sees the other's rows (inserts are
// acknowledged by a quorum, reads are sequentially consistent), and
// recomputes the bucket above both.
//
// A merge conflicts with any other write: its rows build on the version
// below, which the other write's rows hide or are hidden by. A recompute
// conflicts with merges only: two recomputes of a bucket at the same
// version both hold every event stored before their reads, and an event
// one of them misses was stored after that read, by a write that still
// merges it above them or recomputes after them. Writers set the lowest bit
// of the writer ID of merges, and clear it for recomputes.
//
// A merge also conflicts with the rows of a lower version that may hold
// its events (their max_sequence is at or above the lowest sequence low
// of its events in the bucket): a recompute that read the merge's events
// and inserted its rows after the merge read the state of the bucket, at
// a version the merge built on, so that the merge may have added its
// events to rows already holding them. Pass low nil for a recompute.
//
// Rows of a higher version need no check: a write's version is one above
// the highest it read, so every write above this one descends, version by
// version, from a write at this version, which is this one or a
// conflicting one. The check relies on the rows it looks for still being
// there, which a merge of the table with rows of a higher version could
// collapse in the milliseconds between the insert and the check:
// [Store.RepairBuckets] heals what that misses.
func (s *Store) conflicts(ctx context.Context, b buckets, c counters, writer uint64, low map[bucketKey]uint64) (map[bucketKey]bool, error) {
	var a args
	kind := ""
	if writer&1 == 0 {
		kind = " AND bitAnd(r.writer, 1) = 1"
	}
	cond := "r.version = t.counter" + kind
	if low != nil {
		lows := make(counters, len(b.keys))
		for _, k := range b.keys {
			lows[k] = low[k]
		}
		// The lowest sequences go in the CTE of the counters' shape.
		cond = "(r.version = t.counter OR (r.version < t.counter AND r.max_sequence >= l.counter))"
		return s.conflicting(ctx, &a, b, c, writer, cond, `INNER JOIN (`+lows.query(&a, b)+`) l
			ON r.code = l.code AND r.subscription_id = l.subscription_id AND r.bucket_us = l.bucket_us`)
	}
	return s.conflicting(ctx, &a, b, c, writer, cond, "")
}

func (s *Store) conflicting(ctx context.Context, a *args, b buckets, c counters, writer uint64, cond, join string) (map[bucketKey]bool, error) {
	sql := `WITH ` + c.sql(a, b) + `
		SELECT DISTINCT r.code, r.subscription_id, r.bucket_us
		FROM (SELECT code, subscription_id, ` + bucketRowUs + ` AS bucket_us, version, writer, max_sequence FROM usage_buckets_15m
			WHERE ` + b.scope(a, bucketRowUs, "bucket") + ` AND writer != ` + a.add(writer) + `) r
		INNER JOIN ctr t ON r.code = t.code AND r.subscription_id = t.subscription_id AND r.bucket_us = t.bucket_us
		` + join + `
		WHERE ` + cond
	out := map[bucketKey]bool{}
	var k bucketKey
	err := s.each(ctx, sql, *a, []any{&k.code, &k.sub, &k.us}, func() error {
		out[k] = true
		return nil
	})
	return out, err
}

// rowKey is the expression, over usage_bucket_definitions, of the
// definition_id of the bucket rows of a definition's generation (see
// [instanceKey]).
const rowKey = "if(generation = 0, definition_id, concat(definition_id, '.', toString(generation)))"

// instanceKey returns the definition_id of the bucket rows of generation
// of the definition id: each generation of a definition (each time it is
// defined again after a drop) has rows of its own, so that the rows a
// writer or a backfill of a dropped generation leaves behind are never
// read. Definitions written before generations existed have generation
// 0 and rows keyed by their ID.
func instanceKey(id string, generation uint64) string {
	if generation == 0 {
		return id
	}
	return id + "." + strconv.FormatUint(generation, 10)
}

// aggregated returns the CTEs "defs", "valued" and "rows": the events of
// the CTE "ev" aggregated into the rows of the buckets of the definitions
// selected by defs, keyed by the generation of each definition.
func aggregated(defs string) string {
	zero := fmt.Sprintf("toDecimal256(0, %d)", scale)
	return `defs AS (
	SELECT organization_id, code, ` + rowKey + ` AS row_key, property, dimensions, fold FROM usage_bucket_definitions FINAL WHERE ` + defs + `
),
valued AS (
	SELECT ev.*, d.row_key AS definition_id,
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
		sumIf(ifNull(amount, ` + zero + `), isnum) AS vamt, max(sequence) AS max_seq
	FROM valued
	GROUP BY organization_id, code, subscription_id, definition_id, bucket_us, dims
)`
}

// evColumns are the columns of the events the bucket statements read,
// before the bucket.
const evColumns = `organization_id, code, subscription_id, timestamp, sequence, properties,
		precise_total_amount_cents AS amount, ingested_at, `

// mergeSQL returns the statement adding the events of the CTE "ev" to the
// current rows (CTE "cur", read FINAL) of their buckets: counts and sums
// add, the maximum and the last times take the greatest, the latest value
// the most recent by (timestamp, sequence). Rows are written as the
// version of their bucket in the CTE "ctr", by writer.
func mergeSQL(ev, ctr, defs, cur, writer string) string {
	const newer = `(c.latest_at IS NULL OR (r.latest_at IS NOT NULL AND (r.latest_at, r.latest_sequence) > (c.latest_at, c.latest_sequence)))`
	// Without join_use_nulls, a row the bucket does not hold yet joins the
	// defaults: zeros, the epoch, and NULL for the nullable columns.
	return `INSERT INTO usage_buckets_15m (` + bucketColumns + `)
WITH
ev AS (` + ev + `),
` + ctr + `,
` + aggregated(defs) + `,
cur AS (` + cur + `)
SELECT r.organization_id, r.code, r.subscription_id, r.definition_id, fromUnixTimestamp64Micro(r.bucket_us, 'UTC'),
	r.dims, toString(r.dims),
	r.n + c.events_count, r.amt + c.precise_total_amount_cents,
	greatest(r.last_at, c.last_event_at), greatest(r.ingested, c.last_ingested_at),
	r.vn + c.value_count, r.units + c.units,
	if(isNull(c.max_value), r.max_value, if(isNull(r.max_value), c.max_value, greatest(assumeNotNull(r.max_value), assumeNotNull(c.max_value)))),
	if(` + newer + `, r.latest_value, c.latest_value),
	if(` + newer + `, r.latest_at, c.latest_at),
	if(` + newer + `, r.latest_sequence, c.latest_sequence),
	r.vamt + c.value_precise_total_amount_cents,
	greatest(r.max_seq, c.max_sequence), t.counter, ` + writer + `, 0
FROM rows r
INNER JOIN ctr t ON r.code = t.code AND r.subscription_id = t.subscription_id AND r.bucket_us = t.bucket_us
LEFT JOIN cur c ON r.code = c.code AND r.subscription_id = c.subscription_id AND r.definition_id = c.definition_id
	AND r.bucket_us = c.bucket_us AND toString(r.dims) = c.dimensions_key`
}

// curColumns are the columns of the current rows a merge reads.
const curColumns = `code, subscription_id, definition_id, ` + bucketRowUs + ` AS bucket_us, dimensions_key,
	events_count, precise_total_amount_cents, last_event_at, last_ingested_at, value_count, units, max_value,
	latest_value, latest_at, latest_sequence, value_precise_total_amount_cents, max_sequence`

// recomputeSQL returns the statement aggregating again, from the events
// of the buckets of the CTE "ctr" (ev, read FINAL), the rows of the
// definitions selected by defs, as the version of their bucket, by writer.
// Every row also records the highest sequence of the events stored in its
// bucket, every copy included (seqs, read without FINAL): a merge of
// events that a recompute may already hold sees it, and recomputes
// instead.
func recomputeSQL(ev, seqs, ctr, defs, writer string) string {
	return `INSERT INTO usage_buckets_15m (` + bucketColumns + `)
WITH
ev AS (` + ev + `),
seqs AS (` + seqs + `),
` + ctr + `,
` + aggregated(defs) + `
SELECT r.organization_id, r.code, r.subscription_id, r.definition_id, fromUnixTimestamp64Micro(r.bucket_us, 'UTC'),
	r.dims, toString(r.dims), r.n, r.amt, r.last_at, r.ingested, r.vn, r.units, r.max_value, r.latest_value,
	r.latest_at, r.latest_sequence, r.vamt, greatest(r.max_seq, q.seq), t.counter, ` + writer + `, 0
FROM rows r
INNER JOIN ctr t ON r.code = t.code AND r.subscription_id = t.subscription_id AND r.bucket_us = t.bucket_us
LEFT JOIN seqs q ON r.code = q.code AND r.subscription_id = q.subscription_id AND r.bucket_us = q.bucket_us`
}

// tombstoneSQL returns the statement tombstoning, as the version of their
// bucket in the CTE "ctr", the current rows (cur, read FINAL) of a lower
// version: after a recompute, the rows of dimension values the events of
// the bucket no longer hold.
func tombstoneSQL(ctr, cur, writer string) string {
	return `INSERT INTO usage_buckets_15m (organization_id, code, subscription_id, definition_id, bucket, dimensions, dimensions_key,
	last_event_at, last_ingested_at, max_sequence, version, writer, is_deleted)
WITH ` + ctr + `
SELECT c.organization_id, c.code, c.subscription_id, c.definition_id, c.bucket, c.dimensions, c.dimensions_key,
	c.bucket, c.bucket, c.max_sequence, t.counter, ` + writer + `, 1
FROM (` + cur + `) c
INNER JOIN ctr t ON c.code = t.code AND c.subscription_id = t.subscription_id AND c.bucket_us = t.bucket_us
WHERE c.version < t.counter`
}

// maxRounds bounds the rounds of a recompute of buckets other writers keep
// writing.
const maxRounds = 10

// backoff is the base of the random pause between the rounds of a
// recompute, doubling with every round.
const backoff = 10 * time.Millisecond

// keyLocks serializes, within a process, the maintenance of each bucket,
// so that the writers of a process never give a bucket the same version;
// writers of different processes rely on [Store.conflicts]. The zero value
// is ready.
type keyLocks struct {
	mu sync.Mutex
	m  map[lockKey]*keyLock
}

type lockKey struct {
	org string
	bucketKey
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

// lock locks the buckets, in their order, and returns the unlocking.
func (l *keyLocks) lock(b buckets) (unlock func()) {
	held := make([]*keyLock, len(b.keys))
	l.mu.Lock()
	if l.m == nil {
		l.m = map[lockKey]*keyLock{}
	}
	for i, k := range b.keys {
		key := lockKey{org: b.org, bucketKey: k}
		kl := l.m[key]
		if kl == nil {
			kl = &keyLock{}
			l.m[key] = kl
		}
		kl.refs++
		held[i] = kl
	}
	l.mu.Unlock()
	for _, kl := range held {
		kl.mu.Lock()
	}
	return func() {
		for _, kl := range held {
			kl.mu.Unlock()
		}
		l.mu.Lock()
		for i, k := range b.keys {
			if held[i].refs--; held[i].refs == 0 {
				delete(l.m, lockKey{org: b.org, bucketKey: k})
			}
		}
		l.mu.Unlock()
	}
}

// recompute aggregates the buckets again from the events, for every
// definition of their codes or, with id, for the generation whose rows are
// keyed by id ([instanceKey]), as a version above
// every row of each bucket, then tombstones the rows of the definitions it
// did not write. A bucket another writer merged into meanwhile at that
// version is recomputed again, higher (see [Store.conflicts]).
func (s *Store) recompute(ctx context.Context, b buckets, id string) error {
	defer s.locks.lock(b)()
	return s.recomputeLocked(ctx, b, id)
}

func (s *Store) recomputeLocked(ctx context.Context, b buckets, id string) error {
	if s.hook != nil {
		s.hook("recompute")
	}
	for round := 0; len(b.keys) > 0; round++ {
		if round == maxRounds {
			return fmt.Errorf("recompute buckets: %d buckets still written by other writers after %d rounds", len(b.keys), round)
		}
		if round > 0 {
			// Writers of other processes contending for a bucket back off
			// apart.
			t := time.NewTimer(time.Duration(rand.Int64N(int64(backoff) << min(round, 6))))
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		st, _, err := s.states(ctx, b)
		if err != nil {
			return fmt.Errorf("recompute buckets: %w", err)
		}
		next := counters{}
		for _, k := range b.keys {
			next[k] = st[k].version + 1
		}
		writer := rand.Uint64() &^ 1
		if err := s.recomputeAt(ctx, b, next, writer, id); err != nil {
			return err
		}
		held, err := s.conflicts(ctx, b, next, writer, nil)
		if err != nil {
			return fmt.Errorf("recompute buckets: %w", err)
		}
		b = b.with(slices.DeleteFunc(slices.Clone(b.keys), func(k bucketKey) bool { return !held[k] }))
	}
	return nil
}

// recomputeAt aggregates the buckets again from the events as the versions
// next, by writer, and tombstones the rows of a lower version it did not
// write.
func (s *Store) recomputeAt(ctx context.Context, b buckets, next counters, writer uint64, id string) error {
	var a args
	w := a.add(writer)
	ev := `SELECT ` + evColumns + bucketUs + ` AS bucket_us FROM events_enriched FINAL WHERE ` + b.scope(&a, bucketUs, "timestamp")
	seqs := `SELECT code, subscription_id, ` + bucketUs + ` AS bucket_us, max(sequence) AS seq FROM events_enriched
		WHERE ` + b.scope(&a, bucketUs, "timestamp") + ` GROUP BY 1, 2, 3`
	defs := "organization_id = " + a.add(b.org) + " AND code IN " + a.add(set(keyCodes(b.keys)))
	if id != "" {
		defs += " AND " + rowKey + " = " + a.add(id)
	}
	if err := s.conn.Exec(ctx, recomputeSQL(ev, seqs, next.sql(&a, b), defs, w), a...); err != nil {
		return fmt.Errorf("recompute buckets: %w", err)
	}
	a = nil
	w = a.add(writer)
	ctr := next.sql(&a, b)
	cur := `SELECT organization_id, code, subscription_id, definition_id, bucket, ` + bucketRowUs + ` AS bucket_us,
		dimensions, dimensions_key, max_sequence, version
		FROM usage_buckets_15m FINAL WHERE ` + b.scope(&a, bucketRowUs, "bucket")
	if id != "" {
		cur += " AND definition_id = " + a.add(id)
	}
	if err := s.conn.Exec(ctx, tombstoneSQL(ctr, cur, w), a...); err != nil {
		return fmt.Errorf("tombstone buckets: %w", err)
	}
	return nil
}

func keyCodes(keys []bucketKey) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.code
	}
	return out
}

// merge adds the stored rows to the rows of their buckets, reading each
// bucket's current rows once, at a cost proportional to the rows, not to
// the events of their buckets. A bucket is recomputed instead when its rows
// may already hold some of the rows (they hold a sequence at or above the
// lowest of the bucket's new rows: a recompute ran since their insert), or
// when another writer wrote it meanwhile, at its version or, holding such a
// sequence, below it (see [Store.conflicts]).
func (s *Store) merge(ctx context.Context, b buckets, stored []row) error {
	defer s.locks.lock(b)()
	st, defined, err := s.states(ctx, b)
	if err != nil {
		return fmt.Errorf("merge buckets: %w", err)
	}
	if s.hook != nil {
		s.hook("merge: states read")
	}
	b = b.with(slices.DeleteFunc(slices.Clone(b.keys), func(k bucketKey) bool { return !defined[k.code] }))
	lowest := map[bucketKey]uint64{}
	for _, r := range stored {
		k := keyOf(r)
		if l, ok := lowest[k]; !ok || r.sequence < l {
			lowest[k] = r.sequence
		}
	}
	next := counters{}
	var mergeable, again []bucketKey
	for _, k := range b.keys {
		if l, ok := lowest[k]; !ok || st[k].sequence >= l {
			again = append(again, k)
			continue
		}
		next[k] = st[k].version + 1
		mergeable = append(mergeable, k)
	}
	if len(mergeable) > 0 {
		m := b.with(mergeable)
		in := map[bucketKey]bool{}
		for _, k := range mergeable {
			in[k] = true
		}
		batch, codes, err := batchTable(m.org, stored, in)
		if err != nil {
			return fmt.Errorf("merge buckets: %w", err)
		}
		writer := rand.Uint64() | 1
		var a args
		w := a.add(writer)
		// The rows of the write go with the statement, as an external
		// table: the merge reads them, not the events of their buckets.
		ev := `SELECT organization_id, code, subscription_id, timestamp, sequence, properties,
			CAST(amount AS Nullable(Decimal(76, 26))) AS amount, ingested_at, ` + bucketUs + ` AS bucket_us FROM ` + batch.Name()
		ctr := next.sql(&a, m)
		defs := "organization_id = " + a.add(m.org) + " AND code IN " + a.add(set(codes))
		cur := `SELECT ` + curColumns + ` FROM usage_buckets_15m FINAL WHERE ` + m.scope(&a, bucketRowUs, "bucket")
		if err := s.conn.Exec(ch.Context(ctx, ch.WithExternalTable(batch)), mergeSQL(ev, ctr, defs, cur, w), a...); err != nil {
			return fmt.Errorf("merge buckets: %w", err)
		}
		held, err := s.conflicts(ctx, m, next, writer, lowest)
		if err != nil {
			return fmt.Errorf("merge buckets: %w", err)
		}
		for _, k := range mergeable {
			if held[k] {
				again = append(again, k)
			}
		}
	}
	if len(again) > 0 {
		slices.SortFunc(again, compareKeys)
		return s.recomputeLocked(ctx, b.with(again), "")
	}
	return nil
}

// batchTable returns the external table of the stored rows of org in the
// buckets in, and their codes.
func batchTable(org string, stored []row, in map[bucketKey]bool) (*ext.Table, []string, error) {
	t, err := ext.NewTable("batch",
		ext.Column("organization_id", "String"),
		ext.Column("code", "String"),
		ext.Column("subscription_id", "String"),
		ext.Column("timestamp", "DateTime64(6, 'UTC')"),
		ext.Column("sequence", "UInt64"),
		ext.Column("properties", "Map(String, String)"),
		ext.Column("amount", "Nullable(String)"),
		ext.Column("ingested_at", "DateTime64(6, 'UTC')"),
	)
	if err != nil {
		return nil, nil, err
	}
	var codes []string
	for _, r := range stored {
		if r.OrganizationID != org || !in[keyOf(r)] {
			continue
		}
		if err := t.Append(r.OrganizationID, r.Code, r.SubscriptionID, r.Timestamp, r.sequence, r.props, r.amount, r.IngestedAt); err != nil {
			return nil, nil, err
		}
		codes = append(codes, r.Code)
	}
	return t, codes, nil
}

// maintain brings the buckets of the rows up to date: the merged rows, new
// rows a write accepted, are added to their buckets, and the buckets of
// the recomputed rows (replacements and the copies they replace) are
// aggregated again from the events. The buckets of the healed rows, stored
// copies of duplicates, are recomputed when they hold no event as recent as
// the copy: the write that stored it failed before its maintenance. A
// writer reads the definitions after its insert, so that a definition it
// does not see is backfilled from events that include its rows.
func (s *Store) maintain(ctx context.Context, merged, recomputed, healed []row) error {
	ctx = consistent(ctx)
	unmaintained, err := s.unmaintained(ctx, healed)
	if err != nil {
		return fmt.Errorf("buckets: %w", err)
	}
	recomputed = append(slices.Clip(recomputed), unmaintained...)
	again := map[string]map[bucketKey]bool{}
	for _, r := range recomputed {
		if again[r.OrganizationID] == nil {
			again[r.OrganizationID] = map[bucketKey]bool{}
		}
		again[r.OrganizationID][keyOf(r)] = true
	}
	// The merges read the definitions with the state of the buckets.
	byOrg := map[string][]row{}
	keys := map[string]map[bucketKey]bool{}
	var orgs []buckets
	for _, r := range merged {
		k := keyOf(r)
		if again[r.OrganizationID][k] {
			continue
		}
		if keys[r.OrganizationID] == nil {
			keys[r.OrganizationID] = map[bucketKey]bool{}
			orgs = append(orgs, buckets{org: r.OrganizationID})
		}
		byOrg[r.OrganizationID] = append(byOrg[r.OrganizationID], r)
		keys[r.OrganizationID][k] = true
	}
	for i := range orgs {
		b := &orgs[i]
		for k := range keys[b.org] {
			b.keys = append(b.keys, k)
		}
		slices.SortFunc(b.keys, compareKeys)
	}
	for _, b := range orgs {
		for keys := range slices.Chunk(b.keys, repairChunk) {
			if err := s.merge(ctx, b.with(keys), byOrg[b.org]); err != nil {
				return err
			}
		}
	}
	orgs, err = s.defined(ctx, recomputed)
	if err != nil {
		return fmt.Errorf("buckets: %w", err)
	}
	for _, b := range orgs {
		for keys := range slices.Chunk(b.keys, repairChunk) {
			if err := s.recompute(ctx, b.with(keys), ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// unmaintained returns, as rows of their buckets, the buckets of the
// copies that have definitions and hold no event with a sequence at or
// above the copy's: the maintenance of the write that stored the copy never
// ran (a merge or a recompute holding the copy records its sequence or a
// higher one).
func (s *Store) unmaintained(ctx context.Context, copies []row) ([]row, error) {
	if len(copies) == 0 {
		return nil, nil
	}
	highest := map[lockKey]uint64{}
	var orgs []buckets
	at := map[string]int{}
	for _, r := range copies {
		k := lockKey{org: r.OrganizationID, bucketKey: keyOf(r)}
		seq, seen := highest[k]
		if !seen {
			i, ok := at[r.OrganizationID]
			if !ok {
				i = len(orgs)
				at[r.OrganizationID] = i
				orgs = append(orgs, buckets{org: r.OrganizationID})
			}
			orgs[i].keys = append(orgs[i].keys, k.bucketKey)
		}
		highest[k] = max(seq, r.sequence)
	}
	var out []row
	for _, b := range orgs {
		slices.SortFunc(b.keys, compareKeys)
		for keys := range slices.Chunk(b.keys, repairChunk) {
			st, defined, err := s.states(ctx, b.with(keys))
			if err != nil {
				return nil, err
			}
			for _, k := range keys {
				if defined[k.code] && st[k].sequence < highest[lockKey{org: b.org, bucketKey: k}] {
					out = append(out, row{Row: ingest.Row{Event: metering.Event{
						OrganizationID: b.org, Code: k.code, SubscriptionID: k.sub, Timestamp: time.UnixMicro(k.us).UTC(),
					}}})
				}
			}
		}
	}
	return out, nil
}

// repairChunk is the number of buckets a statement maintains.
const repairChunk = 1000

// RepairBuckets recomputes the buckets of the events stored with a
// sequence at or after since (the store's sequences are the writers' clocks
// in nanoseconds) and tombstones the rows they left.
//
// It is required for correctness, not an optimization: the checks a write
// makes after its insert (see [Store.conflicts]) look for rows that a
// background merge of the table may collapse before the check reads them,
// a write failing between its insert and its bucket maintenance may never
// be retried, and a write may run against a replica missing a concurrent
// insert. Each leaves buckets that only this repair heals. Schedule it
// every 5 to 15 minutes, with since the start of the previous run minus a
// margin above the longest write and the clock skew between writers (10
// minutes is ample): runs overlap, and recomputing a bucket that is right
// is harmless.
func (s *Store) RepairBuckets(ctx context.Context, since time.Time) error {
	ctx = consistent(writing(ctx))
	from := uint64(max(since.UnixNano(), 0))
	var (
		recomputed []row
		r          row
	)
	err := s.each(ctx, `SELECT DISTINCT organization_id, code, subscription_id, fromUnixTimestamp64Micro(`+bucketUs+`, 'UTC')
		FROM events_enriched
		WHERE sequence >= $1 AND (organization_id, code) IN (SELECT organization_id, code FROM usage_bucket_definitions FINAL)`,
		args{from}, []any{&r.OrganizationID, &r.Code, &r.SubscriptionID, &r.Timestamp}, func() error {
			recomputed = append(recomputed, r)
			return nil
		})
	if err != nil {
		return wrap("repair buckets", err)
	}
	return wrap("repair buckets", s.maintain(ctx, nil, recomputed, nil))
}
