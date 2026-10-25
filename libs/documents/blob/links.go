package blob

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// LinksOptions configures [NewLinks].
type LinksOptions struct {
	// BaseURL is the URL at which the [Links] handler is mounted, for
	// example "https://api.example.com/files/". Links are
	// BaseURL + "<org>/<ref>?sig=…".
	BaseURL string
	// Key is the HMAC-SHA256 key signing the links; at least 32 bytes.
	Key []byte
	// Logger logs failures of the HTTP handler; [slog.Default] when nil.
	Logger *slog.Logger
}

// Links is a blob store whose URLs are permanent links: [Links.URL]
// returns a signed link that never expires, and the [Links] handler
// verifies it and redirects to a short-lived URL of the underlying store.
// Links may therefore be kept in emails, webhooks and third-party systems
// (CRM file fields, payment pages) while every download still goes
// through an expiring URL. Put, Get and Delete go to the underlying store.
type Links struct {
	store  port.BlobStore
	base   string
	key    []byte
	logger *slog.Logger
}

var _ port.BlobStore = (*Links)(nil)

// NewLinks wraps store with permanent links.
func NewLinks(store port.BlobStore, opts LinksOptions) (*Links, error) {
	if store == nil {
		return nil, errors.New("blob: links need a store")
	}
	if len(opts.Key) < minKeyLen {
		return nil, fmt.Errorf("blob: link signing key must be at least %d bytes", minKeyLen)
	}
	base, err := url.Parse(opts.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("blob: invalid links base URL %q", opts.BaseURL)
	}
	l := &Links{
		store:  store,
		base:   strings.TrimSuffix(opts.BaseURL, "/") + "/",
		key:    append([]byte(nil), opts.Key...),
		logger: opts.Logger,
	}
	if l.logger == nil {
		l.logger = slog.Default()
	}
	return l, nil
}

// Put implements port.BlobStore.
func (l *Links) Put(ctx context.Context, org core.OrganizationID, name, contentType string, body io.Reader) (core.BlobRef, error) {
	return l.store.Put(ctx, org, name, contentType, body)
}

// Get implements port.BlobStore.
func (l *Links) Get(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (io.ReadCloser, error) {
	return l.store.Get(ctx, org, ref)
}

// Delete implements port.BlobStore.
func (l *Links) Delete(ctx context.Context, org core.OrganizationID, ref core.BlobRef) error {
	return l.store.Delete(ctx, org, ref)
}

// URL implements port.BlobStore: the permanent link of ref of org. It
// does not check that the object exists; a link to a deleted object
// answers 404.
func (l *Links) URL(_ context.Context, org core.OrganizationID, ref core.BlobRef) (string, error) {
	if err := checkOrg(org); err != nil {
		return "", err
	}
	random, name, err := checkRef(ref)
	if err != nil {
		return "", err
	}
	return l.base + org.String() + "/" + random + "/" + url.PathEscape(name) + "?sig=" + l.sign(org, ref), nil
}

// sign returns the link signature of ref of org.
func (l *Links) sign(org core.OrganizationID, ref core.BlobRef) string {
	m := hmac.New(sha256.New, l.key)
	fmt.Fprintf(m, "tanvo-link-v1\n%s\n%s", org, ref)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// ServeHTTP answers a link returned by [Links.URL] with a 302 redirect
// to a short-lived URL of the underlying store. The request path, with
// BaseURL's path stripped, is "<org>/<ref>". It answers 403 to a bad
// signature and 404 to a malformed path or a missing object.
func (l *Links) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	orgPart, refPart, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	org, err := core.ParseID[core.OrganizationID](orgPart)
	ref := core.BlobRef(refPart)
	if _, _, refErr := checkRef(ref); !ok || err != nil || org.IsZero() || refErr != nil {
		http.NotFound(w, r)
		return
	}
	if !hmac.Equal([]byte(r.URL.Query().Get("sig")), []byte(l.sign(org, ref))) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	target, err := l.store.URL(r.Context(), org, ref)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		l.logger.ErrorContext(r.Context(), "blob: resolve link", "organization_id", org.String(), "ref", ref, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}
