package documents

import (
	"regexp"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// newWriter returns the writer of a document of p in locale (the
// customer's applicable document locale when empty).
func newWriter(cat *i18n.Catalog, p *Party, locale string, cur money.Currency) *writer {
	if locale == "" {
		locale = documentLocale(p)
	}
	tz := p.Customer.ApplicableTimezone(customer.Defaults{Timezone: p.BillingEntity.Timezone})
	return &writer{l: cat.Locale(locale), cat: cat, cur: cur, tz: tz, subs: map[core.SubscriptionID]*Subscription{}}
}

func documentLocale(p *Party) string {
	if s := present(p.Customer.DocumentLocale); s != "" {
		return s
	}
	return p.BillingEntity.DocumentLocale
}

// billingEntityLines is the billing entity's column of every document.
func (w *writer) billingEntityLines(be *organization.BillingEntity) []string {
	name := present(be.LegalName)
	if name == "" {
		name = be.Name
	}
	lines := []string{name, present(be.LegalNumber)}
	lines = append(lines, w.simpleAddress(be.Address)...)
	lines = append(lines, present(be.Email))
	if tin := present(be.TaxID); tin != "" {
		lines = append(lines, w.l.T("invoice.tax_identification_number", i18n.A("tax_identification_number", tin)))
	}
	return nonEmpty(lines)
}

// simpleAddress is the address block of billing entities, and of customers
// on credit notes and receipts: lines, "zip, city", state, country.
func (w *writer) simpleAddress(a core.Address) []string {
	zipCity := a.Zipcode
	if a.Zipcode != "" && a.City != "" {
		zipCity += ", "
	}
	zipCity += a.City
	var country string
	if a.Country != "" {
		country = w.cat.CommonCountryName(a.Country)
	}
	return nonEmpty([]string{a.Line1, a.Line2, zipCity, a.State, country})
}

var emailSeparator = regexp.MustCompile(`,\s*`)

// customerLines is the customer's column. formatted selects the addressing
// formatter (invoices) over the simple layout (credit notes, receipts);
// rawEmail keeps the email list as stored.
func (w *writer) customerLines(c *customer.Customer, formatted, rawEmail bool) []string {
	lines := []string{c.DisplayName(true), present(c.LegalNumber)}
	if formatted {
		lines = append(lines, w.l.FormatAddress(c.BillingAddress, present(c.DocumentLocale))...)
	} else {
		lines = append(lines, w.simpleAddress(c.BillingAddress)...)
	}
	if c.Email != nil {
		if rawEmail {
			lines = append(lines, *c.Email)
		} else {
			lines = append(lines, emailSeparator.ReplaceAllString(*c.Email, ", "))
		}
	}
	if tin := present(c.TaxIdentificationNumber); tin != "" {
		lines = append(lines, w.l.T("invoice.tax_identification_number", i18n.A("tax_identification_number", tin)))
	}
	return nonEmpty(lines)
}

// metadataRows are the customer metadata printed on invoices.
func metadataRows(c *customer.Customer) [][2]string {
	var out [][2]string
	for _, m := range c.Metadata {
		if m.DisplayInInvoice {
			out = append(out, [2]string{m.Key, m.Value})
		}
	}
	return out
}

func (w *writer) newLayout(title string, p *Party, withLogo bool) *layout {
	lang, region := language(w.l.Tag())
	l := &layout{Lang: lang, Region: region, Fonts: fontChain(w.l.Tag()), locale: w.l.Tag(), Title: title, Info: [][2]string{}, Meta: [][2]string{}}
	if withLogo && p.Logo != nil && len(p.Logo.Data) > 0 {
		l.Logo = "logo" // the renderer appends the extension
	}
	return l
}

func (w *writer) footer(l *layout, number string, text []string) {
	if len(text) > 0 {
		l.Blocks = append(l.Blocks, block{Kind: blockText, Style: "body3", Lines: text})
	}
	l.Footer = footer{Number: number, Page: pageNumbering(w.l)}
}

func (w *writer) localTime(t *time.Time) core.Date {
	if t == nil {
		return core.Date{}
	}
	return w.localDate(*t)
}
