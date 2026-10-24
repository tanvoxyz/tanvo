module github.com/tanvoxyz/tanvo/providers/salesforce

go 1.27.1

require github.com/tanvoxyz/tanvo/libs/core v0.0.0

require (
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/tanvoxyz/tanvo/libs/money v0.0.0 // indirect
	github.com/tanvoxyz/tanvo/libs/period v0.0.0 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/core => ../../libs/core

replace github.com/tanvoxyz/tanvo/libs/money => ../../libs/money

replace github.com/tanvoxyz/tanvo/libs/period => ../../libs/period
