package kinesis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Credentials are AWS credentials.
type Credentials struct {
	// AccessKeyID and SecretAccessKey sign requests.
	AccessKeyID, SecretAccessKey string
	// SessionToken is set on temporary credentials.
	SessionToken string
	// Expires is when temporary credentials expire; zero for long-term
	// ones.
	Expires time.Time
}

// CredentialsProvider supplies AWS credentials.
type CredentialsProvider interface {
	// Retrieve returns valid credentials.
	Retrieve(ctx context.Context) (Credentials, error)
}

// StaticCredentials is a CredentialsProvider of fixed credentials.
type StaticCredentials Credentials

// Retrieve returns the credentials.
func (s StaticCredentials) Retrieve(context.Context) (Credentials, error) { return Credentials(s), nil }

// refreshWindow is how long before their expiry cached credentials are
// renewed.
const refreshWindow = 5 * time.Minute

// cachedCredentials caches the credentials of fetch until shortly before
// they expire. A failed fetch is not cached.
type cachedCredentials struct {
	fetch func(ctx context.Context) (Credentials, error)
	now   func() time.Time

	mu    sync.Mutex
	creds Credentials
	ok    bool
}

func (c *cachedCredentials) Retrieve(ctx context.Context) (Credentials, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && (c.creds.Expires.IsZero() || c.now().Add(refreshWindow).Before(c.creds.Expires)) {
		return c.creds, nil
	}
	creds, err := c.fetch(ctx)
	if err != nil {
		return Credentials{}, err
	}
	c.creds, c.ok = creds, true
	return creds, nil
}

// Environment reads the ambient configuration of the AWS default
// credential chain.
type Environment struct {
	// Getenv reads a variable; nil is os.Getenv.
	Getenv func(string) string
	// ReadFile reads a file; nil is os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// InstanceMetadataEndpoint is the base URL of the EC2 instance
	// metadata service; empty is http://169.254.169.254.
	InstanceMetadataEndpoint string
}

func (e Environment) getenv(k string) string {
	if e.Getenv == nil {
		return os.Getenv(k)
	}
	return e.Getenv(k)
}

func (e Environment) readFile(name string) ([]byte, error) {
	if e.ReadFile == nil {
		return os.ReadFile(name)
	}
	return e.ReadFile(name)
}

// errNotConfigured is a credential source the environment does not
// configure; the chain moves on to the next.
var errNotConfigured = errors.New("not configured")

// imdsTimeout bounds each call to the instance metadata service, so a
// process off EC2 learns quickly that it has no instance profile.
const imdsTimeout = time.Second

// ambient is the default credential chain: the environment variables,
// then a web identity token file, then the container credentials
// endpoint, then the instance metadata service. The first source the
// environment configures is used; none is ErrMissingCredentials.
type ambient struct {
	env Environment
	c   caller
	// stsURL overrides the STS endpoint of the web identity source.
	stsURL string
}

func (a ambient) Retrieve(ctx context.Context) (Credentials, error) {
	for _, source := range []func(context.Context) (Credentials, error){a.fromEnv, a.fromWebIdentity, a.fromContainer, a.fromInstance} {
		creds, err := source(ctx)
		if errors.Is(err, errNotConfigured) {
			continue
		}
		return creds, err
	}
	return Credentials{}, ErrMissingCredentials
}

func (a ambient) fromEnv(context.Context) (Credentials, error) {
	id, secret := a.env.getenv("AWS_ACCESS_KEY_ID"), a.env.getenv("AWS_SECRET_ACCESS_KEY")
	if id == "" || secret == "" {
		return Credentials{}, errNotConfigured
	}
	return Credentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: a.env.getenv("AWS_SESSION_TOKEN")}, nil
}

func (a ambient) fromWebIdentity(ctx context.Context) (Credentials, error) {
	file, role := a.env.getenv("AWS_WEB_IDENTITY_TOKEN_FILE"), a.env.getenv("AWS_ROLE_ARN")
	if file == "" || role == "" {
		return Credentials{}, errNotConfigured
	}
	token, err := a.env.readFile(file)
	if err != nil {
		return Credentials{}, fmt.Errorf("kinesis: web identity token: %w", err)
	}
	session := a.env.getenv("AWS_ROLE_SESSION_NAME")
	if session == "" {
		session = fmt.Sprintf("tanvo-%d", time.Now().UnixNano())
	}
	region := a.env.getenv("AWS_REGION")
	if region == "" {
		region = a.env.getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}
	endpoint := a.stsURL
	if endpoint == "" {
		endpoint = stsEndpoint(region)
	}
	return assumeRoleWithWebIdentity(ctx, a.c, endpoint, role, session, strings.TrimSpace(string(token)))
}

