// Package blob implements the [port.BlobStore] port: [Local] keeps files on
// the local disk and serves them through signed URLs, [S3] keeps them in an
// S3-compatible object store (AWS S3, MinIO, Cloudflare R2, …) with a
// hand-written AWS Signature Version 4.
//
// # References
//
// Put returns a [core.BlobRef] of the form
//
//	<32 lowercase hex characters>/<sanitized file name>
//
// The hex part is 128 random bits from crypto/rand, which makes references
// unguessable; the file name keeps them readable. References are opaque to
// callers and are validated on every call, so a reference can never name a
// path outside its object.
//
// # Tenancy
//
// The organization is not part of the reference: every object is stored
// under the organization's prefix (<org>/<ref>), and the organization passed
// to Get, Delete and URL selects that prefix. A reference used with another
// organization therefore names a different, missing object and never
// resolves.
//
// [port.BlobStore]: https://pkg.go.dev/github.com/tanvoxyz/tanvo/libs/core/port#BlobStore
package blob
