// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Command tanvo-license generates signing keys, signs license keys and
// inspects them.
//
//	tanvo-license keygen -out <seed file> <key_id>
//	tanvo-license sign -seed-file <path> -key-id <id> -licensee <name> \
//	    -services analytics,mcp -expires 2027-12-31 [-license-id <id>] [-issued <date>]
//	tanvo-license inspect [-public <key_id>:<base64url key>]... [<key>]
//
// keygen writes the private seed (base64url, mode 0600) to the -out file,
// which must not exist, and prints the public key line to embed in
// TrustedKeys. sign prints a license key. inspect decodes a key (the
// argument, or TANVO_LICENSE_KEY when there is none), verifies it against
// the built-in trusted keys or the -public keys, prints it as JSON and exits
// with status 1 when it is not valid.
//
// Dates are YYYY-MM-DD in UTC or RFC 3339 times; a date given to -expires
// includes that whole day.
package main
