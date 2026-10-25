package core

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Stable field error codes. They are part of the API contract: clients
// match on them, so a code is never renamed. Packages define further codes
// for their own rules.
const (
	// CodeRequired: the field is mandatory and missing or blank.
	CodeRequired = "value_is_mandatory"
	// CodeInvalid: the value is not acceptable for the field.
	CodeInvalid = "invalid_value"
	// CodeTooLong: the text exceeds the field's maximum length.
	CodeTooLong = "value_is_too_long"
	// CodeTooShort: the text is shorter than the field's minimum length.
	CodeTooShort = "value_is_too_short"
	// CodeOutOfRange: the number is outside the field's range.
	CodeOutOfRange = "value_is_out_of_range"
	// CodeInvalidFormat: the text does not have the expected format.
	CodeInvalidFormat = "invalid_format"
	// CodeInvalidEmail: the text is not a valid email address (list).
	CodeInvalidEmail = "invalid_email_format"
	// CodeInvalidCountry: the text is not an ISO 3166-1 alpha-2 code.
	CodeInvalidCountry = "country_code_invalid"
	// CodeInvalidTimezone: the text is not an IANA time zone name.
	CodeInvalidTimezone = "invalid_timezone"
	// CodeInvalidCurrency: the currency is unset or unknown.
	CodeInvalidCurrency = "currency_invalid"
	// CodeCurrencyMismatch: the currency differs from the one required.
	CodeCurrencyMismatch = "currencies_does_not_match"
	// CodeAlreadyExists: the value must be unique and is already used.
	CodeAlreadyExists = "value_already_exist"
	// CodeDuplicated: a list contains the same value twice.
	CodeDuplicated = "duplicated_value"
	// CodeTooMany: a list has more items than allowed.
	CodeTooMany = "too_many_items"
	// CodeForbidden: the field must not be set in this configuration.
	CodeForbidden = "value_not_allowed"
	// CodeImmutable: the field can no longer be changed.
	CodeImmutable = "value_is_immutable"
	// CodeNotInFuture: the instant must be in the future.
	CodeNotInFuture = "invalid_date"
	// CodeNegative: the amount must not be negative.
	CodeNegative = "value_is_negative"
	// CodeNotPositive: the amount must be strictly positive.
	CodeNotPositive = "value_must_be_positive"
)

// FieldError is one failed rule on one field. Field is the snake_case path
// of the field as the API names it ("billing_address.country",
// "thresholds[2].value").
type FieldError struct {
	// Field is the path of the offending field.
	Field string
	// Code is the stable reason, one of the Code* constants or a
	// package-specific code.
	Code string
	// Message, when set, explains the failure for people, such as an
	// expression evaluator's report; Code stays the stable reason.
	Message string
}

// String returns "field: code".
func (e FieldError) String() string { return e.Field + ": " + e.Code }

// ValidationError lists every rule an entity breaks. It matches
// [ErrInvalidValue] with errors.Is.
type ValidationError struct {
	// Errors holds the field errors in the order they were found.
	Errors []FieldError
}

// Error implements error.
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.String()
	}
	return "validation failed: " + strings.Join(parts, ", ")
}

// Is reports whether target is [ErrInvalidValue].
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidValue }

// Has reports whether the error holds code for field.
func (e *ValidationError) Has(field, code string) bool {
	for _, fe := range e.Errors {
		if fe.Field == field && fe.Code == code {
			return true
		}
	}
	return false
}

// FieldErrors returns the field errors carried by err, or nil when err is
// not (and does not wrap) a *ValidationError.
func FieldErrors(err error) []FieldError {
	if ve, ok := errors.AsType[*ValidationError](err); ok {
		return ve.Errors
	}
	return nil
}

// Validator accumulates field errors. The zero value is ready to use:
//
//	var v core.Validator
//	v.Required("name", c.Name)
//	v.MaxLen("name", c.Name, 255)
//	return v.Err()
type Validator struct {
	errs []FieldError
}

// Add records code for field.
func (v *Validator) Add(field, code string) {
	v.errs = append(v.errs, FieldError{Field: field, Code: code})
}

// Check records code for field when ok is false, and returns ok.
func (v *Validator) Check(ok bool, field, code string) bool {
	if !ok {
		v.Add(field, code)
	}
	return ok
}

// Merge records the errors of err (a *ValidationError) under prefix, so that
// nested values report "prefix.field". Other non-nil errors are recorded as
// [CodeInvalid] on prefix.
func (v *Validator) Merge(prefix string, err error) {
	if err == nil {
		return
	}
	fes := FieldErrors(err)
	if fes == nil {
		v.Add(prefix, CodeInvalid)
		return
	}
	for _, fe := range fes {
		f := fe.Field
		if prefix != "" {
			f = prefix + "." + f
		}
		v.Add(f, fe.Code)
	}
}

