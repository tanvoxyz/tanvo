// Package rpc holds the Connect side of the API role: the server
// interceptors (panic recovery, OpenTelemetry, API key authentication
// against the tanvo.common.v1.access option, protovalidate) and the
// service handlers, which map requests to the libraries' services.
package rpc
