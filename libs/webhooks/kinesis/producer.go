package kinesis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/streaming"
)

// The STS role session names.
//
// source: lib/lago/kinesis/producer.rb:12-13
const (
	// RoleSessionName names the sessions of destination roles.
	RoleSessionName = "tanvo-event-destinations"
	// IntermediateSessionName names the sessions of the intermediate role.
	IntermediateSessionName = "tanvo-streaming-intermediate"
)

// Config configures a [Producer].
type Config struct {
	// HTTPClient sends the AWS requests; nil is [NewHTTPClient].
	HTTPClient *http.Client
	// Credentials is the ambient AWS identity that assumes the roles; nil
	// is the default chain over Environment.
	Credentials CredentialsProvider
	// Environment configures the default chain.
	Environment Environment
	// IntermediateRoleARN, when set, is assumed first, once per region,
	// and its credentials sign the AssumeRole of each destination role:
	// for a worker whose own identity is not the principal destinations
	// trust.
	IntermediateRoleARN string
	// KinesisEndpoint overrides the Kinesis endpoint
	// (https://<account>.data-kinesis.<region>.amazonaws.com), for
	// tests and Kinesis-compatible services.
	KinesisEndpoint string
	// STSEndpoint overrides the regional STS endpoint
	// (https://sts.<region>.amazonaws.com).
	STSEndpoint string
	// Logger receives the delivery records; nil is slog.Default.
	Logger *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Sleep waits between attempts; nil waits on a timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

// credentialsKey identifies the assumed credentials of a destination: two
// destinations share them only when role, region and external ID match.
//
// source: lib/lago/kinesis/producer.rb:60-62
type credentialsKey struct{ role, region, externalID string }

// Producer writes records to Kinesis destinations. It caches, for the
// life of the process, the assumed credentials of each role, region and
// external ID, the intermediate credentials of each region, and which
// destinations this process failed to obtain credentials for. It is safe
// for concurrent use.
//
// source: lib/lago/kinesis/producer.rb
type Producer struct {
	c            caller
	ambient      CredentialsProvider
	intermediate string
	kinesisURL   string
	stsURL       string
	log          streaming.Logger

	mu            sync.Mutex
	assumed       map[credentialsKey]*cachedCredentials
	intermediates map[string]*cachedCredentials
	unavailable   map[credentialsKey]bool
}

var _ port.StreamProducer = (*Producer)(nil)

// New returns a producer.
func New(cfg Config) *Producer {
	c := caller{hc: cfg.HTTPClient, now: cfg.Now, sleep: cfg.Sleep}
	if c.hc == nil {
		c.hc = NewHTTPClient()
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = sleep
	}
	amb := cfg.Credentials
	if amb == nil {
		amb = &cachedCredentials{fetch: ambient{env: cfg.Environment, c: c, stsURL: strings.TrimSuffix(cfg.STSEndpoint, "/")}.Retrieve, now: c.now}
	}
	return &Producer{
		c: c, ambient: amb, intermediate: cfg.IntermediateRoleARN,
		kinesisURL: strings.TrimSuffix(cfg.KinesisEndpoint, "/"), stsURL: strings.TrimSuffix(cfg.STSEndpoint, "/"),
		log:           streaming.NewLogger(cfg.Logger),
		assumed:       map[credentialsKey]*cachedCredentials{},
		intermediates: map[string]*cachedCredentials{},
		unavailable:   map[credentialsKey]bool{},
	}
}

func keyOf(d *streaming.Destination) credentialsKey {
	return credentialsKey{d.Kinesis.RoleARN, d.Kinesis.Region, d.Kinesis.ExternalID}
}

// CredentialsAvailable reports whether this process has not already
// failed to obtain credentials for d.
//
// source: lib/lago/kinesis/producer.rb:56-58
func (p *Producer) CredentialsAvailable(d *streaming.Destination) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.unavailable[keyOf(d)]
}

// Produce puts data on the stream of d under partitionKey. A delivery
// failure is logged as throttled or dropped and returns nil; a missing
// identity or a denied role also marks the destination's credentials
// unavailable to this process. Other failures are returned.
//
// source: lib/lago/kinesis/producer.rb:81-115
func (p *Producer) Produce(ctx context.Context, d *streaming.Destination, data []byte, partitionKey string) error {
	start := p.c.now()
	shard, seq, err := p.putRecord(ctx, d, data, partitionKey)
	ms := p.c.now().Sub(start).Milliseconds()
	if err == nil {
		p.log.Emit(ctx, streaming.OutcomeDelivered, d,
			slog.String("stream", d.Kinesis.StreamARN), slog.String("partition_key", partitionKey),
			slog.Int("bytes", len(data)), slog.String("shard", shard), slog.String("sequence", seq),
			slog.Int64("duration_ms", ms))
		return nil
	}
	if !deliveryError(err) || ctx.Err() != nil {
		return err
	}
	if credentialsError(err) {
		p.mu.Lock()
		p.unavailable[keyOf(d)] = true
		p.mu.Unlock()
	}
	outcome := streaming.OutcomeDropped
	if throttled(err) {
		outcome = streaming.OutcomeThrottled
	}
	p.log.Emit(ctx, outcome, d,
		slog.String("stream", d.Kinesis.StreamARN), slog.String("partition_key", partitionKey),
		slog.Int("bytes", len(data)), slog.Int64("duration_ms", ms),
		slog.String("error", errorName(err)), slog.String("message", errorMessage(err)))
	return nil
}

