package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"time"
)

// EncodeOptions controls the generated headers of [Message.Encode].
type EncodeOptions struct {
	// Date is the Date header; the zero time means time.Now.
	Date time.Time
	// Rand is the source of the Message-ID and the MIME boundaries;
	// nil means crypto/rand.Reader.
	Rand io.Reader
	// MessageID overrides the generated Message-ID (without brackets).
	MessageID string
}

// Encode returns m as an RFC 5322 message: headers encoded per RFC 2047,
// text and HTML bodies in quoted-printable as multipart/alternative, and
// attachments in base64 inside multipart/mixed. Bcc is never written.
func (m *Message) Encode(opts EncodeOptions) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	rnd := opts.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	date := opts.Date
	if date.IsZero() {
		date = time.Now()
	}
	id := opts.MessageID
	if id == "" {
		b := make([]byte, 16)
		if _, err := io.ReadFull(rnd, b); err != nil {
			return nil, fmt.Errorf("message id: %w", err)
		}
		domain := "localhost"
		if _, d, ok := strings.Cut(m.From.Address, "@"); ok && d != "" {
			domain = d
		}
		id = hex.EncodeToString(b) + "@" + domain
	}

	var buf bytes.Buffer
	h := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
		}
	}
	h("Date", date.Format(time.RFC1123Z))
	h("From", m.From.String())
	h("Reply-To", joinAddresses(m.ReplyTo))
	h("To", joinAddresses(m.To))
	h("Cc", joinAddresses(m.Cc))
	h("Message-ID", "<"+id+">")
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("MIME-Version", "1.0")

	body, ctype, err := m.encodeBody(rnd)
	if err != nil {
		return nil, err
	}
	h("Content-Type", ctype)
	if !strings.HasPrefix(ctype, "multipart/") {
		h("Content-Transfer-Encoding", "quoted-printable")
	}
	buf.WriteString("\r\n")
	buf.Write(body)
	return buf.Bytes(), nil
}

func joinAddresses(as []Address) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}

func boundary(rnd io.Reader) (string, error) {
	b := make([]byte, 15)
	if _, err := io.ReadFull(rnd, b); err != nil {
		return "", fmt.Errorf("mime boundary: %w", err)
	}
	return "tanvo-" + hex.EncodeToString(b), nil
}

// encodeBody returns the body and its Content-Type.
func (m *Message) encodeBody(rnd io.Reader) ([]byte, string, error) {
	alt, altType, err := m.encodeAlternative(rnd)
	if err != nil {
		return nil, "", err
	}
	if len(m.Attachments) == 0 {
		return alt, altType, nil
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	bd, err := boundary(rnd)
	if err != nil {
		return nil, "", err
	}
	if err := w.SetBoundary(bd); err != nil {
		return nil, "", fmt.Errorf("mime boundary: %w", err)
	}
	hdr := textproto.MIMEHeader{"Content-Type": {altType}}
	if !strings.HasPrefix(altType, "multipart/") {
		hdr.Set("Content-Transfer-Encoding", "quoted-printable")
	}
	p, err := w.CreatePart(hdr)
	if err != nil {
		return nil, "", fmt.Errorf("mime part: %w", err)
	}
	if _, err := p.Write(alt); err != nil {
		return nil, "", fmt.Errorf("mime part: %w", err)
	}
	for _, a := range m.Attachments {
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		p, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {mime.FormatMediaType(ct, map[string]string{"name": a.Filename})},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename})},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, "", fmt.Errorf("mime attachment: %w", err)
		}
		if err := writeBase64(p, a.Data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("mime close: %w", err)
	}
	return buf.Bytes(), "multipart/mixed; boundary=" + w.Boundary(), nil
}

func (m *Message) encodeAlternative(rnd io.Reader) ([]byte, string, error) {
	type part struct{ ctype, body string }
	var parts []part
	if m.Text != "" {
		parts = append(parts, part{"text/plain; charset=utf-8", m.Text})
	}
	if m.HTML != "" {
		parts = append(parts, part{"text/html; charset=utf-8", m.HTML})
	}
	if len(parts) == 1 {
		b, err := qp(parts[0].body)
		return b, parts[0].ctype, err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	bd, err := boundary(rnd)
	if err != nil {
		return nil, "", err
	}
	if err := w.SetBoundary(bd); err != nil {
		return nil, "", fmt.Errorf("mime boundary: %w", err)
	}
	for _, p := range parts {
		pw, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {p.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, "", fmt.Errorf("mime part: %w", err)
		}
		b, err := qp(p.body)
		if err != nil {
			return nil, "", err
		}
		if _, err := pw.Write(b); err != nil {
			return nil, "", fmt.Errorf("mime part: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("mime close: %w", err)
	}
	return buf.Bytes(), "multipart/alternative; boundary=" + w.Boundary(), nil
}

func qp(s string) ([]byte, error) {
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	if _, err := w.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return nil, fmt.Errorf("quoted-printable: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("quoted-printable: %w", err)
	}
	return buf.Bytes(), nil
}

func writeBase64(w io.Writer, data []byte) error {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 0 {
		n := min(76, len(enc))
		if _, err := io.WriteString(w, enc[:n]+"\r\n"); err != nil {
			return fmt.Errorf("base64 attachment: %w", err)
		}
		enc = enc[n:]
	}
	return nil
}
