package core

import (
	"time"

	"github.com/google/uuid"
)

// Clock is the time source of the domain. Code that needs "now" takes a
// Clock instead of calling time.Now, so tests drive it (testing §1).
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// SystemClock is the [Clock] of the operating system; it returns UTC.
type SystemClock struct{}

// Now implements [Clock].
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FixedClock is a [Clock] that always returns the same instant; for tests.
type FixedClock time.Time

// Now implements [Clock].
func (c FixedClock) Now() time.Time { return time.Time(c).UTC() }

// IDGenerator generates row identifiers (domain §2.1).
type IDGenerator interface {
	// NewUUID returns a fresh UUIDv7.
	NewUUID() uuid.UUID
}

// UUIDv7Generator is the production [IDGenerator].
type UUIDv7Generator struct{}

// NewUUID implements [IDGenerator]. It panics only if the system's random
// source fails, as uuid.Must does.
func (UUIDv7Generator) NewUUID() uuid.UUID { return uuid.Must(uuid.NewV7()) }
