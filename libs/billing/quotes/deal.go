package quotes

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/quotes/validators"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
)

// dealExpiration is lago QuoteVersions::DealExpiration.earliest
// (app/services/quote_versions/deal_expiration.rb:9-16): the earliest date
// of the plans' endDate, the wallet credits' expirationAt and their
// recurring rules' expirationAt; false when the deal is unbounded.
func dealExpiration(v *quote.Version) (time.Time, bool) {
	items := decodeItems(v.BillingItems)
	var dates []any
	for _, p := range objects(items["plans"]) {
		dates = append(dates, dig(p, "payload", "endDate"))
	}
	for _, w := range objects(items["walletCredits"]) {
		dates = append(dates, dig(w, "payload", "expirationAt"))
		for _, r := range objects(dig(w, "payload", "recurringTransactionRules")) {
			dates = append(dates, r["expirationAt"])
		}
	}
	var earliest time.Time
	found := false
	for _, d := range dates {
		s, ok := d.(string)
		if !ok {
			continue
		}
		t, ok := validators.ParseDate(s)
		if !ok {
			continue
		}
		if !found || t.Before(earliest) {
			earliest, found = t, true
		}
	}
	return earliest, found
}

// dealCovers is lago DealExpiration.covers? (deal_expiration.rb:21-29):
// true for an unbounded deal or a nil at, otherwise whether the UTC date
// of at is before the deal expiration.
func dealCovers(v *quote.Version, at *time.Time) bool {
	exp, ok := dealExpiration(v)
	if !ok || at == nil {
		return true
	}
	u := at.UTC()
	d := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return d.Before(exp)
}
