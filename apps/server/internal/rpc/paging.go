package rpc

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Page returns the page request of a list RPC's page_size and page_token
// (AIP-158): 0 is [core.DefaultPageSize], larger sizes are capped at
// [core.MaxPageSize]. The repositories reject a token they did not issue
// with core.ErrInvalidValue, which [Error] maps to InvalidArgument.
func Page(size int32, token string) core.PageRequest {
	return core.PageRequest{Size: int(size), Token: token}
}

// Items converts the items of a page with fn, for the repeated field of a
// list response; the page's NextToken is the next_page_token.
func Items[T, P any](p core.Page[T], fn func(T) P) []P {
	out := make([]P, len(p.Items))
	for i, it := range p.Items {
		out[i] = fn(it)
	}
	return out
}
