// Package flight protects the billing caches against stampedes: when
// concurrent callers of one process miss the same key, a [Group] runs the
// computation once and hands its result to all of them.
//
// It wraps golang.org/x/sync/singleflight with the context rules the
// caches need: a caller whose context ends stops waiting, and a caller
// whose computation was cut short by another caller's ended context
// computes again itself.
package flight
