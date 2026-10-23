package einvoice

import (
	"strings"
)

// xmlDeclaration starts every document.
const xmlDeclaration = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

// indent is the indentation of one nesting level.
const indent = "  "

// attr is an XML attribute.
type attr struct {
	name, value string
}

// node is an element or a comment of the document tree. An element holds
// either text or children; an element with neither is written self-closed.
type node struct {
	name     string
	attrs    []attr
	text     string
	comment  bool
	children []*node
}

// newRoot returns a root element.
func newRoot(name string, attrs ...attr) *node {
	return &node{name: name, attrs: attrs}
}

// add appends a child element and returns it.
func (n *node) add(name string, attrs ...attr) *node {
	c := &node{name: name, attrs: attrs}
	n.children = append(n.children, c)
	return c
}

// with appends a child element and fills it with fn.
func (n *node) with(name string, fn func(*node), attrs ...attr) {
	fn(n.add(name, attrs...))
}

// leaf appends a child element holding text.
func (n *node) leaf(name, text string, attrs ...attr) {
	c := n.add(name, attrs...)
	c.text = text
}

// note appends a comment.
func (n *node) note(text string) {
	n.children = append(n.children, &node{text: text, comment: true})
}

// document serializes the tree rooted at n with an XML declaration, two
// spaces of indentation per level and a final newline.
func (n *node) document() []byte {
	var b strings.Builder
	b.WriteString(xmlDeclaration)
	n.write(&b, 0)
	return []byte(b.String())
}

func (n *node) write(b *strings.Builder, depth int) {
	for range depth {
		b.WriteString(indent)
	}
	if n.comment {
		b.WriteString("<!--")
		b.WriteString(commentText(n.text))
		b.WriteString("-->\n")
		return
	}
	b.WriteByte('<')
	b.WriteString(n.name)
	for _, a := range n.attrs {
		b.WriteByte(' ')
		b.WriteString(a.name)
		b.WriteString(`="`)
		b.WriteString(escapeAttr(a.value))
		b.WriteByte('"')
	}
	switch {
	case len(n.children) > 0:
		b.WriteString(">\n")
		for _, c := range n.children {
			c.write(b, depth+1)
		}
		for range depth {
			b.WriteString(indent)
		}
		b.WriteString("</")
		b.WriteString(n.name)
		b.WriteString(">\n")
	case n.text != "":
		b.WriteByte('>')
		b.WriteString(escapeText(n.text))
		b.WriteString("</")
		b.WriteString(n.name)
		b.WriteString(">\n")
	default:
		b.WriteString("/>\n")
	}
}

// validXMLChar reports whether r may appear in an XML 1.0 document.
func validXMLChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' ||
		(r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
}

// escapeText escapes character data as libxml2 does: &, <, > and carriage
// returns. Characters XML cannot carry are dropped.
func escapeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '\r':
			b.WriteString("&#13;")
		case !validXMLChar(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeAttr escapes an attribute value as libxml2 does.
func escapeAttr(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\n':
			b.WriteString("&#10;")
		case r == '\r':
			b.WriteString("&#13;")
		case r == '\t':
			b.WriteString("&#9;")
		case !validXMLChar(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// commentText keeps a comment well formed: it may neither contain "--" nor
// end with "-".
func commentText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if validXMLChar(r) {
			b.WriteRune(r)
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "- -")
	}
	if strings.HasSuffix(out, "-") {
		out += " "
	}
	return out
}
