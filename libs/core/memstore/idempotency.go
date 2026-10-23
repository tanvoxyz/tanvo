package memstore

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/idempotency"
)

// idempotencyKey identifies a record.
type idempotencyKey struct {
	org core.OrganizationID
	key string
}

// idempotencyData holds the idempotency records.
type idempotencyData struct {
	records map[idempotencyKey]idempotency.Record
}

func newIdempotencyData() *idempotencyData {
	return &idempotencyData{records: map[idempotencyKey]idempotency.Record{}}
}

func (d *idempotencyData) snapshot() *idempotencyData {
	return &idempotencyData{records: maps.Clone(d.records)}
}

func idempotencyClone(r idempotency.Record) idempotency.Record {
	r.RequestHash = slices.Clone(r.RequestHash)
	r.Response = slices.Clone(r.Response)
	return r
}

// idempotencyRepo implements idempotency.Repository.
type idempotencyRepo struct{ d *idempotencyData }

var _ idempotency.Repository = idempotencyRepo{}

func (r idempotencyRepo) Claim(_ context.Context, rec *idempotency.Record) (bool, error) {
	k := idempotencyKey{rec.OrganizationID, rec.Key}
	if old, ok := r.d.records[k]; ok && !old.Expired(rec.CreatedAt) {
		return false, nil
	}
	c := idempotencyClone(*rec)
	c.Response = nil
	r.d.records[k] = c
	return true, nil
}

func (r idempotencyRepo) Get(_ context.Context, org core.OrganizationID, key string) (*idempotency.Record, error) {
	rec, ok := r.d.records[idempotencyKey{org, key}]
	if !ok {
		return nil, notFound("idempotency_key", stringer(key))
	}
	c := idempotencyClone(rec)
	return &c, nil
}

func (r idempotencyRepo) Complete(_ context.Context, org core.OrganizationID, key string, response []byte, expiresAt time.Time) error {
	k := idempotencyKey{org, key}
	rec, ok := r.d.records[k]
	if !ok {
		return notFound("idempotency_key", stringer(key))
	}
	rec.Response = slices.Clone(response)
	if rec.Response == nil {
		rec.Response = []byte{}
	}
	rec.ExpiresAt = expiresAt.UTC()
	r.d.records[k] = rec
	return nil
}

func (r idempotencyRepo) Release(_ context.Context, org core.OrganizationID, key string) error {
	k := idempotencyKey{org, key}
	if rec, ok := r.d.records[k]; ok && rec.Pending() {
		delete(r.d.records, k)
	}
	return nil
}

func (r idempotencyRepo) Purge(_ context.Context, org core.OrganizationID, now time.Time, limit int) (int, error) {
	keys := slices.SortedFunc(maps.Keys(r.d.records), func(a, b idempotencyKey) int {
		if c := a.org.Compare(b.org); c != 0 {
			return c
		}
		return strings.Compare(a.key, b.key)
	})
	n := 0
	for _, k := range keys {
		if n >= limit {
			break
		}
		if k.org == org && r.d.records[k].Expired(now) {
			delete(r.d.records, k)
			n++
		}
	}
	return n, nil
}

func (r idempotencyRepo) Organizations(_ context.Context, now time.Time) ([]core.OrganizationID, error) {
	seen := map[core.OrganizationID]bool{}
	var out []core.OrganizationID
	for k, rec := range r.d.records {
		if rec.Expired(now) && !seen[k.org] {
			seen[k.org] = true
			out = append(out, k.org)
		}
	}
	slices.SortFunc(out, func(a, b core.OrganizationID) int { return a.Compare(b) })
	return out, nil
}
