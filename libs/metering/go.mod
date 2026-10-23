module github.com/tanvoxyz/tanvo/libs/metering

go 1.27.1

require github.com/tanvoxyz/tanvo/libs/money v0.0.0

require github.com/cockroachdb/apd/v3 v3.2.3 // indirect

replace github.com/tanvoxyz/tanvo/libs/money => ../money
