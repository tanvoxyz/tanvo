package rpc

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Prefixes of the customer-facing references (the tanvo.common.v1.ref
// rule): a field documented as "an ID or `code:<code>`" takes a UUID or
// one of them. Lago's paths take the code only; the ID is accepted too
// because it survives a code change, and the prefix keeps the two apart.
const (
	// CodePrefix introduces a resource code ("code:api_calls").
	CodePrefix = "code:"
	// ExternalIDPrefix introduces an external ID ("external_id:cus_42").
	ExternalIDPrefix = "external_id:"
)

// Ref is a parsed resource reference: exactly one field is set.
type Ref struct {
	// ID is the Tanvo ID.
	ID uuid.UUID
	// Code is the value after "code:".
	Code string
	// ExternalID is the value after "external_id:".
	ExternalID string
}

// ParseRef parses a reference: "code:<code>", "external_id:<id>" or a
// UUID. An empty value after a prefix, or a value that is neither, fails
// with an InvalidArgument violation on field.
func ParseRef(field, s string) (Ref, error) {
	switch {
	case strings.HasPrefix(s, CodePrefix):
		if v := strings.TrimPrefix(s, CodePrefix); v != "" {
			return Ref{Code: v}, nil
		}
	case strings.HasPrefix(s, ExternalIDPrefix):
		if v := strings.TrimPrefix(s, ExternalIDPrefix); v != "" {
			return Ref{ExternalID: v}, nil
		}
	default:
		if u, err := uuid.Parse(s); err == nil && u != uuid.Nil {
			return Ref{ID: u}, nil
		}
	}
	return Ref{}, InvalidArgument("invalid reference", []FieldViolation{{
		Field: field, Reason: core.CodeInvalid,
		Description: "must be an ID, external_id:<value> or code:<value>",
	}})
}

// ParseID parses the UUID of a field that takes only IDs; an invalid one
// fails with an InvalidArgument violation on field. An empty s is the
// zero ID.
func ParseID[I core.Identifier[I]](field, s string) (I, error) {
	var zero I
	if s == "" {
		return zero, nil
	}
	id, err := core.ParseID[I](s)
	if err != nil {
		return zero, InvalidArgument("invalid ID", []FieldViolation{{Field: field, Reason: core.CodeInvalid, Description: "must be an ID"}})
	}
	return id, nil
}

// Lookup finds a resource of type T by reference. A nil finder means the
// resource has no such reference: using it is InvalidArgument.
type Lookup[I core.Identifier[I], T any] struct {
	// ByID finds the resource by ID.
	ByID func(ctx context.Context, id I) (T, error)
	// ByCode finds the resource by code.
	ByCode func(ctx context.Context, code string) (T, error)
	// ByExternalID finds the resource by external ID.
	ByExternalID func(ctx context.Context, externalID string) (T, error)
}

// Find parses the reference s of field and calls the matching finder.
// Its errors are returned as they are; map them with [Error].
func (l Lookup[I, T]) Find(ctx context.Context, field, s string) (T, error) {
	var zero T
	r, err := ParseRef(field, s)
	if err != nil {
		return zero, err
	}
	unsupported := func(kind string) (T, error) {
		return zero, InvalidArgument("invalid reference", []FieldViolation{{
			Field: field, Reason: core.CodeInvalid, Description: "this resource has no " + kind,
		}})
	}
	switch {
	case r.Code != "":
		if l.ByCode == nil {
			return unsupported("code")
		}
		return l.ByCode(ctx, r.Code)
	case r.ExternalID != "":
		if l.ByExternalID == nil {
			return unsupported("external_id")
		}
		return l.ByExternalID(ctx, r.ExternalID)
	default:
		id, err := core.ParseID[I](r.ID.String())
		if err != nil {
			return zero, err
		}
		return l.ByID(ctx, id)
	}
}
