package rpc

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// CatalogRef parses the reference of a catalog object (an ID or
// `code:<code>`) for the billing services; an external_id reference, or
// an invalid one, fails with an InvalidArgument violation on field. An
// empty s is the zero reference.
func CatalogRef(field, s string) (refs.Ref, error) {
	if s == "" {
		return refs.Ref{}, nil
	}
	r, err := ParseRef(field, s)
	if err != nil {
		return refs.Ref{}, err
	}
	if r.ExternalID != "" {
		return refs.Ref{}, InvalidArgument("invalid reference", []FieldViolation{{
			Field: field, Reason: core.CodeInvalid, Description: "this resource has no external_id",
		}})
	}
	return refs.Ref{ID: r.ID, Code: r.Code}, nil
}

// CatalogRefs parses the references of a repeated field (tax_ids, …);
// the violations name field[i]. A nil in is nil.
func CatalogRefs(field string, in []string) ([]refs.Ref, error) {
	if in == nil {
		return nil, nil
	}
	out := make([]refs.Ref, len(in))
	for i, s := range in {
		r, err := CatalogRef(fmt.Sprintf("%s[%d]", field, i), s)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}
