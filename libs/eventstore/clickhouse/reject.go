package clickhouse

import (
	"errors"
	"fmt"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/eventstore"
)

// rejectedCode reports whether a server exception is one that a value of
// the inserted rows causes: the same rows fail again, other rows may not.
// Codes that may also come from the schema, the statement, the settings
// or the server's state (TYPE_MISMATCH, BAD_ARGUMENTS, UNKNOWN_TABLE,
// MEMORY_LIMIT_EXCEEDED, TOO_MANY_PARTS, TIMEOUT_EXCEEDED, …) are not
// rejections: they fail every batch alike, or pass once the server
// recovers.
func rejectedCode(code int32) bool {
	switch code {
	case 6, // CANNOT_PARSE_TEXT
		26,  // CANNOT_PARSE_QUOTED_STRING
		27,  // CANNOT_PARSE_INPUT_ASSERTION_FAILED
		38,  // CANNOT_PARSE_DATE
		41,  // CANNOT_PARSE_DATETIME
		69,  // ARGUMENT_OUT_OF_BOUND
		70,  // CANNOT_CONVERT_TYPE
		72,  // CANNOT_PARSE_NUMBER
		117, // INCORRECT_DATA
		128, // TOO_LARGE_ARRAY_SIZE
		131, // TOO_LARGE_STRING_SIZE
		321, // VALUE_IS_OUT_OF_RANGE_OF_DATA_TYPE
		349, // CANNOT_INSERT_NULL_IN_ORDINARY_COLUMN
		407, // DECIMAL_OVERFLOW
		469: // VIOLATED_CONSTRAINT
		return true
	}
	return false
}

// rejected reports whether err, from the insert of events, means that the
// store rejects a value of the events rather than fails: a value the
// driver cannot convert to its column (a decimal that overflows), or a
// server exception caused by a value. A wrong number of values or a
// connection, timeout or server failure is not a rejection.
func rejected(err error) bool {
	if errors.Is(err, eventstore.ErrMissingOrganization) {
		return true
	}
	var be *proto.BlockError
	if errors.As(err, &be) {
		return be.Op == "AppendRow"
	}
	var ex *ch.Exception
	if errors.As(err, &ex) {
		return rejectedCode(ex.Code)
	}
	return false
}

// rejection marks err with [core.ErrInvalidValue] when the store rejects
// the events ([rejected]), so that a writer can tell the events it will
// never store from a failure; err otherwise.
func rejection(err error) error {
	if err == nil || !rejected(err) {
		return err
	}
	return fmt.Errorf("%w: %w", core.ErrInvalidValue, err)
}
