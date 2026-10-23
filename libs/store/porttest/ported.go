package porttest

import (
	"slices"
	"testing"
)

// skippedCase is a spec case of the reference implementation that the
// suite does not port, with the reason (docs/testing.md §2.1: no silent
// drops).
type skippedCase struct {
	// source is the spec file and line of the case.
	source string
	// reason says why the case is not ported.
	reason string
}

// reversed returns a reversed copy of s.
func reversed[T any](s []T) []T {
	out := slices.Clone(s)
	slices.Reverse(out)
	return out
}

// checkSkipped fails the test when a skipped case lacks its source or
// reason.
func checkSkipped(t *testing.T, cases []skippedCase) {
	t.Helper()
	for _, c := range cases {
		if c.source == "" || c.reason == "" {
			t.Errorf("skipped case got = %+v, want = a source and a reason", c)
		}
	}
}
