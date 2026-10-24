package netsuite

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// credentials are the token-based authentication keys of a connection.
type credentials struct {
	realm, consumerKey, consumerSecret, token, tokenSecret string
}

// sign sets the OAuth 1.0a Authorization header of r (RFC 5849 §3.4,
// HMAC-SHA256 as NetSuite requires) for timestamp ts and nonce.
func (c credentials) sign(r *http.Request, ts, nonce string) {
	oauth := map[string]string{
		"oauth_consumer_key":     c.consumerKey,
		"oauth_token":            c.token,
		"oauth_signature_method": "HMAC-SHA256",
		"oauth_timestamp":        ts,
		"oauth_nonce":            nonce,
		"oauth_version":          "1.0",
	}
	var params []string
	for k, v := range oauth {
		params = append(params, encode(k)+"="+encode(v))
	}
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			params = append(params, encode(k)+"="+encode(v))
		}
	}
	slices.Sort(params)
	base := strings.ToUpper(r.Method) + "&" + encode(baseURL(r.URL)) + "&" + encode(strings.Join(params, "&"))
	mac := hmac.New(sha256.New, []byte(encode(c.consumerSecret)+"&"+encode(c.tokenSecret)))
	mac.Write([]byte(base))
	oauth["oauth_signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	keys := make([]string, 0, len(oauth))
	for k := range oauth {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	h := []string{`realm="` + encode(c.realm) + `"`}
	for _, k := range keys {
		h = append(h, k+`="`+encode(oauth[k])+`"`)
	}
	r.Header.Set("Authorization", "OAuth "+strings.Join(h, ", "))
}

// baseURL is the scheme, host and path of u, lowercased where RFC 5849
// §3.4.1.2 requires.
func baseURL(u *url.URL) string {
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	case u.Scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return strings.ToLower(u.Scheme) + "://" + host + path
}

// encode is the RFC 3986 percent-encoding of RFC 5849 §3.6.
func encode(s string) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(hexByte(c)))
	}
	return b.String()
}

func hexByte(c byte) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[c>>4], digits[c&0xf]})
}
