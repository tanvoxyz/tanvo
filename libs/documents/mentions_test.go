package documents_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/documents"
)

// TestLocalizeMentionVariables ports QuoteVersions::MentionVariablesLocalizer.
func TestLocalizeMentionVariables(t *testing.T) {
	m, err := documents.NewMentionLocalizer()
	if err != nil {
		t.Fatal(err)
	}
	address := `{"billing_entity_address": {"address_line1": "4 rue de la Paix", "address_line2": null, "locality": "Paris",
		"postal_code": "75002", "administrative_area": null, "country_code": "FR"}}`
	tests := []struct {
		name, source, locale, in, want string
	}{
		{
			name: "dates", source: "spec/services/quote_versions/mention_variables_localizer_spec.rb:16", locale: "en",
			in:   `{"quote_date": "2026-04-01", "commercial_terms_start_date": "2026-04-01"}`,
			want: `{"quote_date": "Apr 01, 2026", "commercial_terms_start_date": "Apr 01, 2026"}`,
		},
		{
			name: "dates in French", source: "spec/services/quote_versions/mention_variables_localizer_spec.rb:26", locale: "fr",
			in:   `{"quote_date": "2026-04-01", "commercial_terms_start_date": "2026-04-01"}`,
			want: `{"quote_date": "1 avr. 2026", "commercial_terms_start_date": "1 avr. 2026"}`,
		},
		{
			name: "term duration", source: "spec/services/quote_versions/mention_variables_localizer_spec.rb:38", locale: "en",
			in: `{"commercial_terms_term_duration": {"unit": "years", "count": 1}}`, want: `{"commercial_terms_term_duration": "1 year"}`,
		},
		{
			name: "term duration in French", source: "spec/services/quote_versions/mention_variables_localizer_spec.rb:46", locale: "fr",
			in: `{"commercial_terms_term_duration": {"unit": "years", "count": 1}}`, want: `{"commercial_terms_term_duration": "un an"}`,
		},
		{
			name: "plural term duration", locale: "en",
			in: `{"commercial_terms_term_duration": {"unit": "months", "count": 6}}`, want: `{"commercial_terms_term_duration": "6 months"}`,
		},
		{
			name: "term duration without plural forms", locale: "zh-TW",
			in: `{"commercial_terms_term_duration": {"unit": "months", "count": 6}}`, want: `{"commercial_terms_term_duration": "6個月"}`,
		},
		{
			name: "term duration in zh-CN", locale: "zh-CN",
			in: `{"commercial_terms_term_duration": {"unit": "months", "count": 1}}`, want: `{"commercial_terms_term_duration": "1个月"}`,
		},
		{
			name: "payment terms", source: "spec/services/quote_versions/mention_variables_localizer_spec.rb:54", locale: "en",
			in: `{"commercial_terms_payment_terms": 30}`, want: `{"commercial_terms_payment_terms": "Net 30"}`,
		},
		{
			name: "billing address", source: "spec/services/quote_versions/mention_variables_localizer_spec.rb:73", locale: "en",
			in: address, want: `{"billing_entity_address": "4 rue de la Paix\n75002 Paris\nFrance"}`,
		},
		{
			name: "pass through", source: "spec/services/quote_versions/mention_variables_localizer_spec.rb:81", locale: "en",
			in:   `{"customer_name": "Hooli Inc - Gavin Belson", "quote_currency": "EUR"}`,
			want: `{"customer_name": "Hooli Inc - Gavin Belson", "quote_currency": "EUR"}`,
		},
		{
			name: "partial dictionary", source: "spec/services/quote_versions/mention_variables_localizer_spec.rb:89", locale: "en",
			in: `{"customer_name": "Hooli Inc - Gavin Belson"}`, want: `{"customer_name": "Hooli Inc - Gavin Belson"}`,
		},
		{
			name: "blank values", source: "spec/services/quote_versions/mention_variables_localizer_spec.rb:104", locale: "en",
			in: `{"commercial_terms_start_date": null, "commercial_terms_term_duration": null,
				"commercial_terms_payment_terms": null, "billing_entity_address": null}`,
			want: `{"commercial_terms_start_date": null, "commercial_terms_term_duration": null,
				"commercial_terms_payment_terms": null, "billing_entity_address": null}`,
		},
		{
			name: "unknown locale is English", locale: "xx",
			in: `{"commercial_terms_payment_terms": 15}`, want: `{"commercial_terms_payment_terms": "Net 15"}`,
		},
		{name: "empty snapshot", locale: "en", in: ``, want: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			got, err := m.Localize(json.RawMessage(tt.in), tt.locale)
			if err != nil {
				t.Fatal(err)
			}
			var g, w any
			if err := json.Unmarshal(got, &g); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.want), &w); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g, w) {
				t.Errorf("got = %s, want = %s", got, tt.want)
			}
		})
	}
	if _, err := m.Localize(json.RawMessage(`{"quote_date": "April 1"}`), "en"); err == nil {
		t.Errorf("got = nil, want = an error for an invalid date")
	}
}
