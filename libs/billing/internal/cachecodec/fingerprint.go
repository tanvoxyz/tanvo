package cachecodec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"reflect"
)

// Fingerprint returns a short digest of the shape of T: its kind and, for
// structs, every field's name, tag and type, recursively. Caches prefix
// their keys with it, so that server processes of another version, whose
// types differ, sharing one Redis or Valkey neither read nor overwrite
// each other's entries: JSON would decode an entry of another shape
// without an error, leaving the fields it lacks at their zero values.
func Fingerprint[T any]() string {
	h := sha256.New()
	writeType(h, reflect.TypeFor[T](), map[reflect.Type]bool{})
	return hex.EncodeToString(h.Sum(nil)[:6])
}

func writeType(h hash.Hash, t reflect.Type, seen map[reflect.Type]bool) {
	fmt.Fprintf(h, "%s|%s.%s|", t.Kind(), t.PkgPath(), t.Name())
	if seen[t] {
		return
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Array {
			fmt.Fprintf(h, "%d|", t.Len())
		}
		writeType(h, t.Elem(), seen)
	case reflect.Map:
		writeType(h, t.Key(), seen)
		writeType(h, t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			fmt.Fprintf(h, "{%s %q ", f.Name, f.Tag)
			writeType(h, f.Type, seen)
			h.Write([]byte("}"))
		}
	}
}
