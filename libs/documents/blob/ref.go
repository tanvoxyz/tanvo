package blob

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
)

const (
	// randomLen is the length of the random part of a reference: 128 bits
	// in hex.
	randomLen = 32
	// maxNameLen bounds the sanitized file name of a reference, in bytes.
	maxNameLen = 128
	// maxExtLen is the longest extension kept when a name is shortened.
	maxExtLen = 16
	// defaultName replaces a file name that sanitizes to nothing.
	defaultName = "file"
)

// newRef returns a fresh reference for a file called name.
func newRef(name string) core.BlobRef {
	var b [randomLen / 2]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (it aborts the program instead).
	return core.BlobRef(hex.EncodeToString(b[:]) + "/" + sanitizeName(name))
}

// sanitizeName reduces name to its last path element made of ASCII letters,
// digits, '.', '_' and '-' (other characters become '_'), never starting with
// '.' or '-', at most maxNameLen bytes long.
func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		if isNameChar(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := strings.TrimLeft(b.String(), ".-")
	if len(s) > maxNameLen {
		ext := path.Ext(s)
		if len(ext) > maxExtLen {
			ext = ""
		}
		s = s[:maxNameLen-len(ext)] + ext
	}
	if strings.Trim(s, "_") == "" {
		return defaultName
	}
	return s
}

func isNameChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '.' || r == '_' || r == '-'
}

// checkRef validates ref and returns its random part and file name. Any
// reference Put did not produce, including every path traversal attempt, is
// rejected with an error wrapping both [core.ErrInvalidValue] and
// [core.ErrNotFound].
func checkRef(ref core.BlobRef) (random, name string, err error) {
	random, name, ok := strings.Cut(string(ref), "/")
	if !ok || !isRandom(random) || !isName(name) {
		return "", "", fmt.Errorf("blob: %w: malformed reference %q: %w", core.ErrInvalidValue, ref, core.ErrNotFound)
	}
	return random, name, nil
}

func isRandom(s string) bool {
	if len(s) != randomLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isName(s string) bool {
	if s == "" || len(s) > maxNameLen || s[0] == '.' || s[0] == '-' {
		return false
	}
	for _, r := range s {
		if !isNameChar(r) {
			return false
		}
	}
	return true
}

// checkOrg rejects the unset organization.
func checkOrg(org core.OrganizationID) error {
	if org.IsZero() {
		return fmt.Errorf("blob: %w: organization is required", core.ErrInvalidValue)
	}
	return nil
}

// objectKey is the organization-scoped key of ref: <org>/<ref>.
func objectKey(org core.OrganizationID, ref core.BlobRef) string {
	return org.String() + "/" + string(ref)
}
