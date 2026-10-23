package dunning

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Campaign is a dunning campaign (domain §16.1). It is applied to a billing
// entity (the default of its customers) or to a customer (an override), and
// soft-deleted.
type Campaign struct {
	// ID identifies the campaign.
	ID core.DunningCampaignID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique among the organization's live campaigns.
	Code string
	// Name is required, ≤ 255 characters.
	Name string
	// Description is free text.
	Description *string
	// DaysBetweenAttempts is the minimum number of days between two
	// attempts (≥ 1).
	DaysBetweenAttempts int
	// MaxAttempts is the number of attempts per currency (≥ 1).
	MaxAttempts int
	// BCCEmails are copied on every dunning email; each is a valid address.
	BCCEmails []string
	// Thresholds are the overdue balances that trigger an attempt; at least
	// one, one per currency.
	Thresholds []Threshold
	// DeletedAt is set once deleted.
	DeletedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Threshold is the overdue balance in one currency from which a campaign
// dunns a customer.
type Threshold struct {
	// ID identifies the threshold.
	ID core.DunningCampaignThresholdID
	// Currency is the currency of the overdue balance.
	Currency money.Currency
	// Amount is the minimum overdue balance (≥ 0), in minor units of
	// Currency.
	Amount int64
}

// New returns a campaign with the defaults of domain §16.1: one day between
// attempts and one attempt.
func New(org core.OrganizationID, id core.DunningCampaignID, code, name string, now time.Time) *Campaign {
	return &Campaign{
		ID:                  id,
		OrganizationID:      org,
		Code:                code,
		Name:                name,
		DaysBetweenAttempts: 1,
		MaxAttempts:         1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

// Validate checks the invariants of domain §16.1.
func (c *Campaign) Validate() error {
	var v core.Validator
	v.Code("code", c.Code, 255)
	v.String("name", c.Name, 255)
	v.Check(c.DaysBetweenAttempts >= 1, "days_between_attempts", core.CodeOutOfRange)
	v.Check(c.MaxAttempts >= 1, "max_attempts", core.CodeOutOfRange)
	for i, e := range c.BCCEmails {
		v.Email(fmt.Sprintf("bcc_emails[%d]", i), e)
	}
	// A campaign may lose all its thresholds on update; only creation
	// requires one (app/services/dunning_campaigns/create_service.rb:16).
	seen := map[string]bool{}
	for i, t := range c.Thresholds {
		p := fmt.Sprintf("thresholds[%d]", i)
		if v.Currency(p+".currency", t.Currency) {
			v.Check(!seen[t.Currency.Code()], p+".currency", core.CodeAlreadyExists)
			seen[t.Currency.Code()] = true
		}
		v.NonNegative(p+".amount", t.Amount)
	}
	return v.Err()
}

// Deleted reports whether the campaign was deleted.
func (c *Campaign) Deleted() bool { return c.DeletedAt != nil }

// Delete soft-deletes the campaign at now; the caller resets the customers
// and billing entities that used it.
func (c *Campaign) Delete(now time.Time) error {
	if c.Deleted() {
		return &core.TransitionError{Entity: "dunning_campaign", From: "deleted", To: "deleted"}
	}
	c.DeletedAt = &now
	c.UpdatedAt = now
	return nil
}

// Threshold returns the campaign's threshold in currency code cur.
func (c *Campaign) Threshold(cur string) (Threshold, bool) {
	for _, t := range c.Thresholds {
		if t.Currency.Code() == cur {
			return t, true
		}
	}
	return Threshold{}, false
}

// Applicable returns the campaign that applies to a customer: none when the
// customer is excluded, otherwise the customer's own campaign or, without
// one, its billing entity's.
func Applicable(excluded bool, customerCampaign, billingEntityCampaign core.DunningCampaignID) core.DunningCampaignID {
	switch {
	case excluded:
		return core.DunningCampaignID{}
	case !customerCampaign.IsZero():
		return customerCampaign
	default:
		return billingEntityCampaign
	}
}

// CustomerState is a customer's dunning progress under a campaign.
type CustomerState struct {
	// Excluded reports a customer opted out of dunning.
	Excluded bool
	// Attempts counts the attempts made per currency code.
	Attempts map[string]int
	// LastAttemptAt is the instant of the last attempt; nil when none.
	LastAttemptAt *time.Time
}

// Attempt is the outcome of [Campaign.Plan]: the thresholds to dun now and
// the customer's new attempt counters.
type Attempt struct {
	// Thresholds are the thresholds reached, ordered by currency code; a
	// payment request is created for each (and each billing entity holding
	// overdue invoices in its currency).
	Thresholds []Threshold
	// Attempts are the customer's counters after this attempt.
	Attempts map[string]int
	// Finished reports that every dunned currency reached
	// [Campaign.MaxAttempts]: dunning_campaign.finished is emitted.
	Finished bool
}

// Due reports whether the attempt dunns anything; when it does, the caller
// stores [Attempt.Attempts] and now as the customer's last attempt.
func (a Attempt) Due() bool { return len(a.Thresholds) > 0 }

// Plan makes the hourly dunning decision for a customer (domain §16.1)
// given its overdue balances (Σ total due of overdue invoices ready for
// payment processing, in minor units, per currency code): nothing when the
// customer is excluded or [Campaign.DaysBetweenAttempts] days have not
// passed since its last attempt; otherwise every currency whose attempts
// are below [Campaign.MaxAttempts] and whose balance reaches its threshold
// is dunned, and its counter incremented.
func (c *Campaign) Plan(s CustomerState, overdue map[string]int64, now time.Time) Attempt {
	none := Attempt{Attempts: maps.Clone(s.Attempts)}
	if s.Excluded {
		return none
	}
	if s.LastAttemptAt != nil && !now.After(s.LastAttemptAt.AddDate(0, 0, c.DaysBetweenAttempts)) {
		return none
	}
	var out Attempt
	for _, cur := range slices.Sorted(maps.Keys(overdue)) {
		if s.Attempts[cur] >= c.MaxAttempts {
			continue
		}
		if t, ok := c.Threshold(cur); ok && t.Amount <= overdue[cur] {
			out.Thresholds = append(out.Thresholds, t)
		}
	}
	if len(out.Thresholds) == 0 {
		return none
	}
	out.Attempts = maps.Clone(s.Attempts)
	if out.Attempts == nil {
		out.Attempts = map[string]int{}
	}
	for _, t := range out.Thresholds {
		out.Attempts[t.Currency.Code()]++
	}
	out.Finished = true
	for _, n := range out.Attempts {
		if n < c.MaxAttempts {
			out.Finished = false
		}
	}
	return out
}
