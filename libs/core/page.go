package core

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// DefaultPageSize is the page size used when a [PageRequest] has none.
const DefaultPageSize = 20

// MaxPageSize bounds [PageRequest.Size].
const MaxPageSize = 1000

// PageRequest asks for one page of a list ordered by creation (UUIDv7
// order, ascending) unless the repository method states otherwise
// (architecture §4.2: cursor pagination).
type PageRequest struct {
	// Size is the maximum number of items; 0 means [DefaultPageSize].
	Size int
	// Token is the opaque cursor returned as [Page.NextToken] by the
	// previous page; "" starts at the beginning.
	Token string
}

// Limit returns the effective page size.
func (r PageRequest) Limit() int {
	switch {
	case r.Size <= 0:
		return DefaultPageSize
	case r.Size > MaxPageSize:
		return MaxPageSize
	default:
		return r.Size
	}
}

// Page is one page of a list.
type Page[T any] struct {
	// Items are the items of the page.
	Items []T
	// NextToken is the cursor of the next page; "" on the last page.
	NextToken string
}

// ListOrder is the order of a filtered list. The zero value is
// [OrderCreated], the order of every list that does not take one.
type ListOrder int

// ListOrder values.
const (
	// OrderCreated lists in creation order, oldest first.
	OrderCreated ListOrder = iota
	// OrderRecent lists the most recent items first: by the sort key the
	// repository method documents (for example the issuing date of an
	// invoice), then newest first. It is the order of the API's lists.
	OrderRecent
)

// Valid reports whether o is a declared ListOrder.
func (o ListOrder) Valid() bool { return o == OrderCreated || o == OrderRecent }

// String returns "created" or "recent".
func (o ListOrder) String() string {
	switch o {
	case OrderCreated:
		return "created"
	case OrderRecent:
		return "recent"
	default:
		return fmt.Sprintf("ListOrder(%d)", int(o))
	}
}

// Cursor is the decoded page token of a list sorted by a sort key, then by
// ID: the position of the last item of the previous page.
type Cursor struct {
	// Key is the sort key of the item in its textual form; "" for a NULL
	// key.
	Key string
	// ID is the item's ID.
	ID uuid.UUID
}

// Encode returns the opaque page token of c.
func (c Cursor) Encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.Key + "\x00" + c.ID.String()))
}

// DecodeCursor decodes a token returned by [Cursor.Encode]; a malformed
// token fails with [ErrInvalidValue].
func DecodeCursor(token string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: page token", ErrInvalidValue)
	}
	key, id, ok := strings.Cut(string(b), "\x00")
	if !ok {
		return Cursor{}, fmt.Errorf("%w: page token", ErrInvalidValue)
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: page token", ErrInvalidValue)
	}
	return Cursor{Key: key, ID: u}, nil
}

// ParsePageToken parses the page token of a list in ID order (the ID of
// the last item of the previous page); a malformed token fails with
// [ErrInvalidValue].
func ParsePageToken(token string) (uuid.UUID, error) {
	u, err := uuid.Parse(token)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: page token", ErrInvalidValue)
	}
	return u, nil
}

// SearchTerm normalizes the free-text search term of a list filter: the
// term is trimmed, and a blank term does not filter.
func SearchTerm(s string) string { return strings.TrimSpace(s) }
