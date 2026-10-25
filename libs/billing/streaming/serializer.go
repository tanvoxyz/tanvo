package streaming

import (
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ObjectType is the object_type of the usage-refreshed records.
//
// source: app/services/event_destinations/customer_usage/refreshed_service.rb:9
const ObjectType = "customer_usage"

// Envelope is one record written to a stream.
//
// source: app/services/event_destinations/customer_usage/refreshed_service.rb:99-110
type Envelope struct {
	// EventID is a fresh UUIDv7.
	EventID string `json:"event_id"`
	// EventType is the streamed event type.
	EventType string `json:"event_type"`
	// ObjectType is [ObjectType].
	ObjectType string `json:"object_type"`
	// OrganizationID is the customer's organization.
	OrganizationID string `json:"organization_id"`
	// CustomerExternalID is the customer's external ID.
	CustomerExternalID string `json:"customer_external_id"`
	// SubscriptionExternalID is the subscription's external ID.
	SubscriptionExternalID string `json:"subscription_external_id"`
	// Version is the instant of the delivery in UTC with microseconds,
	// the same for every record of one delivery.
	Version string `json:"version"`
	// CustomerUsage is the usage of the subscription.
	CustomerUsage CustomerUsage `json:"customer_usage"`
}

// CustomerUsage is the usage of one subscription in a record: its
// pre-tax amounts, the ongoing usage of the customer's wallets and the
// charges with usage.
//
// source: app/serializers/event_destinations/customer_usage_serializer.rb
type CustomerUsage struct {
	// FromDatetime is the start of the usage period.
	FromDatetime string `json:"from_datetime"`
	// ToDatetime is the end, exclusive (domain §2.3), of the usage
	// period.
	ToDatetime string `json:"to_datetime"`
	// IssuingDate is the date the period will be invoiced.
	IssuingDate string `json:"issuing_date"`
	// Currency is the plan currency.
	Currency string `json:"currency"`
	// AmountCents is the sum of the fees before taxes.
	AmountCents int64 `json:"amount_cents"`
	// Wallets are the customer's active wallets in application order;
	// empty, never absent, without any.
	Wallets []WalletUsage `json:"wallets"`
	// ChargesUsage are the charges with usage, one per charge.
	ChargesUsage []ChargeUsage `json:"charges_usage"`
}

// WalletUsage is the ongoing usage a refresh allocated to a wallet.
type WalletUsage struct {
	// LagoID is the wallet's ID.
	LagoID string `json:"lago_id"`
	// Credits is the ongoing usage in credits.
	Credits string `json:"credits"`
	// AmountCents is the ongoing usage in minor units.
	AmountCents int64 `json:"amount_cents"`
	// AmountCurrency is the wallet currency.
	AmountCurrency string `json:"amount_currency"`
}

// ChargeUsage sums the fees of one charge.
type ChargeUsage struct {
	// Units is the summed units.
	Units string `json:"units"`
	// EventsCount is the summed event count.
	EventsCount int64 `json:"events_count"`
	// AmountCents is the summed amount before taxes.
	AmountCents int64 `json:"amount_cents"`
	// AmountCurrency is the currency of the charge's first fee.
	AmountCurrency string `json:"amount_currency"`
	// Charge names the charge.
	Charge Ref `json:"charge"`
	// BillableMetric names the charge's metric.
	BillableMetric Ref `json:"billable_metric"`
}

// Ref names an object by ID and code.
type Ref struct {
	// LagoID is the object's ID.
	LagoID string `json:"lago_id"`
	// Code is the object's code.
	Code string `json:"code"`
}

// ChargeInfo is what a record names of a charge.
type ChargeInfo struct {
	// Code is the charge code.
	Code string
	// MetricID is the charge's billable metric.
	MetricID core.BillableMetricID
	// MetricCode is the metric code.
	MetricCode string
}

// SerializeUsage returns the record body of the usage u, the customer's
// wallets ws and the charges of u's fees.
//
// The fees without usage (Fee#non_zero?) are left out, as the daily usages
// trim them; the others are grouped by charge in the order of their first
// fee.
//
// source: app/serializers/event_destinations/customer_usage_serializer.rb:5-15
func SerializeUsage(u *usage.Usage, ws []*wallet.Wallet, charges map[core.ChargeID]ChargeInfo) CustomerUsage {
	out := CustomerUsage{
		FromDatetime: u.From.UTC().Format(time.RFC3339),
		ToDatetime:   u.To.UTC().Format(time.RFC3339),
		IssuingDate:  u.IssuingDate.String(),
		Currency:     u.Currency.Code(),
		AmountCents:  u.Amount,
		Wallets:      make([]WalletUsage, 0, len(ws)),
		ChargesUsage: []ChargeUsage{},
	}
	for _, w := range ws {
		out.Wallets = append(out.Wallets, WalletUsage{
			LagoID:         w.ID.String(),
			Credits:        RubyDecimal(w.CreditsOngoingUsageBalance),
			AmountCents:    w.OngoingUsageBalanceAmount,
			AmountCurrency: w.Currency.Code(),
		})
	}
	type group struct {
		fees  []*invoice.Fee
		units money.Decimal
	}
	var order []core.ChargeID
	groups := map[core.ChargeID]*group{}
	for _, f := range u.Fees {
		if !f.NonZero() {
			continue
		}
		g, ok := groups[f.ChargeID]
		if !ok {
			g = &group{}
			groups[f.ChargeID] = g
			order = append(order, f.ChargeID)
		}
		g.fees = append(g.fees, f)
		g.units = g.units.Add(f.Units)
	}
	for _, id := range order {
		g := groups[id]
		info := charges[id]
		cu := ChargeUsage{
			Units:          RubyDecimal(g.units),
			AmountCurrency: g.fees[0].Currency.Code(),
			Charge:         Ref{LagoID: id.String(), Code: info.Code},
			BillableMetric: Ref{LagoID: info.MetricID.String(), Code: info.MetricCode},
		}
		for _, f := range g.fees {
			cu.EventsCount += f.EventsCount
			cu.AmountCents += f.Amount
		}
		out.ChargesUsage = append(out.ChargesUsage, cu)
	}
	return out
}

// chargeIDs returns the charges of the fees of u that have usage.
func chargeIDs(u *usage.Usage) []core.ChargeID {
	var ids []core.ChargeID
	for _, f := range u.Fees {
		if f.NonZero() && !slices.Contains(ids, f.ChargeID) {
			ids = append(ids, f.ChargeID)
		}
	}
	return ids
}

// RubyDecimal formats d as the records have always carried decimals: plain
// notation without trailing zeros and with at least one fractional digit
// ("15.0", "0.5", "1.25").
func RubyDecimal(d money.Decimal) string {
	d = d.Reduce()
	if d.IsZero() {
		return "0.0"
	}
	s := d.String()
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}
