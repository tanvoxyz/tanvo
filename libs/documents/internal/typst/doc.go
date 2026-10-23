// Package typst compiles documents with the Typst CLI, run as a child
// process. The templates and fonts are embedded and written to a private
// directory when the [Engine] starts; each compilation gets its own job
// directory under it holding the document's JSON data and files.
package typst
