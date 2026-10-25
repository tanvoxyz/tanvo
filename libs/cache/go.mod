module github.com/tanvoxyz/tanvo/libs/cache

go 1.27.1

require (
	github.com/tanvoxyz/tanvo/libs/core v0.0.0
	github.com/tanvoxyz/tanvo/libs/dockertest v0.0.0
	github.com/valkey-io/valkey-go v1.0.79
	github.com/valkey-io/valkey-go/valkeyotel v1.0.79
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk/metric v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/tanvoxyz/tanvo/libs/money v0.0.0 // indirect
	github.com/tanvoxyz/tanvo/libs/period v0.0.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/core => ../core

replace github.com/tanvoxyz/tanvo/libs/dockertest => ../dockertest

replace github.com/tanvoxyz/tanvo/libs/money => ../money

replace github.com/tanvoxyz/tanvo/libs/period => ../period
