module github.com/tanvoxyz/tanvo/providers/anrok

go 1.27.1

require (
	github.com/tanvoxyz/tanvo/libs/core v0.0.0
	github.com/tanvoxyz/tanvo/libs/money v0.0.0
	github.com/tanvoxyz/tanvo/providers/apiclient v0.0.0
	go.opentelemetry.io/otel/metric v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/tanvoxyz/tanvo/libs/period v0.0.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.71.0 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/core => ../../libs/core

replace github.com/tanvoxyz/tanvo/libs/money => ../../libs/money

replace github.com/tanvoxyz/tanvo/libs/period => ../../libs/period

replace github.com/tanvoxyz/tanvo/providers/apiclient => ../apiclient
