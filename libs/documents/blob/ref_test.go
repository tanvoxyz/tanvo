package blob

import (
	"errors"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestSanitizeName(t *testing.T) {
	long := strings.Repeat("a", 200)
	tests := []struct {
		in, want string
	}{
		{"invoice-2024-001.pdf", "invoice-2024-001.pdf"},
		{"../../etc/passwd", "passwd"},
		{`..\..\windows\system.ini`, "system.ini"},
		{"/abs/path/", "file"},
		{"..", "file"},
		{".hidden", "hidden"},
		{"-rf", "rf"},
		{"facture été.pdf", "facture__t_.pdf"},
		{"a\x00b\nc", "a_b_c"},
		{"", "file"},
		{"???", "file"},
		{long + ".pdf", strings.Repeat("a", maxNameLen-4) + ".pdf"},
		{long, strings.Repeat("a", maxNameLen)},
	}
	for _, tt := range tests {
		got := sanitizeName(tt.in)
		if got != tt.want {
			t.Errorf("sanitizeName(%q) = %q, want = %q", tt.in, got, tt.want)
		}
		if !isName(got) {
			t.Errorf("isName(sanitizeName(%q)) = false, want = true", tt.in)
		}
	}
}

func TestNewRef(t *testing.T) {
	seen := map[core.BlobRef]bool{}
	for range 100 {
		ref := newRef("../logo.png")
		random, name, err := checkRef(ref)
		if err != nil {
			t.Fatalf("checkRef(%q) = %v, want = nil", ref, err)
		}
		if len(random) != randomLen || name != "logo.png" {
			t.Errorf("newRef = %q, want = <32 hex>/logo.png", ref)
		}
		if seen[ref] {
			t.Fatalf("newRef repeated %q", ref)
		}
		seen[ref] = true
	}
}

// badRefs are references Put never returns.
func badRefs() []core.BlobRef {
	const r = "0123456789abcdef0123456789abcdef"
	return []core.BlobRef{
		"",
		"x",
		r,
		r + "/",
		r + "/..",
		r + "/.",
		r + "/../x",
		r + "/a/b",
		r + "/.content-type",
		r + "/-x",
		r + "/a b",
		r + `/a\b`,
		"../" + r + "/x",
		"0123456789ABCDEF0123456789ABCDEF/x",
		"0123456789abcdef0123456789abcde/x",
		"/" + r + "/x",
		r + "/" + core.BlobRef(strings.Repeat("a", maxNameLen+1)),
	}
}

func TestCheckRefRejects(t *testing.T) {
	for _, ref := range badRefs() {
		_, _, err := checkRef(ref)
		if !errors.Is(err, core.ErrInvalidValue) || !errors.Is(err, core.ErrNotFound) {
			t.Errorf("checkRef(%q) = %v, want = ErrInvalidValue and ErrNotFound", ref, err)
		}
	}
}
