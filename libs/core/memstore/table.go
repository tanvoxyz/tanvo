package memstore

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

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
		i, found := slices.BinarySearchFunc(rows, req.Token, func(v *V, tok string) int {
			return cmp.Compare(key(v).String(), tok)
		})
		if found {
			i++
		}
		start = i
	}
	if start > len(rows) {
		return core.Page[*V]{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
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
