package documents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
)

// MentionLocalizer renders the raw, locale-independent mention variables
// of a quote version as the display strings of a document locale (lago
// QuoteVersions::MentionVariablesLocalizer). It is safe for concurrent
// use.
//
// source: app/services/quote_versions/mention_variables_localizer.rb:9-75
type MentionLocalizer struct {
	cat *i18n.Catalog
}

// NewMentionLocalizer loads the translations of the localizer.
func NewMentionLocalizer() (*MentionLocalizer, error) {
	cat, err := i18n.Load()
	if err != nil {
		return nil, fmt.Errorf("documents: %w", err)
	}
	return &MentionLocalizer{cat: cat}, nil
}

// Localize returns the mention variables raw, a JSON object, in locale:
// the dates (quote_date, commercial_terms_start_date) in the locale's
// default date format, the term duration and the payment terms
// translated, and the billing entity address formatted for its country,
// one line per row. Every other key passes through unchanged; a blank
// locale-sensitive value is null. An unknown locale is English.
func (m *MentionLocalizer) Localize(raw json.RawMessage, locale string) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("{}"), nil
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("documents: decode mention variables: %w", err)
	}
	l := m.cat.Locale(locale)
	out := make(map[string]any, len(in))
	for k, v := range in {
		var (
			s   *string
			err error
		)
		switch k {
		case "commercial_terms_term_duration":
			s, err = termDuration(l, v)
		case "commercial_terms_payment_terms":
			s, err = paymentTerms(l, v)
		case "billing_entity_address":
			s, err = mentionAddress(l, v)
		case "quote_date", "commercial_terms_start_date":
			s, err = mentionDate(l, v)
		default:
			out[k] = v
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("documents: mention variable %s: %w", k, err)
		}
		out[k] = s
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("documents: encode mention variables: %w", err)
	}
	return b, nil
}

// isNull reports a JSON null or an absent value.
func isNull(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// mentionDate formats an ISO 8601 date (I18n.l(date, format: :default)).
func mentionDate(l *i18n.Locale, v json.RawMessage) (*string, error) {
	if isNull(v) {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil, err
	}
	out := l.Date(core.Date{Year: t.Year(), Month: t.Month(), Day: t.Day()})
	return &out, nil
}

// termDuration translates {unit, count}
// (quote_version.mention_variables.term_duration.<unit>).
func termDuration(l *i18n.Locale, v json.RawMessage) (*string, error) {
	if isNull(v) {
		return nil, nil
	}
	var d struct {
		Unit  string `json:"unit"`
		Count *int64 `json:"count"`
	}
	if err := json.Unmarshal(v, &d); err != nil {
		return nil, err
	}
	if d.Unit == "" && d.Count == nil {
		return nil, nil
	}
	var n int64
	if d.Count != nil {
		n = *d.Count
	}
	out := l.Count("quote_version.mention_variables.term_duration."+d.Unit, n)
	return &out, nil
}

// paymentTerms translates the net payment term in days
// (quote_version.mention_variables.payment_terms).
func paymentTerms(l *i18n.Locale, v json.RawMessage) (*string, error) {
	if isNull(v) {
		return nil, nil
	}
	var n int64
	if err := json.Unmarshal(v, &n); err != nil {
		return nil, err
	}
	out := l.Count("quote_version.mention_variables.payment_terms", n)
	return &out, nil
}

// mentionAddress formats the billing entity address with the addressing
// gem's DefaultFormatter in l, without HTML; null when it has no line.
func mentionAddress(l *i18n.Locale, v json.RawMessage) (*string, error) {
	if isNull(v) {
		return nil, nil
	}
	var a struct {
		Line1   *string `json:"address_line1"`
		Line2   *string `json:"address_line2"`
		City    *string `json:"locality"`
		Zipcode *string `json:"postal_code"`
		State   *string `json:"administrative_area"`
		Country *string `json:"country_code"`
	}
	if err := json.Unmarshal(v, &a); err != nil {
		return nil, err
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	addr := core.Address{
		Line1: str(a.Line1), Line2: str(a.Line2), City: str(a.City),
		Zipcode: str(a.Zipcode), State: str(a.State), Country: str(a.Country),
	}
	if addr == (core.Address{}) {
		return nil, nil
	}
	lines := l.FormatAddress(addr, l.Tag())
	if len(lines) == 0 {
		return nil, nil
	}
	out := strings.Join(lines, "\n")
	return &out, nil
}
