package blob

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

const (
	dirPerm  = 0o750
	filePerm = 0o640
	// contentTypeFile is the sidecar holding an object's media type. It
	// starts with '.', which no sanitized file name does.
	contentTypeFile = ".content-type"
	// defaultContentType is used when Put is given no media type.
	defaultContentType = "application/octet-stream"
	// defaultURLTTL is the lifetime of a download URL when none is set.
	defaultURLTTL = 15 * time.Minute
	// minKeyLen is the shortest accepted URL signing key, in bytes.
	minKeyLen = 32
)

var (
	// ErrBadSignature is returned by [Local.Verify] when a URL signature does
	// not match.
	ErrBadSignature = errors.New("blob: bad URL signature")
	// ErrExpired is returned by [Local.Verify] when a URL is past its expiry.
	ErrExpired = errors.New("blob: URL expired")
)

// LocalOptions configures [NewLocal].
type LocalOptions struct {
	// Root is the directory holding the files; it is created when missing.
	Root string
	// BaseURL is the URL at which the [Local] handler is mounted, for
	// example "https://billing.example.com/documents/". Download URLs are
	// BaseURL + "<org>/<ref>?expires=…&sig=…".
	BaseURL string
	// Key is the HMAC-SHA256 key signing download URLs; at least 32 bytes.
	Key []byte
	// TTL is the lifetime of download URLs; 15 minutes when zero.
	TTL time.Duration
	// Clock is the time source; [core.SystemClock] when nil.
	Clock core.Clock
	// Logger logs failures of the HTTP handler; [slog.Default] when nil.
	Logger *slog.Logger
}

// Local is a blob store on the local disk. Objects live at
// Root/<org>/<random>/<name>, each with a sidecar file holding its media
// type. Writes are atomic (temporary file, then rename); directories are
// created 0o750 and files 0o640. All file access goes through an [os.Root],
// so no path can escape Root.
//
// Local is also the [http.Handler] serving the signed download URLs that
// [Local.URL] returns; mount it at BaseURL's path with [http.StripPrefix].
type Local struct {
	root   *os.Root
	base   string
	key    []byte
	ttl    time.Duration
	clock  core.Clock
	logger *slog.Logger
}

// NewLocal opens a local blob store. Close it to release the root directory.
func NewLocal(opts LocalOptions) (*Local, error) {
	if opts.Root == "" {
		return nil, errors.New("blob: local root is required")
	}
	if len(opts.Key) < minKeyLen {
		return nil, fmt.Errorf("blob: URL signing key must be at least %d bytes", minKeyLen)
	}
	base, err := url.Parse(opts.BaseURL)
	if err != nil || opts.BaseURL == "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("blob: invalid base URL %q", opts.BaseURL)
	}
	if opts.TTL < 0 {
		return nil, fmt.Errorf("blob: negative URL TTL %v", opts.TTL)
	}
	if err := os.MkdirAll(opts.Root, dirPerm); err != nil {
		return nil, fmt.Errorf("blob: create root: %w", err)
	}
	root, err := os.OpenRoot(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("blob: open root: %w", err)
	}
	l := &Local{
		root:   root,
		base:   strings.TrimSuffix(opts.BaseURL, "/") + "/",
		key:    append([]byte(nil), opts.Key...),
		ttl:    opts.TTL,
		clock:  opts.Clock,
		logger: opts.Logger,
	}
	if l.ttl == 0 {
		l.ttl = defaultURLTTL
	}
	if l.clock == nil {
		l.clock = core.SystemClock{}
	}
	if l.logger == nil {
		l.logger = slog.Default()
	}
	return l, nil
}

// Close releases the root directory.
func (l *Local) Close() error {
	if err := l.root.Close(); err != nil {
		return fmt.Errorf("blob: close root: %w", err)
	}
	return nil
}

// Put implements port.BlobStore.
func (l *Local) Put(ctx context.Context, org core.OrganizationID, name, contentType string, body io.Reader) (core.BlobRef, error) {
	if err := checkOrg(org); err != nil {
		return "", err
	}
	contentType, err := normalizeContentType(contentType)
	if err != nil {
		return "", err
	}
	ref := newRef(name)
	random, file, _ := checkRef(ref)
	dir := org.String() + "/" + random
	if err := l.root.MkdirAll(dir, dirPerm); err != nil {
		return "", fmt.Errorf("blob: create directory: %w", err)
	}
	err = l.writeAtomic(ctx, dir, contentTypeFile, strings.NewReader(contentType))
	if err == nil {
		err = l.writeAtomic(ctx, dir, file, body)
	}
	if err != nil {
		if rmErr := l.root.RemoveAll(dir); rmErr != nil {
			l.logger.WarnContext(ctx, "blob: remove partial object", "dir", dir, "error", rmErr)
		}
		return "", err
	}
	return ref, nil
}

// writeAtomic writes src to dir/name through a temporary file renamed into
// place, so readers never see a partial file.
func (l *Local) writeAtomic(ctx context.Context, dir, name string, src io.Reader) error {
	var b [8]byte
	_, _ = rand.Read(b[:])
	tmp := dir + "/.tmp-" + hex.EncodeToString(b[:])
	f, err := l.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return fmt.Errorf("blob: create temporary file: %w", err)
	}
	_, err = io.Copy(f, &ctxReader{ctx: ctx, r: src})
	if err == nil {
		err = f.Chmod(filePerm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = l.root.Rename(tmp, dir+"/"+name)
	}
	if err != nil {
		_ = l.root.Remove(tmp) // Best effort; the error below is what matters.
		return fmt.Errorf("blob: write %s: %w", name, err)
	}
	return nil
}

