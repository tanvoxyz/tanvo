// Package cachecodec encodes the values the billing caches keep in a
// [port.Cache], so that a value read back is equal to the one written
// whether the cache lives in the process or on Redis or Valkey.
//
// It is encoding/json/v2 with marshalers for the types JSON alone does
// not round-trip: [money.Currency], whose fields are unexported, and
// [json.RawMessage], which JSON would reformat. Nil slices and maps stay
// nil.
//
// [port.Cache]: https://pkg.go.dev/github.com/tanvoxyz/tanvo/libs/core/port#Cache
// [money.Currency]: https://pkg.go.dev/github.com/tanvoxyz/tanvo/libs/money#Currency
// [json.RawMessage]: https://pkg.go.dev/encoding/json#RawMessage
package cachecodec