// containerHost is the host of a relative container credentials URI.
const containerHost = "http://169.254.170.2"

func (a ambient) fromContainer(ctx context.Context) (Credentials, error) {
	uri := a.env.getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	if rel := a.env.getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); rel != "" {
		uri = containerHost + rel
	}
	if uri == "" {
		return Credentials{}, errNotConfigured
	}
	auth := a.env.getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN")
	if file := a.env.getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"); file != "" {
		b, err := a.env.readFile(file)
		if err != nil {
			return Credentials{}, fmt.Errorf("kinesis: container authorization token: %w", err)
		}
		auth = strings.TrimSpace(string(b))
	}
	var creds Credentials
	err := a.c.call(ctx, func(time.Time) (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, uri, nil)
		if err != nil {
			return nil, fmt.Errorf("kinesis: container credentials: %w", err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return req, nil
	}, func(an answer) error {
		var err error
		creds, err = decodeMetadataCredentials("container credentials", an)
		return err
	})
	return creds, err
}

func (a ambient) fromInstance(ctx context.Context) (Credentials, error) {
	if strings.EqualFold(a.env.getenv("AWS_EC2_METADATA_DISABLED"), "true") {
		return Credentials{}, errNotConfigured
	}
	base := a.env.InstanceMetadataEndpoint
	if base == "" {
		base = "http://169.254.169.254"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*imdsTimeout)
	defer cancel()
	get := func(method, path string, header http.Header) (answer, error) {
		ictx, icancel := context.WithTimeout(ctx, imdsTimeout)
		defer icancel()
		req, err := http.NewRequestWithContext(ictx, method, base+path, nil)
		if err != nil {
			return answer{}, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		resp, err := a.c.hc.Do(req)
		if err != nil {
			return answer{}, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return answer{}, err
		}
		return answer{status: resp.StatusCode, header: resp.Header, body: body}, nil
	}
	tok, err := get(http.MethodPut, "/latest/api/token", http.Header{"X-Aws-Ec2-Metadata-Token-Ttl-Seconds": {"21600"}})
	if err != nil || tok.status != http.StatusOK {
		// No instance metadata service: not on EC2.
		return Credentials{}, errNotConfigured
	}
	h := http.Header{"X-Aws-Ec2-Metadata-Token": {strings.TrimSpace(string(tok.body))}}
	role, err := get(http.MethodGet, "/latest/meta-data/iam/security-credentials/", h)
	if err != nil || role.status != http.StatusOK {
		return Credentials{}, errNotConfigured
	}
	name := strings.TrimSpace(strings.SplitN(string(role.body), "\n", 2)[0])
	if name == "" {
		return Credentials{}, errNotConfigured
	}
	an, err := get(http.MethodGet, "/latest/meta-data/iam/security-credentials/"+name, h)
	if err != nil {
		return Credentials{}, &NetworkError{Err: err}
	}
	return decodeMetadataCredentials("instance metadata", an)
}

// decodeMetadataCredentials decodes the credentials document of the
// container and instance metadata endpoints.
func decodeMetadataCredentials(source string, an answer) (Credentials, error) {
	if an.status != http.StatusOK {
		return Credentials{}, fmt.Errorf("kinesis: %s: HTTP %d: %s", source, an.status, snippet(an.body))
	}
	var doc struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		Token           string `json:"Token"`
		Expiration      string `json:"Expiration"`
	}
	if err := json.Unmarshal(an.body, &doc); err != nil {
		return Credentials{}, fmt.Errorf("kinesis: %s: decode: %w", source, err)
	}
	if doc.AccessKeyID == "" || doc.SecretAccessKey == "" {
		return Credentials{}, fmt.Errorf("kinesis: %s: %w", source, ErrMissingCredentials)
	}
	creds := Credentials{AccessKeyID: doc.AccessKeyID, SecretAccessKey: doc.SecretAccessKey, SessionToken: doc.Token}
	if doc.Expiration != "" {
		exp, err := time.Parse(time.RFC3339, doc.Expiration)
		if err != nil {
			return Credentials{}, fmt.Errorf("kinesis: %s: expiration: %w", source, err)
		}
		creds.Expires = exp.UTC()
	}
	return creds, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