func errorMessage(err error) string {
	var api *APIError
	if errors.As(err, &api) {
		return api.Message
	}
	return err.Error()
}

// streamARN matches a Kinesis stream ARN.
var streamARN = regexp.MustCompile(`^arn:aws(?:-cn|-us-gov)?:kinesis:[a-z0-9-]+:(\d{12}):stream/[A-Za-z0-9_.-]{1,128}$`)

// regionName matches an AWS region name.
var regionName = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-\d+$`)

// kinesisEndpoint returns the endpoint of the data plane of d's stream.
func (p *Producer) kinesisEndpoint(d *streaming.Destination) (string, error) {
	m := streamARN.FindStringSubmatch(d.Kinesis.StreamARN)
	if m == nil {
		return "", fmt.Errorf("kinesis: invalid stream ARN %q", d.Kinesis.StreamARN)
	}
	if !regionName.MatchString(d.Kinesis.Region) {
		return "", fmt.Errorf("kinesis: invalid region %q", d.Kinesis.Region)
	}
	if p.kinesisURL != "" {
		return p.kinesisURL, nil
	}
	return "https://" + m[1] + ".data-kinesis." + d.Kinesis.Region + "." + dnsSuffix(d.Kinesis.Region), nil
}

func (p *Producer) stsEndpoint(region string) string {
	if p.stsURL != "" {
		return p.stsURL
	}
	return stsEndpoint(region)
}

// putRecord sends PutRecord and returns the shard and sequence number of
// the record.
func (p *Producer) putRecord(ctx context.Context, d *streaming.Destination, data []byte, partitionKey string) (string, string, error) {
	endpoint, err := p.kinesisEndpoint(d)
	if err != nil {
		return "", "", err
	}
	creds, err := p.destinationCredentials(d).Retrieve(ctx)
	if err != nil {
		return "", "", err
	}
	body, err := json.Marshal(map[string]string{
		"StreamARN": d.Kinesis.StreamARN, "Data": base64.StdEncoding.EncodeToString(data), "PartitionKey": partitionKey,
	})
	if err != nil {
		return "", "", fmt.Errorf("kinesis: encode PutRecord: %w", err)
	}
	var out struct {
		ShardID        string `json:"ShardId"`
		SequenceNumber string `json:"SequenceNumber"`
	}
	err = p.c.call(ctx, func(now time.Time) (*http.Request, error) {
		req, err := newRequest(endpoint+"/", "application/x-amz-json-1.1", body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Amz-Target", "Kinesis_20131202.PutRecord")
		signer{creds: creds, region: d.Kinesis.Region, service: "kinesis"}.sign(req, body, now)
		return req, nil
	}, func(an answer) error {
		if an.status != http.StatusOK {
			return kinesisError(an)
		}
		if err := json.Unmarshal(an.body, &out); err != nil {
			return fmt.Errorf("kinesis: decode PutRecord answer: %w", err)
		}
		return nil
	})
	return out.ShardID, out.SequenceNumber, err
}

// kinesisError decodes a JSON 1.1 error answer.
func kinesisError(an answer) error {
	var doc struct {
		Type     string `json:"__type"`
		Message  string `json:"message"`
		Message2 string `json:"Message"`
	}
	_ = json.Unmarshal(an.body, &doc)
	code := doc.Type
	if code == "" {
		code = an.header.Get("X-Amzn-Errortype")
	}
	if i := strings.LastIndexByte(code, '#'); i >= 0 {
		code = code[i+1:]
	}
	if i := strings.IndexByte(code, ':'); i >= 0 {
		code = code[:i]
	}
	if code == "" {
		code = http.StatusText(an.status)
	}
	msg := doc.Message
	if msg == "" {
		msg = doc.Message2
	}
	if msg == "" && doc.Type == "" {
		msg = snippet(an.body)
	}
	return &APIError{Service: "kinesis", Code: code, Message: msg, StatusCode: an.status}
}

// destinationCredentials returns the cached assumed credentials of d's
// role, region and external ID.
//
// source: lib/lago/kinesis/producer.rb:151-180
func (p *Producer) destinationCredentials(d *streaming.Destination) *cachedCredentials {
	k := keyOf(d)
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.assumed[k]; ok {
		return c
	}
	base := p.ambient
	if p.intermediate != "" {
		base = p.intermediateCredentials(k.region)
	}
	c := &cachedCredentials{now: p.c.now, fetch: func(ctx context.Context) (Credentials, error) {
		b, err := base.Retrieve(ctx)
		if err != nil {
			return Credentials{}, err
		}
		return assumeRole(ctx, p.c, p.stsEndpoint(k.region), k.region, b, k.role, RoleSessionName, k.externalID)
	}}
	p.assumed[k] = c
	return c
}

// intermediateCredentials returns the cached credentials of the
// intermediate role in region; p.mu is held.
//
// source: lib/lago/kinesis/producer.rb:64-74
func (p *Producer) intermediateCredentials(region string) *cachedCredentials {
	if c, ok := p.intermediates[region]; ok {
		return c
	}
	c := &cachedCredentials{now: p.c.now, fetch: func(ctx context.Context) (Credentials, error) {
		b, err := p.ambient.Retrieve(ctx)
		if err != nil {
			return Credentials{}, err
		}
		return assumeRole(ctx, p.c, p.stsEndpoint(region), region, b, p.intermediate, IntermediateSessionName, "")
	}}
	p.intermediates[region] = c
	return c
}
