package memstore

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// table is one in-memory table of rows of type V keyed by K. Rows are stored
// and returned as copies made by clone, so callers never share storage with
// the table and a snapshot can share the stored rows.
type table[K interface {
	comparable
	Compare(K) int
	String() string
}, V any] struct {
	rows  map[K]V
	clone func(V) V
}

// newTable returns an empty table; clone deep-copies a row (copying its
// slices and maps). A nil clone copies the struct only.
func newTable[K interface {
	comparable
	Compare(K) int
	String() string
}, V any](clone func(V) V,
) *table[K, V] {
	if clone == nil {
		clone = func(v V) V { return v }
	}
	return &table[K, V]{rows: map[K]V{}, clone: clone}
}

// snapshot returns a table with the same rows; stored rows are never
// mutated in place, so sharing them is safe.
func (t *table[K, V]) snapshot() *table[K, V] {
	return &table[K, V]{rows: maps.Clone(t.rows), clone: t.clone}
}

// get returns a copy of the row with key k.
func (t *table[K, V]) get(k K) (*V, bool) {
	v, ok := t.rows[k]
	if !ok {
		return nil, false
	}
	c := t.clone(v)
	return &c, true
}

// has reports whether a row with key k exists.
func (t *table[K, V]) has(k K) bool {
	_, ok := t.rows[k]
	return ok
}

// put stores a copy of v under k.
func (t *table[K, V]) put(k K, v V) { t.rows[k] = t.clone(v) }

// del removes the row with key k and reports whether it existed.
func (t *table[K, V]) del(k K) bool {
	_, ok := t.rows[k]
	delete(t.rows, k)
	return ok
}

// find returns copies of the rows that match pred, ordered by key (UUIDv7
// creation order).
func (t *table[K, V]) find(pred func(*V) bool) []*V {
	keys := slices.SortedFunc(maps.Keys(t.rows), func(a, b K) int { return a.Compare(b) })
	var out []*V
	for _, k := range keys {
		v := t.rows[k]
		if pred == nil || pred(&v) {
			c := t.clone(v)
			out = append(out, &c)
		}
	}
	return out
}

// first returns a copy of the first row (in key order) matching pred.
func (t *table[K, V]) first(pred func(*V) bool) (*V, bool) {
	rows := t.find(pred)
	if len(rows) == 0 {
		return nil, false
	}
	return rows[0], true
}

// any reports whether a row matches pred.
func (t *table[K, V]) any(pred func(*V) bool) bool {
	for _, v := range t.rows {
		if pred(&v) {
			return true
		}
	}
	return false
}

// count returns the number of rows matching pred.
func (t *table[K, V]) count(pred func(*V) bool) int {
	n := 0
	for _, v := range t.rows {
		if pred == nil || pred(&v) {
			n++
		}
	}
	return n
}

// deleteWhere removes the rows matching pred and returns how many.
func (t *table[K, V]) deleteWhere(pred func(*V) bool) int {
	n := 0
	for k, v := range t.rows {
		if pred(&v) {
			delete(t.rows, k)
			n++
		}
	}
	return n
}

// paginate cuts rows (already in key order) into the page asked by req,
// using the key of the last item as the cursor.
func paginate[K interface {
	comparable
	Compare(K) int
	String() string
}, V any](rows []*V, key func(*V) K, req core.PageRequest,
) (core.Page[*V], error) {
	start := 0
	if req.Token != "" {
		// The token is the canonical form of a UUID, as PostgreSQL parses
		// it; the canonical lower-case form sorts like the UUID bytes.
		u, err := core.ParsePageToken(req.Token)
		if err != nil {
			return core.Page[*V]{}, err
		}
		i, found := slices.BinarySearchFunc(rows, u.String(), func(v *V, tok string) int {
			return cmp.Compare(key(v).String(), tok)
		})
		if found {
			i++
		}
		start = i
	}
	end := min(start+req.Limit(), len(rows))
	p := core.Page[*V]{Items: rows[start:end]}
	if end < len(rows) && end > start {
		p.NextToken = key(rows[end-1]).String()
	}
	return p, nil
}

// notFound returns core.ErrNotFound wrapped with the entity name and key.
func notFound(entity string, key fmt.Stringer) error {
	return fmt.Errorf("%s %s: %w", entity, key, core.ErrNotFound)
}

// conflict returns core.ErrConflict wrapped with the entity and the rule.
func conflict(entity, rule string) error {
	return fmt.Errorf("%s: %s: %w", entity, rule, core.ErrConflict)
}