// Get implements port.BlobStore.
func (l *Local) Get(_ context.Context, org core.OrganizationID, ref core.BlobRef) (io.ReadCloser, error) {
	f, _, err := l.open(org, ref)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// open opens the object ref of org and returns it with its media type.
func (l *Local) open(org core.OrganizationID, ref core.BlobRef) (*os.File, string, error) {
	if err := checkOrg(org); err != nil {
		return nil, "", err
	}
	if _, _, err := checkRef(ref); err != nil {
		return nil, "", err
	}
	key := objectKey(org, ref)
	f, err := l.root.Open(key)
	if err != nil {
		return nil, "", notFound(ref, err)
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fs.ErrNotExist
	}
	if err != nil {
		_ = f.Close()
		return nil, "", notFound(ref, err)
	}
	ct, err := l.root.ReadFile(key[:strings.LastIndexByte(key, '/')+1] + contentTypeFile)
	if err != nil {
		_ = f.Close()
		return nil, "", notFound(ref, err)
	}
	return f, string(ct), nil
}

// notFound maps a missing file to core.ErrNotFound.
func notFound(ref core.BlobRef, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("blob: %q: %w", ref, core.ErrNotFound)
	}
	return fmt.Errorf("blob: open %q: %w", ref, err)
}

// Delete implements port.BlobStore.
func (l *Local) Delete(_ context.Context, org core.OrganizationID, ref core.BlobRef) error {
	if err := checkOrg(org); err != nil {
		return err
	}
	random, _, err := checkRef(ref)
	if err != nil {
		return err
	}
	if err := l.root.RemoveAll(org.String() + "/" + random); err != nil {
		return fmt.Errorf("blob: delete %q: %w", ref, err)
	}
	return nil
}

// URL implements port.BlobStore. It returns BaseURL + "<org>/<ref>" with
// the expiry (Unix seconds) and its HMAC-SHA256 signature as query
// parameters; a missing object is [core.ErrNotFound].
func (l *Local) URL(_ context.Context, org core.OrganizationID, ref core.BlobRef) (string, error) {
	if err := checkOrg(org); err != nil {
		return "", err
	}
	random, name, err := checkRef(ref)
	if err != nil {
		return "", err
	}
	if _, err := l.root.Stat(objectKey(org, ref)); err != nil {
		return "", notFound(ref, err)
	}
	expires := l.clock.Now().Add(l.ttl).Unix()
	q := url.Values{
		"expires": {strconv.FormatInt(expires, 10)},
		"sig":     {l.sign(org, ref, expires)},
	}
	return l.base + org.String() + "/" + random + "/" + url.PathEscape(name) + "?" + q.Encode(), nil
}

// sign returns the URL signature of ref of org expiring at expires.
func (l *Local) sign(org core.OrganizationID, ref core.BlobRef, expires int64) string {
	m := hmac.New(sha256.New, l.key)
	fmt.Fprintf(m, "tanvo-blob-v1\n%s\n%s\n%d", org, ref, expires)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Verify checks the signature sig of a download URL of ref of org expiring
// at expires (Unix seconds). It returns [ErrExpired] past the expiry and
// [ErrBadSignature] when sig does not match.
func (l *Local) Verify(org core.OrganizationID, ref core.BlobRef, expires int64, sig string) error {
	if !hmac.Equal([]byte(sig), []byte(l.sign(org, ref, expires))) {
		return ErrBadSignature
	}
	if l.clock.Now().Unix() > expires {
		return ErrExpired
	}
	return nil
}

// ServeHTTP serves the file of a download URL returned by [Local.URL] after
// verifying its signature. The request path, with BaseURL's path stripped,
// is "<org>/<ref>". It answers 403 to a bad or expired signature and 404 to
// a malformed path or a missing file; it supports range and conditional
// requests through [http.ServeContent].
func (l *Local) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	q := r.URL.Query()
	expires, err := strconv.ParseInt(q.Get("expires"), 10, 64)
	if err == nil {
		err = l.Verify(org, ref, expires, q.Get("sig"))
	}
	if err != nil {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	f, contentType, err := l.open(org, ref)
	if err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			l.logger.ErrorContext(r.Context(), "blob: serve file", "ref", ref, "error", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		l.logger.ErrorContext(r.Context(), "blob: stat file", "ref", ref, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	_, name, _ := checkRef(ref)
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "private, max-age=0")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// normalizeContentType validates a media type; the empty one becomes
// application/octet-stream.
func normalizeContentType(ct string) (string, error) {
	if ct == "" {
		return defaultContentType, nil
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return "", fmt.Errorf("blob: %w: content type %q: %w", core.ErrInvalidValue, ct, err)
	}
	return mime.FormatMediaType(mt, params), nil
}

// ctxReader stops reading once its context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, fmt.Errorf("blob: %w", err)
	}
	return c.r.Read(p)
}
