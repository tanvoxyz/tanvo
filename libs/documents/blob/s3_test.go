package blob

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

const (
	testAccessKey = "AKIDTEST"
	testSecretKey = "secret/test/key"
	testRegion    = "eu-west-1"
	testBucket    = "docs"
)

var authPattern = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=` + testAccessKey +
	`/(\d{8})/` + testRegion + `/s3/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})$`)

type fakeObject struct {
	body        []byte
	contentType string
}

// fakeS3 is a path-style S3 that verifies every SigV4 signature, header or
// presigned, and keeps objects in memory.
type fakeS3 struct {
	t        *testing.T
	clock    *testClock
	signer   signer
	requests atomic.Int64

	mu      sync.Mutex
	objects map[string]fakeObject
	headers []http.Header
}

func newFakeS3(t *testing.T, clock *testClock, token string) (*fakeS3, *httptest.Server) {
	t.Helper()
	f := &fakeS3{
		t:     t,
		clock: clock,
		signer: signer{
			accessKeyID: testAccessKey, secretAccessKey: testSecretKey, sessionToken: token,
			region: testRegion, service: "s3",
		},
		objects: map[string]fakeObject{},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message><RequestId>req-1</RequestId></Error>`, code, code)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s3Error(w, http.StatusBadRequest, "IncompleteBody")
		return
	}
	if code := f.verify(r, body); code != "" {
		s3Error(w, http.StatusForbidden, code)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headers = append(f.headers, r.Header.Clone())
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != testBucket {
		s3Error(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	switch r.Method {
	case http.MethodPut:
		f.objects[key] = fakeObject{body: body, contentType: r.Header.Get("Content-Type")}
	case http.MethodGet:
		o, ok := f.objects[key]
		if !ok {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Type", o.contentType)
		_, _ = w.Write(o.body)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

// verify recomputes the request signature; it returns an S3 error code, or
// "" when the request is authentic.
func (f *fakeS3) verify(r *http.Request, body []byte) string {
	q := r.URL.Query()
	if sig := q.Get("X-Amz-Signature"); sig != "" {
		date, err := time.Parse(amzDateFormat, q.Get("X-Amz-Date"))
		if err != nil {
			return "AuthorizationQueryParametersError"
		}
		secs, err := strconv.Atoi(q.Get("X-Amz-Expires"))
		if err != nil || secs > 604800 {
			return "AuthorizationQueryParametersError"
		}
		if f.clock.Now().After(date.Add(time.Duration(secs) * time.Second)) {
			return "AccessDenied"
		}
		if q.Get("X-Amz-Security-Token") != f.signer.sessionToken {
			return "InvalidToken"
		}
		q.Del("X-Amz-Signature")
		creq := canonicalRequest(r.Method, r.URL.Path, q, "host:"+r.Host+"\n", "host", unsignedPayload)
		want := f.signer.signature(date, stringToSign(q.Get("X-Amz-Date"), f.signer.scope(date), creq))
		if !hmac.Equal([]byte(sig), []byte(want)) {
			return "SignatureDoesNotMatch"
		}
		return ""
	}
	m := authPattern.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil {
		f.t.Errorf("Authorization = %q, want = the SigV4 header shape", r.Header.Get("Authorization"))
		return "AuthorizationHeaderMalformed"
	}
	if r.Header.Get("X-Amz-Content-Sha256") != hexSHA256(body) {
		return "XAmzContentSHA256Mismatch"
	}
	if r.Header.Get("X-Amz-Security-Token") != f.signer.sessionToken {
		return "InvalidToken"
	}
	date, err := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
	if err != nil || date.Format(scopeDateFormat) != m[1] {
		return "AuthorizationHeaderMalformed"
	}
	signedNames := strings.Split(m[2], ";")
	for _, required := range []string{"host", "x-amz-content-sha256", "x-amz-date"} {
		if !slices.Contains(signedNames, required) {
			f.t.Errorf("SignedHeaders = %s, want = it to include %s", m[2], required)
		}
	}
	h := http.Header{}
	for _, name := range signedNames {
		if name != "host" {
			h[http.CanonicalHeaderKey(name)] = r.Header.Values(name)
		}
	}
	signed, canonical := canonicalHeaders(r.Host, h)
	creq := canonicalRequest(r.Method, r.URL.Path, q, canonical, signed, r.Header.Get("X-Amz-Content-Sha256"))
	want := f.signer.signature(date, stringToSign(r.Header.Get("X-Amz-Date"), f.signer.scope(date), creq))
	if signed != m[2] || !hmac.Equal([]byte(m[3]), []byte(want)) {
		return "SignatureDoesNotMatch"
	}
	return ""
}

func newTestS3(t *testing.T, token string, mod func(*S3Options)) (*S3, *fakeS3, *testClock) {
	t.Helper()
	clock := newTestClock()
	f, srv := newFakeS3(t, clock, token)
	opts := S3Options{
		Endpoint:        srv.URL,
		Region:          testRegion,
		Bucket:          testBucket,
		AccessKeyID:     testAccessKey,
		SecretAccessKey: testSecretKey,
		SessionToken:    token,
		PathStyle:       true,
		Prefix:          "/tanvo/",
		URLExpiry:       5 * time.Minute,
		HTTPClient:      srv.Client(),
		Clock:           clock,
	}
	if mod != nil {
		mod(&opts)
	}
	s, err := NewS3(opts)
	if err != nil {
		t.Fatalf("NewS3 = %v, want = nil", err)
	}
	return s, f, clock
}

func TestNewS3Validates(t *testing.T) {
	ok := S3Options{
		Endpoint: "https://s3.amazonaws.com", Region: "us-east-1", Bucket: "b",
		AccessKeyID: "a", SecretAccessKey: "s",
	}
	tests := []struct {
		name string
		mod  func(*S3Options)
	}{
		{"bad scheme", func(o *S3Options) { o.Endpoint = "ftp://h" }},
		{"no host", func(o *S3Options) { o.Endpoint = "https://" }},
		{"path in endpoint", func(o *S3Options) { o.Endpoint = "https://h/x" }},
		{"no region", func(o *S3Options) { o.Region = "" }},
		{"no bucket", func(o *S3Options) { o.Bucket = "" }},
		{"no secret", func(o *S3Options) { o.SecretAccessKey = "" }},
		{"expiry too long", func(o *S3Options) { o.URLExpiry = MaxURLExpiry + time.Second }},
		{"expiry too short", func(o *S3Options) { o.URLExpiry = time.Millisecond }},
		{"negative max put", func(o *S3Options) { o.MaxPutSize = -1 }},
	}
	if _, err := NewS3(ok); err != nil {
		t.Fatalf("NewS3(valid) = %v, want = nil", err)
	}
	for _, tt := range tests {
		opts := ok
		tt.mod(&opts)
		if _, err := NewS3(opts); err == nil {
			t.Errorf("%s: NewS3 = nil, want = an error", tt.name)
		}
	}
}

func TestS3ObjectURL(t *testing.T) {
	tests := []struct {
		pathStyle bool
		endpoint  string
		want      string
	}{
		{false, "https://s3.eu-west-1.amazonaws.com", "https://docs.s3.eu-west-1.amazonaws.com/a/b%20c"},
		{true, "http://127.0.0.1:9000", "http://127.0.0.1:9000/docs/a/b%20c"},
	}
	for _, tt := range tests {
		s, err := NewS3(S3Options{
			Endpoint: tt.endpoint, Region: "r", Bucket: testBucket, AccessKeyID: "a", SecretAccessKey: "s",
			PathStyle: tt.pathStyle,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := s.objectURL("a/b c").String(); got != tt.want {
			t.Errorf("objectURL(path style %v) = %s, want = %s", tt.pathStyle, got, tt.want)
		}
	}
}

func TestS3RoundTrip(t *testing.T) {
	for _, token := range []string{"", "session-token"} {
		t.Run("token="+token, func(t *testing.T) {
			ctx := context.Background()
			s, f, _ := newTestS3(t, token, nil)
			org, _ := testOrgs(t)
			ref, err := s.Put(ctx, org, "Invoice 7.pdf", "application/pdf", strings.NewReader("%PDF"))
			if err != nil {
				t.Fatalf("Put = %v, want = nil", err)
			}
			key := "tanvo/" + org.String() + "/" + string(ref)
			f.mu.Lock()
			o, ok := f.objects[key]
			put := f.headers[len(f.headers)-1]
			f.mu.Unlock()
			if !ok || string(o.body) != "%PDF" || o.contentType != "application/pdf" {
				t.Errorf("stored %q = %+v (present %v), want = %%PDF as application/pdf", key, o, ok)
			}
			if !strings.Contains(put.Get("Authorization"), "content-type;host;") {
				t.Errorf("PUT Authorization = %q, want = Content-Type signed", put.Get("Authorization"))
			}
			if token != "" && !strings.Contains(put.Get("Authorization"), "x-amz-security-token") {
				t.Errorf("PUT Authorization = %q, want = X-Amz-Security-Token signed", put.Get("Authorization"))
			}
			rc, err := s.Get(ctx, org, ref)
			if err != nil {
				t.Fatalf("Get = %v, want = nil", err)
			}
			if got := readAll(t, rc); got != "%PDF" {
				t.Errorf("body = %q, want = %%PDF", got)
			}
			if err := s.Delete(ctx, org, ref); err != nil {
				t.Fatalf("Delete = %v, want = nil", err)
			}
			if _, err := s.Get(ctx, org, ref); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Get after Delete = %v, want = ErrNotFound", err)
			}
			if err := s.Delete(ctx, org, ref); err != nil {
				t.Errorf("second Delete = %v, want = nil", err)
			}
		})
	}
}

func TestS3PutLimits(t *testing.T) {
	ctx := context.Background()
	s, f, _ := newTestS3(t, "", func(o *S3Options) { o.MaxPutSize = 4 })
	org, _ := testOrgs(t)
	if _, err := s.Put(ctx, org, "a", "", strings.NewReader("12345")); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Put over the limit = %v, want = ErrInvalidValue", err)
	}
	if _, err := s.Put(ctx, org, "a", "bad type;", strings.NewReader("1")); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Put with bad content type = %v, want = ErrInvalidValue", err)
	}
	if _, err := s.Put(ctx, core.OrganizationID{}, "a", "", strings.NewReader("1")); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Put without org = %v, want = ErrInvalidValue", err)
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("requests = %d, want = 0", n)
	}
	ref, err := s.Put(ctx, org, "a", "", strings.NewReader("1234"))
	if err != nil {
		t.Fatalf("Put at the limit = %v, want = nil", err)
	}
	f.mu.Lock()
	ct := f.objects["tanvo/"+org.String()+"/"+string(ref)].contentType
	f.mu.Unlock()
	if ct != defaultContentType {
		t.Errorf("content type = %q, want = %q", ct, defaultContentType)
	}
}

func TestS3Tenancy(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestS3(t, "", nil)
	a, b := testOrgs(t)
	ref, err := s.Put(ctx, a, "logo.png", "image/png", strings.NewReader("png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, b, ref); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get from other org = %v, want = ErrNotFound", err)
	}
	if err := s.Delete(ctx, b, ref); err != nil {
		t.Errorf("Delete from other org = %v, want = nil", err)
	}
	rc, err := s.Get(ctx, a, ref)
	if err != nil {
		t.Fatalf("Get after other org's Delete = %v, want = nil", err)
	}
	_ = rc.Close()
	u, err := s.URL(ctx, b, ref)
	if err != nil {
		t.Fatal(err)
	}
	if code := fetch(t, s, u); code != http.StatusNotFound {
		t.Errorf("GET of other org's URL = %d, want = %d", code, http.StatusNotFound)
	}
}

func TestS3RejectsBadRefs(t *testing.T) {
	ctx := context.Background()
	s, f, _ := newTestS3(t, "", nil)
	org, _ := testOrgs(t)
	for _, ref := range badRefs() {
		if _, err := s.Get(ctx, org, ref); !errors.Is(err, core.ErrNotFound) || !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("Get(%q) = %v, want = ErrInvalidValue and ErrNotFound", ref, err)
		}
		if _, err := s.URL(ctx, org, ref); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("URL(%q) = %v, want = ErrInvalidValue", ref, err)
		}
		if err := s.Delete(ctx, org, ref); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("Delete(%q) = %v, want = ErrInvalidValue", ref, err)
		}
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("requests = %d, want = 0", n)
	}
}

