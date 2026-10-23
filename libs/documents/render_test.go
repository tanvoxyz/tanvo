package documents

import (
	"bytes"
	"context"
	hexpkg "encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"rsc.io/pdf"

	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/typst"
)

// Typst rendering tests. They need the typst executable (mise install) and
// are skipped without it. UPDATE_GOLDEN=1 rewrites testdata/golden.

// logBuffer is a concurrency-safe slog destination.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestRenderer(t *testing.T) (*Renderer, *logBuffer) {
	t.Helper()
	return newTestRendererFonts(t, nil)
}

// newTestRendererFonts starts a renderer with fontPaths (nil: the system
// font directories).
func newTestRendererFonts(t *testing.T, fontPaths []string) (*Renderer, *logBuffer) {
	t.Helper()
	logs := &logBuffer{}
	r, err := New(Options{Dir: t.TempDir(), FontPaths: fontPaths, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if errors.Is(err, typst.ErrNotFound) {
		t.Skip("typst is not installed")
	}
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return r, logs
}

// pdfText extracts a PDF's text: one line per text object (BT … ET), in
// content order, page by page. rsc.io/pdf parses the file and interprets
// the content streams; it does not decode Identity-H fonts, so their
// ToUnicode maps are applied here.
func pdfText(t *testing.T, b []byte) string {
	t.Helper()
	rd, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("read PDF: %v", err)
	}
	var out strings.Builder
	for i := 1; i <= rd.NumPage(); i++ {
		fmt.Fprintf(&out, "--- page %d\n", i)
		p := rd.Page(i)
		fonts := map[string]map[string]string{}
		for _, name := range p.Fonts() {
			fonts[name] = toUnicode(t, p.Font(name).V.Key("ToUnicode"))
		}
		var cur map[string]string
		var line strings.Builder
		show := func(raw string) {
			for j := 0; j+1 < len(raw); j += 2 {
				line.WriteString(cur[raw[j:j+2]])
			}
		}
		pdf.Interpret(p.V.Key("Contents"), func(stk *pdf.Stack, op string) {
			args := make([]pdf.Value, stk.Len())
			for j := len(args) - 1; j >= 0; j-- {
				args[j] = stk.Pop()
			}
			switch op {
			case "Tf":
				cur = fonts[args[0].Name()]
			case "Tj":
				show(args[0].RawString())
			case "TJ":
				a := args[0]
				for j := range a.Len() {
					x := a.Index(j)
					if x.Kind() == pdf.String {
						show(x.RawString())
					} else if x.Float64() < -250 {
						line.WriteString(" ")
					}
				}
			case "ET":
				if s := strings.TrimSpace(line.String()); s != "" {
					out.WriteString(s + "\n")
				}
				line.Reset()
			}
		})
	}
	return out.String()
}

var (
	bfchar  = regexp.MustCompile(`(?s)beginbfchar(.*?)endbfchar`)
	bfrange = regexp.MustCompile(`(?s)beginbfrange(.*?)endbfrange`)
	hexTok  = regexp.MustCompile(`<([0-9A-Fa-f]*)>`)
)

// toUnicode reads a ToUnicode CMap of two-byte codes into code → text.
func toUnicode(t *testing.T, v pdf.Value) map[string]string {
	t.Helper()
	m := map[string]string{}
	if v.Kind() != pdf.Stream {
		return m
	}
	rc := v.Reader()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read ToUnicode: %v", err)
	}
	hex := func(s string) []byte {
		out, err := hexpkg.DecodeString(s)
		if err != nil {
			t.Fatalf("ToUnicode hex %q: %v", s, err)
		}
		return out
	}
	utf16be := func(b []byte) string {
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		}
		return string(utf16.Decode(u))
	}
	for _, sec := range bfchar.FindAllSubmatch(b, -1) {
		toks := hexTok.FindAllSubmatch(sec[1], -1)
		for j := 0; j+1 < len(toks); j += 2 {
			m[string(hex(string(toks[j][1])))] = utf16be(hex(string(toks[j+1][1])))
		}
	}
	for _, sec := range bfrange.FindAllSubmatch(b, -1) {
		toks := hexTok.FindAllSubmatch(sec[1], -1)
		for j := 0; j+2 < len(toks); j += 3 {
			lo, hi, dst := hex(string(toks[j][1])), hex(string(toks[j+1][1])), hex(string(toks[j+2][1]))
			for c := int(lo[0])<<8 | int(lo[1]); c <= int(hi[0])<<8|int(hi[1]); c++ {
				d := slices.Clone(dst)
				d[len(d)-1] += byte(c - (int(lo[0])<<8 | int(lo[1])))
				m[string([]byte{byte(c >> 8), byte(c)})] = utf16be(d)
			}
		}
	}
	return m
}

