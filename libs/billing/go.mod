module github.com/tanvoxyz/tanvo/libs/billing

go 1.27.1

require (
	github.com/google/go-cmp v0.7.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/tanvoxyz/tanvo/libs/core v0.0.0
	github.com/tanvoxyz/tanvo/libs/expression/go v0.0.0
	github.com/tanvoxyz/tanvo/libs/metering v0.0.0
	github.com/tanvoxyz/tanvo/libs/money v0.0.0
	github.com/tanvoxyz/tanvo/libs/period v0.0.0
	github.com/tanvoxyz/tanvo/libs/pricing v0.0.0
	github.com/tanvoxyz/tanvo/libs/store v0.0.0
	github.com/tanvoxyz/tanvo/libs/tax v0.0.0
)

require (
	ariga.io/atlas v1.3.0 // indirect
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/tetratelabs/wazero v1.12.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/core => ../core

replace github.com/tanvoxyz/tanvo/libs/money => ../money

replace github.com/tanvoxyz/tanvo/libs/period => ../period

replace github.com/tanvoxyz/tanvo/libs/pricing => ../pricing

replace github.com/tanvoxyz/tanvo/libs/metering => ../metering

replace github.com/tanvoxyz/tanvo/libs/tax => ../tax

replace github.com/tanvoxyz/tanvo/libs/store => ../store

replace github.com/tanvoxyz/tanvo/libs/expression/go => ../expression/go