// stringer adapts a string to fmt.Stringer for error messages.
type stringer string

func (s stringer) String() string { return string(s) }

// paginateSorted cuts rows, already sorted in the order of a list sorted
// by a sort key then by ID, into the page asked by req. cursor returns the
// position of a row; after reports whether a row comes after the decoded
// cursor of req.Token in the list order.
func paginateSorted[V any](rows []*V, cursor func(*V) core.Cursor, after func(*V, core.Cursor) bool, req core.PageRequest) (core.Page[*V], error) {
	start := 0
	if req.Token != "" {
		c, err := core.DecodeCursor(req.Token)
		if err != nil {
			return core.Page[*V]{}, err
		}
		start = len(rows)
		for i, r := range rows {
			if after(r, c) {
				start = i
				break
			}
		}
	}
	end := min(start+req.Limit(), len(rows))
	p := core.Page[*V]{Items: rows[start:end]}
	if end < len(rows) && end > start {
		p.NextToken = cursor(rows[end-1]).Encode()
	}
	return p, nil
}

// paginateDesc is [paginate] for rows in descending key order (newest
// first): the cursor is the key of the last item, and the page starts at
// the first row with a smaller key.
func paginateDesc[K interface {
	comparable
	Compare(K) int
	String() string
}, V any](rows []*V, key func(*V) K, req core.PageRequest,
) (core.Page[*V], error) {
	start := 0
	if req.Token != "" {
		u, err := core.ParsePageToken(req.Token)
		if err != nil {
			return core.Page[*V]{}, err
		}
		tok := u.String()
		start = len(rows)
		for i, r := range rows {
			if key(r).String() < tok {
				start = i
				break
			}
		}
	}
	end := min(start+req.Limit(), len(rows))
	p := core.Page[*V]{Items: rows[start:end]}
	if end < len(rows) && end > start {
		p.NextToken = key(rows[end-1]).String()
	}
	return p, nil
}

// paginateRecent cuts rows into the page asked by req in the order of the
// API's lists: by creation instant, newest first, then by ID ascending
// (lago base_query.rb:111-112). key returns the creation instant and ID of
// a row; the page token is a [core.Cursor] over them.
func paginateRecent[V any](rows []*V, key func(*V) (time.Time, uuid.UUID), req core.PageRequest) (core.Page[*V], error) {
	var at time.Time
	if req.Token != "" {
		c, err := core.DecodeCursor(req.Token)
		if err != nil {
			return core.Page[*V]{}, err
		}
		if at, err = time.Parse(time.RFC3339Nano, c.Key); err != nil {
			return core.Page[*V]{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
		}
	}
	slices.SortStableFunc(rows, func(a, b *V) int {
		ta, ia := key(a)
		tb, ib := key(b)
		if c := tb.Compare(ta); c != 0 {
			return c
		}
		return strings.Compare(ia.String(), ib.String())
	})
	cursor := func(x *V) core.Cursor {
		t, id := key(x)
		return core.Cursor{Key: t.UTC().Format(time.RFC3339Nano), ID: id}
	}
	return paginateSorted(rows, cursor, func(x *V, c core.Cursor) bool {
		t, id := key(x)
		if !t.Equal(at) {
			return t.Before(at)
		}
		return id.String() > c.ID.String()
	}, req)
}

// paginateOldest is [paginateRecent] in creation order, oldest first, then
// by ID ascending (lago api_keys_resolver.rb:18).
func paginateOldest[V any](rows []*V, key func(*V) (time.Time, uuid.UUID), req core.PageRequest) (core.Page[*V], error) {
	var at time.Time
	if req.Token != "" {
		c, err := core.DecodeCursor(req.Token)
		if err != nil {
			return core.Page[*V]{}, err
		}
		if at, err = time.Parse(time.RFC3339Nano, c.Key); err != nil {
			return core.Page[*V]{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
		}
	}
	slices.SortStableFunc(rows, func(a, b *V) int {
		ta, ia := key(a)
		tb, ib := key(b)
		if c := ta.Compare(tb); c != 0 {
			return c
		}
		return strings.Compare(ia.String(), ib.String())
	})
	cursor := func(x *V) core.Cursor {
		t, id := key(x)
		return core.Cursor{Key: t.UTC().Format(time.RFC3339Nano), ID: id}
	}
	return paginateSorted(rows, cursor, func(x *V, c core.Cursor) bool {
		t, id := key(x)
		if !t.Equal(at) {
			return t.After(at)
		}
		return id.String() > c.ID.String()
	}, req)
}
