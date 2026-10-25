module github.com/tanvoxyz/tanvo/libs/ingest

go 1.27.1

require (
	github.com/ClickHouse/clickhouse-go/v2 v2.49.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/tanvoxyz/tanvo/libs/core v0.0.0
	github.com/tanvoxyz/tanvo/libs/dockertest v0.0.0
	github.com/tanvoxyz/tanvo/libs/eventstore v0.0.0
	github.com/tanvoxyz/tanvo/libs/expression/go v0.0.0
	github.com/tanvoxyz/tanvo/libs/metering v0.0.0
	github.com/tanvoxyz/tanvo/libs/money v0.0.0
	github.com/twmb/franz-go v1.22.2
	github.com/twmb/franz-go/pkg/kadm v1.19.0
)

require (
	github.com/ClickHouse/ch-go v0.74.0 // indirect
	github.com/andybalholm/brotli v1.2.6 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	github.com/go-faster/city v1.0.1 // indirect
	github.com/go-faster/errors v0.7.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/paulmach/orb v0.13.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/tanvoxyz/tanvo/libs/period v0.0.0 // indirect
	github.com/tetratelabs/wazero v1.12.0 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.15.0 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/core => ../core

replace github.com/tanvoxyz/tanvo/libs/money => ../money

replace github.com/tanvoxyz/tanvo/libs/period => ../period

replace github.com/tanvoxyz/tanvo/libs/metering => ../metering

replace github.com/tanvoxyz/tanvo/libs/eventstore => ../eventstore

replace github.com/tanvoxyz/tanvo/libs/expression/go => ../expression/go

replace github.com/tanvoxyz/tanvo/libs/dockertest => ../dockertest
