module github.com/tanvoxyz/tanvo/apps/server

go 1.27.1

require (
	buf.build/go/protovalidate v1.4.0
	connectrpc.com/connect/v2 v2.0.2
	connectrpc.com/vanguard v0.5.0
	github.com/ClickHouse/clickhouse-go/v2 v2.49.0
	github.com/exaring/otelpgx v0.13.0
	github.com/go-viper/mapstructure/v2 v2.5.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/knadh/koanf/parsers/toml/v2 v2.2.2
	github.com/knadh/koanf/providers/env/v2 v2.0.2
	github.com/knadh/koanf/providers/file v1.2.1
	github.com/knadh/koanf/v2 v2.4.1
	github.com/prometheus/client_golang v1.24.1
	github.com/riverqueue/river v0.49.0
	github.com/riverqueue/river/rivertype v0.49.0
	github.com/riverqueue/rivercontrib/otelriver v0.12.0
	github.com/spf13/cobra v1.10.2
	github.com/tanvoxyz/tanvo/libs/auth v0.0.0
	github.com/tanvoxyz/tanvo/libs/billing v0.0.0
	github.com/tanvoxyz/tanvo/libs/core v0.0.0
	github.com/tanvoxyz/tanvo/libs/eventstore v0.0.0
	github.com/tanvoxyz/tanvo/libs/ingest v0.0.0
	github.com/tanvoxyz/tanvo/libs/jobs v0.0.0
	github.com/tanvoxyz/tanvo/libs/mail v0.0.0
	github.com/tanvoxyz/tanvo/libs/metering v0.0.0
	github.com/tanvoxyz/tanvo/libs/money v0.0.0
	github.com/tanvoxyz/tanvo/libs/period v0.0.0
	github.com/tanvoxyz/tanvo/libs/proto v0.0.0
	github.com/tanvoxyz/tanvo/libs/store v0.0.0
	github.com/tanvoxyz/tanvo/libs/tax v0.0.0
	github.com/tanvoxyz/tanvo/libs/webhooks v0.0.0
	github.com/tanvoxyz/tanvo/providers/x402 v0.0.0
	github.com/twmb/franz-go v1.22.2
	github.com/twmb/franz-go/plugin/kotel v1.7.1
	go.opentelemetry.io/contrib/bridges/otelslog v0.21.0
	go.opentelemetry.io/contrib/exporters/autoexport v0.72.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/exporters/prometheus v0.69.0
	go.opentelemetry.io/otel/log v1.47.0
	go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/sdk/log v1.47.0
	go.opentelemetry.io/otel/sdk/metric v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
	golang.org/x/sync v0.24.0
	google.golang.org/genproto v0.0.0-20261019180522-ce908d29299b
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261012220534-4341edf3f941
	google.golang.org/protobuf v1.36.12
)

require (
	ariga.io/atlas v1.3.0 // indirect
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260825204119-511051f7f437.2 // indirect
	cel.dev/cel-go v0.32.0 // indirect
	cel.dev/expr v0.25.3 // indirect
	github.com/ClickHouse/ch-go v0.74.0 // indirect
	github.com/andybalholm/brotli v1.2.6 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-faster/city v1.0.1 // indirect
	github.com/go-faster/errors v0.7.1 // indirect
	github.com/go-jose/go-jose/v4 v4.1.6 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/knadh/koanf/maps v0.1.2 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/paulmach/orb v0.13.0 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.72.0 // indirect
	github.com/prometheus/otlptranslator v1.0.0 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/riverqueue/river/riverdriver v0.49.0 // indirect
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.49.0 // indirect
	github.com/riverqueue/river/rivershared v0.49.0 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/tanvoxyz/tanvo/libs/expression/go v0.0.0 // indirect
	github.com/tanvoxyz/tanvo/libs/pricing v0.0.0 // indirect
	github.com/tetratelabs/wazero v1.12.0 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/twmb/franz-go/pkg/kadm v1.19.0 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.15.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/bridges/prometheus v0.72.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261019180522-ce908d29299b // indirect
	google.golang.org/grpc v1.84.0 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/auth => ../../libs/auth

replace github.com/tanvoxyz/tanvo/libs/core => ../../libs/core

replace github.com/tanvoxyz/tanvo/libs/eventstore => ../../libs/eventstore

replace github.com/tanvoxyz/tanvo/libs/expression/go => ../../libs/expression/go

replace github.com/tanvoxyz/tanvo/libs/ingest => ../../libs/ingest

replace github.com/tanvoxyz/tanvo/libs/jobs => ../../libs/jobs

replace github.com/tanvoxyz/tanvo/libs/mail => ../../libs/mail

replace github.com/tanvoxyz/tanvo/libs/metering => ../../libs/metering

replace github.com/tanvoxyz/tanvo/libs/money => ../../libs/money

replace github.com/tanvoxyz/tanvo/libs/period => ../../libs/period

replace github.com/tanvoxyz/tanvo/libs/pricing => ../../libs/pricing

replace github.com/tanvoxyz/tanvo/libs/proto => ../../libs/proto

replace github.com/tanvoxyz/tanvo/libs/store => ../../libs/store

replace github.com/tanvoxyz/tanvo/libs/tax => ../../libs/tax

replace github.com/tanvoxyz/tanvo/libs/webhooks => ../../libs/webhooks

replace github.com/tanvoxyz/tanvo/libs/billing => ../../libs/billing

replace github.com/tanvoxyz/tanvo/providers/x402 => ../../providers/x402
