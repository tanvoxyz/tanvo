package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Conversions between the column types sqlc generates and core types.

// idScanner is the pointer type of a core.ID[K] (which implements
// sql.Scanner); core does not export a constructor generic over the ID
// type, so ids are built by scanning their UUID bytes.
type idScanner[T any] interface {
	*T
	Scan(src any) error
}

// idOf converts a UUID column to the core ID type T (uuid.Nil to zero).
func idOf[T any, PT idScanner[T]](u uuid.UUID) T {
	var v T
	if u == uuid.Nil {
		return v
	}
	// Scanning 16 bytes cannot fail.
	_ = PT(&v).Scan(u[:])
	return v
}

// optIDOf converts a nullable UUID column to T (NULL to zero).
func optIDOf[T any, PT idScanner[T]](u *uuid.UUID) T {
	if u == nil {
		return *new(T)
	}
	return idOf[T, PT](*u)
}

// idsOf converts a UUID array column to a slice of T.
func idsOf[T any, PT idScanner[T]](us []uuid.UUID) []T {
	if len(us) == 0 {
		return nil
	}
	out := make([]T, len(us))
	for i, u := range us {
		out[i] = idOf[T, PT](u)
	}
	return out
}

// uuider is any core ID.
type uuider interface {
	UUID() uuid.UUID
	IsZero() bool
}

// optUUID returns the UUID of id, or nil when id is zero (a NULL column).
func optUUID[I uuider](id I) *uuid.UUID {
	if id.IsZero() {
		return nil
	}
	u := id.UUID()
	return &u
}

// uuids returns the UUIDs of ids (never nil, for array parameters).
func uuids[I uuider](ids []I) []uuid.UUID {
	out := make([]uuid.UUID, len(ids))
	for i, id := range ids {
		out[i] = id.UUID()
	}
	return out
}

// utc returns t in UTC.
func utc(t time.Time) time.Time { return t.UTC() }

// optUTC returns *t in UTC, or nil.
func optUTC(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// locationName returns the IANA name of loc, or nil when loc is nil.
func locationName(loc *time.Location) *string {
	if loc == nil {
		return nil
	}
	s := loc.String()
	return &s
}

// location loads the IANA time zone name (nil or "" to nil).
func location(name *string) (*time.Location, error) {
	if name == nil || *name == "" {
		return nil, nil
	}
	loc, err := time.LoadLocation(*name)
	if err != nil {
		return nil, fmt.Errorf("time zone %q: %w", *name, err)
	}
	return loc, nil
}

// currencyCode returns the code of c, or nil when c is zero.
func currencyCode(c money.Currency) *string {
	if c.IsZero() {
		return nil
	}
	s := c.Code()
	return &s
}

// currency looks up an ISO 4217 code (nil or "" to the zero currency).
func currency(code *string) (money.Currency, error) {
	if code == nil || *code == "" {
		return money.Currency{}, nil
	}
	c, err := money.LookupCurrency(*code)
	if err != nil {
		return money.Currency{}, fmt.Errorf("currency: %w", err)
	}
	return c, nil
}

// mustCurrency is currency for a NOT NULL column.
func mustCurrency(code string) (money.Currency, error) { return currency(&code) }

// enumStrings converts a slice of string enums to []string (never nil).
func enumStrings[E ~string](es []E) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = string(e)
	}
	return out
}

// enumsOf converts a text array column to a slice of enum values.
func enumsOf[E ~string](ss []string) []E {
	if len(ss) == 0 {
		return nil
	}
	out := make([]E, len(ss))
	for i, s := range ss {
		out[i] = E(s)
	}
	return out
}

// metadataJSON is the stored form of one core.MetadataItem.
type metadataJSON struct {
	Key              string `json:"key"`
	Value            string `json:"value"`
	DisplayInInvoice bool   `json:"display_in_invoice,omitempty"`
}

// encodeMetadata encodes m as the jsonb array of a metadata column.
func encodeMetadata(m core.Metadata) []byte {
	items := make([]metadataJSON, len(m))
	for i, it := range m {
		items[i] = metadataJSON(it)
	}
	b, _ := json.Marshal(items) // strings and bools always encode
	return b
}

// decodeMetadata decodes a metadata column (an empty array to nil).
func decodeMetadata(b []byte) (core.Metadata, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var items []metadataJSON
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	if len(items) == 0 {
		return nil, nil
	}
	m := make(core.Metadata, len(items))
	for i, it := range items {
		m[i] = core.MetadataItem(it)
	}
	return m, nil
}

// encodeJSON encodes v for a jsonb column of a store-defined value object.
func encodeJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return b, nil
}

// decodeJSON decodes a jsonb column into v; an empty column leaves v.
func decodeJSON(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}

// rawJSON returns b, or nil for an empty or null document (client-supplied
// documents are stored verbatim in json columns).
func rawJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}

// likePattern returns the ILIKE pattern matching s anywhere, with the LIKE
// wildcards of s escaped (the default escape character is \).
func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// optString returns nil for "" and &s otherwise.
func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// deref returns *p, or the zero value when p is nil.
func deref[T any](p *T) T {
	if p == nil {
		return *new(T)
	}
	return *p
}

// optInt32 converts an optional int for an integer column.
func optInt32(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p) // domain values are small and validated
	return &v
}

// optInt converts a nullable integer column.
func optInt(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}
