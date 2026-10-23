package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func sampleMessage() *Message {
	return &Message{
		Kind:    KindInvoiceCreated,
		From:    Address{Name: "Société Générale", Address: "noreply@tanvo.test"},
		ReplyTo: []Address{{Name: "ACME", Address: "billing@acme.test"}},
		To:      []Address{{Address: "a@example.com"}, {Name: "Bé", Address: "b@example.com"}},
		Cc:      []Address{{Address: "c@example.com"}},
		Bcc:     []Address{{Address: "hidden@example.com"}},
		Subject: "Votre facture n° 42 — €1 234,50",
		Text:    "Bonjour,\nvoici votre facture: " + strings.Repeat("x", 120),
		HTML:    "<p>Bonjour</p>",
	}
}

func TestEncode(t *testing.T) {
	date := time.Date(2024, time.October, 3, 12, 0, 0, 0, time.UTC)
	for _, withAttachment := range []bool{false, true} {
		m := sampleMessage()
		pdf := bytes.Repeat([]byte{0, 1, 2, 250}, 100)
		if withAttachment {
			m.Attachments = []Attachment{{Filename: "facture-n°42.pdf", ContentType: "application/pdf", Data: pdf}}
		}
		raw, err := m.Encode(EncodeOptions{Date: date, MessageID: "id-1@tanvo.test"})
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		if bytes.Contains(raw, []byte("hidden@example.com")) {
			t.Errorf("encoded message reveals the Bcc")
		}
		for _, line := range bytes.Split(raw, []byte("\r\n")) {
			if len(line) > 998 {
				t.Errorf("line of %d octets exceeds RFC 5322", len(line))
			}
		}
		pm, err := netmail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("ReadMessage() error = %v", err)
		}
		dec := new(mime.WordDecoder)
		subj, err := dec.DecodeHeader(pm.Header.Get("Subject"))
		if err != nil || subj != m.Subject {
			t.Errorf("Subject = %q (%v), want = %q", subj, err, m.Subject)
		}
		from, err := pm.Header.AddressList("From")
		if err != nil || len(from) != 1 || *from[0] != m.From {
			t.Errorf("From = %v (%v), want = %v", from, err, m.From)
		}
		to, err := pm.Header.AddressList("To")
		if err != nil || len(to) != 2 || to[1].Name != "Bé" {
			t.Errorf("To = %v (%v), want two addresses", to, err)
		}
		if got := pm.Header.Get("Message-ID"); got != "<id-1@tanvo.test>" {
			t.Errorf("Message-ID = %q, want = <id-1@tanvo.test>", got)
		}
		if d, err := pm.Header.Date(); err != nil || !d.Equal(date) {
			t.Errorf("Date = %v (%v), want = %v", d, err, date)
		}
		if got := pm.Header.Get("MIME-Version"); got != "1.0" {
			t.Errorf("MIME-Version = %q, want = 1.0", got)
		}

		mt, params, err := mime.ParseMediaType(pm.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("Content-Type: %v", err)
		}
		alt := multipart.NewReader(pm.Body, params["boundary"])
		if withAttachment {
			if mt != "multipart/mixed" {
				t.Fatalf("Content-Type = %s, want = multipart/mixed", mt)
			}
			mixed := alt
			p, err := mixed.NextPart()
			if err != nil {
				t.Fatalf("first part: %v", err)
			}
			_, ap, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			alt = multipart.NewReader(p, ap["boundary"])
			readAlternative(t, alt, m)
			a, err := mixed.NextPart()
			if err != nil {
				t.Fatalf("attachment part: %v", err)
			}
			if got := a.FileName(); got != "facture-n°42.pdf" {
				t.Errorf("FileName() = %q, want = facture-n°42.pdf", got)
			}
			b, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, a))
			if !bytes.Equal(b, pdf) {
				t.Errorf("attachment data differs after decoding")
			}
			continue
		}
		if mt != "multipart/alternative" {
			t.Fatalf("Content-Type = %s, want = multipart/alternative", mt)
		}
		readAlternative(t, alt, m)
	}
}

