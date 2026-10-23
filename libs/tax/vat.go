package tax

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

// vatPatterns are the syntaxes of VAT numbers by ISO country, after
// normalization (from valvat, MIT). Cyprus also rejects numbers starting
// with 12, which RE2 cannot express; see [ValidVATNumberSyntax].
var vatPatterns = map[string]*regexp.Regexp{
	"AT": regexp.MustCompile(`^ATU[0-9]{8}$`),
	"BE": regexp.MustCompile(`^BE[0-1][0-9]{9}$`),
	"BG": regexp.MustCompile(`^BG[0-9]{9,10}$`),
	"CY": regexp.MustCompile(`^CY[0-69][0-9]{7}[A-Z]$`),
	"CZ": regexp.MustCompile(`^CZ[0-9]{8,10}$`),
	"DE": regexp.MustCompile(`^DE[0-9]{9}$`),
	"DK": regexp.MustCompile(`^DK[0-9]{8}$`),
	"EE": regexp.MustCompile(`^EE10[0-9]{7}$`),
	"GR": regexp.MustCompile(`^EL[0-9]{9}$`),
	"ES": regexp.MustCompile(`^ES([A-Z][0-9]{8}|[0-9]{8}[A-Z]|[A-Z][0-9]{7}[A-Z])$`),
	"FI": regexp.MustCompile(`^FI[0-9]{8}$`),
	"FR": regexp.MustCompile(`^FR[A-HJ-NP-Z0-9]{2}[0-9]{9}$`),
	"GB": regexp.MustCompile(`^(GB|XI)([0-9]{9}|[0-9]{12}|(HA|GD)[0-9]{3})$`),
	"HR": regexp.MustCompile(`^HR[0-9]{11}$`),
	"HU": regexp.MustCompile(`^HU[0-9]{8}$`),
	"IE": regexp.MustCompile(`^IE([0-9][A-Z][0-9]{5}|[0-9]{7}[A-Z]?)[A-Z]$`),
	"IT": regexp.MustCompile(`^IT[0-9]{11}$`),
	"LT": regexp.MustCompile(`^LT([0-9]{7}1[0-9]|[0-9]{10}1[0-9])$`),
	"LU": regexp.MustCompile(`^LU[0-9]{8}$`),
	"LV": regexp.MustCompile(`^LV[0-9]{11}$`),
	"MT": regexp.MustCompile(`^MT[0-9]{8}$`),
	"NL": regexp.MustCompile(`^NL[0-9]{9}B[0-9]{2}$`),
	"PL": regexp.MustCompile(`^PL[0-9]{10}$`),
	"PT": regexp.MustCompile(`^PT[0-9]{9}$`),
	"RO": regexp.MustCompile(`^RO[1-9][0-9]{1,9}$`),
	"SE": regexp.MustCompile(`^SE[0-9]{10}01$`),
	"SI": regexp.MustCompile(`^SI[0-9]{8}$`),
	"SK": regexp.MustCompile(`^SK[0-9]{10}$`),
}

// vatCountryToISO maps VAT number prefixes that differ from the ISO code.
var vatCountryToISO = map[string]string{"EL": "GR", "XI": "GB"}

// NormalizeVATNumber upper-cases a VAT number and removes punctuation,
// symbols, control characters and whitespace.
func NormalizeVATNumber(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsControl(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, strings.ToUpper(s))
}

// VATNumberCountry returns the ISO country of a VAT number from its prefix
// (EL is Greece, XI Northern Ireland), when it is an EU member state or the
// United Kingdom.
func VATNumberCountry(s string) (string, bool) {
	n := NormalizeVATNumber(s)
	if len(n) < 3 || n[0] < 'A' || n[0] > 'Z' || n[1] < 'A' || n[1] > 'Z' {
		return "", false
	}
	cc := n[:2]
	if iso, ok := vatCountryToISO[cc]; ok {
		cc = iso
	}
	if _, ok := vatPatterns[cc]; !ok {
		return "", false
	}
	return cc, true
}

// ValidVATNumberSyntax reports whether a VAT number is well formed for the
// country its prefix names. It checks the syntax only: whether the number
// exists is VIES's to say.
func ValidVATNumberSyntax(s string) bool {
	cc, ok := VATNumberCountry(s)
	if !ok {
		return false
	}
	n := NormalizeVATNumber(s)
	if cc == "CY" && strings.HasPrefix(n, "CY12") {
		return false
	}
	return vatPatterns[cc].MatchString(n)
}

// VIESErrorType classifies a failed VIES check, deciding how it is retried
// and reported.
type VIESErrorType string

// VIES error types.
const (
	VIESErrorRateLimit              VIESErrorType = "rate_limit"
	VIESErrorTimeout                VIESErrorType = "timeout"
	VIESErrorBlocked                VIESErrorType = "blocked"
	VIESErrorInvalidRequester       VIESErrorType = "invalid_requester"
	VIESErrorServiceUnavailable     VIESErrorType = "service_unavailable"
	VIESErrorMemberStateUnavailable VIESErrorType = "member_state_unavailable"
	VIESErrorUnknown                VIESErrorType = "unknown"
)

var viesFaults = map[string]VIESErrorType{
	"SERVICE_UNAVAILABLE":            VIESErrorServiceUnavailable,
	"MS_UNAVAILABLE":                 VIESErrorMemberStateUnavailable,
	"INVALID_REQUESTER_INFO":         VIESErrorInvalidRequester,
	"TIMEOUT":                        VIESErrorTimeout,
	"VAT_BLOCKED":                    VIESErrorBlocked,
	"IP_BLOCKED":                     VIESErrorBlocked,
	"GLOBAL_MAX_CONCURRENT_REQ":      VIESErrorRateLimit,
	"GLOBAL_MAX_CONCURRENT_REQ_TIME": VIESErrorRateLimit,
	"MS_MAX_CONCURRENT_REQ":          VIESErrorRateLimit,
	"MS_MAX_CONCURRENT_REQ_TIME":     VIESErrorRateLimit,
}

// VIESFault classifies a VIES fault string. INVALID_INPUT is no failure but
// an invalid number, reported with ok false; any other fault is a failure
// of the returned type, unknown faults being [VIESErrorUnknown]. A transport
// failure (HTTP error) is [VIESErrorServiceUnavailable].
func VIESFault(fault string) (t VIESErrorType, ok bool) {
	if fault == "INVALID_INPUT" {
		return "", false
	}
	if t, found := viesFaults[fault]; found {
		return t, true
	}
	return VIESErrorUnknown, true
}

var viesRetryDelays = []time.Duration{5 * time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute}

// viesMaxRetryDelay caps the delay between VIES retries.
const viesMaxRetryDelay = time.Hour

// VIESRetryDelay is how long to wait before retrying a VIES check that
// failed attempts times: 5, 5, 10, 20 and 40 minutes, then hourly.
func VIESRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts <= len(viesRetryDelays) {
		return viesRetryDelays[attempts-1]
	}
	return viesMaxRetryDelay
}
