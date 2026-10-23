package mail

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenLocales are the locales whose rendering is pinned per kind: every
// kind in English, and the documents customers receive in German, French
// and Traditional Chinese too.
func goldenLocales(k Kind) []string {
	switch k {
	case KindInvoiceCreated, KindCreditNoteCreated, KindPaymentReceiptCreated, KindPaymentRequestRequested:
		return []string{"en", "de", "fr", "zh-TW", "zh-CN"}
	default:
		return []string{"en"}
	}
}

// dump is the golden representation of a message.
func dump(m *Message) []byte {
	var b bytes.Buffer
	join := func(as []Address) string {
		s := make([]string, len(as))
		for i, a := range as {
			s[i] = a.String()
		}
		return strings.Join(s, ", ")
	}
	fmt.Fprintf(&b, "Kind: %s\nLocale: %s\nFrom: %s\nReply-To: %s\nTo: %s\nCc: %s\nBcc: %s\nSubject: %s\n",
		m.Kind, m.Locale, m.From.String(), join(m.ReplyTo), join(m.To), join(m.Cc), join(m.Bcc), m.Subject)
	for _, a := range m.Attachments {
		fmt.Fprintf(&b, "Attachment: %s (%s, %d bytes)\n", a.Filename, a.ContentType, len(a.Data))
	}
	fmt.Fprintf(&b, "\n==== text\n%s\n==== html\n%s\n", m.Text, m.HTML)
	return b.Bytes()
}

func TestGolden(t *testing.T) {
	c := newComposer(t)
	for k, build := range builders() {
		for _, l := range goldenLocales(k) {
			t.Run(string(k)+"/"+l, func(t *testing.T) {
				m, err := build(c, l)
				if err != nil {
					t.Fatalf("build error = %v", err)
				}
				got := dump(m)
				path := filepath.Join("testdata", "golden", string(k)+"."+l+".golden")
				if *update {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, got, 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden (run go test -update): %v", err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s differs from the golden file; got:\n%s", path, got)
				}
			})
		}
	}
}

// TestSweep renders every kind in every locale, with PDFs on and off, and
// checks that nothing is left untranslated or unrendered.
func TestSweep(t *testing.T) {
	composers := map[string]*Composer{"pdfs": newComposer(t), "no-pdfs": newComposer(t, withoutPDFs)}
	for name, c := range composers {
		for k, build := range builders() {
			for _, l := range Locales() {
				m, err := build(c, l)
				if err != nil {
					t.Errorf("%s %s %s: build error = %v", name, k, l, err)
					continue
				}
				if m.Kind != k {
					t.Errorf("%s %s %s: Kind = %s", name, k, l, m.Kind)
				}
				wantLocale := "en"
				switch k {
				case KindInvoiceCreated, KindCreditNoteCreated, KindPaymentReceiptCreated, KindPaymentRequestRequested, KindAuthenticationMethodsUpdated:
					wantLocale = l
				}
				if m.Locale != wantLocale {
					t.Errorf("%s %s %s: Locale = %s, want = %s", name, k, l, m.Locale, wantLocale)
				}
				for part, s := range map[string]string{"subject": m.Subject, "text": m.Text, "html": m.HTML} {
					if strings.TrimSpace(s) == "" {
						t.Errorf("%s %s %s: empty %s", name, k, l, part)
					}
					for _, bad := range []string{"%{", "<no value>", "%!", "translation missing", "Lago"} {
						if strings.Contains(s, bad) {
							t.Errorf("%s %s %s: %s contains %q", name, k, l, part, bad)
						}
					}
				}
				if _, err := m.Encode(EncodeOptions{}); err != nil {
					t.Errorf("%s %s %s: Encode() error = %v", name, k, l, err)
				}
			}
		}
	}
	if got, want := len(builders()), len(Kinds()); got != want {
		t.Errorf("builders cover %d kinds, want = %d", got, want)
	}
}
