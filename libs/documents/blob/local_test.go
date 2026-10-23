package blob

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func newTestLocal(t *testing.T) (*Local, *testClock, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "blobs")
	clock := newTestClock()
	l, err := NewLocal(LocalOptions{
		Root:    root,
		BaseURL: "https://billing.example.com/documents",
		Key:     []byte(strings.Repeat("k", 32)),
		TTL:     10 * time.Minute,
		Clock:   clock,
	})
	if err != nil {
		t.Fatalf("NewLocal = %v, want = nil", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, clock, root
}

func readAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNewLocalValidates(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	tests := []struct {
		name string
		opts LocalOptions
	}{
		{"no root", LocalOptions{BaseURL: "https://h/d/", Key: key}},
		{"short key", LocalOptions{Root: t.TempDir(), BaseURL: "https://h/d/", Key: key[:31]}},
		{"no base", LocalOptions{Root: t.TempDir(), Key: key}},
		{"base with query", LocalOptions{Root: t.TempDir(), BaseURL: "https://h/d/?x=1", Key: key}},
		{"negative ttl", LocalOptions{Root: t.TempDir(), BaseURL: "https://h/d/", Key: key, TTL: -time.Second}},
	}
	for _, tt := range tests {
		if _, err := NewLocal(tt.opts); err == nil {
			t.Errorf("%s: NewLocal = nil, want = an error", tt.name)
		}
	}
}

func TestLocalRoundTrip(t *testing.T) {
	ctx := context.Background()
	l, _, root := newTestLocal(t)
	org, _ := testOrgs(t)
	ref, err := l.Put(ctx, org, "../Invoice 001.pdf", "application/pdf", strings.NewReader("%PDF-1.7"))
	if err != nil {
		t.Fatalf("Put = %v, want = nil", err)
	}
	if !strings.HasSuffix(string(ref), "/Invoice_001.pdf") {
		t.Errorf("ref = %q, want = <random>/Invoice_001.pdf", ref)
	}
	rc, err := l.Get(ctx, org, ref)
	if err != nil {
		t.Fatalf("Get = %v, want = nil", err)
	}
	if got := readAll(t, rc); got != "%PDF-1.7" {
		t.Errorf("body = %q, want = %%PDF-1.7", got)
	}

	file := filepath.Join(root, org.String(), string(ref))
	for _, c := range []struct {
		path string
		want fs.FileMode
	}{
		{filepath.Join(root, org.String()), dirPerm},
		{filepath.Dir(file), dirPerm},
		{file, filePerm},
		{filepath.Join(filepath.Dir(file), contentTypeFile), filePerm},
	} {
		fi, err := os.Stat(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != c.want {
			t.Errorf("mode of %s = %v, want = %v", c.path, got, c.want)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("object directory has %d entries, want = 2 (no temporary files left)", len(entries))
	}

	if err := l.Delete(ctx, org, ref); err != nil {
		t.Fatalf("Delete = %v, want = nil", err)
	}
	if _, err := l.Get(ctx, org, ref); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get after Delete = %v, want = ErrNotFound", err)
	}
	if err := l.Delete(ctx, org, ref); err != nil {
		t.Errorf("second Delete = %v, want = nil", err)
	}
	if _, err := os.Stat(filepath.Dir(file)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("object directory after Delete: %v, want = not exist", err)
	}
}

func TestLocalPutErrors(t *testing.T) {
	ctx := context.Background()
	l, _, root := newTestLocal(t)
	org, _ := testOrgs(t)
	if _, err := l.Put(ctx, core.OrganizationID{}, "a", "", strings.NewReader("x")); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Put without org = %v, want = ErrInvalidValue", err)
	}
	if _, err := l.Put(ctx, org, "a", "text/plain\r\nX-Evil: 1", strings.NewReader("x")); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Put with bad content type = %v, want = ErrInvalidValue", err)
	}
	boom := errors.New("boom")
	if _, err := l.Put(ctx, org, "a", "", iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Errorf("Put with failing body = %v, want = boom", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := l.Put(canceled, org, "a", "", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Put with canceled context = %v, want = context.Canceled", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, org.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("organization directory has %d entries after failed puts, want = 0", len(entries))
	}
}

func TestLocalTenancy(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newTestLocal(t)
	a, b := testOrgs(t)
	ref, err := l.Put(ctx, a, "logo.png", "image/png", strings.NewReader("png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Get(ctx, b, ref); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get from other org = %v, want = ErrNotFound", err)
	}
	if _, err := l.URL(ctx, b, ref); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("URL from other org = %v, want = ErrNotFound", err)
	}
	if err := l.Delete(ctx, b, ref); err != nil {
		t.Errorf("Delete from other org = %v, want = nil", err)
	}
	rc, err := l.Get(ctx, a, ref)
	if err != nil {
		t.Fatalf("Get after other org's Delete = %v, want = nil", err)
	}
	_ = rc.Close()

	// A URL signed for a cannot be replayed with b in the path.
	u, err := l.URL(ctx, a, ref)
	if err != nil {
		t.Fatal(err)
	}
	if code := serve(t, l, strings.Replace(u, a.String(), b.String(), 1)).Code; code != http.StatusForbidden {
		t.Errorf("status with swapped org = %d, want = %d", code, http.StatusForbidden)
	}
}

func TestLocalRejectsBadRefs(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newTestLocal(t)
	org, _ := testOrgs(t)
	for _, ref := range badRefs() {
		if _, err := l.Get(ctx, org, ref); !errors.Is(err, core.ErrNotFound) || !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("Get(%q) = %v, want = ErrInvalidValue and ErrNotFound", ref, err)
		}
		if _, err := l.URL(ctx, org, ref); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("URL(%q) = %v, want = ErrInvalidValue", ref, err)
		}
		if err := l.Delete(ctx, org, ref); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("Delete(%q) = %v, want = ErrInvalidValue", ref, err)
		}
	}
	if _, err := l.Get(ctx, org, "0123456789abcdef0123456789abcdef/missing.pdf"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get(missing) = %v, want = ErrNotFound", err)
	}
}

