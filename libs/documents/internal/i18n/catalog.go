package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed locales/*/*.yml data/countries.json
var files embed.FS

// DefaultLocale is the locale used for unknown locales and missing keys.
const DefaultLocale = "en"

// Catalog holds every locale's translations and the country data. It is
// immutable once loaded and safe for concurrent use.
type Catalog struct {
	locales   map[string]*Locale
	countries countryData
}

type countryData struct {
	// Names are the country names by document locale (CLDR).
	Names map[string]map[string]string `json:"names"`
	// CommonNames are the English common names of the countries gem.
	CommonNames map[string]string `json:"common_names"`
	// AddressFormats are the address formats by country.
	AddressFormats map[string]addressFormat `json:"address_formats"`
}

type addressFormat struct {
	Format      string `json:"format"`
	LocalFormat string `json:"local_format"`
	Locale      string `json:"locale"`
}

// Load parses the embedded translations and country data.
func Load() (*Catalog, error) {
	c := &Catalog{locales: map[string]*Locale{}}
	dirs, err := fs.ReadDir(files, "locales")
	if err != nil {
		return nil, fmt.Errorf("i18n: read locales: %w", err)
	}
	for _, d := range dirs {
		tag := d.Name()
		tree := map[string]any{}
		entries, err := fs.ReadDir(files, path.Join("locales", tag))
		if err != nil {
			return nil, fmt.Errorf("i18n: read locale %s: %w", tag, err)
		}
		for _, e := range entries {
			b, err := files.ReadFile(path.Join("locales", tag, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("i18n: read %s/%s: %w", tag, e.Name(), err)
			}
			var doc map[string]any
			if err := yaml.Unmarshal(b, &doc); err != nil {
				return nil, fmt.Errorf("i18n: parse %s/%s: %w", tag, e.Name(), err)
			}
			root, ok := doc[tag].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("i18n: %s/%s: missing root key %q", tag, e.Name(), tag)
			}
			merge(tree, root)
		}
		c.locales[tag] = &Locale{tag: tag, tree: tree, catalog: c}
	}
	en, ok := c.locales[DefaultLocale]
	if !ok {
		return nil, fmt.Errorf("i18n: missing locale %q", DefaultLocale)
	}
	for tag, l := range c.locales {
		if tag != DefaultLocale {
			l.fallback = en
		}
	}
	b, err := files.ReadFile("data/countries.json")
	if err != nil {
		return nil, fmt.Errorf("i18n: read countries: %w", err)
	}
	if err := json.Unmarshal(b, &c.countries); err != nil {
		return nil, fmt.Errorf("i18n: parse countries: %w", err)
	}
	for _, l := range c.locales {
		if err := l.loadFormats(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func merge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				merge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// Locales returns the supported locale tags, sorted.
func (c *Catalog) Locales() []string {
	tags := make([]string, 0, len(c.locales))
	for t := range c.locales {
		tags = append(tags, t)
	}
	slices.Sort(tags)
	return tags
}

// Supported reports whether tag resolves to a document locale
// ([ResolveLocale]).
func (c *Catalog) Supported(tag string) bool {
	_, ok := c.locales[ResolveLocale(tag)]
	return ok
}

// Locale returns the locale tag resolves to ([ResolveLocale]), or English
// when it resolves to none.
func (c *Catalog) Locale(tag string) *Locale {
	if l, ok := c.locales[ResolveLocale(tag)]; ok {
		return l
	}
	return c.locales[DefaultLocale]
}

// Locale translates and formats in one document locale.
type Locale struct {
	tag      string
	tree     map[string]any
	fallback *Locale
	catalog  *Catalog
	money    moneyFormat
	date     dateFormat
}

// Tag returns the locale tag, such as "pt-BR".
func (l *Locale) Tag() string { return l.tag }

// Arg is a named interpolation argument of a translation.
type Arg struct {
	// Name is the placeholder name, without %{ }.
	Name string
	// Value is the text substituted for the placeholder.
	Value string
}

// A returns the interpolation argument name = value.
func A(name, value string) Arg { return Arg{Name: name, Value: value} }

// Has reports whether key resolves to a string in l or its fallback.
func (l *Locale) Has(key string) bool {
	_, ok := l.lookup(key).(string)
	return ok
}

// T translates key, interpolating args. A key missing in l and in English
// returns "translation missing: <locale>.<key>", as Ruby I18n does.
func (l *Locale) T(key string, args ...Arg) string {
	v, ok := l.lookup(key).(string)
	if !ok {
		return "translation missing: " + l.tag + "." + key
	}
	return interpolate(v, args)
}

// Plural translates the pluralized key for count, as Ruby I18n's default
// pluralization: "zero" when count is 0 and the key has it, "one" when count
// is 1, "other" otherwise. The count is also available as %{count}.
func (l *Locale) Plural(key string, count int64, args ...Arg) string {
	m, ok := l.lookup(key).(map[string]any)
	if !ok {
		return "translation missing: " + l.tag + "." + key
	}
	form := "other"
	if _, hasZero := m["zero"]; count == 0 && hasZero {
		form = "zero"
	} else if count == 1 {
		form = "one"
	}
	v, ok := m[form].(string)
	if !ok {
		return "translation missing: " + l.tag + "." + key + "." + form
	}
	return interpolate(v, append([]Arg{A("count", strconv.FormatInt(count, 10))}, args...))
}

func (l *Locale) lookup(key string) any {
	for loc := l; loc != nil; loc = loc.fallback {
		if v := walk(loc.tree, key); v != nil {
			return v
		}
	}
	return nil
}

func walk(tree map[string]any, key string) any {
	var cur any = tree
	for part := range strings.SplitSeq(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[part]
		if !ok {
			return nil
		}
	}
	return cur
}

// interpolate replaces %{name} placeholders; unknown placeholders are kept.
func interpolate(s string, args []Arg) string {
	if !strings.Contains(s, "%{") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "%{")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		name := s[i+2 : i+j]
		b.WriteString(s[:i])
		if v, ok := argValue(args, name); ok {
			b.WriteString(v)
		} else {
			b.WriteString(s[i : i+j+1])
		}
		s = s[i+j+1:]
	}
}

func argValue(args []Arg, name string) (string, bool) {
	for _, a := range args {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

func (l *Locale) str(key string) string {
	v, _ := l.lookup(key).(string)
	return v
}

func (l *Locale) list(key string) []string {
	raw, _ := l.lookup(key).([]any)
	out := make([]string, len(raw))
	for i, v := range raw {
		if s, ok := v.(string); ok {
			out[i] = s
		}
	}
	return out
}
