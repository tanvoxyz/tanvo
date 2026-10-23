package pdfa

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// FacturXNamespace is the namespace of the Factur-X XMP properties.
const FacturXNamespace = "urn:factur-x:pdfa:CrossIndustryDocument:invoice:1p0#"

// FacturX are the Factur-X XMP properties of a hybrid invoice.
type FacturX struct {
	// DocumentType is "INVOICE" (also for credit notes) or "ORDER".
	DocumentType string
	// FileName is the embedded XML's file name, "factur-x.xml".
	FileName string
	// Version is the Factur-X version, "1.0".
	Version string
	// ConformanceLevel is the profile, such as "EN 16931".
	ConformanceLevel string
}

// ErrMalformed reports a document this package cannot update.
var ErrMalformed = errors.New("pdfa: malformed document")

var (
	startXRef = regexp.MustCompile(`startxref\s+(\d+)\s+%%EOF\s*$`)
	trailerRe = regexp.MustCompile(`(?s)trailer\s*<<(.*?)>>\s*startxref`)
	sizeRe    = regexp.MustCompile(`/Size\s+(\d+)`)
	rootRe    = regexp.MustCompile(`/Root\s+(\d+)\s+(\d+)\s+R`)
	infoRe    = regexp.MustCompile(`/Info\s+\d+\s+\d+\s+R`)
	idRe      = regexp.MustCompile(`/ID\s*\[[^\]]*\]`)
	metaRe    = regexp.MustCompile(`/Metadata\s+(\d+)\s+(\d+)\s+R`)
)

// AddFacturX returns pdf with the Factur-X extension schema and properties
// added to its XMP metadata. pdf must use a classic cross-reference table,
// as Typst writes.
func AddFacturX(pdf []byte, fx FacturX) ([]byte, error) {
	m := startXRef.FindSubmatch(pdf)
	if m == nil {
		return nil, fmt.Errorf("%w: no startxref", ErrMalformed)
	}
	prev := string(m[1])
	tail := pdf[max(0, len(pdf)-4096):]
	ts := trailerRe.FindAllSubmatch(tail, -1)
	if ts == nil {
		return nil, fmt.Errorf("%w: no trailer (cross-reference streams are not supported)", ErrMalformed)
	}
	trailer := ts[len(ts)-1][1]
	sm := sizeRe.FindSubmatch(trailer)
	rm := rootRe.FindSubmatch(trailer)
	if sm == nil || rm == nil {
		return nil, fmt.Errorf("%w: trailer without /Size or /Root", ErrMalformed)
	}
	catalog, err := object(pdf, string(rm[1]), string(rm[2]))
	if err != nil {
		return nil, err
	}
	mm := metaRe.FindSubmatch(catalog)
	if mm == nil {
		return nil, fmt.Errorf("%w: catalog without /Metadata", ErrMalformed)
	}
	num, gen := string(mm[1]), string(mm[2])
	meta, err := object(pdf, num, gen)
	if err != nil {
		return nil, err
	}
	packet, err := streamData(meta)
	if err != nil {
		return nil, err
	}
	updated, err := addProperties(packet, fx)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	b.Write(pdf)
	if pdf[len(pdf)-1] != '\n' {
		b.WriteByte('\n')
	}
	offset := b.Len()
	fmt.Fprintf(&b, "%s %s obj\n<</Length %d/Type/Metadata/Subtype/XML>>\nstream\n", num, gen, len(updated))
	b.Write(updated)
	b.WriteString("\nendstream\nendobj\n")
	xref := b.Len()
	g, _ := strconv.Atoi(gen)
	fmt.Fprintf(&b, "xref\n%s 1\n%010d %05d n\r\n", num, offset, g)
	fmt.Fprintf(&b, "trailer\n<</Size %s/Root %s %s R", sm[1], rm[1], rm[2])
	if s := infoRe.Find(trailer); s != nil {
		b.Write(s)
	}
	if s := idRe.Find(trailer); s != nil {
		b.Write(s)
	}
	fmt.Fprintf(&b, "/Prev %s>>\nstartxref\n%d\n%%%%EOF\n", prev, xref)
	return b.Bytes(), nil
}

// object returns the body of the last definition of object num gen.
func object(pdf []byte, num, gen string) ([]byte, error) {
	re := regexp.MustCompile(`(?:^|[\r\n])` + num + `\s+` + gen + `\s+obj\b`)
	locs := re.FindAllIndex(pdf, -1)
	if locs == nil {
		return nil, fmt.Errorf("%w: object %s %s not found", ErrMalformed, num, gen)
	}
	start := locs[len(locs)-1][1]
	end := bytes.Index(pdf[start:], []byte("endobj"))
	if end < 0 {
		return nil, fmt.Errorf("%w: object %s %s not terminated", ErrMalformed, num, gen)
	}
	return pdf[start : start+end], nil
}

var lengthRe = regexp.MustCompile(`/Length\s+(\d+)`)

