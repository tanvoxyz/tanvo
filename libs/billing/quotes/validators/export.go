package validators

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// ParseDate is Utils::Datetime.parse_iso8601(s)&.to_date: the calendar
// date (at midnight UTC) of an ISO 8601 date or date and time, read in its
// own offset; false when it is not one.
func ParseDate(s string) (time.Time, bool) { return parseISO8601Date(s) }

// ParseInstant is Utils::Datetime.parse_iso8601(s): the instant of an ISO
// 8601 date or date and time, a missing offset being UTC; false when it is
// not one.
func ParseInstant(s string) (time.Time, bool) {
	d, ok := parseISO8601Date(s)
	if !ok {
		return time.Time{}, false
	}
	p, _ := parseISO8601Parts(s)
	return time.Date(d.Year(), d.Month(), d.Day(), p.hour%24, p.minute, min(p.second, 59), p.nanos, time.FixedZone("", p.offset)).UTC(), true
}

// UnderscoreProperties is Utils::ChargeProperties.underscore_keys on the
// JSON object raw: every key is underscored, deeply, except under
// custom_properties. A null or empty raw returns nil.
func UnderscoreProperties(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	v, err := decodeValue(raw)
	if err != nil {
		return nil, err
	}
	if v.isNull() {
		return nil, nil
	}
	if v.kind != kindObject {
		return nil, errors.New("validators: properties must be a JSON object")
	}
	return json.Marshal(underscoreKeys(v))
}

// CurrencyListed reports whether code is in lago's currency list
// (Currencies::ACCEPTED_CURRENCIES): an ISO 4217 code, not a custom one.
func CurrencyListed(code string) bool {
	c, err := money.LookupCurrency(code)
	return err == nil && currencyListed(c)
}
