module github.com/tanvoxyz/tanvo/libs/store

go 1.27.1

require (
	ariga.io/atlas v1.3.0
	github.com/google/go-cmp v0.7.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/tanvoxyz/tanvo/libs/core v0.0.0
	github.com/tanvoxyz/tanvo/libs/money v0.0.0
	github.com/tanvoxyz/tanvo/libs/period v0.0.0
)

require (
	github.com/agext/levenshtein v1.2.1 // indirect
	github.com/apparentlymart/go-textseg/v13 v13.0.0 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/bmatcuk/doublestar v1.3.4 // indirect
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	github.com/go-openapi/inflect v0.19.0 // indirect
	github.com/hashicorp/hcl/v2 v2.13.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mitchellh/go-wordwrap v0.0.0-20150314170334-ad45545899c7 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/zclconf/go-cty v1.14.4 // indirect
	github.com/zclconf/go-cty-yaml v1.1.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/core => ../core

replace github.com/tanvoxyz/tanvo/libs/money => ../money

replace github.com/tanvoxyz/tanvo/libs/period => ../period
