// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Package pgtest gives the integration tests of the analytics service a
// throw-away PostgreSQL 18 server: the server named by TANVO_TEST_POSTGRES_DSN
// (a superuser DSN), or else a postgres:18 container started through the
// docker CLI and removed by [Server.Close].
//
// A test package starts one server from TestMain, builds a template
// database once (typically with [ApplyCoreMigrations]) and clones a
// database from it for each test with [Server.CreateDatabase].
package pgtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DSNEnv names the environment variable holding the superuser DSN of an
// existing server to use instead of a container.
const DSNEnv = "TANVO_TEST_POSTGRES_DSN"

// Image is the container image started when [DSNEnv] is unset; the same
// as the core store's tests.
const Image = "postgres:18"

// ErrUnavailable reports that no server can be started: [DSNEnv] is unset
// and the docker CLI is missing or its engine unreachable.
var ErrUnavailable = errors.New("pgtest: no PostgreSQL server available")

// Server is a PostgreSQL server for tests.
type Server struct {
	adminDSN  string
	container string
	databases []string
}

// Start returns the server of [DSNEnv], or starts a container.
func Start(ctx context.Context) (*Server, error) {
	s := &Server{}
	if dsn := os.Getenv(DSNEnv); dsn != "" {
		s.adminDSN = dsn
	} else {
		if err := s.startContainer(ctx); err != nil {
			return nil, err
		}
	}
	if err := s.waitReady(ctx); err != nil {
		_ = s.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return s, nil
}

// startContainer starts a postgres container with durability turned off
// and its data on a tmpfs.
func (s *Server) startContainer(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("%w: docker is not on PATH", ErrUnavailable)
	}
	if out, err := docker(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("%w: docker engine unreachable: %w: %s", ErrUnavailable, err, out)
	}
	const password = "postgres"
	out, err := docker(ctx, "run", "-d", "--rm", "-P",
		"--label", "xyz.tanvo.test=analytics-pgtest",
		"--tmpfs", "/var/lib/postgresql:rw",
		"-e", "POSTGRES_PASSWORD="+password, "-e", "POSTGRES_INITDB_ARGS=--no-sync",
		Image, "postgres",
		"-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off",
		"-c", "max_connections=300")
	if err != nil {
		return fmt.Errorf("pgtest: docker run %s: %w: %s", Image, err, out)
	}
	s.container = strings.TrimSpace(out)
	out, err = docker(ctx, "port", s.container, "5432/tcp")
	if err != nil {
		return fmt.Errorf("pgtest: docker port: %w: %s", err, out)
	}
	addr, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	host, port, ok := strings.Cut(strings.TrimSpace(addr), ":")
	if !ok {
		return fmt.Errorf("pgtest: docker port: unexpected output %q", out)
	}
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	}
	s.adminDSN = fmt.Sprintf("postgres://postgres:%s@%s:%s/postgres?sslmode=disable", password, host, port)
	return nil
}