// Required records [CodeRequired] when s is blank.
func (v *Validator) Required(field, s string) bool {
	return v.Check(strings.TrimSpace(s) != "", field, CodeRequired)
}

// MaxLen records [CodeTooLong] when s has more than n characters.
func (v *Validator) MaxLen(field, s string, n int) bool {
	return v.Check(utf8.RuneCountInString(s) <= n, field, CodeTooLong)
}

// String checks a required text of at most n characters (domain §2.2
// `string(n)`).
func (v *Validator) String(field, s string, n int) bool {
	return v.Required(field, s) && v.MaxLen(field, s, n)
}

// OptionalString checks an optional text of at most n characters.
func (v *Validator) OptionalString(field string, s *string, n int) bool {
	if s == nil {
		return true
	}
	return v.MaxLen(field, *s, n)
}

// Code checks a resource code: required, 1–n characters (domain §2.1).
func (v *Validator) Code(field, s string, n int) bool { return v.String(field, s, n) }

// IntRange records [CodeOutOfRange] when n is outside [lo, hi].
func (v *Validator) IntRange(field string, n, lo, hi int) bool {
	return v.Check(n >= lo && n <= hi, field, CodeOutOfRange)
}

// NonNegative records [CodeNegative] when n < 0.
func (v *Validator) NonNegative(field string, n int64) bool {
	return v.Check(n >= 0, field, CodeNegative)
}

// Positive records [CodeNotPositive] when n ≤ 0.
func (v *Validator) Positive(field string, n int64) bool {
	return v.Check(n > 0, field, CodeNotPositive)
}

// DecimalNonNegative records [CodeNegative] when d < 0.
func (v *Validator) DecimalNonNegative(field string, d money.Decimal) bool {
	return v.Check(!d.IsNegative(), field, CodeNegative)
}

// DecimalPositive records [CodeNotPositive] when d ≤ 0.
func (v *Validator) DecimalPositive(field string, d money.Decimal) bool {
	return v.Check(d.IsPositive(), field, CodeNotPositive)
}

// Enum records [CodeInvalid] when e is not a declared value, or
// [CodeRequired] when it is empty.
func (v *Validator) Enum(field string, e interface {
	Valid() bool
	String() string
},
) bool {
	if e.String() == "" {
		v.Add(field, CodeRequired)
		return false
	}
	return v.Check(e.Valid(), field, CodeInvalid)
}

// OptionalEnum is [Validator.Enum] that accepts the empty (unset) value.
func (v *Validator) OptionalEnum(field string, e interface {
	Valid() bool
	String() string
},
) bool {
	if e.String() == "" {
		return true
	}
	return v.Check(e.Valid(), field, CodeInvalid)
}

// Currency records [CodeInvalidCurrency] when c is unset.
func (v *Validator) Currency(field string, c money.Currency) bool {
	return v.Check(!c.IsZero(), field, CodeInvalidCurrency)
}

// Email records [CodeInvalidEmail] unless s is a single valid address.
func (v *Validator) Email(field, s string) bool {
	return v.Check(ValidEmail(s), field, CodeInvalidEmail)
}

// EmailList records [CodeInvalidEmail] unless s is a comma-separated list
// of valid addresses (domain §6 `email`).
func (v *Validator) EmailList(field, s string) bool {
	return v.Check(ValidEmailList(s), field, CodeInvalidEmail)
}

// Future records [CodeNotInFuture] unless t is after now.
func (v *Validator) Future(field string, t, now time.Time) bool {
	return v.Check(t.After(now), field, CodeNotInFuture)
}

// Err returns the accumulated errors as a *ValidationError, or nil.
func (v *Validator) Err() error {
	if len(v.errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: v.errs}
}

// Valid reports whether no error was recorded.
func (v *Validator) Valid() bool { return len(v.errs) == 0 }

// ValidEmail reports whether s is one bare email address (no display name).
func ValidEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == strings.TrimSpace(s) && strings.Contains(a.Address, "@")
}

// ValidEmailList reports whether s is a comma-separated list of email
// addresses; whitespace around each address is ignored.
func ValidEmailList(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for part := range strings.SplitSeq(s, ",") {
		if !ValidEmail(strings.TrimSpace(part)) {
			return false
		}
	}
	return true
}

// LoadZone resolves an IANA time zone name, failing with [ErrInvalidValue].
// "" and "UTC" give time.UTC.
func LoadZone(name string) (*time.Location, error) {
	if name == "" || name == "UTC" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return nil, fmt.Errorf("%w: time zone %q", ErrInvalidValue, name)
	}
	return loc, nil
}
