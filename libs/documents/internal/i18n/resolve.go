package i18n

import "strings"

// documentLocales are the supported document locales: lago-api's nine
// (config/application.rb:49) and zh-CN, a Tanvo addition.
func documentLocales() []string {
	return []string{"de", "en", "es", "fr", "it", "nb", "pt-BR", "sv", "zh-CN", "zh-TW"}
}

// ResolveLocale maps a BCP 47 tag ("zh-Hant-HK", "pt_BR", "en-US") to a
// document locale, or "" when none fits. Matching ignores case and accepts
// "_" for "-". Chinese is resolved by script: Hans, or no script with
// region CN, SG or none, is zh-CN; Hant, or no script with region TW, HK
// or MO, is zh-TW (plain "zh" is zh-CN). Other languages resolve to their
// only document locale whatever the region ("pt-PT" is pt-BR; "no" and
// "nn" are nb). libs/mail resolves tags with the same rules.
func ResolveLocale(tag string) string {
	parts := strings.FieldsFunc(tag, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 {
		return ""
	}
	lang := strings.ToLower(parts[0])
	var script, region string
	for _, p := range parts[1:] {
		switch {
		case len(p) == 4 && script == "" && region == "":
			script = strings.ToLower(p)
		case (len(p) == 2 || len(p) == 3 && p[0] >= '0' && p[0] <= '9') && region == "":
			region = strings.ToUpper(p)
		}
	}
	switch lang {
	case "zh":
		switch script {
		case "hans":
			return "zh-CN"
		case "hant":
			return "zh-TW"
		case "":
			switch region {
			case "TW", "HK", "MO":
				return "zh-TW"
			case "", "CN", "SG":
				return "zh-CN"
			}
		}
		return ""
	case "no", "nn", "nb":
		return "nb"
	case "pt":
		return "pt-BR"
	}
	for _, l := range documentLocales() {
		if l == lang {
			return l
		}
	}
	return ""
}
