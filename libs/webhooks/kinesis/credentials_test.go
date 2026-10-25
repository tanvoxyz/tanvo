package kinesis

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// The post-vanilla case of the AWS Signature Version 4 test suite.
func TestSignPostVanilla(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://example.amazonaws.com/", nil)
	s := signer{creds: Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, region: "us-east-1", service: "service"}
	s.sign(req, nil, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, Signature=5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization got = %s, want = %s", got, want)
	}
}

func testCaller() caller {
	return caller{hc: NewHTTPClient(), now: time.Now, sleep: func(context.Context, time.Duration) error { return nil }}
}

func TestAmbientChain(t *testing.T) {
	const credsJSON = `{"AccessKeyId":"ASIAC","SecretAccessKey":"s","Token":"tok","Expiration":"2099-01-01T00:00:00Z"}`
	container := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "pod-token" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, credsJSON)
	}))
	defer container.Close()
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			_, _ = io.WriteString(w, "imds-token")
		case r.Header.Get("X-Aws-Ec2-Metadata-Token") != "imds-token":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/":
			_, _ = io.WriteString(w, "worker\n")
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/worker":
			_, _ = io.WriteString(w, `{"Code":"Success","AccessKeyId":"ASIAI","SecretAccessKey":"s","Token":"tok","Expiration":"2099-01-01T00:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer imds.Close()
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f, _ := url.ParseQuery(string(b))
		if f.Get("Action") != "AssumeRoleWithWebIdentity" || f.Get("WebIdentityToken") != "jwt" || r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials>
<AccessKeyId>ASIAW</AccessKeyId><SecretAccessKey>s</SecretAccessKey><SessionToken>t</SessionToken>
<Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`)
	}))
	defer sts.Close()

	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr error
	}{
		{name: "environment", env: map[string]string{"AWS_ACCESS_KEY_ID": "AKIAE", "AWS_SECRET_ACCESS_KEY": "s"}, want: "AKIAE"},
		{
			name: "container credentials", want: "ASIAC",
			env: map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": container.URL + "/v1/credentials", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": "/token"},
		},
		{name: "instance metadata", env: map[string]string{}, want: "ASIAI"},
		{name: "nothing configured", env: map[string]string{"AWS_EC2_METADATA_DISABLED": "true"}, wantErr: ErrMissingCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := ambient{c: testCaller(), env: Environment{
				Getenv:                   func(k string) string { return tt.env[k] },
				ReadFile:                 func(string) ([]byte, error) { return []byte("pod-token\n"), nil },
				InstanceMetadataEndpoint: imds.URL,
			}}
			got, err := a.Retrieve(t.Context())
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Retrieve() error got = %v, want = %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || got.AccessKeyID != tt.want {
				t.Fatalf("Retrieve() got = %s, %v, want = %s, nil", got.AccessKeyID, err, tt.want)
			}
		})
	}

	t.Run("web identity", func(t *testing.T) {
		c := testCaller()
		got, err := assumeRoleWithWebIdentity(t.Context(), c, sts.URL, "arn:aws:iam::1:role/r", "s", "jwt")
		if err != nil || got.AccessKeyID != "ASIAW" || !got.Expires.Equal(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("assumeRoleWithWebIdentity() got = %+v, %v, want = ASIAW expiring 2099", got, err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(dir+"/token", []byte("jwt\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		a := ambient{c: c, stsURL: sts.URL, env: Environment{Getenv: func(k string) string {
			return map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": dir + "/token", "AWS_ROLE_ARN": "arn:aws:iam::1:role/r", "AWS_REGION": "eu-west-1"}[k]
		}}}
		if got, err := a.Retrieve(t.Context()); err != nil || got.AccessKeyID != "ASIAW" {
			t.Errorf("Retrieve() got = %s, %v, want = ASIAW, nil", got.AccessKeyID, err)
		}
	})
}

func TestKinesisErrorCodes(t *testing.T) {
	tests := []struct {
		body, header, want string
	}{
		{body: `{"__type":"ResourceNotFoundException","message":"m"}`, want: "ResourceNotFoundException"},
		{body: `{"__type":"com.amazonaws.kinesis.v20131202#ValidationException","Message":"m"}`, want: "ValidationException"},
		{body: ``, header: "AccessDeniedException:http://internal.amazon.com/", want: "AccessDeniedException"},
	}
	for _, tt := range tests {
		h := http.Header{}
		if tt.header != "" {
			h.Set("X-Amzn-ErrorType", tt.header)
		}
		err := kinesisError(answer{status: 400, header: h, body: []byte(tt.body)})
		var api *APIError
		if !errors.As(err, &api) || api.Code != tt.want || !strings.HasPrefix(errorName(err), "kinesis.") {
			t.Errorf("kinesisError(%s) got = %v, want = %s", tt.body, err, tt.want)
		}
	}
}