// serve sends a GET of rawURL to l's handler mounted under /documents/.
func serve(t *testing.T, l *Local, rawURL string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, rawURL, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	http.StripPrefix("/documents", l).ServeHTTP(w, req)
	return w
}

func TestLocalURL(t *testing.T) {
	ctx := context.Background()
	l, clock, _ := newTestLocal(t)
	org, _ := testOrgs(t)
	ref, err := l.Put(ctx, org, "invoice.pdf", "application/pdf", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := l.URL(ctx, org, ref)
	if err != nil {
		t.Fatalf("URL = %v, want = nil", err)
	}
	wantPrefix := "https://billing.example.com/documents/" + org.String() + "/" + string(ref) + "?"
	if !strings.HasPrefix(raw, wantPrefix) {
		t.Errorf("URL = %s, want prefix = %s", raw, wantPrefix)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.Query().Get("expires"), "1772367000"; got != want { // 2026-03-01T12:10:00Z
		t.Errorf("expires = %s, want = %s", got, want)
	}

	w := serve(t, l, raw)
	if w.Code != http.StatusOK || w.Body.String() != "0123456789" {
		t.Fatalf("GET = %d %q, want = 200 \"0123456789\"", w.Code, w.Body.String())
	}
	for k, want := range map[string]string{
		"Content-Type":           "application/pdf",
		"Content-Disposition":    "attachment; filename=invoice.pdf",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want = %q", k, got, want)
		}
	}
	if w := serve(t, l, raw, "Range", "bytes=2-4"); w.Code != http.StatusPartialContent || w.Body.String() != "234" {
		t.Errorf("ranged GET = %d %q, want = 206 \"234\"", w.Code, w.Body.String())
	}

	tampered := map[string]string{
		"signature": strings.Replace(raw, "sig=", "sig=x", 1),
		"expiry":    strings.Replace(raw, "expires=1772367000", "expires=1772367999", 1),
		"no query":  strings.Split(raw, "?")[0],
		"ref":       strings.Replace(raw, "invoice.pdf", "invoice2.pdf", 1),
	}
	for name, tu := range tampered {
		if code := serve(t, l, tu).Code; code != http.StatusForbidden {
			t.Errorf("GET with tampered %s = %d, want = %d", name, code, http.StatusForbidden)
		}
	}
	for _, path := range []string{"/documents/x", "/documents/" + org.String() + "/../../etc/passwd", "/documents/" + org.String() + "/x/y/z"} {
		if code := serve(t, l, "https://billing.example.com"+path+"?"+u.RawQuery).Code; code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want = %d", path, code, http.StatusNotFound)
		}
	}
	post := httptest.NewRequest(http.MethodPost, raw, nil)
	pw := httptest.NewRecorder()
	http.StripPrefix("/documents", l).ServeHTTP(pw, post)
	if pw.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want = %d", pw.Code, http.StatusMethodNotAllowed)
	}

	// Verify directly.
	expires := clock.Now().Add(time.Minute).Unix()
	sig := l.sign(org, ref, expires)
	if err := l.Verify(org, ref, expires, sig); err != nil {
		t.Errorf("Verify = %v, want = nil", err)
	}
	if err := l.Verify(org, ref, expires, sig[1:]); !errors.Is(err, ErrBadSignature) {
		t.Errorf("Verify(bad sig) = %v, want = ErrBadSignature", err)
	}

	// Past the expiry.
	clock.Advance(10*time.Minute + time.Second)
	if code := serve(t, l, raw).Code; code != http.StatusForbidden {
		t.Errorf("GET after expiry = %d, want = %d", code, http.StatusForbidden)
	}
	if err := l.Verify(org, ref, expires, sig); !errors.Is(err, ErrExpired) {
		t.Errorf("Verify after expiry = %v, want = ErrExpired", err)
	}

	// A fresh URL of a deleted file is refused at creation and at download.
	fresh, err := l.URL(ctx, org, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Delete(ctx, org, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := l.URL(ctx, org, ref); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("URL after Delete = %v, want = ErrNotFound", err)
	}
	if code := serve(t, l, fresh).Code; code != http.StatusNotFound {
		t.Errorf("GET after Delete = %d, want = %d", code, http.StatusNotFound)
	}
}
