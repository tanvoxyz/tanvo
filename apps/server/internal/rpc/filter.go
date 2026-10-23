package rpc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// FilterField is the request field the AIP-160 filter of a list RPC is
// read from; violations are reported on it.
const FilterField = "filter"

// Comparator is a comparison operator of an AIP-160 restriction.
type Comparator string

// The comparators of AIP-160.
const (
	CmpEq  Comparator = "="
	CmpNe  Comparator = "!="
	CmpLt  Comparator = "<"
	CmpLe  Comparator = "<="
	CmpGt  Comparator = ">"
	CmpGe  Comparator = ">="
	CmpHas Comparator = ":"
)

// FilterExpr is a node of a parsed AIP-160 filter: [FilterAnd],
// [FilterOr], [FilterNot] or [Restriction].
type FilterExpr interface{ filterExpr() }

// FilterAnd holds when every term holds.
type FilterAnd []FilterExpr

// FilterOr holds when a term holds.
type FilterOr []FilterExpr

// FilterNot holds when X does not.
type FilterNot struct {
	// X is the negated expression.
	X FilterExpr
}

// Restriction compares a field with a value: `aggregation_type = SUM`.
type Restriction struct {
	// Field is the field path ("plan_id", "metadata.key").
	Field string
	// Op is the comparator.
	Op Comparator
	// Value is the literal's text, unquoted.
	Value string
	// Quoted reports whether the literal was a string literal.
	Quoted bool
}

func (FilterAnd) filterExpr()   {}
func (FilterOr) filterExpr()    {}
func (FilterNot) filterExpr()   {}
func (Restriction) filterExpr() {}

// ParseFilter parses an AIP-160 filter. It supports restrictions
// (`field op value`, with the comparators =, !=, <, <=, >, >= and :),
// AND, OR (binding tighter than AND, as AIP-160 specifies), NOT and -,
// juxtaposition as AND, and parentheses. Values are string literals in
// double or single quotes, or bare words (numbers, booleans, enum names,
// IDs). An empty filter is nil. A malformed one fails with an
// InvalidArgument violation on [FilterField].
func ParseFilter(s string) (FilterExpr, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	toks, err := lexFilter(s)
	if err != nil {
		return nil, filterError(err.Error())
	}
	p := &filterParser{toks: toks}
	e, err := p.expression()
	if err == nil && !p.done() {
		err = fmt.Errorf("unexpected %q", p.peek().text)
	}
	if err != nil {
		return nil, filterError(err.Error())
	}
	return e, nil
}

// Equalities reads a filter made of equality restrictions on the allowed
// fields, combined with AND, where a field may also be compared with
// several values joined by OR (`aggregation_type = SUM OR
// aggregation_type = MAX`). It returns the values of each field. Any
// other filter fails with an InvalidArgument violation on [FilterField].
func Equalities(e FilterExpr, allowed ...string) (map[string][]string, error) {
	out := map[string][]string{}
	if e == nil {
		return out, nil
	}
	terms := []FilterExpr{e}
	if and, ok := e.(FilterAnd); ok {
		terms = and
	}
	for _, t := range terms {
		alts := []FilterExpr{t}
		if or, ok := t.(FilterOr); ok {
			alts = or
		}
		field := ""
		var values []string
		for _, a := range alts {
			r, ok := a.(Restriction)
			if !ok || r.Op != CmpEq {
				return nil, filterError("only `field = value` restrictions joined by AND, or by OR on one field, are supported")
			}
			if !slices.Contains(allowed, r.Field) {
				return nil, filterError(fmt.Sprintf("unknown field %q; supported: %s", r.Field, strings.Join(allowed, ", ")))
			}
			if field != "" && r.Field != field {
				return nil, filterError("OR must compare one field")
			}
			field = r.Field
			values = append(values, r.Value)
		}
		if _, dup := out[field]; dup {
			return nil, filterError(fmt.Sprintf("field %q is restricted twice", field))
		}
		out[field] = values
	}
	return out, nil
}

// FilterBool parses the single boolean value of a filter field: true or
// false.
func FilterBool(field string, values []string) (*bool, error) {
	if values == nil {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, filterError(fmt.Sprintf("field %q takes one value", field))
	}
	b, err := strconv.ParseBool(values[0])
	if err != nil || (values[0] != "true" && values[0] != "false") {
		return nil, filterError(fmt.Sprintf("field %q takes true or false", field))
	}
	return &b, nil
}

// FilterSingle returns the single value of a filter field, "" when the
// field is not restricted.
func FilterSingle(field string, values []string) (string, error) {
	switch len(values) {
	case 0:
		return "", nil
	case 1:
		return values[0], nil
	}
	return "", filterError(fmt.Sprintf("field %q takes one value", field))
}

