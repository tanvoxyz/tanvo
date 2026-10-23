package expression

import (
	"errors"
	"fmt"
	"strings"
)

// SyntaxErrorKind tells why an expression does not parse.
type SyntaxErrorKind uint8

// The kinds of syntax error.
const (
	// SyntaxInvalid is an input the grammar does not accept.
	SyntaxInvalid SyntaxErrorKind = iota
	// SyntaxArguments is a call of round, ceil or floor with more than two
	// arguments.
	SyntaxArguments
	// SyntaxDecimal is a number literal that is not a decimal.
	SyntaxDecimal
	// SyntaxNesting is an expression that nests parentheses deeper than
	// [MaxDepth]; its position is the first parenthesis beyond it.
	SyntaxNesting
	// SyntaxLength is an expression longer than [MaxSourceLen] bytes.
	SyntaxLength
)

// Limits of the language. Expressions are user input, so parsing and
// evaluation are bounded rather than left to run until the module traps.
const (
	// MaxSourceLen is the longest expression, in bytes.
	MaxSourceLen = 4096
	// MaxDepth is the deepest nesting of parentheses, those of function
	// calls included.
	MaxDepth = 128
	// MaxDigits is the most significant digits of a number, from a literal,
	// an event or an operation.
	MaxDigits = 2000
	// MaxScale bounds a number's scale either way: at most MaxScale digits
	// after the decimal point, and at most MaxScale trailing zeros before it.
	MaxScale = 1000
	// MaxRoundDigits bounds the digits that round, ceil and floor round to,
	// either way.
	MaxRoundDigits = 1000
	// MaxNumberLen is the longest text, in bytes, that is read as a number:
	// a longer number property makes its event invalid, and a longer string
	// property is a string.
	MaxNumberLen = 4096
)

// String returns the kind's name.
func (k SyntaxErrorKind) String() string {
	switch k {
	case SyntaxInvalid:
		return "syntax"
	case SyntaxArguments:
		return "arguments"
	case SyntaxDecimal:
		return "decimal"
	case SyntaxNesting:
		return "nesting"
	case SyntaxLength:
		return "length"
	default:
		return fmt.Sprintf("SyntaxErrorKind(%d)", uint8(k))
	}
}

// SyntaxError is an expression that does not parse.
type SyntaxError struct {
	// Kind tells why.
	Kind SyntaxErrorKind
	// Line and Column are the 1-based position where the input stops
	// matching the grammar (Column counts characters), and Offset its byte
	// offset; for SyntaxNesting, the position of the first parenthesis
	// beyond [MaxDepth]. All three are 0 for the other kinds.
	Line, Column, Offset int
	// Message is lago-expression's report: for SyntaxInvalid, the
	// position, the input's line with a caret, and what was expected.
	Message string
}

// Error returns the position and what was expected on one line.
func (e *SyntaxError) Error() string {
	switch e.Kind {
	case SyntaxInvalid:
	case SyntaxNesting:
		return fmt.Sprintf("expression: syntax error at line %d, column %d: %s", e.Line, e.Column, e.Message)
	default:
		return "expression: " + e.Message
	}
	detail := "invalid syntax"
	for line := range strings.Lines(e.Message) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "= "); ok {
			detail = rest
		}
	}
	return fmt.Sprintf("expression: syntax error at line %d, column %d: %s", e.Line, e.Column, detail)
}

// ErrorKind tells why an event could not be evaluated.
type ErrorKind uint8

// The kinds of evaluation error, in the module's encoding.
const (
	// ErrorEmptyArgumentList is least or greatest without arguments.
	ErrorEmptyArgumentList ErrorKind = iota
	// ErrorExpectedDecimal is a string where a number is needed.
	ErrorExpectedDecimal
	// ErrorMissingVariable is a property the event does not have.
	ErrorMissingVariable
	// ErrorDivisionByZero is a division by zero.
	ErrorDivisionByZero
	// ErrorInvalidEvent is an event whose number property is not a
	// decimal, such as a float that is not finite.
	ErrorInvalidEvent
	// ErrorDigitsOutOfRange is a rounding to more than [MaxRoundDigits]
	// digits either way.
	ErrorDigitsOutOfRange
	// ErrorNumberOutOfRange is a number beyond [MaxDigits] or [MaxScale].
	ErrorNumberOutOfRange
	// ErrorTrap is a failure of the module itself, such as running out of
	// memory. The language's limits keep valid input from causing one.
	ErrorTrap
)

// Sentinel errors that an [*EvaluationError] matches with [errors.Is].
var (
	ErrEmptyArgumentList = errors.New("expression: empty argument list")
	ErrExpectedDecimal   = errors.New("expression: expected a decimal")
	ErrMissingVariable   = errors.New("expression: missing variable")
	ErrDivisionByZero    = errors.New("expression: division by zero")
	ErrInvalidEvent      = errors.New("expression: invalid event")
	ErrDigitsOutOfRange  = errors.New("expression: rounding digits out of range")
	ErrNumberOutOfRange  = errors.New("expression: number out of range")
	ErrTrap              = errors.New("expression: module trap")
)

// String returns the kind's name.
func (k ErrorKind) String() string {
	switch k {
	case ErrorEmptyArgumentList:
		return "empty_argument_list"
	case ErrorExpectedDecimal:
		return "expected_decimal"
	case ErrorMissingVariable:
		return "missing_variable"
	case ErrorDivisionByZero:
		return "division_by_zero"
	case ErrorInvalidEvent:
		return "invalid_event"
	case ErrorDigitsOutOfRange:
		return "digits_out_of_range"
	case ErrorNumberOutOfRange:
		return "number_out_of_range"
	case ErrorTrap:
		return "trap"
	default:
		return fmt.Sprintf("ErrorKind(%d)", uint8(k))
	}
}

func (k ErrorKind) sentinel() error {
	switch k {
	case ErrorEmptyArgumentList:
		return ErrEmptyArgumentList
	case ErrorExpectedDecimal:
		return ErrExpectedDecimal
	case ErrorMissingVariable:
		return ErrMissingVariable
	case ErrorDivisionByZero:
		return ErrDivisionByZero
	case ErrorInvalidEvent:
		return ErrInvalidEvent
	case ErrorDigitsOutOfRange:
		return ErrDigitsOutOfRange
	case ErrorNumberOutOfRange:
		return ErrNumberOutOfRange
	default:
		return ErrTrap
	}
}

// EvaluationError is an event an expression could not be evaluated for.
type EvaluationError struct {
	// Kind tells why.
	Kind ErrorKind
	// Message is lago-expression's message, such as "Variable: b not
	// found", which lago-api reports as "expression_evaluation_failed:
	// Variable: b not found".
	Message string
}

// Error returns the message.
func (e *EvaluationError) Error() string { return "expression: " + e.Message }

// Unwrap returns the sentinel error of the kind, such as [ErrMissingVariable].
func (e *EvaluationError) Unwrap() error { return e.Kind.sentinel() }