func TestS3ErrorMapping(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestS3(t, "", func(o *S3Options) { o.Bucket = "other" })
	org, _ := testOrgs(t)
	ref := core.BlobRef("0123456789abcdef0123456789abcdef/x.pdf")
	_, err := s.Get(ctx, org, ref)
	var se *S3Error
	if errors.Is(err, core.ErrNotFound) || !errors.As(err, &se) || se.Code != "NoSuchBucket" || se.RequestID != "req-1" {
		t.Errorf("Get from missing bucket = %v, want = an S3Error NoSuchBucket, not ErrNotFound", err)
	}
	if err := s.Delete(ctx, org, ref); !errors.As(err, &se) || se.Code != "NoSuchBucket" {
		t.Errorf("Delete in missing bucket = %v, want = an S3Error NoSuchBucket", err)
	}
	if _, err := s.Put(ctx, org, "x", "", strings.NewReader("x")); !errors.As(err, &se) || se.StatusCode != http.StatusNotFound {
		t.Errorf("Put in missing bucket = %v, want = an S3Error 404", err)
	}

	bad, _, _ := newTestS3(t, "", func(o *S3Options) { o.SecretAccessKey = "wrong" })
	if _, err := bad.Get(ctx, org, ref); !errors.As(err, &se) || se.Code != "SignatureDoesNotMatch" {
		t.Errorf("Get with wrong secret = %v, want = an S3Error SignatureDoesNotMatch", err)
	}
}

