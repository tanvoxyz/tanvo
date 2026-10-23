package validators

import (
	"embed"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// The schema files are lago's UPDATE_DEFINITION and APPROVE_DEFINITION of
// app/services/quote_versions/validators/{one_off,subscription_creation}/
// schema.rb at v1.55.0, serialized to JSON with their key order. Enum
// lists lago builds from model constants are markers expanded from Tanvo's
// own lists: the ISO currencies and the (fixed) charge models.
//
//go:embed schema/*.json
var schemaFiles embed.FS

const (
	markerCurrencies        = "__CURRENCIES__"
	markerChargeModels      = "__CHARGE_MODELS__"
	markerFixedChargeModels = "__FIXED_CHARGE_MODELS__"
)

// schemas holds the four parsed schemas, keyed by file name; they are
// read once and never modified.
var schemas = sync.OnceValues(func() (map[string]*schema, error) {
	out := map[string]*schema{}
	for _, name := range []string{"one_off_update", "one_off_approve", "subscription_creation_update", "subscription_creation_approve"} {
		data, err := schemaFiles.ReadFile("schema/" + name + ".json")
		if err != nil {
			return nil, fmt.Errorf("read schema %s: %w", name, err)
		}
		doc, err := decodeValue(data)
		if err != nil {
			return nil, fmt.Errorf("decode schema %s: %w", name, err)
		}
		s, err := compileSchema(doc)
		if err != nil {
			return nil, fmt.Errorf("compile schema %s: %w", name, err)
		}
		out[name] = s
	}
	return out, nil
})

// schema is the subset of JSON Schema (draft 2020-12) the quote schemas
// use, plus lago's x-error extension.
type schema struct {
	// types is the allowed JSON types (string, number, integer, boolean,
	// object, array, null); empty allows any.
	types      []string
	properties []property
	propIndex  map[string]*schema
	additional *schema
	items      *schema
	// not set means the schema is {"not": {}}, which fails everything.
	not              bool
	required         []string
	enum             []*value
	hasEnum          bool
	constant         *value
	minimum          *money.Decimal
	exclusiveMinimum *money.Decimal
	minLength        *int
	minItems         *int
	maxItems         *int
	format           string
	// xErrorAll is the x-error string; xError the x-error hash.
	xErrorAll *string
	xError    map[string]string
}

type property struct {
	name   string
	schema *schema
}

func compileSchema(doc *value) (*schema, error) {
	if doc == nil || doc.kind != kindObject {
		return nil, fmt.Errorf("schema is not an object")
	}
	s := &schema{}
	for _, key := range doc.keys {
		v := doc.vals[key]
		var err error
		switch key {
		case "type":
			if t, ok := v.str(); ok {
				s.types = []string{t}
			} else {
				for _, e := range v.items() {
					t, _ := e.str()
					s.types = append(s.types, t)
				}
			}
		case "properties":
			s.propIndex = map[string]*schema{}
			for _, name := range v.keys {
				sub, err := compileSchema(v.vals[name])
				if err != nil {
					return nil, fmt.Errorf("%s: %w", name, err)
				}
				s.properties = append(s.properties, property{name: name, schema: sub})
				s.propIndex[name] = sub
			}
		case "additionalProperties":
			s.additional, err = compileSchema(v)
		case "items":
			s.items, err = compileSchema(v)
		case "not":
			s.not = true
		case "required":
			for _, e := range v.items() {
				name, _ := e.str()
				s.required = append(s.required, name)
			}
		case "enum":
			s.hasEnum = true
			for _, e := range v.items() {
				s.enum = append(s.enum, expandMarker(e)...)
			}
		case "const":
			s.constant = v
		case "minimum":
			s.minimum, err = decimalKeyword(v)
		case "exclusiveMinimum":
			s.exclusiveMinimum, err = decimalKeyword(v)
		case "minLength":
			s.minLength, err = intKeyword(v)
		case "minItems":
			s.minItems, err = intKeyword(v)
		case "maxItems":
			s.maxItems, err = intKeyword(v)
		case "format":
			s.format, _ = v.str()
		case "x-error":
			if str, ok := v.str(); ok {
				s.xErrorAll = &str
			} else {
				s.xError = map[string]string{}
				for _, k := range v.keys {
					s.xError[k], _ = v.vals[k].str()
				}
			}
		default:
			return nil, fmt.Errorf("unsupported keyword %q", key)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	return s, nil
}

// expandMarker replaces an enum marker with the values it stands for.
func expandMarker(e *value) []*value {
	str, _ := e.str()
	var out []*value
	add := func(s string) { out = append(out, &value{kind: kindString, s: s}) }
	switch str {
	case markerCurrencies:
		for c := range money.Currencies() {
			add(c.Code())
		}
	case markerChargeModels:
		for _, m := range catalog.ChargeModelValues() {
			add(string(m))
		}
	case markerFixedChargeModels:
		for _, m := range catalog.FixedChargeModelValues() {
			add(string(m))
		}
	default:
		return []*value{e}
	}
	return out
}

func decimalKeyword(v *value) (*money.Decimal, error) {
	d, ok := v.decimal()
	if !ok {
		return nil, fmt.Errorf("not a number")
	}
	return &d, nil
}

func intKeyword(v *value) (*int, error) {
	if v == nil || v.kind != kindNumber {
		return nil, fmt.Errorf("not an integer")
	}
	n, err := strconv.Atoi(v.s)
	if err != nil {
		return nil, fmt.Errorf("not an integer: %w", err)
	}
	return &n, nil
}

// code returns the error code of a failed keyword: the x-error message for
// it, else "is_invalid" (lago BaseStructuralValidator#code_for).
func (s *schema) code(keyword string) string {
	if s.xErrorAll != nil {
		return *s.xErrorAll
	}
	if c, ok := s.xError[keyword]; ok {
		return c
	}
	if c, ok := s.xError["*"]; ok {
		return c
	}
	return "is_invalid"
}

// validate checks v against s and reports each failure to report, with
// the JSON pointer tokens of the failing instance, in json_schemer's
// order: the applicator keywords (properties in schema order,
// additionalProperties in document order, items), then type, enum, const,
// minimum, exclusiveMinimum, minLength, maxItems, minItems, required, then
// format; nested errors depth first.
func (s *schema) validate(v *value, path []string, report func(path []string, code string)) {
	if s.not {
		report(path, s.code("not"))
	}
	switch v.kind {
	case kindObject:
		for _, p := range s.properties {
			if e, ok := v.vals[p.name]; ok {
				p.schema.validate(e, appendPath(path, p.name), report)
			}
		}
		if s.additional != nil {
			for _, k := range v.keys {
				if _, ok := s.propIndex[k]; !ok {
					s.additional.validate(v.vals[k], appendPath(path, k), report)
				}
			}
		}
	case kindArray:
		if s.items != nil {
			for i, e := range v.arr {
				s.items.validate(e, appendPath(path, strconv.Itoa(i)), report)
			}
		}
	}
	if len(s.types) > 0 && !s.typeMatches(v) {
		report(path, s.code("type"))
	}
	if s.hasEnum && !s.enumMatches(v) {
		report(path, s.code("enum"))
	}
	if s.constant != nil && !s.constant.equal(v) {
		report(path, s.code("const"))
	}
	if v.kind == kindNumber {
		if d, ok := v.decimal(); ok {
			if s.minimum != nil && d.LessThan(*s.minimum) {
				report(path, s.code("minimum"))
			}
			if s.exclusiveMinimum != nil && d.LessThanOrEqual(*s.exclusiveMinimum) {
				report(path, s.code("exclusiveMinimum"))
			}
		}
	}
	if v.kind == kindString && s.minLength != nil && utf8.RuneCountInString(v.s) < *s.minLength {
		report(path, s.code("minLength"))
	}
	if v.kind == kindArray {
		if s.maxItems != nil && len(v.arr) > *s.maxItems {
			report(path, s.code("maxItems"))
		}
		if s.minItems != nil && len(v.arr) < *s.minItems {
			report(path, s.code("minItems"))
		}
	}
	if v.kind == kindObject {
		for _, k := range s.required {
			if _, ok := v.vals[k]; !ok {
				report(appendPath(path, k), s.code("required"))
			}
		}
	}
	if v.kind == kindString && s.format != "" && !validFormat(s.format, v.s) {
		report(path, s.code("format"))
	}
}

func (s *schema) typeMatches(v *value) bool {
	for _, t := range s.types {
		switch t {
		case "null":
			if v.kind == kindNull {
				return true
			}
		case "boolean":
			if v.kind == kindBool {
				return true
			}
		case "string":
			if v.kind == kindString {
				return true
			}
		case "number":
			if v.kind == kindNumber {
				return true
			}
		case "integer":
			if d, ok := v.decimal(); ok && d.IsInteger() {
				return true
			}
		case "object":
			if v.kind == kindObject {
				return true
			}
		case "array":
			if v.kind == kindArray {
				return true
			}
		}
	}
	return false
}

func (s *schema) enumMatches(v *value) bool {
	for _, e := range s.enum {
		if e.equal(v) {
			return true
		}
	}
	return false
}

func validFormat(format, s string) bool {
	switch format {
	case "uuid":
		return validUUID(s)
	case "date-time":
		return validDateTimeFormat(s)
	default:
		return true
	}
}

func appendPath(path []string, token string) []string {
	out := make([]string, len(path), len(path)+1)
	copy(out, path)
	return append(out, token)
}

// structuralField is lago's field_for: billing_items followed by the JSON
// pointer tokens (escaped), empty tokens dropped.
func structuralField(path []string) string {
	parts := []string{"billing_items"}
	for _, t := range path {
		if t == "" {
			continue
		}
		parts = append(parts, strings.NewReplacer("~", "~0", "/", "~1").Replace(t))
	}
	return strings.Join(parts, ".")
}
