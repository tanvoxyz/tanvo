package tax_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/tax"
)

const viesFaultBody = `<?xml version="1.0" encoding="UTF-8"?><env:Envelope xmlns:env="http://schemas.xmlsoap.org/soap/envelope/"><env:Body><env:Fault><faultcode>env:Server</faultcode><faultstring>FAULT</faultstring></env:Fault></env:Body></env:Envelope>`

const viesValidBody = `<env:Envelope xmlns:env="http://schemas.xmlsoap.org/soap/envelope/"><env:Header/><env:Body><ns2:checkVatResponse xmlns:ns2="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><ns2:countryCode>IE</ns2:countryCode><ns2:vatNumber>6388047V</ns2:vatNumber><ns2:requestDate>2026-10-24+02:00</ns2:requestDate><ns2:valid>true</ns2:valid><ns2:name>GOOGLE IRELAND LIMITED</ns2:name><ns2:address>---</ns2:address></ns2:checkVatResponse></env:Body></env:Envelope>`

const viesInvalidBody = `<env:Envelope xmlns:env="http://schemas.xmlsoap.org/soap/envelope/"><env:Body><ns2:checkVatResponse xmlns:ns2="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><ns2:countryCode>IE</ns2:countryCode><ns2:vatNumber>6388047X</ns2:vatNumber><ns2:requestDate>2026-10-24+02:00</ns2:requestDate><ns2:valid>false</ns2:valid><ns2:name>---</ns2:name><ns2:address>---</ns2:address></ns2:checkVatResponse></env:Body></env:Envelope>`