// checkGolden compares got with testdata/golden/name.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (UPDATE_GOLDEN=1 creates it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s: got =\n%s\nwant =\n%s", name, got, want)
	}
}

func isPDFA(b []byte) bool {
	return bytes.Contains(b, []byte("<pdfaid:part>3</pdfaid:part>")) && bytes.Contains(b, []byte("<pdfaid:conformance>B</pdfaid:conformance>"))
}

func testPaymentRequest(t *testing.T) *PaymentRequest {
	t.Helper()
	inv := prepaidCreditsInvoice(300, 200)(t, &ids{})
	return &PaymentRequest{
		Party: inv.Party,
		Request: payment.Request{
			Amount: inv.Invoice.TotalAmount, Currency: usd(), Email: ptr("john.doe@example.com"),
			CreatedAt: at("2025-09-20 10:00:00"),
		},
		Invoices: []invoice.Invoice{inv.Invoice},
	}
}

func TestRenderKinds(t *testing.T) {
	r, _ := newTestRenderer(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		kind port.DocumentKind
		data func(t *testing.T) any
	}{
		{"invoice", port.DocumentInvoice, func(t *testing.T) any { return prepaidCreditsInvoice(300, 200)(t, &ids{}) }},
		{"invoice_fixed_charges", port.DocumentInvoice, func(t *testing.T) any { return fixedChargesInvoice(false)(t, &ids{}) }},
		{"invoice_progressive", port.DocumentInvoice, func(t *testing.T) any { return progressiveBillingInvoice(true)(t, &ids{}) }},
		{"credit_note", port.DocumentCreditNote, func(t *testing.T) any {
			return creditNoteData("CN-202509-001", 2000, nil, creditSpec{typ: "subscription", name: "Premium", amount: 2000, units: "1"})(t, &ids{})
		}},
		{"payment_receipt", port.DocumentPaymentReceipt, func(t *testing.T) any {
			return receiptForInvoice(t, prepaidCreditsInvoice(300, 200)(t, &ids{}))
		}},
		{"payment_request", port.DocumentPaymentRequest, func(t *testing.T) any { return testPaymentRequest(t) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, err := r.Render(ctx, c.kind, "en", c.data(t))
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if doc.ContentType != ContentTypePDF {
				t.Errorf("content type: got = %q, want = %q", doc.ContentType, ContentTypePDF)
			}
			if !isPDFA(doc.Body) {
				t.Errorf("PDF/A: got = no PDF/A-3b identification, want = PDF/A-3b")
			}
			checkGolden(t, c.name+".txt", []byte(pdfText(t, doc.Body)))
		})
	}
}

// Every Latin-script locale renders as PDF/A-3b with the embedded Inter.
func TestRenderLocales(t *testing.T) {
	r, _ := newTestRenderer(t)
	for _, loc := range r.Locales() {
		if cjkFamilies(loc) != nil {
			continue // TestRenderChinese*
		}
		t.Run(loc, func(t *testing.T) {
			b, err := r.InvoicePDF(context.Background(), prepaidCreditsInvoice(300, 200)(t, &ids{}), loc)
			if err != nil {
				t.Fatalf("InvoicePDF: %v", err)
			}
			if !isPDFA(b) {
				t.Errorf("PDF/A: got = no PDF/A-3b identification, want = PDF/A-3b")
			}
			if loc == "fr" {
				checkGolden(t, "invoice_fr.txt", []byte(pdfText(t, b)))
			}
		})
	}
}

// zh-CN and zh-TW render with a Noto CJK font of the system font
// directories (fonts-noto-cjk in the container image).
func TestRenderChineseWithCJKFont(t *testing.T) {
	r, _ := newTestRenderer(t)
	for _, loc := range []string{"zh-CN", "zh-TW"} {
		t.Run(loc, func(t *testing.T) {
			if !slices.ContainsFunc(cjkFamilies(loc), r.engine.HasFamily) {
				t.Skipf("none of the fonts %q is installed", cjkFamilies(loc))
			}
			b, err := r.InvoicePDF(context.Background(), prepaidCreditsInvoice(300, 200)(t, &ids{}), loc)
			if err != nil {
				t.Fatalf("InvoicePDF: %v", err)
			}
			if !isPDFA(b) {
				t.Errorf("PDF/A: got = no PDF/A-3b identification, want = PDF/A-3b")
			}
			want := r.cat.Locale(loc).T("invoice.document_name")
			if got := pdfText(t, b); !strings.Contains(got, want) {
				t.Errorf("text: got = %q, want = %q in it", got, want)
			}
		})
	}
}

