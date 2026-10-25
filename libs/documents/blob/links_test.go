package blob

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func newTestLinks(t *testing.T) (*Links, *Local, *testClock) {
	t.Helper()
	local, clock, _ := newTestLocal(t)
	l, err := NewLinks(local, LinksOptions{BaseURL: "https://api.example.com/files", Key: []byte(strings.Repeat("l", 32))})
	if err != nil {
		t.Fatalf("NewLinks = %v, want = nil", err)
	}
	return l, local, clock
}

func TestNewLinksValidates(t *testing.T) {
	local, _, _ := newTestLocal(t)
	key := []byte(strings.Repeat("l", 32))
	tests := []struct {
		name string
		opts LinksOptions
	}{
		{"short key", LinksOptions{BaseURL: "https://h/files/", Key: key[:31]}},
		{"no base", LinksOptions{Key: key}},
		{"relative base", LinksOptions{BaseURL: "/files/", Key: key}},
		{"base with query", LinksOptions{BaseURL: "https://h/files/?x=1", Key: key}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewLinks(local, tt.opts); err == nil {
				t.Errorf("NewLinks() error got = nil, want = an error")
			}
		})
	}
	if _, err := NewLinks(nil, LinksOptions{BaseURL: "https://h/files/", Key: key}); err == nil {
		t.Errorf("NewLinks(nil) error got = nil, want = an error")
	}
}

func TestLinksRedirect(t *testing.T) {
	ctx := context.Background()
	l, local, clock := newTestLinks(t)
	orgA, orgB := testOrgs(t)
	ref, err := l.Put(ctx, orgA, "INV-001.pdf", "application/pdf", strings.NewReader("%PDF"))
	if err != nil {
		t.Fatal(err)
	}
	link, err := l.URL(ctx, orgA, ref)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.example.com/files/" + orgA.String() + "/" + string(ref) + "?sig="
	if !strings.HasPrefix(link, want) {
		t.Fatalf("URL() got = %q, want = prefix %q", link, want)
	}
	// The link is permanent: it is the same a year later.
	clock.Advance(365 * 24 * time.Hour)
	again, _ := l.URL(ctx, orgA, ref)
	if again != link {
		t.Errorf("URL() later got = %q, want = %q", again, link)
	}
	u, _ := url.Parse(link)
	serve := func(path, query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://api.example.com"+path+"?"+query, nil)
		http.StripPrefix("/files", l).ServeHTTP(rec, req)
		return rec
	}
	rec := serve(u.Path, u.RawQuery)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET link status got = %d, want = %d", rec.Code, http.StatusFound)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://billing.example.com/documents/"+orgA.String()+"/"+string(ref)+"?") {
		t.Errorf("Location got = %q, want = the local signed URL", loc)
	}
	// The redirect target is a working short-lived URL.
	lu, _ := url.Parse(loc)
	frec := httptest.NewRecorder()
	http.StripPrefix("/documents", local).ServeHTTP(frec, httptest.NewRequest(http.MethodGet, "https://billing.example.com"+lu.Path+"?"+lu.RawQuery, nil))
	if frec.Code != http.StatusOK || frec.Body.String() != "%PDF" {
		t.Errorf("GET target got = %d %q, want = 200 %%PDF", frec.Code, frec.Body.String())
	}

	tests := []struct {
		name  string
		path  string
		query string
		want  int
	}{
		{"bad signature", u.Path, "sig=nope", http.StatusForbidden},
		{"no signature", u.Path, "", http.StatusForbidden},
		{"other organization", strings.Replace(u.Path, orgA.String(), orgB.String(), 1), u.RawQuery, http.StatusForbidden},
		{"malformed path", "/files/" + orgA.String() + "/../x", u.RawQuery, http.StatusNotFound},
		{"bad organization", "/files/acme/" + string(ref), u.RawQuery, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serve(tt.path, tt.query).Code; got != tt.want {
				t.Errorf("status got = %d, want = %d", got, tt.want)
			}
		})
	}

	if err := l.Delete(ctx, orgA, ref); err != nil {
		t.Fatal(err)
	}
	if got := serve(u.Path, u.RawQuery).Code; got != http.StatusNotFound {
		t.Errorf("GET deleted status got = %d, want = %d", got, http.StatusNotFound)
	}
	rec = httptest.NewRecorder()
	l.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "https://api.example.com/x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status got = %d, want = %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestLinksURLValidates(t *testing.T) {
	l, _, _ := newTestLinks(t)
	orgA, _ := testOrgs(t)
	if _, err := l.URL(context.Background(), core.OrganizationID{}, "0123456789abcdef0123456789abcdef/a.pdf"); err == nil {
		t.Errorf("URL(no org) error got = nil, want = an error")
	}
	if _, err := l.URL(context.Background(), orgA, "../etc/passwd"); err == nil {
		t.Errorf("URL(bad ref) error got = nil, want = an error")
	}
}
