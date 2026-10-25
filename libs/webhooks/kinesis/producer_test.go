package kinesis

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/streaming"
)

// fakeAWS is a recorded STS and Kinesis endpoint.
type fakeAWS struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	assumes []url.Values // AssumeRole forms, in order
	signers []string     // access key of each AssumeRole's signature
	puts    []map[string]string
	putKeys []string // access key of each PutRecord's signature
	put     func(n int) (int, string)
	assume  func(form url.Values) (int, string)
}

func newFakeAWS(t *testing.T) *fakeAWS {
	f := &fakeAWS{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

var credentialRe = regexp.MustCompile(`Credential=([^/]+)/\d{8}/([^/]+)/([^/]+)/aws4_request`)

func (f *fakeAWS) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m := credentialRe.FindStringSubmatch(r.Header.Get("Authorization"))
	f.mu.Lock()
	defer f.mu.Unlock()
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		if target != "Kinesis_20131202.PutRecord" || m == nil || m[3] != "kinesis" {
			http.Error(w, "unexpected request "+target, http.StatusBadRequest)
			return
		}
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		f.puts = append(f.puts, in)
		f.putKeys = append(f.putKeys, m[1])
		status, out := 200, `{"ShardId":"shardId-000000000000","SequenceNumber":"4966"}`
		if f.put != nil {
			status, out = f.put(len(f.puts))
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
		return
	}
	form, _ := url.ParseQuery(string(body))
	if form.Get("Action") != "AssumeRole" || m == nil || m[3] != "sts" {
		http.Error(w, "unexpected STS request", http.StatusBadRequest)
		return
	}
	f.assumes = append(f.assumes, form)
	f.signers = append(f.signers, m[1])
	status, out := 200, fmt.Sprintf(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleResult><Credentials>
    <AccessKeyId>ASIA%d</AccessKeyId><SecretAccessKey>secret</SecretAccessKey>
    <SessionToken>token</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration>
  </Credentials></AssumeRoleResult></AssumeRoleResponse>`, len(f.assumes))
	if f.assume != nil {
		status, out = f.assume(form)
	}
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, out)
}

func (f *fakeAWS) assumedRoles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.assumes {
		out = append(out, a.Get("RoleArn"))
	}
	return out
}

// logBuffer collects text log records without their time.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *logBuffer) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{ReplaceAttr: func(g []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey && len(g) == 0 {
			return slog.Attr{}
		}
		return a
	}}))
}

type harness struct {
	aws  *fakeAWS
	logs *logBuffer
	env  map[string]string
	p    *Producer
}

func newHarness(t *testing.T, edit func(*Config)) *harness {
	h := &harness{aws: newFakeAWS(t), logs: &logBuffer{}, env: map[string]string{
		"AWS_ACCESS_KEY_ID": "akid", "AWS_SECRET_ACCESS_KEY": "secret", "AWS_EC2_METADATA_DISABLED": "true",
	}}
	cfg := Config{
		Environment:     Environment{Getenv: func(k string) string { return h.env[k] }},
		KinesisEndpoint: h.aws.srv.URL, STSEndpoint: h.aws.srv.URL,
		Logger: h.logs.logger(),
		Sleep:  func(context.Context, time.Duration) error { return nil },
	}
	if edit != nil {
		edit(&cfg)
	}
	h.p = New(cfg)
	return h
}

var ids = core.UUIDv7Generator{}

// destination is the destination of the fixtures.
//
// source: spec/factories/streaming_destinations.rb
func destination() *streaming.Destination {
	return &streaming.Destination{
		ID: core.NewID[core.StreamingDestinationID](ids), OrganizationID: core.NewID[core.OrganizationID](ids),
		Type: streaming.TypeKinesis, Active: true, EventTypes: streaming.EventTypes(),
		Kinesis: streaming.KinesisSettings{
			StreamARN: "arn:aws:kinesis:eu-west-1:123456789012:stream/tanvo-streaming-sandbox",
			Region:    "eu-west-1",
			RoleARN:   "arn:aws:iam::123456789012:role/tanvo-streaming-sandbox-writer",
		},
	}
}

var hello = []byte(`{"hello":"world"}`)

// source: spec/lib/lago/kinesis/producer_spec.rb:26
func TestProducePutsTheRecord(t *testing.T) {
	h := newHarness(t, nil)
	d := destination()
	if err := h.p.Produce(t.Context(), d, hello, "cust_1"); err != nil {
		t.Fatalf("Produce() = %v, want = nil", err)
	}
	if len(h.aws.puts) != 1 {
		t.Fatalf("PutRecord calls got = %d, want = 1", len(h.aws.puts))
	}
	got := h.aws.puts[0]
	if got["StreamARN"] != d.Kinesis.StreamARN || got["PartitionKey"] != "cust_1" || got["Data"] != base64.StdEncoding.EncodeToString(hello) {
		t.Errorf("PutRecord got = %v, want = the stream ARN, cust_1 and the base64 data", got)
	}
	if h.aws.putKeys[0] != "ASIA1" {
		t.Errorf("PutRecord signed by got = %s, want = ASIA1 (the assumed role)", h.aws.putKeys[0])
	}
	if h.aws.signers[0] != "akid" {
		t.Errorf("AssumeRole signed by got = %s, want = akid (the ambient identity)", h.aws.signers[0])
	}
}

// source: spec/lib/lago/kinesis/producer_spec.rb:38
func TestProduceLogsDelivered(t *testing.T) {
	h := newHarness(t, nil)
	d := destination()
	if err := h.p.Produce(t.Context(), d, hello, "cust_1"); err != nil {
		t.Fatalf("Produce() = %v, want = nil", err)
	}
	re := regexp.MustCompile(`level=INFO msg="streaming delivery" outcome=delivered destination_id=` + d.ID.String() +
		` organization_id=` + d.OrganizationID.String() + ` .*partition_key=cust_1 bytes=17 shard=shardId-000000000000 sequence=4966 duration_ms=\d+`)
	if got := h.logs.String(); !re.MatchString(got) {
		t.Errorf("log got = %s, want = matching %s", got, re)
	}
}

func TestProduceDeliveryErrors(t *testing.T) {
	type tc struct {
		name, source, outcome, errName string
		put                            func(int) (int, string)
		assume                         func(url.Values) (int, string)
		env                            map[string]string
		unavailable                    bool
	}
	kinesisErr := func(code string, status int) func(int) (int, string) {
		return func(int) (int, string) {
			return status, `{"__type":"` + code + `","message":"test"}`
		}
	}
	tests := []tc{
		{name: "ResourceNotFound", put: kinesisErr("ResourceNotFoundException", 400), outcome: "dropped", errName: "kinesis.ResourceNotFoundException"},
		{name: "AccessDenied", put: kinesisErr("AccessDeniedException", 400), outcome: "dropped", errName: "kinesis.AccessDeniedException"},
		{name: "Validation", put: kinesisErr("ValidationException", 400), outcome: "dropped", errName: "kinesis.ValidationException"},
		{name: "InvalidArgument", put: kinesisErr("com.amazonaws.kinesis.v20131202#InvalidArgumentException", 400), outcome: "dropped", errName: "kinesis.InvalidArgumentException"},
		{
			name: "ProvisionedThroughputExceeded", source: "spec/lib/lago/kinesis/producer_spec.rb:64",
			put: kinesisErr("ProvisionedThroughputExceededException", 400), outcome: "throttled", errName: "kinesis.ProvisionedThroughputExceededException",
		},
		{
			name: "STS AccessDenied", outcome: "dropped", errName: "sts.AccessDenied", unavailable: true,
			assume: func(url.Values) (int, string) {
				return 403, `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>test</Message></Error></ErrorResponse>`
			},
		},
		{name: "MissingCredentials", source: "spec/lib/lago/kinesis/producer_spec.rb:261", env: map[string]string{"AWS_EC2_METADATA_DISABLED": "true"}, outcome: "dropped", errName: "MissingCredentials", unavailable: true},
		{name: "NetworkingError", outcome: "dropped", errName: "NetworkingError"},
	}
	for _, tt := range tests {
		// source: spec/lib/lago/kinesis/producer_spec.rb:51-52
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, func(c *Config) {
				if tt.name == "NetworkingError" {
					c.KinesisEndpoint = "http://127.0.0.1:1"
				}
			})
			h.aws.put, h.aws.assume = tt.put, tt.assume
			if tt.env != nil {
				h.env = tt.env
			}
			d := destination()
			if err := h.p.Produce(t.Context(), d, hello, "cust_1"); err != nil {
				t.Fatalf("Produce() = %v, want = nil", err)
			}
			re := regexp.MustCompile(`level=ERROR msg="streaming delivery" outcome=` + tt.outcome + ` .*error=` + regexp.QuoteMeta(tt.errName))
			if got := h.logs.String(); !re.MatchString(got) {
				t.Errorf("log got = %s, want = matching %s", got, re)
			}
			if got := h.p.CredentialsAvailable(d); got == tt.unavailable {
				t.Errorf("CredentialsAvailable() got = %v, want = %v", got, !tt.unavailable)
			}
		})
	}
}

// source: spec/lib/lago/kinesis/producer_spec.rb:74
func TestProduceReturnsOtherErrors(t *testing.T) {
	tests := []struct {
		name string
		edit func(*streaming.Destination)
		put  func(int) (int, string)
	}{
		{name: "an internal failure", put: func(int) (int, string) { return 500, `{"__type":"InternalFailure","message":"boom"}` }},
		{name: "a malformed stream ARN", edit: func(d *streaming.Destination) { d.Kinesis.StreamARN = "arn" }},
		{name: "a malformed region", edit: func(d *streaming.Destination) { d.Kinesis.Region = "eu-west-1.evil.example" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.aws.put = tt.put
			d := destination()
			if tt.edit != nil {
				tt.edit(d)
			}
			if err := h.p.Produce(t.Context(), d, hello, "cust_1"); err == nil {
				t.Fatal("Produce() = nil, want = an error")
			}
			if got := h.logs.String(); got != "" {
				t.Errorf("log got = %s, want = nothing", got)
			}
			if !h.p.CredentialsAvailable(d) {
				t.Error("CredentialsAvailable() got = false, want = true")
			}
		})
	}
}

// source: spec/lib/lago/kinesis/producer_spec.rb:82
func TestClientLimits(t *testing.T) {
	hc := NewHTTPClient()
	tr := hc.Transport.(*http.Transport)
	if tr.TLSHandshakeTimeout != OpenTimeout || tr.ResponseHeaderTimeout != ReadTimeout || hc.Timeout != OpenTimeout+ReadTimeout {
		t.Errorf("limits got = %v, %v, %v, want = %v, %v, %v", tr.TLSHandshakeTimeout, tr.ResponseHeaderTimeout, hc.Timeout,
			OpenTimeout, ReadTimeout, OpenTimeout+ReadTimeout)
	}
	if OpenTimeout != 2*time.Second || ReadTimeout != 5*time.Second || MaxAttempts != 2 {
		t.Errorf("constants got = %v, %v, %d, want = 2s, 5s, 2", OpenTimeout, ReadTimeout, MaxAttempts)
	}
}

// source: spec/lib/lago/kinesis/producer_spec.rb:98
func TestAttemptBudget(t *testing.T) {
	tests := []struct {
		name   string
		status int
		code   string
		want   int
	}{
		{name: "a throttle is retried once", status: 400, code: "ProvisionedThroughputExceededException", want: 2},
		{name: "a transient failure is retried once", status: 503, code: "ServiceUnavailable", want: 2},
		{name: "a configuration fault is not retried", status: 400, code: "ResourceNotFoundException", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.env["AWS_RETRY_MODE"], h.env["AWS_MAX_ATTEMPTS"] = "standard", "20"
			h.aws.put = func(int) (int, string) { return tt.status, `{"__type":"` + tt.code + `"}` }
			_ = h.p.Produce(t.Context(), destination(), hello, "cust_1")
			if got := len(h.aws.puts); got != tt.want {
				t.Errorf("attempts got = %d, want = %d", got, tt.want)
			}
		})
	}
	t.Run("a retry that succeeds delivers", func(t *testing.T) {
		h := newHarness(t, nil)
		h.aws.put = func(n int) (int, string) {
			if n == 1 {
				return 400, `{"__type":"ProvisionedThroughputExceededException"}`
			}
			return 200, `{"ShardId":"shardId-000000000001","SequenceNumber":"1"}`
		}
		if err := h.p.Produce(t.Context(), destination(), hello, "cust_1"); err != nil {
			t.Fatalf("Produce() = %v, want = nil", err)
		}
		if got := h.logs.String(); !strings.Contains(got, "outcome=delivered") {
			t.Errorf("log got = %s, want = delivered", got)
		}
	})
}

func TestAssumedCredentials(t *testing.T) {
	t.Run("assumes the role once per process", func(t *testing.T) {
		// source: spec/lib/lago/kinesis/producer_spec.rb:113
		h := newHarness(t, nil)
		d := destination()
		for _, key := range []string{"cust_1", "cust_2"} {
			if err := h.p.Produce(t.Context(), d, hello, key); err != nil {
				t.Fatalf("Produce() = %v, want = nil", err)
			}
		}
		if got := h.aws.assumedRoles(); len(got) != 1 || got[0] != d.Kinesis.RoleARN {
			t.Fatalf("AssumeRole got = %v, want = [%s]", got, d.Kinesis.RoleARN)
		}
		if got := h.aws.assumes[0].Get("RoleSessionName"); got != RoleSessionName {
			t.Errorf("RoleSessionName got = %s, want = %s", got, RoleSessionName)
		}
	})
	t.Run("passes the external id when set", func(t *testing.T) {
		// source: spec/lib/lago/kinesis/producer_spec.rb:123
		h := newHarness(t, nil)
		d := destination()
		d.Kinesis.ExternalID = "00000000-0000-4000-8000-000000000001"
		_ = h.p.Produce(t.Context(), d, hello, "cust_1")
		if got := h.aws.assumes[0].Get("ExternalId"); got != d.Kinesis.ExternalID {
			t.Errorf("ExternalId got = %q, want = %q", got, d.Kinesis.ExternalID)
		}
	})
	t.Run("omits the external id when there is none", func(t *testing.T) {
		// source: spec/lib/lago/kinesis/producer_spec.rb:134
		h := newHarness(t, nil)
		_ = h.p.Produce(t.Context(), destination(), hello, "cust_1")
		if _, ok := h.aws.assumes[0]["ExternalId"]; ok {
			t.Errorf("ExternalId got = %v, want = absent", h.aws.assumes[0])
		}
	})
	t.Run("never shares credentials across external ids or roles", func(t *testing.T) {
		// source: spec/lib/lago/kinesis/producer_spec.rb:140
		// source: spec/lib/lago/kinesis/producer_spec.rb:153
		h := newHarness(t, nil)
		a, b, c := destination(), destination(), destination()
		b.Kinesis.ExternalID = "00000000-0000-4000-8000-000000000002"
		c.Kinesis.RoleARN = "arn:aws:iam::210987654321:role/other-writer"
		for _, d := range []*streaming.Destination{a, b, c, a} {
			_ = h.p.Produce(t.Context(), d, hello, "cust_1")
		}
		if got := len(h.aws.assumes); got != 3 {
			t.Errorf("AssumeRole calls got = %d, want = 3", got)
		}
	})
	t.Run("renews credentials before they expire", func(t *testing.T) {
		clock := time.Date(2098, 12, 31, 23, 0, 0, 0, time.UTC)
		h := newHarness(t, func(c *Config) { c.Now = func() time.Time { return clock } })
		d := destination()
		_ = h.p.Produce(t.Context(), d, hello, "cust_1")
		clock = clock.Add(56 * time.Minute) // within five minutes of 2099-01-01
		_ = h.p.Produce(t.Context(), d, hello, "cust_1")
		if got := len(h.aws.assumes); got != 2 {
			t.Errorf("AssumeRole calls got = %d, want = 2", got)
		}
	})
}

func TestIntermediateRole(t *testing.T) {
	const intermediate = "arn:aws:iam::123456789012:role/StreamingIntermediate"
	t.Run("declared", func(t *testing.T) {
		// source: spec/lib/lago/kinesis/producer_spec.rb:176
		// source: spec/lib/lago/kinesis/producer_spec.rb:190
		// source: spec/lib/lago/kinesis/producer_spec.rb:198
		h := newHarness(t, func(c *Config) { c.IntermediateRoleARN = intermediate })
		d, other, far := destination(), destination(), destination()
		other.Kinesis.RoleARN = "arn:aws:iam::210987654321:role/other-writer"
		far.Kinesis.Region = "us-east-1"
		far.Kinesis.StreamARN = "arn:aws:kinesis:us-east-1:123456789012:stream/far"
		for _, x := range []*streaming.Destination{d, other, far} {
			if err := h.p.Produce(t.Context(), x, hello, "cust_1"); err != nil {
				t.Fatalf("Produce() = %v, want = nil", err)
			}
		}
		want := []string{intermediate, d.Kinesis.RoleARN, other.Kinesis.RoleARN, intermediate, far.Kinesis.RoleARN}
		if got := h.aws.assumedRoles(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("AssumeRole got = %v, want = %v", got, want)
		}
		if got := h.aws.assumes[0].Get("RoleSessionName"); got != IntermediateSessionName {
			t.Errorf("intermediate RoleSessionName got = %s, want = %s", got, IntermediateSessionName)
		}
		// The destination assumes are signed by the intermediate session.
		if got := strings.Join(h.aws.signers, ","); got != "akid,ASIA1,ASIA1,akid,ASIA4" {
			t.Errorf("AssumeRole signers got = %s, want = akid,ASIA1,ASIA1,akid,ASIA4", got)
		}
	})
	t.Run("not declared", func(t *testing.T) {
		// source: spec/lib/lago/kinesis/producer_spec.rb:215
		h := newHarness(t, nil)
		_ = h.p.Produce(t.Context(), destination(), hello, "cust_1")
		if got := strings.Join(h.aws.signers, ","); got != "akid" {
			t.Errorf("AssumeRole signers got = %s, want = akid", got)
		}
	})
}

func TestCredentialsAvailability(t *testing.T) {
	t.Run("starts out available", func(t *testing.T) {
		// source: spec/lib/lago/kinesis/producer_spec.rb:225
		h := newHarness(t, nil)
		if !h.p.CredentialsAvailable(destination()) {
			t.Error("CredentialsAvailable() got = false, want = true")
		}
	})
	denied := func(url.Values) (int, string) {
		return 403, `<ErrorResponse><Error><Code>AccessDenied</Code><Message>no</Message></Error></ErrorResponse>`
	}
	for _, name := range []string{"MissingCredentials", "STS AccessDenied"} {
		// source: spec/lib/lago/kinesis/producer_spec.rb:229-230
		t.Run("marked unreachable after "+name, func(t *testing.T) {
			h := newHarness(t, nil)
			if name == "MissingCredentials" {
				h.env = map[string]string{"AWS_EC2_METADATA_DISABLED": "true"}
			} else {
				h.aws.assume = denied
			}
			d := destination()
			_ = h.p.Produce(t.Context(), d, hello, "cust_1")
			if h.p.CredentialsAvailable(d) {
				t.Error("CredentialsAvailable() got = true, want = false")
			}
			// source: spec/lib/lago/kinesis/producer_spec.rb:248
			other := destination()
			other.Kinesis.RoleARN = "arn:aws:iam::210987654321:role/other-writer"
			if !h.p.CredentialsAvailable(other) {
				t.Error("CredentialsAvailable(other) got = false, want = true")
			}
		})
	}
	t.Run("a throttle leaves it alone", func(t *testing.T) {
		// source: spec/lib/lago/kinesis/producer_spec.rb:239
		h := newHarness(t, nil)
		h.aws.put = func(int) (int, string) { return 400, `{"__type":"ProvisionedThroughputExceededException"}` }
		d := destination()
		_ = h.p.Produce(t.Context(), d, hello, "cust_1")
		if !h.p.CredentialsAvailable(d) {
			t.Error("CredentialsAvailable() got = false, want = true")
		}
	})
}
