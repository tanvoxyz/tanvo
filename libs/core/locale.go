package core

import "strings"

// ValidLocale reports whether s looks like a BCP 47 language tag: a 2–3
// letter language, optionally followed by subtags of 2–8 letters or digits
// ("en", "pt-BR", "zh-Hans-CN"). Only the syntax is checked.
func ValidLocale(s string) bool {
	parts := strings.Split(s, "-")
	if l := len(parts[0]); l < 2 || l > 3 || !localeSubtag(parts[0], false) {
		return false
	}
	for _, p := range parts[1:] {
		if len(p) < 2 || len(p) > 8 || !localeSubtag(p, true) {
			return false
		}
	}
	return true
}

func localeSubtag(s string, digits bool) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case digits && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
