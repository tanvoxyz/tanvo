module github.com/tanvoxyz/tanvo/libs/core

go 1.27.1

require (
	github.com/google/uuid v1.6.0
	github.com/tanvoxyz/tanvo/libs/money v0.0.0
	github.com/tanvoxyz/tanvo/libs/period v0.0.0
	go.yaml.in/yaml/v3 v3.0.4
)

require github.com/cockroachdb/apd/v3 v3.2.3 // indirect

replace github.com/tanvoxyz/tanvo/libs/money => ../money

replace github.com/tanvoxyz/tanvo/libs/period => ../period
