package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/clientip"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/mail"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// EnvPrefix is the prefix of Tanvo's environment variables.
const EnvPrefix = "TANVO_"

// FileEnv names the environment variable holding the path of an optional
// TOML settings file; the --config flag overrides it.
const FileEnv = EnvPrefix + "CONFIG"

// Role is a process role (architecture §13.1).
type Role string

// Roles.
const (
	// RoleAPI serves the Connect/REST API.
	RoleAPI Role = "api"
	// RoleWorker runs River workers.
	RoleWorker Role = "worker"
	// RoleConsumer runs the Kafka ingest consumers.
	RoleConsumer Role = "consumer"
	// RoleAll is every role.
	RoleAll Role = "all"
)

// Roles is a set of roles.
type Roles map[Role]bool

// ParseRoles parses role names; "all" expands to every role.
func ParseRoles(names []string) (Roles, error) {
	out := Roles{}
	for _, n := range names {
		for _, part := range strings.Split(n, ",") {
			switch r := Role(strings.TrimSpace(part)); r {
			case RoleAll:
				out[RoleAPI], out[RoleWorker], out[RoleConsumer] = true, true, true
			case RoleAPI, RoleWorker, RoleConsumer:
				out[r] = true
			case "":
			default:
				return nil, fmt.Errorf("role %q: want api, worker, consumer or all", part)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no role given")
	}
	return out, nil
}

// ResolveRoles parses the roles to run, names or else c.Role. "all"
// includes the consumer role only when Kafka brokers are configured, so
// that an installation ingesting in PostgreSQL runs `tanvo serve`
// without Kafka; naming the consumer role explicitly requires them.
func (c Config) ResolveRoles(names []string) (Roles, error) {
	if len(names) == 0 {
		names = c.Role
	}
	rs, err := ParseRoles(names)
	if err != nil {
		return nil, err
	}
	explicit := false
	for _, n := range names {
		for _, part := range strings.Split(n, ",") {
			explicit = explicit || Role(strings.TrimSpace(part)) == RoleConsumer
		}
	}
	if !explicit && len(c.KafkaBrokers) == 0 {
		delete(rs, RoleConsumer)
	}
	if len(rs) == 0 {
		return nil, errors.New("no role given")
	}
	return rs, nil
}

// Has reports whether r is in the set.
func (rs Roles) Has(r Role) bool { return rs[r] }

// String returns the roles, sorted and comma-separated ("api,worker").
func (rs Roles) String() string {
	names := make([]string, 0, len(rs))
	for r := range rs {
		names = append(names, string(r))
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

// Config is the server's configuration: TANVO_* environment variables
// over an optional TOML file whose keys are the variable names without
// the prefix, in lower case (TANVO_DATABASE_URL is database_url).
// OpenTelemetry is configured by the standard OTEL_* variables instead.
type Config struct {
	// Role lists the roles `tanvo serve` runs (api, worker, consumer or
	// all, comma-separated); the --role flag overrides it.
	Role []string `koanf:"role"`
	// Environment is deployment.environment.name ("production").
	Environment string `koanf:"environment"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `koanf:"log_level"`
	// LogFormat is json or text.
	LogFormat string `koanf:"log_format"`

	// HTTPAddr is the listen address of the HTTP server: the API on the
	// api role, health and metrics on every role.
	HTTPAddr string `koanf:"http_addr"`
	// HTTPReadHeaderTimeout bounds reading request headers.
	HTTPReadHeaderTimeout time.Duration `koanf:"http_read_header_timeout"`
	// HTTPMaxBodyBytes bounds a request body.
	HTTPMaxBodyBytes int64 `koanf:"http_max_body_bytes"`
	// TrustedProxies are the proxies, addresses or CIDR ranges, trusted
	// to report the client address in X-Forwarded-For and Client-Ip, in
	// addition to the loopback and private ranges
	// ([clientip.DefaultTrusted]).
	TrustedProxies clientip.Proxies `koanf:"trusted_proxies"`
	// ShutdownTimeout bounds the graceful shutdown.
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
	// MetricsEndpoint serves OpenTelemetry metrics in the Prometheus
	// format at /metrics, besides the OTEL_METRICS_EXPORTER reader.
	MetricsEndpoint bool `koanf:"metrics_endpoint"`

	// DatabaseURL is the PostgreSQL connection string. Required.
	DatabaseURL string `koanf:"database_url"`
	// DatabaseMaxConns bounds the pool; 0 keeps pgx's default.
	DatabaseMaxConns int32 `koanf:"database_max_conns"`
	// DatabaseDirectURL, when set, is a direct (session) PostgreSQL
	// connection string for what needs the state of a session: the
	// LISTEN of the catalog changes and of the job queue and the
	// migrations' session advisory locks. It lets
	// DatabaseURL point at a pooler in transaction mode (PgBouncer).
	// Empty, DatabaseURL serves everything.
	DatabaseDirectURL string `koanf:"database_direct_url"`
	// DatabaseRowLevelSecurity makes transactions set the tenant for the
	// row-level security policies (a role that does not own the tables).
	DatabaseRowLevelSecurity bool `koanf:"database_row_level_security"`
	// MigrateOnStart applies the migrations when serve starts; otherwise
	// serve checks that they are applied and refuses to start if not.
	MigrateOnStart bool `koanf:"migrate_on_start"`
	// MigrateLockTimeout bounds how long a migration statement waits for a
	// lock before the migration fails (store.WithLockTimeout), so that it
	// does not queue the writes of the running release behind it; 0 waits
	// indefinitely.
	MigrateLockTimeout time.Duration `koanf:"migrate_lock_timeout"`

	// JobsSchema is the schema of River's tables.
	JobsSchema string `koanf:"jobs_schema"`
	// WorkerQueues are the queues the worker role works; empty is all.
	WorkerQueues []string `koanf:"worker_queues"`
	// JobsDisablePeriodic keeps the workers out of the periodic schedule.
	JobsDisablePeriodic bool `koanf:"jobs_disable_periodic"`
	// DisableEventsValidation leaves the hourly post-validation of events
	// out of the periodic schedule (lago LAGO_DISABLE_EVENTS_VALIDATION,
	// clock.rb:201).
	DisableEventsValidation bool `koanf:"disable_events_validation"`
	// JobsClockConcurrency is the concurrency of the clock queue; 0 is
	// the library default.
	JobsClockConcurrency int `koanf:"jobs_clock_concurrency"`
	// JobsQueueConcurrency overrides the concurrency of queues, as
	// "queue=workers" pairs ("alerts=20,wallets=5"); the other queues keep
	// the library default (lago sets its workers per queue, for instance
	// SIDEKIQ_ALERTS and SIDEKIQ_WALLETS, app/jobs/usage_monitoring/ and
	// app/jobs/customers/refresh_wallet_job.rb:6-11).
	JobsQueueConcurrency []string `koanf:"jobs_queue_concurrency"`

	// SubscriptionTerminationAlertDays are the days before its ending_at
	// a subscription gets subscription.termination_alert (lago
	// LAGO_SUBSCRIPTION_TERMINATION_ALERT_SENT_AT_DAYS, 15 and 45 by
	// default); empty sends none.
	SubscriptionTerminationAlertDays []int `koanf:"subscription_termination_alert_days"`
	// DailyUsageSchedulingJitter is the window the daily usage
	// computations of one clock run are spread over (lago
	// LAGO_DAILY_USAGE_SCHEDULING_JITTER_SECONDS); 0 is 30 minutes.
	DailyUsageSchedulingJitter time.Duration `koanf:"daily_usage_scheduling_jitter"`
	// AnrokDraftTaxesCacheTTL keeps Anrok's taxes of a draft invoice for
	// the same payload (lago LAGO_ANROK_DRAFT_TAXES_CACHE_TTL_SECONDS, a
	// day by default); 0 turns the cache off.
	AnrokDraftTaxesCacheTTL time.Duration `koanf:"anrok_draft_taxes_cache_ttl"`
	// AnalyticsCacheTTL is how long a computed analytics report is reused
	// (lago Analytics::Base.cache_expiration, 4 hours); 0 turns the cache
	// off.
	AnalyticsCacheTTL time.Duration `koanf:"analytics_cache_ttl"`
	// UsageCacheEntries bounds the current-usage cache of each server
	// process, in charge buckets (lago Subscriptions::ChargeCacheService
	// in the Rails cache); 0 turns the cache off.
	UsageCacheEntries int `koanf:"usage_cache_entries"`
	// CacheURL is the Redis or Valkey server every cache of the
	// installation uses, shared by the server processes (redis://,
	// rediss://, valkey://, valkeys:// or unix://; lago
	// LAGO_REDIS_CACHE_URL); empty keeps each cache in its process.
	CacheURL string `koanf:"cache_url"`
	// CachePrefix prefixes every key the installation writes to CacheURL.
	CachePrefix string `koanf:"cache_prefix"`
	// CacheClientSideTTL keeps the values read from CacheURL in the
	// process for at most this long, invalidated by the server when they
	// change (client-side caching, Redis 6 or later and Valkey); 0 reads
	// the server every time.
	CacheClientSideTTL time.Duration `koanf:"cache_client_side_ttl"`
	// CacheSentinelUsername and CacheSentinelPassword authenticate to the
	// sentinels of a CacheURL with master_set, independently of the
	// server's credentials in the URL (lago
	// LAGO_REDIS_CACHE_SENTINEL_USERNAME).
	CacheSentinelUsername string `koanf:"cache_sentinel_username"`
	// CacheSentinelPassword is the sentinels' password (lago
	// LAGO_REDIS_CACHE_SENTINEL_PASSWORD).
	CacheSentinelPassword string `koanf:"cache_sentinel_password"`
	// APIKeyCacheTTL is how long the event ingestion RPCs reuse an API key
	// read from the database (lago config.api_key_cache_ttl, an hour); the
	// cache needs CacheURL, since a key revoked in one process must stop
	// working in every other. 0 turns it off.
	APIKeyCacheTTL time.Duration `koanf:"api_key_cache_ttl"`
	// UsageBucketsMaxRowsPerBucket is the largest number of rows one
	// bucket of one subscription of a usage bucket definition may hold
	// (its combinations of dimension values): past it, the definition is
	// blocked and its reads stay on the events (architecture §7.4); 0
	// blocks none.
	UsageBucketsMaxRowsPerBucket int64 `koanf:"usage_buckets_max_rows_per_bucket"`
	// UsageBucketsIdleTTL is how long a usage bucket definition is kept
	// without a read served from it before the daily sweep drops it
	// (architecture §7.4); 0 drops none for being idle.
	UsageBucketsIdleTTL time.Duration `koanf:"usage_buckets_idle_ttl"`

	// IngestMode is the default ingestion mode, postgres or kafka.
	IngestMode string `koanf:"ingest_mode"`
	// IngestKafkaOrganizations ingest in kafka mode whatever IngestMode.
	IngestKafkaOrganizations []string `koanf:"ingest_kafka_organizations"`
	// IngestPostgresOrganizations ingest in postgres mode whatever
	// IngestMode.
	IngestPostgresOrganizations []string `koanf:"ingest_postgres_organizations"`
	// IngestEnrichConcurrency is the enrichment concurrency of a consumer.
	IngestEnrichConcurrency int `koanf:"ingest_enrich_concurrency"`

	// EventStoreSchema is the PostgreSQL schema of the PostgreSQL event
	// store (enriched events and usage buckets).
	EventStoreSchema string `koanf:"eventstore_schema"`
	// EventStoreHashPartitions is the hash partition count of the
	// PostgreSQL event store, fixed at its creation; 0 is the default.
	EventStoreHashPartitions int `koanf:"eventstore_hash_partitions"`
	// ClickHouseURL is the ClickHouse DSN of the kafka-mode event store.
	ClickHouseURL string `koanf:"clickhouse_url"`

	// KafkaBrokers are the seed brokers.
	KafkaBrokers []string `koanf:"kafka_brokers"`
	// KafkaClientID is the client ID.
	KafkaClientID string `koanf:"kafka_client_id"`
	// KafkaTLS connects with TLS.
	KafkaTLS bool `koanf:"kafka_tls"`
	// KafkaSASLMechanism is plain, scram-sha-256 or scram-sha-512; empty
	// disables SASL.
	KafkaSASLMechanism string `koanf:"kafka_sasl_mechanism"`
	// KafkaSASLUsername and KafkaSASLPassword are the SASL credentials.
	KafkaSASLUsername string `koanf:"kafka_sasl_username"`
	// KafkaSASLPassword is the SASL password.
	KafkaSASLPassword string `koanf:"kafka_sasl_password"`
	// KafkaTopicPrefix is prepended to the topic names.
	KafkaTopicPrefix string `koanf:"kafka_topic_prefix"`
	// KafkaConsumerGroupPrefix is prepended to the consumer group names.
	KafkaConsumerGroupPrefix string `koanf:"kafka_consumer_group_prefix"`
	// KafkaCreateTopics creates missing topics at startup.
	KafkaCreateTopics bool `koanf:"kafka_create_topics"`
	// KafkaTopicPartitions is the partition count of created topics.
	KafkaTopicPartitions int32 `koanf:"kafka_topic_partitions"`
	// KafkaTopicReplicas is the replication factor of created topics; -1
	// is the broker default.
	KafkaTopicReplicas int16 `koanf:"kafka_topic_replicas"`

	// WebhooksAllowPrivate lets webhook deliveries reach private
	// addresses and plain HTTP (self-hosted receivers).
	WebhooksAllowPrivate bool `koanf:"webhooks_allow_private"`
	// WebhooksTimeout bounds one delivery attempt; 0 is the default.
	WebhooksTimeout time.Duration `koanf:"webhooks_timeout"`
	// OIDCAllowPrivate lets OpenID Connect providers (single sign-on
	// discovery, keys, tokens) use plain HTTP and private addresses, for
	// development against a local provider.
	OIDCAllowPrivate bool `koanf:"oidc_allow_private"`

	// StreamingIntermediateRoleARN, when set, is the AWS role the
	// workers assume first, per region, to assume the role of each
	// streaming destination (lago LAGO_STREAMING_INTERMEDIATE_ROLE_ARN).
	// The workers' own identity comes from the standard AWS environment
	// (variables, web identity, container or instance credentials).
	StreamingIntermediateRoleARN string `koanf:"streaming_intermediate_role_arn"`
	// StreamingKinesisEndpoint overrides the Kinesis endpoint of every
	// destination, for Kinesis-compatible services and local testing.
	StreamingKinesisEndpoint string `koanf:"streaming_kinesis_endpoint"`
	// StreamingSTSEndpoint overrides the regional STS endpoint.
	StreamingSTSEndpoint string `koanf:"streaming_sts_endpoint"`

	// AppURL is the web console URL used in email links.
	AppURL string `koanf:"app_url"`
	// APIURL is the public URL of the API (lago LAGO_API_URL); payment
	// providers deliver their webhooks to APIURL +
	// "/webhooks/{provider}/{connection_id}". Unset registers no provider
	// webhook endpoint.
	APIURL string `koanf:"api_url"`
	// GoCardlessClientID and GoCardlessClientSecret identify the
	// installation's GoCardless partner application, which merchants
	// connect their accounts to by OAuth (lago GOCARDLESS_CLIENT_ID and
	// GOCARDLESS_CLIENT_SECRET); unset, GoCardless cannot be connected.
	GoCardlessClientID string `koanf:"gocardless_client_id"`
	// GoCardlessClientSecret is the application's secret.
	GoCardlessClientSecret string `koanf:"gocardless_client_secret"`
	// GoCardlessSandbox connects merchants through GoCardless' sandbox
	// (lago picks it by the Rails environment, gocardless_provider.rb:16-22).
	GoCardlessSandbox bool `koanf:"gocardless_sandbox"`
	// XeroClientID and XeroClientSecret identify the installation's Xero
	// app, which merchants connect their organizations to by OAuth (the
	// app Nango holds for lago); unset, Xero cannot be connected.
	XeroClientID string `koanf:"xero_client_id"`
	// XeroClientSecret is the app's secret.
	XeroClientSecret string `koanf:"xero_client_secret"`
	// HubSpotClientID and HubSpotClientSecret identify the installation's
	// HubSpot app, which merchants connect their portals to by OAuth;
	// unset, HubSpot cannot be connected.
	HubSpotClientID string `koanf:"hubspot_client_id"`
	// HubSpotClientSecret is the app's secret.
	HubSpotClientSecret string `koanf:"hubspot_client_secret"`
	// MailFrom is the instance's sender address.
	MailFrom string `koanf:"mail_from"`
	// SMTPHost is the SMTP server; empty disables sending (emails are
	// logged and dropped).
	SMTPHost string `koanf:"smtp_host"`
	// SMTPPort is the SMTP port.
	SMTPPort int `koanf:"smtp_port"`
	// SMTPUsername and SMTPPassword are the SMTP credentials.
	SMTPUsername string `koanf:"smtp_username"`
	// SMTPPassword is the SMTP password.
	SMTPPassword string `koanf:"smtp_password"`
	// SMTPSecurity is starttls, starttls_optional, tls or none.
	SMTPSecurity string `koanf:"smtp_security"`

	// BlobBackend stores the documents (invoice, credit note and receipt
	// PDFs and e-invoices), logos, quote files and data exports: "local"
	// (BlobLocalRoot), "s3" (any S3-compatible service) or "gcs" (Google
	// Cloud Storage through its S3 interoperability API, with HMAC keys);
	// empty stores nothing, so no document is generated.
	BlobBackend string `koanf:"blob_backend"`
	// BlobLocalRoot is the directory of the local backend.
	BlobLocalRoot string `koanf:"blob_local_root"`
	// BlobEndpoint is the service URL of the s3 backend (scheme and host,
	// for example https://s3.eu-west-1.amazonaws.com); the gcs backend
	// defaults to https://storage.googleapis.com.
	BlobEndpoint string `koanf:"blob_endpoint"`
	// BlobRegion is the signing region; the gcs backend defaults to auto.
	BlobRegion string `koanf:"blob_region"`
	// BlobBucket is the bucket of the s3 and gcs backends.
	BlobBucket string `koanf:"blob_bucket"`
	// BlobAccessKeyID is the access key (an HMAC key for gcs).
	BlobAccessKeyID string `koanf:"blob_access_key_id"`
	// BlobSecretAccessKey is the secret of BlobAccessKeyID.
	BlobSecretAccessKey string `koanf:"blob_secret_access_key"`
	// BlobSessionToken is the optional session token of temporary
	// credentials.
	BlobSessionToken string `koanf:"blob_session_token"`
	// BlobPathStyle addresses the bucket in the path instead of the host
	// (MinIO usually needs it).
	BlobPathStyle bool `koanf:"blob_path_style"`
	// BlobPrefix is an optional key prefix inside the bucket.
	BlobPrefix string `koanf:"blob_prefix"`
	// BlobURLTTL is the lifetime of the short-lived download URLs the
	// permanent file links redirect to; 15 minutes when zero.
	BlobURLTTL time.Duration `koanf:"blob_url_ttl"`
	// TypstPath is the typst executable rendering the PDFs; "typst" in
	// PATH when empty.
	TypstPath string `koanf:"typst_path"`
	// DocumentsFontPaths are font directories for the scripts the bundled
	// font does not cover (Noto CJK for Chinese documents); empty uses the
	// system font directories.
	DocumentsFontPaths []string `koanf:"documents_font_paths"`
	// DisablePDFGeneration renders no PDF and attaches none to emails
	// (lago LAGO_DISABLE_PDF_GENERATION); e-invoice XML is still made.
	DisablePDFGeneration bool `koanf:"disable_pdf_generation"`

	// DisableSignup turns off self sign-up (lago LAGO_DISABLE_SIGNUP).
	DisableSignup bool `koanf:"disable_signup"`
	// GoogleAuthClientID and GoogleAuthClientSecret are the OAuth client
	// of Google sign-in (lago GOOGLE_AUTH_CLIENT_ID and
	// GOOGLE_AUTH_CLIENT_SECRET); unset disables it. Google redirects to
	// AppURL + "/auth/google/callback".
	GoogleAuthClientID string `koanf:"google_auth_client_id"`
	// GoogleAuthClientSecret is the OAuth client secret of Google sign-in.
	GoogleAuthClientSecret string `koanf:"google_auth_client_secret"`
	// SecretKey is the installation's secret, base64 of at least 32
	// bytes (lago SECRET_KEY_BASE). The keys that sign customer portal
	// tokens and encrypt the secrets kept at rest (integration
	// credentials) are derived from it; unset disables the customer portal
	// and the SSO connections. Changing it invalidates the portal links
	// and makes the stored secrets undecryptable.
	SecretKey string `koanf:"secret_key"`
}

// The blob backends of [Config.BlobBackend].
const (
	// BlobLocal stores the files on the local disk.
	BlobLocal = "local"
	// BlobS3 stores the files in an S3-compatible service.
	BlobS3 = "s3"
	// BlobGCS stores the files in Google Cloud Storage.
	BlobGCS = "gcs"
)

// minSecretKeyBytes is the minimum size of the decoded SecretKey.
const minSecretKeyBytes = 32

// SecretKeyBytes returns the decoded SecretKey, nil when unset.
func (c Config) SecretKeyBytes() ([]byte, error) {
	if c.SecretKey == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(c.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("secret_key: want base64: %w", err)
	}
	if len(b) < minSecretKeyBytes {
		return nil, fmt.Errorf("secret_key: got %d bytes, want at least %d", len(b), minSecretKeyBytes)
	}
	return b, nil
}

// Defaults returns the configuration before any source is read.
func Defaults() Config {
	return Config{
		Role:                     []string{string(RoleAll)},
		LogLevel:                 "info",
		LogFormat:                "json",
		HTTPAddr:                 ":8080",
		HTTPReadHeaderTimeout:    10 * time.Second,
		HTTPMaxBodyBytes:         8 << 20,
		ShutdownTimeout:          30 * time.Second,
		MigrateOnStart:           true,
		MigrateLockTimeout:       store.DefaultLockTimeout,
		JobsSchema:               "river",
		IngestMode:               string(ingest.ModePostgres),
		IngestEnrichConcurrency:  4,
		EventStoreSchema:         "eventstore",
		KafkaClientID:            "tanvo",
		KafkaConsumerGroupPrefix: "tanvo",
		KafkaTopicPartitions:     6,
		KafkaTopicReplicas:       -1,
		SMTPPort:                 587,
		SMTPSecurity:             string(mail.SecuritySTARTTLS),

		SubscriptionTerminationAlertDays: []int{15, 45},
		AnrokDraftTaxesCacheTTL:          24 * time.Hour,
		AnalyticsCacheTTL:                4 * time.Hour,
		UsageCacheEntries:                100_000,
		CachePrefix:                      "tanvo:",
		APIKeyCacheTTL:                   time.Hour,
		UsageBucketsMaxRowsPerBucket:     10_000,
		UsageBucketsIdleTTL:              45 * 24 * time.Hour,
	}
}

// Source tells [Load] where to read from.
type Source struct {
	// Environ lists the environment as "KEY=value"; nil is os.Environ.
	Environ []string
	// File is the TOML file; empty reads the file named by TANVO_CONFIG,
	// if any.
	File string
}

// Load reads the configuration from the file, then the environment,
// which wins, and validates it. Unknown TANVO_* variables and file keys
// are returned as warnings, not errors, so that tooling variables
// (TANVO_TEST_*) do not break the server.
func Load(src Source) (Config, []string, error) {
	environ := src.Environ
	if environ == nil {
		environ = os.Environ()
	}
	path := src.File
	if path == "" {
		for _, kv := range environ {
			if v, ok := strings.CutPrefix(kv, FileEnv+"="); ok {
				path = v
			}
		}
	}
	k := koanf.New("\x00") // flat keys: no nesting
	if path != "" {
		if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
			return Config{}, nil, fmt.Errorf("config: read %s: %w", path, err)
		}
	}
	if err := k.Load(env.Provider("\x00", env.Opt{
		Prefix:      EnvPrefix,
		EnvironFunc: func() []string { return environ },
		TransformFunc: func(key, v string) (string, any) {
			if key == FileEnv {
				return "", nil
			}
			return strings.ToLower(strings.TrimPrefix(key, EnvPrefix)), v
		},
	}), nil); err != nil {
		return Config{}, nil, fmt.Errorf("config: read environment: %w", err)
	}
	cfg := Defaults()
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{DecoderConfig: &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
			mapstructure.TextUnmarshallerHookFunc(),
		),
		WeaklyTypedInput: true,
	}}); err != nil {
		return Config{}, nil, fmt.Errorf("config: %w", err)
	}
	known := keys()
	var warnings []string
	for _, key := range slices.Sorted(maps.Keys(k.All())) {
		if !known[key] {
			warnings = append(warnings, fmt.Sprintf("unknown setting %s (%s%s)", key, EnvPrefix, strings.ToUpper(key)))
		}
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, warnings, err
	}
	return cfg, warnings, nil
}

// keys returns the koanf keys of Config.
func keys() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeFor[Config]()
	for i := range t.NumField() {
		if tag := t.Field(i).Tag.Get("koanf"); tag != "" {
			out[tag] = true
		}
	}
	return out
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (c *Config) normalize() {
	c.Role = trimAll(c.Role)
	c.WorkerQueues = trimAll(c.WorkerQueues)
	c.JobsQueueConcurrency = trimAll(c.JobsQueueConcurrency)
	c.KafkaBrokers = trimAll(c.KafkaBrokers)
	c.IngestKafkaOrganizations = trimAll(c.IngestKafkaOrganizations)
	c.IngestPostgresOrganizations = trimAll(c.IngestPostgresOrganizations)
	c.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
	c.LogFormat = strings.ToLower(strings.TrimSpace(c.LogFormat))
	c.IngestMode = strings.ToLower(strings.TrimSpace(c.IngestMode))
	c.KafkaSASLMechanism = strings.ToLower(strings.TrimSpace(c.KafkaSASLMechanism))
	c.BlobBackend = strings.ToLower(strings.TrimSpace(c.BlobBackend))
	c.DocumentsFontPaths = trimAll(c.DocumentsFontPaths)
}

// Validate checks every setting and returns all the problems at once.
// Settings that depend on the roles are checked by [Config.ValidateFor].
func (c Config) Validate() error {
	var errs []error
	bad := func(key, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s%s: %s", EnvPrefix, strings.ToUpper(key), fmt.Sprintf(format, args...)))
	}
	if _, err := ParseRoles(c.Role); err != nil {
		bad("role", "%v", err)
	}
	if _, err := c.SlogLevel(); err != nil {
		bad("log_level", "%q: want debug, info, warn or error", c.LogLevel)
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		bad("log_format", "%q: want json or text", c.LogFormat)
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		bad("http_addr", "%q: %v", c.HTTPAddr, err)
	}
	if c.HTTPReadHeaderTimeout <= 0 {
		bad("http_read_header_timeout", "must be positive")
	}
	if c.HTTPMaxBodyBytes <= 0 {
		bad("http_max_body_bytes", "must be positive")
	}
	if c.ShutdownTimeout <= 0 {
		bad("shutdown_timeout", "must be positive")
	}
	if c.MigrateLockTimeout < 0 {
		bad("migrate_lock_timeout", "must not be negative")
	}
	for _, d := range c.SubscriptionTerminationAlertDays {
		if d < 0 {
			bad("subscription_termination_alert_days", "%d: must not be negative", d)
		}
	}
	if c.DailyUsageSchedulingJitter < 0 {
		bad("daily_usage_scheduling_jitter", "must not be negative")
	}
	if c.AnrokDraftTaxesCacheTTL < 0 {
		bad("anrok_draft_taxes_cache_ttl", "must not be negative")
	}
	if c.AnalyticsCacheTTL < 0 {
		bad("analytics_cache_ttl", "must not be negative")
	}
	if c.UsageCacheEntries < 0 {
		bad("usage_cache_entries", "must not be negative")
	}
	if c.CacheURL != "" {
		if u, err := url.Parse(c.CacheURL); err != nil || !slices.Contains([]string{"redis", "rediss", "valkey", "valkeys", "unix"}, u.Scheme) {
			bad("cache_url", "want a redis://, rediss://, valkey://, valkeys:// or unix:// URL")
		}
	}
	if c.CacheClientSideTTL < 0 {
		bad("cache_client_side_ttl", "must not be negative")
	}
	if c.APIKeyCacheTTL < 0 {
		bad("api_key_cache_ttl", "must not be negative")
	}
	if c.UsageBucketsMaxRowsPerBucket < 0 {
		bad("usage_buckets_max_rows_per_bucket", "must not be negative")
	}
	if c.UsageBucketsIdleTTL < 0 {
		bad("usage_buckets_idle_ttl", "must not be negative")
	} else if c.UsageBucketsIdleTTL > 0 && c.UsageBucketsIdleTTL < 24*time.Hour {
		// Shorter than a day, the daily sweep would drop the definitions
		// of the reads of a daily job between two of them.
		bad("usage_buckets_idle_ttl", "must be 0 or at least 24h")
	}
	if c.DatabaseURL == "" {
		bad("database_url", "required")
	} else if u, err := url.Parse(c.DatabaseURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		bad("database_url", "want a postgres:// URL")
	}
	if c.DatabaseDirectURL != "" {
		if u, err := url.Parse(c.DatabaseDirectURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			bad("database_direct_url", "want a postgres:// URL")
		}
	}
	if c.DatabaseMaxConns < 0 {
		bad("database_max_conns", "must not be negative")
	}
	if !identifier(c.JobsSchema) {
		bad("jobs_schema", "%q: want a lower-case SQL identifier", c.JobsSchema)
	}
	if postgres.ValidateSchema(c.EventStoreSchema) != nil || c.EventStoreSchema == "public" {
		bad("eventstore_schema", "%q: want a lower-case SQL identifier other than public, not starting with pg_", c.EventStoreSchema)
	}
	if c.EventStoreSchema == c.JobsSchema {
		bad("eventstore_schema", "must differ from %sJOBS_SCHEMA", EnvPrefix)
	}
	if c.EventStoreHashPartitions < 0 {
		bad("eventstore_hash_partitions", "must not be negative")
	}
	for _, q := range c.WorkerQueues {
		if !port.Queue(q).Valid() {
			bad("worker_queues", "unknown queue %q", q)
		}
	}
	if c.JobsClockConcurrency < 0 {
		bad("jobs_clock_concurrency", "must not be negative")
	}
	if _, err := c.QueueConcurrency(); err != nil {
		bad("jobs_queue_concurrency", "%v", err)
	}
	if m := ingest.Mode(c.IngestMode); m != ingest.ModePostgres && m != ingest.ModeKafka {
		bad("ingest_mode", "%q: want postgres or kafka", c.IngestMode)
	}
	seen := map[string]string{}
	for key, ids := range map[string][]string{"ingest_kafka_organizations": c.IngestKafkaOrganizations, "ingest_postgres_organizations": c.IngestPostgresOrganizations} {
		for _, id := range ids {
			if _, err := core.ParseID[core.OrganizationID](id); err != nil {
				bad(key, "%q is not an organization ID", id)
			}
			if other, ok := seen[id]; ok && other != key {
				bad(key, "%s is also in %s%s", id, EnvPrefix, strings.ToUpper(other))
			}
			seen[id] = key
		}
	}
	if c.IngestEnrichConcurrency < 1 {
		bad("ingest_enrich_concurrency", "must be positive")
	}
	if c.ClickHouseURL != "" {
		if u, err := url.Parse(c.ClickHouseURL); err != nil || (u.Scheme != "clickhouse" && u.Scheme != "clickhouses" && u.Scheme != "tcp") {
			bad("clickhouse_url", "want a clickhouse:// URL")
		}
	}
	switch c.KafkaSASLMechanism {
	case "":
	case "plain", "scram-sha-256", "scram-sha-512":
		if c.KafkaSASLUsername == "" || c.KafkaSASLPassword == "" {
			bad("kafka_sasl_mechanism", "needs %sKAFKA_SASL_USERNAME and %sKAFKA_SASL_PASSWORD", EnvPrefix, EnvPrefix)
		}
	default:
		bad("kafka_sasl_mechanism", "%q: want plain, scram-sha-256 or scram-sha-512", c.KafkaSASLMechanism)
	}
	if c.KafkaTopicPartitions < 1 {
		bad("kafka_topic_partitions", "must be positive")
	}
	if c.KafkaTopicReplicas == 0 || c.KafkaTopicReplicas < -1 {
		bad("kafka_topic_replicas", "must be positive, or -1 for the broker default")
	}
	if c.WebhooksTimeout < 0 {
		bad("webhooks_timeout", "must not be negative")
	}
	if c.APIURL != "" {
		if u, err := url.Parse(c.APIURL); err != nil || u.Scheme == "" || u.Host == "" {
			bad("api_url", "want an absolute URL")
		}
	}
	if c.AppURL != "" {
		if u, err := url.Parse(c.AppURL); err != nil || u.Scheme == "" || u.Host == "" {
			bad("app_url", "want an absolute URL")
		}
	}
	for _, e := range []struct{ key, v string }{
		{"streaming_kinesis_endpoint", c.StreamingKinesisEndpoint}, {"streaming_sts_endpoint", c.StreamingSTSEndpoint},
	} {
		if e.v == "" {
			continue
		}
		if u, err := url.Parse(e.v); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			bad(e.key, "want an absolute http(s) URL")
		}
	}
	if v := c.StreamingIntermediateRoleARN; v != "" && !strings.HasPrefix(v, "arn:") {
		bad("streaming_intermediate_role_arn", "want an IAM role ARN")
	}
	if (c.GoCardlessClientID == "") != (c.GoCardlessClientSecret == "") {
		bad("gocardless_client_id", "set with %sGOCARDLESS_CLIENT_SECRET, or neither", EnvPrefix)
	}
	if (c.XeroClientID == "") != (c.XeroClientSecret == "") {
		bad("xero_client_id", "set with %sXERO_CLIENT_SECRET, or neither", EnvPrefix)
	}
	if (c.HubSpotClientID == "") != (c.HubSpotClientSecret == "") {
		bad("hubspot_client_id", "set with %sHUBSPOT_CLIENT_SECRET, or neither", EnvPrefix)
	}
	if (c.GoogleAuthClientID == "") != (c.GoogleAuthClientSecret == "") {
		bad("google_auth_client_id", "set with %sGOOGLE_AUTH_CLIENT_SECRET, or neither", EnvPrefix)
	}
	if c.GoogleAuthClientID != "" && c.AppURL == "" {
		bad("app_url", "required with %sGOOGLE_AUTH_CLIENT_ID", EnvPrefix)
	}
	if _, err := c.SecretKeyBytes(); err != nil {
		bad("secret_key", "%v", err)
	}
	if c.BlobBackend != "" {
		// The permanent file links are signed and served under the API.
		if c.SecretKey == "" {
			bad("secret_key", "required with %sBLOB_BACKEND, which signs the file links", EnvPrefix)
		}
		if c.APIURL == "" {
			bad("api_url", "required with %sBLOB_BACKEND, which serves the file links", EnvPrefix)
		}
	}
	switch c.BlobBackend {
	case "":
	case BlobLocal:
		if c.BlobLocalRoot == "" {
			bad("blob_local_root", "required with %sBLOB_BACKEND=local", EnvPrefix)
		}
	case BlobS3, BlobGCS:
		if c.BlobBackend == BlobS3 {
			if c.BlobEndpoint == "" {
				bad("blob_endpoint", "required with %sBLOB_BACKEND=s3", EnvPrefix)
			}
			if c.BlobRegion == "" {
				bad("blob_region", "required with %sBLOB_BACKEND=s3", EnvPrefix)
			}
		}
		if c.BlobBucket == "" {
			bad("blob_bucket", "required with %sBLOB_BACKEND=%s", EnvPrefix, c.BlobBackend)
		}
		if c.BlobAccessKeyID == "" || c.BlobSecretAccessKey == "" {
			bad("blob_access_key_id", "required with %sBLOB_SECRET_ACCESS_KEY by %sBLOB_BACKEND=%s", EnvPrefix, EnvPrefix, c.BlobBackend)
		}
		if c.BlobEndpoint != "" {
			if u, err := url.Parse(c.BlobEndpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				bad("blob_endpoint", "want an http or https URL")
			}
		}
		if c.BlobURLTTL > 7*24*time.Hour {
			bad("blob_url_ttl", "%v: want at most 7 days", c.BlobURLTTL)
		}
	default:
		bad("blob_backend", "%q: want local, s3 or gcs", c.BlobBackend)
	}
	if c.BlobURLTTL < 0 {
		bad("blob_url_ttl", "must not be negative")
	}
	if c.SMTPHost != "" {
		if c.MailFrom == "" {
			bad("mail_from", "required with %sSMTP_HOST", EnvPrefix)
		}
		if c.SMTPPort < 1 || c.SMTPPort > 65535 {
			bad("smtp_port", "%d: want 1 to 65535", c.SMTPPort)
		}
		switch mail.Security(c.SMTPSecurity) {
		case mail.SecuritySTARTTLS, mail.SecuritySTARTTLSOptional, mail.SecurityTLS, mail.SecurityNone:
		default:
			bad("smtp_security", "%q: want starttls, starttls_optional, tls or none", c.SMTPSecurity)
		}
	}
	return errors.Join(errs...)
}

// ValidateFor checks the settings the roles need: Kafka brokers and
// ClickHouse when an organization ingests in kafka mode or the consumer
// role runs.
func (c Config) ValidateFor(roles Roles) error {
	var errs []error
	if c.KafkaUsed() || roles.Has(RoleConsumer) {
		if len(c.KafkaBrokers) == 0 {
			errs = append(errs, fmt.Errorf("%sKAFKA_BROKERS: required by kafka ingestion and the consumer role", EnvPrefix))
		}
		if c.ClickHouseURL == "" {
			errs = append(errs, fmt.Errorf("%sCLICKHOUSE_URL: required by kafka ingestion and the consumer role", EnvPrefix))
		}
	}
	return errors.Join(errs...)
}

// KafkaUsed reports whether some organization ingests in kafka mode.
func (c Config) KafkaUsed() bool {
	return ingest.Mode(c.IngestMode) == ingest.ModeKafka || len(c.IngestKafkaOrganizations) > 0
}

// SlogLevel returns LogLevel as a slog level.
func (c Config) SlogLevel() (slog.Level, error) {
	var l slog.Level
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
		err := l.UnmarshalText([]byte(c.LogLevel))
		return l, err
	}
	return l, fmt.Errorf("log level %q", c.LogLevel)
}

// Queues returns the queues the worker role works: WorkerQueues, or
// every queue.
func (c Config) Queues() []port.Queue {
	if len(c.WorkerQueues) == 0 {
		return port.QueueValues()
	}
	out := make([]port.Queue, len(c.WorkerQueues))
	for i, q := range c.WorkerQueues {
		out[i] = port.Queue(q)
	}
	return out
}

// QueueConcurrency returns the concurrency overrides of
// JobsQueueConcurrency by queue, or an error naming the first malformed
// pair: an unknown or repeated queue, or workers not in [1, 10000].
func (c Config) QueueConcurrency() (map[port.Queue]int, error) {
	out := map[port.Queue]int{}
	for _, kv := range c.JobsQueueConcurrency {
		name, n, ok := strings.Cut(kv, "=")
		q := port.Queue(strings.TrimSpace(name))
		if !ok || !q.Valid() {
			return nil, fmt.Errorf("%q: want queue=workers with a known queue", kv)
		}
		if _, dup := out[q]; dup {
			return nil, fmt.Errorf("%q: queue %s given twice", kv, q)
		}
		w, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil || w < 1 || w > 10_000 {
			return nil, fmt.Errorf("%q: workers must be an integer in [1, 10000]", kv)
		}
		out[q] = w
	}
	return out, nil
}

// identifier reports whether s is a plain lower-case SQL identifier.
func identifier(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
