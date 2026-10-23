package config_test

import (
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
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tanvo.toml")
	body := "database_url = \"postgres://file/tanvo\"\nhttp_addr = \":9000\"\nworker_queues = [\"default\"]\nshutdown_timeout = \"5s\"\nbogus = 1\n"
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
		{"clickhouse scheme", []string{dsn, "TANVO_CLICKHOUSE_URL=http://ch"}, "TANVO_CLICKHOUSE_URL"},
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
