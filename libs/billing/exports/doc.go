// Package exports builds the data exports (domain §20, parity §1.43): CSV
// files of the invoices, invoice fees, credit notes and credit note items
// matching a list filter, requested by a member and built in the
// background.
//
// An export is built in three jobs, as in lago DataExports: the export job
// lists the matching IDs and splits them in parts of [BatchSize]; each
// part job renders its rows; the last part to complete enqueues the
// combine job, which writes the header and the parts in index order to the
// blob store and completes the export.
package exports
