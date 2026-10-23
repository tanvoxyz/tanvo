package tax

import (
	"strings"
	"unicode"
)

// transliterations maps the Latin letters with diacritics most likely in
// jurisdiction names to ASCII.
var transliterations = map[rune]string{
	'À': "A", 'Á': "A", 'Â': "A", 'Ã': "A", 'Ä': "A", 'Å': "A", 'Æ': "AE",
	'Ç': "C", 'È': "E", 'É': "E", 'Ê': "E", 'Ë': "E", 'Ì': "I", 'Í': "I",
	'Î': "I", 'Ï': "I", 'Ð': "D", 'Ñ': "N", 'Ò': "O", 'Ó': "O", 'Ô': "O",
	'Õ': "O", 'Ö': "O", 'Ø': "O", 'Ù': "U", 'Ú': "U", 'Û': "U", 'Ü': "U",
	'Ý': "Y", 'Þ': "TH", 'ß': "ss", 'à': "a", 'á': "a", 'â': "a", 'ã': "a",
	'ä': "a", 'å': "a", 'æ': "ae", 'ç': "c", 'è': "e", 'é': "e", 'ê': "e",
	'ë': "e", 'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ð': "d", 'ñ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ù': "u",
	'ú': "u", 'û': "u", 'ü': "u", 'ý': "y", 'þ': "th", 'ÿ': "y",
	'Ā': "A", 'ā': "a", 'Ă': "A", 'ă': "a", 'Ą': "A", 'ą': "a", 'Ć': "C",
	'ć': "c", 'Č': "C", 'č': "c", 'Ď': "D", 'ď': "d", 'Đ': "D", 'đ': "d",
	'Ē': "E", 'ē': "e", 'Ė': "E", 'ė': "e", 'Ę': "E", 'ę': "e", 'Ě': "E",
	'ě': "e", 'Ğ': "G", 'ğ': "g", 'Ģ': "G", 'ģ': "g", 'Ī': "I", 'ī': "i",
	'Į': "I", 'į': "i", 'İ': "I", 'ı': "i", 'Ķ': "K", 'ķ': "k", 'Ĺ': "L",
	'ĺ': "l", 'Ļ': "L", 'ļ': "l", 'Ľ': "L", 'ľ': "l", 'Ł': "L", 'ł': "l",
	'Ń': "N", 'ń': "n", 'Ņ': "N", 'ņ': "n", 'Ň': "N", 'ň': "n", 'Ō': "O",
	'ō': "o", 'Ő': "O", 'ő': "o", 'Œ': "OE", 'œ': "oe", 'Ŕ': "R", 'ŕ': "r",
	'Ř': "R", 'ř': "r", 'Ś': "S", 'ś': "s", 'Ş': "S", 'ş': "s", 'Š': "S",
	'š': "s", 'Ţ': "T", 'ţ': "t", 'Ť': "T", 'ť': "t", 'Ū': "U", 'ū': "u",
	'Ů': "U", 'ů': "u", 'Ű': "U", 'ű': "u", 'Ų': "U", 'ų': "u", 'Ÿ': "Y",
	'Ź': "Z", 'ź': "z", 'Ż': "Z", 'ż': "z", 'Ž': "Z", 'ž': "z", 'Ș': "S",
	'ș': "s", 'Ț': "T", 'ț': "t",
}

// parameterize turns s into a lowercase identifier: letters are
// transliterated to ASCII, every run of characters other than ASCII letters,
// digits, '-' and '_' becomes sep, and repeated or surrounding separators are
// dropped. "Campione d'Italia" gives "campione_d_italia".
func parameterize(s, sep string) string {
	var b strings.Builder
	pending := false
	for _, r := range s {
		if t, ok := transliterations[r]; ok {
			if pending && b.Len() > 0 {
				b.WriteString(sep)
			}
			pending = false
			b.WriteString(strings.ToLower(t))
			continue
		}
		switch {
		case r < unicode.MaxASCII && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'):
			if string(r) == sep {
				pending = true
				continue
			}
			if pending && b.Len() > 0 {
				b.WriteString(sep)
			}
			pending = false
			b.WriteRune(unicode.ToLower(r))
		default:
			pending = true
		}
	}
	return b.String()
}

// underscore turns a camelCase or PascalCase word into snake_case, and '-'
// into '_': "notCollecting" gives "not_collecting".
func underscore(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if i > 0 && unicode.IsUpper(r) {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('_')
			}
		}
		if r == '-' {
			r = '_'
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// humanize turns a snake_case word into a sentence: underscores become
// spaces and the first letter is capitalized ("not_collecting" gives "Not
// collecting").
func humanize(s string) string {
	s = strings.TrimLeft(s, "_")
	s = strings.TrimSuffix(s, "_id")
	s = strings.ToLower(strings.ReplaceAll(s, "_", " "))
	rs := []rune(s)
	if len(rs) > 0 {
		rs[0] = unicode.ToUpper(rs[0])
	}
	return string(rs)
}
