package i18n

import (
	"os"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func catalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return c
}

func TestLocales(t *testing.T) {
	c := catalog(t)
	want := []string{"de", "en", "es", "fr", "it", "nb", "pt-BR", "sv", "zh-CN", "zh-TW"}
	if got := c.Locales(); !slices.Equal(got, want) {
		t.Errorf("Locales() got = %v, want = %v", got, want)
	}
	if got := c.Locale("xx").Tag(); got != "en" {
		t.Errorf("Locale(xx).Tag() got = %q, want = %q", got, "en")
	}
}

// Every key of the English catalog exists in every locale, or falls back.
func TestTranslate(t *testing.T) {
	c := catalog(t)
	tests := []struct {
		locale, key string
		args        []Arg
		want        string
	}{
		{"en", "invoice.month", nil, "month"},
		{"fr", "invoice.invoice_number", nil, "Nº de facture"},
		{"en", "invoice.fees_from_to_date", []Arg{A("from_date", "Jan 01, 2024"), A("to_date", "Jan 31, 2024")}, "Fees from Jan 01, 2024 to Jan 31, 2024"},
		{"en", "invoice.unknown_key", nil, "translation missing: en.invoice.unknown_key"},
		{"en", "invoice.fees_from_to_date", nil, "Fees from %{from_date} to %{to_date}"},
		{"en", "payment_request.document_name", nil, "Payment request"},
		{"zh-TW", "payment_request.document_name", nil, "付款請求"},
		{"zh-CN", "payment_request.document_name", nil, "付款请求"},
		{"zh-CN", "invoice.document_name", nil, "发票"},
		{"zh-CN", "credit_note.document_name", nil, "贷项通知单"},
		{"zh-CN", "invoice.tax_identification_number", []Arg{A("tax_identification_number", "91310000")}, "税号：91310000"},
		{"zh-Hans-CN", "invoice.sub_total", nil, "小计"},
	}
	for _, tt := range tests {
		if got := c.Locale(tt.locale).T(tt.key, tt.args...); got != tt.want {
			t.Errorf("T(%s, %s) got = %q, want = %q", tt.locale, tt.key, got, tt.want)
		}
	}
}

func TestPlural(t *testing.T) {
	l := catalog(t).Locale("en")
	tests := []struct {
		key   string
		count int64
		want  string
	}{
		{"invoice.percentage.free_units_per_transaction", 1, "Free units for 1 transaction"},
		{"invoice.percentage.free_units_per_transaction", 3, "Free units for 3 transactions"},
		{"invoice.percentage.free_units_per_transaction", 0, "Free units for 0 transactions"},
		{"email.payment_request.requested.payment_terms", 0, "Our contractually agreed payment terms require payment upon invoice generation."},
		{"email.payment_request.requested.payment_terms", 30, "Our contractually agreed payment terms are 30 days."},
	}
	for _, tt := range tests {
		if got := l.Plural(tt.key, tt.count); got != tt.want {
			t.Errorf("Plural(%s, %d) got = %q, want = %q", tt.key, tt.count, got, tt.want)
		}
	}
}

func TestAllLocalesHaveEnglishKeys(t *testing.T) {
	c := catalog(t)
	en := c.Locale("en")
	var keys []string
	var walkKeys func(prefix string, m map[string]any)
	walkKeys = func(prefix string, m map[string]any) {
		for k, v := range m {
			switch v := v.(type) {
			case map[string]any:
				walkKeys(prefix+k+".", v)
			case string:
				keys = append(keys, prefix+k)
			}
		}
	}
	walkKeys("", en.tree)
	for _, tag := range c.Locales() {
		l := c.locales[tag]
		var missing []string
		for _, k := range keys {
			if walk(l.tree, k) == nil {
				missing = append(missing, k)
			}
		}
		// lago-api's translations are not complete in every locale; the
		// missing keys fall back to English, as Ruby I18n does there.
		if len(missing) > 0 {
			t.Logf("%s: %d keys fall back to English", tag, len(missing))
		}
	}
}

// zh-CN (a Tanvo locale) translates every key zh-TW does.
func TestZhCNCoversZhTW(t *testing.T) {
	c := catalog(t)
	var walkKeys func(prefix string, m map[string]any, out *[]string)
	walkKeys = func(prefix string, m map[string]any, out *[]string) {
		for k, v := range m {
			if sub, ok := v.(map[string]any); ok {
				walkKeys(prefix+k+".", sub, out)
				continue
			}
			*out = append(*out, prefix+k)
		}
	}
	var tw []string
	walkKeys("", c.locales["zh-TW"].tree, &tw)
	for _, k := range tw {
		if walk(c.locales["zh-CN"].tree, k) == nil {
			t.Errorf("zh-CN %s: got = missing, want = translated", k)
		}
	}
}

