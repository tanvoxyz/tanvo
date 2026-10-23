package blob

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AWS Signature Version 4 constants.
const (
	sigAlgorithm    = "AWS4-HMAC-SHA256"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	amzDateFormat   = "20060102T150405Z"
	scopeDateFormat = "20060102"
	sigTerminator   = "aws4_request"
)

// signer signs requests with AWS Signature Version 4.
type signer struct {
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
	region          string
	service         string
}

// sign adds the X-Amz-Date, X-Amz-Content-Sha256, optional
// X-Amz-Security-Token and Authorization headers to req. Every header
// already on req, plus Host, is signed; headers the transport adds later
// (User-Agent, Accept-Encoding) are not.
func (s signer) sign(req *http.Request, payloadHash string, now time.Time) {
	t := now.UTC()
	amzDate := t.Format(amzDateFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.sessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	signed, canonical := canonicalHeaders(host, req.Header)
	creq := canonicalRequest(req.Method, req.URL.Path, req.URL.Query(), canonical, signed, payloadHash)
	scope := s.scope(t)
	sig := s.signature(t, stringToSign(amzDate, scope, creq))
	req.Header.Set("Authorization", sigAlgorithm+" Credential="+s.accessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
}

// presign returns u with the X-Amz-* query parameters of a presigned
// request of method, valid for expires from now. Only Host is signed and
// the payload is unsigned.
func (s signer) presign(method string, u *url.URL, now time.Time, expires time.Duration) *url.URL {
	t := now.UTC()
	amzDate := t.Format(amzDateFormat)
	scope := s.scope(t)
	q := u.Query()
	q.Set("X-Amz-Algorithm", sigAlgorithm)
	q.Set("X-Amz-Credential", s.accessKeyID+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(expires/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")
	if s.sessionToken != "" {
		q.Set("X-Amz-Security-Token", s.sessionToken)
	}
	creq := canonicalRequest(method, u.Path, q, "host:"+u.Host+"\n", "host", unsignedPayload)
	sig := s.signature(t, stringToSign(amzDate, scope, creq))
	out := *u
	out.RawQuery = canonicalQuery(q) + "&X-Amz-Signature=" + sig
	return &out
}

// scope is the credential scope date/region/service/aws4_request.
func (s signer) scope(t time.Time) string {
	return t.Format(scopeDateFormat) + "/" + s.region + "/" + s.service + "/" + sigTerminator
}

// signature derives the signing key for t's date and signs sts with it.
func (s signer) signature(t time.Time, sts string) string {
	k := hmacSHA256([]byte("AWS4"+s.secretAccessKey), t.Format(scopeDateFormat))
	k = hmacSHA256(k, s.region)
	k = hmacSHA256(k, s.service)
	k = hmacSHA256(k, sigTerminator)
	return hex.EncodeToString(hmacSHA256(k, sts))
}

func stringToSign(amzDate, scope, creq string) string {
	return sigAlgorithm + "\n" + amzDate + "\n" + scope + "\n" + hexSHA256([]byte(creq))
}

// canonicalRequest builds the SigV4 canonical request. S3 paths are encoded
// once and not normalized.
func canonicalRequest(method, path string, query url.Values, headers, signed, payloadHash string) string {
	if path == "" {
		path = "/"
	}
	return method + "\n" + awsEscape(path, false) + "\n" + canonicalQuery(query) + "\n" +
		headers + "\n" + signed + "\n" + payloadHash
}

// canonicalHeaders returns the signed header names and the canonical
// header block of host and h.
func canonicalHeaders(host string, h http.Header) (signed, canonical string) {
	values := map[string]string{"host": host}
	for name, vs := range h {
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		values[strings.ToLower(name)] = strings.Join(trimmed, ",")
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name + ":" + values[name] + "\n")
	}
	return strings.Join(names, ";"), b.String()
}

// canonicalQuery encodes q sorted by key, then value.
func canonicalQuery(q url.Values) string {
	pairs := make([]string, 0, len(q))
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, awsEscape(k, true)+"="+awsEscape(v, true))
		}
	}
	slices.Sort(pairs)
	return strings.Join(pairs, "&")
}

// awsEscape percent-encodes every byte of s except the unreserved
// characters A-Z a-z 0-9 - . _ ~, and '/' unless encodeSlash.
func awsEscape(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' || c == '/' && !encodeSlash {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0xf])
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
