// Package apiclienttest is an httptest server replaying recorded provider
// answers for the adapter tests of providers/* (docs/testing.md §3).
package apiclienttest

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Reply is one recorded answer: a fixture under testdata/fixtures or an
// inline body.
type Reply struct {
	// Status is the HTTP status; 200 when zero.
	Status int
	// Fixture is a file name under testdata/fixtures.
	Fixture string
	// Body is an inline body, used when Fixture is empty.
	Body string
	// Header is set on the answer.
	Header map[string]string
}

// Fixture replies 200 with testdata/fixtures/<name>.
func Fixture(name string) Reply { return Reply{Status: http.StatusOK, Fixture: name} }

// JSON replies status with an inline body.
func JSON(status int, body string) Reply { return Reply{Status: status, Body: body} }

// Request is a request the server received.
type Request struct {
	// Method and Path are the request line.
	Method, Path string
	// Query is the parsed query.
	Query url.Values
	// Header is the request header.
	Header http.Header
	// Body is the raw body.
	Body []byte
}

// JSONBody decodes the body as JSON into a generic value.
func (r Request) JSONBody(t *testing.T) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("request body %s: %v", r.Body, err)
	}
	return v
}

// Server answers "METHOD /path" routes with their replies in order,
// repeating the last one; an unknown route fails the test.
type Server struct {
	// URL is the server's base URL.
	URL string

	t      *testing.T
	mu     sync.Mutex
	routes map[string][]Reply
	reqs   []Request
}

// New starts a server closed with the test.
func New(t *testing.T, routes map[string][]Reply) *Server {
	t.Helper()
	s := &Server{t: t, routes: maps.Clone(routes)}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body})
	seq, found := s.routes[key]
	var rep Reply
	if found && len(seq) > 0 {
		rep = seq[0]
		if len(seq) > 1 {
			s.routes[key] = seq[1:]
		}
	}
	s.mu.Unlock()
	if !found {
		s.t.Errorf("unexpected request %s", key)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	out := []byte(rep.Body)
	if rep.Fixture != "" {
		var err error
		if out, err = os.ReadFile(filepath.Join("testdata", "fixtures", rep.Fixture)); err != nil {
			s.t.Errorf("read fixture: %v", err)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	for k, v := range rep.Header {
		w.Header().Set(k, v)
	}
	status := rep.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

// Requests returns the requests to "METHOD /path", or every request when
// key is empty.
func (s *Server) Requests(key string) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Request
	for _, r := range s.reqs {
		if key == "" || r.Method+" "+r.Path == key {
			out = append(out, r)
		}
	}
	return out
}

// Only returns the single request to key.
func (s *Server) Only(key string) Request {
	s.t.Helper()
	rs := s.Requests(key)
	if len(rs) != 1 {
		s.t.Fatalf("requests to %s: got = %d, want = 1", key, len(rs))
	}
	return rs[0]
}

// EqualJSON reports whether got and want are the same JSON value.
func EqualJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("got %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("want %s: %v", want, err)
	}
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	return string(ga) == string(gb)
}
