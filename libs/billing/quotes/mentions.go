package quotes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/quotes/validators"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// mentionVariables computes the values of the content placeholders frozen
// when version v of quote q is approved (lago
// QuoteVersions::ComputeMentionVariablesService,
// app/services/quote_versions/compute_mention_variables_service.rb:12-143).
// The values are locale independent; they are localized when they are
// shown (documents.MentionLocalizer).
func (s *Service) mentionVariables(ctx context.Context, r port.Repos, q *quote.Quote, v *quote.Version) (json.RawMessage, error) {
	org := q.OrganizationID
	env, err := kernel.LoadCustomerEnv(ctx, r, org, q.CustomerID, core.BillingEntityID{})
	if err != nil {
		return nil, err
	}
	o, err := r.Organizations.Get(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("load organization %s: %w", org, err)
	}
	logo, err := s.blobURL(ctx, org, o.Logo)
	if err != nil {
		return nil, err
	}
	var amended *subscription.Subscription
	if q.OrderType == quote.OrderTypeSubscriptionAmendment && !q.SubscriptionID.IsZero() {
		amended, err = r.Subscriptions.Get(ctx, org, q.SubscriptionID)
		if errors.Is(err, core.ErrNotFound) {
			amended = nil
		} else if err != nil {
			return nil, fmt.Errorf("load subscription %s: %w", q.SubscriptionID, err)
		}
	}
	be, err := versionBillingEntity(ctx, r, v, amended, env.BillingEntity)
	if err != nil {
		return nil, err
	}
	c := env.Customer
	loc := env.Location
	items := decodeItems(v.BillingItems)
	start, end := termDates(q, items, amended, loc)

	m := map[string]any{
		"customer_name":                  c.DisplayName(true),
		"customer_email":                 c.Email,
		"organization_name":              o.Name,
		"organization_logo":              nilIfEmpty(logo),
		"billing_entity_name":            nil,
		"billing_entity_legal_name":      nil,
		"billing_entity_address":         nil,
		"billing_entity_tax_id":          nil,
		"billing_entity_email":           nil,
		"quote_number":                   q.Number,
		"quote_date":                     q.CreatedAt.In(loc).Format(time.DateOnly),
		"quote_version":                  strconv.Itoa(v.Version),
		"quote_currency":                 nilIfEmpty(v.Currency.Code()),
		"commercial_terms_term_duration": termDuration(start, end),
		"commercial_terms_start_date":    nil,
		"commercial_terms_payment_terms": c.ApplicableNetPaymentTermDays(env.Defaults),
	}
	if start != nil {
		m["commercial_terms_start_date"] = start.Format(time.DateOnly)
	}
	if be != nil {
		a := be.Address
		m["billing_entity_name"] = be.Name
		m["billing_entity_legal_name"] = be.LegalName
		m["billing_entity_tax_id"] = be.TaxID
		m["billing_entity_email"] = be.Email
		m["billing_entity_address"] = map[string]any{
			"address_line1":       nilIfEmpty(a.Line1),
			"address_line2":       nilIfEmpty(a.Line2),
			"locality":            nilIfEmpty(a.City),
			"postal_code":         nilIfEmpty(a.Zipcode),
			"administrative_area": nilIfEmpty(a.State),
			"country_code":        nilIfEmpty(a.Country),
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode mention variables: %w", err)
	}
	return b, nil
}

// versionBillingEntity is lago QuoteVersion#billing_entity
// (app/models/quote_version.rb:68-70): the version's own, else the amended
// subscription's, else the customer's.
func versionBillingEntity(ctx context.Context, r port.Repos, v *quote.Version, amended *subscription.Subscription, customer *organization.BillingEntity) (*organization.BillingEntity, error) {
	for _, id := range []core.BillingEntityID{v.BillingEntityID, amendedEntity(amended)} {
		if id.IsZero() {
			continue
		}
		be, err := r.BillingEntities.Get(ctx, v.OrganizationID, id)
		if err == nil {
			return be, nil
		}
		if !errors.Is(err, core.ErrNotFound) {
			return nil, fmt.Errorf("load billing entity %s: %w", id, err)
		}
	}
	return customer, nil
}

func amendedEntity(s *subscription.Subscription) core.BillingEntityID {
	if s == nil {
		return core.BillingEntityID{}
	}
	return s.BillingEntityID
}

// termDates are the start and end of the commercial term: for a one-off,
// the earliest fromDatetime and latest toDatetime of the add-ons
// (overrides first); otherwise the earliest startDate and latest endDate
// of the plans, an amendment starting at the amended subscription's start
// when no plan states one (compute_mention_variables_service.rb:90-130).
func termDates(q *quote.Quote, items object, amended *subscription.Subscription, loc *time.Location) (start, end *time.Time) {
	var starts, ends []time.Time
	if q.OrderType == quote.OrderTypeOneOff {
		for _, a := range objects(items["addOns"]) {
			starts = appendDate(starts, effective(a, "fromDatetime"))
			ends = appendDate(ends, effective(a, "toDatetime"))
		}
	} else {
		for _, p := range objects(items["plans"]) {
			starts = appendDate(starts, dig(p, "payload", "startDate"))
			ends = appendDate(ends, dig(p, "payload", "endDate"))
		}
	}
	if len(starts) > 0 {
		t := slices.MinFunc(starts, time.Time.Compare)
		start = &t
	} else if q.OrderType == quote.OrderTypeSubscriptionAmendment && amended != nil {
		at := amended.SubscriptionAt.In(loc)
		t := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		start = &t
	}
	if len(ends) > 0 {
		t := slices.MaxFunc(ends, time.Time.Compare)
		end = &t
	}
	return start, end
}

func appendDate(dates []time.Time, v any) []time.Time {
	s, ok := v.(string)
	if !ok {
		return dates
	}
	if d, ok := validators.ParseDate(s); ok {
		return append(dates, d)
	}
	return dates
}

// termDuration is the largest whole unit between start and end: years,
// months, then days (compute_mention_variables_service.rb:67-81,139-143).
func termDuration(start, end *time.Time) map[string]any {
	if start == nil || end == nil {
		return nil
	}
	months := (end.Year()*12 + int(end.Month())) - (start.Year()*12 + int(start.Month()))
	if end.Day() < start.Day() {
		months--
	}
	switch {
	case months < 1:
		return map[string]any{"unit": "days", "count": int(end.Sub(*start).Hours() / 24)}
	case months%12 == 0:
		return map[string]any{"unit": "years", "count": months / 12}
	default:
		return map[string]any{"unit": "months", "count": months}
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
