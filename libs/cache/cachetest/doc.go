// Package cachetest is the conformance suite of [port.Cache]
// implementations: [Run] checks reads, writes, expiry, deletion, copying
// of values, binary and large values, and concurrent use.
//
// The backends of libs/cache run it in their tests, and any other
// implementation of the port should too.
package cachetest
