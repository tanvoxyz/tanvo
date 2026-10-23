module github.com/tanvoxyz/tanvo/libs/expression/go

go 1.27.1

require (
	github.com/tanvoxyz/tanvo/libs/money v0.0.0
	github.com/tetratelabs/wazero v1.12.0
)

require (
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	golang.org/x/sys v0.44.0 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/money => ../../money
