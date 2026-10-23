package documents

import (
	"errors"
	"fmt"
	"slices"
)

// fontChain is the font fallback chain of a document locale: Inter
// (embedded), then for Chinese the Noto CJK families of the locale's
// script, first as fonts-noto-cjk installs them (Noto Sans CJK SC/TC, the
// family names Typst lists for NotoSansCJK-*.ttc), then as Google Fonts
// ships them (Noto Sans SC/TC).
//
// lago-api asks only for Inter (font-family: 'Inter' in
// app/views/templates/invoices/v4.slim:11 and credit_notes/_styles.slim:3)
// and renders through Gotenberg's Chromium
// (app/services/utils/pdf_generator.rb:34, /forms/chromium/convert/html;
// image getlago/lago-gotenberg:8.15 in lago's deploy/docker-compose.production.yml:457),
// whose image ships fonts-noto-cjk: Chinese falls back to Noto Sans CJK
// implicitly. Tanvo names the fallback and does not embed the CJK fonts
// (tens of megabytes); the container image installs fonts-noto-cjk, found
// through [Options.FontPaths]. zh-CN is a Tanvo locale, not lago-api's
// (config/application.rb:49).
func fontChain(locale string) []string {
	return append([]string{"Inter"}, cjkFamilies(locale)...)
}

// cjkFamilies are the CJK families of fontChain, nil for non-Chinese
// locales.
func cjkFamilies(locale string) []string {
	switch locale {
	case "zh-CN":
		return []string{"Noto Sans CJK SC", "Noto Sans SC"}
	case "zh-TW":
		return []string{"Noto Sans CJK TC", "Noto Sans TC"}
	}
	return nil
}

// ErrMissingFont reports that a document needs a font family none of the
// renderer's font paths provides.
var ErrMissingFont = errors.New("documents: missing font")

// MissingFontError is a document whose locale needs one of Families.
type MissingFontError struct {
	// Locale is the document locale.
	Locale string
	// Families are the acceptable font families.
	Families []string
}

func (e *MissingFontError) Error() string {
	return fmt.Sprintf("documents: locale %s needs one of the fonts %q; install fonts-noto-cjk or add its directory to Options.FontPaths", e.Locale, e.Families)
}

// Is makes a MissingFontError match [ErrMissingFont].
func (e *MissingFontError) Is(target error) bool { return target == ErrMissingFont }

// checkFonts fails when the document's locale needs a CJK font the engine
// does not have, rather than rendering missing glyphs.
func (r *Renderer) checkFonts(locale string) error {
	fams := cjkFamilies(locale)
	if fams == nil || slices.ContainsFunc(fams, r.engine.HasFamily) {
		return nil
	}
	return &MissingFontError{Locale: locale, Families: fams}
}
