// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package store

import (
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/ee/analytics/period"
)

// Filter restricts a report; empty fields do not restrict it.
type Filter struct {
	// Periods are the report's periods, oldest first.
	Periods []period.Period
	// Currency is an ISO 4217 code.
	Currency string
	// CustomerCountry is the customers' billing country.
	CustomerCountry string
	// CustomerType is "company" or "individual".
	CustomerType string
	// ExternalCustomerID is one customer's external ID.
	ExternalCustomerID string
	// ExternalSubscriptionID is one subscription's external ID.
	ExternalSubscriptionID string
	// BillingEntityCode is one billing entity's code.
	BillingEntityCode string
	// PlanCode is one plan's code, deleted plans included.
	PlanCode string
	// CustomerTINEmpty keeps the customers without (true) or with (false)
	// a tax identification number.
	CustomerTINEmpty *bool
}

// From returns the first day of the range.
func (f Filter) From() time.Time { return f.Periods[0].Start }

// To returns the last day of the range.
func (f Filter) To() time.Time { return f.Periods[len(f.Periods)-1].End }

// query builds the SQL of a report: its arguments, the organization
// first ($1).
type query struct {
	args []any
}

func newQuery(org string) *query { return &query{args: []any{org}} }

// arg adds v and returns its placeholder.
func (q *query) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

// periods adds the periods and returns the SQL of a relation b(ord,
// start_date, end_date), ord counting from 1.
func (q *query) periods(ps []period.Period) string {
	starts := make([]time.Time, len(ps))
	ends := make([]time.Time, len(ps))
	for i, p := range ps {
		starts[i], ends[i] = p.Start, p.End
	}
	return "(SELECT t.ord::int AS ord, t.s AS start_date, t.e AS end_date FROM unnest(" +
		q.arg(starts) + "::date[], " + q.arg(ends) + "::date[]) WITH ORDINALITY AS t(s, e, ord)) AS b"
}

// customers returns the condition keeping the customers of col that the
// filter's customer fields select, or "TRUE".
func (q *query) customers(f Filter, col string) string {
	var conds []string
	if f.CustomerCountry != "" {
		conds = append(conds, "upper(c.billing_address_country) = upper("+q.arg(f.CustomerCountry)+")")
	}
	if f.CustomerType != "" {
		conds = append(conds, "c.customer_type = "+q.arg(f.CustomerType))
	}
	if f.ExternalCustomerID != "" {
		conds = append(conds, "c.external_id = "+q.arg(f.ExternalCustomerID))
	}
	if f.CustomerTINEmpty != nil {
		cond := "COALESCE(c.tax_identification_number, '') = ''"
		if !*f.CustomerTINEmpty {
			cond = "COALESCE(c.tax_identification_number, '') <> ''"
		}
		conds = append(conds, cond)
	}
	if len(conds) == 0 {
		return "TRUE"
	}
	return col + " IN (SELECT c.id FROM exports_customers c WHERE c.organization_id = $1 AND " + strings.Join(conds, " AND ") + ")"
}

// currency returns the condition on the currency column col.
func (q *query) currency(f Filter, col string) string {
	if f.Currency == "" {
		return "TRUE"
	}
	return col + " = " + q.arg(f.Currency)
}

// billingEntity returns the condition on the billing entity column col.
func (q *query) billingEntity(f Filter, col string) string {
	if f.BillingEntityCode == "" {
		return "TRUE"
	}
	return col + " IN (SELECT be.id FROM exports_billing_entities be WHERE be.organization_id = $1 AND be.code = " + q.arg(f.BillingEntityCode) + ")"
}

// subscription returns the condition on the subscription column col.
func (q *query) subscription(f Filter, col string) string {
	if f.ExternalSubscriptionID == "" {
		return "TRUE"
	}
	return col + " IN (SELECT s.id FROM exports_subscriptions s WHERE s.organization_id = $1 AND s.external_id = " + q.arg(f.ExternalSubscriptionID) + ")"
}

// plan returns the condition on the plan column col.
func (q *query) plan(f Filter, col string) string {
	if f.PlanCode == "" {
		return "TRUE"
	}
	return col + " IN (SELECT p.id FROM analytics.plans p WHERE p.organization_id = $1 AND p.code = " + q.arg(f.PlanCode) + ")"
}

// subscriptionPlan returns the condition on the plan of the subscription
// column col.
func (q *query) subscriptionPlan(f Filter, col string) string {
	if f.PlanCode == "" {
		return "TRUE"
	}
	return col + " IN (SELECT s.id FROM exports_subscriptions s WHERE s.organization_id = $1 AND " + q.plan(f, "s.plan_id") + ")"
}

// subscriptionBillingEntity returns the condition on the applicable
// billing entity of the subscription column col: its own, or its
// customer's.
func (q *query) subscriptionBillingEntity(f Filter, col string) string {
	if f.BillingEntityCode == "" {
		return "TRUE"
	}
	return col + " IN (SELECT s.id FROM exports_subscriptions s JOIN exports_customers c ON c.organization_id = s.organization_id AND c.id = s.customer_id" +
		" WHERE s.organization_id = $1 AND " + q.billingEntity(f, "COALESCE(s.billing_entity_id, c.billing_entity_id)") + ")"
}

// and joins conditions.
func and(conds ...string) string {
	return strings.Join(conds, " AND ")
}
