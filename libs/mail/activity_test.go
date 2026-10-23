package mail

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNewActivity(t *testing.T) {
	msg := func() *Message {
		return &Message{
			Subject: "Test Subject", To: []Address{{Address: "to@example.com"}},
			HTML: "<h1>Hello</h1><p>Body preview</p>",
		}
	}
	inv := &DocumentRef{Type: "Invoice", ID: "inv-1", Number: "INV-1"}
	tests := []struct {
		name       string
		m          *Message
		o          ActivityOptions
		wantOK     bool
		wantStatus string
		wantSource string
		wantErr    string
	}{
		// source: spec/services/utils/email_activity_log_spec.rb:40
		{"sent by default", msg(), ActivityOptions{Document: inv}, true, ActivitySent, SourceSystem, ""},
		// source: spec/services/utils/email_activity_log_spec.rb:51
		{"resent", msg(), ActivityOptions{Document: inv, Resend: true}, true, ActivityResent, SourceSystem, ""},
		// source: spec/services/utils/email_activity_log_spec.rb:60
		{"failed", msg(), ActivityOptions{Document: inv, Err: errors.New("SMTP failed")}, true, ActivityFailed, SourceSystem, "SMTP failed"},
		// source: spec/services/utils/email_activity_log_spec.rb:70
		{"api source", msg(), ActivityOptions{Document: inv, APIKeyID: "key-123"}, true, ActivitySent, SourceAPI, ""},
		// source: spec/services/utils/email_activity_log_spec.rb:78
		{"front source", msg(), ActivityOptions{Document: inv, UserID: "user-456"}, true, ActivitySent, SourceFront, ""},
		// source: spec/services/utils/email_activity_log_spec.rb:94
		{"document nil", msg(), ActivityOptions{}, false, "", "", ""},
		// source: spec/services/utils/email_activity_log_spec.rb:100
		{"message nil", nil, ActivityOptions{Document: inv}, false, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, ok := NewActivity(tt.m, tt.o)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want = %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if a.Status != tt.wantStatus || a.Source != tt.wantSource || a.Error != tt.wantErr {
				t.Errorf("status, source, error = %q, %q, %q, want = %q, %q, %q", a.Status, a.Source, a.Error, tt.wantStatus, tt.wantSource, tt.wantErr)
			}
			if a.Subject != "Test Subject" || !slices.Equal(a.To, []string{"to@example.com"}) || a.Document != *inv {
				t.Errorf("activity = %+v, want subject, to and document copied", a)
			}
		})
	}

	// source: spec/services/utils/email_activity_log_spec.rb:142 and :278
	for _, ref := range []DocumentRef{{Type: "CreditNote", ID: "cn-1", Number: "CN-1"}, {Type: "PaymentReceipt", ID: "pr-1", Number: "PR-1"}} {
		a, ok := NewActivity(msg(), ActivityOptions{Document: &ref})
		if !ok || a.Document != ref {
			t.Errorf("Document = %+v, want = %+v", a.Document, ref)
		}
	}
}

func TestBodyPreview(t *testing.T) {
	tests := []struct {
		name string
		m    Message
		want string
	}{
		// source: spec/services/utils/email_activity_log_spec.rb:155
		{"html only", Message{HTML: "<h1>Hello</h1><p>Body preview</p>"}, "Hello Body preview"},
		// source: spec/services/utils/email_activity_log_spec.rb:179
		{"prefers text", Message{Text: "Plain text body", HTML: "<p>HTML body</p>"}, "Plain text body"},
		// source: spec/services/utils/email_activity_log_spec.rb:203
		{"simple body", Message{Text: "Simple body text"}, "Simple body text"},
		// source: spec/services/utils/email_activity_log_spec.rb:227
		{"empty", Message{}, ""},
		{"styles, entities and whitespace", Message{HTML: "<style>p{color:red}</style><p>Fish &amp;\n\n chips</p>"}, "Fish & chips"},
	}
	for _, tt := range tests {
		if got := BodyPreview(&tt.m); got != tt.want {
			t.Errorf("%s: BodyPreview() = %q, want = %q", tt.name, got, tt.want)
		}
	}

	// source: spec/services/utils/email_activity_log_spec.rb:253
	got := BodyPreview(&Message{HTML: "<p>" + strings.Repeat("é", 600) + "</p>"})
	if n := utf8.RuneCountInString(got); n != BodyPreviewLength || !strings.HasSuffix(got, "...") {
		t.Errorf("long preview has %d characters ending %q, want = %d ending ...", n, got[len(got)-3:], BodyPreviewLength)
	}
}