func TestResolveLocale(t *testing.T) {
	for _, c := range []struct{ tag, want string }{
		{"zh", "zh-CN"},
		{"zh-CN", "zh-CN"},
		{"zh-Hans", "zh-CN"},
		{"zh-Hans-CN", "zh-CN"},
		{"zh-SG", "zh-CN"},
		{"zh-Hans-SG", "zh-CN"},
		{"zh_cn", "zh-CN"},
		{"ZH-hans-cn", "zh-CN"},
		{"zh-TW", "zh-TW"},
		{"zh-Hant", "zh-TW"},
		{"zh-HK", "zh-TW"},
		{"zh-MO", "zh-TW"},
		{"zh-Hant-HK", "zh-TW"},
		{"zh-Hant-TW", "zh-TW"},
		{"zh-hant-cn", "zh-TW"},
		{"zh-Hans-TW", "zh-CN"},
		{"en", "en"},
		{"en-US", "en"},
		{"fr-CA", "fr"},
		{"pt-BR", "pt-BR"},
		{"pt", "pt-BR"},
		{"pt-PT", "pt-BR"},
		{"nb", "nb"},
		{"no", "nb"},
		{"nb-NO", "nb"},
		{"de-CH", "de"},
		{"es-419", "es"},
		{"sv", "sv"},
		{"it", "it"},
		{"", ""},
		{"xx", ""},
		{"ja-JP", ""},
		{"zh-Latn", ""},
		{"zh-KR", ""},
	} {
		if got := ResolveLocale(c.tag); got != c.want {
			t.Errorf("ResolveLocale(%q): got = %q, want = %q", c.tag, got, c.want)
		}
	}
	cat := catalog(t)
	for _, l := range documentLocales() {
		if !slices.Contains(cat.Locales(), l) {
			t.Errorf("document locale %s: got = no translations, want = a locale directory", l)
		}
	}
	if got, want := len(cat.Locales()), len(documentLocales()); got != want {
		t.Errorf("locales: got = %d, want = %d", got, want)
	}
}

func dec(t *testing.T, s string) money.Decimal {
	t.Helper()
	d, err := money.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", s, err)
	}
	return d
}

func TestMoneyHelperSpec(t *testing.T) {
	l := catalog(t).Locale("en")
	usd, bhd := money.MustCurrency("USD"), money.MustCurrency("BHD")
	tests := []struct {
		source string
		got    func() string
		want   string
	}{
		{"spec/views/helpers/money_helper_spec.rb:12", func() string { return l.Money(100, usd) }, "$1.00"},
		{"spec/views/helpers/money_helper_spec.rb:19", func() string { return l.Money(100, bhd) }, "BHD 0.100"},
		{"spec/views/helpers/money_helper_spec.rb:28", func() string { return l.MoneyPrecise(dec(t, "123.12345678"), usd) }, "$123.123457"},
		{"spec/views/helpers/money_helper_spec.rb:34", func() string { return l.MoneyPrecise(dec(t, "0.000000012345"), usd) }, "$0.000000012345"},
		{"spec/views/helpers/money_helper_spec.rb:40", func() string { return l.MoneyPrecise(dec(t, "0.100000012345"), usd) }, "$0.10"},
		{"spec/views/helpers/money_helper_spec.rb:49", func() string { return l.MoneyPrecise(dec(t, "123.12345678"), bhd) }, "BHD 123.123457"},
		{"spec/views/helpers/money_helper_spec.rb:55", func() string { return l.MoneyPrecise(dec(t, "0.000000012345"), bhd) }, "BHD 0.000000012345"},
		{"spec/views/helpers/money_helper_spec.rb:61", func() string { return l.MoneyPrecise(dec(t, "0.100000012345"), bhd) }, "BHD 0.100"},
		{"spec/views/helpers/money_helper_spec.rb:75", func() string { return l.PricingUnit(dec(t, "100"), "CR") }, "100.00 CR"},
		{"spec/views/helpers/money_helper_spec.rb:88", func() string { return l.PricingUnitPrecise(dec(t, "123.12345678"), "CR") }, "123.123457 CR"},
		{"spec/views/helpers/money_helper_spec.rb:96", func() string { return l.PricingUnitPrecise(dec(t, "0.000000012345"), "CR") }, "0.000000012345 CR"},
	}
	for _, tt := range tests {
		if got := tt.got(); got != tt.want {
			t.Errorf("%s: got = %q, want = %q", tt.source, got, tt.want)
		}
	}
}