func TestVIESClient(t *testing.T) {
	name := "GOOGLE IRELAND LIMITED"
	tests := []struct {
		name     string
		source   string
		vat      string
		status   int
		body     string
		noCall   bool
		want     *tax.VIESLookup
		wantType tax.VIESErrorType
		wantMsg  string
	}{
		{
			name: "known number", vat: "IE 6388047V", status: 200, body: viesValidBody,
			want: &tax.VIESLookup{CountryCode: "IE", VATNumber: "6388047V", RequestDate: "2026-10-24", Valid: true, Name: &name},
		},
		{name: "unknown number", vat: "IE6388047X", status: 200, body: viesInvalidBody},
		{name: "invalid input fault", vat: "IE6388047V", status: 200, body: strings.Replace(viesFaultBody, "FAULT", "INVALID_INPUT", 1)},
		{
			name: "malformed number", source: "spec/services/customers/vies_check_service_spec.rb:100",
			vat: "invalid_vat_number", noCall: true,
		},
		{name: "GB number", vat: "GB123456789", noCall: true},
		{
			name: "rate limit", source: "spec/services/customers/vies_check_service_spec.rb:230",
			vat: "IT12345678901", status: 200, body: strings.Replace(viesFaultBody, "FAULT", "MS_MAX_CONCURRENT_REQ", 1),
			wantType: tax.VIESErrorRateLimit, wantMsg: "The VIES web service returned the error: MS_MAX_CONCURRENT_REQ",
		},
		{
			name: "member state unavailable", source: "spec/services/customers/vies_check_service_spec.rb:231",
			vat: "IT12345678901", status: 200, body: strings.Replace(viesFaultBody, "FAULT", "MS_UNAVAILABLE", 1),
			wantType: tax.VIESErrorMemberStateUnavailable, wantMsg: "The VIES web service returned the error: MS_UNAVAILABLE",
		},
		{
			name: "service unavailable", source: "spec/services/customers/vies_check_service_spec.rb:232",
			vat: "IT12345678901", status: 200, body: strings.Replace(viesFaultBody, "FAULT", "SERVICE_UNAVAILABLE", 1),
			wantType: tax.VIESErrorServiceUnavailable, wantMsg: "The VIES web service returned the error: SERVICE_UNAVAILABLE",
		},
		{
			name: "timeout", source: "spec/services/customers/vies_check_service_spec.rb:233",
			vat: "IT12345678901", status: 200, body: strings.Replace(viesFaultBody, "FAULT", "TIMEOUT", 1),
			wantType: tax.VIESErrorTimeout, wantMsg: "The VIES web service returned the error: TIMEOUT",
		},
		{
			name: "blocked", source: "spec/services/customers/vies_check_service_spec.rb:234",
			vat: "IT12345678901", status: 200, body: strings.Replace(viesFaultBody, "FAULT", "VAT_BLOCKED", 1),
			wantType: tax.VIESErrorBlocked, wantMsg: "The VIES web service returned the error: VAT_BLOCKED",
		},
		{
			name: "invalid requester", source: "spec/services/customers/vies_check_service_spec.rb:235",
			vat: "IT12345678901", status: 200, body: strings.Replace(viesFaultBody, "FAULT", "INVALID_REQUESTER_INFO", 1),
			wantType: tax.VIESErrorInvalidRequester, wantMsg: "The VIES web service returned the error: INVALID_REQUESTER_INFO",
		},
		{
			name: "unknown fault", vat: "IT12345678901", status: 200, body: strings.Replace(viesFaultBody, "FAULT", "SOMETHING_ELSE", 1),
			wantType: tax.VIESErrorUnknown, wantMsg: "The VIES web service returned the error: SOMETHING_ELSE",
		},
		{
			name: "HTTP 307", source: "spec/services/customers/vies_check_service_spec.rb:245",
			vat: "IT12345678901", status: 307, body: "ERROR",
			wantType: tax.VIESErrorServiceUnavailable, wantMsg: "The VIES web service returned the error: 307",
		},
		{
			name: "HTTP 400", source: "spec/services/customers/vies_check_service_spec.rb:246",
			vat: "IT12345678901", status: 400, body: "ERROR",
			wantType: tax.VIESErrorServiceUnavailable, wantMsg: "The VIES web service returned the error: 400",
		},
		{
			name: "HTTP 500", source: "spec/services/customers/vies_check_service_spec.rb:247",
			vat: "IT12345678901", status: 500, body: "ERROR",
			wantType: tax.VIESErrorServiceUnavailable, wantMsg: "The VIES web service returned the error: 500",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				if got := r.Header.Get("Content-Type"); got != "text/xml;charset=UTF-8" {
					t.Errorf("Content-Type = %q, want = text/xml;charset=UTF-8", got)
				}
				if tt.status == http.StatusTemporaryRedirect {
					w.Header().Set("Location", "/elsewhere")
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			c := &tax.VIESClient{HTTP: srv.Client(), Endpoint: srv.URL}
			got, err := c.CheckVAT(context.Background(), tt.vat)
			if called == tt.noCall {
				t.Fatalf("called = %v, want = %v", called, !tt.noCall)
			}
			if tt.wantType != "" {
				var ve *tax.VIESError
				if !errors.As(err, &ve) {
					t.Fatalf("err = %v, want = a *VIESError", err)
				}
				if ve.Type != tt.wantType || ve.Message != tt.wantMsg {
					t.Errorf("err = %+v, want = {%s %s}", ve, tt.wantType, tt.wantMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want = nil", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got = %+v, want = %+v", got, tt.want)
			}
			if called && !strings.Contains(gotBody, "<urn:countryCode>"+tt.vat[:2]+"</urn:countryCode>") {
				t.Errorf("request = %s, want the country code %s", gotBody, tt.vat[:2])
			}
		})
	}
}

func TestVIESClientRequest(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		_, _ = io.WriteString(w, viesInvalidBody)
	}))
	defer srv.Close()
	c := &tax.VIESClient{HTTP: srv.Client(), Endpoint: srv.URL}
	if _, err := c.CheckVAT(context.Background(), "el-123456789"); err != nil {
		t.Fatalf("err = %v, want = nil", err)
	}
	want := `<urn:checkVat><urn:countryCode>EL</urn:countryCode><urn:vatNumber>123456789</urn:vatNumber></urn:checkVat>`
	if !strings.Contains(got, want) {
		t.Errorf("request = %s, want it to contain %s", got, want)
	}
}
