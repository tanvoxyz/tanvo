package core

// BlobRef references a file kept by the blob store port (logos, documents,
// exports; architecture §10). The empty BlobRef means "no file".
type BlobRef string

// IsZero reports whether r references no file.
func (r BlobRef) IsZero() bool { return r == "" }