// streamData returns the data of an unfiltered stream object.
func streamData(obj []byte) ([]byte, error) {
	i := bytes.Index(obj, []byte("stream"))
	if i < 0 {
		return nil, fmt.Errorf("%w: metadata is not a stream", ErrMalformed)
	}
	if bytes.Contains(obj[:i], []byte("/Filter")) {
		return nil, fmt.Errorf("%w: metadata stream is filtered", ErrMalformed)
	}
	data := obj[i+len("stream"):]
	data = bytes.TrimPrefix(data, []byte("\r"))
	data = bytes.TrimPrefix(data, []byte("\n"))
	if m := lengthRe.FindSubmatch(obj[:i]); m != nil {
		if n, err := strconv.Atoi(string(m[1])); err == nil && n <= len(data) {
			return data[:n], nil
		}
	}
	j := bytes.LastIndex(data, []byte("endstream"))
	if j < 0 {
		return nil, fmt.Errorf("%w: metadata stream not terminated", ErrMalformed)
	}
	return bytes.TrimRight(data[:j], "\r\n"), nil
}

const extensionSchema = `<rdf:li rdf:parseType="Resource">` +
	`<pdfaSchema:schema>Factur-X PDFA Extension Schema</pdfaSchema:schema>` +
	`<pdfaSchema:namespaceURI>` + FacturXNamespace + `</pdfaSchema:namespaceURI>` +
	`<pdfaSchema:prefix>fx</pdfaSchema:prefix>` +
	`<pdfaSchema:property><rdf:Seq>` +
	`<rdf:li rdf:parseType="Resource"><pdfaProperty:name>DocumentFileName</pdfaProperty:name><pdfaProperty:valueType>Text</pdfaProperty:valueType><pdfaProperty:category>external</pdfaProperty:category><pdfaProperty:description>The name of the embedded XML document</pdfaProperty:description></rdf:li>` +
	`<rdf:li rdf:parseType="Resource"><pdfaProperty:name>DocumentType</pdfaProperty:name><pdfaProperty:valueType>Text</pdfaProperty:valueType><pdfaProperty:category>external</pdfaProperty:category><pdfaProperty:description>The type of the hybrid document in capital letters, e.g. INVOICE or ORDER</pdfaProperty:description></rdf:li>` +
	`<rdf:li rdf:parseType="Resource"><pdfaProperty:name>Version</pdfaProperty:name><pdfaProperty:valueType>Text</pdfaProperty:valueType><pdfaProperty:category>external</pdfaProperty:category><pdfaProperty:description>The actual version of the standard applying to the embedded XML document</pdfaProperty:description></rdf:li>` +
	`<rdf:li rdf:parseType="Resource"><pdfaProperty:name>ConformanceLevel</pdfaProperty:name><pdfaProperty:valueType>Text</pdfaProperty:valueType><pdfaProperty:category>external</pdfaProperty:category><pdfaProperty:description>The conformance level of the embedded XML document</pdfaProperty:description></rdf:li>` +
	`</rdf:Seq></pdfaSchema:property></rdf:li>`

const extensionNamespaces = `xmlns:pdfaExtension="http://www.aiim.org/pdfa/ns/extension/" ` +
	`xmlns:pdfaSchema="http://www.aiim.org/pdfa/ns/schema#" ` +
	`xmlns:pdfaProperty="http://www.aiim.org/pdfa/ns/property#"`

// addProperties adds the Factur-X schema to the packet's PDF/A extension
// schemas and the Factur-X properties as a new rdf:Description.
func addProperties(packet []byte, fx FacturX) ([]byte, error) {
	if bytes.Contains(packet, []byte(FacturXNamespace)) {
		return nil, fmt.Errorf("%w: Factur-X metadata already present", ErrMalformed)
	}
	end := []byte("</rdf:RDF>")
	i := bytes.LastIndex(packet, end)
	if i < 0 {
		return nil, fmt.Errorf("%w: XMP without rdf:RDF", ErrMalformed)
	}
	var desc bytes.Buffer
	schemas := []byte("</rdf:Bag></pdfaExtension:schemas>")
	j := bytes.LastIndex(packet, schemas)
	if j < 0 {
		desc.WriteString(`<rdf:Description rdf:about="" ` + extensionNamespaces + `><pdfaExtension:schemas><rdf:Bag>` +
			extensionSchema + `</rdf:Bag></pdfaExtension:schemas></rdf:Description>`)
	}
	desc.WriteString(`<rdf:Description rdf:about="" xmlns:fx="` + FacturXNamespace + `">`)
	for _, p := range [][2]string{
		{"DocumentType", fx.DocumentType},
		{"DocumentFileName", fx.FileName},
		{"Version", fx.Version},
		{"ConformanceLevel", fx.ConformanceLevel},
	} {
		desc.WriteString("<fx:" + p[0] + ">")
		if err := xml.EscapeText(&desc, []byte(p[1])); err != nil {
			return nil, fmt.Errorf("pdfa: escape %s: %w", p[0], err)
		}
		desc.WriteString("</fx:" + p[0] + ">")
	}
	desc.WriteString("</rdf:Description>")

	var out bytes.Buffer
	if j >= 0 {
		out.Write(packet[:j])
		out.WriteString(extensionSchema)
		out.Write(packet[j:i])
	} else {
		out.Write(packet[:i])
	}
	out.Write(desc.Bytes())
	out.Write(packet[i:])
	return out.Bytes(), nil
}