// Without a CJK font, a zh-CN or zh-TW document is an error, not a PDF
// with missing glyphs.
func TestRenderChineseWithoutCJKFont(t *testing.T) {
	r, _ := newTestRendererFonts(t, []string{})
	for _, loc := range []string{"zh-CN", "zh-TW"} {
		t.Run(loc, func(t *testing.T) {
			_, err := r.Render(context.Background(), port.DocumentInvoice, loc, prepaidCreditsInvoice(300, 200)(t, &ids{}))
			var mf *MissingFontError
			if !errors.As(err, &mf) || !errors.Is(err, ErrMissingFont) {
				t.Fatalf("Render: got = %v, want = a MissingFontError", err)
			}
			if got, want := mf.Locale, loc; got != want {
				t.Errorf("locale: got = %q, want = %q", got, want)
			}
			if got, want := mf.Families, cjkFamilies(loc); !slices.Equal(got, want) {
				t.Errorf("families: got = %q, want = %q", got, want)
			}
		})
	}
}

// Customer data in a script no font covers still renders, as a plain PDF
// with a warning: the document must not be blocked by a customer's name.
func TestRenderMissingGlyphFallback(t *testing.T) {
	r, logs := newTestRendererFonts(t, []string{})
	inv := prepaidCreditsInvoice(300, 200)(t, &ids{})
	inv.Customer.LegalName = ptr("บริษัท ตัวอย่าง")
	b, err := r.InvoicePDF(context.Background(), inv, "en")
	if err != nil {
		t.Fatalf("InvoicePDF: %v", err)
	}
	if isPDFA(b) {
		t.Errorf("PDF/A: got = PDF/A-3b, want = a plain PDF")
	}
	if got := logs.String(); !strings.Contains(got, "PDF/A not met") {
		t.Errorf("logs: got = %q, want = a PDF/A fallback warning", got)
	}
}

// source: spec/services/invoices/generate_pdf_service_spec.rb (e-invoicing)
func TestRenderFacturX(t *testing.T) {
	r, _ := newTestRenderer(t)
	inv := prepaidCreditsInvoice(300, 200)(t, &ids{})
	inv.BillingEntity.EInvoicing = true
	inv.BillingEntity.Address.Country = "FR"
	b, err := r.InvoicePDF(context.Background(), inv, "fr")
	if err != nil {
		t.Fatalf("InvoicePDF: %v", err)
	}
	xml, err := r.InvoiceXML(inv, "fr", CII)
	if err != nil {
		t.Fatalf("InvoiceXML: %v", err)
	}
	if !isPDFA(b) {
		t.Errorf("PDF/A: got = no PDF/A-3b identification, want = PDF/A-3b")
	}
	for _, want := range []string{
		"/AFRelationship/Alternative", "factur-x.xml", "/EmbeddedFiles",
		"<fx:DocumentType>INVOICE</fx:DocumentType>", "<fx:DocumentFileName>factur-x.xml</fx:DocumentFileName>",
		"<fx:ConformanceLevel>EN 16931</fx:ConformanceLevel>", "pdfaExtension:schemas",
	} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("PDF: got = no %q, want = %q", want, want)
		}
	}
	if !bytes.Contains(xml, []byte("<rsm:CrossIndustryInvoice")) {
		t.Errorf("XML: got = %.80q, want = a CII invoice", xml)
	}
	// The PDF stays readable after the incremental update.
	if got := pdfText(t, b); !strings.Contains(got, "LAGO-202509-002") {
		t.Errorf("text: got = %q, want = the invoice number", got)
	}
}

func TestRenderEInvoice(t *testing.T) {
	r, _ := newTestRenderer(t)
	inv := prepaidCreditsInvoice(300, 200)(t, &ids{})
	doc, err := r.Render(context.Background(), port.DocumentEInvoice, "en", inv)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want, err := r.InvoiceXML(inv, "en", CII)
	if err != nil {
		t.Fatalf("InvoiceXML: %v", err)
	}
	if doc.ContentType != ContentTypeXML || !bytes.Equal(doc.Body, want) {
		t.Errorf("einvoice: got = %s %.60q, want = %s %.60q", doc.ContentType, doc.Body, ContentTypeXML, want)
	}
}

func TestRenderUnsupported(t *testing.T) {
	r, _ := newTestRenderer(t)
	for _, c := range []struct {
		kind port.DocumentKind
		data any
	}{
		{port.DocumentInvoice, &CreditNote{}},
		{port.DocumentKind("statement"), &Invoice{}},
	} {
		if _, err := r.Render(context.Background(), c.kind, "en", c.data); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Render(%s, %T): got = %v, want = ErrUnsupported", c.kind, c.data, err)
		}
	}
}
