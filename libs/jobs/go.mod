module github.com/tanvoxyz/tanvo/libs/jobs

go 1.27.1

require (
	github.com/jackc/pgx/v5 v5.11.0
	github.com/riverqueue/river v0.49.0
	github.com/riverqueue/river/riverdriver v0.49.0
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.49.0
	github.com/riverqueue/river/rivertype v0.49.0
	github.com/tanvoxyz/tanvo/libs/core v0.0.0
	github.com/tanvoxyz/tanvo/libs/dockertest v0.0.0
	golang.org/x/time v0.16.0
)

require (
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/riverqueue/river/rivershared v0.49.0 // indirect
	github.com/tanvoxyz/tanvo/libs/money v0.0.0 // indirect
	github.com/tanvoxyz/tanvo/libs/period v0.0.0 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/core => ../core

replace github.com/tanvoxyz/tanvo/libs/money => ../money

replace github.com/tanvoxyz/tanvo/libs/period => ../period

replace github.com/tanvoxyz/tanvo/libs/dockertest => ../dockertest