func readAlternative(t *testing.T, r *multipart.Reader, m *Message) {
	t.Helper()
	for _, want := range []struct{ ct, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		p, err := r.NextRawPart()
		if err != nil {
			t.Fatalf("NextRawPart() error = %v", err)
		}
		if ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type")); ct != want.ct {
			t.Errorf("part type = %s, want = %s", ct, want.ct)
		}
		b, err := io.ReadAll(quotedprintable.NewReader(p))
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		if got := strings.ReplaceAll(string(b), "\r\n", "\n"); got != want.body {
			t.Errorf("%s body = %q, want = %q", want.ct, got, want.body)
		}
	}
}

func TestEncodeSinglePart(t *testing.T) {
	m := sampleMessage()
	m.HTML = ""
	raw, err := m.Encode(EncodeOptions{})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	pm, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	if ct, _, _ := mime.ParseMediaType(pm.Header.Get("Content-Type")); ct != "text/plain" {
		t.Errorf("Content-Type = %s, want = text/plain", ct)
	}
	if got := pm.Header.Get("Content-Transfer-Encoding"); got != "quoted-printable" {
		t.Errorf("Content-Transfer-Encoding = %q, want = quoted-printable", got)
	}
	if id := pm.Header.Get("Message-ID"); !strings.HasSuffix(id, "@tanvo.test>") {
		t.Errorf("Message-ID = %q, want a generated id at the sender's domain", id)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Message)
	}{
		{"no sender", func(m *Message) { m.From = Address{} }},
		{"no recipient", func(m *Message) { m.To, m.Cc, m.Bcc = nil, nil, nil }},
		{"no body", func(m *Message) { m.Text, m.HTML = "", "" }},
		{"header injection in subject", func(m *Message) { m.Subject = "Hi\r\nBcc: evil@example.com" }},
		{"header injection in address", func(m *Message) { m.To[0].Address = "a@example.com\r\nX: y" }},
		{"bad attachment name", func(m *Message) { m.Attachments = []Attachment{{Filename: "", Data: []byte("x")}} }},
	}
	for _, tt := range tests {
		m := sampleMessage()
		tt.mutate(m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", tt.name)
		}
	}
	if err := sampleMessage().Validate(); err != nil {
		t.Errorf("Validate() = %v, want = nil", err)
	}
}

func TestRecipients(t *testing.T) {
	m := sampleMessage()
	m.Cc = append(m.Cc, Address{Address: "A@example.com"})
	want := []string{"a@example.com", "b@example.com", "c@example.com", "hidden@example.com"}
	if got := m.Recipients(); !slices.Equal(got, want) {
		t.Errorf("Recipients() = %v, want = %v", got, want)
	}
}

func TestMemorySenderAndPortMailer(t *testing.T) {
	s := NewMemorySender()
	pm := NewPortMailer(s, slog.New(slog.DiscardHandler))
	e := port.Email{
		From: "Tanvo <noreply@tanvo.test>", ReplyTo: "billing@acme.test",
		To: []string{"a@example.com, b@example.com"}, Bcc: []string{"c@example.com"},
		Subject: "Hello", Text: "Hi", Attachments: []port.Attachment{{Name: "a.pdf", ContentType: "application/pdf", Body: []byte("x")}},
	}
	if err := pm.Send(t.Context(), core.OrganizationID{}, e); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	got := s.Messages()
	if len(got) != 1 {
		t.Fatalf("len(Messages()) = %d, want = 1", len(got))
	}
	if m := got[0]; m.From.Name != "Tanvo" || len(m.To) != 2 || len(m.Bcc) != 1 || len(m.Attachments) != 1 || m.ReplyTo[0].Address != "billing@acme.test" {
		t.Errorf("message = %+v, want the converted email", m)
	}

	boom := errors.New("boom")
	s.FailWith(boom)
	if err := pm.Send(t.Context(), core.OrganizationID{}, e); !errors.Is(err, boom) {
		t.Errorf("Send() error = %v, want = %v", err, boom)
	}
	s.FailWith(nil)
	if err := pm.Send(t.Context(), core.OrganizationID{}, port.Email{From: "bad", To: []string{"a@example.com"}}); err == nil {
		t.Errorf("Send(bad from) error = nil, want an error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Send(ctx, sampleMessage()); !errors.Is(err, context.Canceled) {
		t.Errorf("Send(canceled) error = %v, want = %v", err, context.Canceled)
	}
	s.Reset()
	if n := len(s.Messages()); n != 0 {
		t.Errorf("len(Messages()) after Reset = %d, want = 0", n)
	}
}
