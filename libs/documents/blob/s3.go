package blob

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

const (
	// MaxURLExpiry is the longest lifetime S3 accepts for a presigned URL.
	MaxURLExpiry = 7 * 24 * time.Hour
	// DefaultMaxPutSize is the default largest object Put accepts.
	DefaultMaxPutSize = 256 << 20
	// maxErrorBody bounds the error document read from S3.
	maxErrorBody = 64 << 10
)

// S3Options configures [NewS3].
type S3Options struct {
	// Endpoint is the service URL, scheme and host only, for example
	// "https://s3.eu-west-1.amazonaws.com", "http://127.0.0.1:9000" (MinIO)
	// or "https://<account>.r2.cloudflarestorage.com" (R2).
	Endpoint string
	// Region is the signing region, for example "eu-west-1"; R2 uses "auto".
	Region string
	// Bucket is the bucket holding the objects.
	Bucket string
	// AccessKeyID is the access key.
	AccessKeyID string
	// SecretAccessKey is the secret key.
	SecretAccessKey string
	// SessionToken is the optional session token of temporary credentials.
	SessionToken string
	// PathStyle addresses the bucket in the path (endpoint/bucket/key)
	// instead of the host (bucket.endpoint/key); MinIO usually needs it.
	PathStyle bool
	// Prefix is an optional key prefix, for example "documents"; keys are
	// <prefix>/<org>/<ref>.
	Prefix string
	// URLExpiry is the lifetime of presigned URLs: 15 minutes when zero, at
	// most [MaxURLExpiry].
	URLExpiry time.Duration
	// MaxPutSize is the largest object Put accepts, in bytes;
	// [DefaultMaxPutSize] when zero.
	MaxPutSize int64
	// HTTPClient sends the requests; [http.DefaultClient] when nil.
	HTTPClient *http.Client
	// Clock is the time source of signatures; [core.SystemClock] when nil.
	Clock core.Clock
}

// S3 is a blob store on an S3-compatible object store, spoken to through
// net/http with a hand-written AWS Signature Version 4. Objects are stored
// at <prefix>/<org>/<ref>.
//
// Put buffers the body in memory (up to MaxPutSize) and signs its SHA-256,
// so the payload is integrity-checked by the server and the request has a
// Content-Length, which PutObject requires; streaming without a length
// would need the aws-chunked encoding, which not every S3-compatible store
// implements.
type S3 struct {
	scheme string
	host   string
	bucket string
	path   bool
	prefix string
	expiry time.Duration
	maxPut int64
	client *http.Client
	clock  core.Clock
	signer signer
}

// NewS3 returns an S3 blob store. It does not contact the service.
func NewS3(opts S3Options) (*S3, error) {
	u, err := url.Parse(opts.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("blob: invalid S3 endpoint %q", opts.Endpoint)
	}
	switch {
	case opts.Region == "":
		return nil, errors.New("blob: S3 region is required")
	case opts.Bucket == "":
		return nil, errors.New("blob: S3 bucket is required")
	case opts.AccessKeyID == "" || opts.SecretAccessKey == "":
		return nil, errors.New("blob: S3 credentials are required")
	case opts.URLExpiry < 0 || opts.URLExpiry > MaxURLExpiry:
		return nil, fmt.Errorf("blob: S3 URL expiry %v outside (0, %v]", opts.URLExpiry, MaxURLExpiry)
	case opts.URLExpiry != 0 && opts.URLExpiry < time.Second:
		return nil, fmt.Errorf("blob: S3 URL expiry %v under one second", opts.URLExpiry)
	case opts.MaxPutSize < 0:
		return nil, fmt.Errorf("blob: negative S3 max put size %d", opts.MaxPutSize)
	}
	s := &S3{
		scheme: u.Scheme,
		host:   u.Host,
		bucket: opts.Bucket,
		path:   opts.PathStyle,
		expiry: opts.URLExpiry,
		maxPut: opts.MaxPutSize,
		client: opts.HTTPClient,
		clock:  opts.Clock,
		signer: signer{
			accessKeyID:     opts.AccessKeyID,
			secretAccessKey: opts.SecretAccessKey,
			sessionToken:    opts.SessionToken,
			region:          opts.Region,
			service:         "s3",
		},
	}
	if p := strings.Trim(opts.Prefix, "/"); p != "" {
		s.prefix = p + "/"
	}
	if s.expiry == 0 {
		s.expiry = defaultURLTTL
	}
	if s.maxPut == 0 {
		s.maxPut = DefaultMaxPutSize
	}
	if s.client == nil {
		s.client = http.DefaultClient
	}
	if s.clock == nil {
		s.clock = core.SystemClock{}
	}
	return s, nil
}

// objectURL is the URL of the object key.
func (s *S3) objectURL(key string) *url.URL {
	u := &url.URL{Scheme: s.scheme, Host: s.host, Path: "/" + key}
	if s.path {
		u.Path = "/" + s.bucket + "/" + key
	} else {
		u.Host = s.bucket + "." + s.host
	}
	u.RawPath = awsEscape(u.Path, false)
	return u
}