func docker(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// waitReady waits until a connection succeeds.
func (s *Server) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var last error
	for {
		conn, err := pgx.Connect(ctx, s.adminDSN)
		if err == nil {
			err = conn.Ping(ctx)
			_ = conn.Close(ctx)
			if err == nil {
				return nil
			}
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("pgtest: waiting for postgres: %w (last error: %w)", ctx.Err(), last)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Close removes the container, or drops the databases created on the
// server of [DSNEnv].
func (s *Server) Close(ctx context.Context) error {
	if s.container != "" {
		if out, err := docker(ctx, "rm", "-f", s.container); err != nil {
			return fmt.Errorf("pgtest: docker rm: %w: %s", err, out)
		}
		return nil
	}
	var errs []error
	for _, db := range slices.Backward(s.databases) {
		errs = append(errs, s.DropDatabase(ctx, db))
	}
	return errors.Join(errs...)
}

// Connect connects to database db (the server's default when empty) as
// the superuser.
func (s *Server) Connect(ctx context.Context, db string) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, s.DSN(db, "", ""))
	if err != nil {
		return nil, fmt.Errorf("pgtest: connect to %q: %w", db, err)
	}
	return conn, nil
}

// DSN returns the DSN of database db (the server's default when empty),
// authenticating as user with password when user is set and as the
// superuser otherwise.
func (s *Server) DSN(db, user, password string) string {
	if strings.HasPrefix(s.adminDSN, "postgres://") || strings.HasPrefix(s.adminDSN, "postgresql://") {
		u, err := url.Parse(s.adminDSN)
		if err != nil {
			return s.adminDSN
		}
		if db != "" {
			u.Path = "/" + db
		}
		if user != "" {
			u.User = url.UserPassword(user, password)
		}
		return u.String()
	}
	dsn := s.adminDSN
	if db != "" {
		dsn += " dbname=" + db
	}
	if user != "" {
		dsn += " user=" + user + " password=" + password
	}
	return dsn
}

// CreateDatabase creates a database, a clone of template unless it is
// empty, and returns its name. [Server.Close] drops the databases left.
func (s *Server) CreateDatabase(ctx context.Context, template string) (string, error) {
	name := RandomName("test_")
	conn, err := s.Connect(ctx, "")
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close(ctx) }()
	sql := "CREATE DATABASE " + name
	if template != "" {
		sql += " TEMPLATE " + template
	}
	if _, err := conn.Exec(ctx, sql); err != nil {
		return "", fmt.Errorf("pgtest: create database: %w", err)
	}
	s.databases = append(s.databases, name)
	return name, nil
}

// DropDatabase drops database name.
func (s *Server) DropDatabase(ctx context.Context, name string) error {
	conn, err := s.Connect(ctx, "")
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("pgtest: drop database %s: %w", name, err)
	}
	s.databases = slices.DeleteFunc(s.databases, func(d string) bool { return d == name })
	return nil
}

// RandomName returns prefix followed by random hex digits, a valid
// unquoted identifier when prefix is one.
func RandomName(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// CoreMigrationsDir returns the directory of the core store's versioned
// migrations, libs/store/migrations, searched from the working directory
// upwards.
func CoreMigrationsDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("pgtest: %w", err)
	}
	for {
		candidate := filepath.Join(dir, "libs", "store", "migrations")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("pgtest: libs/store/migrations not found above the working directory")
		}
		dir = parent
	}
}

// ApplyCoreMigrations applies the core store's migrations to the database
// of conn in lexical order: each file in one implicit transaction, or
// statement by statement outside a transaction when it declares
// "-- atlas:txmode none" (CREATE INDEX CONCURRENTLY).
func ApplyCoreMigrations(ctx context.Context, conn *pgx.Conn) error {
	dir, err := CoreMigrationsDir()
	if err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("pgtest: %w", err)
	}
	slices.Sort(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("pgtest: %w", err)
		}
		sql := string(b)
		if !strings.Contains(sql, "-- atlas:txmode none") {
			// Without arguments pgx uses the simple protocol, which runs
			// the statements of the file in one implicit transaction.
			if _, err := conn.Exec(ctx, sql); err != nil {
				return fmt.Errorf("pgtest: migration %s: %w", filepath.Base(f), err)
			}
			continue
		}
		for _, stmt := range Statements(sql) {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("pgtest: migration %s: %w", filepath.Base(f), err)
			}
		}
	}
	return nil
}

// Statements splits a migration into its statements: comment lines are
// dropped, and a statement ends with a line ending in a semicolon.
func Statements(sql string) []string {
	var out []string
	var cur strings.Builder
	for line := range strings.Lines(sql) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line)
		if strings.HasSuffix(trimmed, ";") {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
	}
	if rest := strings.TrimSpace(cur.String()); rest != "" {
		out = append(out, rest)
	}
	return out
}
