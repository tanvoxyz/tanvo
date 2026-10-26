// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/ee/license"
)

// usage is printed for a missing or unknown command.
const usage = `usage:
  tanvo-license keygen -out <seed file> <key_id>
  tanvo-license sign -seed-file <path> -key-id <id> -licensee <name> -services <a,b> -expires <date> [-license-id <id>] [-issued <date>]
  tanvo-license inspect [-public <key_id>:<base64url key>]... [<key>]
`

// errUsage reports a command line error whose message has been printed.
var errUsage = errors.New("usage")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, time.Now))
}

// run executes the command line args and returns the exit status.
func run(args []string, stdout, stderr io.Writer, getenv func(string) string, now func() time.Time) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "keygen":
		err = keygen(args[1:], stdout, stderr)
	case "sign":
		err = sign(args[1:], stdout, stderr, now)
	case "inspect":
		err = inspect(args[1:], stdout, stderr, getenv, now)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "tanvo-license: unknown command %q\n%s", args[0], usage)
		return 2
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage), errors.Is(err, flag.ErrHelp):
		return 2
	default:
		fmt.Fprintf(stderr, "tanvo-license %s: %v\n", args[0], err)
		return 1
	}
}

// parse parses flags that may appear before or after the positional
// arguments, and returns the positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			// The flag set has printed the error and its usage.
			return nil, fmt.Errorf("%w: %w", errUsage, err)
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// newFlagSet returns a flag set that reports errors to stderr.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// keygen generates a signing key pair: the seed goes to the -out file and
// the public key line to stdout.
func keygen(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("keygen", stderr)
	out := fs.String("out", "", "file to write the private seed to (must not exist)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *out == "" {
		fmt.Fprintln(stderr, "usage: tanvo-license keygen -out <seed file> <key_id>")
		return errUsage
	}
	keyID := pos[0]
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	line := license.FormatPublicKey(keyID, pub)
	if _, _, err := license.ParsePublicKey(line); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		return fmt.Errorf("create seed directory: %w", err)
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create seed file: %w", err)
	}
	_, werr := fmt.Fprintln(f, base64.RawURLEncoding.EncodeToString(priv.Seed()))
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("write seed file: %w", err)
	}
	_, err = fmt.Fprintln(stdout, line)
	return err
}

// sign prints a license key signed with the seed in -seed-file.
func sign(args []string, stdout, stderr io.Writer, now func() time.Time) error {
	fs := newFlagSet("sign", stderr)
	seedFile := fs.String("seed-file", "", "file holding the private seed written by keygen")
	keyID := fs.String("key-id", "", "ID of the signing key")
	licensee := fs.String("licensee", "", "name of the licensee")
	services := fs.String("services", "", `comma-separated services, or "*" for all`)
	expires := fs.String("expires", "", "last day of the license (YYYY-MM-DD, inclusive) or an RFC 3339 time")
	issued := fs.String("issued", "", "first day of the license (YYYY-MM-DD) or an RFC 3339 time; default: today")
	licenseID := fs.String("license-id", "", "license ID; default: a random UUID")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || *seedFile == "" || *keyID == "" || *licensee == "" || *services == "" || *expires == "" {
		fmt.Fprintln(stderr, "usage: tanvo-license sign -seed-file <path> -key-id <id> -licensee <name> -services <a,b> -expires <date> [-license-id <id>] [-issued <date>]")
		return errUsage
	}
	priv, err := readSeed(*seedFile)
	if err != nil {
		return err
	}
	expiresAt, err := parseTime(*expires, true)
	if err != nil {
		return fmt.Errorf("-expires: %w", err)
	}
	issuedAt := now().UTC().Truncate(24 * time.Hour)
	if *issued != "" {
		if issuedAt, err = parseTime(*issued, false); err != nil {
			return fmt.Errorf("-issued: %w", err)
		}
	}
	id := *licenseID
	if id == "" {
		id = newUUID()
	}
	var list []string
	for s := range strings.SplitSeq(*services, ",") {
		if s = strings.TrimSpace(s); s != "" {
			list = append(list, s)
		}
	}
	key, err := license.Sign(priv, license.License{
		KeyID:     *keyID,
		ID:        id,
		Licensee:  *licensee,
		Services:  list,
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, key)
	return err
}

// publicKeys collects repeated -public flags.
type publicKeys map[string]ed25519.PublicKey

// String implements flag.Value.
func (p publicKeys) String() string { return fmt.Sprint(len(p), " keys") }

// Set implements flag.Value.
func (p publicKeys) Set(s string) error {
	id, pub, err := license.ParsePublicKey(s)
	if err != nil {
		return err
	}
	p[id] = pub
	return nil
}

// inspection is the JSON that inspect prints.
type inspection struct {
	Valid     bool      `json:"valid"`
	Error     string    `json:"error,omitempty"`
	KeyID     string    `json:"key_id,omitempty"`
	LicenseID string    `json:"license_id,omitempty"`
	Licensee  string    `json:"licensee,omitempty"`
	Services  []string  `json:"services,omitempty"`
	IssuedAt  time.Time `json:"issued_at,omitzero"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// inspect decodes and verifies a key and prints what it holds.
func inspect(args []string, stdout, stderr io.Writer, getenv func(string) string, now func() time.Time) error {
	fs := newFlagSet("inspect", stderr)
	keys := publicKeys{}
	fs.Var(keys, "public", "trusted public key <key_id>:<base64url key> (repeatable); default: the built-in keys")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var key string
	switch len(pos) {
	case 0:
		key = getenv(license.EnvKey)
	case 1:
		key = pos[0]
	}
	if len(pos) > 1 || strings.TrimSpace(key) == "" {
		fmt.Fprintf(stderr, "usage: tanvo-license inspect [-public <key_id>:<base64url key>]... [<key>]\n(the key defaults to $%s)\n", license.EnvKey)
		return errUsage
	}
	trusted := map[string]ed25519.PublicKey(keys)
	if len(trusted) == 0 {
		trusted = license.TrustedKeys()
	}
	lic, verr := license.NewVerifier(trusted, now).Verify(key)
	if verr != nil && lic.ID == "" {
		// Show what an untrusted key claims, when it decodes at all.
		lic, _ = license.ParseUnverified(key)
	}
	out := inspection{
		Valid:     verr == nil,
		KeyID:     lic.KeyID,
		LicenseID: lic.ID,
		Licensee:  lic.Licensee,
		Services:  lic.Services,
		IssuedAt:  lic.IssuedAt,
		ExpiresAt: lic.ExpiresAt,
	}
	if verr != nil {
		out.Error = verr.Error()
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	return verr
}

// readSeed reads a private key from a seed file written by keygen.
func readSeed(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read seed file: %w", err)
	}
	seed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("seed file %s: want %d base64url bytes", path, ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// parseTime reads a YYYY-MM-DD date in UTC or an RFC 3339 time. With
// endOfDay, a date means the end of that day.
func parseTime(s string, endOfDay bool) (time.Time, error) {
	if d, err := time.ParseInLocation(time.DateOnly, s, time.UTC); err == nil {
		if endOfDay {
			d = d.AddDate(0, 0, 1)
		}
		return d, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is neither YYYY-MM-DD nor RFC 3339", s)
	}
	return t.UTC(), nil
}

// newUUID returns a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails.
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
