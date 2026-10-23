// Package i18n holds the translations and the locale-dependent formatting of
// Tanvo's documents: the strings of invoices, credit notes, payment receipts
// and payment requests, dates, money amounts, numbers and postal addresses.
//
// The translations are lago-api's (config/locales, AGPL-3.0, ported
// verbatim with their source in each file's header) for the nine document
// locales de, en, es, fr, it, nb, pt-BR, sv and zh-TW, plus the few
// strings Tanvo adds (locales/*/tanvo.yml). Lookups follow Ruby I18n:
// %{name} interpolation, the zero/one/other plural keys and a fallback to
// English for keys a locale lacks.
//
// zh-CN is a Tanvo locale lago-api does not have (config/application.rb:49):
// its translations use mainland terminology and its formats follow Unicode
// CLDR zh-Hans-CN. [ResolveLocale] maps Chinese tags to zh-CN or zh-TW by
// script and region.
//
// Formatting ports lago-api's view helpers: MoneyHelper (the money gem's
// formatter with the locale's marks and the "%u%n" or "%{iso_code} %n"
// format), RoundingHelper, DateHelper (I18n.l with the locale's date
// names) and the addressing gem's default formatter for customer
// addresses, with the country names of Unicode CLDR (data/SOURCES).
package i18n
