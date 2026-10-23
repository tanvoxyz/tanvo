package core

import (
	"slices"
	"strings"
)

// Address is the postal address value object shared by organizations,
// billing entities and customers (domain §4.1). Every field is optional;
// Country, when set, is an ISO 3166-1 alpha-2 code.
type Address struct {
	// Line1 is the first address line.
	Line1 string
	// Line2 is the second address line.
	Line2 string
	// City is the city or locality.
	City string
	// State is the state, province or region.
	State string
	// Zipcode is the postal code.
	Zipcode string
	// Country is the ISO 3166-1 alpha-2 country code, upper case.
	Country string
}

// Normalize trims every field and upper-cases the country, as the address
// is stored.
func (a Address) Normalize() Address {
	return Address{
		Line1:   strings.TrimSpace(a.Line1),
		Line2:   strings.TrimSpace(a.Line2),
		City:    strings.TrimSpace(a.City),
		State:   strings.TrimSpace(a.State),
		Zipcode: strings.TrimSpace(a.Zipcode),
		Country: strings.ToUpper(strings.TrimSpace(a.Country)),
	}
}

// IsZero reports whether every field of a is empty.
func (a Address) IsZero() bool { return a == Address{} }

// Validate checks the country code and the length of each line (255).
func (a Address) Validate() error {
	var v Validator
	v.MaxLen("line1", a.Line1, 255)
	v.MaxLen("line2", a.Line2, 255)
	v.MaxLen("city", a.City, 255)
	v.MaxLen("state", a.State, 255)
	v.MaxLen("zipcode", a.Zipcode, 255)
	if a.Country != "" {
		v.Check(ValidCountry(a.Country), "country", CodeInvalidCountry)
	}
	return v.Err()
}

// WithFallback returns a with each empty field taken from fallback; used to
// print a shipping address that falls back to the billing address field by
// field (domain §6).
func (a Address) WithFallback(fallback Address) Address {
	pick := func(s, f string) string {
		if s == "" {
			return f
		}
		return s
	}
	return Address{
		Line1:   pick(a.Line1, fallback.Line1),
		Line2:   pick(a.Line2, fallback.Line2),
		City:    pick(a.City, fallback.City),
		State:   pick(a.State, fallback.State),
		Zipcode: pick(a.Zipcode, fallback.Zipcode),
		Country: pick(a.Country, fallback.Country),
	}
}

// ValidCountry reports whether code is an assigned ISO 3166-1 alpha-2 code
// (upper case).
func ValidCountry(code string) bool {
	_, ok := slices.BinarySearch(countryCodes(), code)
	return ok
}

// IsEUCountry reports whether code is a member state of the European Union
// (for EU tax management, domain §4.2 and §22.10).
func IsEUCountry(code string) bool {
	switch strings.ToUpper(code) {
	case "AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR", "DE", "GR", "HU", "IE",
		"IT", "LV", "LT", "LU", "MT", "NL", "PL", "PT", "RO", "SK", "SI", "ES", "SE":
		return true
	default:
		return false
	}
}

// countryCodes returns the sorted ISO 3166-1 alpha-2 codes.
func countryCodes() []string {
	return []string{
		"AD", "AE", "AF", "AG", "AI", "AL", "AM", "AO", "AQ", "AR", "AS", "AT",
		"AU", "AW", "AX", "AZ", "BA", "BB", "BD", "BE", "BF", "BG", "BH", "BI",
		"BJ", "BL", "BM", "BN", "BO", "BQ", "BR", "BS", "BT", "BV", "BW", "BY",
		"BZ", "CA", "CC", "CD", "CF", "CG", "CH", "CI", "CK", "CL", "CM", "CN",
		"CO", "CR", "CU", "CV", "CW", "CX", "CY", "CZ", "DE", "DJ", "DK", "DM",
		"DO", "DZ", "EC", "EE", "EG", "EH", "ER", "ES", "ET", "FI", "FJ", "FK",
		"FM", "FO", "FR", "GA", "GB", "GD", "GE", "GF", "GG", "GH", "GI", "GL",
		"GM", "GN", "GP", "GQ", "GR", "GS", "GT", "GU", "GW", "GY", "HK", "HM",
		"HN", "HR", "HT", "HU", "ID", "IE", "IL", "IM", "IN", "IO", "IQ", "IR",
		"IS", "IT", "JE", "JM", "JO", "JP", "KE", "KG", "KH", "KI", "KM", "KN",
		"KP", "KR", "KW", "KY", "KZ", "LA", "LB", "LC", "LI", "LK", "LR", "LS",
		"LT", "LU", "LV", "LY", "MA", "MC", "MD", "ME", "MF", "MG", "MH", "MK",
		"ML", "MM", "MN", "MO", "MP", "MQ", "MR", "MS", "MT", "MU", "MV", "MW",
		"MX", "MY", "MZ", "NA", "NC", "NE", "NF", "NG", "NI", "NL", "NO", "NP",
		"NR", "NU", "NZ", "OM", "PA", "PE", "PF", "PG", "PH", "PK", "PL", "PM",
		"PN", "PR", "PS", "PT", "PW", "PY", "QA", "RE", "RO", "RS", "RU", "RW",
		"SA", "SB", "SC", "SD", "SE", "SG", "SH", "SI", "SJ", "SK", "SL", "SM",
		"SN", "SO", "SR", "SS", "ST", "SV", "SX", "SY", "SZ", "TC", "TD", "TF",
		"TG", "TH", "TJ", "TK", "TL", "TM", "TN", "TO", "TR", "TT", "TV", "TW",
		"TZ", "UA", "UG", "UM", "US", "UY", "UZ", "VA", "VC", "VE", "VG", "VI",
		"VN", "VU", "WF", "WS", "YE", "YT", "ZA", "ZM", "ZW",
	}
}
