package mail

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	htmltemplate "html/template"
	netmail "net/mail"
	"net/url"
	"strings"
	texttemplate "text/template"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// ErrSkipped is matched (errors.Is) by the [SkipError] a builder returns
// when lago's mailer would not send the email.
var ErrSkipped = errors.New("email skipped")

// ErrFilesNotReady is returned when a document email needs PDF files that
// were not provided; the caller retries once libs/documents rendered them
// (lago's PaymentReceipts::FilesNotReadyError).
var ErrFilesNotReady = errors.New("document files not ready")

// Reasons of a [SkipError].
const (
	// ReasonEmailSettingDisabled: the billing entity does not send this
	// document by email.
	ReasonEmailSettingDisabled = "email_setting_disabled"
	// ReasonBillingEntityEmailMissing: the billing entity has no email,
	// used as reply-to.
	ReasonBillingEntityEmailMissing = "billing_entity_email_missing"
	// ReasonNoRecipient: nobody to send to.
	ReasonNoRecipient = "no_recipient"
	// ReasonZeroFeesAmount: invoices whose fees amount is zero are never
	// emailed.
	ReasonZeroFeesAmount = "zero_fees_amount"
	// ReasonNoChanges: nothing changed.
	ReasonNoChanges = "no_changes"
	// ReasonExportNotReady: the data export is not completed or expired.
	ReasonExportNotReady = "export_not_ready"
	// ReasonTokenMissing: the link token is missing.
	ReasonTokenMissing = "token_missing"
)

// SkipError explains why an email is not sent.
type SkipError struct {
	// Kind is the email that was skipped.
	Kind Kind
	// Reason is one of the Reason constants.
	Reason string
}

// Error implements error.
func (e *SkipError) Error() string { return fmt.Sprintf("%s email skipped: %s", e.Kind, e.Reason) }

// Is matches [ErrSkipped].
func (e *SkipError) Is(target error) bool { return target == ErrSkipped }

func skip(k Kind, reason string) error { return &SkipError{Kind: k, Reason: reason} }

// Config configures a [Composer].
type Config struct {
	// DefaultFrom is the instance's sender address, optionally with a
	// display name (lago's LAGO_FROM_EMAIL).
	DefaultFrom string
	// AppURL is the web console URL used in links (lago's
	// LAGO_FRONT_URL), for example "https://app.tanvo.example".
	AppURL string
	// DisablePDFs leaves out PDF attachments and download links (lago's
	// LAGO_DISABLE_PDF_GENERATION).
	DisablePDFs bool
}

// Composer builds messages from typed view data. It is safe for
// concurrent use.
type Composer struct {
	cfg         Config
	defaultFrom Address
	cat         *catalog
	html        map[Kind]*htmltemplate.Template
	text        map[Kind]*texttemplate.Template
}

// NewComposer parses the embedded templates and translations.
func NewComposer(cfg Config) (*Composer, error) {
	c := &Composer{cfg: cfg, html: map[Kind]*htmltemplate.Template{}, text: map[Kind]*texttemplate.Template{}}
	if strings.TrimSpace(cfg.DefaultFrom) != "" {
		a, err := netmail.ParseAddress(cfg.DefaultFrom)
		if err != nil {
			return nil, fmt.Errorf("mail: default from %q: %w", cfg.DefaultFrom, err)
		}
		c.defaultFrom = *a
	}
	if cfg.AppURL != "" {
		if _, err := url.Parse(cfg.AppURL); err != nil {
			return nil, fmt.Errorf("mail: app url: %w", err)
		}
	}
	cat, err := loadCatalog()
	if err != nil {
		return nil, fmt.Errorf("mail: %w", err)
	}
	c.cat = cat
	for _, k := range Kinds() {
		h, err := htmltemplate.New(string(k)).ParseFS(templateFS, "templates/layout.html.tmpl", "templates/"+string(k)+".html.tmpl")
		if err != nil {
			return nil, fmt.Errorf("mail: parse %s html: %w", k, err)
		}
		t, err := texttemplate.New(string(k)).ParseFS(templateFS, "templates/layout.txt.tmpl", "templates/"+string(k)+".txt.tmpl")
		if err != nil {
			return nil, fmt.Errorf("mail: parse %s text: %w", k, err)
		}
		c.html[k], c.text[k] = h, t
	}
	return c, nil
}

// Localizer returns a localizer for the locale resolved by
// [ResolveLocale].
func (c *Composer) Localizer(locale string) *Localizer {
	return &Localizer{cat: c.cat, locale: ResolveLocale(locale)}
}

// layoutData fills the shared layout.
type layoutData struct {
	// OrganizationName shows the organization header (lago shows it when
	// the mailer sets @organization).
	OrganizationName string
	// LogoURL is the organization logo next to the name.
	LogoURL string
	// ContactEmail adds the "Questions? Contact us at" footer.
	ContactEmail string
	// Wordmark shows the product wordmark above system emails.
	Wordmark bool
}

type view struct {
	L      *Localizer
	D      any
	Layout layoutData
	PDFs   bool
}

func (c *Composer) render(m *Message, l *Localizer, lay layoutData, data any) error {
	v := view{L: l, D: data, Layout: lay, PDFs: !c.cfg.DisablePDFs}
	var hb, tb bytes.Buffer
	if err := c.html[m.Kind].ExecuteTemplate(&hb, "layout", v); err != nil {
		return fmt.Errorf("render %s html (%s): %w", m.Kind, l.locale, err)
	}
	if err := c.text[m.Kind].ExecuteTemplate(&tb, "layout", v); err != nil {
		return fmt.Errorf("render %s text (%s): %w", m.Kind, l.locale, err)
	}
	m.HTML, m.Text, m.Locale = hb.String(), tb.String(), l.locale
	return nil
}

// link joins the app URL and a path.
func (c *Composer) link(path string) (string, error) {
	if c.cfg.AppURL == "" {
		return "", errors.New("mail: AppURL is not configured")
	}
	return strings.TrimRight(c.cfg.AppURL, "/") + path, nil
}

// systemFrom returns the instance sender, failing when none is set.
func (c *Composer) systemFrom() (Address, error) {
	if c.defaultFrom.Address == "" {
		return Address{}, errors.New("mail: no sender address configured (DefaultFrom)")
	}
	return c.defaultFrom, nil
}
