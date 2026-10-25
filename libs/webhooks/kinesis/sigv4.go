package kinesis

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"time"
)

// AWS Signature Version 4 constants.
const (
	sigAlgorithm    = "AWS4-HMAC-SHA256"
	amzDateFormat   = "20060102T150405Z"
	scopeDateFormat = "20060102"
	sigTerminator   = "aws4_request"
)

// signer signs requests with AWS Signature Version 4. The requests of
// this package all go to the root path without a query.
type signer struct {
	creds   Credentials
	region  string
	service string
}

// sign adds the X-Amz-Date, optional X-Amz-Security-Token and
// Authorization headers to req for body. Every header already on req,
// plus Host, is signed.
func (s signer) sign(req *http.Request, body []byte, now time.Time) {
	t := now.UTC()
	amzDate := t.Format(amzDateFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	if s.creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.creds.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	signed, canonical := canonicalHeaders(host, req.Header)
	creq := req.Method + "\n" + path + "\n" + req.URL.RawQuery + "\n" + canonical + "\n" + signed + "\n" + hexSHA256(body)
	scope := t.Format(scopeDateFormat) + "/" + s.region + "/" + s.service + "/" + sigTerminator
	sts := sigAlgorithm + "\n" + amzDate + "\n" + scope + "\n" + hexSHA256([]byte(creq))
	k := hmacSHA256([]byte("AWS4"+s.creds.SecretAccessKey), t.Format(scopeDateFormat))
	k = hmacSHA256(k, s.region)
	k = hmacSHA256(k, s.service)
	k = hmacSHA256(k, sigTerminator)
	sig := hex.EncodeToString(hmacSHA256(k, sts))
	req.Header.Set("Authorization", sigAlgorithm+" Credential="+s.creds.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
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

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
