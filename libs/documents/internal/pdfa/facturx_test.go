package pdfa

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"rsc.io/pdf"
)

const xmp = `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
	`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:pdfaid="http://www.aiim.org/pdfa/ns/id/"><pdfaid:part>3</pdfaid:part><pdfaid:conformance>B</pdfaid:conformance></rdf:Description>` +
	`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`

// minimalPDF writes a one-page PDF with a classic cross-reference table and
// an XMP metadata stream, as Typst does.
func minimalPDF(meta string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	var offs []int
	obj := func(s string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), s)
	}
	obj("<</Type/Catalog/Pages 2 0 R/Metadata 4 0 R>>")
	obj("<</Type/Pages/Kids[3 0 R]/Count 1>>")
	obj("<</Type/Page/Parent 2 0 R/MediaBox[0 0 595 842]>>")
	obj(fmt.Sprintf("<</Length %d/Type/Metadata/Subtype/XML>>\nstream\n%s\nendstream", len(meta), meta))
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f\r\n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n\r\n", o)
	}
	fmt.Fprintf(&b, "trailer\n<</Size %d/Root 1 0 R/ID[<00><00>]>>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}

var invoiceFX = FacturX{DocumentType: "INVOICE", FileName: "factur-x.xml", Version: "1.0", ConformanceLevel: "EN 16931"}

func readMetadata(t *testing.T, b []byte) string {
	t.Helper()
	r, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("read PDF: %v", err)
	}
	rc := r.Trailer().Key("Root").Key("Metadata").Reader()
	defer rc.Close()
	m, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	return string(m)
}

func TestAddFacturX(t *testing.T) {
	out, err := AddFacturX(minimalPDF(xmp), invoiceFX)
	if err != nil {
		t.Fatalf("AddFacturX: %v", err)
	}
	meta := readMetadata(t, out)
	for _, want := range []string{
		"<pdfaid:part>3</pdfaid:part>",
		"<pdfaSchema:namespaceURI>" + FacturXNamespace + "</pdfaSchema:namespaceURI>",
		"<fx:DocumentType>INVOICE</fx:DocumentType>",
		"<fx:DocumentFileName>factur-x.xml</fx:DocumentFileName>",
		"<fx:Version>1.0</fx:Version>",
		"<fx:ConformanceLevel>EN 16931</fx:ConformanceLevel>",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("metadata: got = %s, want = %q in it", meta, want)
		}
	}
	if n := strings.Count(meta, "<pdfaExtension:schemas>"); n != 1 {
		t.Errorf("extension schemas: got = %d, want = 1", n)
	}
	if !bytes.HasPrefix(out, minimalPDF(xmp)) {
		t.Errorf("update: got = a rewritten file, want = an incremental update")
	}
	if !bytes.Contains(out, []byte("/Prev ")) {
		t.Errorf("trailer: got = no /Prev, want = the previous cross-reference")
	}
}

// An XMP packet with PDF/A extension schemas gets the Factur-X schema added
// to its bag.
func TestAddFacturXExistingSchemas(t *testing.T) {
	packet := strings.Replace(xmp, "</rdf:RDF>",
		`<rdf:Description rdf:about="" xmlns:pdfaExtension="http://www.aiim.org/pdfa/ns/extension/" xmlns:pdfaSchema="http://www.aiim.org/pdfa/ns/schema#" xmlns:pdfaProperty="http://www.aiim.org/pdfa/ns/property#">`+
			`<pdfaExtension:schemas><rdf:Bag><rdf:li rdf:parseType="Resource"><pdfaSchema:prefix>x</pdfaSchema:prefix></rdf:li></rdf:Bag></pdfaExtension:schemas></rdf:Description></rdf:RDF>`, 1)
	out, err := AddFacturX(minimalPDF(packet), invoiceFX)
	if err != nil {
		t.Fatalf("AddFacturX: %v", err)
	}
	meta := readMetadata(t, out)
	if got, want := strings.Count(meta, "<pdfaExtension:schemas>"), 1; got != want {
		t.Errorf("extension schemas: got = %d, want = %d", got, want)
	}
	if got, want := strings.Count(meta, "<rdf:li rdf:parseType=\"Resource\"><pdfaSchema:"), 2; got != want {
		t.Errorf("schemas in the bag: got = %d, want = %d", got, want)
	}
}

func TestAddFacturXMalformed(t *testing.T) {
	added, err := AddFacturX(minimalPDF(xmp), invoiceFX)
	if err != nil {
		t.Fatalf("AddFacturX: %v", err)
	}
	for _, c := range []struct {
		name string
		pdf  []byte
	}{
		{"no startxref", []byte("%PDF-1.7\n")},
		{"no metadata", bytes.Replace(minimalPDF(xmp), []byte("/Metadata 4 0 R"), []byte("/Lang (en)     "), 1)},
		{"filtered metadata", bytes.Replace(minimalPDF(xmp), []byte("/Type/Metadata"), []byte("/Filter/Flate "), 1)},
		{"no rdf", minimalPDF(`<x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)},
		{"already Factur-X", added},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := AddFacturX(c.pdf, invoiceFX); !errors.Is(err, ErrMalformed) {
				t.Errorf("AddFacturX: got = %v, want = ErrMalformed", err)
			}
		})
	}
}
