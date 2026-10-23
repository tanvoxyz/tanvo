package store

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Pagination is keyset pagination on the id column (UUIDv7, so creation
// order): the token is the id of the last item of the previous page. A list
// query takes the parameters returned by [pageArgs] and reads one row more
// than the page size, which tells whether a next page exists:
//
//	-- name: ListThings :many
//	SELECT * FROM things
//	WHERE organization_id = @organization_id
//	  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
//	ORDER BY id
//	LIMIT @lim;

// pageArgs returns the cursor (nil for the first page) and the row limit
// (page size + 1) of p.
func pageArgs(p core.PageRequest) (*uuid.UUID, int32, error) {
	lim := int32(p.Limit()) + 1 // Limit is at most core.MaxPageSize
	if p.Token == "" {
		return nil, lim, nil
	}
	u, err := uuid.Parse(p.Token)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	return &u, lim, nil
}

// pageOf cuts rows (read with the limit of [pageArgs]) into the page of p,
// converting each row with conv.
func pageOf[R, T any](rows []R, p core.PageRequest, conv func(R) (T, error), key func(T) string) (core.Page[T], error) {
	n := p.Limit()
	more := len(rows) > n
	if more {
		rows = rows[:n]
	}
	items := make([]T, 0, len(rows))
	for _, r := range rows {
		it, err := conv(r)
		if err != nil {
			return core.Page[T]{}, err
		}
		items = append(items, it)
	}
	out := core.Page[T]{Items: items}
	if more && len(items) > 0 {
		out.NextToken = key(items[len(items)-1])
	}
	return out, nil
}

// convAll converts every row with conv.
func convAll[R, T any](rows []R, conv func(R) (T, error)) ([]T, error) {
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		it, err := conv(r)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}
