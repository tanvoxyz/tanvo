// Package porttest is the conformance suite of the transactional ports of
// libs/core: every repository of port.Repos, the transaction boundary
// (port.Transactor), locks, sequences and the job queue. It runs against
// any implementation; libs/store runs it against both core/memstore and
// PostgreSQL, so the two cannot drift apart.
//
// The suite only uses libs/core, so it could live in libs/core (as
// core/porttest) beside memstore; it is kept here while libs/store is its
// only second implementation.
//
// Each test gets a fresh, empty transactor from [Harness.New]. Fixtures use
// instants with microsecond precision (the precision of timestamptz) in
// UTC, and results are compared with [Diff], which compares decimals,
// currencies, time zones, instants and JSON documents by value.
package porttest

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Harness adapts an implementation to the suite.
type Harness struct {
	// Name names the implementation ("memstore", "postgres"), for the rare
	// assertion that documents a known difference.
	Name string
	// New returns an empty transactor for one test; it is cleaned up when t
	// ends.
	New func(t *testing.T) port.Transactor
	// Jobs returns the jobs committed through tr and not yet taken, oldest
	// first.
	Jobs func(t *testing.T, tr port.Transactor) []port.Job
	// Concurrent reports whether transactions of the implementation run
	// concurrently (PostgreSQL) rather than one after the other (memstore);
	// the lock contention tests run only then.
	Concurrent bool
}

// group is a named part of the suite.
type group struct {
	name string
	run  func(t *testing.T, h Harness)
}

// groups lists the parts of the suite, one per aggregate.
func groups() []group {
	return []group{
		{"transactor", testTransactor},
		{"organization", testOrganization},
		{"access", testAccess},
		{"customer", testCustomer},
		{"coupon", testCoupon},
		{"dunning", testDunning},
		{"catalog", testCatalog},
		{"catalog_features", testCatalogFeatures},
		{"subscription", testSubscription},
		{"event", testEvent},
		{"alert", testAlert},
		{"invoice", testInvoice},
		{"creditnote", testCreditNote},
		{"wallet", testWallet},
		{"payment", testPayment},
		{"integration", testIntegration},
		{"webhook", testWebhook},
		{"export", testExport},
		{"quote", testQuote},
	}
}

// Run runs the whole suite. Every test runs in parallel with the others.
func Run(t *testing.T, h Harness) {
	for _, g := range groups() {
		t.Run(g.name, func(t *testing.T) {
			t.Parallel()
			g.run(t, h)
		})
	}
}
