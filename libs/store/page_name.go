package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// nameCursorArgs returns the cursor and row limit of a list by name, then
// by creation instant, newest first, then by ID (see [nameToken]).
func nameCursorArgs(p core.PageRequest) (after *uuid.UUID, afterName *string, afterAt *time.Time, lim int32, err error) {
	lim = int32(p.Limit()) + 1 // Limit is at most core.MaxPageSize
	if p.Token == "" {
		return nil, nil, nil, lim, nil
	}
	c, err := core.DecodeCursor(p.Token)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	at, name, ok := strings.Cut(c.Key, " ")
	if !ok {
		return nil, nil, nil, 0, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	return &c.ID, &name, &t, lim, nil
}

// nameToken returns the page token of a list by name after the item with
// that name, creation instant and ID; its key is the instant (RFC 3339,
// without spaces), a space, then the name.
func nameToken(name string, at time.Time, id uuid.UUID) string {
	return core.Cursor{Key: at.UTC().Format(time.RFC3339Nano) + " " + name, ID: id}.Encode()
}
