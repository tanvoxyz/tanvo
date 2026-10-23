// Package mail composes and delivers Tanvo's emails (architecture §3.2,
// parity §1.47).
//
// A [Composer] turns typed view data into a [Message]: one builder per
// lago-api mailer (invoice, credit note, payment receipt, payment request,
// password reset, API key created/rotated/destroyed, data export,
// authentication methods updated, admin feature toggled) plus the
// invitation email Tanvo adds. Bodies are rendered from embedded HTML and
// text templates in every locale lago ships (de, en, es, fr, it, nb, pt-BR,
// sv, zh-TW), falling back to English for keys a locale lacks. Builders
// apply the billing entity's email settings and the from, reply-to and
// recipient rules of lago's mailers; when lago would not send, they return
// an error matching [ErrSkipped].
//
// Attachments (PDF and XML documents) are passed in as bytes; rendering
// them belongs to libs/documents.
//
// A [Sender] delivers messages: [SMTPSender] over SMTP with PLAIN or LOGIN
// authentication and STARTTLS or implicit TLS, and [MemorySender] for
// tests. [PortMailer] adapts a Sender to the core port.Mailer interface.
package mail
