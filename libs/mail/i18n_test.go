package mail

import (
	"strings"
	"testing"
)

func TestResolveLocale(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "en"},
		{"en", "en"},
		{"fr", "fr"},
		{"FR", "fr"},
		{"pt-BR", "pt-BR"},
		{"pt_br", "pt-BR"},
		{"zh-tw", "zh-TW"},
		{"de-AT", "de"},
		{"nb-NO", "nb"},
		{"pt", "en"},
		{"ja", "en"},
	}
	for _, tt := range tests {
		if got := ResolveLocale(tt.in); got != tt.want {
			t.Errorf("ResolveLocale(%q) = %q, want = %q", tt.in, got, tt.want)
		}
	}
}

func TestLocalizerT(t *testing.T) {
	c := newComposer(t)
	tests := []struct {
		locale, key string
		args        []any
		want        string
		wantErr     bool
	}{
		{"en", "email.invoice.finalized.subject", []any{"billing_entity_name", "ACME", "invoice_number", "1"}, "Your Invoice from ACME #1", false},
		{"fr", "email.invoice.finalized.invoice_number", nil, "Nº de facture", false},
		{"en", "email.payment_request.requested.payment_terms", []any{"count", 0}, "Our contractually agreed payment terms require payment upon invoice generation.", false},
		{"en", "email.payment_request.requested.payment_terms", []any{"count", 1}, "Our contractually agreed payment terms is 1 day.", false},
		{"en", "email.payment_request.requested.payment_terms", []any{"count", 2}, "Our contractually agreed payment terms are 2 days.", false},
		// Locales without system-email strings fall back to English.
		{"de", "email.password_reset.subject", nil, "Reset your Tanvo password", false},
		{"en", "email.does.not.exist", nil, "", true},
		{"en", "email.invoice.finalized.subject", []any{"billing_entity_name", "ACME"}, "", true},
		{"en", "email.payment_request.requested.payment_terms", nil, "", true},
		{"en", "email.invoice.finalized.subject", []any{"odd"}, "", true},
	}
	for _, tt := range tests {
		got, err := c.Localizer(tt.locale).T(tt.key, tt.args...)
		if (err != nil) != tt.wantErr {
			t.Errorf("T(%s, %s) error = %v, want error = %v", tt.locale, tt.key, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("T(%s, %s) = %q, want = %q", tt.locale, tt.key, got, tt.want)
		}
	}
}

// Every locale defines the strings of every document email in its own
// language, and no file still says Lago.
func TestLocaleFiles(t *testing.T) {
	c := newComposer(t)
	keys := []string{
		"email.invoice.finalized.subject", "email.credit_note.created.subject",
		"email.payment_receipt.created.subject", "email.payment_request.requested.subject",
		"date.formats.default", "money.format", "money.decimal_mark",
	}
	for _, l := range Locales() {
		for _, k := range keys {
			if _, ok := c.cat.lookup(l, k); !ok {
				t.Errorf("%s: missing %s", l, k)
			}
		}
		b, err := localeFS.ReadFile("locales/" + l + ".yaml")
		if err != nil {
			t.Fatalf("read %s: %v", l, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "Lago") {
				t.Errorf("%s.yaml:%d still mentions Lago", l, i+1)
			}
		}
	}
}
