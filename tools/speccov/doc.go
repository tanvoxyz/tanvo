// Command speccov checks that every upstream spec case a module owns is
// either ported or listed as skipped, and renders the module's SKIPPED.md.
//
// A module declares what it owns in testdata/specs.json:
//
//	{
//	  "title": "the NetSuite integration",
//	  "files": ["services/integrations/aggregator/contacts/payloads/netsuite_spec.rb"],
//	  "reasons": {"nango": "Asserts the broker's request shape."},
//	  "notes": ["Markdown paragraphs printed before the cases."],
//	  "skipped": {"services/integrations/aggregator/contacts/payloads/netsuite_spec.rb:42": "nango"}
//	}
//
// Paths are relative to the upstream spec directory. A case (an it,
// specify or it_behaves_like line) is ported when a test or testdata file
// anywhere in the repository cites its line, or the line of an enclosing
// context or describe block, as "spec/<path>:<line>". Every other case
// must be skipped with one of the module's reasons. A skipped key is a
// case's line, the line of a context or describe block (covering every
// case inside it), or a bare file path (covering the whole file); the
// innermost key wins, and a cited case counts as ported even inside a
// skipped block or file.
//
// Usage, from the repository root:
//
//	go run ./tools/speccov -lago ~/Code/frameworks/lago/api [-write] [module ...]
//
// Without modules, every module with a testdata/specs.json is checked.
// -write rewrites each module's SKIPPED.md. The exit status is 1 when a
// case is neither ported nor skipped, a case skipped by its own line is also
// ported, or a skipped key matches no case, block or owned file.
package main
