package documents

import (
	"strings"

	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
)

// layout is the document handed to the Typst templates as JSON: every text
// is already translated and formatted, so the templates only lay it out.
type layout struct {
	// Lang is the document language (an ISO 639-1 code for Typst).
	Lang string `json:"lang"`
	// Region is the document region, if any (Typst's text region).
	Region string `json:"region,omitempty"`
	// Fonts is the font fallback chain ([fontChain]).
	Fonts []string `json:"fonts"`
	// locale is the document locale.
	locale string
	// Title is the document's name (also its PDF title).
	Title string `json:"title"`
	// Logo is the file name of the logo next to the data, if any.
	Logo string `json:"logo,omitempty"`
	// Info are the label/value rows under the title.
	Info [][2]string `json:"info"`
	// Meta are the customer metadata rows beside Info.
	Meta [][2]string `json:"meta"`
	// Parties are the issuer and recipient columns.
	Parties []party `json:"parties"`
	// Amount is the headline amount.
	Amount string `json:"amount"`
	// AmountNote is the line under the headline amount.
	AmountNote string `json:"amount_note,omitempty"`
	// Blocks are the document's body.
	Blocks []block `json:"blocks"`
	// Footer is the page footer.
	Footer footer `json:"footer"`
}

type party struct {
	Title string   `json:"title"`
	Lines []string `json:"lines"`
}

type footer struct {
	// Number is the document number printed on the left.
	Number string `json:"number"`
	// Page is the page numbering, as text and counters.
	Page []pagePart `json:"page"`
}

// pagePart is a piece of the page numbering: literal text, or the current
// page ("current") or page count ("total").
type pagePart struct {
	Text    string `json:"text,omitempty"`
	Counter string `json:"counter,omitempty"`
}

// Block kinds.
const (
	blockHeading  = "heading"
	blockTable    = "table"
	blockTotals   = "totals"
	blockText     = "text"
	blockSections = "sections"
)

type block struct {
	Kind string `json:"kind"`
	// Text is a heading's text.
	Text string `json:"text,omitempty"`
	// Break starts the block on a new page.
	Break bool `json:"break,omitempty"`
	// Style is a table's style ("resume", "breakdown", "presentation") or a
	// text's style ("body1", "body3", "alert").
	Style string `json:"style,omitempty"`
	// Columns are a table's column widths (fractions) and alignments.
	Columns []column `json:"columns,omitempty"`
	// Rows are a table's rows.
	Rows []row `json:"rows,omitempty"`
	// Totals are a totals table's rows.
	Totals []totalRow `json:"totals,omitempty"`
	// Lines are a text block's lines.
	Lines []string `json:"lines,omitempty"`
	// Sections are custom sections.
	Sections []section `json:"sections,omitempty"`
	// Gap adds space above the block.
	Gap bool `json:"gap,omitempty"`
}

type column struct {
	Width float64 `json:"width"`
	Align string  `json:"align"`
}

// Row styles of item tables.
const (
	rowHead     = "head"     // column headers (muted)
	rowFee      = "fee"      // a fee: rule above
	rowName     = "name"     // a fee whose details follow: rule above
	rowDetail   = "detail"   // a detail line (muted, indented)
	rowSubtotal = "subtotal" // a detail subtotal or conversion line
	rowPlain    = "plain"    // breakdown and presentation rows
	rowEnd      = "end"      // a presentation group's last row
)

type row struct {
	Style string `json:"style"`
	Cells []cell `json:"cells"`
}

// cell is a table cell: main lines, in bold when Bold, then muted lines.
type cell struct {
	Text []string `json:"text,omitempty"`
	Sub  []string `json:"sub,omitempty"`
	Bold bool     `json:"bold,omitempty"`
}

type totalRow struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Strong bool   `json:"strong,omitempty"`
}

type section struct {
	Title string   `json:"title"`
	Lines []string `json:"lines"`
}

func txt(s ...string) cell { return cell{Text: nonEmpty(s)} }

func bold(s string, sub ...string) cell { return cell{Text: []string{s}, Sub: sub, Bold: true} }

func nonEmpty(s []string) []string {
	var out []string
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// breakLines splits text into its non-blank lines (LineBreakHelper).
func breakLines(text string) []string {
	var out []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// pageNumbering splits the locale's "Page %{current} of %{total}".
func pageNumbering(l *i18n.Locale) []pagePart {
	s := l.T("document.page_numbering")
	var parts []pagePart
	for s != "" {
		i := strings.Index(s, "%{")
		if i < 0 {
			parts = append(parts, pagePart{Text: s})
			break
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			parts = append(parts, pagePart{Text: s})
			break
		}
		if i > 0 {
			parts = append(parts, pagePart{Text: s[:i]})
		}
		name := s[i+2 : i+j]
		if name == "current" || name == "total" {
			parts = append(parts, pagePart{Counter: name})
		} else {
			parts = append(parts, pagePart{Text: s[i : i+j+1]})
		}
		s = s[i+j+1:]
	}
	return parts
}

// language returns Typst's lang and region for a document locale.
func language(tag string) (lang, region string) {
	l, r, _ := strings.Cut(tag, "-")
	if l == "nb" {
		return "nb", r
	}
	return l, r
}

// fiveColumns are the item table columns (lago's .invoice-resume-table).
func fiveColumns() []column {
	return []column{{48, "left"}, {14.5, "right"}, {12.5, "right"}, {12.5, "right"}, {12.5, "right"}}
}

// fourColumns are the item table columns without tax rate.
func fourColumns() []column {
	return []column{{48, "left"}, {14.5, "right"}, {25, "right"}, {12.5, "right"}}
}

// twoColumns are the item and amount columns.
func twoColumns() []column { return []column{{80, "left"}, {20, "right"}} }

// threeColumns are the credit note's item, tax rate and amount columns.
func threeColumns() []column { return []column{{60, "left"}, {20, "right"}, {20, "right"}} }
