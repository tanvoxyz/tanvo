package rpc

import (
	"net/http"
	"strings"
)

// maskParams are the query parameters of the REST API holding a
// google.protobuf.FieldMask: update_mask, by its proto and JSON names.
var maskParams = []string{"update_mask", "updateMask"}

// RESTFieldMasks lets REST clients write the paths of the update_mask
// query parameter with the proto field names (snake_case), as the JSON of
// the API uses them: it rewrites each path to the lowerCamelCase form
// protojson requires of a google.protobuf.FieldMask, which rejects
// underscores. Paths already in lowerCamelCase are left as they are, so
// both forms are accepted, and nested paths are rewritten segment by
// segment ("email_settings.invoice_finalized" becomes
// "emailSettings.invoiceFinalized").
func RESTFieldMasks(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		changed := false
		for _, p := range maskParams {
			vs := q[p]
			for i, v := range vs {
				if c := camelMask(v); c != v {
					vs[i], changed = c, true
				}
			}
		}
		if !changed {
			next.ServeHTTP(w, r)
			return
		}
		r = r.Clone(r.Context())
		r.URL.RawQuery = q.Encode()
		next.ServeHTTP(w, r)
	})
}

// camelMask returns the comma-separated paths of mask with every segment
// in lowerCamelCase.
func camelMask(mask string) string {
	if !strings.Contains(mask, "_") {
		return mask
	}
	paths := strings.Split(mask, ",")
	for i, path := range paths {
		segs := strings.Split(strings.TrimSpace(path), ".")
		for j, s := range segs {
			segs[j] = lowerCamel(s)
		}
		paths[i] = strings.Join(segs, ".")
	}
	return strings.Join(paths, ",")
}

// lowerCamel returns the JSON name protobuf derives from the field name s:
// each underscore dropped and the letter after it upper-cased.
func lowerCamel(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	upper := false
	for _, c := range s {
		switch {
		case c == '_':
			upper = true
		case upper && 'a' <= c && c <= 'z':
			b.WriteRune(c - 'a' + 'A')
			upper = false
		default:
			b.WriteRune(c)
			upper = false
		}
	}
	return b.String()
}
