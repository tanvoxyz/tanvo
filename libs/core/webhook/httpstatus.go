package webhook

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// HTTPStatusRange is an inclusive range of HTTP status codes.
type HTTPStatusRange struct {
	// Min is the lowest code kept.
	Min int
	// Max is the highest code kept.
	Max int
}

// HTTPStatusFilter keeps deliveries by the HTTP status of their last
// attempt: one in Ranges, or, with Timeout, no response at all on a
// failed delivery. The zero value keeps every delivery.
type HTTPStatusFilter struct {
	// Ranges are the code ranges kept.
	Ranges []HTTPStatusRange
	// Timeout keeps failed deliveries that got no response
	// (webhooks_query.rb:70-72); a retrying one is not kept.
	Timeout bool
}

// httpStatusPattern is the accepted form of an HTTP status filter value
// (webhooks_query_filters_contract.rb:10).
var httpStatusPattern = regexp.MustCompile(`(?i)^(\d{3}|\dxx|\d{3}\s*-\s*\d{3}|timeout)$`)

// ParseHTTPStatusFilter parses HTTP status filter values (lago
// WebhooksQuery#get_http_status_ranges): an exact code ("404"), a class
// ("5xx"), a range ("200-204") or "timeout", in any case. Another value
// fails with [core.ErrInvalidValue].
func ParseHTTPStatusFilter(values []string) (HTTPStatusFilter, error) {
	var f HTTPStatusFilter
	for _, raw := range values {
		v := strings.ToLower(raw)
		if !httpStatusPattern.MatchString(v) {
			return HTTPStatusFilter{}, fmt.Errorf("http status %q: %w", raw, core.ErrInvalidValue)
		}
		switch {
		case v == "timeout":
			f.Timeout = true
		case strings.HasSuffix(v, "xx"):
			base := int(v[0]-'0') * 100
			f.Ranges = append(f.Ranges, HTTPStatusRange{Min: base, Max: base + 99})
		case strings.Contains(v, "-"):
			lo, hi, _ := strings.Cut(v, "-")
			minCode, _ := strconv.Atoi(strings.TrimSpace(lo))
			maxCode, _ := strconv.Atoi(strings.TrimSpace(hi))
			f.Ranges = append(f.Ranges, HTTPStatusRange{Min: minCode, Max: maxCode})
		default:
			code, _ := strconv.Atoi(v)
			f.Ranges = append(f.Ranges, HTTPStatusRange{Min: code, Max: code})
		}
	}
	return f, nil
}

// IsZero reports whether f keeps every delivery.
func (f HTTPStatusFilter) IsZero() bool { return len(f.Ranges) == 0 && !f.Timeout }

// Matches reports whether f keeps d.
func (f HTTPStatusFilter) Matches(d *Delivery) bool {
	if f.IsZero() {
		return true
	}
	if d.HTTPStatus == nil {
		return f.Timeout && d.Status == StatusFailed
	}
	for _, r := range f.Ranges {
		if *d.HTTPStatus >= r.Min && *d.HTTPStatus <= r.Max {
			return true
		}
	}
	return false
}
