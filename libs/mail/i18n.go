package mail

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed locales/*.yaml
var localeFS embed.FS

// DefaultLocale is the locale used when a requested locale is not shipped,
// and the fallback for keys a locale does not translate.
const DefaultLocale = "en"

// Locales returns the shipped locales: lago-api's, in the order it lists
// them (config/application.rb:49), then zh-CN, a Tanvo addition.
func Locales() []string {
	return []string{"en", "fr", "nb", "de", "it", "es", "sv", "pt-BR", "zh-TW", "zh-CN"}
}

// catalog holds the translations of every shipped locale.
type catalog struct {
	locales map[string]map[string]any
}

func loadCatalog() (*catalog, error) {
	c := &catalog{locales: map[string]map[string]any{}}
	for _, loc := range Locales() {
		b, err := fs.ReadFile(localeFS, path.Join("locales", loc+".yaml"))
		if err != nil {
			return nil, fmt.Errorf("read locale %s: %w", loc, err)
		}
		var m map[string]any
		if err := yaml.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("parse locale %s: %w", loc, err)
		}
		c.locales[loc] = m
	}
	return c, nil
}

// ResolveLocale maps a BCP 47 tag to a shipped locale: an exact match
// (case-insensitive, "_" accepted for "-"), else Chinese by script (see
// [chineseLocale]), else the tag's language ("fr-CA" → "fr"), else
// [DefaultLocale] — the fallbacks Rails applies with config.i18n.fallbacks.
func ResolveLocale(tag string) string {
	tag = strings.ReplaceAll(strings.TrimSpace(tag), "_", "-")
	for _, l := range Locales() {
		if strings.EqualFold(l, tag) {
			return l
		}
	}
	lang, rest, _ := strings.Cut(tag, "-")
	if strings.EqualFold(lang, "zh") {
		return chineseLocale(rest)
	}
	for _, l := range Locales() {
		if strings.EqualFold(l, lang) {
			return l
		}
	}
	return DefaultLocale
}

// chineseLocale picks the Chinese locale for the subtags after "zh": the
// Traditional script (Hant), or a region writing it (TW, HK, MO), gives
// zh-TW; anything else, plain "zh" included, gives zh-CN.
func chineseLocale(subtags string) string {
	for st := range strings.SplitSeq(subtags, "-") {
		switch strings.ToUpper(st) {
		case "HANS":
			return "zh-CN"
		case "HANT", "TW", "HK", "MO":
			return "zh-TW"
		}
	}
	return "zh-CN"
}

func (c *catalog) lookup(locale, key string) (any, bool) {
	for _, loc := range []string{locale, DefaultLocale} {
		var cur any = c.locales[loc]
		for part := range strings.SplitSeq(key, ".") {
			m, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = m[part]
		}
		if cur != nil {
			return cur, true
		}
	}
	return nil, false
}

// Localizer translates and formats for one locale. Templates call its
// methods; every method fails on a missing key or argument, so a broken
// template never renders silently.
type Localizer struct {
	cat    *catalog
	locale string
}

// Locale returns the resolved locale.
func (l *Localizer) Locale() string { return l.locale }

// T returns the translation of key with its %{name} placeholders replaced
// by args, given as name, value pairs. A "count" argument selects the
// plural form (zero, one, other) of a pluralized key, as Rails I18n does.
func (l *Localizer) T(key string, args ...any) (string, error) {
	if len(args)%2 != 0 {
		return "", fmt.Errorf("translate %s: odd number of arguments", key)
	}
	vals := make(map[string]string, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			return "", fmt.Errorf("translate %s: argument name %v is not a string", key, args[i])
		}
		vals[name] = fmt.Sprint(args[i+1])
	}
	v, ok := l.cat.lookup(l.locale, key)
	if !ok {
		return "", fmt.Errorf("translate %s: missing translation for %s", key, l.locale)
	}
	if forms, ok := v.(map[string]any); ok {
		cs, ok := vals["count"]
		if !ok {
			return "", fmt.Errorf("translate %s: pluralized key needs a count", key)
		}
		n, err := strconv.Atoi(cs)
		if err != nil {
			return "", fmt.Errorf("translate %s: count %q: %w", key, cs, err)
		}
		form := "other"
		switch {
		case n == 0 && forms["zero"] != nil:
			form = "zero"
		case n == 1:
			form = "one"
		}
		v = forms[form]
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("translate %s: not a string", key)
	}
	return interpolate(key, s, vals)
}

func interpolate(key, s string, vals map[string]string) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(s, "%{")
		if i < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		name := s[i+2 : i+j]
		v, ok := vals[name]
		if !ok {
			return "", fmt.Errorf("translate %s: missing argument %q", key, name)
		}
		b.WriteString(s[:i])
		b.WriteString(v)
		s = s[i+j+1:]
	}
}

func (l *Localizer) str(key string) (string, error) {
	v, ok := l.cat.lookup(l.locale, key)
	if !ok {
		return "", fmt.Errorf("missing %s for %s", key, l.locale)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", key)
	}
	return s, nil
}

func (l *Localizer) monthName(key string, m int) (string, error) {
	v, ok := l.cat.lookup(l.locale, key)
	if !ok {
		return "", fmt.Errorf("missing %s for %s", key, l.locale)
	}
	list, ok := v.([]any)
	if !ok || m < 1 || m >= len(list) {
		return "", fmt.Errorf("%s has no month %d", key, m)
	}
	s, ok := list[m].(string)
	if !ok {
		return "", fmt.Errorf("%s month %d is not a string", key, m)
	}
	return s, nil
}