// key is the S3 key of ref of org, after validating both.
func (s *S3) key(org core.OrganizationID, ref core.BlobRef) (string, error) {
	if err := checkOrg(org); err != nil {
		return "", err
	}
	if _, _, err := checkRef(ref); err != nil {
		return "", err
	}
	return s.prefix + objectKey(org, ref), nil
}

// do signs and sends a request for key.
func (s *S3) do(ctx context.Context, method, key string, body []byte, header http.Header) (*http.Response, error) {
	var r io.Reader
	payloadHash := hexSHA256(nil)
	if body != nil {
		r = bytes.NewReader(body)
		payloadHash = hexSHA256(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.objectURL(key).String(), r)
	if err != nil {
		return nil, fmt.Errorf("blob: build S3 request: %w", err)
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	s.signer.sign(req, payloadHash, s.clock.Now())
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blob: S3 %s: %w", method, err)
	}
	return resp, nil
}

// Put implements port.BlobStore with PutObject.
func (s *S3) Put(ctx context.Context, org core.OrganizationID, name, contentType string, body io.Reader) (core.BlobRef, error) {
	if err := checkOrg(org); err != nil {
		return "", err
	}
	contentType, err := normalizeContentType(contentType)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(body, s.maxPut+1))
	if err != nil {
		return "", fmt.Errorf("blob: read body: %w", err)
	}
	if int64(len(data)) > s.maxPut {
		return "", fmt.Errorf("blob: %w: object larger than %d bytes", core.ErrInvalidValue, s.maxPut)
	}
	ref := newRef(name)
	key, _ := s.key(org, ref)
	resp, err := s.do(ctx, http.MethodPut, key, data, http.Header{"Content-Type": {contentType}})
	if err != nil {
		return "", err
	}
	defer drain(resp)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("blob: put %q: %w", ref, readS3Error(resp))
	}
	return ref, nil
}

// Get implements port.BlobStore with GetObject.
func (s *S3) Get(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (io.ReadCloser, error) {
	key, err := s.key(org, ref)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}
	defer drain(resp)
	if err := readS3Error(resp); !isNoSuchKey(err) {
		return nil, fmt.Errorf("blob: get %q: %w", ref, err)
	}
	return nil, fmt.Errorf("blob: %q: %w", ref, core.ErrNotFound)
}

// Delete implements port.BlobStore with DeleteObject.
func (s *S3) Delete(ctx context.Context, org core.OrganizationID, ref core.BlobRef) error {
	key, err := s.key(org, ref)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodDelete, key, nil, nil)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode/100 == 2 {
		return nil
	}
	if err := readS3Error(resp); !isNoSuchKey(err) {
		return fmt.Errorf("blob: delete %q: %w", ref, err)
	}
	return nil
}

// URL implements port.BlobStore with a presigned GetObject URL valid for
// URLExpiry. It does not contact the service: the URL of a missing object,
// or of a reference used with another organization (which names another
// key), answers 404.
func (s *S3) URL(_ context.Context, org core.OrganizationID, ref core.BlobRef) (string, error) {
	key, err := s.key(org, ref)
	if err != nil {
		return "", err
	}
	return s.signer.presign(http.MethodGet, s.objectURL(key), s.clock.Now(), s.expiry).String(), nil
}

// S3Error is an error response of the S3 API.
type S3Error struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Code is the S3 error code, for example "NoSuchKey" or "AccessDenied";
	// empty when the response had no error document.
	Code string
	// Message is the human-readable message.
	Message string
	// RequestID is the request identifier, for support.
	RequestID string
}

// Error implements error.
func (e *S3Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("S3 status %d", e.StatusCode)
	}
	return fmt.Sprintf("S3 status %d: %s: %s (request %s)", e.StatusCode, e.Code, e.Message, e.RequestID)
}

// readS3Error decodes the error document of resp.
func readS3Error(resp *http.Response) error {
	e := &S3Error{StatusCode: resp.StatusCode}
	var doc struct {
		Code      string `xml:"Code"`
		Message   string `xml:"Message"`
		RequestID string `xml:"RequestId"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, maxErrorBody)).Decode(&doc); err == nil {
		e.Code, e.Message, e.RequestID = doc.Code, doc.Message, doc.RequestID
	}
	return e
}

// isNoSuchKey reports whether err is a missing-object response: a 404 with
// the NoSuchKey code or with no error document. Other 404 codes, such as
// NoSuchBucket, are configuration errors.
func isNoSuchKey(err error) bool {
	var e *S3Error
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound && (e.Code == "" || e.Code == "NoSuchKey")
}

// drain discards the rest of resp's body and closes it so the connection
// can be reused.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	_ = resp.Body.Close()
}