// fetch GETs a presigned URL and returns the status.
func fetch(t *testing.T, s *S3, rawURL string) int {
	t.Helper()
	resp, err := s.client.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer drain(resp)
	return resp.StatusCode
}

func TestS3URL(t *testing.T) {
	ctx := context.Background()
	s, _, clock := newTestS3(t, "tok", nil)
	org, _ := testOrgs(t)
	ref, err := s.Put(ctx, org, "export.csv", "text/csv", strings.NewReader("a,b"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.URL(ctx, org, ref)
	if err != nil {
		t.Fatalf("URL = %v, want = nil", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"X-Amz-Expires":        "300",
		"X-Amz-Date":           "20260301T120000Z",
		"X-Amz-Security-Token": "tok",
		"X-Amz-SignedHeaders":  "host",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want = %q", k, got, want)
		}
	}
	if want := "/docs/tanvo/" + org.String() + "/" + string(ref); u.Path != want {
		t.Errorf("path = %s, want = %s", u.Path, want)
	}
	if code := fetch(t, s, raw); code != http.StatusOK {
		t.Errorf("GET = %d, want = %d", code, http.StatusOK)
	}
	if code := fetch(t, s, strings.Replace(raw, "export.csv", "export2.csv", 1)); code != http.StatusForbidden {
		t.Errorf("GET of tampered URL = %d, want = %d", code, http.StatusForbidden)
	}
	clock.Advance(5*time.Minute + time.Second)
	if code := fetch(t, s, raw); code != http.StatusForbidden {
		t.Errorf("GET after expiry = %d, want = %d", code, http.StatusForbidden)
	}
}
