package cli_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

// pdfAttachments returns the PDF attachments of m.
func pdfAttachments(t *testing.T, m smtpMessage) [][]byte {
	t.Helper()
	var out [][]byte
	var walk func(header map[string][]string, body io.Reader)
	walk = func(header map[string][]string, body io.Reader) {
		mt, params, err := mime.ParseMediaType(firstHeader(header, "Content-Type"))
		if err != nil {
			return
		}
		if strings.HasPrefix(mt, "multipart/") {
			mr := multipart.NewReader(body, params["boundary"])
			for {
				p, err := mr.NextRawPart()
				if err != nil {
					return
				}
				walk(p.Header, p)
			}
		}
		if mt == "application/pdf" {
			b, err := io.ReadAll(body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.EqualFold(firstHeader(header, "Content-Transfer-Encoding"), "base64") {
				b = decodeBase64(t, b)
			}
			out = append(out, b)
		}
	}
	walk(m.Msg.Header, m.Msg.Body)
	return out
}

func firstHeader(h map[string][]string, k string) string {
	for hk, v := range h {
		if strings.EqualFold(hk, k) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// TestScenarioInvoiceEmailAndPDF finalizes the in-advance invoice of a new
// subscription: its PDF is rendered with typst into the local blob store,
// downloadable, and emailed to the customer as an attachment when the
// billing entity sends finalized invoices and the customer has an email.
//
// source: spec/scenarios/emails/invoice_email_activity_spec.rb:37
// source: spec/scenarios/emails/invoice_email_activity_spec.rb:51
// source: spec/scenarios/emails/invoice_email_activity_spec.rb:59
func TestScenarioInvoiceEmailAndPDF(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		settings []string
		email    string
		sent     bool
	}{
		{"invoice finalized", []string{"EMAIL_SETTING_INVOICE_FINALIZED"}, "customer@example.com", true},
		{"email scenario disabled", []string{}, "customer@example.com", false},
		{"customer without email", []string{"EMAIL_SETTING_INVOICE_FINALIZED"}, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			smtp := newFakeSMTP(t)
			environ := append(smtp.environ(), "TANVO_BLOB_BACKEND=local", "TANVO_BLOB_LOCAL_ROOT="+t.TempDir())
			s := newScenario(t, time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC), scenarioOptions{environ: environ, selfAPIURL: true})
			entity := s.api(http.MethodGet, "/v1/billing_entities", nil).str("billing_entities.0.id")
			s.api(http.MethodPatch, "/v1/billing_entities/"+entity+"?update_mask=email,emailSettings", map[string]any{
				"email": "billing@acme.test", "email_settings": tt.settings,
			})
			customer := map[string]any{"external_id": "cust", "name": "Customer"}
			if tt.email != "" {
				customer["email"] = tt.email
			}
			s.api(http.MethodPost, "/v1/customers", customer)
			s.plan("plan", "INTERVAL_MONTHLY", "10", true)
			s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "sub", "customer_id": "external_id:cust", "plan_id": "code:plan"})
			s.idle()

			invs := s.waitInvoices("cust", 1)
			id := invs[0].str("id")
			inv := s.api(http.MethodGet, "/v1/invoices/"+id, nil).at("invoice")
			if got := inv.str("file_available"); got != "true" {
				t.Errorf("invoice file available: got = %s, want = true", got)
			}
			url := s.api(http.MethodPost, "/v1/invoices/"+id+":download", map[string]any{}).str("pdf.url")
			if url == "" {
				t.Fatal("invoice download: got = no URL, want = the PDF's")
			}
			res, err := http.Get(url) //nolint:noctx // a test download
			if err != nil {
				t.Fatal(err)
			}
			pdf, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if res.StatusCode != http.StatusOK || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
				t.Errorf("invoice PDF download: got status = %d, %d bytes (%.20q), want = 200 and a PDF", res.StatusCode, len(pdf), pdf)
			}

			var invoiceMails []smtpMessage
			for _, m := range smtp.messages() {
				if strings.Contains(m.Subject(), inv.str("number")) {
					invoiceMails = append(invoiceMails, m)
				}
			}
			if !tt.sent {
				if len(invoiceMails) != 0 {
					t.Errorf("invoice emails: got = %d, want = 0", len(invoiceMails))
				}
				return
			}
			if len(invoiceMails) != 1 {
				var subjects []string
				for _, m := range smtp.messages() {
					subjects = append(subjects, m.Subject())
				}
				t.Fatalf("invoice emails: got = %d (subjects %q), want = 1", len(invoiceMails), subjects)
			}
			m := invoiceMails[0]
			if len(m.To) != 1 || m.To[0] != tt.email {
				t.Errorf("invoice email recipients: got = %v, want = [%s]", m.To, tt.email)
			}
			atts := pdfAttachments(t, m)
			if len(atts) != 1 || !bytes.HasPrefix(atts[0], []byte("%PDF-")) {
				t.Errorf("invoice email PDF attachments: got = %d, want = one PDF", len(atts))
			}
			s.noFailedJobs()
		})
	}
}

// decodeBase64 decodes a base64 MIME body, line breaks included.
func decodeBase64(t *testing.T, b []byte) []byte {
	t.Helper()
	out, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(b)), ""))
	if err != nil {
		t.Fatalf("attachment: %v", err)
	}
	return out
}
