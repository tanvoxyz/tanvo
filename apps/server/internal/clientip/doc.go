// Package clientip resolves the address of the client behind a request
// the way Rails' request.remote_ip does (ActionDispatch::RemoteIp): the
// X-Forwarded-For and Client-Ip headers are read through a chain of
// trusted proxies, by default the loopback and private ranges.
package clientip
