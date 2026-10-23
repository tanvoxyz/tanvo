package quote

import (
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Number prefixes.
const (
	// QuotePrefix starts quote numbers.
	QuotePrefix = "QT"
	// OrderFormPrefix starts order form numbers.
	OrderFormPrefix = "OF"
	// OrderPrefix starts order numbers.
	OrderPrefix = "OR"
)

// Rule codes of the quote flow (lago's names).
const (
	// CodeActiveVersionExists: the quote already has a draft or approved
	// version.
	CodeActiveVersionExists = "active_version_exists"
	// CodeNotApprovable: only a draft version can be approved.
	CodeNotApprovable = "not_approvable"
	// CodeNotVoidable: the version or order form cannot be voided.
	CodeNotVoidable = "not_voidable"
	// CodeNotClonable: a new version cannot be made while one is approved.
	CodeNotClonable = "not_clonable"
	// CodeNotSignable: only a generated order form can be signed.
	CodeNotSignable = "not_signable"
	// CodeNotExecutable: only a created order can be executed.
	CodeNotExecutable = "not_executable"
)

// FormatNumber returns "<prefix>-<year>-<seq>" with the sequence padded
// to at least 4 digits, for example QT-2026-0007.
func FormatNumber(prefix string, year, seq int) string {
	return fmt.Sprintf("%s-%04d-%04d", prefix, year, seq)
}

// Quote is a commercial proposal to a customer (domain §21.1).
type Quote struct {
	// ID identifies the quote.
	ID core.QuoteID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Number is unique per organization, see [FormatNumber].
	Number string
	// SequentialID is the sequence of the number, per organization and
	// year.
	SequentialID int
	// CustomerID is the customer the quote is for.
	CustomerID core.CustomerID
	// OrderType is what executing the order does.
	OrderType OrderType
	// Owners are the members responsible for the quote.
	Owners []core.MembershipID
	// SubscriptionID is the amended subscription (amendments only).
	SubscriptionID core.SubscriptionID
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// AssignNumber sets SequentialID and, unless a number is already set,
// Number from the creation year (lago ensure_number).
func (q *Quote) AssignNumber(seq int) {
	q.SequentialID = seq
	if q.Number == "" {
		q.Number = FormatNumber(QuotePrefix, q.CreatedAt.UTC().Year(), seq)
	}
}

// Validate checks the invariants of domain §21.1.
func (q *Quote) Validate() error {
	var v core.Validator
	v.Required("number", q.Number)
	v.Check(!q.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	if v.Enum("order_type", q.OrderType) && q.OrderType == OrderTypeSubscriptionAmendment {
		v.Check(!q.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	}
	for _, o := range q.Owners {
		if !v.Check(!o.IsZero(), "owners", core.CodeInvalid) {
			break
		}
	}
	v.Check(len(q.Owners) == len(dedup(q.Owners)), "owners", core.CodeDuplicated)
	return v.Err()
}

func dedup[T comparable](s []T) []T {
	out := make([]T, 0, len(s))
	for _, x := range s {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}
