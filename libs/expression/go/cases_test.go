package expression_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"

	expression "github.com/tanvoxyz/tanvo/libs/expression/go"
)

// The cases of ../testdata/*.json, which the Rust tests run too; their format
// is described in ../tests/cases.rs.

type caseFile struct {
	Description string     `json:"description"`
	Event       *caseEvent `json:"event"`
	Cases       []testCase `json:"cases"`
}

type caseEvent struct {
	Code       string         `json:"code"`
	Timestamp  int64          `json:"timestamp"`
	Properties map[string]any `json:"properties"`
}

type testCase struct {
	Source      string     `json:"source"` // empty for Tanvo's own cases
	Name        string     `json:"name"`
	Note        string     `json:"note"`
	Expression  string     `json:"expression"`
	Event       *caseEvent `json:"event"`
	Valid       bool       `json:"valid"`
	SyntaxError *struct {
		Kind     string   `json:"kind"`
		Line     *int     `json:"line"`
		Column   *int     `json:"column"`
		Offset   *int     `json:"offset"`
		Message  *string  `json:"message"`
		Contains []string `json:"contains"`
	} `json:"syntax_error"`
	Number *string `json:"number"`
	String *string `json:"string"`
	Error  *struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *testCase) evaluates() bool { return c.Number != nil || c.String != nil || c.Error != nil }

// event converts a case's event: a JSON number is a json.Number, and
// {"number": "text"} a json.Number of that text.
func (e *caseEvent) event() expression.Event {
	props := make(map[string]any, len(e.Properties))
	for k, v := range e.Properties {
		if o, ok := v.(map[string]any); ok {
			v = json.Number(o["number"].(string))
		}
		props[k] = v
	}
	return expression.Event{Code: e.Code, Timestamp: time.Unix(e.Timestamp, 0), Properties: props}
}

type loadedCase struct {
	file string
	testCase
	event *caseEvent
}

func loadCases(t testing.TB) []loadedCase {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "testdata", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no testdata: %v", err)
	}
	var cases []loadedCase
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		dec.DisallowUnknownFields()
		var f caseFile
		if err := dec.Decode(&f); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, c := range f.Cases {
			ev := c.Event
			if ev == nil {
				ev = f.Event
			}
			cases = append(cases, loadedCase{file: filepath.Base(path), testCase: c, event: ev})
		}
	}
	return cases
}

func newEngine(t testing.TB, opts ...expression.Option) *expression.Engine {
	t.Helper()
	e, err := expression.NewEngine(t.Context(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return e
}

func checkResult(t *testing.T, c *testCase, v expression.Value, err error) {
	t.Helper()
	switch {
	case c.Number != nil:
		want := money.MustParse(*c.Number)
		if got, ok := v.Decimal(); err != nil || !ok || !got.Equal(want) {
			t.Errorf("got = %v (%v, %v), want = number %s", v, v.Kind(), err, *c.Number)
		}
	case c.String != nil:
		if got, ok := v.Text(); err != nil || !ok || got != *c.String {
			t.Errorf("got = %q (%v, %v), want = string %q", v, v.Kind(), err, *c.String)
		}
	case c.Error != nil:
		var eerr *expression.EvaluationError
		if !errors.As(err, &eerr) {
			t.Fatalf("got = %v, %v, want = error %s %q", v, err, c.Error.Kind, c.Error.Message)
		}
		if eerr.Kind.String() != c.Error.Kind || eerr.Message != c.Error.Message {
			t.Errorf("got = error %s %q, want = %s %q", eerr.Kind, eerr.Message, c.Error.Kind, c.Error.Message)
		}
	}
}

func TestCases(t *testing.T) {
	e := newEngine(t)
	for _, c := range loadCases(t) {
		t.Run(c.file+"/"+c.Name, func(t *testing.T) {
			p, err := e.Compile(t.Context(), c.Expression)
			if c.SyntaxError != nil {
				var serr *expression.SyntaxError
				if !errors.As(err, &serr) {
					t.Fatalf("got = %v, want = a syntax error (%s)", err, c.Source)
				}
				want := c.SyntaxError
				if serr.Kind.String() != want.Kind {
					t.Errorf("got = kind %s, want = %s", serr.Kind, want.Kind)
				}
				for _, f := range []struct {
					name string
					got  int
					want *int
				}{{"line", serr.Line, want.Line}, {"column", serr.Column, want.Column}, {"offset", serr.Offset, want.Offset}} {
					if f.want != nil && f.got != *f.want {
						t.Errorf("got = %s %d, want = %d", f.name, f.got, *f.want)
					}
				}
				if want.Message != nil && serr.Message != *want.Message {
					t.Errorf("got = message %q, want = %q", serr.Message, *want.Message)
				}
				for _, part := range want.Contains {
					if !strings.Contains(serr.Message, part) {
						t.Errorf("got = message %q, want = it to contain %q", serr.Message, part)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("got = %v, want = it to parse (%s)", err, c.Source)
			}
			if !c.evaluates() {
				if !c.Valid {
					t.Fatal("a case without an expectation")
				}
				return
			}
			v, err := p.Evaluate(t.Context(), c.event.event())
			checkResult(t, &c.testCase, v, err)
		})
	}
}

// TestCasesBatch evaluates the cases of each expression in one batch.
func TestCasesBatch(t *testing.T) {
	e := newEngine(t)
	byExpr := map[string][]loadedCase{}
	var order []string
	for _, c := range loadCases(t) {
		if !c.evaluates() {
			continue
		}
		if _, ok := byExpr[c.Expression]; !ok {
			order = append(order, c.Expression)
		}
		byExpr[c.Expression] = append(byExpr[c.Expression], c)
	}
	for _, src := range order {
		cases := byExpr[src]
		p, err := e.Compile(t.Context(), src)
		if err != nil {
			t.Fatal(err)
		}
		events := make([]expression.Event, len(cases))
		for i, c := range cases {
			events[i] = c.event.event()
		}
		results, err := p.EvaluateBatch(t.Context(), events)
		if err != nil {
			t.Fatal(err)
		}
		for i, c := range cases {
			t.Run(c.file+"/"+c.Name, func(t *testing.T) {
				checkResult(t, &c.testCase, results[i].Value, results[i].Err)
			})
		}
	}
}
