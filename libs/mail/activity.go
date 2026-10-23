package mail

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Activity statuses of an [EmailActivity] (lago's Utils::EmailActivityLog).
const (
	// ActivitySent: delivered automatically or through the API.
	ActivitySent = "sent"
	// ActivityResent: delivered again on request.
	ActivityResent = "resent"
	// ActivityFailed: delivery failed for good.
	ActivityFailed = "failed"
)

// Activity sources.
const (
	// SourceAPI: triggered with an API key.
	SourceAPI = "api"
	// SourceFront: triggered by a signed-in user.
	SourceFront = "front"
	// SourceSystem: sent by Tanvo itself.
	SourceSystem = "system"
)

// ActivityType is the activity log type of a sent email.
const ActivityType = "email.sent"

// BodyPreviewLength bounds [EmailActivity.BodyPreview].
const BodyPreviewLength = 500

// DocumentRef identifies the document an email is about.
type DocumentRef struct {
	// Type is "invoice", "credit_note" or "payment_receipt".
	Type string
	// ID is the document's identifier.
	ID string
	// Number is the document number.
	Number string
}

// ActivityOptions describe who sent a document email and how it went.
type ActivityOptions struct {
	// Document is the emailed document; nil means no activity is recorded.
	Document *DocumentRef
	// Resend marks a manual resend.
	Resend bool
	// UserID is the signed-in user who triggered it.
	UserID string
	// APIKeyID is the API key that triggered it.
	APIKeyID string
	// Err is the final delivery error, if any.
	Err error
}

// EmailActivity is the "email.sent" activity log entry of a document
// email; the caller stores it in the activity log (libs/eventstore).
type EmailActivity struct {
	// Status is sent, resent or failed.
	Status string
	// Source is api, front or system.
	Source string
	// UserID is the triggering user, if any.
	UserID string
	// APIKeyID is the triggering API key, if any.
	APIKeyID string
	// Subject is the email subject.
	Subject string
	// To, Cc and Bcc are the recipient addresses.
	To, Cc, Bcc []string
	// BodyPreview is the start of the text (or tag-stripped HTML) body.
	BodyPreview string
	// Document is the emailed document.
	Document DocumentRef
	// Error is the delivery error message, if failed.
	Error string
}

// NewActivity returns the activity entry of m, or false when lago records
// none (no message or no document).
func NewActivity(m *Message, o ActivityOptions) (EmailActivity, bool) {
	if m == nil || o.Document == nil {
		return EmailActivity{}, false
	}
	a := EmailActivity{
		Status: ActivitySent, Source: SourceSystem, UserID: o.UserID, APIKeyID: o.APIKeyID,
		Subject: m.Subject, To: addrs(m.To), Cc: addrs(m.Cc), Bcc: addrs(m.Bcc),
		BodyPreview: BodyPreview(m), Document: *o.Document,
	}
	switch {
	case o.Err != nil:
		a.Status, a.Error = ActivityFailed, o.Err.Error()
	case o.Resend:
		a.Status = ActivityResent
	}
	switch {
	case o.APIKeyID != "":
		a.Source = SourceAPI
	case o.UserID != "":
		a.Source = SourceFront
	}
	return a, true
}

func addrs(as []Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Address)
	}
	return out
}

var (
	tagPattern   = regexp.MustCompile(`(?s)<[^>]*>`)
	stylePattern = regexp.MustCompile(`(?is)<(style|script)[^>]*>.*?</(style|script)>`)
	spacePattern = regexp.MustCompile(`\s+`)
)

// BodyPreview returns the text body, or the HTML body with tags stripped,
// with whitespace collapsed and truncated to [BodyPreviewLength]
// characters ending in "..." as Rails' truncate does.
func BodyPreview(m *Message) string {
	raw := m.Text
	if raw == "" {
		raw = stylePattern.ReplaceAllString(m.HTML, " ")
		raw = tagPattern.ReplaceAllString(strings.ReplaceAll(raw, "<", " <"), "")
		raw = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&#39;", "'", "&quot;", "\"").Replace(raw)
	}
	s := strings.TrimSpace(spacePattern.ReplaceAllString(raw, " "))
	if utf8.RuneCountInString(s) <= BodyPreviewLength {
		return s
	}
	r := []rune(s)
	return string(r[:BodyPreviewLength-3]) + "..."
}
