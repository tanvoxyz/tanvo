package tax

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// VIESEndpoint is the SOAP endpoint of the EU VIES service.
const VIESEndpoint = "https://ec.europa.eu/taxation_customs/vies/services/checkVatService"

// viesTimeout bounds a VIES request when [VIESClient.HTTP] is nil.
const viesTimeout = time.Minute

// VIESLookup is VIES's answer for a VAT number it knows, as sent in the
// customer.vies_check webhook.
type VIESLookup struct {
	// CountryCode is the country VIES registered the number in (EL for
	// Greece, XI for Northern Ireland).
	CountryCode string `json:"country_code"`
	// VATNumber is the number without its country prefix.
	VATNumber string `json:"vat_number"`
	// RequestDate is the date of the check (YYYY-MM-DD).
	RequestDate string `json:"request_date,omitempty"`
	// Valid is always true: VIES knows the number.
	Valid bool `json:"valid"`
	// Name is the registered name, nil when VIES does not disclose it.
	Name *string `json:"name"`
	// Address is the registered address, nil when VIES does not disclose
	// it.
	Address *string `json:"address"`
}

// VIESError is a VIES check that failed: a VIES fault, or an HTTP status
// other than 2xx ([VIESErrorServiceUnavailable]). An unknown fault is
// [VIESErrorUnknown].
type VIESError struct {
	// Type classifies the failure.
	Type VIESErrorType
	// Message describes it: "The VIES web service returned the error:
	// <fault or HTTP status>".
	Message string
}

func (e *VIESError) Error() string { return e.Message }

func newVIESError(t VIESErrorType, cause string) *VIESError {
	return &VIESError{Type: t, Message: "The VIES web service returned the error: " + cause}
}

// VIESChecker checks VAT numbers with VIES.
type VIESChecker interface {
	// CheckVAT returns VIES's lookup of vat when VIES knows it. It returns
	// nil without error when vat is malformed ([ValidVATNumberSyntax]),
	// is a United Kingdom (GB) number, or is unknown to VIES; a
	// [*VIESError] when VIES failed; another error when VIES could not be
	// reached.
	CheckVAT(ctx context.Context, vat string) (*VIESLookup, error)
}

// VIESClient is the [VIESChecker] over the VIES SOAP web service. It
// follows no redirect: a redirect is an HTTP error.
//
// source: valvat 2.0.1 lib/valvat/lookup.rb, lib/valvat/lookup/vies.rb
type VIESClient struct {
	// HTTP sends the requests; nil uses a client with a one-minute
	// timeout.
	HTTP *http.Client
	// Endpoint is the service URL; empty is [VIESEndpoint].
	Endpoint string
}

var _ VIESChecker = (*VIESClient)(nil)

// CheckVAT implements [VIESChecker].
func (c *VIESClient) CheckVAT(ctx context.Context, vat string) (*VIESLookup, error) {
	if !ValidVATNumberSyntax(vat) {
		return nil, nil
	}
	n := NormalizeVATNumber(vat)
	if n[:2] == "GB" {
		// GB numbers are checked with HMRC, which needs credentials Tanvo
		// does not configure: valvat answers invalid without a request.
		return nil, nil
	}
	body, err := c.post(ctx, n[:2], n[2:])
	if err != nil {
		return nil, err
	}
	fields, err := parseVIES(body)
	if err != nil {
		return nil, err
	}
	if fault, ok := fields["faultstring"]; ok {
		t, failed := VIESFault(value(fault))
		if !failed {
			return nil, nil
		}
		return nil, newVIESError(t, value(fault))
	}
	if value(fields["valid"]) != "true" {
		return nil, nil
	}
	l := &VIESLookup{
		CountryCode: value(fields["country_code"]),
		VATNumber:   value(fields["vat_number"]),
		Valid:       true,
		Name:        fields["name"],
		Address:     fields["address"],
	}
	if d := value(fields["request_date"]); len(d) >= len(time.DateOnly) {
		l.RequestDate = d[:len(time.DateOnly)]
	}
	return l, nil
}

func (c *VIESClient) post(ctx context.Context, country, number string) ([]byte, error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = VIESEndpoint
	}
	hc := http.Client{Timeout: viesTimeout}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var b bytes.Buffer
	b.WriteString(`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:urn="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><soapenv:Header/><soapenv:Body><urn:checkVat><urn:countryCode>`)
	_ = xml.EscapeText(&b, []byte(country))
	b.WriteString(`</urn:countryCode><urn:vatNumber>`)
	_ = xml.EscapeText(&b, []byte(number))
	b.WriteString(`</urn:vatNumber></urn:checkVat></soapenv:Body></soapenv:Envelope>`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &b)
	if err != nil {
		return nil, fmt.Errorf("vies: build request: %w", err)
	}
	req.Header.Set("Accept", "text/xml;charset=UTF-8")
	req.Header.Set("Content-Type", "text/xml;charset=UTF-8")
	req.Header.Set("SOAPAction", "")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vies: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vies: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newVIESError(VIESErrorServiceUnavailable, fmt.Sprint(resp.StatusCode))
	}
	return body, nil
}

// parseVIES returns the children of the first element of the SOAP body,
// keyed in snake case without the trader_ prefix; "---" (undisclosed) is
// nil.
func parseVIES(body []byte) (map[string]*string, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	fields := map[string]*string{}
	depth, bodyDepth := 0, -1
	var key string
	var text strings.Builder
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("vies: parse response: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case bodyDepth < 0 && t.Name.Local == "Body":
				bodyDepth = depth
			case bodyDepth > 0 && depth == bodyDepth+2:
				key = viesKey(t.Name.Local)
				text.Reset()
			}
		case xml.CharData:
			if key != "" {
				text.Write(t)
			}
		case xml.EndElement:
			if key != "" && depth == bodyDepth+2 {
				if _, seen := fields[key]; !seen {
					if v := text.String(); v != "---" {
						fields[key] = &v
					} else {
						fields[key] = nil
					}
				}
				key = ""
			}
			if depth == bodyDepth+1 {
				// Only the first element of the body is read.
				return fields, nil
			}
			depth--
		}
	}
	if bodyDepth < 0 {
		return nil, errors.New("vies: parse response: no SOAP body")
	}
	return fields, nil
}

var (
	viesKeyAcronym = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	viesKeyCamel   = regexp.MustCompile(`([a-z\d])([A-Z])`)
)

func viesKey(name string) string {
	k := viesKeyAcronym.ReplaceAllString(name, "${1}_${2}")
	k = viesKeyCamel.ReplaceAllString(k, "${1}_${2}")
	k = strings.ReplaceAll(k, "-", "_")
	k = strings.ToLower(k)
	return strings.TrimPrefix(k, "trader_")
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
