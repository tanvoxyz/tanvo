package blob

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// awsExample is the signer of the AWS documentation examples
// (https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html,
// https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-query-string-auth.html).
var (
	awsExample = signer{
		accessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		secretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		region:          "us-east-1",
		service:         "s3",
	}
	awsExampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
)

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestSignHeaderVectors(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		url     string
		header  map[string]string
		payload string
		want    string
	}{
		{
			name:    "GET object with Range",
			method:  http.MethodGet,
			url:     "https://examplebucket.s3.amazonaws.com/test.txt",
			header:  map[string]string{"Range": "bytes=0-9"},
			payload: emptySHA256,
			want:    "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name:   "PUT object",
			method: http.MethodPut,
			url:    "https://examplebucket.s3.amazonaws.com/test$file.text",
			header: map[string]string{
				"Date":                "Fri, 24 May 2013 00:00:00 GMT",
				"X-Amz-Storage-Class": "REDUCED_REDUNDANCY",
			},
			payload: hexSHA256([]byte("Welcome to Amazon S3.")),
			want:    "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name:    "GET bucket lifecycle",
			method:  http.MethodGet,
			url:     "https://examplebucket.s3.amazonaws.com/?lifecycle",
			payload: emptySHA256,
			want:    "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name:    "list objects",
			method:  http.MethodGet,
			url:     "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
			payload: emptySHA256,
			want:    "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, tt.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			awsExample.sign(req, tt.payload, awsExampleTime)
			auth := req.Header.Get("Authorization")
			_, got, _ := strings.Cut(auth, "Signature=")
			if got != tt.want {
				t.Errorf("signature = %s, want = %s (Authorization %q)", got, tt.want, auth)
			}
			if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=") {
				t.Errorf("Authorization = %q, want = the documented credential prefix", auth)
			}
		})
	}
}

func TestPresignVector(t *testing.T) {
	u, err := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := awsExample.presign(http.MethodGet, u, awsExampleTime, 86400*time.Second)
	q := got.Query()
	if sig := q.Get("X-Amz-Signature"); sig != "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404" {
		t.Errorf("X-Amz-Signature = %s, want = aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404", sig)
	}
	want := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    "AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request",
		"X-Amz-Date":          "20130524T000000Z",
		"X-Amz-Expires":       "86400",
		"X-Amz-SignedHeaders": "host",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want = %q", k, q.Get(k), v)
		}
	}
	if got.Host != "examplebucket.s3.amazonaws.com" || got.Path != "/test.txt" {
		t.Errorf("URL = %s, want = https://examplebucket.s3.amazonaws.com/test.txt?…", got)
	}
}

func TestAWSEscape(t *testing.T) {
	tests := []struct {
		in          string
		encodeSlash bool
		want        string
	}{
		{"/a/b c", false, "/a/b%20c"},
		{"/a/b c", true, "%2Fa%2Fb%20c"},
		{"test$file.text", false, "test%24file.text"},
		{"A-Za-z0-9-._~", true, "A-Za-z0-9-._~"},
		{"é+=", true, "%C3%A9%2B%3D"},
	}
	for _, tt := range tests {
		if got := awsEscape(tt.in, tt.encodeSlash); got != tt.want {
			t.Errorf("awsEscape(%q, %v) = %q, want = %q", tt.in, tt.encodeSlash, got, tt.want)
		}
	}
}
