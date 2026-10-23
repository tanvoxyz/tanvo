package config

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/mail"
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
	// ShutdownTimeout bounds the graceful shutdown.
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
	// MetricsEndpoint serves OpenTelemetry metrics in the Prometheus
	// format at /metrics, besides the OTEL_METRICS_EXPORTER reader.
	MetricsEndpoint bool `koanf:"metrics_endpoint"`

	// DatabaseURL is the PostgreSQL connection string. Required.
	DatabaseURL string `koanf:"database_url"`
	// DatabaseMaxConns bounds the pool; 0 keeps pgx's default.
	DatabaseMaxConns int32 `koanf:"database_max_conns"`
	// DatabaseRowLevelSecurity makes transactions set the tenant for the
	// row-level security policies (a role that does not own the tables).
	DatabaseRowLevelSecurity bool `koanf:"database_row_level_security"`
	// MigrateOnStart applies the migrations when serve starts; otherwise
	// serve checks that they are applied and refuses to start if not.
	MigrateOnStart bool `koanf:"migrate_on_start"`

	// JobsSchema is the schema of River's tables.
	JobsSchema string `koanf:"jobs_schema"`
	// WorkerQueues are the queues the worker role works; empty is all.
	WorkerQueues []string `koanf:"worker_queues"`
	// JobsDisablePeriodic keeps the workers out of the periodic schedule.
	JobsDisablePeriodic bool `koanf:"jobs_disable_periodic"`
	// JobsClockConcurrency is the concurrency of the clock queue; 0 is
	// the library default.
	JobsClockConcurrency int `koanf:"jobs_clock_concurrency"`

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

	// AppURL is the web console URL used in email links.
	AppURL string `koanf:"app_url"`
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
	c.KafkaBrokers = trimAll(c.KafkaBrokers)
	c.IngestKafkaOrganizations = trimAll(c.IngestKafkaOrganizations)
	c.IngestPostgresOrganizations = trimAll(c.IngestPostgresOrganizations)
	c.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
	c.LogFormat = strings.ToLower(strings.TrimSpace(c.LogFormat))
	c.IngestMode = strings.ToLower(strings.TrimSpace(c.IngestMode))
	c.KafkaSASLMechanism = strings.ToLower(strings.TrimSpace(c.KafkaSASLMechanism))
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
	if c.DatabaseURL == "" {
		bad("database_url", "required")
	} else if u, err := url.Parse(c.DatabaseURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		bad("database_url", "want a postgres:// URL")
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
	if c.AppURL != "" {
		if u, err := url.Parse(c.AppURL); err != nil || u.Scheme == "" || u.Host == "" {
			bad("app_url", "want an absolute URL")
		}
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
