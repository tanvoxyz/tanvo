// Package export holds asynchronous CSV exports of document lists (domain
// §20). A [DataExport] is built in [Part]s (batches of rows) in the
// exports queue; the parts are concatenated into one file, which is
// emailed to the requester and expires 7 days after completion.
package export
