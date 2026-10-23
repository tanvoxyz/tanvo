package core

import (
	"errors"
	"fmt"
)

// Sentinel errors shared by entities and repositories. Implementations wrap
// them (fmt.Errorf("…: %w", ErrNotFound)) and callers test them with
// errors.Is.
var (
	// ErrNotFound is returned when a row does not exist in the organization.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a write violates a uniqueness rule, such
	// as a code or external ID already used by a live row (domain §2.1).
	ErrConflict = errors.New("conflict")
	// ErrInvalidValue is returned for a value outside its domain, such as an
	// undeclared enumeration value or a malformed identifier.
	ErrInvalidValue = errors.New("invalid value")
	// ErrInvalidTransition is returned when a lifecycle transition is not
	// allowed from the current status.
	ErrInvalidTransition = errors.New("invalid transition")
	// ErrNotAllowed is returned when an operation is refused by a business
	// rule other than a field validation (for example deleting the last
	// non-expiring API key).
	ErrNotAllowed = errors.New("not allowed")
)

// InvalidEnum returns an error wrapping [ErrInvalidValue] for value s of the
// enumeration named name.
func InvalidEnum(name, s string) error {
	return fmt.Errorf("%w: %s %q", ErrInvalidValue, name, s)
}

// TransitionError describes a refused lifecycle transition. It matches
// [ErrInvalidTransition] with errors.Is.
type TransitionError struct {
	// Entity names the entity, for example "invoice".
	Entity string
	// From is the current status.
	From string
	// To is the requested status.
	To string
}

// Error implements error.
func (e *TransitionError) Error() string {
	return fmt.Sprintf("%s: cannot transition from %q to %q", e.Entity, e.From, e.To)
}

// Is reports whether target is [ErrInvalidTransition].
func (e *TransitionError) Is(target error) bool { return target == ErrInvalidTransition }

// RuleError is a refused operation with a stable code, such as
// "coupon_is_not_reusable" or "last_admin". It matches [ErrNotAllowed] with
// errors.Is.
type RuleError struct {
	// Code is the stable, machine-readable reason.
	Code string
	// Message is a human-readable explanation.
	Message string
}

// NewRuleError returns a *RuleError with the given code and message.
func NewRuleError(code, message string) *RuleError {
	return &RuleError{Code: code, Message: message}
}

// Error implements error.
func (e *RuleError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Is reports whether target is [ErrNotAllowed].
func (e *RuleError) Is(target error) bool { return target == ErrNotAllowed }
