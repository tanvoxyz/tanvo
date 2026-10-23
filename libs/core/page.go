package core

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
