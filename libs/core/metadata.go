package core

import (
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"
)

// MetadataItem is one key/value pair of a [Metadata] list.
type MetadataItem struct {
	// Key is the metadata key, unique within the list.
	Key string
	// Value is the metadata value.
	Value string
	// DisplayInInvoice prints the pair on invoices (customer metadata only).
	DisplayInInvoice bool
}

// Metadata is an ordered key/value list attached to an entity (domain §2.2).
// An update replaces the whole list (domain §2.3).
type Metadata []MetadataItem

// MetadataLimits bounds a [Metadata] list. A zero limit means unbounded.
type MetadataLimits struct {
	// MaxItems is the maximum number of pairs.
	MaxItems int
	// MaxKeyLength is the maximum key length in characters.
	MaxKeyLength int
	// MaxValueLength is the maximum value length in characters.
	MaxValueLength int
}

// CustomerMetadataLimits returns the limits of customer metadata: ≤ 5
// items, key ≤ 20, value ≤ 100 characters (domain §6).
func CustomerMetadataLimits() MetadataLimits {
	return MetadataLimits{MaxItems: 5, MaxKeyLength: 20, MaxValueLength: 100}
}

// InvoiceMetadataLimits returns the limits of invoice metadata: ≤ 5 items,
// key ≤ 20, value ≤ 140 characters (domain §11.10).
func InvoiceMetadataLimits() MetadataLimits {
	return MetadataLimits{MaxItems: 5, MaxKeyLength: 20, MaxValueLength: 140}
}

// ItemMetadataLimits returns the limits of the metadata of plans, credit
// notes and wallets: ≤ 50 items, key ≤ 100, value ≤ 255 characters (lago
// Metadata::ItemMetadata, app/models/metadata/item_metadata.rb:5-7).
func ItemMetadataLimits() MetadataLimits {
	return MetadataLimits{MaxItems: 50, MaxKeyLength: 100, MaxValueLength: 255}
}

// WalletTransactionMetadataLimits returns the limits of wallet transaction
// metadata: ≤ 15 items (domain §14.2).
func WalletTransactionMetadataLimits() MetadataLimits { return MetadataLimits{MaxItems: 15} }

// Validate checks m against l: item count, key presence, key uniqueness and
// lengths. Errors are reported on "items[i].key" / "items[i].value", and on
// "items" for the count.
func (m Metadata) Validate(l MetadataLimits) error {
	var v Validator
	if l.MaxItems > 0 && len(m) > l.MaxItems {
		v.Add("items", CodeTooMany)
	}
	seen := make(map[string]bool, len(m))
	for i, it := range m {
		key := fmt.Sprintf("items[%d].key", i)
		if v.Required(key, it.Key) {
			if l.MaxKeyLength > 0 {
				v.MaxLen(key, it.Key, l.MaxKeyLength)
			}
			v.Check(!seen[it.Key], key, CodeDuplicated)
			seen[it.Key] = true
		}
		if l.MaxValueLength > 0 && utf8.RuneCountInString(it.Value) > l.MaxValueLength {
			v.Add(fmt.Sprintf("items[%d].value", i), CodeTooLong)
		}
	}
	return v.Err()
}

// Get returns the value of key.
func (m Metadata) Get(key string) (string, bool) {
	i := slices.IndexFunc(m, func(it MetadataItem) bool { return it.Key == key })
	if i < 0 {
		return "", false
	}
	return m[i].Value, true
}

// Clone returns a copy of m that shares no storage with it.
func (m Metadata) Clone() Metadata { return slices.Clone(m) }

// Merge returns m with the pairs of other: a key of m takes the value of
// other in place, and the other keys are appended in their order (lago
// Metadata::UpdateItemService partial update, a Hash#merge).
func (m Metadata) Merge(other Metadata) Metadata {
	out := m.Clone()
	for _, it := range other {
		if i := slices.IndexFunc(out, func(o MetadataItem) bool { return o.Key == it.Key }); i >= 0 {
			out[i] = it
			continue
		}
		out = append(out, it)
	}
	return out
}

// Without returns m without the pair of key.
func (m Metadata) Without(key string) Metadata {
	return slices.DeleteFunc(m.Clone(), func(it MetadataItem) bool { return it.Key == key })
}

// MetadataFilter narrows a list by metadata: every key with a value keeps
// the items whose metadata has that key with exactly that value, and every
// key with an empty value keeps the items whose metadata lacks the key.
type MetadataFilter map[string]string

// Present returns the key/value pairs that must be present, sorted by key.
func (f MetadataFilter) Present() []MetadataItem {
	var out []MetadataItem
	for _, k := range slices.Sorted(maps.Keys(f)) {
		if v := f[k]; v != "" {
			out = append(out, MetadataItem{Key: k, Value: v})
		}
	}
	return out
}

// Absent returns the keys that must be absent, sorted.
func (f MetadataFilter) Absent() []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(f)) {
		if f[k] == "" {
			out = append(out, k)
		}
	}
	return out
}

// Matches reports whether m passes the filter.
func (f MetadataFilter) Matches(m Metadata) bool {
	for k, want := range f {
		v, ok := m.Get(k)
		if want == "" && ok || want != "" && (!ok || v != want) {
			return false
		}
	}
	return true
}
