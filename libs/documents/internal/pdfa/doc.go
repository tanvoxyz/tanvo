// Package pdfa post-processes PDF/A-3 documents produced by Typst. It adds
// the Factur-X/ZUGFeRD XMP metadata to a document carrying an embedded
// factur-x.xml, as an incremental update that replaces the metadata stream
// and leaves every other byte of the document unchanged.
package pdfa