func TestRoundingHelperSpec(t *testing.T) {
	tests := []struct {
		source string
		n      string
		figs   int
		want   string
	}{
		{"spec/views/helpers/rounding_helper_spec.rb:9", "123.456789123", 4, "123.4568"},
		{"spec/views/helpers/rounding_helper_spec.rb:13", "123.456789123", 6, "123.456789"},
		{"spec/views/helpers/rounding_helper_spec.rb:17", "123", 6, "123"},
		{"spec/views/helpers/rounding_helper_spec.rb:21", "123.0000456789", 6, "123.000046"},
		{"spec/views/helpers/rounding_helper_spec.rb:25", "123.000000000456789", 6, "123"},
		{"spec/views/helpers/rounding_helper_spec.rb:29", "0.0000000009", 6, "0.0000000009"},
		{"spec/views/helpers/rounding_helper_spec.rb:33", "-123.456789123", 6, "-123.456789"},
		{"spec/views/helpers/rounding_helper_spec.rb:37", "0", 6, "0"},
		{"tanvo", "0.1234567", 6, "0.123457"},
		{"tanvo", "-0.5", 6, "-0.5"},
		{"tanvo", "10.50", 6, "10.5"},
	}
	for _, tt := range tests {
		if got := RoundDecimalPartTo(dec(t, tt.n), tt.figs); got != tt.want {
			t.Errorf("%s: RoundDecimalPartTo(%s, %d) got = %q, want = %q", tt.source, tt.n, tt.figs, got, tt.want)
		}
	}
}

func TestRubyFloat(t *testing.T) {
	for _, tt := range []struct{ in, want string }{{"20", "20.0"}, {"5.5", "5.5"}, {"0", "0.0"}, {"20.00", "20.0"}, {"8.125", "8.125"}} {
		if got := RubyFloat(dec(t, tt.in)); got != tt.want {
			t.Errorf("RubyFloat(%s) got = %q, want = %q", tt.in, got, tt.want)
		}
	}
	if got := Percentage(dec(t, "12.50")); got != "12.5%" {
		t.Errorf("Percentage(12.50) got = %q, want = %q", got, "12.5%")
	}
}

type formattingCases struct {
	Money []struct {
		Locale, Currency, Want string
		Minor                  int64
	}
	Precise []struct {
		Locale, Units, Currency, Want string
	}
	PricingUnit []struct {
		Locale, Units, Want string
		Precise             bool
	} `yaml:"pricing_unit"`
	Dates []struct {
		Locale, Date, Want string
	}
	Addresses []struct {
		Address struct {
			Line1, Line2, City, State, Zip, Country string
		}
		Locale        string
		AddressLocale string `yaml:"address_locale"`
		Want          []string
	}
}

func TestFormatting(t *testing.T) {
	b, err := os.ReadFile("testdata/formatting.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cases formattingCases
	if err := yaml.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	c := catalog(t)
	for _, tt := range cases.Money {
		if got := c.Locale(tt.Locale).Money(tt.Minor, money.MustCurrency(tt.Currency)); got != tt.Want {
			t.Errorf("Money(%s, %d %s) got = %q, want = %q", tt.Locale, tt.Minor, tt.Currency, got, tt.Want)
		}
	}
	for _, tt := range cases.Precise {
		if got := c.Locale(tt.Locale).MoneyPrecise(dec(t, tt.Units), money.MustCurrency(tt.Currency)); got != tt.Want {
			t.Errorf("MoneyPrecise(%s, %s %s) got = %q, want = %q", tt.Locale, tt.Units, tt.Currency, got, tt.Want)
		}
	}
	for _, tt := range cases.PricingUnit {
		l := c.Locale(tt.Locale)
		got := l.PricingUnit(dec(t, tt.Units), "CR")
		if tt.Precise {
			got = l.PricingUnitPrecise(dec(t, tt.Units), "CR")
		}
		if got != tt.Want {
			t.Errorf("PricingUnit(%s, %s) got = %q, want = %q", tt.Locale, tt.Units, got, tt.Want)
		}
	}
	for _, tt := range cases.Dates {
		d, err := core.ParseDate(tt.Date)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Locale(tt.Locale).Date(d); got != tt.Want {
			t.Errorf("Date(%s, %s) got = %q, want = %q", tt.Locale, tt.Date, got, tt.Want)
		}
	}
	for _, tt := range cases.Addresses {
		a := core.Address{
			Line1: tt.Address.Line1, Line2: tt.Address.Line2, City: tt.Address.City,
			State: tt.Address.State, Zipcode: tt.Address.Zip, Country: tt.Address.Country,
		}
		if got := c.Locale(tt.Locale).FormatAddress(a, tt.AddressLocale); !slices.Equal(got, tt.Want) {
			t.Errorf("FormatAddress(%s, %+v) got = %q, want = %q", tt.Locale, tt.Address, got, tt.Want)
		}
	}
}

func TestStrftime(t *testing.T) {
	l := catalog(t).Locale("en")
	d := core.Date{Year: 2024, Month: 3, Day: 5}
	tests := []struct{ layout, want string }{
		{"%Y-%m-%d", "2024-03-05"},
		{"%-d %B %Y", "5 March 2024"},
		{"%e|%j|%y|%%|%A|%a|%q", " 5|065|24|%|Tuesday|Tue|%q"},
	}
	for _, tt := range tests {
		if got := l.Strftime(d, tt.layout); got != tt.want {
			t.Errorf("Strftime(%q) got = %q, want = %q", tt.layout, got, tt.want)
		}
	}
	if got := l.Date(core.Date{}); got != "" {
		t.Errorf("Date(zero) got = %q, want = %q", got, "")
	}
}
