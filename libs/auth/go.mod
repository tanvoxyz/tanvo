module github.com/tanvoxyz/tanvo/libs/auth

go 1.27.1

require (
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/go-jose/go-jose/v4 v4.1.6
	github.com/tanvoxyz/tanvo/libs/core v0.0.0
	golang.org/x/crypto v0.58.0
	golang.org/x/oauth2 v0.37.0
)

require (
	github.com/cockroachdb/apd/v3 v3.2.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/tanvoxyz/tanvo/libs/money v0.0.0 // indirect
	github.com/tanvoxyz/tanvo/libs/period v0.0.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/tanvoxyz/tanvo/libs/core => ../core

replace github.com/tanvoxyz/tanvo/libs/money => ../money

replace github.com/tanvoxyz/tanvo/libs/period => ../period
