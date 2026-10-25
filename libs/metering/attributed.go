package metering

import (
	"context"
	"errors"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Attributed usage (parity §1.42): a subscription's usage over a window,
// grouped by the value one attribution label gives each event, over every
// count and sum charge at once.

// AttributedOrder is what attributed values are ranked by, descending.
type AttributedOrder string

// The orders of attributed values.
const (
	// AttributedOrderAmount ranks by amount.
	AttributedOrderAmount AttributedOrder = "amount"
	// AttributedOrderEvents ranks by number of events.
	AttributedOrderEvents AttributedOrder = "events_count"
)

// PriceLookupSeparator joins the values of the keys of a [PriceKeySet]
// entry.
const PriceLookupSeparator = "\x1f"

// PriceLookup resolves which filter of a charge prices an event. Filters
// are ranked from 1 (the matching filter with the most keys, the oldest on
// a tie); each key set maps the joined values of its keys to the best rank
// holding them, and an event takes the lowest rank found across the key
// sets, or the default rank len(FilterIDs)+1, the charge's own price.
type PriceLookup struct {
	// KeySets are the sets of filter keys, the largest first.
	KeySets []PriceKeySet
	// FilterIDs are the filters by rank (index rank-1).
	FilterIDs []string
	// UnitAmounts are the unit amounts of the filters by rank, in minor
	// units of the currency.
	UnitAmounts []money.Decimal
}

// DefaultRank is the rank of an event no filter matches.
func (l *PriceLookup) DefaultRank() int { return len(l.FilterIDs) + 1 }

// PriceKeySet is the filters of a [PriceLookup] holding one set of keys.
type PriceKeySet struct {
	// Keys are the property keys, sorted.
	Keys []string
	// Entries maps the event's values of Keys, joined by
	// [PriceLookupSeparator], to a rank.
	Entries map[string]int
}

// AttributedCharge is a charge an attributed usage query reads.
type AttributedCharge struct {
	// ChargeID is the charge.
	ChargeID string
	// Code is the code of the charge's billable metric.
	Code string
	// Count counts events as units (a count metric); otherwise the units
	// are the events' values (a sum metric).
	Count bool
	// UnitAmount is the charge's own unit amount, in minor units; nil
	// leaves the charge unpriced.
	UnitAmount *money.Decimal
	// Lookup resolves the filter of each event; nil when the charge has no
	// filter, or it is neither priced nor split.
	Lookup *PriceLookup
	// Split gives each filter of Lookup a cell of its own, plus one for the
	// events no filter matches.
	Split bool
}

// AttributedQuery is a query of attributed usage.
type AttributedQuery struct {
	// OrganizationID is the organization.
	OrganizationID string
	// SubscriptionID is the external subscription ID.
	SubscriptionID string
	// From and To are the inclusive bounds of the events' timestamps.
	From, To time.Time
	// GroupKey is the attribution label grouped by.
	GroupKey string
	// LabelFilters keep the events whose label (the key) is one of the
	// values; "" matches the events without the label.
	LabelFilters map[string][]string
	// Charges are the charges read, in their order.
	Charges []AttributedCharge
	// Order ranks the values.
	Order AttributedOrder
	// Search keeps the values of the page containing it,
	// case-insensitively; "" keeps every value.
	Search string
	// Limit and Offset are the page, over the ranked values (or the
	// matching ones with a search).
	Limit, Offset int
	// MaxGroups is the most values the query groups before failing with
	// [ErrAttributedTooManyGroups]; zero means no limit.
	MaxGroups int
	// MaxExecutionTime is how long the query runs before failing with
	// [ErrAttributedTimeout]; zero means no limit.
	MaxExecutionTime time.Duration
}

// AttributedCell is a column group of the attributed usage: the usage of a
// charge, or of one filter of a split charge.
type AttributedCell struct {
	// ChargeID is the charge.
	ChargeID string
	// ChargeFilterID is the filter of a split charge; "" for a charge that
	// is not split and for the events of a split charge no filter matches.
	ChargeFilterID string
	// Priced reports whether the cell has an amount.
	Priced bool
	// Charge is the index of the charge in [AttributedQuery.Charges].
	Charge int
	// Rank is the filter rank of the cell of a split charge; 0 otherwise.
	Rank int
}

// Cells returns the cells of q: one per charge, in order, and one per
// filter of a split charge, by rank, followed by its default cell.
//
// source: app/services/events/stores/clickhouse/attributed_usage_query.rb:44-55
func (q AttributedQuery) Cells() []AttributedCell {
	var cells []AttributedCell
	for i, c := range q.Charges {
		priced := c.UnitAmount != nil
		if !c.Split || c.Lookup == nil {
			cells = append(cells, AttributedCell{ChargeID: c.ChargeID, Priced: priced, Charge: i})
			continue
		}
		for r, id := range c.Lookup.FilterIDs {
			cells = append(cells, AttributedCell{ChargeID: c.ChargeID, ChargeFilterID: id, Priced: priced, Charge: i, Rank: r + 1})
		}
		cells = append(cells, AttributedCell{ChargeID: c.ChargeID, Priced: priced, Charge: i, Rank: c.Lookup.DefaultRank()})
	}
	return cells
}

// AttributedCellUsage is the usage of one cell.
type AttributedCellUsage struct {
	// Units are the units.
	Units money.Decimal
	// Amount is the amount in minor units; zero when the cell is unpriced.
	Amount money.Decimal
	// Events is the number of events.
	Events int64
}

// AttributedValues is usage over every cell.
type AttributedValues struct {
	// Amount is the sum of the priced cells' amounts, in minor units.
	Amount money.Decimal
	// Events is the number of events.
	Events int64
	// Cells are the usage of each cell of [AttributedQuery.Cells].
	Cells []AttributedCellUsage
}

// Add returns v plus w; a zero value of either side reads as zeros.
func (v AttributedValues) Add(w AttributedValues) AttributedValues {
	n := max(len(v.Cells), len(w.Cells))
	out := AttributedValues{Amount: v.Amount.Add(w.Amount), Events: v.Events + w.Events, Cells: make([]AttributedCellUsage, n)}
	for i := range n {
		var a, b AttributedCellUsage
		if i < len(v.Cells) {
			a = v.Cells[i]
		}
		if i < len(w.Cells) {
			b = w.Cells[i]
		}
		out.Cells[i] = AttributedCellUsage{Units: a.Units.Add(b.Units), Amount: a.Amount.Add(b.Amount), Events: a.Events + b.Events}
	}
	return out
}

// AttributedRow is the usage of one attribution value.
type AttributedRow struct {
	// Value is the label's value.
	Value string
	// Rank is the value's rank among every value, from 1.
	Rank int
	// InPage reports whether the row is in the requested page; the first
	// ranked row is returned outside it too.
	InPage bool
	AttributedValues
}

// AttributedUsage is the result of an attributed usage query.
type AttributedUsage struct {
	// Rows are the rows of the page and the first ranked row, by rank.
	Rows []AttributedRow
	// Unattributed is the usage of the events without the label.
	Unattributed AttributedValues
	// Level is the usage over every value (not only the page); the
	// unattributed usage is not part of it.
	Level AttributedValues
	// Groups is the number of values.
	Groups int64
}

// The failures of an attributed usage query on its limits.
var (
	// ErrAttributedTooManyGroups is a query grouping more than
	// [AttributedQuery.MaxGroups] values.
	ErrAttributedTooManyGroups = errors.New("metering: attributed usage: too many groups")
	// ErrAttributedTimeout is a query running past
	// [AttributedQuery.MaxExecutionTime].
	ErrAttributedTimeout = errors.New("metering: attributed usage: query timeout")
	// ErrAttributedMemoryLimit is a query past the store's memory limit.
	ErrAttributedMemoryLimit = errors.New("metering: attributed usage: memory limit exceeded")
)

// AttributedUsageReader is an event store answering attributed usage
// queries: events carry the attribution labels on the ClickHouse store
// only.
type AttributedUsageReader interface {
	// AttributedUsageAvailable reports whether the store holding the
	// organization's events answers attributed usage queries.
	AttributedUsageAvailable(organizationID string) bool
	// AttributedUsage answers q.
	AttributedUsage(ctx context.Context, q AttributedQuery) (AttributedUsage, error)
}
