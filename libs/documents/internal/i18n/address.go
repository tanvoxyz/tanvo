package i18n

import (
	"regexp"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// genericAddressFormat is the addressing gem's format of countries it does not
// define.
const genericAddressFormat = "%given_name %family_name\n%organization\n%address_line1\n%address_line2\n%address_line3\n%locality"

var (
	placeholderPattern = regexp.MustCompile(`%[a-z1-9_]+`)
	edgePunctuation    = regexp.MustCompile(`\A[ \-,]+|[ \-,]+\z`)
	multipleSpaces     = regexp.MustCompile(`\s\s+`)
)

// FormatAddress formats a customer's postal address as the addressing gem's
// DefaultFormatter does in lago-api's invoices: the country's format with the
// country name (in l's language) last, or first followed by the country's
// local format when addressLocale is the country's own language, without
// empty lines. addressLocale is the customer's own document locale, empty
// when the customer has none. The subdivision codes of the gem's
// administrative areas are not resolved: the state is printed as stored.
func (l *Locale) FormatAddress(a core.Address, addressLocale string) []string {
	cc := strings.ToUpper(a.Country)
	f, ok := l.catalog.countries.AddressFormats[cc]
	if !ok {
		f = addressFormat{Format: genericAddressFormat}
	}
	var layout string
	if f.LocalFormat != "" && localeMatches(f.Locale, addressLocale) {
		layout = "%country\n" + f.LocalFormat
	} else {
		layout = f.Format + "\n%country"
	}
	country := cc
	if name, ok := l.catalog.countries.Names[l.tag][cc]; ok {
		country = name
	}
	values := map[string]string{
		"%address_line1":       a.Line1,
		"%address_line2":       a.Line2,
		"%locality":            a.City,
		"%postal_code":         a.Zipcode,
		"%administrative_area": a.State,
		"%country":             country,
	}
	out := placeholderPattern.ReplaceAllStringFunc(layout, func(m string) string { return values[m] })
	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(edgePunctuation.ReplaceAllString(line, ""))
		line = multipleSpaces.ReplaceAllString(line, " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// localeMatches reports whether an address format's locale and a document
// locale share a CLDR candidate (Addressing::Locale.match_candidates). Among
// lago-api's document locales only zh-TW (zh-Hant-TW) shares one, with
// zh-Hant; Tanvo's zh-CN (zh-Hans-CN) shares zh-Hans, so Chinese customers
// of zh-CN documents get the Chinese address order.
func localeMatches(formatLocale, documentLocale string) bool {
	if formatLocale == "" || documentLocale == "" {
		return false
	}
	return formatLocale == "zh-Hant" && documentLocale == "zh-TW" ||
		formatLocale == "zh-Hans" && documentLocale == "zh-CN"
}

// CountryName returns the name of the ISO 3166-1 alpha-2 country code in l's
// language, or the code when it is unknown.
func (l *Locale) CountryName(code string) string {
	cc := strings.ToUpper(code)
	if name, ok := l.catalog.countries.Names[l.tag][cc]; ok {
		return name
	}
	return code
}

// CommonCountryName returns the English common name of a country (the
// countries gem's common_name, used for billing entity addresses whatever
// the document locale), or the code when it is unknown.
func (c *Catalog) CommonCountryName(code string) string {
	if name, ok := c.countries.CommonNames[strings.ToUpper(code)]; ok {
		return name
	}
	return code
}
