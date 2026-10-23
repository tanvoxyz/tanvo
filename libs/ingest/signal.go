package ingest

import (
	"cmp"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// UsageSignal tells the usage refresh that a customer's subscriptions
// received events (architecture §7.1); the refresh recomputes their usage
// up to the watermark. It is an output of ingestion only: the usage
// buckets consume it.
type UsageSignal struct {
	// OrganizationID is the organization of the customer.
	OrganizationID core.OrganizationID `json:"organization_id"`
	// CustomerID is the customer whose usage changed.
	CustomerID core.CustomerID `json:"customer_id"`
	// SubscriptionIDs are the subscriptions that received events, sorted
	// and distinct.
	SubscriptionIDs []core.SubscriptionID `json:"subscription_ids"`
	// Watermark is the latest ingestion instant of the events signalled.
	Watermark time.Time `json:"watermark"`
}

type signalKey struct {
	org      core.OrganizationID
	customer core.CustomerID
}

// Signals collapses events into one signal per (organization, customer),
// in the order the customers first appear. Events without a subscription
// signal nothing.
func Signals(events []*Enriched) []UsageSignal {
	var out []UsageSignal
	at := map[signalKey]int{}
	for _, e := range events {
		if e == nil || e.SubscriptionID.IsZero() {
			continue
		}
		k := signalKey{e.OrganizationID, e.CustomerID}
		i, ok := at[k]
		if !ok {
			i = len(out)
			at[k] = i
			out = append(out, UsageSignal{OrganizationID: e.OrganizationID, CustomerID: e.CustomerID})
		}
		s := &out[i]
		if !slices.Contains(s.SubscriptionIDs, e.SubscriptionID) {
			s.SubscriptionIDs = append(s.SubscriptionIDs, e.SubscriptionID)
		}
		if e.IngestedAt.After(s.Watermark) {
			s.Watermark = e.IngestedAt
		}
	}
	for i := range out {
		slices.SortFunc(out[i].SubscriptionIDs, func(a, b core.SubscriptionID) int { return cmp.Compare(a.String(), b.String()) })
	}
	return out
}

// inAdvance returns the events bound for in-advance billing.
func inAdvance(events []*Enriched) []*Enriched {
	var out []*Enriched
	for _, e := range events {
		if e.InAdvance && !e.SubscriptionID.IsZero() {
			out = append(out, e)
		}
	}
	return out
}