// FilterEnum parses the values of a filter field naming values of the
// proto enumeration e, by their name with or without the enumeration's
// prefix and in any case (`SUM`, `aggregation_type_sum`). UNSPECIFIED is
// not accepted.
func FilterEnum[P ~int32](field string, values []string, e protoreflect.EnumDescriptor) ([]P, error) {
	prefix := ""
	if z := e.Values().ByNumber(0); z != nil {
		prefix = strings.TrimSuffix(string(z.Name()), "UNSPECIFIED")
	}
	out := make([]P, 0, len(values))
	for _, s := range values {
		found := false
		for i := range e.Values().Len() {
			v := e.Values().Get(i)
			name := string(v.Name())
			if v.Number() != 0 && (strings.EqualFold(name, s) || strings.EqualFold(strings.TrimPrefix(name, prefix), s)) {
				out = append(out, P(v.Number()))
				found = true
				break
			}
		}
		if !found {
			return nil, filterError(fmt.Sprintf("field %q: unknown value %q", field, s))
		}
	}
	return out, nil
}

func filterError(msg string) error {
	return InvalidArgument("invalid filter", []FieldViolation{{Field: FilterField, Reason: core.CodeInvalid, Description: msg}})
}

type tokKind int

const (
	tokWord tokKind = iota
	tokString
	tokOp
	tokLParen
	tokRParen
	tokMinus
)

type token struct {
	kind tokKind
	text string
}

func lexFilter(s string) ([]token, error) {
	var out []token
	rs := []rune(s)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '(':
			out = append(out, token{tokLParen, "("})
			i++
		case c == ')':
			out = append(out, token{tokRParen, ")"})
			i++
		case c == '"' || c == '\'':
			var b strings.Builder
			j := i + 1
			for ; j < len(rs) && rs[j] != c; j++ {
				if rs[j] == '\\' && j+1 < len(rs) {
					j++
				}
				b.WriteRune(rs[j])
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("unterminated string at %d", i)
			}
			out = append(out, token{tokString, b.String()})
			i = j + 1
		case c == '=' || c == ':':
			out = append(out, token{tokOp, string(c)})
			i++
		case c == '!' || c == '<' || c == '>':
			if i+1 < len(rs) && rs[i+1] == '=' {
				out = append(out, token{tokOp, string(rs[i : i+2])})
				i += 2
				continue
			}
			if c == '!' {
				return nil, fmt.Errorf("unexpected ! at %d", i)
			}
			out = append(out, token{tokOp, string(c)})
			i++
		case c == '-' && (len(out) == 0 || out[len(out)-1].kind != tokOp):
			out = append(out, token{tokMinus, "-"})
			i++
		default:
			j := i
			for j < len(rs) && !unicode.IsSpace(rs[j]) && !strings.ContainsRune("()=:!<>\"'", rs[j]) {
				j++
			}
			out = append(out, token{tokWord, string(rs[i:j])})
			i = j
		}
	}
	return out, nil
}

type filterParser struct {
	toks []token
	pos  int
}

func (p *filterParser) done() bool { return p.pos >= len(p.toks) }

func (p *filterParser) peek() token {
	if p.done() {
		return token{kind: -1}
	}
	return p.toks[p.pos]
}

func (p *filterParser) keyword(k string) bool {
	t := p.peek()
	if t.kind == tokWord && t.text == k {
		p.pos++
		return true
	}
	return false
}

// expression := sequence { "AND" sequence }; sequence := factor { factor }.
func (p *filterParser) expression() (FilterExpr, error) {
	var terms FilterAnd
	for {
		f, err := p.factor()
		if err != nil {
			return nil, err
		}
		terms = append(terms, f)
		if p.keyword("AND") {
			continue
		}
		if p.done() || p.peek().kind == tokRParen {
			break
		}
	}
	if len(terms) == 1 {
		return terms[0], nil
	}
	return terms, nil
}

// factor := term { "OR" term }.
func (p *filterParser) factor() (FilterExpr, error) {
	var alts FilterOr
	for {
		t, err := p.term()
		if err != nil {
			return nil, err
		}
		alts = append(alts, t)
		if !p.keyword("OR") {
			break
		}
	}
	if len(alts) == 1 {
		return alts[0], nil
	}
	return alts, nil
}

// term := [ "NOT" | "-" ] simple; simple := restriction | "(" expression ")".
func (p *filterParser) term() (FilterExpr, error) {
	if p.keyword("NOT") || p.peek().kind == tokMinus {
		if p.peek().kind == tokMinus {
			p.pos++
		}
		x, err := p.term()
		if err != nil {
			return nil, err
		}
		return FilterNot{X: x}, nil
	}
	t := p.peek()
	switch t.kind {
	case tokLParen:
		p.pos++
		e, err := p.expression()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen {
			return nil, fmt.Errorf("missing )")
		}
		p.pos++
		return e, nil
	case tokWord:
		if t.text == "AND" || t.text == "OR" {
			return nil, fmt.Errorf("unexpected %s", t.text)
		}
		p.pos++
		op := p.peek()
		if op.kind != tokOp {
			return nil, fmt.Errorf("%q needs a comparator", t.text)
		}
		p.pos++
		v := p.peek()
		if v.kind != tokWord && v.kind != tokString {
			return nil, fmt.Errorf("%q %s needs a value", t.text, op.text)
		}
		p.pos++
		return Restriction{Field: t.text, Op: Comparator(op.text), Value: v.text, Quoted: v.kind == tokString}, nil
	case -1:
		return nil, fmt.Errorf("unexpected end")
	}
	return nil, fmt.Errorf("unexpected %q", t.text)
}
