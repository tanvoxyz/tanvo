package validators

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// rubyDateLimit is the longest string Ruby's Date parsers accept; a longer
// one raises ArgumentError.
const rubyDateLimit = 128

var (
	// rfc3339Re is Ruby's Date._rfc3339.
	rfc3339Re = regexp.MustCompile(`(?i)\A\s*(-?\d{4})-(\d{2})-(\d{2})(?:t|\s)(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(z|[-+]\d{2}:\d{2})\s*\z`)
	// hour24Re, leapSecondRe and offsetRe are json_schemer's date-time
	// format guards.
	hour24Re     = regexp.MustCompile(`[Tt\s]24:`)
	leapSecondRe = regexp.MustCompile(`[Tt\s]\d{2}:\d{2}:6`)
	offsetRe     = regexp.MustCompile(`(?i)(Z|[\+\-]([01][0-9]|2[0-3]):[0-5][0-9])\z`)
	uuidRe       = regexp.MustCompile(`(?i)\A[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\z`)

	// isoExtRe and isoBasicRe are the calendar and ordinal forms of Ruby's
	// Date._iso8601 (extended and basic notation) that carry a year; the
	// other forms (week dates, truncated dates, times alone) have none, so
	// ActiveSupport's TimeZone#iso8601 rejects them.
	isoExtRe   = regexp.MustCompile(`(?i)\A\s*(?:([-+]?\d{2,})-(\d{2})-(\d{2})|([-+]?\d{2,})-(\d{3}))(?:t(\d{2}):(\d{2})(?::(\d{2})(?:[,.](\d+))?)?(z|[-+]\d{2}(?::?\d{2})?)?)?\s*\z`)
	isoBasicRe = regexp.MustCompile(`(?i)\A\s*(?:([-+]?(?:\d{4}|\d{2}))(\d{2})(\d{2})|([-+]?(?:\d{4}|\d{2}))(\d{3}))(?:t?(\d{2})(\d{2})(?:(\d{2})(?:[,.](\d+))?)?(z|[-+]\d{2}(?:\d{2})?)?)?\s*\z`)
)

// validUUID is json_schemer's uuid format.
func validUUID(s string) bool {
	return uuidRe.MatchString(s) || s == "00000000-0000-0000-0000-000000000000"
}

// dateTimeParts are the components of a parsed date and time.
type dateTimeParts struct {
	year, month, day, yday     int
	hour, minute, second       int
	nanos                      int
	offset                     int // seconds east of UTC
	hasOffset, hasYday, hasDay bool
}

// parseRFC3339 is Ruby's DateTime.rfc3339 component check: a valid civil
// date and a time with seconds 0–60 (60 is read as 59, as Ruby does).
func parseRFC3339(s string) (dateTimeParts, bool) {
	var p dateTimeParts
	if len(s) > rubyDateLimit {
		return p, false
	}
	m := rfc3339Re.FindStringSubmatch(s)
	if m == nil {
		return p, false
	}
	p.year, _ = strconv.Atoi(m[1])
	p.month, _ = strconv.Atoi(m[2])
	p.day, _ = strconv.Atoi(m[3])
	p.hour, _ = strconv.Atoi(m[4])
	p.minute, _ = strconv.Atoi(m[5])
	p.second, _ = strconv.Atoi(m[6])
	p.nanos = fractionNanos(m[7])
	off, ok := parseOffset(m[8])
	if !ok {
		return p, false
	}
	p.offset, p.hasOffset, p.hasDay = off, true, true
	if !validCivil(p.year, p.month, p.day) || p.hour > 23 || p.minute > 59 || p.second > 60 {
		return p, false
	}
	return p, true
}

// validDateTimeFormat is json_schemer's date-time format.
func validDateTimeFormat(s string) bool {
	if hour24Re.MatchString(s) {
		return false
	}
	p, ok := parseRFC3339(s)
	if !ok {
		return false
	}
	if leapSecondRe.MatchString(s) {
		sec := min(p.second, 59)
		utc := time.Date(p.year, time.Month(p.month), p.day, p.hour, p.minute, sec, 0, time.FixedZone("", p.offset)).UTC()
		if utc.Hour() != 23 || utc.Minute() != 59 {
			return false
		}
	}
	return offsetRe.MatchString(s)
}

// parseTime returns the instant of a string the date-time format accepts
// (Time.zone.parse in UTC; a second of 60 rolls over, as Ruby's Time does).
func parseTime(s string) (time.Time, bool) {
	p, ok := parseRFC3339(s)
	if !ok {
		return time.Time{}, false
	}
	return time.Date(p.year, time.Month(p.month), p.day, p.hour, p.minute, p.second, p.nanos, time.FixedZone("", p.offset)).UTC(), true
}

