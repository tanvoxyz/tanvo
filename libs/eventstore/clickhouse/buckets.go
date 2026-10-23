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
// backfilled from every stored event of the code, a page of buckets at a
// time, then it is written ready. A writer that missed the definition
// inserted its events before the definition was written, hence before the
// backfill read them; a writer that saw it merges into its rows or
// recomputes them, as for any definition.
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
	if err := s.backfill(consistent(ctx), d); err != nil {
		return wrap("define buckets: backfill", err)
	}
	return wrap("define buckets", s.writeDefinition(ctx, d, true, false))
}

// backfill recomputes the rows of the definition in every bucket holding
// events of its code, by pages of buckets in (subscription, bucket) order.
func (s *Store) backfill(ctx context.Context, d metering.BucketDefinition) error {
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
		if err := s.recompute(ctx, page, d.ID()); err != nil {
			return err
		}
		after, first = page.keys[len(page.keys)-1], false
	}
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

// BucketStats implements [eventstore.BucketDefiner].
func (s *Store) BucketStats(ctx context.Context, d metering.BucketDefinition) (eventstore.BucketStats, error) {
	if err := checkDefinition(d); err != nil {
		return eventstore.BucketStats{}, wrap("bucket stats", err)
	}
	var st eventstore.BucketStats
	err := s.each(ctx, `SELECT toInt64(sum(n)), toInt64(max(n)) FROM (
		SELECT count() AS n FROM usage_buckets_15m FINAL
		WHERE organization_id = $1 AND code = $2 AND definition_id = $3 AND is_deleted = 0
		GROUP BY subscription_id, bucket
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
