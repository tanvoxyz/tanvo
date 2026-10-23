package core

import (
	"fmt"
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
