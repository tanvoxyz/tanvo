package memstore

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// nameKey is the sort key of a list by name: the name (byte order), then
// the creation instant, newest first, then the ID ascending.
type nameKey struct {
	name string
	at   time.Time
	id   uuid.UUID
}

// compareNameKeys orders a before b in the order of [nameKey].
func compareNameKeys(a, b nameKey) int {
	if c := strings.Compare(a.name, b.name); c != 0 {
		return c
	}
	if c := b.at.Compare(a.at); c != 0 {
		return c
	}
	return strings.Compare(a.id.String(), b.id.String())
}

// nameCursor returns the page cursor of k; its key is the creation
// instant (RFC 3339, without spaces), a space, then the name, as the store
// encodes it.
func nameCursor(k nameKey) core.Cursor {
	return core.Cursor{Key: k.at.UTC().Format(time.RFC3339Nano) + " " + k.name, ID: k.id}
}

// parseNameCursor decodes the key of a [nameCursor].
func parseNameCursor(c core.Cursor) (nameKey, error) {
	at, name, ok := strings.Cut(c.Key, " ")
	if !ok {
		return nameKey{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nameKey{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	return nameKey{name: name, at: t, id: c.ID}, nil
}

// paginateByName cuts rows into the page asked by req, listed in the
// order of [nameKey].
func paginateByName[V any](rows []*V, key func(*V) nameKey, req core.PageRequest) (core.Page[*V], error) {
	var after nameKey
	if req.Token != "" {
		c, err := core.DecodeCursor(req.Token)
		if err != nil {
			return core.Page[*V]{}, err
		}
		if after, err = parseNameCursor(c); err != nil {
			return core.Page[*V]{}, err
		}
	}
	slices.SortStableFunc(rows, func(a, b *V) int { return compareNameKeys(key(a), key(b)) })
	return paginateSorted(rows, func(x *V) core.Cursor { return nameCursor(key(x)) }, func(x *V, _ core.Cursor) bool {
		return compareNameKeys(key(x), after) > 0
	}, req)
}

// matchesNameOrCode reports whether the trimmed search term is blank or
// within name or code, case-insensitively.
func matchesNameOrCode(search, name, code string) bool {
	term := strings.ToLower(core.SearchTerm(search))
	return term == "" || strings.Contains(strings.ToLower(name), term) || strings.Contains(strings.ToLower(code), term)
}
