package kinesis

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// stsEndpoint is the regional STS endpoint of region.
func stsEndpoint(region string) string {
	return "https://sts." + region + "." + dnsSuffix(region)
}

// dnsSuffix is the DNS suffix of the partition of region.
func dnsSuffix(region string) string {
	if strings.HasPrefix(region, "cn-") {
		return "amazonaws.com.cn"
	}
	return "amazonaws.com"
}

const stsContentType = "application/x-www-form-urlencoded; charset=utf-8"

// stsCredentials is the Credentials element of an STS answer.
type stsCredentials struct {
	AccessKeyID     string `xml:"AccessKeyId"`
	SecretAccessKey string `xml:"SecretAccessKey"`
	SessionToken    string `xml:"SessionToken"`
	Expiration      string `xml:"Expiration"`
}

// stsAnswer is an AssumeRole or AssumeRoleWithWebIdentity answer.
type stsAnswer struct {
	AssumeRole      stsCredentials `xml:"AssumeRoleResult>Credentials"`
	WebIdentityRole stsCredentials `xml:"AssumeRoleWithWebIdentityResult>Credentials"`
}

// stsError is an STS error answer.
type stsError struct {
	Code    string `xml:"Error>Code"`
	Message string `xml:"Error>Message"`
}

// assumeRole calls STS AssumeRole at endpoint (in region), signed with
// base; externalID is sent only when set.
func assumeRole(ctx context.Context, c caller, endpoint, region string, base Credentials, roleARN, session, externalID string) (Credentials, error) {
	form := url.Values{
		"Action": {"AssumeRole"}, "Version": {"2011-06-15"},
		"RoleArn": {roleARN}, "RoleSessionName": {session},
	}
	if externalID != "" {
		form.Set("ExternalId", externalID)
	}
	body := []byte(form.Encode())
	var creds Credentials
	err := c.call(ctx, func(now time.Time) (*http.Request, error) {
		req, err := newRequest(endpoint+"/", stsContentType, body)
		if err != nil {
			return nil, err
		}
		signer{creds: base, region: region, service: "sts"}.sign(req, body, now)
		return req, nil
	}, func(an answer) error {
		var err error
		creds, err = decodeSTS(an)
		return err
	})
	return creds, err
}

// assumeRoleWithWebIdentity calls STS AssumeRoleWithWebIdentity, which is
// not signed.
func assumeRoleWithWebIdentity(ctx context.Context, c caller, endpoint, roleARN, session, token string) (Credentials, error) {
	body := []byte(url.Values{
		"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"},
		"RoleArn": {roleARN}, "RoleSessionName": {session}, "WebIdentityToken": {token},
	}.Encode())
	var creds Credentials
	err := c.call(ctx, func(time.Time) (*http.Request, error) {
		return newRequest(endpoint+"/", stsContentType, body)
	}, func(an answer) error {
		var err error
		creds, err = decodeSTS(an)
		return err
	})
	return creds, err
}

func decodeSTS(an answer) (Credentials, error) {
	if an.status != http.StatusOK {
		var e stsError
		if err := xml.Unmarshal(an.body, &e); err != nil || e.Code == "" {
			e.Code, e.Message = http.StatusText(an.status), snippet(an.body)
		}
		return Credentials{}, &APIError{Service: "sts", Code: e.Code, Message: e.Message, StatusCode: an.status}
	}
	var a stsAnswer
	if err := xml.Unmarshal(an.body, &a); err != nil {
		return Credentials{}, fmt.Errorf("kinesis: decode STS answer: %w", err)
	}
	c := a.AssumeRole
	if c.AccessKeyID == "" {
		c = a.WebIdentityRole
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return Credentials{}, fmt.Errorf("kinesis: STS answer without credentials: %s", snippet(an.body))
	}
	exp, err := time.Parse(time.RFC3339, strings.TrimSpace(c.Expiration))
	if err != nil {
		return Credentials{}, fmt.Errorf("kinesis: STS credentials expiration: %w", err)
	}
	return Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Expires: exp.UTC()}, nil
}
