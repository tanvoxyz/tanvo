package kinesis

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
)

// ErrMissingCredentials is returned when no ambient AWS identity is
// configured: no environment credentials, web identity token, container
// credentials endpoint or instance profile.
var ErrMissingCredentials = errors.New("kinesis: missing AWS credentials")

// APIError is an error answered by an AWS service.
type APIError struct {
	// Service is "kinesis" or "sts".
	Service string
	// Code is the AWS error code ("ResourceNotFoundException",
	// "AccessDenied").
	Code string
	// Message is the AWS error message.
	Message string
	// StatusCode is the HTTP status.
	StatusCode int
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s (HTTP %d): %s", e.Service, e.Code, e.StatusCode, e.Message)
}

// NetworkError is a request that got no answer: a refused or reset
// connection, a DNS failure, a timeout.
type NetworkError struct{ Err error }

// Error implements error.
func (e *NetworkError) Error() string { return "kinesis: networking error: " + e.Err.Error() }

// Unwrap returns the transport error.
func (e *NetworkError) Unwrap() error { return e.Err }

// The Kinesis error codes of a record the stream refuses for good.
//
// source: lib/lago/kinesis/producer.rb:26-39
var (
	throttleCodes = []string{"ProvisionedThroughputExceededException"}
	deliveryCodes = []string{
		"ResourceNotFoundException", "AccessDeniedException", "ValidationException", "InvalidArgumentException",
		"ProvisionedThroughputExceededException",
	}
)

// errorName names err in delivery logs.
func errorName(err error) string {
	var api *APIError
	var ne *NetworkError
	switch {
	case errors.As(err, &api):
		return api.Service + "." + api.Code
	case errors.Is(err, ErrMissingCredentials):
		return "MissingCredentials"
	case errors.As(err, &ne):
		return "NetworkingError"
	default:
		return fmt.Sprintf("%T", err)
	}
}

// deliveryError reports whether err is a delivery failure: logged and
// dropped rather than returned.
func deliveryError(err error) bool {
	var api *APIError
	var ne *NetworkError
	switch {
	case errors.As(err, &api):
		return api.Service == "kinesis" && slices.Contains(deliveryCodes, api.Code) ||
			api.Service == "sts" && api.Code == "AccessDenied"
	case errors.Is(err, ErrMissingCredentials):
		return true
	default:
		return errors.As(err, &ne)
	}
}

// credentialsError reports whether err means this process cannot obtain
// credentials for the destination at all.
//
// source: lib/lago/kinesis/producer.rb:43-46
func credentialsError(err error) bool {
	var api *APIError
	if errors.As(err, &api) {
		return api.Service == "sts" && api.Code == "AccessDenied"
	}
	return errors.Is(err, ErrMissingCredentials)
}

// throttled reports whether err is a stream out of capacity.
func throttled(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Service == "kinesis" && slices.Contains(throttleCodes, api.Code)
}

// retryCodes are the throttling and transient error codes the AWS
// standard retry mode retries.
var retryCodes = []string{
	"Throttling", "ThrottlingException", "ThrottledException", "RequestThrottledException", "TooManyRequestsException",
	"ProvisionedThroughputExceededException", "TransactionInProgressException", "RequestLimitExceeded",
	"BandwidthLimitExceeded", "LimitExceededException", "RequestThrottled", "SlowDown", "PriorRequestNotComplete",
	"EC2ThrottledException", "RequestTimeout", "RequestTimeoutException", "InternalError",
	"IDPCommunicationError",
}

// retryable reports whether the AWS standard retry mode retries err.
func retryable(err error) bool {
	var api *APIError
	var ne *NetworkError
	switch {
	case errors.As(err, &api):
		return slices.Contains(retryCodes, api.Code) || api.StatusCode == http.StatusInternalServerError || api.StatusCode == http.StatusBadGateway ||
			api.StatusCode == http.StatusServiceUnavailable || api.StatusCode == http.StatusGatewayTimeout
	default:
		return errors.As(err, &ne)
	}
}
