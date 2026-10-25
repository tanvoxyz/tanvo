package config_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

const dsn = "TANVO_DATABASE_URL=postgres://tanvo@localhost/tanvo"

// blobSecret is a valid TANVO_SECRET_KEY (32 bytes).
const blobSecret = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestLoadDefaults(t *testing.T) {
	cfg, warnings, err := config.Load(config.Source{Environ: []string{dsn, "PATH=/bin"}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings got = %v, want = none", warnings)
	}
	want := config.Defaults()
	want.DatabaseURL = "postgres://tanvo@localhost/tanvo"
	if cfg.HTTPAddr != want.HTTPAddr || cfg.ShutdownTimeout != want.ShutdownTimeout || !cfg.MigrateOnStart || cfg.IngestMode != "postgres" || cfg.EventStoreSchema != "eventstore" {
		t.Errorf("Load() got = %+v, want = defaults %+v", cfg, want)
	}
	if got := cfg.Queues(); !slices.Equal(got, port.QueueValues()) {
		t.Errorf("Queues() got = %v, want = every queue", got)
	}
}

func TestLoadTypes(t *testing.T) {
	cfg, _, err := config.Load(config.Source{Environ: []string{
		dsn,
		"TANVO_SHUTDOWN_TIMEOUT=45s",
		"TANVO_MIGRATE_ON_START=false",
		"TANVO_DATABASE_MAX_CONNS=12",
		"TANVO_WORKER_QUEUES=billing, webhooks",
		"TANVO_KAFKA_BROKERS=a:9092,b:9092",
		"TANVO_LOG_LEVEL=DEBUG",
		"TANVO_INGEST_KAFKA_ORGANIZATIONS=0192f3a4-6b7c-7d8e-9f01-23456789abcd",
		"TANVO_TRUSTED_PROXIES=203.0.113.7, 2001:db8::/32",
		"TANVO_SUBSCRIPTION_TERMINATION_ALERT_DAYS=1",
		"TANVO_DAILY_USAGE_SCHEDULING_JITTER=10m",
		"TANVO_DISABLE_EVENTS_VALIDATION=true",
		"TANVO_JOBS_QUEUE_CONCURRENCY=alerts=20, wallets = 5",
		"TANVO_USAGE_BUCKETS_MAX_ROWS_PER_BUCKET=500",
		"TANVO_USAGE_BUCKETS_IDLE_TTL=720h",
		"TANVO_CACHE_URL=valkeys://cache.internal:6380/2",
		"TANVO_CACHE_CLIENT_SIDE_TTL=30s",
		"TANVO_API_KEY_CACHE_TTL=10m",
		"TANVO_CACHE_SENTINEL_USERNAME=sentinel",
		"TANVO_CACHE_SENTINEL_PASSWORD=watch",
	}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ShutdownTimeout != 45*time.Second {
		t.Errorf("ShutdownTimeout got = %v, want = 45s", cfg.ShutdownTimeout)
	}
	if cfg.MigrateOnStart {
		t.Error("MigrateOnStart got = true, want = false")
	}
	if cfg.DatabaseMaxConns != 12 {
		t.Errorf("DatabaseMaxConns got = %d, want = 12", cfg.DatabaseMaxConns)
	}
	if !slices.Equal(cfg.SubscriptionTerminationAlertDays, []int{1}) {
		t.Errorf("SubscriptionTerminationAlertDays got = %v, want = [1]", cfg.SubscriptionTerminationAlertDays)
	}
	if cfg.DailyUsageSchedulingJitter != 10*time.Minute {
		t.Errorf("DailyUsageSchedulingJitter got = %v, want = 10m", cfg.DailyUsageSchedulingJitter)
	}
	if cfg.UsageBucketsMaxRowsPerBucket != 500 || cfg.UsageBucketsIdleTTL != 720*time.Hour {
		t.Errorf("usage bucket limits got = %d %v, want = 500 720h", cfg.UsageBucketsMaxRowsPerBucket, cfg.UsageBucketsIdleTTL)
	}
	if cfg.CacheURL != "valkeys://cache.internal:6380/2" || cfg.CachePrefix != "tanvo:" || cfg.CacheClientSideTTL != 30*time.Second || cfg.APIKeyCacheTTL != 10*time.Minute {
		t.Errorf("cache got = %q %q %v %v, want = valkeys://cache.internal:6380/2 tanvo: 30s 10m", cfg.CacheURL, cfg.CachePrefix, cfg.CacheClientSideTTL, cfg.APIKeyCacheTTL)
	}
	if cfg.CacheSentinelUsername != "sentinel" || cfg.CacheSentinelPassword != "watch" {
		t.Errorf("cache sentinel auth got = %q %q, want = sentinel watch", cfg.CacheSentinelUsername, cfg.CacheSentinelPassword)
	}
	if !cfg.DisableEventsValidation {
		t.Errorf("DisableEventsValidation got = false, want = true")
	}
	if got, err := cfg.QueueConcurrency(); err != nil || !maps.Equal(got, map[port.Queue]int{port.QueueAlerts: 20, port.QueueWallets: 5}) {
		t.Errorf("QueueConcurrency() got = %v, %v, want = map[alerts:20 wallets:5]", got, err)
	}
	if got := cfg.Queues(); !slices.Equal(got, []port.Queue{port.QueueBilling, port.QueueWebhooks}) {
		t.Errorf("Queues() got = %v, want = [billing webhooks]", got)
	}
	if !slices.Equal(cfg.KafkaBrokers, []string{"a:9092", "b:9092"}) {
		t.Errorf("KafkaBrokers got = %v, want = [a:9092 b:9092]", cfg.KafkaBrokers)
	}
	if l, _ := cfg.SlogLevel(); l.String() != "DEBUG" {
		t.Errorf("SlogLevel() got = %v, want = DEBUG", l)
	}
	if !cfg.KafkaUsed() {
		t.Error("KafkaUsed() got = false, want = true")
	}
	var proxies []string
	for _, p := range cfg.TrustedProxies {
		proxies = append(proxies, p.String())
	}
	if want := []string{"203.0.113.7/32", "2001:db8::/32"}; !slices.Equal(proxies, want) {
		t.Errorf("TrustedProxies got = %v, want = %v", proxies, want)
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tanvo.toml")
	body := "database_url = \"postgres://file/tanvo\"\nhttp_addr = \":9000\"\nworker_queues = [\"default\"]\ntrusted_proxies = [\"10.9.0.0/16\"]\nshutdown_timeout = \"5s\"\nbogus = 1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := config.Load(config.Source{Environ: []string{"TANVO_CONFIG=" + path, "TANVO_HTTP_ADDR=:9100"}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseURL != "postgres://file/tanvo" {
		t.Errorf("DatabaseURL got = %q, want = the file's", cfg.DatabaseURL)
	}
	if cfg.HTTPAddr != ":9100" {
		t.Errorf("HTTPAddr got = %q, want = the environment's :9100", cfg.HTTPAddr)
	}
	if cfg.ShutdownTimeout != 5*time.Second || !slices.Equal(cfg.WorkerQueues, []string{"default"}) {
		t.Errorf("file values got = %v %v, want = 5s [default]", cfg.ShutdownTimeout, cfg.WorkerQueues)
	}
	if len(cfg.TrustedProxies) != 1 || cfg.TrustedProxies[0].String() != "10.9.0.0/16" {
		t.Errorf("TrustedProxies got = %v, want = [10.9.0.0/16]", cfg.TrustedProxies)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "bogus") {
		t.Errorf("warnings got = %v, want = the unknown key bogus", warnings)
	}
}

func TestLoadUnknownVariableWarns(t *testing.T) {
	_, warnings, err := config.Load(config.Source{Environ: []string{dsn, "TANVO_DATABSE_URL=x"}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "TANVO_DATABSE_URL") {
		t.Errorf("warnings got = %v, want = TANVO_DATABSE_URL", warnings)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{"missing database", nil, "TANVO_DATABASE_URL: required"},
		{"bad database scheme", []string{"TANVO_DATABASE_URL=mysql://x"}, "TANVO_DATABASE_URL: want a postgres:// URL"},
		{"bad duration", []string{dsn, "TANVO_SHUTDOWN_TIMEOUT=soon"}, "shutdown_timeout"},
		{"bad bool", []string{dsn, "TANVO_MIGRATE_ON_START=maybe"}, "migrate_on_start"},
		{"bad level", []string{dsn, "TANVO_LOG_LEVEL=loud"}, "TANVO_LOG_LEVEL"},
		{"bad format", []string{dsn, "TANVO_LOG_FORMAT=xml"}, "TANVO_LOG_FORMAT"},
		{"bad addr", []string{dsn, "TANVO_HTTP_ADDR=8080"}, "TANVO_HTTP_ADDR"},
		{"bad queue", []string{dsn, "TANVO_WORKER_QUEUES=billing,nope"}, `unknown queue "nope"`},
		{"bad queue concurrency queue", []string{dsn, "TANVO_JOBS_QUEUE_CONCURRENCY=nope=2"}, "TANVO_JOBS_QUEUE_CONCURRENCY"},
		{"bad queue concurrency workers", []string{dsn, "TANVO_JOBS_QUEUE_CONCURRENCY=alerts=0"}, "TANVO_JOBS_QUEUE_CONCURRENCY"},
		{"repeated queue concurrency", []string{dsn, "TANVO_JOBS_QUEUE_CONCURRENCY=alerts=2,alerts=3"}, "given twice"},
		{"bad mode", []string{dsn, "TANVO_INGEST_MODE=sqs"}, "TANVO_INGEST_MODE"},
		{"bad role", []string{dsn, "TANVO_ROLE=web"}, "TANVO_ROLE"},
		{"bad org", []string{dsn, "TANVO_INGEST_KAFKA_ORGANIZATIONS=acme"}, "not an organization ID"},
		{"org in both modes", []string{dsn, "TANVO_INGEST_KAFKA_ORGANIZATIONS=0192f3a4-6b7c-7d8e-9f01-23456789abcd", "TANVO_INGEST_POSTGRES_ORGANIZATIONS=0192f3a4-6b7c-7d8e-9f01-23456789abcd"}, "is also in"},
		{"public event store", []string{dsn, "TANVO_EVENTSTORE_SCHEMA=public"}, "TANVO_EVENTSTORE_SCHEMA"},
		{"reserved event store schema", []string{dsn, "TANVO_EVENTSTORE_SCHEMA=pg_events"}, "TANVO_EVENTSTORE_SCHEMA"},
		{"quoted schema", []string{dsn, "TANVO_JOBS_SCHEMA=Jobs;drop"}, "TANVO_JOBS_SCHEMA"},
		{"sasl without user", []string{dsn, "TANVO_KAFKA_SASL_MECHANISM=plain"}, "TANVO_KAFKA_SASL_USERNAME"},
		{"smtp without from", []string{dsn, "TANVO_SMTP_HOST=smtp.example.com"}, "TANVO_MAIL_FROM"},
		{"smtp security", []string{dsn, "TANVO_SMTP_HOST=smtp.example.com", "TANVO_MAIL_FROM=a@b.c", "TANVO_SMTP_SECURITY=ssl"}, "TANVO_SMTP_SECURITY"},
		{"google without secret", []string{dsn, "TANVO_APP_URL=https://app.example.com", "TANVO_GOOGLE_AUTH_CLIENT_ID=id"}, "TANVO_GOOGLE_AUTH_CLIENT_ID"},
		{"google without app url", []string{dsn, "TANVO_GOOGLE_AUTH_CLIENT_ID=id", "TANVO_GOOGLE_AUTH_CLIENT_SECRET=s"}, "TANVO_APP_URL"},
		{"bad trusted proxy", []string{dsn, "TANVO_TRUSTED_PROXIES=lb.internal"}, "trusted_proxies"},
		{"clickhouse scheme", []string{dsn, "TANVO_CLICKHOUSE_URL=http://ch"}, "TANVO_CLICKHOUSE_URL"},
		{"secret key not base64", []string{dsn, "TANVO_SECRET_KEY=not base64!"}, "TANVO_SECRET_KEY"},
		{"bad blob backend", []string{dsn, "TANVO_BLOB_BACKEND=azure"}, "TANVO_BLOB_BACKEND"},
		{"bad kinesis endpoint", []string{dsn, "TANVO_STREAMING_KINESIS_ENDPOINT=kinesis.local"}, "TANVO_STREAMING_KINESIS_ENDPOINT"},
		{"bad sts endpoint", []string{dsn, "TANVO_STREAMING_STS_ENDPOINT=ftp://sts"}, "TANVO_STREAMING_STS_ENDPOINT"},
		{"bad intermediate role", []string{dsn, "TANVO_STREAMING_INTERMEDIATE_ROLE_ARN=writer"}, "TANVO_STREAMING_INTERMEDIATE_ROLE_ARN"},
		{"blob without secret key", []string{dsn, "TANVO_BLOB_BACKEND=local", "TANVO_BLOB_LOCAL_ROOT=/var/tanvo", "TANVO_API_URL=https://api.example.com"}, "TANVO_SECRET_KEY: required with TANVO_BLOB_BACKEND"},
		{"blob without api url", []string{dsn, "TANVO_BLOB_BACKEND=local", "TANVO_BLOB_LOCAL_ROOT=/var/tanvo", "TANVO_SECRET_KEY=" + blobSecret}, "TANVO_API_URL: required with TANVO_BLOB_BACKEND"},
		{"local blob without root", []string{dsn, "TANVO_BLOB_BACKEND=local", "TANVO_API_URL=https://api.example.com", "TANVO_SECRET_KEY=" + blobSecret}, "TANVO_BLOB_LOCAL_ROOT"},
		{"s3 blob without endpoint", []string{dsn, "TANVO_BLOB_BACKEND=s3", "TANVO_API_URL=https://api.example.com", "TANVO_SECRET_KEY=" + blobSecret, "TANVO_BLOB_REGION=eu-west-1", "TANVO_BLOB_BUCKET=b", "TANVO_BLOB_ACCESS_KEY_ID=k", "TANVO_BLOB_SECRET_ACCESS_KEY=s"}, "TANVO_BLOB_ENDPOINT"},
		{"s3 blob without region", []string{dsn, "TANVO_BLOB_BACKEND=s3", "TANVO_API_URL=https://api.example.com", "TANVO_SECRET_KEY=" + blobSecret, "TANVO_BLOB_ENDPOINT=https://s3.example.com", "TANVO_BLOB_BUCKET=b", "TANVO_BLOB_ACCESS_KEY_ID=k", "TANVO_BLOB_SECRET_ACCESS_KEY=s"}, "TANVO_BLOB_REGION"},
		{"gcs blob without bucket", []string{dsn, "TANVO_BLOB_BACKEND=gcs", "TANVO_API_URL=https://api.example.com", "TANVO_SECRET_KEY=" + blobSecret, "TANVO_BLOB_ACCESS_KEY_ID=k", "TANVO_BLOB_SECRET_ACCESS_KEY=s"}, "TANVO_BLOB_BUCKET"},
		{"gcs blob without keys", []string{dsn, "TANVO_BLOB_BACKEND=gcs", "TANVO_API_URL=https://api.example.com", "TANVO_SECRET_KEY=" + blobSecret, "TANVO_BLOB_BUCKET=b"}, "TANVO_BLOB_ACCESS_KEY_ID"},
		{"blob endpoint scheme", []string{dsn, "TANVO_BLOB_BACKEND=s3", "TANVO_API_URL=https://api.example.com", "TANVO_SECRET_KEY=" + blobSecret, "TANVO_BLOB_ENDPOINT=ftp://s3", "TANVO_BLOB_REGION=r", "TANVO_BLOB_BUCKET=b", "TANVO_BLOB_ACCESS_KEY_ID=k", "TANVO_BLOB_SECRET_ACCESS_KEY=s"}, "TANVO_BLOB_ENDPOINT: want an http"},
		{"blob url ttl too long", []string{dsn, "TANVO_BLOB_BACKEND=gcs", "TANVO_API_URL=https://api.example.com", "TANVO_SECRET_KEY=" + blobSecret, "TANVO_BLOB_BUCKET=b", "TANVO_BLOB_ACCESS_KEY_ID=k", "TANVO_BLOB_SECRET_ACCESS_KEY=s", "TANVO_BLOB_URL_TTL=200h"}, "TANVO_BLOB_URL_TTL"},
		{"short secret key", []string{dsn, "TANVO_SECRET_KEY=c2hvcnQ="}, "want at least 32"},
		{"negative bucket rows", []string{dsn, "TANVO_USAGE_BUCKETS_MAX_ROWS_PER_BUCKET=-1"}, "TANVO_USAGE_BUCKETS_MAX_ROWS_PER_BUCKET"},
		{"cache scheme", []string{dsn, "TANVO_CACHE_URL=memcached://cache:11211"}, "TANVO_CACHE_URL"},
		{"negative client side ttl", []string{dsn, "TANVO_CACHE_CLIENT_SIDE_TTL=-1s"}, "TANVO_CACHE_CLIENT_SIDE_TTL"},
		{"negative api key cache ttl", []string{dsn, "TANVO_API_KEY_CACHE_TTL=-1s"}, "TANVO_API_KEY_CACHE_TTL"},
		{"short bucket idle time", []string{dsn, "TANVO_USAGE_BUCKETS_IDLE_TTL=1h"}, "TANVO_USAGE_BUCKETS_IDLE_TTL: must be 0 or at least 24h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := config.Load(config.Source{Environ: tt.env})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load() error got = %v, want = containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, _, err := config.Load(config.Source{Environ: []string{"TANVO_LOG_LEVEL=loud", "TANVO_HTTP_ADDR=x"}})
	if err == nil {
		t.Fatal("Load() error = nil, want = an error")
	}
	for _, want := range []string{"TANVO_DATABASE_URL", "TANVO_LOG_LEVEL", "TANVO_HTTP_ADDR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error got = %v, want = containing %s", err, want)
		}
	}
}

func TestValidateFor(t *testing.T) {
	cfg, _, err := config.Load(config.Source{Environ: []string{dsn}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateFor(config.Roles{config.RoleAPI: true, config.RoleWorker: true}); err != nil {
		t.Errorf("ValidateFor(api,worker) error = %v, want = nil", err)
	}
	err = cfg.ValidateFor(config.Roles{config.RoleConsumer: true})
	if err == nil || !strings.Contains(err.Error(), "TANVO_KAFKA_BROKERS") || !strings.Contains(err.Error(), "TANVO_CLICKHOUSE_URL") {
		t.Errorf("ValidateFor(consumer) error got = %v, want = brokers and ClickHouse required", err)
	}
	cfg.IngestMode = "kafka"
	if err := cfg.ValidateFor(config.Roles{config.RoleAPI: true}); err == nil {
		t.Error("ValidateFor(api, kafka mode) error = nil, want = brokers required")
	}
}

func TestParseRoles(t *testing.T) {
	tests := []struct {
		in      []string
		want    string
		wantErr bool
	}{
		{in: []string{"all"}, want: "api,consumer,worker"},
		{in: []string{"api,worker"}, want: "api,worker"},
		{in: []string{"worker", "api"}, want: "api,worker"},
		{in: []string{"consumer"}, want: "consumer"},
		{in: []string{"web"}, wantErr: true},
		{in: []string{""}, wantErr: true},
	}
	for _, tt := range tests {
		got, err := config.ParseRoles(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseRoles(%v) error = %v, wantErr = %v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && got.String() != tt.want {
			t.Errorf("ParseRoles(%v) got = %s, want = %s", tt.in, got, tt.want)
		}
	}
}

func TestResolveRoles(t *testing.T) {
	tests := []struct {
		name    string
		brokers []string
		names   []string
		role    []string
		want    string
	}{
		{name: "all without kafka", role: []string{"all"}, want: "api,worker"},
		{name: "all with kafka", brokers: []string{"k:9092"}, role: []string{"all"}, want: "api,consumer,worker"},
		{name: "explicit consumer", names: []string{"consumer"}, role: []string{"all"}, want: "consumer"},
		{name: "flag over setting", names: []string{"api"}, role: []string{"worker"}, want: "api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.KafkaBrokers, cfg.Role = tt.brokers, tt.role
			got, err := cfg.ResolveRoles(tt.names)
			if err != nil {
				t.Fatalf("ResolveRoles() error = %v", err)
			}
			if got.String() != tt.want {
				t.Errorf("ResolveRoles() got = %s, want = %s", got, tt.want)
			}
		})
	}
}

func TestSecretKeyBytes(t *testing.T) {
	key := strings.Repeat("A", 44) // 33 bytes of zeros, base64
	c, _, err := config.Load(config.Source{Environ: []string{dsn, "TANVO_SECRET_KEY=" + key}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.SecretKeyBytes()
	if err != nil || len(b) != 33 {
		t.Errorf("SecretKeyBytes() got = %d bytes, %v, want = 33 bytes", len(b), err)
	}
	c.SecretKey = ""
	if b, err := c.SecretKeyBytes(); b != nil || err != nil {
		t.Errorf("SecretKeyBytes() unset got = %v, %v, want = nil, nil", b, err)
	}
}

func TestLoadBlobBackends(t *testing.T) {
	base := []string{dsn, "TANVO_API_URL=https://api.example.com", "TANVO_SECRET_KEY=" + blobSecret}
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{"none", []string{dsn}, ""},
		{"local", append(base, "TANVO_BLOB_BACKEND=Local", "TANVO_BLOB_LOCAL_ROOT=/var/tanvo"), "local"},
		{"s3", append(base, "TANVO_BLOB_BACKEND=s3", "TANVO_BLOB_ENDPOINT=http://127.0.0.1:9000", "TANVO_BLOB_REGION=us-east-1", "TANVO_BLOB_BUCKET=b", "TANVO_BLOB_ACCESS_KEY_ID=k", "TANVO_BLOB_SECRET_ACCESS_KEY=s", "TANVO_BLOB_PATH_STYLE=true"), "s3"},
		{"gcs", append(base, "TANVO_BLOB_BACKEND=gcs", "TANVO_BLOB_BUCKET=b", "TANVO_BLOB_ACCESS_KEY_ID=k", "TANVO_BLOB_SECRET_ACCESS_KEY=s"), "gcs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _, err := config.Load(config.Source{Environ: slices.Clone(tt.env)})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.BlobBackend != tt.want {
				t.Errorf("BlobBackend got = %q, want = %q", cfg.BlobBackend, tt.want)
			}
		})
	}
}

func TestLoadDocuments(t *testing.T) {
	cfg, _, err := config.Load(config.Source{Environ: []string{dsn, "TANVO_TYPST_PATH=/usr/bin/typst", "TANVO_DOCUMENTS_FONT_PATHS=/a, /b", "TANVO_DISABLE_PDF_GENERATION=true"}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TypstPath != "/usr/bin/typst" || !slices.Equal(cfg.DocumentsFontPaths, []string{"/a", "/b"}) || !cfg.DisablePDFGeneration {
		t.Errorf("Load() got = %q %q %v, want = /usr/bin/typst [/a /b] true", cfg.TypstPath, cfg.DocumentsFontPaths, cfg.DisablePDFGeneration)
	}
}