// parseISO8601Parts is Ruby's Date._iso8601 restricted to the forms that
// carry a year.
func parseISO8601Parts(s string) (dateTimeParts, bool) {
	var p dateTimeParts
	if len(s) > rubyDateLimit {
		return p, false
	}
	m := isoExtRe.FindStringSubmatch(s)
	if m == nil {
		m = isoBasicRe.FindStringSubmatch(s)
	}
	if m == nil {
		return p, false
	}
	if m[1] != "" {
		p.year = isoYear(m[1])
		p.month, _ = strconv.Atoi(m[2])
		p.day, _ = strconv.Atoi(m[3])
		p.hasDay = true
	} else {
		p.year = isoYear(m[4])
		p.yday, _ = strconv.Atoi(m[5])
		p.hasYday = true
	}
	if m[6] != "" {
		p.hour, _ = strconv.Atoi(m[6])
		p.minute, _ = strconv.Atoi(m[7])
		if m[8] != "" {
			p.second, _ = strconv.Atoi(m[8])
		}
		p.nanos = fractionNanos(m[9])
		if m[10] != "" {
			off, ok := parseOffset(m[10])
			if !ok {
				return p, false
			}
			p.offset, p.hasOffset = off, true
		}
	}
	return p, true
}

// isoYear reads a year, completing a two- or three-digit one with
// Ruby's comp_year69.
func isoYear(s string) int {
	y, _ := strconv.Atoi(strings.TrimPrefix(s, "+"))
	digits := strings.TrimLeft(s, "+-")
	if len(digits) < 4 && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "+") {
		if y >= 69 {
			return y + 1900
		}
		return y + 2000
	}
	return y
}

// validISO8601 is Utils::Datetime.valid_format? (ActiveSupport
// TimeZone#iso8601): the components must build a Ruby Time, whose day may
// overflow the month up to 31 and whose hour may be 24 at 24:00:00.
func validISO8601(s string) bool {
	p, ok := parseISO8601Parts(s)
	if !ok {
		return false
	}
	if p.hasYday {
		if p.yday < 1 || p.yday > daysInYear(p.year) {
			return false
		}
	} else if p.month < 1 || p.month > 12 || p.day < 1 || p.day > 31 {
		return false
	}
	if p.hour > 24 || p.minute > 59 || p.second > 60 || (p.hour == 24 && (p.minute != 0 || p.second != 0 || p.nanos != 0)) {
		return false
	}
	return p.offset > -86400 && p.offset < 86400
}

// parseISO8601Date is Utils::Datetime.parse_iso8601(s)&.to_date: the
// calendar date of a valid ISO 8601 date and time in its own offset; false
// when DateTime.iso8601 rejects it (an impossible civil date included).
func parseISO8601Date(s string) (time.Time, bool) {
	p, ok := parseISO8601Parts(s)
	if !ok {
		return time.Time{}, false
	}
	var d time.Time
	if p.hasYday {
		if p.yday < 1 || p.yday > daysInYear(p.year) {
			return time.Time{}, false
		}
		d = time.Date(p.year, 1, p.yday, 0, 0, 0, 0, time.UTC)
	} else {
		if !validCivil(p.year, p.month, p.day) {
			return time.Time{}, false
		}
		d = time.Date(p.year, time.Month(p.month), p.day, 0, 0, 0, 0, time.UTC)
	}
	if p.hour > 24 || p.minute > 59 || p.second > 60 || (p.hour == 24 && (p.minute != 0 || p.second != 0)) {
		return time.Time{}, false
	}
	if p.offset <= -86400 || p.offset >= 86400 {
		return time.Time{}, false
	}
	if p.hour == 24 {
		d = d.AddDate(0, 0, 1)
	}
	return d, true
}

func validCivil(year, month, day int) bool {
	if month < 1 || month > 12 || day < 1 {
		return false
	}
	return day <= time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func daysInYear(year int) int {
	return time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).YearDay()
}

// parseOffset reads "z", "+hh", "+hhmm" or "+hh:mm".
func parseOffset(s string) (int, bool) {
	if s == "" || strings.EqualFold(s, "z") {
		return 0, true
	}
	sign := 1
	if s[0] == '-' {
		sign = -1
	}
	digits := strings.ReplaceAll(s[1:], ":", "")
	h, err := strconv.Atoi(digits[:2])
	if err != nil {
		return 0, false
	}
	mm := 0
	if len(digits) > 2 {
		if mm, err = strconv.Atoi(digits[2:]); err != nil {
			return 0, false
		}
	}
	return sign * (h*3600 + mm*60), true
}

// fractionNanos reads the digits of a fraction of a second.
func fractionNanos(s string) int {
	if s == "" {
		return 0
	}
	if len(s) > 9 {
		s = s[:9]
	}
	n, _ := strconv.Atoi(s + strings.Repeat("0", 9-len(s)))
	return n
}
